package runstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	sqlite3 "github.com/mattn/go-sqlite3"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/sanitize"
	"github.com/serpro69/capy/internal/sqliteutil"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

type legacyFixture struct {
	db                          *sql.DB
	path, id, generation, owner string
	settings                    PolicyConfig
}

func newLegacyFixture(t *testing.T) *legacyFixture {
	t.Helper()
	path := policyPath(t)
	f := &legacyFixture{path: path, owner: filepath.Dir(filepath.Dir(filepath.Dir(path))), id: uuid.NewString(), generation: uuid.NewString(), settings: defaultPolicyConfig()}
	root := filepath.Dir(path)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "locks"), 0700))
	for _, name := range []string{"lifecycle.lock", "recovery.lock"} {
		file, err := openRegular(filepath.Join(root, name), true)
		require.NoError(t, err)
		require.NoError(t, file.Close())
	}
	for slot := 0; slot < OwnerSlots; slot++ {
		file, err := openRegular((&Store{path: path}).OwnerLeasePath(slot), true)
		require.NoError(t, err)
		require.NoError(t, file.Close())
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	f.db = f.connect(t)
	t.Cleanup(func() { f.close(t) })
	schema, err := os.ReadFile("testdata/schema_v8.sql")
	require.NoError(t, err)
	_, err = f.db.ExecContext(t.Context(), "PRAGMA auto_vacuum=INCREMENTAL;"+string(schema))
	require.NoError(t, err)
	_, err = f.db.ExecContext(t.Context(), "INSERT INTO run_meta(singleton,version,store_id,owner) VALUES(1,8,?,?)", f.id, f.owner)
	require.NoError(t, err)
	_, err = f.db.ExecContext(t.Context(), "INSERT INTO run_policy VALUES(1,1,?,?,?,8,1,'conservative')", f.settings.TTLHours, f.settings.MaxRunBytes, f.settings.MaxRetainedBytes)
	require.NoError(t, err)
	_, err = f.db.ExecContext(t.Context(), "INSERT INTO run_owners(slot,generation) VALUES(0,?)", f.generation)
	require.NoError(t, err)
	_, err = f.db.ExecContext(t.Context(), "PRAGMA journal_mode=WAL")
	require.NoError(t, err)
	return f
}

func (f *legacyFixture) connect(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sqliteutil.OpenWithCanary(t.Context(), sqliteutil.EncryptedDSN(f.path, testKey)+"&mode=rw&_foreign_keys=on", f.path, "fixture")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db
}

func (f *legacyFixture) close(t *testing.T) {
	t.Helper()
	if f.db != nil {
		require.NoError(t, f.db.Close())
		f.db = nil
	}
}

func (f *legacyFixture) identity(t *testing.T) schemaIdentity {
	t.Helper()
	info, err := os.Lstat(f.path)
	require.NoError(t, err)
	return schemaIdentity{id: f.id, file: info}
}

func (f *legacyFixture) run(t *testing.T, state executor.CleanupState, terminal bool, batch, data string) Result {
	t.Helper()
	id := uuid.NewString()
	now := time.Now().UTC()
	started := now.Add(-time.Second)
	expires := now.Add(24 * time.Hour)
	zero := 0
	r := Result{Version: 1, Kind: "run", RunID: &id, OwnerID: f.generation, OwnerType: "mcp", BatchID: batch, State: "running", StartedAt: &started,
		Capture: Capture{State: "recording", CapturedBytes: int64(len(data)), ObservedBytes: int64(len(data))}, Redaction: Projection{Version: 1, State: "ready"}, Index: Index{State: "not_requested"}}
	if terminal {
		r.State, r.ExitCode, r.CompletedAt, r.ExpiresAt = "exited", &zero, &now, &expires
		r.Capture.State, r.Capture.TerminalPersisted = "complete", true
	}
	if state == executor.CleanupNotSpawned {
		r.StartedAt, r.ExitCode, r.State, r.Capture.State = nil, nil, "admitted", "not_started"
		if batch != "" {
			r.State = "queued"
		}
		if terminal {
			r.State = "interrupted"
		}
	}
	program, err := json.Marshal(Program{Owner: f.owner, PolicyRevision: 1, TTLHours: 24, MaxRunBytes: f.settings.MaxRunBytes, Request: executor.ExecRequest{Language: executor.Shell, Code: "printf historical"}})
	require.NoError(t, err)
	encoded, err := marshalLegacyResult(r)
	require.NoError(t, err)
	var completedAt, expiresAt any
	if terminal {
		completedAt, expiresAt = now.UnixNano(), expires.UnixMilli()+1
	}
	charge := runReserve + int64(len(program)+2*len(data))
	_, err = f.db.ExecContext(t.Context(), `INSERT INTO runs(id,generation,slot,created_at,program,result,cleanup,terminal,accounted_bytes,completed_at,expires_at,stdout_bytes,stdout_lines) VALUES(?,?,0,?,?,?,?,?,?,?,?,?,?)`, id, f.generation, now.UnixNano(), program, encoded, state, terminal, charge, completedAt, expiresAt, len(data), 1+strings.Count(data, "\n"))
	require.NoError(t, err)
	_, err = f.db.ExecContext(t.Context(), "UPDATE run_meta SET accounted_bytes=accounted_bytes+?,last_created=max(last_created,?)", charge, now.UnixNano())
	require.NoError(t, err)
	checkpoint, err := sanitize.NewStream().Checkpoint()
	require.NoError(t, err)
	for _, stream := range []string{"stdout", "stderr"} {
		n := 0
		if stream == "stdout" {
			n = len(data)
		}
		_, err = f.db.ExecContext(t.Context(), "INSERT INTO run_streams(run_id,stream,checkpoint,covered,observed) VALUES(?,?,?,?,?)", id, stream, checkpoint, n, n)
		require.NoError(t, err)
	}
	if data != "" {
		_, err = f.db.ExecContext(t.Context(), "INSERT INTO run_events VALUES(?,1,'run','stdout',?,0,1)", id, []byte(data))
		require.NoError(t, err)
		_, err = f.db.ExecContext(t.Context(), "INSERT INTO run_projection VALUES(?,'stdout',0,?,1,'run',1,?,0)", id, len(data), data)
		require.NoError(t, err)
	}
	return r
}

