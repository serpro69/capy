package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequireEncryptionKey_Empty(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "")
	_, err := RequireEncryptionKey()
	require.Error(t, err)
	assert.Contains(t, err.Error(), encryptionKeyEnv)
	assert.Contains(t, err.Error(), "required")
}

func TestRequireEncryptionKey_Short(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "short-key")
	key, err := RequireEncryptionKey()
	require.NoError(t, err)
	assert.Equal(t, "short-key", key)
}

func TestRequireEncryptionKey_Valid(t *testing.T) {
	long := "this-is-a-passphrase-that-is-at-least-32-characters"
	t.Setenv(encryptionKeyEnv, long)
	key, err := RequireEncryptionKey()
	require.NoError(t, err)
	assert.Equal(t, long, key)
}

func TestEncryptionKeyFromEnv_Unset(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "")
	assert.Equal(t, "", EncryptionKeyFromEnv())
}

func TestEncryptionKeyFromEnv_Set(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "my-key")
	assert.Equal(t, "my-key", EncryptionKeyFromEnv())
}

func TestURIEscapePassphrase(t *testing.T) {
	assert.Equal(t, "simple", URIEscapePassphrase("simple"))
	assert.Equal(t, "has%20space", URIEscapePassphrase("has space"))
	assert.Equal(t, "has%26amp", URIEscapePassphrase("has&amp"))
	assert.Equal(t, "has%3Dequals", URIEscapePassphrase("has=equals"))
	assert.Equal(t, "has%25percent", URIEscapePassphrase("has%percent"))
	assert.Equal(t, "has%2Bplus", URIEscapePassphrase("has+plus"))
}

func TestEncryptedDSN(t *testing.T) {
	dsn := EncryptedDSN("/tmp/test.db", "my passphrase")
	assert.Equal(t, "file:/tmp/test.db?cipher=sqlcipher&legacy=4&key=my%20passphrase", dsn)
}

func TestEncryptedDSN_SpecialChars(t *testing.T) {
	dsn := EncryptedDSN("/tmp/test.db", "pass'phrase&with=special+chars")
	assert.Equal(t,
		"file:/tmp/test.db?cipher=sqlcipher&legacy=4&key=pass%27phrase%26with%3Dspecial%2Bchars",
		dsn)
}

func TestEncryptedDSN_PathWithSpecialChars(t *testing.T) {
	dsn := EncryptedDSN("/tmp/path with spaces/test#1.db", "key")
	assert.Equal(t,
		"file:/tmp/path with spaces/test%231.db?cipher=sqlcipher&legacy=4&key=key",
		dsn)

	dsn2 := EncryptedDSN("/tmp/path?query/test.db", "key")
	assert.Equal(t,
		"file:/tmp/path%3Fquery/test.db?cipher=sqlcipher&legacy=4&key=key",
		dsn2)
}

func TestEscapeSQLString(t *testing.T) {
	assert.Equal(t, "no quotes", EscapeSQLString("no quotes"))
	assert.Equal(t, "it''s escaped", EscapeSQLString("it's escaped"))
	assert.Equal(t, "double''''quote", EscapeSQLString("double''quote"))
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
		assert.True(t, isUnencryptedDB(dbPath))
	})

	t.Run("encrypted_sqlite", func(t *testing.T) {
		dbPath := filepath.Join(dir, "encrypted.db")
		dsn := EncryptedDSN(dbPath, "test-passphrase-at-least-32-characters!!")
		db, err := sql.Open("sqlite3", dsn)
		require.NoError(t, err)
		_, err = db.Exec("CREATE TABLE t (id INTEGER)")
		require.NoError(t, err)
		db.Close()
		assert.False(t, isUnencryptedDB(dbPath))
	})

	t.Run("nonexistent_file", func(t *testing.T) {
		assert.False(t, isUnencryptedDB(filepath.Join(dir, "nope.db")))
	})

	t.Run("empty_file", func(t *testing.T) {
		dbPath := filepath.Join(dir, "empty.db")
		require.NoError(t, os.WriteFile(dbPath, []byte{}, 0o644))
		assert.False(t, isUnencryptedDB(dbPath))
	})

	t.Run("short_file", func(t *testing.T) {
		dbPath := filepath.Join(dir, "short.db")
		require.NoError(t, os.WriteFile(dbPath, []byte("too short"), 0o644))
		assert.False(t, isUnencryptedDB(dbPath))
	})
}

func TestErrUnencryptedDB(t *testing.T) {
	err := &errUnencryptedDB{path: "/tmp/test.db"}
	assert.Contains(t, err.Error(), "not encrypted")
	assert.Contains(t, err.Error(), "capy encrypt")
}

func TestOpenDB_UnencryptedDB_ClearError(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "unencrypted.db")

	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE t (id INTEGER)")
	require.NoError(t, err)
	db.Close()

	t.Setenv(encryptionKeyEnv, "test-passphrase-at-least-32-characters!!")
	s := NewContentStore(dbPath, dir, 0)
	defer s.Close()

	_, err = s.SearchWithFallback("anything", 5, SearchOptions{})
	require.Error(t, err)

	var unencErr *errUnencryptedDB
	assert.ErrorAs(t, err, &unencErr, "should be errUnencryptedDB, got: %v", err)
	assert.Contains(t, err.Error(), "not encrypted")
	assert.Contains(t, err.Error(), "capy encrypt")
}

func TestValidateEncryptionReady(t *testing.T) {
	dir := t.TempDir()

	t.Run("no_key", func(t *testing.T) {
		t.Setenv(encryptionKeyEnv, "")
		err := ValidateEncryptionReady(filepath.Join(dir, "any.db"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), encryptionKeyEnv)
	})

	t.Run("key_set_no_db", func(t *testing.T) {
		t.Setenv(encryptionKeyEnv, "test-passphrase-at-least-32-characters!!")
		err := ValidateEncryptionReady(filepath.Join(dir, "nonexistent.db"))
		assert.NoError(t, err)
	})

	t.Run("key_set_encrypted_db", func(t *testing.T) {
		t.Setenv(encryptionKeyEnv, "test-passphrase-at-least-32-characters!!")
		dbPath := filepath.Join(dir, "encrypted.db")
		db, err := sql.Open("sqlite3", EncryptedDSN(dbPath, "test-passphrase-at-least-32-characters!!"))
		require.NoError(t, err)
		_, err = db.Exec("CREATE TABLE t (id INTEGER)")
		require.NoError(t, err)
		db.Close()

		assert.NoError(t, ValidateEncryptionReady(dbPath))
	})

	t.Run("key_set_unencrypted_db", func(t *testing.T) {
		t.Setenv(encryptionKeyEnv, "test-passphrase-at-least-32-characters!!")
		dbPath := filepath.Join(dir, "plain.db")
		db, err := sql.Open("sqlite3", dbPath)
		require.NoError(t, err)
		_, err = db.Exec("CREATE TABLE t (id INTEGER)")
		require.NoError(t, err)
		db.Close()

		err = ValidateEncryptionReady(dbPath)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not encrypted")
	})
}
