package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
)

func TestRunCLIQuotaContinuesAndKeylessReset(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux supervisor required")
	}
	bin := buildStdioCapy(t)
	project := cliProject(t, "max_run_bytes=100000\nmax_retained_bytes=100000\n")
	env := stdioEnv(t, "synthetic-retention-cli-key")
	// Deliberately exceed retained quota while remaining below the raw-run
	// ceiling. The final filesystem effect proves capture loss does not kill it.
	out, diag, status := capturedCLI(t, bin, project, env, "execute", "--project-dir", project, "--json", "--code", "printf 'prefix\\n'; sleep 0.2; head -c 90000 /dev/zero; touch command-completed")
	require.Empty(t, diag)
	require.Equal(t, 74, status, string(out))
	var result runstore.Result
	require.NoError(t, json.Unmarshal(out, &result))
	require.Equal(t, "exited", result.State)
	require.Equal(t, 0, *result.ExitCode)
	require.Equal(t, "incomplete", result.Capture.State)
	require.Equal(t, "quota_exhausted", *result.Capture.Reason)
	require.True(t, result.Capture.TerminalPersisted)
	require.GreaterOrEqual(t, result.Capture.CapturedBytes, int64(len("prefix\n")))
	require.FileExists(t, filepath.Join(project, "command-completed"))
	out, diag, status = capturedCLI(t, bin, project, env, "runs", "read", "--project-dir", project, "--run-id", *result.RunID, "--raw", "--json", "--max-bytes", "7")
	require.Empty(t, diag)
	require.Zero(t, status, string(out))
	var prefix runstore.ReadPage
	require.NoError(t, json.Unmarshal(out, &prefix))
	require.Len(t, prefix.Fragments, 1)
	require.Equal(t, []byte("prefix\n"), prefix.Fragments[0].Data)
	// Reset must not even attempt the selected invalid key-file resolution.
	require.NoError(t, os.WriteFile(filepath.Join(project, ".capy.toml"), []byte("[store]\nkey_file='missing-private-key'\n[runs]\nmax_response_bytes=2048\n"), 0600))
	for _, extra := range [][]string{{}, {"--discard=false"}, {"--discard", "--run-id", *result.RunID}, {"--discard", "positional"}} {
		args := append([]string{"runs", "reset", "--project-dir", project}, extra...)
		_, _, code := capturedCLI(t, bin, project, env, args...)
		require.Equal(t, 2, code)
		require.FileExists(t, filepath.Join(project, ".capy", "runs", "runs.db"))
	}
	keyless := make([]string, 0, len(env))
	for _, value := range env {
		if !strings.HasPrefix(value, "CAPY_DB_KEY=") {
			keyless = append(keyless, value)
		}
	}
	out, diag, status = capturedCLI(t, bin, project, keyless, "runs", "reset", "--project-dir", project, "--discard")
	require.Equal(t, 0, status, "%s %s", out, diag)
	require.NoFileExists(t, filepath.Join(project, ".capy", "runs", "runs.db"))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".capy.toml"), []byte("[runs]\nmax_response_bytes=2048\n"), 0600))
	newEnv := append(keyless, "CAPY_DB_KEY=synthetic-new-retention-key")
	next := cliRun(t, bin, project, newEnv, "printf new-key")
	require.Nil(t, next.Error)
	require.Equal(t, "complete", next.Capture.State)
	require.NotEqual(t, result.RunID, next.RunID)
}

func TestRunCLIResetRefusesLiveMCP(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux supervisor required")
	}
	bin := buildStdioCapy(t)
	project := cliProject(t, "")
	env := stdioEnv(t, "synthetic-reset-live-owner")
	p := startStdioProcess(t, stdioCommand{executable: bin, args: []string{"serve", "--project-dir", project}, dir: project, env: env})
	p.initialize(t)
	r := p.capturedRun(t, map[string]any{"language": "shell", "code": "printf kept"})
	require.False(t, r.IsError)
	out, _, status := capturedCLI(t, bin, project, env, "runs", "reset", "--project-dir", project, "--discard")
	require.Equal(t, 1, status)
	require.Contains(t, string(out), "storage_busy")
	p.close(t)
	_, _, status = capturedCLI(t, bin, project, env, "runs", "reset", "--project-dir", project, "--discard")
	require.Zero(t, status)
}