func (f *legacyFixture) batch(t *testing.T) string {
	t.Helper()
	id := uuid.NewString()
	_, err := f.db.ExecContext(t.Context(), "INSERT INTO run_batches VALUES(?,?,0,?,NULL,NULL,24,1,'[]','batch',1,1,?)", id, f.generation, time.Now().UnixNano(), 4096+batchChildReserve*4)
	require.NoError(t, err)
	_, err = f.db.ExecContext(t.Context(), "UPDATE run_meta SET accounted_bytes=accounted_bytes+?", 4096+batchChildReserve*4)
	require.NoError(t, err)
	return id
}

func (f *legacyFixture) child(t *testing.T, r Result, ordinal int, terminal bool) {
	t.Helper()
	encoded, err := marshalLegacyResult(r)
	require.NoError(t, err)
	_, err = f.db.ExecContext(t.Context(), "INSERT INTO run_batch_children(batch_id,ordinal,run_id,label,command,result,terminal) VALUES(?,?,?,'historical label','true',?,?)", r.BatchID, ordinal, *r.RunID, encoded, terminal)
	require.NoError(t, err)
}

func (f *legacyFixture) upgrade(t *testing.T) {
	t.Helper()
	identity := f.identity(t)
	f.close(t)
	require.NoError(t, upgradeSchema8(t.Context(), f.path, testKey, f.owner, identity))
}

func migrationVersions(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var meta, policy int
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT m.version,p.schema_version FROM run_meta m,run_policy p").Scan(&meta, &policy))
	require.Equal(t, want, meta)
	require.Equal(t, want, policy)
}

