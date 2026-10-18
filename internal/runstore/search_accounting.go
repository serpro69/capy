package runstore

import (
	"context"
	"database/sql"
)

// refreshIndexDebit runs under the caller's RESERVED write transaction. SQLite
// 3.51.2's FTS5 xSavepoint flushes pending postings, so dbstat/page_count include
// the tentative batch before a quota decision. No index write calls retain.
func refreshIndexDebit(ctx context.Context, tx *sql.Tx) (int64, error) {
	var enabled bool
	var used, previous int64
	if err := tx.QueryRowContext(ctx, "SELECT accounted_bytes,index_bytes,index_enabled FROM run_meta WHERE singleton=1").Scan(&used, &previous, &enabled); err != nil {
		return 0, storageError()
	}
	if !enabled {
		return used, nil
	}
	if _, err := tx.ExecContext(ctx, "SAVEPOINT index_flush"); err != nil {
		return 0, storageError()
	}
	if _, err := tx.ExecContext(ctx, "RELEASE index_flush"); err != nil {
		return 0, storageError()
	}
	var mode string
	if err := tx.QueryRowContext(ctx, "SELECT accounting_mode FROM run_policy WHERE singleton=1").Scan(&mode); err != nil {
		return 0, storageError()
	}
	var debit int64
	switch mode {
	case "dbstat":
		// Every search B-tree (both FTS shadow families plus anchors/progress)
		// is named by this fixed internal prefix. SQLite's progress PK autoindex
		// fits the per-run metadata reservation, but include it for clarity.
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(pgsize),0) FROM dbstat WHERE name GLOB 'run_search_*' OR name='sqlite_autoindex_run_search_progress_1'").Scan(&debit); err != nil {
			return 0, storageError()
		}
	case "conservative":
		var pages, free, size int64
		if err := tx.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
			return 0, storageError()
		}
		if err := tx.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&free); err != nil {
			return 0, storageError()
		}
		if err := tx.QueryRowContext(ctx, "PRAGMA page_size").Scan(&size); err != nil || free > pages || size < 1 || pages > (1<<62)/size {
			return 0, storageError()
		}
		debit = (pages - free) * size
	default:
		return 0, storageError()
	}
	used += debit - previous
	if used < 0 {
		return 0, storageError()
	}
	if _, err := tx.ExecContext(ctx, "UPDATE run_meta SET accounted_bytes=?,index_bytes=? WHERE singleton=1", used, debit); err != nil {
		return 0, storageError()
	}
	return used, nil
}

// deleteIndexWindows participates in the eviction tombstone's row budget.
// Delete the two FTS copies before their anchors; a parent cascade must never
// perform an unbounded virtual-table deletion or leave searchable orphans.
func deleteIndexWindows(ctx context.Context, tx *sql.Tx, id string, budget int) (int64, bool, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id FROM run_search_windows WHERE run_id=? LIMIT ?", id, min(32, budget/3))
	if err != nil {
		return 0, false, storageError()
	}
	var ids []int64
	for rows.Next() {
		var rowID int64
		if err := rows.Scan(&rowID); err != nil {
			rows.Close()
			return 0, false, storageError()
		}
		ids = append(ids, rowID)
	}
	rowErr, closeErr := rows.Err(), rows.Close()
	if rowErr != nil || closeErr != nil {
		return 0, false, storageError()
	}
	for _, rowID := range ids {
		for _, table := range []string{"run_search_fts", "run_search_trigram"} {
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE rowid=?", rowID); err != nil {
				return 0, false, storageError()
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM run_search_windows WHERE id=?", rowID); err != nil {
			return 0, false, storageError()
		}
	}
	var more bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM run_search_windows WHERE run_id=?)", id).Scan(&more); err != nil {
		return 0, false, storageError()
	}
	return int64(3 * len(ids)), more, nil
}
