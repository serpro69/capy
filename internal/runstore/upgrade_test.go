package runstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"
	"github.com/serpro69/capy/internal/executor"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// Exercise actual independent legacy DDL; this never relabels a current DB.
func newSchema9Fixture(t *testing.T) *legacyFixture {
	t.Helper()
	f := newLegacyFixture(t)
	// Replace only the empty fixture database, retaining its permanent locks.
	f.close(t)
	require.NoError(t, os.Remove(f.path))
	file, err := os.OpenFile(f.path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	f.db = f.connect(t)
	ddl, err := os.ReadFile("testdata/schema_v9.sql")
	require.NoError(t, err)
	_, err = f.db.ExecContext(t.Context(), "PRAGMA auto_vacuum=INCREMENTAL;"+string(ddl))
	require.NoError(t, err)
	_, err = f.db.ExecContext(t.Context(), "INSERT INTO run_meta(singleton,version,store_id,owner) VALUES(1,9,?,?)", f.id, f.owner)
	require.NoError(t, err)
	_, err = f.db.ExecContext(t.Context(), "INSERT INTO run_policy VALUES(1,1,?,?,?,9,1,'conservative')", f.settings.TTLHours, f.settings.MaxRunBytes, f.settings.MaxRetainedBytes)
	require.NoError(t, err)
	_, err = f.db.ExecContext(t.Context(), "INSERT INTO run_owners(slot,generation) VALUES(0,?)", f.generation)
	require.NoError(t, err)
	_, err = f.db.ExecContext(t.Context(), "PRAGMA journal_mode=WAL")
	require.NoError(t, err)
	return f
}

func schema9Run(t *testing.T, f *legacyFixture, pending bool) string {
	t.Helper()
	// The supported schema-9 admission boundary predates last_phase. Preserve the
	// result's intentionally noncanonical whitespace as byte-level evidence.
	id := "70000000-0000-0000-0000-000000000009"
	now := time.Now().UTC()
	expiry := now.Add(24 * time.Hour)
	proof := executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: executor.CleanupRetired}
	r := Result{Version: 2, Kind: "run", RunID: &id, OwnerID: f.generation, OwnerType: "mcp", State: "exited", CompletedAt: &now, ExpiresAt: &expiry, Cleanup: proof, Capture: Capture{State: "complete", TerminalPersisted: true}, Index: Index{State: "not_requested"}}
	if pending {
		proof.State = executor.CleanupPending
		r.Cleanup = proof
		r.State = "running"
		r.CompletedAt = nil
		r.ExpiresAt = nil
		r.Capture.State = "recording"
		r.Capture.TerminalPersisted = false
	}
	raw, err := json.MarshalIndent(r, "", " ")
	require.NoError(t, err)
	program, err := json.Marshal(Program{TTLHours: 24, PolicyRevision: 1, MaxRunBytes: f.settings.MaxRunBytes})
	require.NoError(t, err)
	_, err = f.db.ExecContext(t.Context(), `INSERT INTO runs(rowid,id,generation,slot,created_at,program,result,cleanup_scope,cleanup,terminal,accounted_bytes) VALUES(73,?,?,0,123,?,?,?,?,?,?)`, id, f.generation, program, raw, proof.Scope, proof.State, !pending, runReserve)
	require.NoError(t, err)
	_, err = f.db.ExecContext(t.Context(), "UPDATE run_meta SET accounted_bytes=?", runReserve)
	require.NoError(t, err)
	_, err = f.db.ExecContext(t.Context(), "INSERT INTO run_events VALUES(?,1,'run','stdout',x'0061ff0a',0,1)", id)
	require.NoError(t, err)
	return id
}

