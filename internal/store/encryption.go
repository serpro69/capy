package store

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
)

// errWrongPassphrase wraps the underlying SQLite error when the canary query
// fails after opening an encrypted DB. Kept separate from corruption errors
// so that getDB() doesn't trigger the backup-and-recreate recovery path.
type errWrongPassphrase struct{ wrapped error }

func (e *errWrongPassphrase) Error() string {
	return fmt.Sprintf("wrong passphrase or corrupted database (check %s): %v", encryptionKeyEnv, e.wrapped)
}
func (e *errWrongPassphrase) Unwrap() error { return e.wrapped }

func isWrongPassphrase(err error) bool {
	var wp *errWrongPassphrase
	return errors.As(err, &wp)
}

// isGarbageFile returns true if the file at path is clearly not a SQLite
// database — too small to contain even one page. sqlite3mc with sqlcipher
// uses a minimum page size of 512 bytes; any valid encrypted DB is at
// least that large. Files smaller than 512 bytes are garbage.
func isGarbageFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Size() > 0 && info.Size() < 512
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
