package vault

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/serpro69/capy/internal/sqliteutil"
)

// MergedDeletion reports selected source pairs for one exact UUID.
type MergedDeletion struct {
	UUID         string
	Removed      bool
	Added, Known int
}

type deletionGroup struct {
	uuid     string
	projects []string
}

// Ordered pairs keep every selected project for one UUID in the same transaction.
func mergeSourceDeletions(ctx context.Context, src ctxQueryer, dest mergeDestination, opts MergeOptions, res *ImportResult) error {
	rows, err := src.QueryContext(ctx, "SELECT session_uuid, effective_project FROM vault_session_deletions ORDER BY session_uuid, effective_project")
	if err != nil {
		return fmt.Errorf("reading source deletions: %w", err)
	}
	defer rows.Close()
	var batch []deletionGroup
	unprocessed := func(err error) error {
		if len(batch) == 0 {
			return err
		}
		ids := make([]string, 0, len(batch))
		for _, group := range batch {
			ids = append(ids, group.uuid)
		}
		return fmt.Errorf("unprocessed deletion UUIDs %s: %w", strings.Join(ids, ", "), err)
	}
	flush := func() error {
		results, err := dest.mergeDeletionBatch(ctx, batch, opts.DryRun)
		if err != nil {
			return unprocessed(fmt.Errorf("processing deletion batch: %w", err))
		}
		res.Deletions = append(res.Deletions, results...)
		batch = nil
		return nil
	}
	for rows.Next() {
		var id, project string
		if err := rows.Scan(&id, &project); err != nil {
			return unprocessed(fmt.Errorf("scanning source deletion: %w", err))
		}
		if opts.Project != "" && !containsProjectASCII(project, opts.Project) {
			continue
		}
		if len(batch) == 0 || batch[len(batch)-1].uuid != id {
			if len(batch) == maxBatchSessions {
				if err := flush(); err != nil {
					return fmt.Errorf("stopped before deletion UUID %s: %w", id, err)
				}
			}
			batch = append(batch, deletionGroup{uuid: id})
		}
		last := &batch[len(batch)-1]
		last.projects = append(last.projects, project)
	}
	if err := rows.Err(); err != nil {
		return unprocessed(fmt.Errorf("reading source deletions: %w", err))
	}
	if len(batch) > 0 {
		return flush()
	}
	return nil
}

func (s *VaultStore) mergeDeletionBatch(ctx context.Context, batch []deletionGroup, preview bool) ([]MergedDeletion, error) {
	db, err := s.getDB(ctx)
	if err != nil {
		return nil, err
	}
	var tx *sql.Tx
	if preview {
		tx, err = db.BeginTx(ctx, nil)
	} else {
		tx, err = sqliteutil.BeginImmediateContext(ctx, db, "vault_meta")
	}
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var cleanup []any
	results := make([]MergedDeletion, 0, len(batch))
	added := false
	for _, group := range batch {
		result := MergedDeletion{UUID: group.uuid}
		var marked bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM vault_sessions WHERE uuid=?),
			EXISTS(SELECT 1 FROM vault_session_deletions WHERE session_uuid=?)`, group.uuid, group.uuid).Scan(&result.Removed, &marked)
		if err != nil {
			return nil, err
		}
		if !preview && (result.Removed || !marked) {
			cleanup = append(cleanup, group.uuid)
		}
		for _, project := range group.projects {
			known, err := mergeDeletionPair(ctx, tx, group.uuid, project, preview)
			if err != nil {
				return nil, err
			}
			if known {
				result.Known++
			} else {
				result.Added++
				added = true
			}
		}
		if !preview && result.Removed {
			if _, err := tx.ExecContext(ctx, "DELETE FROM vault_sessions WHERE uuid=?", group.uuid); err != nil {
				return nil, err
			}
		}
		results = append(results, result)
	}
	if err := deleteSessionIndexes(ctx, tx, cleanup); err != nil {
		return nil, err
	}
	if !preview && added {
		if err := markMinReaderVersion(ctx, tx, readerVersionSessionDeletions); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return results, nil
}

func mergeDeletionPair(ctx context.Context, tx *sql.Tx, uuid, project string, preview bool) (known bool, err error) {
	if preview {
		err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM vault_session_deletions WHERE session_uuid=? AND effective_project=?)", uuid, project).Scan(&known)
		return known, err
	}
	result, err := tx.ExecContext(ctx, insertDeletionSQL, uuid, project)
	if err != nil {
		return false, err
	}
	added, err := result.RowsAffected()
	return added == 0, err
}
