package vault

import (
	"database/sql"
	"fmt"

	"github.com/serpro69/capy/internal/sqliteutil"
)

// migrateVault creates the vault_migrations tracking table and runs any pending
// schema migrations. Migration state is tracked by name in vault_migrations (the
// single source of truth); vault_meta carries no schema_version.
//
// Each migration follows internal/store/migrate.go: guard it with
// vaultMigrationApplied inside a sqliteutil.BeginImmediate transaction, apply
// idempotently, then record the name. Migrations are name-keyed and idempotent,
// so application order does not matter.
//
// Naming note: 0001/0002 are reserved for pending vault v2 work (blob encoding,
// vault_snapshots — see docs/wip/vault/v2/). This feature uses 0003 to avoid
// claiming those slots.
func migrateVault(db *sql.DB) error {
	if err := ensureVaultMigrationsTable(db); err != nil {
		return fmt.Errorf("creating vault_migrations table: %w", err)
	}
	if err := migrate0003AddIndexVersion(db); err != nil {
		return fmt.Errorf("migration 0003_add_index_version: %w", err)
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

// vaultMigrationApplied reports whether a named migration has already been
// recorded in vault_migrations.
func vaultMigrationApplied(tx *sql.Tx, name string) (bool, error) {
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM vault_migrations WHERE name = ?`, name).Scan(&count); err != nil {
		return false, fmt.Errorf("checking migration %s: %w", name, err)
	}
	return count > 0, nil
}

// migrate0003AddIndexVersion adds the index_version column to vault_sessions.
//   - Pre-existing vaults: the table lacks the column, so the ALTER adds it with
//     DEFAULT 1 — every already-archived row is flagged stale (the result-only
//     indexer) and qualifies for `capy vault reindex` / a re-import upgrade.
//   - Fresh vaults: schemaSQL already creates the column, so the table_info guard
//     skips the ALTER (avoiding a duplicate-column error); the migration is still
//     recorded so it never re-runs.
func migrate0003AddIndexVersion(db *sql.DB) error {
	// Fast path: when the migration is already recorded (the common case on every
	// getDB() open), skip acquiring the RESERVED write lock entirely — a plain
	// read avoids needless contention with concurrent readers/writers (e.g. the
	// server sweep). A read error falls through to the locked path.
	var count int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM vault_migrations WHERE name = '0003_add_index_version'`).Scan(&count); err == nil && count > 0 {
		return nil
	}

	tx, err := sqliteutil.BeginImmediate(db, "vault_meta")
	if err != nil {
		return fmt.Errorf("begin immediate: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Re-check inside the write tx: two opens may both pass the read fast-path,
	// but only one wins the lock and applies; the loser sees it applied here.
	applied, err := vaultMigrationApplied(tx, "0003_add_index_version")
	if err != nil {
		return err
	}
	if applied {
		return tx.Commit()
	}

	hasColumn, err := columnExists(tx, "vault_sessions", "index_version")
	if err != nil {
		return err
	}
	if !hasColumn {
		if _, err := tx.Exec(`ALTER TABLE vault_sessions ADD COLUMN index_version INTEGER NOT NULL DEFAULT 1`); err != nil {
			return fmt.Errorf("alter table: %w", err)
		}
	}

	if _, err := tx.Exec(`INSERT INTO vault_migrations (name) VALUES ('0003_add_index_version')`); err != nil {
		return fmt.Errorf("recording migration: %w", err)
	}
	return tx.Commit()
}

// columnExists reports whether table has a column named col, via PRAGMA
// table_info. table is a trusted internal constant, never user input.
func columnExists(tx *sql.Tx, table, col string) (bool, error) {
	rows, err := tx.Query(fmt.Sprintf("PRAGMA table_info(%s)", table)) //nolint:gosec // table is a trusted internal constant
	if err != nil {
		return false, fmt.Errorf("pragma table_info(%s): %w", table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return false, fmt.Errorf("scanning table_info: %w", err)
		}
		if name == col {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterating table_info: %w", err)
	}
	return false, nil
}