func TestMigrationPreservesHistoryProofAndCursors(t *testing.T) {
	f := newLegacyFixture(t)
	b := f.batch(t)
	data := "historical migration diagnostic\nsecond line\n"
	retired := f.run(t, executor.CleanupRetired, true, b, data)
	f.child(t, retired, 0, true)
	never := f.run(t, executor.CleanupNotSpawned, true, b, "")
	f.child(t, never, 1, true)
	evicted := f.run(t, executor.CleanupRetired, true, b, "")
	evicted.State, evicted.StartedAt, evicted.ExitCode, evicted.Capture.State = "start_failed", nil, nil, "not_started"
	f.child(t, evicted, 2, true)
	_, err := f.db.ExecContext(t.Context(), "DELETE FROM runs WHERE id=?", *evicted.RunID)
	require.NoError(t, err)
	_, err = f.db.ExecContext(t.Context(), "UPDATE run_batches SET completed_at=?,expires_at=? WHERE id=?", retired.CompletedAt.UnixNano(), retired.ExpiresAt.UnixMilli()+1, b)
	require.NoError(t, err)
	// The old index writer could change Index only in the retained run blob.
	_, err = f.db.ExecContext(t.Context(), `UPDATE runs SET result=json_set(CAST(result AS TEXT),'$.index.state','ready') WHERE id=?`, *retired.RunID)
	require.NoError(t, err)
	_, err = f.db.ExecContext(t.Context(), "INSERT INTO run_search_windows VALUES(7,?,'stdout',0,?,1,'run',1)", *retired.RunID, len(data))
	require.NoError(t, err)
	for _, table := range []string{"run_search_fts", "run_search_trigram"} {
		_, err = f.db.ExecContext(t.Context(), "INSERT INTO "+table+"(rowid,title,content) VALUES(7,'stdout',?)", data)
		require.NoError(t, err)
	}
	_, err = f.db.ExecContext(t.Context(), "INSERT INTO run_search_progress VALUES(?,'stdout',?,?)", *retired.RunID, len(data), len(data))
	require.NoError(t, err)
	_, err = f.db.ExecContext(t.Context(), "UPDATE run_meta SET index_enabled=1,accounted_bytes=(SELECT sum(accounted_bytes) FROM runs)+(SELECT sum(accounted_bytes) FROM run_batches)")
	require.NoError(t, err)
	tx, err := f.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	_, err = refreshIndexDebit(t.Context(), tx)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	var before []byte
	require.NoError(t, f.db.QueryRowContext(t.Context(), "SELECT result FROM runs WHERE id=?", *retired.RunID).Scan(&before))
	cursors := &Store{id: f.id}
	rawCursor := cursors.cursor(*retired.RunID, "stdout", 1, 4)
	textCursor := cursors.projectionCursor(*retired.RunID, "stdout", 1, 0, 4)
	lockBefore, err := os.Stat(filepath.Join(filepath.Dir(f.path), "lifecycle.lock"))
	require.NoError(t, err)
	f.upgrade(t)
	s, err := openAttached(t.Context(), f.path, testKey, f.owner, unix.LOCK_SH)
	require.NoError(t, err)
	defer s.closeHandles()
	migrationVersions(t, s.db, 9)
	require.Equal(t, f.id, s.ID())
	require.EqualValues(t, 1, s.Policy().Revision)
	var after []byte
	require.NoError(t, s.db.QueryRowContext(t.Context(), "SELECT result FROM runs WHERE id=?", *retired.RunID).Scan(&after))
	require.Equal(t, before, after, "run result timestamps, counters and blobs are not rewritten")
	raw, err := s.ReadRaw(t.Context(), *retired.RunID, "stdout", rawCursor, 4096)
	require.NoError(t, err)
	require.Equal(t, data[4:], string(raw.Fragments[0].Data))
	text, err := s.ReadProjection(t.Context(), *retired.RunID, "stdout", textCursor, 4096)
	require.NoError(t, err)
	require.Equal(t, data[4:], text.Fragments[0].Text)
	batch, err := s.BatchStatus(t.Context(), b)
	require.NoError(t, err)
	require.Equal(t, executor.CleanupRetired, batch.Cleanup.State)
	for _, child := range []struct {
		r     Result
		state executor.CleanupState
	}{{retired, executor.CleanupRetired}, {never, executor.CleanupNotSpawned}, {evicted, executor.CleanupNotSpawned}} {
		var encoded []byte
		require.NoError(t, s.db.QueryRowContext(t.Context(), "SELECT result FROM run_batch_children WHERE run_id=?", *child.r.RunID).Scan(&encoded))
		var value Result
		require.NoError(t, json.Unmarshal(encoded, &value))
		require.Equal(t, 2, value.Version)
		require.Equal(t, executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: child.state}, value.Cleanup)
	}
	var matches int
	require.NoError(t, s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM run_search_fts WHERE run_search_fts MATCH 'diagnostic'").Scan(&matches))
	require.Equal(t, 1, matches)
	var usage, expected int64
	require.NoError(t, s.db.QueryRowContext(t.Context(), "SELECT accounted_bytes,index_bytes+(SELECT sum(accounted_bytes) FROM runs)+(SELECT sum(accounted_bytes) FROM run_batches) FROM run_meta").Scan(&usage, &expected))
	require.Equal(t, expected, usage, "copied history is charged once")
	lockAfter, err := os.Stat(filepath.Join(filepath.Dir(f.path), "lifecycle.lock"))
	require.NoError(t, err)
	require.True(t, os.SameFile(lockBefore, lockAfter))
	var indexes int
	require.NoError(t, s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE type='index' AND tbl_name='runs'").Scan(&indexes))
	require.Equal(t, 7, indexes)
	var foreignKeys bool
	require.NoError(t, s.db.QueryRowContext(t.Context(), "PRAGMA foreign_keys").Scan(&foreignKeys))
	require.True(t, foreignKeys)
	_, err = s.db.ExecContext(t.Context(), "INSERT INTO run_events VALUES('missing',1,'run','stdout',x'41',0,1)")
	require.Error(t, err)
	for _, clause := range []string{"", ",cleanup_scope"} {
		value := ""
		if clause != "" {
			value = ",NULL"
		}
		_, err = s.db.ExecContext(t.Context(), "INSERT INTO runs(id,generation,slot,created_at,program,result,cleanup"+clause+") VALUES('new','owner',0,1,x'00',x'00','not_spawned'"+value+")")
		require.Error(t, err)
	}
	for _, invalid := range []executor.CleanupEvidence{
		{Scope: "unknown", State: executor.CleanupRetired},
		{Scope: executor.CleanupDarwinObservation, State: executor.CleanupRetired},
		{Scope: executor.CleanupLinuxDescendants, State: executor.CleanupObserved},
	} {
		_, err := s.db.ExecContext(t.Context(), "UPDATE runs SET cleanup_scope=?,cleanup=? WHERE id=?", invalid.Scope, invalid.State, *retired.RunID)
		require.Error(t, err)
	}
	databaseBytes, err := os.ReadFile(f.path)
	require.NoError(t, err)
	require.False(t, bytes.HasPrefix(databaseBytes, []byte("SQLite format 3")))
	require.NotContains(t, string(databaseBytes), data)
}

func TestMigrationOnlyConfiguredExecutionMayUpgrade(t *testing.T) {
	f := newLegacyFixture(t)
	f.run(t, executor.CleanupRetired, true, "", "kept")
	f.close(t)
	for _, create := range []bool{false, true} {
		s, err := Open(t.Context(), f.path, testKey, create)
		require.Nil(t, s)
		requireErrorCode(t, err, "schema_mismatch")
	}
	_, err := OpenWithPolicy(t.Context(), f.path, testKey, false, f.settings)
	requireErrorCode(t, err, "schema_mismatch")
	_, err = Inspect(t.Context(), f.path, testKey)
	requireErrorCode(t, err, "schema_mismatch")
	require.ErrorContains(t, err, "schema 8")
	require.ErrorContains(t, err, "schema 9")
	s, err := OpenWithPolicy(t.Context(), f.path, testKey, true, f.settings)
	require.NoError(t, err)
	defer s.Close()
	migrationVersions(t, s.db, 9)
	owner, err := s.ClaimOwner(t.Context())
	require.NoError(t, err)
	defer owner.Lease.Close()
	_, err = s.Admit(t.Context(), owner, executor.CleanupLinuxDescendants, Program{})
	require.NoError(t, err)
}

func TestMigrationPreservesPendingAndLegacyRetiredNoStart(t *testing.T) {
	f := newLegacyFixture(t)
	pending := f.run(t, executor.CleanupPending, false, "", "prefix")
	retired := f.run(t, executor.CleanupRetired, true, "", "")
	retired.State, retired.StartedAt, retired.ExitCode, retired.Capture.State = "start_failed", nil, nil, "not_started"
	encoded, err := marshalLegacyResult(retired)
	require.NoError(t, err)
	_, err = f.db.ExecContext(t.Context(), "UPDATE runs SET result=? WHERE id=?", encoded, *retired.RunID)
	require.NoError(t, err)
	_, err = f.db.ExecContext(t.Context(), "UPDATE run_meta SET admission_blocked=1; UPDATE run_owners SET cleanup_unverified=1")
	require.NoError(t, err)
	f.upgrade(t)
	s, err := openAttached(t.Context(), f.path, testKey, f.owner, unix.LOCK_SH)
	require.NoError(t, err)
	defer s.closeHandles()
	row, err := s.Load(t.Context(), *pending.RunID)
	require.NoError(t, err)
	require.Equal(t, executor.CleanupPending, row.Cleanup.State)
	requireErrorCode(t, s.GuardAdmission(t.Context()), "cleanup_unverified")
	row, err = s.Load(t.Context(), *retired.RunID)
	require.NoError(t, err)
	require.Equal(t, executor.CleanupRetired, row.Cleanup.State, "retained legacy retired is not inferred into another state")
	var count int
	require.NoError(t, s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM runs").Scan(&count))
	require.Equal(t, 2, count)
}

func TestMigrationPreservesLegacyIndexedUnverifiedAnnotation(t *testing.T) {
	f := newLegacyFixture(t)
	b := f.batch(t)
	r := f.run(t, executor.CleanupPending, false, b, "retained uncertain prefix")
	now, expires := time.Now().UTC(), time.Now().UTC().Add(24*time.Hour)
	r.CompletedAt, r.ExpiresAt, r.State = &now, &expires, "interrupted"
	r.Capture.TerminalPersisted, r.Capture.State = true, "incomplete"
	f.child(t, r, 0, false)
	// The v8 index worker loaded the incomplete result after claiming work,
	// so saveIndexResult also persisted the read-side unverified annotation.
	markUnverified(&r)
	r.Index.State = "ready"
	encoded, err := marshalLegacyResult(r)
	require.NoError(t, err)
	_, err = f.db.ExecContext(t.Context(), "UPDATE runs SET result=? WHERE id=?", encoded, *r.RunID)
	require.NoError(t, err)
	_, err = f.db.ExecContext(t.Context(), "UPDATE run_meta SET admission_blocked=1")
	require.NoError(t, err)
	f.upgrade(t)
	f.db = f.connect(t)
	var copy []byte
	require.NoError(t, f.db.QueryRowContext(t.Context(), "SELECT result FROM run_batch_children WHERE run_id=?", *r.RunID).Scan(&copy))
	got, err := decodeManifest(copy)
	require.NoError(t, err)
	require.Equal(t, executor.CleanupPending, got.Cleanup.State)
	require.Equal(t, "cleanup_unverified", got.Error.Code)
	require.Nil(t, got.ExpiresAt)
	var blocked bool
	require.NoError(t, f.db.QueryRowContext(t.Context(), "SELECT admission_blocked FROM run_meta").Scan(&blocked))
	require.True(t, blocked)
}

func TestMigrationWrongKeyBusyAndChangedStore(t *testing.T) {
	f := newLegacyFixture(t)
	f.run(t, executor.CleanupRetired, true, "", "preserved")
	expected := f.identity(t)
	f.close(t)
	before, err := os.ReadFile(f.path)
	require.NoError(t, err)
	_, err = OpenWithPolicy(t.Context(), f.path, "wrong-synthetic-key", true, f.settings)
	requireErrorCode(t, err, "wrong_key")
	after, err := os.ReadFile(f.path)
	require.NoError(t, err)
	require.Equal(t, before, after)
	reader, err := openAttachedSchema(t.Context(), f.path, testKey, f.owner, unix.LOCK_SH, 8, nil)
	require.NoError(t, err)
	_, err = OpenWithPolicy(t.Context(), f.path, testKey, true, f.settings)
	requireErrorCode(t, err, "schema_upgrade_busy")
	migrationVersions(t, reader.db, 8)
	require.NoError(t, reader.closeHandles())
	// A valid same-key replacement retaining the same UUID must still fail the
	// inode check across the shared-to-exclusive gap.
	require.NoError(t, os.Rename(f.path, f.path+".old"))
	require.NoError(t, os.WriteFile(f.path, before, 0600))
	requireErrorCode(t, upgradeSchema8(t.Context(), f.path, testKey, f.owner, expected), "run_unavailable")
	f.db = f.connect(t)
	migrationVersions(t, f.db, 8)
}

func TestMigrationReattachmentRejectsReplacement(t *testing.T) {
	f := newLegacyFixture(t)
	f.run(t, executor.CleanupRetired, true, "", "original history")
	expected := f.identity(t)
	f.upgrade(t)
	data, err := os.ReadFile(f.path)
	require.NoError(t, err)
	require.NoError(t, os.Rename(f.path, f.path+".old"))
	require.NoError(t, os.WriteFile(f.path, data, 0600))
	// The same identity token is carried through the post-upgrade shared and
	// optional policy attachment, not just the pre-upgrade exclusive acquisition.
	for _, lock := range []int{unix.LOCK_SH, unix.LOCK_EX} {
		s, err := openAttachedSchema(t.Context(), f.path, testKey, f.owner, lock, 9, &expected)
		require.Nil(t, s)
		requireErrorCode(t, err, "run_unavailable")
	}
}

func TestMigrationRejectsMalformedLegacyEvidence(t *testing.T) {
	for _, mode := range []string{"missing nonterminal", "bad identity", "bad state", "inconsistent copy", "unknown version", "inconsistent parent", "unexpected proof"} {
		t.Run(mode, func(t *testing.T) {
			f := newLegacyFixture(t)
			b := f.batch(t)
			r := f.run(t, executor.CleanupPending, false, b, "")
			f.child(t, r, 0, false)
			var query string
			switch mode {
			case "missing nonterminal":
				query = "DELETE FROM runs"
			case "bad identity":
				query = "UPDATE run_batch_children SET result=json_set(CAST(result AS TEXT),'$.run_id','different')"
			case "bad state":
				query = "UPDATE run_batch_children SET result=json_set(CAST(result AS TEXT),'$.capture.state','unsupported')"
			case "inconsistent copy":
				query = "UPDATE run_batch_children SET result=json_set(CAST(result AS TEXT),'$.state','queued')"
			case "unknown version":
				query = "UPDATE run_meta SET version=77"
			case "inconsistent parent":
				query = "UPDATE run_batches SET completed_at=1,expires_at=2"
			case "unexpected proof":
				query = `UPDATE run_batch_children SET result=json_set(CAST(result AS TEXT),'$.cleanup',json('{"scope":"darwin_process_group_v1","state":"retired"}'))`
			}
			_, err := f.db.ExecContext(t.Context(), query)
			require.NoError(t, err)
			expected := f.identity(t)
			f.close(t)
			err = upgradeSchema8(t.Context(), f.path, testKey, f.owner, expected)
			require.Error(t, err)
			f.db = f.connect(t)
			var version int
			require.NoError(t, f.db.QueryRowContext(t.Context(), "SELECT version FROM run_meta").Scan(&version))
			if mode == "unknown version" {
				require.Equal(t, 77, version)
			} else {
				migrationVersions(t, f.db, 8)
			}
			var columns int
			require.NoError(t, f.db.QueryRowContext(t.Context(), "SELECT count(*) FROM pragma_table_info('runs') WHERE name='cleanup_scope'").Scan(&columns))
			require.Zero(t, columns, "failed migration rolls back table rebuild")
		})
	}
}

func TestMigrationCapacityAndRollback(t *testing.T) {
	for _, mode := range []string{"headroom", "sqlite full", "quota", "cancelled", "foreign key", "late failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newLegacyFixture(t)
			r := f.run(t, executor.CleanupRetired, true, "", "retained before migration")
			// Table-copy growth is separate from raw payload quota.
			if mode == "sqlite full" {
				_, err := f.db.ExecContext(t.Context(), "UPDATE runs SET program=zeroblob(262144)")
				require.NoError(t, err)
			}
			if mode == "quota" {
				_, err := f.db.ExecContext(t.Context(), "UPDATE run_meta SET accounted_bytes=1073741824")
				require.NoError(t, err)
			}
			if mode == "foreign key" {
				_, err := f.db.ExecContext(t.Context(), "PRAGMA foreign_keys=OFF; INSERT INTO run_events VALUES('orphan',1,'run','stdout',x'41',0,1); PRAGMA foreign_keys=ON")
				require.NoError(t, err)
			}
			if mode == "late failure" {
				_, err := f.db.ExecContext(t.Context(), "CREATE TRIGGER migration_failure BEFORE UPDATE OF version ON run_meta BEGIN SELECT RAISE(ABORT,'synthetic final-marker failure'); END")
				require.NoError(t, err)
			}
			f.close(t)
			s, err := openAttachedSchema(t.Context(), f.path, testKey, f.owner, unix.LOCK_EX, 8, nil)
			require.NoError(t, err)
			defer s.closeHandles()
			var beforeResult, beforeProgram []byte
			require.NoError(t, s.db.QueryRowContext(t.Context(), "SELECT result,program FROM runs WHERE id=?", *r.RunID).Scan(&beforeResult, &beforeProgram))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch mode {
			case "headroom":
				conn, e := s.db.Conn(ctx)
				require.NoError(t, e)
				err = checkMigrationSpace(ctx, conn, f.path, func(string) (uint64, error) { return 0, nil })
				require.NoError(t, conn.Close())
				requireErrorCode(t, err, "schema_upgrade_capacity")
			case "sqlite full":
				var pages int
				require.NoError(t, s.db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages))
				_, err = s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA max_page_count=%d", pages+4))
				require.NoError(t, err)
				err = migrateSchema8(ctx, s)
				requireErrorCode(t, err, "schema_upgrade_capacity")
			case "cancelled":
				// Cancel after the table rebuild, during the final marker write.
				conn, e := s.db.Conn(ctx)
				require.NoError(t, e)
				require.NoError(t, conn.Raw(func(driver any) error {
					return driver.(*sqlite3.SQLiteConn).RegisterFunc("cancel_migration", func() int { cancel(); return 0 }, false)
				}))
				require.NoError(t, conn.Close())
				_, err = s.db.ExecContext(ctx, "CREATE TRIGGER migration_cancel BEFORE UPDATE OF version ON run_meta BEGIN SELECT cancel_migration(); END")
				require.NoError(t, err)
				err = migrateSchema8(ctx, s)
				require.Error(t, err)
				require.True(t, errors.Is(err, context.Canceled))
			default:
				err = migrateSchema8(ctx, s)
				require.Error(t, err)
			}
			migrationVersions(t, s.db, 8)
			var afterResult, afterProgram []byte
			require.NoError(t, s.db.QueryRowContext(t.Context(), "SELECT result,program FROM runs WHERE id=?", *r.RunID).Scan(&afterResult, &afterProgram))
			if !bytes.Equal(beforeResult, afterResult) || !bytes.Equal(beforeProgram, afterProgram) {
				t.Fatal("failed migration changed retained history")
			}
			var count int
			require.NoError(t, s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE name='runs_v9'").Scan(&count))
			require.Zero(t, count)
		})
	}
}