func TestUpgradeSchema9PreservesExactHistory(t *testing.T) {
	f := newSchema9Fixture(t)
	id := schema9Run(t, f, true)
	_, err := f.db.ExecContext(t.Context(), "UPDATE run_meta SET admission_blocked=1; UPDATE run_owners SET cleanup_unverified=1; CREATE INDEX fixture_result ON runs(terminal,slot)")
	require.NoError(t, err)
	var before, program, output []byte
	require.NoError(t, f.db.QueryRow("SELECT result,program FROM runs WHERE id=?", id).Scan(&before, &program))
	require.NoError(t, f.db.QueryRow("SELECT data FROM run_events WHERE run_id=?", id).Scan(&output))
	f.close(t)
	result, err := Upgrade(t.Context(), f.path, testKey, f.settings)
	require.NoError(t, err)
	require.Equal(t, 9, result.FromSchema)
	require.Equal(t, 10, result.SchemaVersion)
	require.NotNil(t, result.AdmissionBlocked)
	require.True(t, *result.AdmissionBlocked)
	require.False(t, result.QuotaIncreased)
	f.db = f.connect(t)
	migrationVersions(t, f.db, 10)
	var after, afterProgram, afterOutput []byte
	var rowid int
	var phase string
	require.NoError(t, f.db.QueryRow("SELECT rowid,result,program,last_phase FROM runs WHERE id=?", id).Scan(&rowid, &after, &afterProgram, &phase))
	require.Equal(t, 73, rowid)
	require.Equal(t, "unknown", phase)
	require.Equal(t, before, after)
	require.Equal(t, program, afterProgram)
	require.NoError(t, f.db.QueryRow("SELECT data FROM run_events WHERE run_id=?", id).Scan(&afterOutput))
	require.Equal(t, output, afterOutput)
	var indexes, owners, epoch int
	require.NoError(t, f.db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name='fixture_result'").Scan(&indexes))
	require.Equal(t, 1, indexes)
	require.NoError(t, f.db.QueryRow("SELECT count(*) FROM run_owners").Scan(&owners))
	require.Equal(t, 1, owners)
	require.NoError(t, f.db.QueryRow("SELECT recovery_revision FROM run_meta").Scan(&epoch))
	require.Zero(t, epoch)
	checkPhaseConstraints(t, f.db)
	// Every original FK survives the rebuild.
	_, err = f.db.Exec("INSERT INTO run_events VALUES('absent',1,'run','stdout',x'41',0,1)")
	require.Error(t, err)
}

func checkPhaseConstraints(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, value := range []string{"omit", "NULL", "'invalid'"} {
		cols, values := "", ""
		if value != "omit" {
			cols = ",last_phase"
			values = "," + value
		}
		_, err := db.Exec("INSERT INTO runs(id,generation,slot,created_at,program,result,cleanup_scope,cleanup" + cols + ") VALUES('new','owner',0,1,x'00',x'00','linux_descendants_v1','not_spawned'" + values + ")")
		require.Error(t, err, value)
	}
	var nullable int
	var defaultValue sql.NullString
	require.NoError(t, db.QueryRow(`SELECT "notnull",dflt_value FROM pragma_table_info('runs') WHERE name='last_phase'`).Scan(&nullable, &defaultValue))
	require.Equal(t, 1, nullable)
	require.False(t, defaultValue.Valid)
}

func TestUpgradeFreshPhasesAndInsertionBoundary(t *testing.T) {
	s, o := retentionStore(t, 8<<20)
	checkPhaseConstraints(t, s.db)
	r, err := s.Admit(t.Context(), o, executor.CleanupLinuxDescendants, Program{})
	require.NoError(t, err)
	loaded, err := s.Load(t.Context(), r.ID)
	require.NoError(t, err)
	require.Equal(t, "admitted", loaded.LastPhase)
	_, children, err := s.AdmitBatch(t.Context(), o, executor.CleanupLinuxDescendants, BatchInput{Concurrency: 1, Commands: []BatchCommand{{Command: "true"}}})
	require.NoError(t, err)
	loaded, err = s.Load(t.Context(), children[0].ID)
	require.NoError(t, err)
	require.Equal(t, "queued", loaded.LastPhase)
	for _, phase := range []string{"", "unknown", "invalid", "authorized", "queued"} {
		row, program, result, err := s.prepareRun(o, executor.CleanupLinuxDescendants, Program{})
		require.NoError(t, err)
		row.LastPhase = phase
		tx, err := s.db.BeginTx(t.Context(), nil)
		require.NoError(t, err)
		requireErrorCode(t, insertRun(t.Context(), tx, row, program, result), "invalid_argument")
		require.NoError(t, tx.Rollback())
	}
}

func TestUpgradeQuotaBeforeMigrationAndFailure(t *testing.T) {
	for _, version := range []int{8, 9, 10} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("v%d/fail=%t", version, fail), func(t *testing.T) {
				f := newLegacyFixture(t)
				if version >= 9 {
					f.close(t)
					require.NoError(t, upgradeLegacyStage(t.Context(), f.path, testKey, f.owner, f.identity(t)))
					f.db = f.connect(t)
				}
				if version == 10 {
					f.close(t)
					_, err := Upgrade(t.Context(), f.path, testKey, f.settings)
					require.NoError(t, err)
					f.db = f.connect(t)
				}
				// Simulate a legitimate old quota too small for preserved metadata.
				_, err := f.db.Exec("UPDATE run_policy SET max_run_bytes=1,max_retained_bytes=1; UPDATE run_meta SET admission_blocked=1")
				require.NoError(t, err)
				if fail && version < 10 {
					_, err = f.db.Exec("CREATE TRIGGER stop_upgrade BEFORE UPDATE OF version ON run_meta BEGIN SELECT RAISE(ABORT,'private fault'); END")
					require.NoError(t, err)
				}
				settings := f.settings
				settings.MaxRunBytes = 1
				f.close(t)
				result, err := Upgrade(t.Context(), f.path, testKey, settings)
				if fail && version < 10 {
					requireErrorCode(t, err, "schema_upgrade_failed")
					require.Equal(t, version, result.SchemaVersion)
				} else {
					require.NoError(t, err)
					require.Equal(t, 10, result.SchemaVersion)
				}
				require.True(t, result.QuotaIncreased)
				require.EqualValues(t, 2, result.Policy.Revision)
				require.Equal(t, settings.MaxRetainedBytes, result.Policy.MaxRetainedBytes)
				require.NotNil(t, result.AdmissionBlocked)
				require.True(t, *result.AdmissionBlocked)
				f.db = f.connect(t)
				var quota, revision int64
				require.NoError(t, f.db.QueryRow("SELECT max_retained_bytes,revision FROM run_policy").Scan(&quota, &revision))
				require.Equal(t, settings.MaxRetainedBytes, quota)
				require.EqualValues(t, 2, revision)
			})
		}
	}
}

