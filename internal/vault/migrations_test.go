package vault

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrateVault_FreshDBHasIndexVersion(t *testing.T) {
	s := newTestVault(t)
	db, err := s.getDB(context.Background()) // runs schemaSQL + migrateVault
	require.NoError(t, err)

	// Fresh DBs get index_version from schemaSQL.
	_, err = db.Exec(`SELECT index_version FROM vault_sessions WHERE 0`)
	require.NoError(t, err, "index_version column must exist on a fresh vault")

	// The migration is still recorded so it never re-runs.
	var cnt int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM vault_migrations WHERE name='0003_add_index_version'`).Scan(&cnt))
	assert.Equal(t, 1, cnt)

	// Idempotent: re-running migrateVault is a no-op (no error, no duplicate).
	require.NoError(t, migrateVault(context.Background(), db))
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM vault_migrations WHERE name='0003_add_index_version'`).Scan(&cnt))
	assert.Equal(t, 1, cnt)
}

func TestMigrate0003_AddsColumnToLegacyVault(t *testing.T) {
	// A pre-feature vault lacks index_version. Build that legacy shape directly
	// (plaintext DB — encryption is irrelevant to the migration logic) and verify
	// the ALTER adds the column with every existing row flagged stale (1).
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "legacy.db"))
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE vault_sessions (uuid TEXT PRIMARY KEY, content_hash TEXT NOT NULL, raw_jsonl BLOB NOT NULL);
		CREATE TABLE vault_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
		CREATE TABLE vault_migrations (name TEXT PRIMARY KEY, applied_at TEXT DEFAULT CURRENT_TIMESTAMP);
		INSERT INTO vault_sessions (uuid, content_hash, raw_jsonl) VALUES ('legacy', 'h', x'7b7d');
	`)
	require.NoError(t, err)

	require.NoError(t, migrate0003AddIndexVersion(context.Background(), db))

	var v int
	require.NoError(t, db.QueryRow(`SELECT index_version FROM vault_sessions WHERE uuid='legacy'`).Scan(&v))
	assert.Equal(t, 1, v, "legacy rows default to stale (1) → eligible for reindex")

	var cnt int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM vault_migrations WHERE name='0003_add_index_version'`).Scan(&cnt))
	assert.Equal(t, 1, cnt)

	// Idempotent re-run: the guard short-circuits, no duplicate-column error.
	require.NoError(t, migrate0003AddIndexVersion(context.Background(), db))
}

func TestMigrateVault_FreshDBHasChunkFTS(t *testing.T) {
	s := newTestVault(t)
	db, err := s.getDB(context.Background()) // runs schemaSQL + migrateVault
	require.NoError(t, err)

	// Fresh DBs get both chunk tables from schemaSQL, with the full column set
	// (title/content_text first — the retrieval-skeleton column-order invariant
	// is positional, so the SELECT below also pins the declared order).
	for _, table := range []string{"vault_chunks", "vault_chunks_trigram"} {
		//nolint:gosec // table is a test-controlled constant, never user input
		_, err = db.Exec(`SELECT title, content_text, session_uuid, subagent_id,
			chunk_index, first_line_index FROM ` + table + ` WHERE 0`)
		require.NoError(t, err, "%s must exist with the full column set on a fresh vault", table)
	}

	// The migration is still recorded so it never re-runs.
	var cnt int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM vault_migrations WHERE name='0004_add_chunk_fts'`).Scan(&cnt))
	assert.Equal(t, 1, cnt)

	// Idempotent: re-running migrateVault is a no-op (no error, no duplicate).
	require.NoError(t, migrateVault(context.Background(), db))
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM vault_migrations WHERE name='0004_add_chunk_fts'`).Scan(&cnt))
	assert.Equal(t, 1, cnt)

	// The chunk-FTS feature ships with an index-version bump: every session
	// archived before it must read as below-current so `capy vault reindex`
	// backfills its chunk tables. Bump this assertion deliberately with the
	// constant.
	assert.Equal(t, 3, currentIndexVersion, "chunk FTS requires the v3 index-version bump")
}

func TestMigrate0004_CreatesChunkTablesOnLegacyVault(t *testing.T) {
	// A pre-feature vault lacks the chunk tables. Build that legacy shape
	// directly (plaintext DB — encryption is irrelevant to the migration logic)
	// and verify the migration creates both tables.
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "legacy.db"))
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE vault_sessions (uuid TEXT PRIMARY KEY, content_hash TEXT NOT NULL, raw_jsonl BLOB NOT NULL);
		CREATE TABLE vault_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
		CREATE TABLE vault_migrations (name TEXT PRIMARY KEY, applied_at TEXT DEFAULT CURRENT_TIMESTAMP);
	`)
	require.NoError(t, err)

	require.NoError(t, migrate0004AddChunkFTS(context.Background(), db))

	for _, table := range []string{"vault_chunks", "vault_chunks_trigram"} {
		var cnt int
		//nolint:gosec // table is a test-controlled constant, never user input
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&cnt))
		assert.Equal(t, 0, cnt, "%s must exist and start empty (backfilled by reindex)", table)
	}

	var cnt int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM vault_migrations WHERE name='0004_add_chunk_fts'`).Scan(&cnt))
	assert.Equal(t, 1, cnt)

	// Idempotent re-run: the guard short-circuits, no duplicate-table error.
	require.NoError(t, migrate0004AddChunkFTS(context.Background(), db))
}

