package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
)

type stdioBatchResponse struct {
	IsError    bool                 `json:"isError"`
	Structured runstore.BatchResult `json:"structuredContent"`
}

func (p *stdioProcess) capturedBatch(t *testing.T, args map[string]any) stdioBatchResponse {
	t.Helper()
	var r stdioBatchResponse
	require.NoError(t, p.request("tools/call", map[string]any{"name": "capy_batch_execute", "arguments": args}, &r))
	return r
}
func (p *stdioProcess) batchChildren(t *testing.T, id string) []runstore.Summary {
	t.Helper()
	entries := []runstore.Summary{}
	cursor := ""
	for {
		var r struct {
			IsError    bool              `json:"isError"`
			Structured runstore.ListPage `json:"structuredContent"`
		}
		require.NoError(t, p.request("tools/call", map[string]any{"name": "capy_runs", "arguments": map[string]any{"action": "list", "batch_id": id, "cursor": cursor, "limit": 1}}, &r))
		require.False(t, r.IsError)
		entries = append(entries, r.Structured.Entries...)
		if r.Structured.NextCursor == nil {
			break
		}
		cursor = *r.Structured.NextCursor
	}
	return entries
}
func (p *stdioProcess) awaitBatch(t *testing.T, id string) runstore.BatchResult {
	t.Helper()
	var r struct {
		IsError    bool `json:"isError"`
		Structured struct {
			Batch runstore.BatchResult `json:"batch"`
		} `json:"structuredContent"`
	}
	require.Eventually(t, func() bool {
		require.NoError(t, p.request("tools/call", map[string]any{"name": "capy_runs", "arguments": map[string]any{"action": "status", "batch_id": id}}, &r))
		require.False(t, r.IsError)
		return r.Structured.Batch.CompletedAt != nil
	}, 15*time.Second, 20*time.Millisecond)
	return r.Structured.Batch
}
func TestMCPSerialBatchCapture(t *testing.T) {
	bin := buildStdioCapy(t)
	launch := func(t *testing.T, project string) *stdioProcess {
		p := startStdioProcess(t, stdioCommand{executable: bin, args: []string{"serve", "--project-dir", project}, dir: project, env: stdioEnv(t, "synthetic-serial-batch-key")})
		p.initialize(t)
		return p
	}
	t.Run("yield and restart recovery", func(t *testing.T) {
		project := stdioProject(t)
		require.NoError(t, os.WriteFile(filepath.Join(project, ".capy.toml"), []byte("[runs]\nmax_response_bytes=2048\n"), 0600))
		p := launch(t, project)
		r := p.capturedBatch(t, map[string]any{"commands": []any{"printf early; printf x >> once; while [ ! -e release ]; do sleep .01; done; printf late", "printf stderr-tail >&2; printf x >> once; exit 7", "printf last; printf x >> once"}, "queries": []any{"stderr-tail"}, "wait_ms": 0})
		require.False(t, r.IsError)
		require.NotEmpty(t, r.Structured.BatchID)
		require.Nil(t, r.Structured.CompletedAt)
		encoded, err := json.Marshal(r)
		require.NoError(t, err)
		require.LessOrEqual(t, len(encoded), 2048)
		id := r.Structured.BatchID
		require.NotNil(t, r.Structured.SearchAction)
		require.NoError(t, p.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": p.nextID}}, time.Now().Add(stdioTimeout)))
		require.NoError(t, os.WriteFile(filepath.Join(project, "release"), nil, 0600))
		result := p.awaitBatch(t, id)
		require.Equal(t, "completed", result.State)
		require.Equal(t, 1, result.FailedCount)
		entries := p.batchChildren(t, id)
		require.Len(t, entries, 3)
		require.Equal(t, 7, *entries[1].ExitCode)
		require.Equal(t, []byte("earlylate"), p.rawRun(t, entries[0].RunID, "stdout"))
		require.Equal(t, []byte("stderr-tail"), p.rawRun(t, entries[1].RunID, "stderr"))
		p.close(t)
		p = launch(t, project)
		require.Equal(t, entries, p.batchChildren(t, id))
		p.close(t)
		once, err := os.ReadFile(filepath.Join(project, "once"))
		require.NoError(t, err)
		require.Equal(t, "xxx", string(once))
		require.NoFileExists(t, filepath.Join(project, "knowledge.db"))
		// CLI queries the identical owner-local manifest without starting an MCP owner.
		cmd := exec.Command(bin, "runs", "status", "--batch-id", id, "--project-dir", project, "--json")
		cmd.Env = stdioEnv(t, "synthetic-serial-batch-key")
		cmd.Dir = project
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, string(output))
		require.Contains(t, string(output), id)
	})
	t.Run("attached request cancellation", func(t *testing.T) {
		project := stdioProject(t)
		p := launch(t, project)
		requestID := p.nextID + 1
		done := make(chan error, 1)
		var r stdioBatchResponse
		go func() {
			done <- p.request("tools/call", map[string]any{"name": "capy_batch_execute", "arguments": map[string]any{"commands": []any{"touch started; sleep 20; touch forbidden", "touch sibling"}, "wait_ms": 30000}}, &r)
		}()
		require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(project, "started")); return err == nil }, 5*time.Second, 10*time.Millisecond)
		require.NoError(t, p.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": requestID}}, time.Now().Add(stdioTimeout)))
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(10 * time.Second):
			t.Fatal("batch did not cancel")
		}
		require.True(t, r.IsError)
		require.Equal(t, "cancelled", r.Structured.State)
		entries := p.batchChildren(t, r.Structured.BatchID)
		require.Equal(t, "cancelled", entries[1].State)
		require.Nil(t, entries[1].ExitCode)
		require.NoFileExists(t, filepath.Join(project, "forbidden"))
		require.NoFileExists(t, filepath.Join(project, "sibling"))
		p.close(t)
	})
	t.Run("owner shutdown", func(t *testing.T) {
		project := stdioProject(t)
		p := launch(t, project)
		r := p.capturedBatch(t, map[string]any{"commands": []any{"touch started; sleep 20; touch forbidden", "touch sibling"}, "wait_ms": 0})
		require.False(t, r.IsError)
		require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(project, "started")); return err == nil }, 5*time.Second, 10*time.Millisecond)
		p.close(t)
		p = launch(t, project)
		b := p.awaitBatch(t, r.Structured.BatchID)
		require.Equal(t, "cancelled", b.State)
		require.NoFileExists(t, filepath.Join(project, "sibling"))
		p.close(t)
	})
}
