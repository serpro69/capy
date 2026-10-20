package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMCPExecuteFileCapturedLifetime(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("captured supervision requires Linux")
	}
	bin := buildStdioCapy(t)
	launch := func(t *testing.T, project string) *stdioProcess {
		p := startStdioProcess(t, stdioCommand{executable: bin, args: []string{"serve", "--project-dir", project}, dir: project, env: stdioEnv(t, "synthetic-file-run-key")})
		p.initialize(t)
		return p
	}
	call := func(t *testing.T, p *stdioProcess, args map[string]any) stdioRunResponse {
		t.Helper()
		args["path"], args["language"] = "input.txt", "shell"
		var result stdioRunResponse
		require.NoError(t, p.request("tools/call", map[string]any{"name": "capy_execute_file", "arguments": args}, &result))
		encoded, err := json.Marshal(result)
		require.NoError(t, err)
		require.LessOrEqual(t, len(encoded), 8192)
		return result
	}
	fixture := func(t *testing.T) string {
		t.Helper()
		project := stdioProject(t)
		require.NoError(t, os.WriteFile(filepath.Join(project, "input.txt"), []byte("file-content"), 0600))
		return project
	}
	t.Run("yield late output and restart recovery", func(t *testing.T) {
		project := fixture(t)
		want := strings.Repeat("file diagnostic\n", 1000) + "\x00\xfftail\n"
		require.NoError(t, os.WriteFile(filepath.Join(project, "input.txt"), []byte(strings.TrimSuffix(want, "\x00\xfftail\n")), 0600))
		p := launch(t, project)
		r := call(t, p, map[string]any{"code": `printf x >> once; touch started; while [ ! -e release ]; do sleep .01; done; cat "$FILE_CONTENT_PATH"; printf '\000\377tail\n'; printf late-stderr >&2`, "wait_ms": 0})
		require.False(t, r.IsError)
		require.NotNil(t, r.Structured.RunID)
		require.Nil(t, r.Structured.CompletedAt)
		id := *r.Structured.RunID
		// Normal mcp-go request cleanup already happened. A late cancellation
		// notification must also leave the handed-off run owned by the service.
		require.NoError(t, p.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": p.nextID}}, time.Now().Add(stdioTimeout)))
		require.NoError(t, os.WriteFile(filepath.Join(project, "release"), nil, 0600))
		terminal := p.awaitRun(t, id)
		require.Equal(t, "exited", terminal.State)
		require.Equal(t, 0, *terminal.ExitCode)
		require.Equal(t, "complete", terminal.Capture.State)
		require.Equal(t, []byte(want), p.rawRun(t, id, "stdout"))
		require.Equal(t, []byte("late-stderr"), p.rawRun(t, id, "stderr"))
		p.close(t)
		p = launch(t, project)
		require.Equal(t, "exited", p.runQuery(t, "status", id).Structured.Run.State)
		require.Equal(t, []byte(want), p.rawRun(t, id, "stdout"))
		p.close(t)
		count, err := os.ReadFile(filepath.Join(project, "once"))
		require.NoError(t, err)
		require.Equal(t, "x", string(count))
		require.NoFileExists(t, filepath.Join(project, "knowledge.db"))
	})
	t.Run("configured deadline and explicit override", func(t *testing.T) {
		project := fixture(t)
		// Include cold encrypted-store and supervisor startup in the budget,
		// then keep the command alive long enough to exercise a running timeout.
		require.NoError(t, os.WriteFile(filepath.Join(project, ".capy.toml"), []byte("[executor]\ntimeout=5\n[runs]\nwait_ms=0\n"), 0600))
		p := launch(t, project)
		r := call(t, p, map[string]any{"code": `printf '%s' "$FILE_CONTENT"; sleep 30; touch must-not-run`})
		require.False(t, r.IsError)
		require.NotNil(t, r.Structured.RunID)
		require.Nil(t, r.Structured.CompletedAt)
		terminal := p.awaitRun(t, *r.Structured.RunID)
		require.NotNil(t, terminal.StartedAt, "the fixture must start before its execution deadline")
		require.Equal(t, "timed_out", terminal.State)
		require.Equal(t, "complete", terminal.Capture.State)
		require.Equal(t, []byte("file-content"), p.rawRun(t, *r.Structured.RunID, "stdout"))
		require.NoFileExists(t, filepath.Join(project, "must-not-run"))
		r = call(t, p, map[string]any{"code": `sleep 5.2; printf '%s' "$FILE_CONTENT"`, "timeout": 10000, "wait_ms": 30000})
		require.False(t, r.IsError)
		require.Equal(t, "exited", r.Structured.State)
		require.Equal(t, []byte("file-content"), p.rawRun(t, *r.Structured.RunID, "stdout"))
		p.close(t)
	})
	t.Run("attached request cancellation", func(t *testing.T) {
		project := fixture(t)
		p := launch(t, project)
		requestID := p.nextID + 1
		done := make(chan error, 1)
		var r stdioRunResponse
		go func() {
			done <- p.request("tools/call", map[string]any{"name": "capy_execute_file", "arguments": map[string]any{
				"path": "input.txt", "language": "shell", "code": `printf '%s' "$FILE_CONTENT"; touch started; sleep 20; touch must-not-run`, "wait_ms": 30000,
			}}, &r)
		}()
		require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(project, "started")); return err == nil }, 5*time.Second, 10*time.Millisecond)
		require.NoError(t, p.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": requestID}}, time.Now().Add(stdioTimeout)))
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(10 * time.Second):
			t.Fatal("attached file cancellation did not finish")
		}
		require.True(t, r.IsError)
		require.Equal(t, "cancelled", r.Structured.State)
		require.Equal(t, "complete", r.Structured.Capture.State)
		require.NoFileExists(t, filepath.Join(project, "must-not-run"))
		p.close(t)
	})
	t.Run("shared capacity and explicit cancellation", func(t *testing.T) {
		project := fixture(t)
		require.NoError(t, os.WriteFile(filepath.Join(project, ".capy.toml"), []byte("[runs]\nmax_active=1\n"), 0600))
		p := launch(t, project)
		r := call(t, p, map[string]any{"code": "touch started; sleep 20; touch must-not-run", "wait_ms": 0})
		require.False(t, r.IsError)
		require.NotNil(t, r.Structured.RunID)
		require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(project, "started")); return err == nil }, 5*time.Second, 10*time.Millisecond)
		blocked := p.capturedRun(t, map[string]any{"language": "shell", "code": "touch capacity-bypass"})
		require.True(t, blocked.IsError)
		require.Equal(t, "capacity_exhausted", blocked.Structured.Error.Code)
		require.Nil(t, blocked.Structured.RunID)
		require.NoFileExists(t, filepath.Join(project, "capacity-bypass"))
		require.False(t, p.runQuery(t, "cancel", *r.Structured.RunID).IsError)
		require.Equal(t, "cancelled", p.awaitRun(t, *r.Structured.RunID).State)
		require.NoFileExists(t, filepath.Join(project, "must-not-run"))
		p.close(t)
	})
	t.Run("owner shutdown cancels yielded run", func(t *testing.T) {
		project := fixture(t)
		p := launch(t, project)
		r := call(t, p, map[string]any{"code": "touch started; sleep 20; touch must-not-run", "wait_ms": 0})
		require.False(t, r.IsError)
		require.NotNil(t, r.Structured.RunID)
		require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(project, "started")); return err == nil }, 5*time.Second, 10*time.Millisecond)
		p.close(t)
		p = launch(t, project)
		terminal := p.runQuery(t, "status", *r.Structured.RunID).Structured.Run
		require.Equal(t, "cancelled", terminal.State)
		require.True(t, terminal.Capture.TerminalPersisted)
		require.NoFileExists(t, filepath.Join(project, "must-not-run"))
		p.close(t)
	})
}
