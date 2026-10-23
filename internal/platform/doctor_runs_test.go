package platform

import (
	"fmt"
	"testing"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/serpro69/capy/internal/sqliteutil"
	"github.com/stretchr/testify/require"
)

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
			checks := CheckRuns(t.Context(), project, key, config.DefaultConfig().Runs)
			found := false
			for _, check := range checks {
				if check.Name != "Run store" {
					continue
				}
				found = true
				require.Equal(t, Warn, check.Status)
				require.Contains(t, check.Detail, "schema_mismatch")
				require.Contains(t, check.Detail, fmt.Sprintf("stored schema=%d", version))
				require.Contains(t, check.Detail, "supported schema=9")
				require.NotContains(t, check.Detail, "permissions")
				require.NotContains(t, check.Detail, "reset --discard")
				if version == 8 {
					require.Contains(t, check.Detail, "captured execution to upgrade")
				} else {
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