func TestMigrationCrashHelper(t *testing.T) {
	path := os.Getenv("CAPY_MIGRATION_CRASH_FIXTURE")
	if path == "" {
		return
	}
	owner := filepath.Dir(filepath.Dir(filepath.Dir(path)))
	s, err := openAttachedSchema(t.Context(), path, testKey, owner, unix.LOCK_EX, 8, nil)
	require.NoError(t, err)
	defer s.closeHandles()
	if os.Getenv("CAPY_MIGRATION_WAL_LIMIT") == "1" {
		// Limit only this disposable helper. Ignoring SIGXFSZ makes the WAL
		// write return a real filesystem error rather than terminating Go.
		signal.Ignore(syscall.SIGXFSZ)
		var limit unix.Rlimit
		require.NoError(t, unix.Getrlimit(unix.RLIMIT_FSIZE, &limit))
		limit.Cur = 65536
		require.NoError(t, unix.Setrlimit(unix.RLIMIT_FSIZE, &limit))
		err := migrateSchema8(t.Context(), s)
		require.Error(t, err)
		info, statErr := os.Stat(path + "-wal")
		require.NoError(t, statErr)
		require.EqualValues(t, 65536, info.Size(), "failure must reach the imposed WAL boundary")
		os.Exit(92)
	}
	conn, err := s.db.Conn(t.Context())
	require.NoError(t, err)
	require.NoError(t, conn.Raw(func(driver any) error {
		return driver.(*sqlite3.SQLiteConn).RegisterFunc("migration_crash", func() int { os.Exit(91); return 0 }, false)
	}))
	require.NoError(t, conn.Close())
	err = migrateSchema8(t.Context(), s)
	t.Fatalf("crash fixture unexpectedly returned: %v", err)
}

