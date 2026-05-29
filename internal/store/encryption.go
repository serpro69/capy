package store

import (
	"fmt"
	"log/slog"
	"net/url"
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

// URIEscapePassphrase percent-encodes a passphrase for use in a SQLite URI.
// SQLite's URI parser follows RFC 3986, so spaces must be %20 (not +).
func URIEscapePassphrase(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// URIEscapePath escapes a file path for use in a SQLite URI by
// percent-encoding ? and # which have special meaning in URIs.
func URIEscapePath(s string) string {
	return strings.NewReplacer("?", "%3F", "#", "%23").Replace(s)
}

// EncryptedDSN builds a DSN with sqlite3mc URI-parameter encryption.
// The file: prefix ensures mattn/go-sqlite3 passes the full URI through
// to sqlite3_open_v2 (including cipher/key params).
func EncryptedDSN(dbPath, passphrase string) string {
	return fmt.Sprintf("file:%s?cipher=sqlcipher&legacy=4&key=%s",
		URIEscapePath(dbPath), URIEscapePassphrase(passphrase))
}

// EscapeSQLString escapes a string for use in a SQL single-quoted literal
// by doubling all single quotes. Used by capy encrypt for PRAGMA rekey.
func EscapeSQLString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
