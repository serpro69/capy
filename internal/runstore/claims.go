package runstore

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/serpro69/capy/internal/sqliteutil"
)

// ClaimIndex reserves bounded indexing work for a live owner generation. The
// caller retains Owner.Lease through the work and release. Task 10 supplies the
// actual index worker; the claim/recovery substrate introduces no FTS writes.
func (s *Store) ClaimIndex(ctx context.Context, owner *Owner, id string) (bool, error) {
	if owner == nil || owner.Lease == nil {
		return false, storageError()
	}
	if _, err := owner.Lease.Stat(); err != nil {
		return false, storageError()
	}
	ctx, cancel := context.WithTimeout(ctx, WriteTimeout)
	defer cancel()
	tx, err := sqliteutil.BeginImmediateContext(ctx, s.db, "runs")
	if err != nil {
		return false, storageError()
	}
	defer tx.Rollback()
	var generation string
	if err := tx.QueryRowContext(ctx, "SELECT generation FROM run_owners WHERE slot=? AND cleanup_unverified=0", owner.Slot).Scan(&generation); err != nil || generation != owner.ID {
		return false, storageError()
	}
	row, err := loadRun(ctx, tx, id)
	if err != nil {
		return false, err
	}
	if row.Result.Error != nil && row.Result.Error.Code == "cleanup_unverified" {
		return false, row.Result.Error
	}
	inserted, err := tx.ExecContext(ctx, "INSERT INTO run_work_claims(run_id,slot,generation) VALUES(?,?,?) ON CONFLICT(run_id) DO NOTHING", id, owner.Slot, owner.ID)
	if err != nil {
		return false, storageError()
	}
	n, err := inserted.RowsAffected()
	if err != nil {
		return false, storageError()
	}
	if n == 0 {
		return false, nil
	}
	row.Result.Index.State, row.Result.Index.Error = "building", nil
	encoded, err := json.Marshal(row.Result)
	if err != nil {
		return false, storageError()
	}
	if _, err := tx.ExecContext(ctx, "UPDATE runs SET result=? WHERE id=?", encoded, id); err != nil {
		return false, storageError()
	}
	if err := tx.Commit(); err != nil {
		return false, storageError()
	}
	return true, nil
}

// ReleaseIndex is generation-scoped so an old worker cannot release a replacement
// generation's claim. Its caller persists index coverage/outcome before release.
func (s *Store) ReleaseIndex(ctx context.Context, owner *Owner, id string) error {
	if owner == nil {
		return storageError()
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM run_work_claims WHERE run_id=? AND slot=? AND generation=?", id, owner.Slot, owner.ID)
	if err != nil {
		return storageError()
	}
	return nil
}

func (s *Store) recoverClaim(ctx context.Context, tx *sql.Tx, id string, slot int, generation string) error {
	var encoded []byte
	if err := tx.QueryRowContext(ctx, "SELECT result FROM runs WHERE id=?", id).Scan(&encoded); err != nil {
		return storageError()
	}
	var result Result
	if json.Unmarshal(encoded, &result) != nil {
		return storageError()
	}
	if result.Index.State == "building" {
		result.Index.State = "failed"
		result.Index.Error = &Error{Code: "index_interrupted", Message: "index worker was lost; a later search can resume retained coverage"}
		encoded, err := json.Marshal(result)
		if err != nil {
			return storageError()
		}
		if _, err := tx.ExecContext(ctx, "UPDATE runs SET result=? WHERE id=?", encoded, id); err != nil {
			return storageError()
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM run_work_claims WHERE run_id=? AND slot=? AND generation=?", id, slot, generation); err != nil {
		return storageError()
	}
	return nil
}
