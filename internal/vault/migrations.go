package vault

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"
)

// migrateVault creates the vault_migrations tracking table and runs any pending
// schema migrations. v1 ships its entire schema in schemaSQL, so there are no
// migrations yet — this is the scaffolding the first post-v1 schema change will
// use. Migration state is tracked by name in vault_migrations (the single
// source of truth); vault_meta carries no schema_version.
//
// Future migrations follow internal/store/migrate.go: guard each with
// vaultMigrationApplied inside a beginImmediate transaction, apply idempotently,
// then record the name.
func migrateVault(db *sql.DB) error {
	if err := ensureVaultMigrationsTable(db); err != nil {
		return fmt.Errorf("creating vault_migrations table: %w", err)
	}
	return nil
}

// ensureVaultMigrationsTable creates the by-name migration-tracking table. This
// (not schemaSQL) owns vault_migrations.
func ensureVaultMigrationsTable(db *sql.DB) error {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS vault_migrations (
			name TEXT PRIMARY KEY,
			applied_at TEXT DEFAULT CURRENT_TIMESTAMP
		)`); err != nil {
		return fmt.Errorf("creating table: %w", err)
	}
	return nil
}

// beginImmediate starts a transaction that holds SQLite's RESERVED write lock
// for its whole lifetime. database/sql's Begin() issues BEGIN DEFERRED, so we
// upgrade to a write transaction with a no-op write against vault_meta — SQLite
// acquires the RESERVED lock on the first write of a DEFERRED tx. Mirrors
// internal/store/migrate.go:beginImmediate so a concurrent writer (e.g. the
// server-startup sweep) blocks instead of failing outright.
//
// Retries on SQLITE_BUSY with exponential backoff because BeginTx can surface
// "database is locked" before the connection-level busy_timeout engages under
// goroutine contention.
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

		if _, err := tx.Exec("DELETE FROM vault_meta WHERE 0"); err != nil {
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
	return nil, fmt.Errorf("could not acquire vault write lock after %d retries", maxRetries)
}

// isBusy reports whether err is a SQLITE_BUSY / SQLITE_LOCKED condition. Mirrors
// internal/store/retry.go:isBusy; consolidating both into sqliteutil is a
// possible future cleanup (Task 1.1 deliberately left store's copy in place).
func isBusy(err error) bool {
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