func TestMigrateVault_FreshDBHasSessionNames(t *testing.T) {
	s := newTestVault(t)
	db, err := s.getDB(context.Background())
	require.NoError(t, err)

	_, err = db.Exec(`SELECT session_uuid, custom_title, renamed_at_ns, machine_id
		FROM vault_session_names WHERE 0`)
	require.NoError(t, err, "fresh vault must expose the complete session-name schema")

	var count int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM vault_migrations WHERE name = '0005_session_names'`).Scan(&count))
	assert.Equal(t, 1, count)

	require.NoError(t, migrateVault(context.Background(), db))
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM vault_migrations WHERE name = '0005_session_names'`).Scan(&count))
	assert.Equal(t, 1, count, "migration rerun must not duplicate its record")
}

func TestMigrate0005_CreatesSessionNamesOnLegacyVault(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "legacy.db"))
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec(`
		PRAGMA foreign_keys = ON;
		CREATE TABLE vault_sessions (uuid TEXT PRIMARY KEY, content_hash TEXT NOT NULL, raw_jsonl BLOB NOT NULL);
		CREATE TABLE vault_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
		CREATE TABLE vault_migrations (name TEXT PRIMARY KEY, applied_at TEXT DEFAULT CURRENT_TIMESTAMP);
		INSERT INTO vault_sessions (uuid, content_hash, raw_jsonl) VALUES ('legacy', 'h', x'7b7d');
	`)
	require.NoError(t, err)

	require.NoError(t, migrate0005AddSessionNames(context.Background(), db))
	_, err = db.Exec(`INSERT INTO vault_session_names
		(session_uuid, custom_title, renamed_at_ns, machine_id)
		VALUES ('legacy', 'Legacy name', 1, 'machine-a')`)
	require.NoError(t, err)

	var count int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM vault_migrations WHERE name = '0005_session_names'`).Scan(&count))
	assert.Equal(t, 1, count)

	require.NoError(t, migrate0005AddSessionNames(context.Background(), db))
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM vault_migrations WHERE name = '0005_session_names'`).Scan(&count))
	assert.Equal(t, 1, count, "migration rerun must be idempotent")

	_, err = db.Exec(`DELETE FROM vault_sessions WHERE uuid = 'legacy'`)
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM vault_session_names`).Scan(&count))
	assert.Equal(t, 0, count, "legacy-migrated table must cascade with its parent")
}

func TestChunkFTSTokenizers(t *testing.T) {
	// Prove the two layers actually tokenize differently — a typo in the
	// tokenize= argument would otherwise surface only at search time (Task 5).
	s := newTestVault(t)
	db, err := s.getDB(context.Background())
	require.NoError(t, err)

	for _, table := range []string{"vault_chunks", "vault_chunks_trigram"} {
		//nolint:gosec // table is a test-controlled constant, never user input
		_, err = db.Exec(`INSERT INTO ` + table +
			` (title, content_text, session_uuid, subagent_id, chunk_index, first_line_index)
			VALUES ('t', 'authentication middleware', 'u1', '', 0, 0)`)
		require.NoError(t, err)
	}

	countMatches := func(table, query string) int {
		t.Helper()
		var cnt int
		//nolint:gosec // table is a test-controlled constant, never user input
		require.NoError(t, db.QueryRow(
			`SELECT COUNT(*) FROM `+table+` WHERE `+table+` MATCH ?`, query).Scan(&cnt))
		return cnt
	}

	// Porter layer stems: "authenticated" and "authentication" share a stem.
	assert.Equal(t, 1, countMatches("vault_chunks", `"authenticated"`),
		"porter layer must stem-match")
	// Trigram layer matches substrings; porter does not.
	assert.Equal(t, 1, countMatches("vault_chunks_trigram", `"thentica"`),
		"trigram layer must substring-match")
	assert.Equal(t, 0, countMatches("vault_chunks", `"thentica"`),
		"porter layer must not substring-match")
}

func TestColumnExists(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "c.db"))
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE t (a INTEGER, b TEXT)`)
	require.NoError(t, err)

	tx, err := db.Begin()
	require.NoError(t, err)
	defer tx.Rollback() //nolint:errcheck

	has, err := columnExists(context.Background(), tx, "t", "b")
	require.NoError(t, err)
	assert.True(t, has)

	has, err = columnExists(context.Background(), tx, "t", "missing")
	require.NoError(t, err)
	assert.False(t, has)
}
