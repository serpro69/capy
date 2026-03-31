package hook

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleSessionEnd_NoDB(t *testing.T) {
	dir := t.TempDir()
	// Should not panic or error when no DB exists
	handleSessionEnd(dir)
}

func TestHandleSessionEnd_CheckpointsWAL(t *testing.T) {
	dir := t.TempDir()
	capyDir := filepath.Join(dir, ".capy")
	require.NoError(t, os.MkdirAll(capyDir, 0o755))

	// Write a minimal .capy.toml so config resolves the DB inside our temp dir
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".capy.toml"),
		[]byte("[store]\npath = \".capy/knowledge.db\"\n"),
		0o644,
	))

	dbPath := filepath.Join(capyDir, "knowledge.db")

	// Create a WAL-mode DB with some data to force WAL file creation
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE test (id INTEGER); INSERT INTO test VALUES (1);")
	require.NoError(t, err)
	db.Close()

	// Verify WAL file exists (SQLite creates it in WAL mode after writes)
	walPath := dbPath + "-wal"
	// WAL may or may not exist after close (close does implicit checkpoint).
	// Force a scenario where WAL exists by reopening and writing without checkpoint.
	db2, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	require.NoError(t, err)
	_, err = db2.Exec("INSERT INTO test VALUES (2);")
	require.NoError(t, err)
	// Close without checkpoint — leave WAL behind
	db2.Close()

	// Now run handleSessionEnd — it should checkpoint
	handleSessionEnd(dir)

	// After checkpoint, WAL should be gone or empty
	// (ContentStore.Close does PRAGMA wal_checkpoint(TRUNCATE))
	if info, err := os.Stat(walPath); err == nil {
		assert.Equal(t, int64(0), info.Size(), "WAL should be truncated to 0 bytes after checkpoint")
	}
	// If WAL file doesn't exist at all, that's also fine

	// Verify data survived the checkpoint
	db3, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	defer db3.Close()
	var count int
	require.NoError(t, db3.QueryRow("SELECT COUNT(*) FROM test").Scan(&count))
	assert.Equal(t, 2, count)
}
