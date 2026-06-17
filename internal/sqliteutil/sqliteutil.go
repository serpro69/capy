// Package sqliteutil holds the SQLite open and recovery logic shared by the
// knowledge store (internal/store) and the vault (internal/vault).
//
// It owns the canary query that proves an encrypted database can be read, the
// classification of open failures (corruption vs. wrong passphrase vs. an
// unencrypted file), and the corrupt-file backup helper. Both stores must
// agree on this classification: the wrong-passphrase and unencrypted-DB error
// types are constructed here so either package can recognise them with the
// exported predicates, which exported predicates over store's unexported types
// could not have done.
package sqliteutil

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"
)

// WrongPassphraseError wraps the underlying SQLite error when the canary query
// fails on an encrypted DB. It is kept distinct from a plain corruption error
// so callers can decline backup-and-recreate recovery on a likely key typo.
// KeyEnv names the passphrase env var so the message points at the right one.
type WrongPassphraseError struct {
	Wrapped error
	KeyEnv  string
}

func (e *WrongPassphraseError) Error() string {
	return fmt.Sprintf("wrong passphrase or corrupted database (check %s): %v", e.KeyEnv, e.Wrapped)
}

func (e *WrongPassphraseError) Unwrap() error { return e.Wrapped }

// IsWrongPassphrase reports whether err is, or wraps, an *WrongPassphraseError.
func IsWrongPassphrase(err error) bool {
	var wp *WrongPassphraseError
	return errors.As(err, &wp)
}

// sqliteHeaderMagic is the 15-byte plaintext header of an unencrypted SQLite DB.
var sqliteHeaderMagic = []byte("SQLite format 3")

// UnencryptedDBError is returned when an existing DB file is a plaintext SQLite
// database rather than an encrypted one — i.e. the file predates encryption.
type UnencryptedDBError struct{ Path string }

func (e *UnencryptedDBError) Error() string {
	return fmt.Sprintf("database at %s is not encrypted — run 'capy encrypt' first", e.Path)
}

// IsUnencryptedDB reports whether the file at path begins with the plaintext
// SQLite header magic, i.e. it is an unencrypted database.
func IsUnencryptedDB(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	header := make([]byte, 15)
	if _, err := io.ReadFull(f, header); err != nil {
		return false
	}
	return bytes.Equal(header, sqliteHeaderMagic)
}

// IsGarbageFile returns true if the file at path is clearly not a SQLite
// database — too small to contain even one page. sqlite3mc with sqlcipher uses
// a minimum page size of 512 bytes; any valid encrypted DB is at least that
// large. Non-empty files smaller than 512 bytes are garbage.
func IsGarbageFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Size() > 0 && info.Size() < 512
}

// IsSQLiteCorruption reports whether err indicates a corrupt or not-a-database
// condition (SQLITE_CORRUPT / SQLITE_NOTADB, or the equivalent message text
// when the typed error is unavailable).
func IsSQLiteCorruption(err error) bool {
	if err == nil {
		return false
	}
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) {
		return sqliteErr.Code == sqlite3.ErrCorrupt || sqliteErr.Code == sqlite3.ErrNotADB
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "malformed") ||
		strings.Contains(msg, "not a database") ||
		strings.Contains(msg, "corrupt")
}

// BackupCorruptDB renames a corrupt DB file and its WAL/SHM sidecars aside with
// a timestamped .corrupt suffix so a fresh DB can be recreated in place.
func BackupCorruptDB(dbPath string) {
	ts := time.Now().Format("20060102T150405")
	suffix := fmt.Sprintf(".corrupt.%s", ts)

	for _, ext := range []string{"", "-wal", "-shm"} {
		src := dbPath + ext
		dst := src + suffix
		if err := os.Rename(src, dst); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			slog.Warn("failed to back up corrupt DB file", "src", src, "error", err)
			continue
		}
		slog.Warn("backed up corrupt DB file", "src", src, "dst", dst)
	}
}

