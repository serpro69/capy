package store

import (
	"database/sql"
	"fmt"
	"strings"
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
// Concurrency: BEGIN IMMEDIATE acquires the write lock eagerly so a second
// process blocks until the first commits, then re-checks and returns early.
func migrate017AddSourceKind(db *sql.DB) error {
	// go-sqlite3 maps LevelSerializable to BEGIN IMMEDIATE, which acquires the
	// write lock eagerly. A second concurrent caller blocks until we commit,
	// then its PRAGMA table_info sees the column and returns early.
	//
	// Retry on SQLITE_BUSY: database/sql's BeginTx may return "database is locked"
	// before the connection-level busy_timeout applies (the lock contention happens
	// at the BEGIN statement itself). We retry with backoff for up to ~5s total.
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

	// Retroactively tag ephemeral rows by label prefix.
	if _, err := tx.Exec(`
		UPDATE sources SET kind = 'ephemeral'
		WHERE label LIKE 'execute:%'
		   OR label LIKE 'file:%'
		   OR label LIKE 'batch:%'`); err != nil {
		return fmt.Errorf("retroactive update: %w", err)
	}

	return tx.Commit()
}

// beginImmediate starts a transaction with BEGIN IMMEDIATE semantics, retrying
// on SQLITE_BUSY with exponential backoff. database/sql's BeginTx can surface
// "database is locked" before the connection-level busy_timeout kicks in when
// multiple goroutines contend for BEGIN IMMEDIATE on the same *sql.DB pool.
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

		// Upgrade to a write transaction by executing a dummy write.
		// database/sql's Begin() uses BEGIN DEFERRED; this forces the
		// RESERVED lock acquisition that BEGIN IMMEDIATE would provide.
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

func isBusy(err error) bool {
	return err != nil && strings.Contains(err.Error(), "database is locked")
}
