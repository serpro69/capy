package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
)

type stdioRunQuery struct {
	IsError    bool `json:"isError"`
	Structured struct {
		Run             runstore.Result `json:"run"`
		CancelRequested bool            `json:"cancel_requested"`
		Error           *runstore.Error `json:"error"`
	} `json:"structuredContent"`
}

func (p *stdioProcess) runQuery(t *testing.T, action, id string) stdioRunQuery {
	t.Helper()
	var r stdioRunQuery
	require.NoError(t, p.request("tools/call", map[string]any{"name": "capy_runs", "arguments": map[string]any{"action": action, "run_id": id}}, &r))
	return r
}

func (p *stdioProcess) awaitRun(t *testing.T, id string) runstore.Result {
	t.Helper()
	var result runstore.Result
	require.Eventually(t, func() bool {
		r := p.runQuery(t, "status", id)
		require.False(t, r.IsError)
		result = r.Structured.Run
		return result.CompletedAt != nil
	}, 10*time.Second, 20*time.Millisecond)
	return result
}

func TestMCPRunHandoffTransport(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("captured supervision requires Linux")
	}
	bin := buildStdioCapy(t)
	launch := func(t *testing.T, project string) *stdioProcess {
		p := startStdioProcess(t, stdioCommand{executable: bin, args: []string{"serve", "--project-dir", project}, dir: project, env: stdioEnv(t, "synthetic-handoff-key")})
		p.initialize(t)
		return p
	}
	t.Run("handler cleanup and late request cancellation", func(t *testing.T) {
		project := stdioProject(t)
		p := launch(t, project)
		r := p.capturedRun(t, map[string]any{"language": "shell", "code": "printf early; printf x >> once; while [ ! -e release ]; do sleep .01; done; printf late; printf stderr-tail >&2", "wait_ms": 0})
		require.False(t, r.IsError)
		require.NotNil(t, r.Structured.RunID)
		require.Nil(t, r.Structured.CompletedAt)
		// The real mcp-go handler already cancelled its context before this
		// response reached the client. A late notification also cannot own it.
		require.NoError(t, p.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": p.nextID}}, time.Now().Add(stdioTimeout)))
		require.NoError(t, os.WriteFile(filepath.Join(project, "release"), nil, 0600))
		terminal := p.awaitRun(t, *r.Structured.RunID)
		require.Equal(t, "exited", terminal.State)
		require.Equal(t, "complete", terminal.Capture.State)
		require.Equal(t, []byte("earlylate"), p.rawRun(t, *r.Structured.RunID, "stdout"))
		require.Equal(t, []byte("stderr-tail"), p.rawRun(t, *r.Structured.RunID, "stderr"))
		count, err := os.ReadFile(filepath.Join(project, "once"))
		require.NoError(t, err)
		require.Equal(t, "x", string(count))
		p.close(t)
	})
	t.Run("explicit request cancellation while attached", func(t *testing.T) {
		project := stdioProject(t)
		p := launch(t, project)
		requestID := p.nextID + 1
		done := make(chan error, 1)
		var response stdioRunResponse
		go func() {
			done <- p.request("tools/call", map[string]any{"name": "capy_execute", "arguments": map[string]any{"language": "shell", "code": "touch started; sleep 20; touch must-not-run", "wait_ms": 30000}}, &response)
		}()
		require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(project, "started")); return err == nil }, 5*time.Second, time.Millisecond)
		require.NoError(t, p.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": requestID}}, time.Now().Add(stdioTimeout)))
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(10 * time.Second):
			t.Fatal("attached cancellation did not finish")
		}
		require.True(t, response.IsError)
		require.Equal(t, "cancelled", response.Structured.State)
		require.NoFileExists(t, filepath.Join(project, "must-not-run"))
		p.close(t)
	})
	t.Run("background capture and explicit own cancel", func(t *testing.T) {
		project := stdioProject(t)
		p := launch(t, project)
		r := p.capturedRun(t, map[string]any{"language": "shell", "code": "printf start; sleep 1; printf late; sleep 20", "background": true, "timeout": 1})
		require.False(t, r.IsError)
		require.NotNil(t, r.Structured.RunID)
		require.Eventually(t, func() bool {
			return p.runQuery(t, "status", *r.Structured.RunID).Structured.Run.Capture.CapturedBytes >= 9
		}, 5*time.Second, 20*time.Millisecond)
		other := launch(t, project)
		foreign := other.runQuery(t, "cancel", *r.Structured.RunID)
		require.True(t, foreign.IsError)
		require.Equal(t, "different_owner", foreign.Structured.Error.Code)
		other.close(t)
		cancelled := p.runQuery(t, "cancel", *r.Structured.RunID)
		require.False(t, cancelled.IsError)
		require.True(t, cancelled.Structured.CancelRequested)
		require.Equal(t, "cancelled", p.awaitRun(t, *r.Structured.RunID).State)
		require.Equal(t, []byte("startlate"), p.rawRun(t, *r.Structured.RunID, "stdout"))
		require.False(t, p.runQuery(t, "cancel", *r.Structured.RunID).Structured.CancelRequested)
		p.close(t)
	})
	t.Run("response write failure cancels owner", func(t *testing.T) {
		for _, phase := range []string{"undelivered admission", "running command"} {
			t.Run(phase, func(t *testing.T) {
				project := stdioProject(t)
				p := launch(t, project)
				args := map[string]any{
					"language": "shell", "code": "touch started; sleep 60; touch must-not-run", "wait_ms": 0,
				}
				method := "tools/call"
				params := map[string]any{"name": "capy_execute", "arguments": args}
				var runID string
				if phase == "running command" {
					r := p.capturedRun(t, args)
					require.False(t, r.IsError)
					require.NotNil(t, r.Structured.RunID)
					require.Nil(t, r.Structured.CompletedAt)
					runID = *r.Structured.RunID
					require.Eventually(t, func() bool {
						_, err := os.Stat(filepath.Join(project, "started"))
						return err == nil
					}, 10*time.Second, 10*time.Millisecond)
					// The run has handed off and started. Fail a later response
					// write without relying on how long startup took.
					method, params = "tools/list", map[string]any{}
				}
				// Keep input open so only a response-write failure stops the
				// owner. An undelivered admission may be cancelled before spawn;
				// the running-command case separately requires actual startup.
				require.NoError(t, p.stdout.Close())
				require.NoError(t, p.write(map[string]any{
					"jsonrpc": "2.0", "id": p.nextID + 1, "method": method, "params": params,
				}, time.Now().Add(stdioTimeout)))
				select {
				case <-p.waited:
				case <-time.After(10 * time.Second):
					t.Fatal("owner did not stop after response write failure")
				}
				require.Equal(t, 1, p.cmd.ProcessState.ExitCode(), p.diagnostics())
				require.Contains(t, p.diagnostics(), "broken pipe")
				require.NoFileExists(t, filepath.Join(project, "must-not-run"))
				reopened := launch(t, project)
				var listed struct {
					IsError    bool              `json:"isError"`
					Structured runstore.ListPage `json:"structuredContent"`
				}
				require.NoError(t, reopened.request("tools/call", map[string]any{"name": "capy_runs", "arguments": map[string]any{"action": "list"}}, &listed))
				require.False(t, listed.IsError)
				require.Len(t, listed.Structured.Entries, 1)
				status := reopened.runQuery(t, "status", listed.Structured.Entries[0].RunID)
				require.False(t, status.IsError)
				terminal := status.Structured.Run
				require.Equal(t, "cancelled", terminal.State)
				require.True(t, terminal.Capture.TerminalPersisted)
				if phase == "running command" {
					require.Equal(t, runID, listed.Structured.Entries[0].RunID)
					require.NotNil(t, terminal.StartedAt)
					require.FileExists(t, filepath.Join(project, "started"))
				}
				reopened.close(t)
			})
		}
	})
	t.Run("configured deadline and explicit override", func(t *testing.T) {
		project := stdioProject(t)
		// Include cold encrypted-store and supervisor startup in the budget,
		// then keep the command alive long enough to exercise a running timeout.
		require.NoError(t, os.WriteFile(filepath.Join(project, ".capy.toml"), []byte("[executor]\ntimeout=5\n[runs]\nwait_ms=0\n"), 0600))
		p := launch(t, project)
		deadline := p.capturedRun(t, map[string]any{"language": "shell", "code": "sleep 30"})
		require.False(t, deadline.IsError)
		require.Nil(t, deadline.Structured.CompletedAt)
		terminal := p.awaitRun(t, *deadline.Structured.RunID)
		require.NotNil(t, terminal.StartedAt, "the fixture must start before its execution deadline")
		require.Equal(t, "timed_out", terminal.State)
		override := p.capturedRun(t, map[string]any{"language": "shell", "code": "sleep 5.2; printf override-tail", "timeout": 10000, "wait_ms": 30000})
		require.False(t, override.IsError)
		require.Equal(t, "exited", override.Structured.State)
		require.Equal(t, []byte("override-tail"), p.rawRun(t, *override.Structured.RunID, "stdout"))
		p.close(t)
	})
	t.Run("recovery workers remain available at maximum capacity", func(t *testing.T) {
		project := stdioProject(t)
		require.NoError(t, os.WriteFile(filepath.Join(project, ".capy.toml"), []byte("[runs]\nmax_active=8\n"), 0600))
		p := launch(t, project)
		executions := make(map[int]bool)
		for i := range 8 {
			p.nextID++
			executions[p.nextID] = true
			require.NoError(t, p.write(map[string]any{"jsonrpc": "2.0", "id": p.nextID, "method": "tools/call", "params": map[string]any{
				"name": "capy_execute", "arguments": map[string]any{"language": "shell", "code": fmt.Sprintf("touch started-%d; sleep 60", i), "background": true, "timeout": 60000},
			}}, time.Now().Add(stdioTimeout)))
		}
		require.Eventually(t, func() bool {
			for i := range 8 {
				if _, err := os.Stat(filepath.Join(project, fmt.Sprintf("started-%d", i))); err != nil {
					return false
				}
			}
			return true
		}, 10*time.Second, 10*time.Millisecond)
		// All execution handlers are still attached. Recovery must be served by
		// transport headroom, not wait for one of those calls to yield.
		p.timeout = 5 * time.Second
		var listed struct {
			IsError    bool              `json:"isError"`
			Structured runstore.ListPage `json:"structuredContent"`
		}
		require.NoError(t, p.request("tools/call", map[string]any{"name": "capy_runs", "arguments": map[string]any{"action": "list"}}, &listed))
		require.False(t, listed.IsError)
		require.Len(t, listed.Structured.Entries, 8)
		id := listed.Structured.Entries[0].RunID
		require.False(t, p.runQuery(t, "status", id).IsError)
		var read struct {
			IsError bool `json:"isError"`
		}
		require.NoError(t, p.request("tools/call", map[string]any{"name": "capy_runs", "arguments": map[string]any{"action": "read", "run_id": id}}, &read))
		require.False(t, read.IsError)
		blocked := p.capturedRun(t, map[string]any{"language": "shell", "code": "touch must-not-run"})
		require.True(t, blocked.IsError)
		require.Equal(t, "capacity_exhausted", blocked.Structured.Error.Code)
		require.NoFileExists(t, filepath.Join(project, "must-not-run"))
		cancels := make(map[int]bool)
		for _, run := range listed.Structured.Entries {
			p.nextID++
			cancels[p.nextID] = true
			require.NoError(t, p.write(map[string]any{"jsonrpc": "2.0", "id": p.nextID, "method": "tools/call", "params": map[string]any{
				"name": "capy_runs", "arguments": map[string]any{"action": "cancel", "run_id": run.RunID},
			}}, time.Now().Add(stdioTimeout)))
		}
		require.NoError(t, p.stdout.SetReadDeadline(time.Now().Add(10*time.Second)))
		for len(executions)+len(cancels) > 0 {
			require.True(t, p.scanner.Scan(), "cancellation stalled: %v", p.scanner.Err())
			var response struct {
				ID     int             `json:"id"`
				Method string          `json:"method"`
				Result json.RawMessage `json:"result"`
			}
			require.NoError(t, json.Unmarshal(p.scanner.Bytes(), &response))
			if response.Method != "" {
				continue
			}
			if executions[response.ID] {
				var execution stdioRunResponse
				require.NoError(t, json.Unmarshal(response.Result, &execution))
				require.Equal(t, "cancelled", execution.Structured.State)
				delete(executions, response.ID)
			} else {
				require.True(t, cancels[response.ID], "unexpected response id %d", response.ID)
				var cancellation stdioRunQuery
				require.NoError(t, json.Unmarshal(response.Result, &cancellation))
				require.False(t, cancellation.IsError)
				require.True(t, cancellation.Structured.CancelRequested)
				delete(cancels, response.ID)
			}
		}
		p.close(t)
	})
	t.Run("default deadline retains output beyond thirty seconds", func(t *testing.T) {
		project := stdioProject(t)
		p := launch(t, project)
		r := p.capturedRun(t, map[string]any{"language": "shell", "code": "printf early; sleep 31; printf beyond-old-default", "wait_ms": 0})
		require.False(t, r.IsError)
		var terminal runstore.Result
		require.Eventually(t, func() bool {
			terminal = p.runQuery(t, "status", *r.Structured.RunID).Structured.Run
			return terminal.CompletedAt != nil
		}, 40*time.Second, 250*time.Millisecond)
		require.Equal(t, "exited", terminal.State)
		require.Equal(t, 0, *terminal.ExitCode)
		require.Equal(t, "complete", terminal.Capture.State)
		require.Equal(t, []byte("earlybeyond-old-default"), p.rawRun(t, *r.Structured.RunID, "stdout"))
		p.close(t)
	})
}
