package store

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/serpro69/capy/internal/sqliteutil"
)

// ValidateEncryptionReady checks that CAPY_DB_KEY is set and, if the DB file
// already exists, that it is actually encrypted. Returns nil if the DB does
// not exist yet (it will be created encrypted on first use).
func ValidateEncryptionReady(dbPath string) error {
	if _, err := RequireEncryptionKey(); err != nil {
		return err
	}
	if sqliteutil.IsUnencryptedDB(dbPath) {
		return &sqliteutil.UnencryptedDBError{Path: dbPath}
	}
	return nil
}

const encryptionKeyEnv = "CAPY_DB_KEY"

const MinPassphraseLength = 32

// RequireEncryptionKey reads CAPY_DB_KEY from the environment.
// Returns an error if the key is empty. Logs a warning if the key
// is shorter than MinPassphraseLength.
func RequireEncryptionKey() (string, error) {
	key := os.Getenv(encryptionKeyEnv)
	if key == "" {
		return "", fmt.Errorf("%s environment variable is required (see: capy encrypt --help)", encryptionKeyEnv)
	}
	if len(key) < MinPassphraseLength {
		slog.Warn("encryption passphrase is short — 32+ characters recommended",
			"length", len(key))
	}
	return key, nil
}

// EncryptionKeyFromEnv reads CAPY_DB_KEY from the environment and returns it.
// Returns an empty string if unset. Used by `capy encrypt` which has its own
// fallback to interactive prompting.
func EncryptionKeyFromEnv() string {
	return os.Getenv(encryptionKeyEnv)
}

// EncryptedDSN builds a DSN with sqlite3mc URI-parameter encryption.
//
// It delegates to sqliteutil.EncryptedDSN, the canonical home for the shared
// SQLite encryption DSN; it is retained here so existing store/vault/cmd callers
// need no change. (sqliteutil cannot live under store — store imports it — so
// the builder had to move down to break the Rekey extraction's import cycle.)
//
// TODO(vault-v2 Task 7 follow-up, optional): migrate the remaining callers
// (internal/store, internal/vault, cmd/capy) to sqliteutil.EncryptedDSN directly
// and drop this wrapper.
func EncryptedDSN(dbPath, passphrase string) string {
	return sqliteutil.EncryptedDSN(dbPath, passphrase)
}

// EscapeSQLString escapes a string for use in a SQL single-quoted literal
// by doubling all single quotes. Used by capy encrypt for PRAGMA rekey.
func EscapeSQLString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