func TestMigrationWALWriteFailureRollsBack(t *testing.T) {
	f := newLegacyFixture(t)
	r := f.run(t, executor.CleanupRetired, true, "", "WAL write failure keeps raw history")
	program, err := json.Marshal(Program{Owner: f.owner, Request: executor.ExecRequest{Language: executor.Shell, Code: strings.Repeat("x", 512<<10)}})
	require.NoError(t, err)
	_, err = f.db.ExecContext(t.Context(), "UPDATE runs SET program=? WHERE id=?", program, *r.RunID)
	require.NoError(t, err)
	f.close(t)
	exe, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestMigrationCrashHelper$")
	cmd.Env = append(os.Environ(), "CAPY_MIGRATION_CRASH_FIXTURE="+f.path, "CAPY_MIGRATION_WAL_LIMIT=1")
	output, err := cmd.CombinedOutput()
	var exited *exec.ExitError
	require.ErrorAs(t, err, &exited, "%s", output)
	require.Equal(t, 92, exited.ExitCode(), "%s", output)
	f.db = f.connect(t)
	migrationVersions(t, f.db, 8)
	var after []byte
	require.NoError(t, f.db.QueryRowContext(t.Context(), "SELECT program FROM runs WHERE id=?", *r.RunID).Scan(&after))
	require.Equal(t, program, after)
}

