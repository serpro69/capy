package store

import (
	"database/sql"
	"fmt"
	"time"
)

// applyMigrations runs all pending one-shot migrations against db.
// Each migration is idempotent: re-running on an already-migrated DB is a no-op.
//
// Currently covers:
//   - 017_add_source_kind: adds the `kind` column to the sources table and
//     retroactively tags ephemeral rows by label prefix.
// TODO: add a migrations-tracking table when the second migration lands;
// until then, per-migration PRAGMA table_info checks provide idempotency.
func applyMigrations(db *sql.DB) error {
	if err := migrate017AddSourceKind(db); err != nil {
		return fmt.Errorf("migration 017_add_source_kind: %w", err)
	}
	return nil
}

// migrate017AddSourceKind adds the `kind` column to the sources table.
// Idempotency: PRAGMA table_info detects whether the column already exists.
// Concurrency: beginImmediate acquires the RESERVED write lock eagerly so a
// second concurrent caller blocks until we commit, then its PRAGMA table_info
// sees the column and returns early.
func migrate017AddSourceKind(db *sql.DB) error {
	// Acquire the write lock eagerly via beginImmediate (see its doc for the
	// dummy-write mechanism). Retries on SQLITE_BUSY with exponential backoff
	// because database/sql's BeginTx can surface "database is locked" before
	// the connection-level busy_timeout kicks in under goroutine contention.
	tx, err := beginImmediate(db)
	if err != nil {
		return fmt.Errorf("begin immediate: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	// Check if the kind column already exists.
	rows, err := tx.Query("PRAGMA table_info(sources)")
	if err != nil {
		return fmt.Errorf("pragma table_info: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dfltValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dfltValue, &pk); err != nil {
			return fmt.Errorf("scanning table_info: %w", err)
		}
		if name == "kind" {
			// Already migrated — nothing to do.
			rows.Close() // release cursor before committing; safe to double-close via defer
			return tx.Commit()
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating table_info: %w", err)
	}

	// Add the column. Note: SQLite's ALTER TABLE ADD COLUMN does not support
	// CHECK constraints, so migrated DBs lack the CHECK (kind IN ('ephemeral', 'durable'))
	// that fresh DBs get from schemaSQL. Write-path validation in Go (Task 3) guards
	// against invalid values at the application layer.
	if _, err := tx.Exec(`ALTER TABLE sources ADD COLUMN kind TEXT NOT NULL DEFAULT 'durable'`); err != nil {
		return fmt.Errorf("alter table: %w", err)
	}

	// Retroactively tag ephemeral rows by label prefix. Contract: every
	// ephemeral source labeled pre-migration was written by capy_execute /
	// capy_execute_file / capy_batch_execute, which use the `execute:`, `file:`,
	// and `batch:` prefixes exclusively. If a future ephemeral source type
	// introduces a new prefix, extend this list AND add a fresh one-shot
	// migration — don't retro-edit this one (it has already run on installed
	// DBs).
	if _, err := tx.Exec(`
		UPDATE sources SET kind = 'ephemeral'
		WHERE label LIKE 'execute:%'
		   OR label LIKE 'file:%'
		   OR label LIKE 'batch:%'`); err != nil {
		return fmt.Errorf("retroactive update: %w", err)
	}

	return tx.Commit()
}

// beginImmediate starts a transaction that holds SQLite's RESERVED write lock
// for its entire lifetime, matching the guarantee of a literal `BEGIN IMMEDIATE`
// without issuing that statement directly. database/sql's Begin() uses
// BEGIN DEFERRED, so we upgrade to a write transaction by executing a no-op
// DELETE immediately after — SQLite acquires the RESERVED lock on the first
// write of a DEFERRED tx. This matches the idiom used by Index (index.go).
//
// Retries on SQLITE_BUSY with exponential backoff because database/sql's
// BeginTx can surface "database is locked" before the connection-level
// busy_timeout kicks in when goroutines contend for the write lock.
func beginImmediate(db *sql.DB) (*sql.Tx, error) {
	const maxRetries = 10
	backoff := 10 * time.Millisecond

	for i := range maxRetries {
		tx, err := db.Begin()
		if err != nil {
			if isBusy(err) && i < maxRetries-1 {
				time.Sleep(backoff)
				backoff *= 2
				continue
			}
			return nil, err
		}

		// Force RESERVED-lock acquisition via a no-op write.
		_, err = tx.Exec("DELETE FROM sources WHERE 0")
		if err != nil {
			tx.Rollback() //nolint:errcheck
			if isBusy(err) && i < maxRetries-1 {
				time.Sleep(backoff)
				backoff *= 2
				continue
			}
			return nil, err
		}
		return tx, nil
	}
	return nil, fmt.Errorf("could not acquire write lock after %d retries", maxRetries)
}

