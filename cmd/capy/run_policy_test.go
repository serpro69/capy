package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
)

func TestRunPolicyOldMCPAndNewCLI(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux supervisor required")
	}
	bin := buildStdioCapy(t)
	project := cliProject(t, "ttl_hours=9\n")
	const key = "synthetic-policy-mcp-cli-key"
	env := stdioEnv(t, key)
	p := startStdioProcess(t, stdioCommand{executable: bin, args: []string{"serve", "--project-dir", project}, dir: project, env: env})
	p.initialize(t)
	first := p.capturedRun(t, map[string]any{"language": "shell", "code": "printf original"})
	require.False(t, first.IsError, "%+v", first)
	require.Equal(t, 9*time.Hour, first.Structured.ExpiresAt.Sub(*first.Structured.CompletedAt))
	writeConfig := func(body string) {
		require.NoError(t, os.WriteFile(filepath.Join(project, ".capy.toml"), []byte("[store]\npath='knowledge.db'\n[runs]\nmax_response_bytes=2048\n"+body), 0600))
	}
	// Owner-local response wait/capacity do not require a policy revision.
	writeConfig("ttl_hours=9\nwait_ms=0\nmax_active=1\nmax_pending=1\n")
	compatible := cliRun(t, bin, project, env, "printf compatible")
	require.NotNil(t, compatible.RunID)
	require.Nil(t, compatible.Error)
	writeConfig("ttl_hours=1\nmax_run_bytes=1048576\nmax_retained_bytes=2097152\n")
	rejected := cliRun(t, bin, project, env, "touch must-not-spawn")
	require.Nil(t, rejected.RunID)
	require.Equal(t, "not_started", rejected.State)
	require.NotNil(t, rejected.Error)
	require.Equal(t, "policy_mismatch", rejected.Error.Code)
	require.NoFileExists(t, filepath.Join(project, "must-not-spawn"))
	// The old MCP continues using its attached revision until it is stopped.
	stillOld := p.capturedRun(t, map[string]any{"language": "shell", "code": "printf still-old"})
	require.False(t, stillOld.IsError, "%+v", stillOld)
	require.Equal(t, 9*time.Hour, stillOld.Structured.ExpiresAt.Sub(*stillOld.Structured.CompletedAt))
	p.close(t)
	updated := cliRun(t, bin, project, env, "printf updated")
	require.Nil(t, updated.Error)
	require.NotNil(t, updated.RunID)
	require.Equal(t, time.Hour, updated.ExpiresAt.Sub(*updated.CompletedAt))
	st, err := runstore.Open(t.Context(), filepath.Join(project, ".capy", "runs", "runs.db"), key, false)
	require.NoError(t, err)
	defer st.Close()
	require.EqualValues(t, 2, st.Policy().Revision)
	require.EqualValues(t, 2097152, st.Policy().MaxRetainedBytes)
	original, err := st.Load(t.Context(), *first.Structured.RunID)
	require.NoError(t, err)
	require.Equal(t, first.Structured.ExpiresAt, original.Result.ExpiresAt)
	require.EqualValues(t, 1, original.Program.PolicyRevision)
	latest, err := st.Load(t.Context(), *updated.RunID)
	require.NoError(t, err)
	require.EqualValues(t, 2, latest.Program.PolicyRevision)
}
