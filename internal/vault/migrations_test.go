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