func TestMigrationBorrowedLeaseBlocksUpgradeAndConcurrentWinner(t *testing.T) {
	f := newLegacyFixture(t)
	expected := f.identity(t)
	f.close(t)
	old, err := openAttachedSchema(t.Context(), f.path, testKey, f.owner, unix.LOCK_SH, 8, nil)
	require.NoError(t, err)
	owner, err := old.ClaimOwner(t.Context())
	require.NoError(t, err)
	refs, err := old.BorrowLeases(owner)
	require.NoError(t, err)
	require.NoError(t, owner.Lease.Close())
	require.NoError(t, old.closeHandles())
	requireErrorCode(t, upgradeSchema8(t.Context(), f.path, testKey, f.owner, expected), "schema_upgrade_busy")
	require.NoError(t, refs.Close())
	require.NoError(t, upgradeSchema8(t.Context(), f.path, testKey, f.owner, expected))
	// A second contender may observe the same store already upgraded after it
	// releases the shared lease. Revalidate instead of rebuilding it again.
	require.NoError(t, upgradeSchema8(t.Context(), f.path, testKey, f.owner, expected))
	f.db = f.connect(t)
	migrationVersions(t, f.db, 9)
}

func TestMigrationManifestAndPublicVersionAgree(t *testing.T) {
	s, o := retentionStore(t, 8<<20)
	b, rows, err := s.AdmitBatch(t.Context(), o, executor.CleanupLinuxDescendants, BatchInput{Concurrency: 1, Commands: []BatchCommand{{Command: "true"}}})
	require.NoError(t, err)
	row, err := s.Load(t.Context(), rows[0].ID)
	require.NoError(t, err)
	require.Equal(t, 2, row.Result.Version)
	var version int
	require.NoError(t, s.db.QueryRowContext(t.Context(), "SELECT json_extract(CAST(result AS TEXT),'$.version') FROM run_batch_children WHERE batch_id=?", b.BatchID).Scan(&version))
	require.Equal(t, 2, version)
}

