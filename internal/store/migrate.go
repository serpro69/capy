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
// Covers:
//   - 017_add_source_kind: adds the `kind` column to the sources table and
//     retroactively tags ephemeral rows by label prefix.
//   - 018_add_session_kind: adds a migration-tracking table and extends the
//     CHECK constraint to accept 'session' as a valid kind value.
func applyMigrations(db *sql.DB) error {
	if err := migrate017AddSourceKind(db); err != nil {
		return fmt.Errorf("migration 017_add_source_kind: %w", err)
	}
	if err := ensureMigrationsTable(db); err != nil {
		return fmt.Errorf("creating migrations table: %w", err)
	}
	if err := migrate018AddSessionKind(db); err != nil {
		return fmt.Errorf("migration 018_add_session_kind: %w", err)
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

// ensureMigrationsTable creates the migration-tracking table and retroactively
// records migration 017 (which used PRAGMA-based idempotency checks). Future
// migrations use this table for idempotency instead.
func ensureMigrationsTable(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS migrations (
			name TEXT PRIMARY KEY,
			applied_at TEXT DEFAULT CURRENT_TIMESTAMP
		)`)
	if err != nil {
		return fmt.Errorf("creating table: %w", err)
	}
	_, err = db.Exec(`INSERT OR IGNORE INTO migrations (name) VALUES ('017_add_source_kind')`)
	if err != nil {
		return fmt.Errorf("recording migration 017: %w", err)
	}
	return nil
}

// migrationApplied checks whether a named migration has already run.
func migrationApplied(tx *sql.Tx, name string) (bool, error) {
	var count int
	err := tx.QueryRow(`SELECT COUNT(*) FROM migrations WHERE name = ?`, name).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("checking migration %s: %w", name, err)
	}
	return count > 0, nil
}

// migrate018AddSessionKind extends the CHECK constraint on the sources table to
// accept 'session' as a valid kind value. Two DB populations exist:
//   - Migrated DBs (ALTER TABLE ADD COLUMN in 017): no CHECK constraint at all,
//     so 'session' inserts already work. Just record the migration.
//   - Fresh DBs (created with schemaSQL): have CHECK (kind IN ('ephemeral', 'durable')).
//     These require a table rebuild to widen the constraint.
//
// Detection: inspect the CREATE TABLE DDL in sqlite_master. If the DDL
// contains 'session' in the CHECK, the schema is already current (fresh DB
// created with updated schemaSQL). If not, a table rebuild is needed.
func migrate018AddSessionKind(db *sql.DB) error {
	tx, err := beginImmediate(db)
	if err != nil {
		return fmt.Errorf("begin immediate: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	applied, err := migrationApplied(tx, "018_add_session_kind")
	if err != nil {
		return err
	}
	if applied {
		return tx.Commit()
	}

	needsRebuild, err := sourcesDDLLacksSession(tx)
	if err != nil {
		return err
	}
	if needsRebuild {
		if err := rebuildSourcesTable(tx); err != nil {
			return fmt.Errorf("rebuilding sources table: %w", err)
		}
	}

	if _, err := tx.Exec(`INSERT INTO migrations (name) VALUES ('018_add_session_kind')`); err != nil {
		return fmt.Errorf("recording migration: %w", err)
	}
	return tx.Commit()
}

// sourcesDDLLacksSession checks whether the sources table's DDL contains a
// CHECK constraint that does NOT include 'session'. Returns true if a rebuild
// is needed, false if the schema already accepts 'session' (either because the
// CHECK includes it or because there's no CHECK at all — ALTER TABLE path).
func sourcesDDLLacksSession(tx *sql.Tx) (bool, error) {
	var ddl string
	err := tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='sources'`).Scan(&ddl)
	if err != nil {
		return false, fmt.Errorf("reading sources DDL: %w", err)
	}
	hasCheck := strings.Contains(ddl, "CHECK")
	hasSession := strings.Contains(ddl, "'session'")
	// Rebuild only when there IS a CHECK that does NOT include 'session'.
	return hasCheck && !hasSession, nil
}

// rebuildSourcesTable replaces the sources table with one that has an updated
// CHECK constraint accepting 'session'. SQLite does not support ALTER TABLE
// to modify constraints, so we create a new table, copy data, drop old, rename.
func rebuildSourcesTable(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		CREATE TABLE sources_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			label TEXT NOT NULL,
			content_type TEXT NOT NULL DEFAULT 'plaintext',
			chunk_count INTEGER NOT NULL DEFAULT 0,
			code_chunk_count INTEGER NOT NULL DEFAULT 0,
			indexed_at TEXT DEFAULT CURRENT_TIMESTAMP,
			last_accessed_at TEXT DEFAULT CURRENT_TIMESTAMP,
			access_count INTEGER NOT NULL DEFAULT 0,
			content_hash TEXT,
			kind TEXT NOT NULL DEFAULT 'durable' CHECK (kind IN ('ephemeral', 'durable', 'session'))
		)`); err != nil {
		return fmt.Errorf("creating sources_new: %w", err)
	}

	if _, err := tx.Exec(`
		INSERT INTO sources_new (id, label, content_type, chunk_count, code_chunk_count,
			indexed_at, last_accessed_at, access_count, content_hash, kind)
		SELECT id, label, content_type, chunk_count, code_chunk_count,
			indexed_at, last_accessed_at, access_count, content_hash, kind
		FROM sources`); err != nil {
		return fmt.Errorf("copying data: %w", err)
	}

	if _, err := tx.Exec(`DROP TABLE sources`); err != nil {
		return fmt.Errorf("dropping old table: %w", err)
	}

	if _, err := tx.Exec(`ALTER TABLE sources_new RENAME TO sources`); err != nil {
		return fmt.Errorf("renaming table: %w", err)
	}

	return nil
}

