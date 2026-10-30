package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
)

// Release verification supplies binaries built from the recorded legacy tags.
// Keep this opt-in: an absent binary is an explicit evidence gap, and building
// an old checkout or fetching release artifacts is not an ordinary unit test.
func TestRecoveryRolloutLegacyBinaries(t *testing.T) {
	binaries := filepath.SplitList(os.Getenv("CAPY_RECOVERY_LEGACY_BINARIES"))
	if len(binaries) == 0 {
		t.Skip("release check requires CAPY_RECOVERY_LEGACY_BINARIES with schema-8/9 binaries")
	}
	capturedCleanup(t)
	candidate := buildStdioCapy(t)
	for _, legacy := range binaries {
		t.Run(filepath.Base(legacy), func(t *testing.T) {
			require.True(t, filepath.IsAbs(legacy), "pin the legacy binary with an absolute path")
			project, key := cliProject(t, ""), "synthetic-rollout-key"
			env := stdioEnv(t, key)
			// Establish that this binary, credential, owner and all three CLI
			// argument forms work before the candidate upgrades the same store.
			out, diagnostic, status := capturedCLI(t, legacy, project, env,
				"execute", "--json", "--project-dir", project, "--code", "printf legacy-control")
			require.Zero(t, status, "%s %s", out, diagnostic)
			require.Empty(t, diagnostic)
			var control struct {
				Version int     `json:"version"`
				RunID   *string `json:"run_id"`
			}
			require.NoError(t, json.Unmarshal(out, &control))
			require.NotNil(t, control.RunID)
			expectedCode := "schema_mismatch"
			if control.Version == 1 {
				expectedCode = "storage_unavailable"
			} else {
				require.Equal(t, 2, control.Version)
			}
			for _, action := range []string{"status", "read"} {
				out, diagnostic, status = capturedCLI(t, legacy, project, env,
					"runs", action, "--json", "--project-dir", project, "--run-id", *control.RunID)
				require.Zero(t, status, "%s %s", out, diagnostic)
				require.Empty(t, diagnostic)
			}
			sibling := cliRun(t, candidate, project, env, "printf 'rollout sibling'")
			require.NotNil(t, sibling.RunID)
			path, uncertain := recoveryCLIPending(t, project, key)
			plan := recoveryCLIPlan(t, candidate, project, env)
			out, _, status = capturedCLI(t, candidate, project, env, recoveryCLIApplyArgs(project, plan.Token)...)
			require.Zero(t, status, "%s", out)
			before, err := os.ReadFile(path)
			require.NoError(t, err)

			out, diagnostic, status = capturedCLI(t, legacy, project, env,
				"execute", "--json", "--project-dir", project, "--code", "touch forbidden-legacy")
			require.Equal(t, 1, status, "%s", out)
			require.Empty(t, diagnostic)
			var refused runstore.Result
			require.NoError(t, json.Unmarshal(out, &refused))
			require.Nil(t, refused.RunID)
			require.NotNil(t, refused.Error)
			// Schema 8 predates a dedicated mismatch code; both releases must
			// reach storage rejection, never pass through a usage/flag failure.
			require.Equal(t, expectedCode, refused.Error.Code)
			if control.Version == 1 {
				require.Equal(t, "run storage is unavailable; check the selected owner's .capy/runs path and permissions; selected path: "+path, refused.Error.Message)
			} else {
				require.Contains(t, refused.Error.Message, "schema 10")
			}
			t.Logf("%s execute refusal: %s", filepath.Base(legacy), refused.Error.Code)
			for _, action := range []string{"status", "read"} {
				out, diagnostic, status = capturedCLI(t, legacy, project, env,
					"runs", action, "--json", "--project-dir", project, "--run-id", uncertain)
				require.Equal(t, 1, status, "%s", out)
				require.Empty(t, diagnostic)
				var query runQueryResult
				require.NoError(t, json.Unmarshal(out, &query))
				require.NotNil(t, query.Error)
				require.Equal(t, refused.Error.Code, query.Error.Code)
				t.Logf("%s %s refusal: %s", filepath.Base(legacy), action, query.Error.Code)
			}
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, before, after, "old binaries must refuse without rewriting schema-10 history")
			// Legacy mode=ro probes can create empty WAL/SHM files. They predate
			// the inspection-only VFS; no WAL frames may be written on refusal.
			wal, err := os.Stat(path + "-wal")
			if !os.IsNotExist(err) {
				require.NoError(t, err)
				require.Zero(t, wal.Size())
			}
			require.NoFileExists(t, filepath.Join(project, "forbidden-legacy"))

			// The compatible binary still reads history and admits fresh work.
			out, _, status = capturedCLI(t, candidate, project, env,
				"runs", "read", "--json", "--project-dir", project, "--run-id", *sibling.RunID, "--raw")
			require.Zero(t, status, "%s", out)
			var page runstore.ReadPage
			require.NoError(t, json.Unmarshal(out, &page))
			require.Len(t, page.Fragments, 1)
			require.Equal(t, "rollout sibling", string(page.Fragments[0].Data))
			fresh := cliRun(t, candidate, project, env, "printf verification")
			require.NotNil(t, fresh.RunID)
			require.True(t, fresh.Capture.TerminalPersisted)
			require.Nil(t, fresh.Recovery)
		})
	}
}
