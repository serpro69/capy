package vault

import (
	"context"
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
// Naming note: 0002 (vault_snapshots) was dropped along with the PreCompact
// archival tasks (see docs/wip/vault/v2/precompact-investigation.md), so v2 adds
// only 0001 (blob encoding) alongside the existing 0003 (index_version). The
// 0002 gap is deliberate and must not be reused; 0004 (chunk FTS) is the next
// slot after it; 0005 adds vault-owned session names.
func migrateVault(ctx context.Context, db *sql.DB) error {
	if err := ensureVaultMigrationsTable(ctx, db); err != nil {
		return fmt.Errorf("creating vault_migrations table: %w", err)
	}
	if err := migrate0001AddBlobEncoding(ctx, db); err != nil {
		return fmt.Errorf("migration 0001_blob_encoding: %w", err)
	}
	if err := migrate0003AddIndexVersion(ctx, db); err != nil {
		return fmt.Errorf("migration 0003_add_index_version: %w", err)
	}
	if err := migrate0004AddChunkFTS(ctx, db); err != nil {
		return fmt.Errorf("migration 0004_add_chunk_fts: %w", err)
	}
	if err := migrate0005AddSessionNames(ctx, db); err != nil {
		return fmt.Errorf("migration 0005_session_names: %w", err)
	}
	return nil
}

// ensureVaultMigrationsTable creates the by-name migration-tracking table. This
// (not schemaSQL) owns vault_migrations.
func ensureVaultMigrationsTable(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
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
func vaultMigrationApplied(ctx context.Context, tx *sql.Tx, name string) (bool, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM vault_migrations WHERE name = ?`, name).Scan(&count); err != nil {
		return false, fmt.Errorf("checking migration %s: %w", name, err)
	}
	return count > 0, nil
}

// migrate0001AddBlobEncoding adds the `encoding` discriminator column to
// vault_sessions and vault_files (design.md §1; see codec.go). It records how each
// blob (raw_jsonl / raw_content) is stored: NULL/"raw" or "zstd". SQLite ADD
// COLUMN does not rewrite existing rows, so every legacy v1 row reads back as
// NULL = raw until rewritten by `capy vault compact` (Task 6).
//   - Pre-existing vaults: the tables lack the columns, so the ALTERs add them.
//   - Fresh vaults: schemaSQL already creates the columns, so the columnExists
//     guard skips the ALTER (avoiding a duplicate-column error); the migration is
//     still recorded so it never re-runs.
func migrate0001AddBlobEncoding(ctx context.Context, db *sql.DB) error {
	const name = "0001_blob_encoding"

	// Fast path: when already recorded (the common case on every getDB() open),
	// skip acquiring the RESERVED write lock — a plain read avoids needless
	// contention with concurrent readers/writers. A read error falls through.
	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM vault_migrations WHERE name = ?`, name).Scan(&count); err == nil && count > 0 {
		return nil
	}

	tx, err := sqliteutil.BeginImmediateContext(ctx, db, "vault_meta")
	if err != nil {
		return fmt.Errorf("begin immediate: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Re-check inside the write tx: two opens may both pass the read fast-path,
	// but only one wins the lock and applies; the loser sees it applied here.
	applied, err := vaultMigrationApplied(ctx, tx, name)
	if err != nil {
		return err
	}
	if applied {
		return tx.Commit()
	}

	for _, table := range []string{"vault_sessions", "vault_files"} {
		hasColumn, err := columnExists(ctx, tx, table, "encoding")
		if err != nil {
			return err
		}
		if !hasColumn {
			//nolint:gosec // table is a trusted internal constant, never user input
			if _, err := tx.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN encoding TEXT", table)); err != nil {
				return fmt.Errorf("alter %s: %w", table, err)
			}
		}
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO vault_migrations (name) VALUES (?)`, name); err != nil {
		return fmt.Errorf("recording migration: %w", err)
	}
	return tx.Commit()
}

// migrate0003AddIndexVersion adds the index_version column to vault_sessions.
//   - Pre-existing vaults: the table lacks the column, so the ALTER adds it with
//     DEFAULT 1 — every already-archived row is flagged stale (the result-only
//     indexer) and qualifies for `capy vault reindex` / a re-import upgrade.
//   - Fresh vaults: schemaSQL already creates the column, so the table_info guard
//     skips the ALTER (avoiding a duplicate-column error); the migration is still
//     recorded so it never re-runs.
func migrate0003AddIndexVersion(ctx context.Context, db *sql.DB) error {
	// Fast path: when the migration is already recorded (the common case on every
	// getDB() open), skip acquiring the RESERVED write lock entirely — a plain
	// read avoids needless contention with concurrent readers/writers (e.g. the
	// server sweep). A read error falls through to the locked path.
	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM vault_migrations WHERE name = '0003_add_index_version'`).Scan(&count); err == nil && count > 0 {
		return nil
	}

	tx, err := sqliteutil.BeginImmediateContext(ctx, db, "vault_meta")
	if err != nil {
		return fmt.Errorf("begin immediate: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Re-check inside the write tx: two opens may both pass the read fast-path,
	// but only one wins the lock and applies; the loser sees it applied here.
	applied, err := vaultMigrationApplied(ctx, tx, "0003_add_index_version")
	if err != nil {
		return err
	}
	if applied {
		return tx.Commit()
	}

	hasColumn, err := columnExists(ctx, tx, "vault_sessions", "index_version")
	if err != nil {
		return err
	}
	if !hasColumn {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE vault_sessions ADD COLUMN index_version INTEGER NOT NULL DEFAULT 1`); err != nil {
			return fmt.Errorf("alter table: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO vault_migrations (name) VALUES ('0003_add_index_version')`); err != nil {
		return fmt.Errorf("recording migration: %w", err)
	}
	return tx.Commit()
}

// migrate0004AddChunkFTS creates the chunk-granularity FTS5 layer tables
// (vault_chunks + vault_chunks_trigram) for the shared retrieval engine
// (design: docs/wip/vault-session-search §D2/§D4).
//   - Pre-existing vaults: the tables are absent, so the CREATEs add them.
//   - Fresh vaults: schemaSQL already creates them (same DDL constant), so the
//     IF NOT EXISTS guard makes the CREATEs no-ops; the migration is still
//     recorded so it never re-runs.
//
// The tables start empty for every already-archived session; the accompanying
// currentIndexVersion bump (store.go) flags those sessions as below-current so
// `capy vault reindex` backfills chunks from stored raw_jsonl blobs.
func migrate0004AddChunkFTS(ctx context.Context, db *sql.DB) error {
	const name = "0004_add_chunk_fts"

	// Fast path: when already recorded (the common case on every getDB() open),
	// skip acquiring the RESERVED write lock — a plain read avoids needless
	// contention with concurrent readers/writers. A read error falls through.
	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM vault_migrations WHERE name = ?`, name).Scan(&count); err == nil && count > 0 {
		return nil
	}

	tx, err := sqliteutil.BeginImmediateContext(ctx, db, "vault_meta")
	if err != nil {
		return fmt.Errorf("begin immediate: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Re-check inside the write tx: two opens may both pass the read fast-path,
	// but only one wins the lock and applies; the loser sees it applied here.
	applied, err := vaultMigrationApplied(ctx, tx, name)
	if err != nil {
		return err
	}
	if applied {
		return tx.Commit()
	}

	if _, err := tx.ExecContext(ctx, chunkFTSTablesSQL); err != nil {
		return fmt.Errorf("creating chunk FTS tables: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO vault_migrations (name) VALUES (?)`, name); err != nil {
		return fmt.Errorf("recording migration: %w", err)
	}
	return tx.Commit()
}

// migrate0005AddSessionNames creates the capy-owned session-name table. Fresh
// vaults already have it through schemaSQL; IF NOT EXISTS keeps the legacy and
// fresh paths identical while the name-keyed migration record makes reruns a
// read-only fast path.
func migrate0005AddSessionNames(ctx context.Context, db *sql.DB) error {
	const name = "0005_session_names"

	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM vault_migrations WHERE name = ?`, name).Scan(&count); err == nil && count > 0 {
		return nil
	}

	tx, err := sqliteutil.BeginImmediateContext(ctx, db, "vault_meta")
	if err != nil {
		return fmt.Errorf("begin immediate: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	applied, err := vaultMigrationApplied(ctx, tx, name)
	if err != nil {
		return err
	}
	if applied {
		return tx.Commit()
	}

	if _, err := tx.ExecContext(ctx, sessionNamesTableSQL); err != nil {
		return fmt.Errorf("creating session names table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO vault_migrations (name) VALUES (?)`, name); err != nil {
		return fmt.Errorf("recording migration: %w", err)
	}
	return tx.Commit()
}

// ctxQueryer is the read surface columnExists needs, satisfied by both *sql.Tx
// (the migration runner, which probes inside its write transaction) and *sql.DB
// (merge, which feature-detects a source vault it must NOT migrate — see
// merge.go). Keeping the parameter an interface lets one implementation serve
// both without duplicating the PRAGMA scan.
type ctxQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// columnExists reports whether table has a column named col, via PRAGMA
// table_info. table is a trusted internal constant, never user input.
func columnExists(ctx context.Context, q ctxQueryer, table, col string) (bool, error) {
	rows, err := q.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table)) //nolint:gosec // table is a trusted internal constant
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
