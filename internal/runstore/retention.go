package runstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/serpro69/capy/internal/sqliteutil"
)

// Reserve bounded mutable metadata/checkpoints and EOF projection at admission.
// EOF emits at most one marker per stream, split across at most 64 raw anchors.
// Keeping this reservation after completion also covers index-claim metadata.
// Program bytes, raw events and non-EOF projection rows are charged separately.
const runReserve = 64 << 10
const eventOverhead = 96
const projectionOverhead = 128
const retentionBatch = 64
const evictionRows = 256

var retryCapture = errors.New("retry capture after committed eviction progress")

type RetentionReport struct {
	Expired, Evicted int
	AccountedBytes   int64
	More             bool
	PendingDeletion  bool
	DeletedRows      int64
}

func quotaError() error {
	return failure("quota_exhausted", "run retained-data quota is exhausted; recover existing runs and allow maintenance or stop owners before increasing runs.max_retained_bytes")
}

// retain runs inside the caller's RESERVED write transaction. Accounting,
// eviction and the corresponding capture/admission commit share one lock, so
// independent processes cannot spend the same capacity. Work is bounded even
// when a lowered policy leaves a large backlog; later maintenance resumes it.
func retain(ctx context.Context, tx *sql.Tx, limit, needed int64, expire bool) (RetentionReport, error) {
	var report RetentionReport
	var err error
	report.AccountedBytes, err = refreshIndexDebit(ctx, tx)
	if err != nil {
		return report, err
	}
	if needed < 0 || needed > limit {
		return report, quotaError()
	}
	remainingRows := 2 * evictionRows
	var retiredBytes int64
	for range retentionBatch {
		pressure := report.AccountedBytes > limit-needed
		// A deferred expired manifest still precedes live logs. Finish this
		// pass and let the caller resume with a fresh deletion budget.
		if remainingRows < 257 {
			var expiredManifest bool
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM run_batches WHERE completed_at IS NOT NULL AND expires_at<=?)", time.Now().UTC().UnixMilli()).Scan(&expiredManifest); err != nil {
				return report, storageError()
			}
			if expiredManifest {
				break
			}
		}
		// Expired manifests are cheap, bounded victims and precede live logs.
		removed, debit, deleted, err := retainBatch(ctx, tx, false, expire || pressure, remainingRows)
		if err != nil {
			return report, err
		}
		if removed {
			report.AccountedBytes -= debit
			report.Expired++
			report.DeletedRows += deleted
			remainingRows -= int(deleted)
			retiredBytes += debit
			report.AccountedBytes, err = refreshIndexDebit(ctx, tx)
			if err != nil {
				return report, err
			}
			if remainingRows <= 0 || retiredBytes >= 4<<20 {
				break
			}
			continue
		}

		var id string
		var charged, expiry int64
		// Only proven terminal records are evictable. Unverified cleanup remains
		// evidence even when the result blob contains a historical expiry.
		now := time.Now().UTC().UnixMilli()
		// Resume already-hidden victims even if current pressure disappeared.
		err = tx.QueryRowContext(ctx, "SELECT id,accounted_bytes,expires_at FROM runs WHERE evicting=1 AND "+acceptedCleanupSQL+" ORDER BY id LIMIT 1").Scan(&id, &charged, &expiry)
		if errors.Is(err, sql.ErrNoRows) && !expire && !pressure {
			break
		}
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.QueryRowContext(ctx, "SELECT id,accounted_bytes,expires_at FROM runs WHERE terminal=1 AND "+acceptedCleanupSQL+" AND expires_at<=? ORDER BY expires_at,id LIMIT 1", now).Scan(&id, &charged, &expiry)
		}
		if errors.Is(err, sql.ErrNoRows) && pressure {
			err = tx.QueryRowContext(ctx, "SELECT id,accounted_bytes,expires_at FROM runs WHERE terminal=1 AND "+acceptedCleanupSQL+" ORDER BY completed_at,id LIMIT 1").Scan(&id, &charged, &expiry)
		}
		if errors.Is(err, sql.ErrNoRows) {
			removed, debit, deleted, err := retainBatch(ctx, tx, pressure, expire, remainingRows)
			if err != nil {
				return report, err
			}
			if removed {
				report.AccountedBytes -= debit
				report.Evicted++
				report.DeletedRows += deleted
				remainingRows -= int(deleted)
				retiredBytes += debit
				report.AccountedBytes, err = refreshIndexDebit(ctx, tx)
				if err != nil {
					return report, err
				}
				if remainingRows <= 0 || retiredBytes >= 4<<20 {
					break
				}
				continue
			}
			break
		}
		if err != nil || charged < 0 || charged > report.AccountedBytes {
			return report, storageError()
		}
		row, err := retainedCleanup(ctx, tx, id)
		if err != nil {
			return report, err
		}
		if err := validateRunManifest(ctx, tx, id, row, true); err != nil {
			return report, err
		}
		// Hide the victim before deleting bounded payload batches. Keep its full
		// debit until final deletion: partial cleanup never creates free quota.
		if _, err := tx.ExecContext(ctx, "UPDATE runs SET evicting=1 WHERE id=? AND terminal=1", id); err != nil {
			return report, storageError()
		}
		deleted, pending, err := deleteIndexWindows(ctx, tx, id, remainingRows)
		if err != nil {
			return report, err
		}
		report.DeletedRows += deleted
		remainingRows -= int(deleted)
		// Fixed internal table names only. The primary-key prefixes bound each
		// rowid selection to this run; parent CASCADE is used only when empty.
		for _, table := range []string{"run_projection", "run_events"} {
			result, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE rowid IN (SELECT rowid FROM "+table+" WHERE run_id=? LIMIT ?)", id, min(evictionRows, remainingRows))
			if err != nil {
				return report, storageError()
			}
			n, err := result.RowsAffected()
			if err != nil {
				return report, storageError()
			}
			report.DeletedRows += n
			remainingRows -= int(n)
			var more bool
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM "+table+" WHERE run_id=?)", id).Scan(&more); err != nil {
				return report, storageError()
			}
			pending = pending || more
		}
		if pending {
			report.PendingDeletion = true
			break
		}
		// Only bounded checkpoints, index progress and one claim remain.
		if _, err := tx.ExecContext(ctx, "DELETE FROM runs WHERE id=? AND terminal=1", id); err != nil {
			return report, storageError()
		}
		report.AccountedBytes -= charged
		retiredBytes += charged
		if _, err := tx.ExecContext(ctx, "UPDATE run_meta SET accounted_bytes=? WHERE singleton=1", report.AccountedBytes); err != nil {
			return report, storageError()
		}
		report.AccountedBytes, err = refreshIndexDebit(ctx, tx)
		if err != nil {
			return report, err
		}
		if expiry <= now {
			report.Expired++
		} else {
			report.Evicted++
		}
		// Bound work across victims, including their encoded program blobs.
		if remainingRows == 0 || retiredBytes >= 4<<20 {
			break
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE run_meta SET accounted_bytes=? WHERE singleton=1", report.AccountedBytes); err != nil {
		return report, storageError()
	}
	report.AccountedBytes, err = refreshIndexDebit(ctx, tx)
	if err != nil {
		return report, err
	}
	report.More = report.AccountedBytes > limit-needed
	return report, nil
}

