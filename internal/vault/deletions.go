package vault

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const sessionDeletionsTableSQL = `
CREATE TABLE IF NOT EXISTS vault_session_deletions (
  session_uuid      TEXT NOT NULL,
  effective_project TEXT NOT NULL,
  PRIMARY KEY (session_uuid, effective_project)
);
`

const insertDeletionSQL = `INSERT INTO vault_session_deletions(session_uuid, effective_project)
	VALUES (?, ?) ON CONFLICT(session_uuid, effective_project) DO NOTHING`

const (
	reasonDeleted     = "deleted"
	reasonDisappeared = "session disappeared"
)

// writeOutcome describes one input after its transaction commits.
type writeOutcome struct {
	readerVersion int
	reason        string
}

func (o writeOutcome) apply(entry ImportedSession) ImportedSession {
	switch o.reason {
	case reasonDeleted:
		entry.Status, entry.Reason = StatusExcluded, o.reason
	case reasonDisappeared:
		entry.Status, entry.Reason = StatusSkipped, o.reason
	}
	return entry
}

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func sessionDeleted(ctx context.Context, q rowQueryer, uuid string) (bool, error) {
	var deleted bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM vault_session_deletions WHERE session_uuid = ?)`, uuid).Scan(&deleted)
	if err != nil {
		return false, fmt.Errorf("checking session deletion: %w", err)
	}
	return deleted, nil
}

func (s *VaultStore) sessionDeleted(ctx context.Context, uuid string) (bool, error) {
	db, err := s.getDB(ctx)
	if err != nil {
		return false, err
	}
	return sessionDeleted(ctx, db, uuid)
}

// Each FTS table has an unindexed UUID. Delete the batch in one scan per table.
func deleteSessionIndexes(ctx context.Context, tx *sql.Tx, uuids []any) error {
	if len(uuids) == 0 {
		return nil
	}
	placeholders := strings.Repeat("?,", len(uuids)-1) + "?"
	for _, table := range []string{"vault_fts", "vault_chunks", "vault_chunks_trigram"} {
		query := "DELETE FROM " + table + " WHERE session_uuid IN (" + placeholders + ")"
		if _, err := tx.ExecContext(ctx, query, uuids...); err != nil {
			return fmt.Errorf("delete %s: %w", table, err)
		}
	}
	return nil
}
