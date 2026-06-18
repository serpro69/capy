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
	"net/url"
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

// EncryptedDSN builds a DSN with sqlite3mc URI-parameter encryption. The file:
// prefix ensures mattn/go-sqlite3 passes the full URI through to
// sqlite3_open_v2 (including the cipher/key params).
//
// This is the canonical home for the shared encryption DSN: it lives here rather
// than in internal/store because store imports sqliteutil, so sqliteutil cannot
// import store. store.EncryptedDSN is a thin wrapper that delegates here.
func EncryptedDSN(dbPath, passphrase string) string {
	return fmt.Sprintf("file:%s?cipher=sqlcipher&legacy=4&key=%s",
		uriEscapePath(dbPath), uriEscapePassphrase(passphrase))
}

// uriEscapePassphrase percent-encodes a passphrase for use in a SQLite URI.
// SQLite's URI parser follows RFC 3986, so spaces must be %20 (not +).
func uriEscapePassphrase(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// uriEscapePath escapes a file path for use in a SQLite URI by percent-encoding
// ? and # which have special meaning in URIs.
func uriEscapePath(s string) string {
	return strings.NewReplacer("?", "%3F", "#", "%23").Replace(s)
}

// RekeyResult reports the outcome of a successful Rekey. BackupPath is the
// pre-rotation database, preserved as <dbPath>.bak. The caller decides whether
// to keep it (a safety net) or remove it — when rotating a compromised key the
// .bak remains decryptable by the OLD key and is therefore a liability.
type RekeyResult struct {
	BackupPath string
}

// Rekey rotates the encryption key of an already-encrypted SQLite database using
// the SQLite backup API: open the source with oldKey, checkpoint it, backup-copy
// into a fresh temp database opened with newKey, then swap the temp file into
// place and verify it opens with newKey. Writing a brand-new file sidesteps the
// WAL/PRAGMA-rekey incompatibility (ADR-020) — no journal-mode dance needed.
//
// Rekey performs NO user-facing I/O: it returns a RekeyResult (carrying the .bak
// path) and wrapped errors so the calling command layer prints its own
// correctly-prefixed messages. On a swap/verify failure the original is rolled
// back from the .bak; if that rollback itself fails the returned error names the
// recovery path.
//
// The final swap is a filesystem rename, NOT mediated by SQLite locking, so the
// caller must ensure no other process is attached to the database. The
// checkpoint here is a best-effort flush on the old-key source connection, not a
// guard against an idle-but-attached process.
func Rekey(dbPath, oldKey, newKey string) (RekeyResult, error) {
	srcDB, err := openEncrypted(dbPath, oldKey)
	if err != nil {
		return RekeyResult{}, err
	}

	if err := Checkpoint(srcDB); err != nil {
		srcDB.Close()
		return RekeyResult{}, err
	}

	tmpPath := dbPath + ".enc.tmp"
	// Guarantee the temp DB is cleaned up on every failure path. On success
	// SwapAndVerify renames it into place, so this no-ops (ENOENT). tmpPath is
	// always distinct from the live DB and the .bak, so this can never delete
	// either.
	defer os.Remove(tmpPath)

	destDB, err := openEncrypted(tmpPath, newKey)
	if err != nil {
		srcDB.Close()
		return RekeyResult{}, fmt.Errorf("creating target database: %w", err)
	}

	if err := backupDB(destDB, srcDB); err != nil {
		destDB.Close()
		srcDB.Close()
		return RekeyResult{}, fmt.Errorf("backup API: %w", err)
	}
	destDB.Close()
	if err := srcDB.Close(); err != nil {
		return RekeyResult{}, fmt.Errorf("closing source database: %w", err)
	}

	bakPath, err := SwapAndVerify(dbPath, tmpPath, newKey)
	if err != nil {
		return RekeyResult{}, err
	}
	return RekeyResult{BackupPath: bakPath}, nil
}

// Checkpoint runs a TRUNCATE WAL checkpoint, failing if any pages are still busy
// (which indicates another connection is attached). The caller must hold the
// only connection for the checkpoint to fully flush.
func Checkpoint(db *sql.DB) error {
	// PRAGMA wal_checkpoint returns (busy, log, checkpointed). busy is a 0/1 flag
	// meaning the checkpoint could not complete because another connection holds a
	// lock — NOT a page count, so the message must not imply one.
	var busy, logFrames, checkpointed int
	if err := db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		return fmt.Errorf("checkpoint failed: %w", err)
	}
	if busy > 0 {
		return errors.New("checkpoint incomplete: database is busy (is the server still running?)")
	}
	return nil
}