func TestUpgradeRefusesPolicyChangesWithoutMutation(t *testing.T) {
	for _, field := range []string{"ttl", "per_run", "decrease"} {
		t.Run(field, func(t *testing.T) {
			f := newSchema9Fixture(t)
			schema9Run(t, f, true)
			settings := f.settings
			switch field {
			case "ttl":
				settings.TTLHours++
			case "per_run":
				settings.MaxRunBytes--
			case "decrease":
				settings.MaxRetainedBytes--
			}
			f.close(t)
			_, err := Upgrade(t.Context(), f.path, testKey, settings)
			requireErrorCode(t, err, "policy_mismatch")
			f.db = f.connect(t)
			migrationVersions(t, f.db, 9)
			p, err := readPolicyVersion(t.Context(), f.db, 9)
			require.NoError(t, err)
			require.Equal(t, f.settings, p.PolicyConfig)
			require.EqualValues(t, 1, p.Revision)
		})
	}
}

func TestUpgradeMissingBusyWrongKeyAndCancelled(t *testing.T) {
	path := policyPath(t)
	_, err := Upgrade(t.Context(), path, testKey, defaultPolicyConfig())
	requireErrorCode(t, err, "run_unavailable")
	require.NoDirExists(t, strings.TrimSuffix(path, "/runs.db"))
	f := newSchema9Fixture(t)
	schema9Run(t, f, true)
	f.close(t)
	reader, err := openAttachedSchema(t.Context(), f.path, testKey, f.owner, unix.LOCK_SH, 9, nil)
	require.NoError(t, err)
	_, err = Upgrade(t.Context(), f.path, testKey, f.settings)
	requireErrorCode(t, err, "schema_upgrade_busy")
	require.NoError(t, reader.closeHandles())
	_, err = Upgrade(t.Context(), f.path, "wrong-key", f.settings)
	requireErrorCode(t, err, "wrong_key")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = Upgrade(ctx, f.path, testKey, f.settings)
	require.ErrorIs(t, err, context.Canceled)
	f.db = f.connect(t)
	migrationVersions(t, f.db, 9)
}