func TestMigrationRejectsContradictoryCaptureCompletion(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(fmt.Sprintf("terminal=%t", terminal), func(t *testing.T) {
			f := newLegacyFixture(t)
			batch := f.batch(t)
			state := executor.CleanupPending
			if terminal {
				state = executor.CleanupRetired
			}
			r := f.run(t, state, terminal, batch, "")
			r.Capture.State = "complete"
			if terminal {
				r.Capture.State = "recording"
			}
			f.child(t, r, 0, terminal)
			encoded, err := marshalLegacyResult(r)
			require.NoError(t, err)
			if terminal {
				_, err = f.db.ExecContext(t.Context(), "DELETE FROM runs WHERE id=?", *r.RunID)
				require.NoError(t, err)
				_, err = f.db.ExecContext(t.Context(), "UPDATE run_batches SET completed_at=?,expires_at=? WHERE id=?", r.CompletedAt.UnixNano(), r.ExpiresAt.UnixMilli()+1, batch)
			} else {
				_, err = f.db.ExecContext(t.Context(), "UPDATE runs SET result=? WHERE id=?", encoded, *r.RunID)
			}
			require.NoError(t, err)
			expected := f.identity(t)
			f.close(t)
			err = upgradeSchema8(t.Context(), f.path, testKey, f.owner, expected)
			requireErrorCode(t, err, "schema_upgrade_failed")
			require.ErrorContains(t, err, "capture")
			f.db = f.connect(t)
			migrationVersions(t, f.db, 8)
			var preserved []byte
			require.NoError(t, f.db.QueryRowContext(t.Context(), "SELECT result FROM run_batch_children WHERE run_id=?", *r.RunID).Scan(&preserved))
			require.Equal(t, encoded, preserved, "failed validation preserves the original saved result")
		})
	}
}

