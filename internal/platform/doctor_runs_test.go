package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/execution"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/serpro69/capy/internal/sqliteutil"
	"github.com/stretchr/testify/require"
)

func TestRunCapabilityDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, platform, detail string
		err                    error
		status                 CheckStatus
	}{
		{"linux", "linux", "linux_descendants_v1", nil, Pass},
		{"darwin", "darwin", "observed does not prove host quiescence", nil, Pass},
		{"unsupported build", "darwin", "unsupported host/build", errors.New("private-backend-error"), Fail},
		{"unknown platform", "other", "unsupported execution platform", nil, Fail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check := checkRunCapability(tc.platform, tc.err)
			require.Equal(t, tc.status, check.Status)
			require.Contains(t, check.Detail, tc.detail)
			require.NotContains(t, check.Detail, "private-backend-error")
		})
	}
}

func TestRunCapabilityDoesNotInitializeStorage(t *testing.T) {
	project := t.TempDir()
	checks := CheckRuns(t.Context(), project, "", config.DefaultConfig().Runs)
	require.NoDirExists(t, filepath.Join(project, ".capy"))
	var capability *CheckResult
	for i := range checks {
		if checks[i].Name == "Run execution" {
			capability = &checks[i]
		}
	}
	require.NotNil(t, capability)
	require.Contains(t, capability.Detail, "no execution probe")
	if execution.Supported() == nil {
		require.Equal(t, Pass, capability.Status)
	} else {
		require.Equal(t, Fail, capability.Status)
	}
	entries, err := os.ReadDir(project)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestCheckRunsSchemaMismatchGuidance(t *testing.T) {
	for _, version := range []int{8, 77} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			project := t.TempDir()
			path, err := config.ResolveRunDBPath(project)
			require.NoError(t, err)
			const key = "synthetic-doctor-migration-key"
			store, err := runstore.Open(t.Context(), path, key, true)
			require.NoError(t, err)
			require.NoError(t, store.Close())
			db, err := sqliteutil.OpenWithCanary(t.Context(), sqliteutil.EncryptedDSN(path, key)+"&mode=rw", path, "fixture")
			require.NoError(t, err)
			defer db.Close()
			_, err = db.ExecContext(t.Context(), "UPDATE run_meta SET version=?", version)
			require.NoError(t, err)
			_, err = db.ExecContext(t.Context(), "UPDATE run_policy SET schema_version=?", version)
			require.NoError(t, err)
			checks := CheckRuns(t.Context(), project, key, config.DefaultConfig().Runs)
			found := false
			for _, check := range checks {
				if check.Name != "Run store" {
					continue
				}
				found = true
				require.Equal(t, Warn, check.Status)
				require.Contains(t, check.Detail, fmt.Sprintf("stored schema=%d", version))
				require.Contains(t, check.Detail, "supported schema=9")
				require.NotContains(t, check.Detail, "permissions")
				require.NotContains(t, check.Detail, "reset --discard")
				if version == 8 {
					require.Contains(t, check.Detail, "upgrade_required")
					require.Contains(t, check.Detail, "inspection does not migrate")
				} else {
					require.Contains(t, check.Detail, "schema_mismatch")
					require.Contains(t, check.Detail, "compatible capy binary")
				}
			}
			require.True(t, found)
			var after int
			require.NoError(t, db.QueryRowContext(t.Context(), "SELECT version FROM run_meta").Scan(&after))
			require.Equal(t, version, after, "doctor must not migrate")
		})
	}
}

func TestCheckRunsDamagedBlockerPreservesQuarantine(t *testing.T) {
	project := t.TempDir()
	path, err := config.ResolveRunDBPath(project)
	require.NoError(t, err)
	const key = "synthetic-damaged-inspection-key"
	store, err := runstore.Open(t.Context(), path, key, true)
	require.NoError(t, err)
	defer store.Close()
	owner, err := store.ClaimOwner(t.Context())
	require.NoError(t, err)
	defer owner.Lease.Close()
	run, err := store.Admit(t.Context(), owner, executor.CleanupLinuxDescendants, runstore.Program{})
	require.NoError(t, err)
	require.NoError(t, store.Pending(t.Context(), run.ID, owner.ID, executor.CleanupLinuxDescendants))
	require.NoError(t, store.Quarantine(t.Context()))
	db, err := sqliteutil.OpenWithCanary(t.Context(), sqliteutil.EncryptedDSN(path, key)+"&mode=rw", path, "fixture")
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec("UPDATE runs SET result=? WHERE id=?", []byte("PRIVATE_INVALID_RESULT"), run.ID)
	require.NoError(t, err)
	found := false
	for _, check := range CheckRuns(t.Context(), project, key, config.DefaultConfig().Runs) {
		require.NotContains(t, check.Detail, "PRIVATE_INVALID_RESULT")
		if check.Name == "Run store" {
			found = true
			require.Equal(t, Fail, check.Status)
			require.Contains(t, check.Detail, "recovery_inconsistent")
			require.Contains(t, check.Detail, "recorded quarantine blocks admission")
			require.Contains(t, check.Detail, "incomplete")
		}
	}
	require.True(t, found)
}