func TestUpgradeSchema9CancellationAndCommitFailure(t *testing.T) {
	for _, mode := range []string{"cancel", "late", "quota", "full"} {
		t.Run(mode, func(t *testing.T) {
			f := newSchema9Fixture(t)
			id := schema9Run(t, f, true)
			if mode == "full" {
				_, err := f.db.Exec("UPDATE runs SET program=zeroblob(262144)")
				require.NoError(t, err)
			}
			f.close(t)
			s, err := openAttachedSchema(t.Context(), f.path, testKey, f.owner, unix.LOCK_EX, 9, nil)
			require.NoError(t, err)
			defer s.closeHandles()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "cancel" {
				conn, err := s.db.Conn(ctx)
				require.NoError(t, err)
				require.NoError(t, conn.Raw(func(driver any) error {
					return driver.(*sqlite3.SQLiteConn).RegisterFunc("cancel_upgrade", func() int { cancel(); return 0 }, false)
				}))
				require.NoError(t, conn.Close())
				_, err = s.db.Exec("CREATE TRIGGER cancel_upgrade BEFORE UPDATE OF version ON run_meta BEGIN SELECT cancel_upgrade(); END")
				require.NoError(t, err)
			}
			if mode == "late" {
				_, err = s.db.Exec("CREATE TRIGGER fail_upgrade BEFORE UPDATE OF version ON run_meta BEGIN SELECT RAISE(ABORT,'private'); END")
				require.NoError(t, err)
			}
			if mode == "quota" {
				s.policy.MaxRetainedBytes = runReserve - 1
			}
			if mode == "full" {
				var pages int
				require.NoError(t, s.db.QueryRow("PRAGMA page_count").Scan(&pages))
				_, err = s.db.Exec(fmt.Sprintf("PRAGMA max_page_count=%d", pages+2))
				require.NoError(t, err)
			}
			var before []byte
			require.NoError(t, s.db.QueryRow("SELECT result FROM runs WHERE id=?", id).Scan(&before))
			err = migrateSchema9(ctx, s)
			require.Error(t, err)
			if mode == "cancel" {
				require.ErrorIs(t, err, context.Canceled)
			}
			migrationVersions(t, s.db, 9)
			var after []byte
			require.NoError(t, s.db.QueryRow("SELECT result FROM runs WHERE id=?", id).Scan(&after))
			require.Equal(t, before, after)
			var n int
			require.NoError(t, s.db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name='runs_v10' OR name='run_recovery_receipts'").Scan(&n))
			require.Zero(t, n)
		})
	}
}

func TestUpgradeSchema9CrashHelper(t *testing.T) {
	path := os.Getenv("CAPY_V10_CRASH_FIXTURE")
	if path == "" {
		return
	}
	owner := strings.TrimSuffix(path, "/.capy/runs/runs.db")
	s, err := openAttachedSchema(t.Context(), path, testKey, owner, unix.LOCK_EX, 9, nil)
	require.NoError(t, err)
	conn, err := s.db.Conn(t.Context())
	require.NoError(t, err)
	require.NoError(t, conn.Raw(func(driver any) error {
		return driver.(*sqlite3.SQLiteConn).RegisterFunc("crash_v10", func() int { os.Exit(91); return 0 }, false)
	}))
	require.NoError(t, conn.Close())
	err = migrateSchema9(t.Context(), s)
	t.Fatalf("crash fixture returned: %v", err)
}

func TestUpgradeSchema9InterruptedProcess(t *testing.T) {
	f := newSchema9Fixture(t)
	id := schema9Run(t, f, true)
	var before []byte
	require.NoError(t, f.db.QueryRow("SELECT result FROM runs WHERE id=?", id).Scan(&before))
	_, err := f.db.Exec("CREATE TRIGGER crash_v10 BEFORE UPDATE OF version ON run_meta BEGIN SELECT crash_v10(); END")
	require.NoError(t, err)
	f.close(t)
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestUpgradeSchema9CrashHelper$")
	cmd.Env = append(os.Environ(), "CAPY_V10_CRASH_FIXTURE="+f.path)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit, "%s", out)
	require.Equal(t, 91, exit.ExitCode(), "%s", out)
	f.db = f.connect(t)
	migrationVersions(t, f.db, 9)
	var after []byte
	require.NoError(t, f.db.QueryRow("SELECT result FROM runs WHERE id=?", id).Scan(&after))
	require.Equal(t, before, after)
	_, err = f.db.Exec("DROP TRIGGER crash_v10")
	require.NoError(t, err)
	f.close(t)
	_, err = Upgrade(t.Context(), f.path, testKey, f.settings)
	require.NoError(t, err)
}