func charge(ctx context.Context, tx *sql.Tx, id string, amount, limit int64, expire bool) error {
	// Callers own the budget: admission has its bounded recovery/admission
	// context, while appendBatch already supplies the 250-ms capture deadline.
	// Reapplying that shorter deadline here can cancel valid large admissions.
	for {
		report, err := retain(ctx, tx, limit, amount, expire)
		if err != nil {
			return err
		}
		if !report.More {
			break
		}
		if report.Expired+report.Evicted == 0 && report.DeletedRows == 0 {
			return quotaError()
		}
		// Projection can amplify a binary block into many small anchored rows.
		// Continue bounded passes under this write deadline instead of mistaking
		// the first 64-victim boundary for actual quota exhaustion.
	}
	if _, err := tx.ExecContext(ctx, "UPDATE run_meta SET accounted_bytes=accounted_bytes+? WHERE singleton=1", amount); err != nil {
		return metadataFailure(ctx, "charging retained run bytes")
	}
	if _, err := tx.ExecContext(ctx, "UPDATE runs SET accounted_bytes=accounted_bytes+? WHERE id=?", amount, id); err != nil {
		return metadataFailure(ctx, "charging run metadata")
	}
	used, err := refreshIndexDebit(ctx, tx)
	if err != nil {
		return err
	}
	if used > limit {
		return quotaError()
	}
	return nil
}

// prepareCapacity commits bounded eviction progress before admitting a large
// program. JSON expansion can require more than 64 small completed runs' space;
// rolling those deletions back with a rejected admission would never progress.
// Admission still rechecks/debits atomically because another writer may win the
// interval between these transactions. No process has been authorized yet.
func (s *Store) prepareCapacity(ctx context.Context, needed int64) error {
	limit := s.policy.MaxRetainedBytes
	if needed < 0 || needed > limit {
		return quotaError()
	}
	var used int64
	if err := s.db.QueryRowContext(ctx, "SELECT accounted_bytes FROM run_meta WHERE singleton=1").Scan(&used); err != nil {
		return storageError()
	}
	if used <= limit-needed {
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		writeCtx, cancel := context.WithTimeout(ctx, WriteTimeout)
		tx, err := sqliteutil.BeginImmediateContext(writeCtx, s.db, "runs")
		if err != nil {
			cancel()
			return storageError()
		}
		report, err := retain(writeCtx, tx, limit, needed, true)
		if err != nil {
			tx.Rollback()
			cancel()
			return err
		}
		err = tx.Commit()
		cancel()
		if err != nil {
			return storageError()
		}
		if !report.More {
			return nil
		}
		if report.Expired+report.Evicted == 0 && report.DeletedRows == 0 {
			return quotaError()
		}
	}
}

// Maintain removes at most 64 small completed runs, or advances one larger
// tombstoned victim by bounded payload batches, then attempts a passive
// checkpoint. Busy readers/writers retain their WAL; no sidecar is ever removed.
func (s *Store) Maintain(ctx context.Context) (RetentionReport, error) {
	ctx, cancel := context.WithTimeout(ctx, WriteTimeout)
	defer cancel()
	tx, err := sqliteutil.BeginImmediateContext(ctx, s.db, "runs")
	if err != nil {
		return RetentionReport{}, storageError()
	}
	defer tx.Rollback()
	report, err := retain(ctx, tx, s.policy.MaxRetainedBytes, 0, true)
	if err != nil {
		return report, err
	}
	if err := tx.Commit(); err != nil {
		return report, storageError()
	}
	var busy, log, checkpointed int
	if err := s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &log, &checkpointed); err != nil {
		return report, storageError()
	}
	return report, nil
}

// reclaim is called only under a fresh exclusive lifecycle lease. Incremental
// vacuum bounds work to 256 pages; it never rewrites the entire database.
func (s *Store) reclaim(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "PRAGMA incremental_vacuum(256)")
	if err != nil {
		return storageError()
	}
	defer rows.Close()
	for rows.Next() {
	}
	if rows.Err() != nil {
		return storageError()
	}
	return nil
}