func TestMigrationInterruptedProcessPreservesHistory(t *testing.T) {
	f := newLegacyFixture(t)
	r := f.run(t, executor.CleanupRetired, true, "", "survives abrupt migration death")
	_, err := f.db.ExecContext(t.Context(), "CREATE TRIGGER migration_crash_trigger BEFORE UPDATE OF version ON run_meta BEGIN SELECT migration_crash(); END")
	require.NoError(t, err)
	f.close(t)
	exe, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestMigrationCrashHelper$")
	cmd.Env = append(os.Environ(), "CAPY_MIGRATION_CRASH_FIXTURE="+f.path)
	output, err := cmd.CombinedOutput()
	var exited *exec.ExitError
	require.ErrorAs(t, err, &exited, "%s", output)
	require.Equal(t, 91, exited.ExitCode(), "%s", output)
	f.db = f.connect(t)
	migrationVersions(t, f.db, 8)
	var raw []byte
	require.NoError(t, f.db.QueryRowContext(t.Context(), "SELECT data FROM run_events WHERE run_id=?", *r.RunID).Scan(&raw))
	require.Equal(t, "survives abrupt migration death", string(raw))
	_, err = f.db.ExecContext(t.Context(), "DROP TRIGGER migration_crash_trigger")
	require.NoError(t, err)
	f.upgrade(t)
	f.db = f.connect(t)
	migrationVersions(t, f.db, 9)
}

// Keep the schema-8 fixture frozen independently of the current public encoder.
func marshalLegacyResult(r Result) ([]byte, error) {
	type plain Result
	data, err := json.Marshal(plain(r))
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	delete(fields, "cleanup")
	return json.Marshal(fields)
}