// BeginImmediate starts a transaction that holds SQLite's RESERVED write lock for
// its entire lifetime, matching the guarantee of a literal `BEGIN IMMEDIATE`
// without issuing that statement directly. database/sql's Begin() uses
// BEGIN DEFERRED, so we upgrade to a write transaction with a no-op write against
// lockTable — SQLite acquires the RESERVED lock on the first write of a DEFERRED
// tx. A concurrent writer then blocks until commit instead of failing outright.
//
// lockTable names a table to issue the no-op `DELETE … WHERE 0` against; it MUST
// be a trusted internal constant (e.g. "sources", "vault_meta"), never user input
// — it is interpolated into the statement, not parameterized (a table name cannot
// be a bind parameter). The table must already exist when this is called.
//
// Retries on SQLITE_BUSY with exponential backoff because database/sql's BeginTx
// can surface "database is locked" before the connection-level busy_timeout
// engages under goroutine contention.
//
// BeginImmediate is the contextless entry point for callers without one (e.g.
// the knowledge store, whose ctx propagation is deliberately not done — see
// docs/wip/vault/v2 Task 3). It delegates to BeginImmediateContext with a
// context.Background(). Callers that hold a context should use
// BeginImmediateContext so cancellation reaches BeginTx/ExecContext.
func BeginImmediate(db *sql.DB, lockTable string) (*sql.Tx, error) {
	return BeginImmediateContext(context.Background(), db, lockTable)
}

// BeginImmediateContext is the context-aware variant of BeginImmediate: ctx
// cancels the transaction start (BeginTx), the lock-acquiring no-op write
// (ExecContext), AND the inter-retry backoff wait. Because the backoff doubles
// each retry (10ms → ~5s on the last of 10 attempts), a cancelled caller that is
// mid-backoff would otherwise wait out a multi-second sleep before the next
// BeginTx noticed the cancellation; waitBackoff returns ctx.Err() promptly
// instead. The contextless BeginImmediate is unaffected — context.Background()
// never fires Done, so waitBackoff always waits the full interval there.
func BeginImmediateContext(ctx context.Context, db *sql.DB, lockTable string) (*sql.Tx, error) {
	const maxRetries = 10
	backoff := 10 * time.Millisecond

	noop := fmt.Sprintf("DELETE FROM %s WHERE 0", lockTable) //nolint:gosec // lockTable is a trusted internal constant, never user input

	for i := range maxRetries {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			if IsBusy(err) && i < maxRetries-1 {
				if werr := waitBackoff(ctx, backoff); werr != nil {
					return nil, werr
				}
				backoff *= 2
				continue
			}
			return nil, err
		}

		// Force RESERVED-lock acquisition via a no-op write.
		if _, err := tx.ExecContext(ctx, noop); err != nil {
			tx.Rollback() //nolint:errcheck
			if IsBusy(err) && i < maxRetries-1 {
				if werr := waitBackoff(ctx, backoff); werr != nil {
					return nil, werr
				}
				backoff *= 2
				continue
			}
			return nil, err
		}
		return tx, nil
	}
	return nil, fmt.Errorf("could not acquire write lock after %d retries", maxRetries)
}

// waitBackoff sleeps for d, returning early with ctx.Err() if ctx is cancelled
// first. With a context that never cancels (context.Background) it is equivalent
// to time.Sleep(d).
func waitBackoff(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// IsBusy reports whether err is a SQLITE_BUSY / SQLITE_LOCKED condition.
// Preferred path: typed sqlite3.Error code match. The string fallback catches
// errors wrapped in ways that strip the typed error (e.g. some database/sql
// paths return a bare error before reaching the driver error layer).
func IsBusy(err error) bool {
	if err == nil {
		return false
	}
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) {
		return sqliteErr.Code == sqlite3.ErrBusy || sqliteErr.Code == sqlite3.ErrLocked
	}
	msg := err.Error()
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database table is locked")
}

// OpenWithCanary opens the SQLite database described by dsn and verifies it can
// be read with the configured key by running a canary query against
// sqlite_master. dbPath is the on-disk path, used to tell an unencrypted DB
// apart from a wrong passphrase; keyEnv names the passphrase env var for the
// wrong-passphrase error message.
//
// On canary failure the error is classified:
//   - corruption that is also a plaintext file → *UnencryptedDBError
//   - corruption otherwise → *WrongPassphraseError (wraps the SQLite error, so
//     IsSQLiteCorruption still reports true and garbage-file recovery proceeds)
//   - any other canary failure → a wrapped "canary query failed" error
//
// The caller owns pragmas, schema creation, migrations, and statement prep on
// the returned *sql.DB.
func OpenWithCanary(ctx context.Context, dsn, dbPath, keyEnv string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	if _, err := db.ExecContext(ctx, "SELECT count(*) FROM sqlite_master"); err != nil {
		db.Close()
		if IsSQLiteCorruption(err) {
			if IsUnencryptedDB(dbPath) {
				return nil, &UnencryptedDBError{Path: dbPath}
			}
			return nil, &WrongPassphraseError{Wrapped: err, KeyEnv: keyEnv}
		}
		return nil, fmt.Errorf("canary query failed: %w", err)
	}

	return db, nil
}