func TestUpgradeIntermediateAndConcurrentWinners(t *testing.T) {
	f := newLegacyFixture(t)
	f.run(t, executor.CleanupPending, false, "", "preserved")
	expected := f.identity(t)
	f.close(t)
	require.NoError(t, upgradeLegacyStage(t.Context(), f.path, testKey, f.owner, expected))
	f.db = f.connect(t)
	migrationVersions(t, f.db, 9)
	f.close(t)
	// A caller that saw 8 accepts another upgrader's committed 9, then advances.
	require.NoError(t, upgradeSupportedSchema(t.Context(), f.path, testKey, f.owner, 8, expected))
	require.NoError(t, upgradeSupportedSchema(t.Context(), f.path, testKey, f.owner, 9, expected))
	require.NoError(t, upgradeLegacyStage(t.Context(), f.path, testKey, f.owner, expected))
	f.db = f.connect(t)
	migrationVersions(t, f.db, 10)
}

func TestUpgradeDoesNotMaintainExpiredHistory(t *testing.T) {
	f := newLegacyFixture(t)
	r := f.run(t, executor.CleanupRetired, true, "", "expired output still retained")
	expired := time.Now().UTC().Add(-48 * time.Hour)
	r.CompletedAt = &expired
	r.ExpiresAt = &expired
	before, err := marshalLegacyResult(r)
	require.NoError(t, err)
	_, err = f.db.Exec("UPDATE runs SET result=?,completed_at=?,expires_at=? WHERE id=?", before, expired.UnixNano(), expired.UnixMilli(), *r.RunID)
	require.NoError(t, err)
	f.close(t)
	result, err := Upgrade(t.Context(), f.path, testKey, f.settings)
	require.NoError(t, err)
	require.Equal(t, 10, result.SchemaVersion)
	f.db = f.connect(t)
	var after, output []byte
	require.NoError(t, f.db.QueryRow("SELECT result FROM runs WHERE id=?", *r.RunID).Scan(&after))
	require.Equal(t, before, after)
	require.NoError(t, f.db.QueryRow("SELECT data FROM run_events WHERE run_id=?", *r.RunID).Scan(&output))
	require.Equal(t, "expired output still retained", string(output))
	f.close(t)
	_, err = openAttachedSchema(t.Context(), f.path, testKey, f.owner, unix.LOCK_SH, 9, nil)
	requireErrorCode(t, err, "schema_mismatch")
}

func TestUpgradeUnsupportedSchemaCannotPublishQuota(t *testing.T) {
	for _, version := range []int{0, -1, 77} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			f := newSchema9Fixture(t)
			_, err := f.db.Exec("UPDATE run_meta SET version=?", version)
			require.NoError(t, err)
			_, err = f.db.Exec("UPDATE run_policy SET schema_version=?", version)
			require.NoError(t, err)
			settings := f.settings
			settings.MaxRetainedBytes *= 2
			f.close(t)
			result, err := Upgrade(t.Context(), f.path, testKey, settings)
			requireErrorCode(t, err, "schema_mismatch")
			require.Nil(t, result.Policy)
			require.False(t, result.QuotaIncreased)
			f.db = f.connect(t)
			migrationVersions(t, f.db, version)
			var quota, revision int64
			require.NoError(t, f.db.QueryRow("SELECT max_retained_bytes,revision FROM run_policy").Scan(&quota, &revision))
			require.Equal(t, f.settings.MaxRetainedBytes, quota)
			require.EqualValues(t, 1, revision)
		})
	}
}

func TestUpgradeAutomaticUsesStoredQuota(t *testing.T) {
	f := newSchema9Fixture(t)
	schema9Run(t, f, true)
	_, err := f.db.Exec("UPDATE run_policy SET max_run_bytes=1,max_retained_bytes=?; UPDATE run_meta SET admission_blocked=1", runReserve-1)
	require.NoError(t, err)
	settings := f.settings
	settings.MaxRunBytes = 1
	f.close(t)
	_, err = OpenWithPolicy(t.Context(), f.path, testKey, true, settings)
	requireErrorCode(t, err, "schema_upgrade_capacity")
	f.db = f.connect(t)
	migrationVersions(t, f.db, 9)
	var quota, revision int64
	require.NoError(t, f.db.QueryRow("SELECT max_retained_bytes,revision FROM run_policy").Scan(&quota, &revision))
	require.EqualValues(t, runReserve-1, quota)
	require.EqualValues(t, 1, revision)
}