// SwapAndVerify removes the live database's WAL/SHM sidecars, renames the
// original to <dbPath>.bak, moves the freshly-written tmpPath into place, and
// verifies it opens with newKey. On any failure it rolls the original back from
// the .bak. It performs NO user-facing I/O — on success it returns the .bak
// path; on failure it returns a wrapped error, and when the rollback ALSO fails
// the error names the .bak so the operator can recover manually.
func SwapAndVerify(dbPath, tmpPath, newKey string) (string, error) {
	// Clean up the temp DB on every failure path. On success tmpPath is renamed
	// into place below, so this no-ops (ENOENT). tmpPath is always distinct from
	// the live DB and the .bak, so it can never delete either. The dual-error
	// rollback messages wrap BOTH errors with %w (Go 1.20+) so a caller can
	// errors.Is/As either the operation failure or the rollback failure.
	defer os.Remove(tmpPath)

	for _, suffix := range []string{"-wal", "-shm"} {
		os.Remove(dbPath + suffix)
	}

	bakPath := dbPath + ".bak"
	if err := os.Rename(dbPath, bakPath); err != nil {
		return "", fmt.Errorf("backing up original: %w", err)
	}

	if err := os.Rename(tmpPath, dbPath); err != nil {
		if rerr := os.Rename(bakPath, dbPath); rerr != nil {
			return "", fmt.Errorf("moving new database into place failed (%w) and rollback failed (%w); manual recovery: original preserved at %s", err, rerr, bakPath)
		}
		return "", fmt.Errorf("moving new database into place: %w", err)
	}

	verifyDB, err := openEncrypted(dbPath, newKey)
	if err != nil {
		if rerr := os.Rename(dbPath, tmpPath); rerr != nil {
			return "", fmt.Errorf("verifying new database failed (%w) and could not move it aside (%w); manual recovery: original preserved at %s", err, rerr, bakPath)
		}
		if rerr := os.Rename(bakPath, dbPath); rerr != nil {
			return "", fmt.Errorf("verifying new database failed (%w) and rollback failed (%w); manual recovery: original preserved at %s", err, rerr, bakPath)
		}
		return "", fmt.Errorf("verifying new database failed, rolled back to original: %w", err)
	}
	verifyDB.Close()

	return bakPath, nil
}

// openEncrypted opens an sqlite3mc-encrypted database with key and runs the
// canary query so a wrong passphrase or corrupt file fails fast. The returned
// pool is capped at a single connection (rekey operations are strictly serial).
func openEncrypted(dbPath, key string) (*sql.DB, error) {
	dsn := EncryptedDSN(dbPath, key) + "&_busy_timeout=5000"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening encrypted database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("SELECT count(*) FROM sqlite_master"); err != nil {
		db.Close()
		return nil, fmt.Errorf("wrong passphrase or corrupted database: %w", err)
	}
	return db, nil
}

// backupDB copies the entire contents of srcDB into destDB via the SQLite online
// backup API. Both handles must already be open with their respective keys;
// destDB receives the data re-encrypted under its own key.
func backupDB(destDB, srcDB *sql.DB) error {
	destConn, err := destDB.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("getting dest connection: %w", err)
	}
	defer destConn.Close()

	srcConn, err := srcDB.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("getting src connection: %w", err)
	}
	defer srcConn.Close()

	return destConn.Raw(func(destRaw any) error {
		return srcConn.Raw(func(srcRaw any) error {
			destSC := destRaw.(*sqlite3.SQLiteConn)
			srcSC := srcRaw.(*sqlite3.SQLiteConn)

			backup, err := destSC.Backup("main", srcSC, "main")
			if err != nil {
				return fmt.Errorf("starting backup: %w", err)
			}
			_, err = backup.Step(-1)
			finishErr := backup.Finish()
			if err != nil {
				return fmt.Errorf("backup step: %w", err)
			}
			if finishErr != nil {
				return fmt.Errorf("backup finish: %w", finishErr)
			}
			return nil
		})
	})
}
