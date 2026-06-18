package vault

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"github.com/serpro69/capy/internal/sqliteutil"
	"github.com/serpro69/capy/internal/store"
)

// compactBatchSize bounds how many legacy blobs Compact rewrites per write
// transaction, mirroring the batched-write discipline elsewhere in the store
// (WriteBatch, RebuildFTSBatch). It keeps each transaction — and the blobs held
// in memory while it runs — bounded on an arbitrarily large vault.
const compactBatchSize = 50

// CompactResult reports what Compact did. SessionsRewritten/FilesRewritten count
// the legacy (encoding IS NULL) rows that were run through the codec; Vacuumed
// records whether a VACUUM ran (it is skipped when nothing was rewritten, so a
// second Compact is a true no-op).
type CompactResult struct {
	SessionsRewritten int
	FilesRewritten    int
	Vacuumed          bool
}

// Compact rewrites every legacy (encoding IS NULL) blob through the zstd codec,
// then reclaims the freed pages with VACUUM. Legacy rows are v1 data written
// before the encoding column existed; they read back as raw until rewritten
// here. Rows already stored 'raw' are the terminal "compression considered,
// declined" state and are deliberately left untouched — selecting them too would
// keep a second Compact from being a clean no-op (design.md §1).
//
// It refuses to run with CAPY_VAULT_NO_COMPRESS set (it would rewrite every blob
// without compressing — pure I/O for no gain), and busy-pre-checks via Checkpoint
// so it fails fast with a clear message if the MCP server still holds the vault
// open, rather than doing all the rewrite work and only hitting contention at
// VACUUM. VACUUM runs on a dedicated connection after the pool closes (mirroring
// Checkpoint); SQLite's exclusive lock genuinely protects it, so — unlike rekey's
// file-swap — no "stop the server" guarantee beyond the pre-check is required.
func (s *VaultStore) Compact(ctx context.Context) (CompactResult, error) {
	var res CompactResult

	if os.Getenv(noCompressEnv) != "" {
		return res, fmt.Errorf("cannot compact while %s is set: it would rewrite blobs without compressing — unset it and retry", noCompressEnv)
	}

	// Busy pre-check: a checkpoint reporting busy pages means another process (the
	// MCP server) holds the vault open. Abort before any rewrite work rather than
	// discovering contention at VACUUM. On success it also flushes the WAL.
	if err := s.Checkpoint(); err != nil {
		return res, fmt.Errorf("vault is busy (%w); stop the MCP server before compacting", err)
	}

	sessions, err := s.compactTable(ctx,
		`SELECT uuid FROM vault_sessions WHERE encoding IS NULL`,
		`SELECT raw_jsonl FROM vault_sessions WHERE uuid = ?`,
		`UPDATE vault_sessions SET raw_jsonl = ?, encoding = ? WHERE uuid = ?`,
		1)
	if err != nil {
		return res, fmt.Errorf("compacting sessions: %w", err)
	}
	files, err := s.compactTable(ctx,
		`SELECT session_uuid, relative_path FROM vault_files WHERE encoding IS NULL`,
		`SELECT raw_content FROM vault_files WHERE session_uuid = ? AND relative_path = ?`,
		`UPDATE vault_files SET raw_content = ?, encoding = ? WHERE session_uuid = ? AND relative_path = ?`,
		2)
	if err != nil {
		return res, fmt.Errorf("compacting files: %w", err)
	}
	res.SessionsRewritten = sessions.rewritten
	res.FilesRewritten = files.rewritten

	// Nothing legacy remained — no pages were freed, so a VACUUM would only churn
	// the file for no gain. This is the second-compact / already-compressed no-op.
	if res.SessionsRewritten == 0 && res.FilesRewritten == 0 {
		return res, nil
	}

	// Stamp the forward-compat marker if compaction produced any compressed blob,
	// matching the normal write path (design.md §1): a compacted v1 vault must
	// carry the same min_reader_version a freshly v2-written vault would.
	if sessions.anyZstd || files.anyZstd {
		if err := s.markCompressed(ctx); err != nil {
			return res, fmt.Errorf("stamping min_reader_version: %w", err)
		}
	}

	// VACUUM needs the connection pool closed first (mirrors Checkpoint): Close
	// finalizes statements, closes the pool, and checkpoints the WAL into the main
	// file. The deferred Close in the caller is then a safe no-op.
	if err := s.Close(); err != nil {
		return res, fmt.Errorf("closing vault before vacuum: %w", err)
	}
	if err := s.vacuum(ctx); err != nil {
		return res, err
	}
	res.Vacuumed = true
	return res, nil
}

// rewriteStats accumulates a table's compaction outcome across its batches.
type rewriteStats struct {
	rewritten int
	anyZstd   bool
}

