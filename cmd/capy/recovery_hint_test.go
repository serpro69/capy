package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
)

func TestRecoveryHintCLIAdmissionAndCredential(t *testing.T) {
	bin := buildStdioCapy(t)
	project := cliProject(t, "")
	key := "synthetic-recovery-guidance-key"
	path, err := config.ResolveRunDBPath(project)
	require.NoError(t, err)
	s, err := runstore.Open(t.Context(), path, key, true)
	require.NoError(t, err)
	require.NoError(t, s.Quarantine(t.Context()))
	require.NoError(t, s.Close())
	for _, tc := range []struct{ name, key, code string }{{"quarantine", key, "cleanup_unverified"}, {"credential", "wrong-synthetic-key", "wrong_key"}} {
		t.Run(tc.name, func(t *testing.T) {
			for _, asJSON := range []bool{true, false} {
				args := []string{"execute", "--project-dir", project, "--code", "touch must-not-spawn"}
				if asJSON {
					args = append(args, "--json")
				}
				out, diag, status := capturedCLI(t, bin, project, stdioEnv(t, tc.key), args...)
				require.NotZero(t, status)
				require.Empty(t, diag)
				require.Contains(t, string(out), tc.code)
				require.Contains(t, string(out), "No command was admitted")
				require.Contains(t, string(out), "capy runs inspect")
				require.NotContains(t, string(out), "--discard")
				if asJSON {
					var result runstore.Result
					require.NoError(t, json.Unmarshal(out, &result))
					require.Nil(t, result.RunID)
					require.True(t, result.Error.RecoveryHint.NoAdmission)
				}
				require.NoFileExists(t, filepath.Join(project, "must-not-spawn"))
			}
		})
	}
}

func TestRecoveryHintCLICredentialResolutionAndQuery(t *testing.T) {
	bin := buildStdioCapy(t)
	project := cliProject(t, "")
	env := stdioEnv(t, "synthetic-guidance-key")
	// A selected empty literal file refuses rather than falling back to env.
	require.NoError(t, os.WriteFile(filepath.Join(project, "empty-key.txt"), nil, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".capy.toml"), []byte("[store]\npath='knowledge.db'\nkey_file='empty-key.txt'\n[runs]\nmax_response_bytes=2048\n"), 0600))
	out, diag, status := capturedCLI(t, bin, project, env, "execute", "--project-dir", project, "--code", "touch must-not-spawn")
	require.NotZero(t, status)
	require.Empty(t, diag)
	require.Contains(t, string(out), "credential_unavailable")
	require.Contains(t, string(out), "passphrase is empty")
	require.Contains(t, string(out), "store.key_file")
	require.NoFileExists(t, filepath.Join(project, "must-not-spawn"))
	// The query transport must preserve the same guidance when search cannot
	// acquire an indexing owner in a quarantined store.
	project = cliProject(t, "")
	path, err := config.ResolveRunDBPath(project)
	require.NoError(t, err)
	s, err := runstore.Open(t.Context(), path, "synthetic-guidance-key", true)
	require.NoError(t, err)
	require.NoError(t, s.Quarantine(t.Context()))
	require.NoError(t, s.Close())
	out, diag, status = capturedCLI(t, bin, project, env, "runs", "search", "--project-dir", project, "--run-id", "00000000-0000-0000-0000-000000000001", "--query", "prefix")
	require.NotZero(t, status)
	require.Empty(t, diag)
	require.Contains(t, string(out), "cleanup_unverified")
	require.Contains(t, string(out), "capy runs inspect")
}
