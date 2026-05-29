package sqliteutil

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// encryptedDSN builds a sqlite3mc/sqlcipher DSN inline so these tests stay
// independent of internal/store (which imports this package).
func encryptedDSN(path, key string) string {
	return fmt.Sprintf("file:%s?cipher=sqlcipher&legacy=4&key=%s", path, key)
}

func TestIsSQLiteCorruption(t *testing.T) {
	assert.True(t, IsSQLiteCorruption(fmt.Errorf("file is not a database")))
	assert.True(t, IsSQLiteCorruption(fmt.Errorf("database disk image is malformed")))
	assert.True(t, IsSQLiteCorruption(fmt.Errorf("database or disk is corrupt")))
	assert.False(t, IsSQLiteCorruption(fmt.Errorf("database is locked")))
	assert.False(t, IsSQLiteCorruption(nil))
}

func TestIsGarbageFile(t *testing.T) {
	dir := t.TempDir()

	t.Run("nonexistent", func(t *testing.T) {
		assert.False(t, IsGarbageFile(filepath.Join(dir, "nope.db")))
	})

	t.Run("empty", func(t *testing.T) {
		p := filepath.Join(dir, "empty.db")
		require.NoError(t, os.WriteFile(p, []byte{}, 0o644))
		assert.False(t, IsGarbageFile(p))
	})

	t.Run("too_small", func(t *testing.T) {
		p := filepath.Join(dir, "small.db")
		require.NoError(t, os.WriteFile(p, []byte("too short"), 0o644))
		assert.True(t, IsGarbageFile(p))
	})

	t.Run("page_sized", func(t *testing.T) {
		p := filepath.Join(dir, "big.db")
		require.NoError(t, os.WriteFile(p, make([]byte, 512), 0o644))
		assert.False(t, IsGarbageFile(p))
	})
}

func TestIsUnencryptedDB(t *testing.T) {
	dir := t.TempDir()

	t.Run("plaintext_sqlite", func(t *testing.T) {
		dbPath := filepath.Join(dir, "plain.db")
		db, err := sql.Open("sqlite3", dbPath)
		require.NoError(t, err)
		_, err = db.Exec("CREATE TABLE t (id INTEGER)")
		require.NoError(t, err)
		db.Close()
		assert.True(t, IsUnencryptedDB(dbPath))
	})

	t.Run("encrypted_sqlite", func(t *testing.T) {
		dbPath := filepath.Join(dir, "encrypted.db")
		db, err := sql.Open("sqlite3", encryptedDSN(dbPath, "test-passphrase-at-least-32-characters!!"))
		require.NoError(t, err)
		_, err = db.Exec("CREATE TABLE t (id INTEGER)")
		require.NoError(t, err)
		db.Close()
		assert.False(t, IsUnencryptedDB(dbPath))
	})

	t.Run("nonexistent_file", func(t *testing.T) {
		assert.False(t, IsUnencryptedDB(filepath.Join(dir, "nope.db")))
	})

	t.Run("empty_file", func(t *testing.T) {
		dbPath := filepath.Join(dir, "empty.db")
		require.NoError(t, os.WriteFile(dbPath, []byte{}, 0o644))
		assert.False(t, IsUnencryptedDB(dbPath))
	})

	t.Run("short_file", func(t *testing.T) {
		dbPath := filepath.Join(dir, "short.db")
		require.NoError(t, os.WriteFile(dbPath, []byte("too short"), 0o644))
		assert.False(t, IsUnencryptedDB(dbPath))
	})
}

func TestUnencryptedDBError(t *testing.T) {
	err := &UnencryptedDBError{Path: "/tmp/test.db"}
	assert.Contains(t, err.Error(), "not encrypted")
	assert.Contains(t, err.Error(), "capy encrypt")
}

func TestWrongPassphraseError(t *testing.T) {
	wrapped := fmt.Errorf("file is not a database")
	err := &WrongPassphraseError{Wrapped: wrapped, KeyEnv: "CAPY_DB_KEY"}

	assert.Contains(t, err.Error(), "wrong passphrase")
	assert.Contains(t, err.Error(), "CAPY_DB_KEY")
	assert.True(t, IsWrongPassphrase(err))
	assert.ErrorIs(t, err, wrapped)
	// A wrapped corruption error must remain classifiable as corruption so that
	// garbage-file recovery in getDB() still proceeds.
	assert.True(t, IsSQLiteCorruption(err))
	assert.False(t, IsWrongPassphrase(fmt.Errorf("some other error")))
}

func TestOpenWithCanary_Encrypted_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "round.db")
	const key = "test-passphrase-at-least-32-characters!!"
	dsn := encryptedDSN(dbPath, key)

	db, err := OpenWithCanary(context.Background(), dsn, dbPath, "CAPY_DB_KEY")
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE t (id INTEGER)")
	require.NoError(t, err)
	require.NoError(t, db.Close())

	// Reopen with the same key succeeds.
	db2, err := OpenWithCanary(context.Background(), dsn, dbPath, "CAPY_DB_KEY")
	require.NoError(t, err)
	require.NoError(t, db2.Close())
}

func TestOpenWithCanary_WrongPassphrase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "wrongkey.db")

	db, err := OpenWithCanary(context.Background(), encryptedDSN(dbPath, "correct-passphrase-at-least-32-characters!!"), dbPath, "CAPY_DB_KEY")
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE t (id INTEGER)")
	require.NoError(t, err)
	require.NoError(t, db.Close())

	_, err = OpenWithCanary(context.Background(), encryptedDSN(dbPath, "different-passphrase-at-least-32-characters!!"), dbPath, "CAPY_DB_KEY")
	require.Error(t, err)
	assert.True(t, IsWrongPassphrase(err), "expected ErrWrongPassphrase, got: %v", err)
}

func TestOpenWithCanary_UnencryptedDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "plain.db")

	// Create a plaintext SQLite DB, then try to open it as encrypted.
	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE t (id INTEGER)")
	require.NoError(t, err)
	require.NoError(t, db.Close())

	_, err = OpenWithCanary(context.Background(), encryptedDSN(dbPath, "some-passphrase-at-least-32-characters!!"), dbPath, "CAPY_DB_KEY")
	require.Error(t, err)
	var unencErr *UnencryptedDBError
	assert.ErrorAs(t, err, &unencErr, "expected UnencryptedDBError, got: %v", err)
}