// compactTable rewrites every legacy row of one table. listSQL selects the row
// keys (1 or nKeyCols TEXT columns); selectSQL fetches one row's blob by key;
// updateSQL writes the recompressed blob + encoding back (its bind order is
// blob, encoding, then the key columns). Keys are collected up front so no read
// cursor is held open across the write transactions, and the rewrites are split
// into bounded BeginImmediate batches.
func (s *VaultStore) compactTable(ctx context.Context, listSQL, selectSQL, updateSQL string, nKeyCols int) (rewriteStats, error) {
	var stats rewriteStats
	db, err := s.getDB(ctx)
	if err != nil {
		return stats, err
	}

	keys, err := collectLegacyKeys(ctx, db, listSQL, nKeyCols)
	if err != nil {
		return stats, fmt.Errorf("listing legacy rows: %w", err)
	}

	for start := 0; start < len(keys); start += compactBatchSize {
		end := min(start+compactBatchSize, len(keys))
		tx, err := sqliteutil.BeginImmediateContext(ctx, db, "vault_meta")
		if err != nil {
			return stats, fmt.Errorf("begin: %w", err)
		}
		if err := rewriteBatch(ctx, tx, selectSQL, updateSQL, keys[start:end], &stats); err != nil {
			_ = tx.Rollback()
			return stats, err
		}
		if err := tx.Commit(); err != nil {
			return stats, fmt.Errorf("commit: %w", err)
		}
	}
	return stats, nil
}

// rewriteBatch recompresses one batch of rows within tx: for each key it reads
// the raw blob, runs it through encodeBlob, and writes the result back with the
// encoding encodeBlob returns ('zstd' when it shrank, 'raw' when it didn't) —
// never a hard-coded 'zstd', which would mislabel an incompressible blob and
// corrupt its read.
func rewriteBatch(ctx context.Context, tx *sql.Tx, selectSQL, updateSQL string, keys [][]any, stats *rewriteStats) error {
	for _, key := range keys {
		var raw []byte
		if err := tx.QueryRowContext(ctx, selectSQL, key...).Scan(&raw); err != nil {
			return fmt.Errorf("reading legacy blob %v: %w", key, err)
		}
		data, enc := encodeBlob(raw)
		args := append([]any{data, enc}, key...)
		if _, err := tx.ExecContext(ctx, updateSQL, args...); err != nil {
			return fmt.Errorf("rewriting legacy blob %v: %w", key, err)
		}
		stats.rewritten++
		if enc == encodingZstd {
			stats.anyZstd = true
		}
	}
	return nil
}

// collectLegacyKeys runs listSQL and returns each row's nCols TEXT key columns as
// a bind-arg slice. Reading just the keys (not the blobs) keeps memory bounded and
// lets the read cursor close before any write transaction opens.
func collectLegacyKeys(ctx context.Context, db *sql.DB, listSQL string, nCols int) ([][]any, error) {
	rows, err := db.QueryContext(ctx, listSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys [][]any
	for rows.Next() {
		vals := make([]string, nCols)
		dest := make([]any, nCols)
		for i := range vals {
			dest[i] = &vals[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("scanning key: %w", err)
		}
		key := make([]any, nCols)
		for i, v := range vals {
			key[i] = v
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating keys: %w", err)
	}
	return keys, nil
}

// markCompressed records the min_reader_version marker in its own transaction,
// used by Compact after it produced at least one zstd blob. It mirrors the
// once-per-write-tx stamping in writeRecord; INSERT OR IGNORE makes it idempotent.
func (s *VaultStore) markCompressed(ctx context.Context) error {
	db, err := s.getDB(ctx)
	if err != nil {
		return err
	}
	tx, err := sqliteutil.BeginImmediateContext(ctx, db, "vault_meta")
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := markMinReaderVersion(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// vacuum runs VACUUM on a dedicated single connection, mirroring Checkpoint's
// standalone-connection pattern. It must be called only after the pool is closed
// so VACUUM gets the exclusive lock it needs. PRAGMA temp_store = MEMORY keeps
// VACUUM's transient copy off disk, so the plaintext never lands in an
// unencrypted on-disk temp file (design.md §1; the §HMW "don't widen the
// secret-exposure surface" goal). ctx is threaded through ExecContext so a long
// VACUUM on a large vault can be cancelled (Ctrl+C) — unlike Checkpoint/Close,
// which run at shutdown when the request ctx is already gone, this runs
// mid-command with a live ctx; cancelling rolls back, leaving the DB intact.
func (s *VaultStore) vacuum(ctx context.Context) error {
	key, err := RequireVaultKey()
	if err != nil {
		return err
	}
	dsn := store.EncryptedDSN(s.dbPath, key) + "&_journal_mode=WAL&_busy_timeout=10000"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return fmt.Errorf("opening vault for vacuum: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if _, err := db.ExecContext(ctx, "PRAGMA temp_store = MEMORY"); err != nil {
		return fmt.Errorf("setting temp_store for vacuum: %w", err)
	}
	if _, err := db.ExecContext(ctx, "VACUUM"); err != nil {
		return fmt.Errorf("vault vacuum failed: %w", err)
	}
	return nil
}
