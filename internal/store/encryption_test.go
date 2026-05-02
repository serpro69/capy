package store

import (
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
