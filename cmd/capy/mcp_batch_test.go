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

func TestMCPParallelBatchControls(t *testing.T) {
	bin := buildStdioCapy(t)
	project := stdioProject(t)
	env := stdioEnv(t, "synthetic-parallel-batch-key-123456789")
	launch := func() *stdioProcess {
		p := startStdioProcess(t, stdioCommand{executable: bin, args: []string{"serve", "--project-dir", project}, dir: project, env: env})
		p.initialize(t)
		return p
	}
	p := launch()
	for _, args := range []map[string]any{{"queries": nil}, {"queries": `["valid",null]`}, {"query_scope": "vault"}, {"concurrency": 1.5}} {
		args["commands"] = []any{"touch invalid-spawn"}
		bad := p.capturedBatch(t, args)
		require.True(t, bad.IsError)
	}
	require.NoFileExists(t, filepath.Join(project, "invalid-spawn"))
	commands := []any{"touch first; while [ ! -e release ]; do sleep .01; done; printf sharedneedle", "touch second; while [ ! -e release ]; do sleep .01; done; printf late >&2; exit 7", "printf sharedneedle"}
	r := p.capturedBatch(t, map[string]any{"commands": commands, "concurrency": 2, "wait_ms": 0, "queries": []any{"sharedneedle"}, "query_scope": "global"})
	require.False(t, r.IsError)
	id := r.Structured.BatchID
	require.NotEmpty(t, id)
	require.Equal(t, 2, r.Structured.EffectiveConcurrency)
	require.Equal(t, "global", r.Structured.SearchAction.QueryScope)
	for _, name := range []string{"first", "second"} {
		require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(project, name)); return err == nil }, 10*time.Second, 10*time.Millisecond)
	}
	require.NoError(t, os.WriteFile(filepath.Join(project, "release"), nil, 0600))
	final := p.awaitBatch(t, id)
	require.Equal(t, 1, final.FailedCount)
	children := p.batchChildren(t, id)
	require.Len(t, children, 3)
	require.Equal(t, []byte("late"), p.rawRun(t, children[1].RunID, "stderr"))
	p.tool(t, "capy_index", map[string]any{"source": "earlier durable", "content": "# shared\n\nsharedneedle reference"})
	var raw json.RawMessage
	require.NoError(t, p.request("tools/call", map[string]any{"name": "capy_runs", "arguments": map[string]any{"action": "search", "batch_id": id, "query_scope": "global"}}, &raw))
	require.LessOrEqual(t, len(raw), 8192)
	require.Contains(t, string(raw), "earlier durable")
	require.Contains(t, string(raw), "knowledge_results")
	require.NotContains(t, string(raw), "batch:cmd")
	p.close(t)
	p = launch()
	require.Equal(t, children, p.batchChildren(t, id))
	p.close(t)
	cmd := exec.Command(bin, "runs", "search", "--batch-id", id, "--query-scope", "global", "--project-dir", project, "--json")
	cmd.Env = env
	cmd.Dir = project
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	require.LessOrEqual(t, len(output), 8192)
	var page runstore.SearchPage
	require.NoError(t, json.Unmarshal(output, &page), string(output))
	require.NotEmpty(t, page.Results[0].Hits)
	require.Equal(t, "earlier durable", page.Knowledge[0].Hits[0].Source)
	cmd = exec.Command(bin, "runs", "search", "--batch-id", id, "--query-scope", "global", "--project-dir", project)
	cmd.Env, cmd.Dir = env, project
	output, err = cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	require.Contains(t, string(output), "knowledge source earlier durable")
	require.Contains(t, string(output), children[0].RunID)
}
