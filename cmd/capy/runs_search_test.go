package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
)

func TestRunCLISearchAndIntent(t *testing.T) {
	bin := buildStdioCapy(t)
	project := cliProject(t, "")
	env := stdioEnv(t, "synthetic-search-key")
	run := cliRun(t, bin, project, env, "printf 'early passing\\n'; printf 'stderr-marker failure\\n' >&2; exit 7", "--intent", "stderr-marker")
	require.NotNil(t, run.RunID)
	require.Equal(t, "ready", run.Index.State)
	require.Contains(t, run.Preview, "stderr-marker")
	for _, jsonOutput := range []bool{false, true} {
		args := []string{"runs", "search", "--project-dir", project, "--run-id", *run.RunID, "--query", "stderr-marker", "--limit", "20"}
		if jsonOutput {
			args = append(args, "--json")
		}
		out, diag, status := capturedCLI(t, bin, project, env, args...)
		require.Zero(t, status, string(out))
		require.Empty(t, diag)
		if jsonOutput {
			var page runstore.SearchPage
			require.NoError(t, json.Unmarshal(out, &page))
			require.NotEmpty(t, page.Results[0].Hits)
			require.Equal(t, "stderr", page.Results[0].Hits[0].Stream)
		} else {
			require.Contains(t, string(out), "stderr-marker")
			require.Contains(t, string(out), "read_cursor:")
		}
	}
	for _, extra := range [][]string{{"--query", ""}, {"--query", strings.Repeat("x", 513)}, {"--query", "marker", "--limit", "0"}, {"--query", "marker", "--query-scope", "global"}, {"--raw"}} {
		args := append([]string{"runs", "search", "--json", "--project-dir", project, "--run-id", *run.RunID}, extra...)
		out, diag, status := capturedCLI(t, bin, project, env, args...)
		require.Equal(t, 2, status, string(out))
		require.Empty(t, diag)
	}
	// A fresh MCP owner reads the same CLI-built index after CLI lease release.
	p := startStdioProcess(t, stdioCommand{executable: bin, args: []string{"serve", "--project-dir", project}, dir: project, env: env})
	p.initialize(t)
	var response struct {
		IsError    bool                `json:"isError"`
		Structured runstore.SearchPage `json:"structuredContent"`
	}
	require.NoError(t, p.request("tools/call", map[string]any{"name": "capy_runs", "arguments": map[string]any{"action": "search", "run_id": *run.RunID, "queries": []string{"stderr-marker"}}}, &response))
	require.False(t, response.IsError)
	require.NotEmpty(t, response.Structured.Results[0].Hits)
	p.close(t)
	quotaProject := cliProject(t, "max_run_bytes=1024\nmax_retained_bytes=71680\n")
	out, diag, status := capturedCLI(t, bin, quotaProject, env, "execute", "--project-dir", quotaProject, "--code", "printf marker", "--intent", "marker")
	require.Zero(t, status, string(out))
	require.Empty(t, diag)
	require.Contains(t, string(out), "Index: quota_blocked")
	require.Contains(t, string(out), "index_quota_blocked")
}
