package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/serpro69/capy/internal/sqliteutil"
	"github.com/stretchr/testify/require"
)

func upgradeCLIFixture(t *testing.T, version int, blocked bool) (string, string, string) {
	t.Helper()
	project := cliProject(t, "")
	key := "synthetic-upgrade-cli-key"
	path, err := config.ResolveRunDBPath(project)
	require.NoError(t, err)
	s, err := runstore.Open(t.Context(), path, key, true)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	// Retain initialized permanent locks, replace the empty DB with frozen DDL.
	require.NoError(t, os.Remove(path))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	db, err := sqliteutil.OpenWithCanary(t.Context(), sqliteutil.EncryptedDSN(path, key), path, "fixture")
	require.NoError(t, err)
	ddl, err := os.ReadFile(fmt.Sprintf("../../internal/runstore/testdata/schema_v%d.sql", version))
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), string(ddl))
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), "INSERT INTO run_meta(singleton,version,store_id,owner,admission_blocked) VALUES(1,?,?,?,?)", version, uuid.NewString(), project, blocked)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), "INSERT INTO run_policy VALUES(1,1,24,67108864,536870912,?,1,'conservative')", version)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	return project, path, key
}

func upgradeFixtureDB(t *testing.T, path, key string) *sql.DB {
	t.Helper()
	db, err := sqliteutil.OpenWithCanary(t.Context(), sqliteutil.EncryptedDSN(path, key)+"&mode=rw", path, "fixture")
	require.NoError(t, err)
	return db
}

func TestRunsUpgradeCLIAndAutomaticExecution(t *testing.T) {
	capturedCleanup(t)
	bin := buildStdioCapy(t)
	for _, version := range []int{8, 9} {
		for _, blocked := range []bool{false, true} {
			for _, offline := range []bool{false, true} {
				t.Run(fmt.Sprintf("v%d/blocked=%t/offline=%t", version, blocked, offline), func(t *testing.T) {
					project, path, key := upgradeCLIFixture(t, version, blocked)
					env := stdioEnv(t, key)
					if offline {
						out, diag, status := capturedCLI(t, bin, project, env, "runs", "upgrade", "--json", "--project-dir", project)
						require.Zero(t, status, "%s", out)
						require.Empty(t, diag)
						var result runstore.UpgradeResult
						require.NoError(t, json.Unmarshal(out, &result))
						require.Equal(t, 10, result.SchemaVersion)
						require.Equal(t, version, result.FromSchema)
						require.NotNil(t, result.AdmissionBlocked)
						require.Equal(t, blocked, *result.AdmissionBlocked)
					} else {
						r := cliRun(t, bin, project, env, "printf executed > upgrade-executed")
						if blocked {
							require.Nil(t, r.RunID)
							require.NotNil(t, r.Error)
							require.Equal(t, "cleanup_unverified", r.Error.Code)
							require.NoFileExists(t, filepath.Join(project, "upgrade-executed"))
						} else {
							require.NotNil(t, r.RunID)
							require.FileExists(t, filepath.Join(project, "upgrade-executed"))
						}
					}
					db := upgradeFixtureDB(t, path, key)
					defer db.Close()
					var schema, count int
					var remainsBlocked bool
					require.NoError(t, db.QueryRow("SELECT version,admission_blocked FROM run_meta").Scan(&schema, &remainsBlocked))
					require.Equal(t, 10, schema)
					require.Equal(t, blocked, remainsBlocked)
					require.NoError(t, db.QueryRow("SELECT count(*) FROM runs").Scan(&count))
					if offline || blocked {
						require.Zero(t, count)
					} else {
						require.Equal(t, 1, count)
					}
				})
			}
		}
	}
}

func TestRunsUpgradeCLIReportsRetainedQuotaAndNoMaintenance(t *testing.T) {
	bin := buildStdioCapy(t)
	project, path, key := upgradeCLIFixture(t, 9, true)
	env := stdioEnv(t, key)
	db := upgradeFixtureDB(t, path, key)
	_, err := db.Exec("UPDATE run_policy SET max_retained_bytes=67108864; CREATE TRIGGER fail_upgrade BEFORE UPDATE OF version ON run_meta BEGIN SELECT RAISE(ABORT,'PRIVATE_ERROR'); END")
	require.NoError(t, err)
	require.NoError(t, db.Close())
	out, diag, status := capturedCLI(t, bin, project, env, "runs", "upgrade", "--json", "--project-dir", project)
	require.Equal(t, 1, status)
	require.Empty(t, diag)
	require.NotContains(t, string(out), "PRIVATE_ERROR")
	var result runstore.UpgradeResult
	require.NoError(t, json.Unmarshal(out, &result))
	require.True(t, result.QuotaIncreased)
	require.Equal(t, 9, result.SchemaVersion)
	require.EqualValues(t, 2, result.Policy.Revision)
	require.NotNil(t, result.AdmissionBlocked)
	require.True(t, *result.AdmissionBlocked)
	for _, flags := range [][]string{{"--apply"}, {"--run-id", "x"}, {"positional"}} {
		args := append([]string{"runs", "upgrade", "--json", "--project-dir", project}, flags...)
		out, _, status = capturedCLI(t, bin, project, env, args...)
		require.Equal(t, 2, status)
		require.Contains(t, string(out), "invalid_argument")
	}
	out, _, status = capturedCLI(t, bin, project, env, "runs", "upgrade", "--project-dir", project)
	require.Equal(t, 1, status)
	require.Contains(t, string(out), "schema 9 → 9")
	require.Contains(t, string(out), "No command admitted")
}

func TestRunsUpgradeUnknownStateIsNotHealthy(t *testing.T) {
	text, err := encodeRunText(runstore.UpgradeResult{Version: 2, Kind: "upgrade", Error: &runstore.Error{Code: "wrong_key", Message: "restore credential"}})
	require.NoError(t, err)
	require.NotContains(t, string(text), "block=false")
	require.NotContains(t, string(text), "schema 0")
	require.Contains(t, string(text), "unavailable")
	encoded, err := encodeRunJSON(runstore.UpgradeResult{Version: 2, Kind: "upgrade"})
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "admission_blocked")
	require.NotContains(t, string(encoded), "policy")
}