func TestUpgradeSchema9ReadersDoNotMigrate(t *testing.T) {
	f := newSchema9Fixture(t)
	f.close(t)
	for _, create := range []bool{false, true} {
		_, err := Open(t.Context(), f.path, testKey, create)
		requireErrorCode(t, err, "schema_mismatch")
	}
	_, err := OpenWithPolicy(t.Context(), f.path, testKey, false, f.settings)
	requireErrorCode(t, err, "schema_mismatch")
	view, err := Inspect(t.Context(), f.path, testKey)
	require.NoError(t, err)
	require.Equal(t, 9, view.SchemaVersion)
	require.Equal(t, "upgrade_required", view.Inspection.Action)
	f.db = f.connect(t)
	migrationVersions(t, f.db, 9)
}

func TestUpgradeIndexedQuotaRecount(t *testing.T) {
	for _, mode := range []string{"unindexed", "conservative", "dbstat"} {
		t.Run(mode, func(t *testing.T) {
			f := newSchema9Fixture(t)
			schema9Run(t, f, true)
			enabled, err := dbstatAvailable(t.Context(), f.db)
			require.NoError(t, err)
			if mode == "dbstat" && !enabled {
				t.Skip("pinned SQLite build does not enable dbstat")
			}
			if mode != "unindexed" {
				_, err = f.db.Exec("UPDATE run_policy SET accounting_mode=?; UPDATE run_meta SET index_enabled=1", mode)
				require.NoError(t, err)
			}
			tx, err := f.db.BeginTx(t.Context(), nil)
			require.NoError(t, err)
			before, err := refreshIndexDebit(t.Context(), tx)
			require.NoError(t, err)
			require.NoError(t, tx.Commit())
			// This is deliberately below the former arbitrary 128-KiB requirement.
			quota := before + 127*1024
			_, err = f.db.Exec("UPDATE run_policy SET max_run_bytes=1,max_retained_bytes=?", quota)
			require.NoError(t, err)
			settings := f.settings
			settings.MaxRunBytes = 1
			settings.MaxRetainedBytes = quota
			f.close(t)
			_, err = Upgrade(t.Context(), f.path, testKey, settings)
			require.NoError(t, err)
			f.db = f.connect(t)
			migrationVersions(t, f.db, 10)
			var after, debit int64
			require.NoError(t, f.db.QueryRow("SELECT accounted_bytes,index_bytes FROM run_meta").Scan(&after, &debit))
			require.Equal(t, int64(runReserve)+debit, after)
			require.LessOrEqual(t, after, quota)
			if mode != "unindexed" {
				require.Greater(t, debit, int64(0))
			}
		})
	}
}

func TestUpgradeOfflineReportsIntermediateCommit(t *testing.T) {
	f := newLegacyFixture(t)
	r := f.run(t, executor.CleanupPending, false, "", "retained across stages")
	_, err := f.db.Exec("CREATE TRIGGER stop_v10 BEFORE UPDATE OF version ON run_meta WHEN NEW.version=10 BEGIN SELECT RAISE(ABORT,'stop final stage'); END")
	require.NoError(t, err)
	f.close(t)
	result, err := Upgrade(t.Context(), f.path, testKey, f.settings)
	requireErrorCode(t, err, "schema_upgrade_failed")
	require.Equal(t, 8, result.FromSchema)
	require.Equal(t, 9, result.SchemaVersion)
	require.Equal(t, 9, result.Policy.SchemaVersion)
	f.db = f.connect(t)
	migrationVersions(t, f.db, 9)
	var data []byte
	require.NoError(t, f.db.QueryRow("SELECT data FROM run_events WHERE run_id=?", *r.RunID).Scan(&data))
	require.Equal(t, "retained across stages", string(data))
	_, err = f.db.Exec("DROP TRIGGER stop_v10")
	require.NoError(t, err)
	f.close(t)
	result, err = Upgrade(t.Context(), f.path, testKey, f.settings)
	require.NoError(t, err)
	require.Equal(t, 9, result.FromSchema)
	require.Equal(t, 10, result.SchemaVersion)
}

func TestRecoveryReceiptRecordBoundsAndLookup(t *testing.T) {
	s, _ := retentionStore(t, 8<<20)
	id := "70000000-0000-0000-0000-000000000010"
	hash := strings.Repeat("a", 64)
	_, err := s.db.Exec(`INSERT INTO run_recovery_receipts(id,plan_hash,store_id,version,applied_at,method,basis,uid,explanation,incident_count,run_count,owner_count,store_count,charged_bytes,audit_expires_at) VALUES(?,?,?,1,1,'operator','groups_stopped',0,'stopped',0,0,0,0,?,2)`, id, hash, s.id, recoveryReceiptReserve)
	require.NoError(t, err)
	// This fixture bypasses apply; fund its receipt before close maintenance
	// performs the same exact debit release as for a real expired receipt.
	_, err = s.db.Exec("UPDATE run_meta SET accounted_bytes=accounted_bytes+?", recoveryReceiptReserve)
	require.NoError(t, err)
	for _, byPlan := range []bool{false, true} {
		lookupID, lookupHash := id, ""
		if byPlan {
			lookupID, lookupHash = "", hash
		}
		receipt, err := readRecoveryReceipt(t.Context(), s.db, lookupID, lookupHash)
		require.NoError(t, err)
		require.Equal(t, id, receipt.ID)
		require.Equal(t, "stopped", receipt.Explanation)
	}
	_, err = readRecoveryReceipt(t.Context(), s.db, "70000000-0000-0000-0000-000000000011", "")
	requireErrorCode(t, err, "receipt_unavailable")
	for _, query := range []string{"UPDATE run_recovery_receipts SET explanation=''", "UPDATE run_recovery_receipts SET charged_bytes=4097", "UPDATE run_recovery_receipts SET version=2"} {
		_, err = s.db.Exec(query)
		require.Error(t, err)
	}
}

func TestUpgradeIndexedQuotaFailureRollsBackRebuild(t *testing.T) {
	for _, state := range []string{"pending", "terminal"} {
		t.Run(state, func(t *testing.T) {
			f := newSchema9Fixture(t)
			id := schema9Run(t, f, state == "pending")
			if state == "terminal" {
				var encoded []byte
				var result Result
				require.NoError(t, f.db.QueryRow("SELECT result FROM runs WHERE id=?", id).Scan(&encoded))
				require.NoError(t, json.Unmarshal(encoded, &result))
				_, err := f.db.Exec("UPDATE runs SET completed_at=?,expires_at=? WHERE id=?", result.CompletedAt.UnixNano(), result.ExpiresAt.UnixMilli()+1, id)
				require.NoError(t, err)
			}
			_, err := f.db.Exec("UPDATE run_meta SET index_enabled=1")
			require.NoError(t, err)
			tx, err := f.db.BeginTx(t.Context(), nil)
			require.NoError(t, err)
			before, err := refreshIndexDebit(t.Context(), tx)
			require.NoError(t, err)
			require.NoError(t, tx.Commit())
			var pageSize int64
			require.NoError(t, f.db.QueryRow("PRAGMA page_size").Scan(&pageSize))
			// The old debit fits: only the post-rebuild recount can refuse this quota.
			quota := before + pageSize
			_, err = f.db.Exec("UPDATE run_policy SET max_run_bytes=1,max_retained_bytes=?", quota)
			require.NoError(t, err)
			var original []byte
			require.NoError(t, f.db.QueryRow("SELECT result FROM runs WHERE id=?", id).Scan(&original))
			settings := f.settings
			settings.MaxRunBytes = 1
			settings.MaxRetainedBytes = quota
			f.close(t)
			result, err := Upgrade(t.Context(), f.path, testKey, settings)
			requireErrorCode(t, err, "schema_upgrade_capacity")
			require.Equal(t, 9, result.SchemaVersion)
			require.False(t, result.QuotaIncreased)
			f.db = f.connect(t)
			migrationVersions(t, f.db, 9)
			var after int64
			require.NoError(t, f.db.QueryRow("SELECT accounted_bytes FROM run_meta").Scan(&after))
			require.Equal(t, before, after)
			var retained []byte
			require.NoError(t, f.db.QueryRow("SELECT result FROM runs WHERE id=?", id).Scan(&retained))
			require.Equal(t, original, retained)
			var newTables int
			require.NoError(t, f.db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name='runs_v10' OR name GLOB 'run_recovery_*' OR name='run_incidents'").Scan(&newTables))
			require.Zero(t, newTables)
		})
	}
}
