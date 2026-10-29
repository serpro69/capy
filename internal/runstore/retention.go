package runstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
		if remainingRows < batchDeletionBudget {
			var id string
			var debit, children int64
			err := selectBatchVictim(ctx, tx, false).Scan(&id, &debit, &children)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return report, storageError()
			}
			if err == nil {
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
		// Administrative closure has its own clock and never supplies execution
		// proof. Open run incidents protect evidence even after late completion.
		now := time.Now().UTC().UnixMilli()
		// Resume already-hidden victims even if current pressure disappeared.
		err = tx.QueryRowContext(ctx, `SELECT id,accounted_bytes,COALESCE(
 (SELECT retention_expires_at FROM run_resolutions WHERE run_id=runs.id),expires_at)
 FROM runs WHERE evicting=1 AND ((terminal=1 AND `+acceptedCleanupSQL+`) OR NOT `+undisposedRunSQL+`)
 AND `+unprotectedRunSQL+` ORDER BY id LIMIT 1`).Scan(&id, &charged, &expiry)
		if errors.Is(err, sql.ErrNoRows) && !expire && !pressure {
			break
		}
		if errors.Is(err, sql.ErrNoRows) {
			err = selectRetentionVictim(ctx, tx, now, false).Scan(&id, &charged, &expiry)
		}
		if errors.Is(err, sql.ErrNoRows) && pressure {
			err = selectRetentionVictim(ctx, tx, now, true).Scan(&id, &charged, &expiry)
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
		disposition, err := readRunResolution(ctx, tx, id)
		if err != nil {
			return report, err
		}
		if disposition == nil && !CleanupAccepted(row) {
			return report, recoveryInconsistent("eviction without operational closure")
		}
		if err := validateRunManifest(ctx, tx, id, row, disposition == nil); err != nil {
			return report, err
		}
		// Hide the victim before deleting bounded payload batches. Keep its full
		// debit until final deletion: partial cleanup never creates free quota.
		if _, err := tx.ExecContext(ctx, "UPDATE runs SET evicting=1 WHERE id=?", id); err != nil {
			return report, storageError()
		}
		if disposition != nil && disposition.PayloadState == "retained" {
			cause := "quota"
			if expiry <= now {
				cause = "ttl"
			}
			if _, err := tx.ExecContext(ctx, "UPDATE run_resolutions SET payload_state='evicting',eviction_cause=? WHERE run_id=?", cause, id); err != nil {
				return report, storageError()
			}
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
		// Audit was independently funded at apply, not borrowed from the run
		// reservation. Keep that debit and its dependencies intact. Only this
		// parent's payload reservation is released, with the eviction evidence
		// in the same transaction; run_search_state cascades with payload.
		if disposition != nil {
			// Wall-clock rollback must not create an impossible audit ordering.
			// This bookkeeping clamp changes neither original nor recovery TTLs.
			evictedAt := max(time.Now().UTC().UnixNano(), disposition.ResolvedAt)
			if _, err := tx.ExecContext(ctx, "UPDATE run_resolutions SET payload_state='evicted',evicted_at=? WHERE run_id=?", evictedAt, id); err != nil {
				return report, storageError()
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM runs WHERE id=?", id); err != nil {
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

// Run-scoped attribution is a retention pin, including an inconsistent
// generation that requires separate repair. Owner/store incidents invent no
// run pin. The indexed run_id/receipt_id lookup also serves disposed victims.
const unprotectedRunSQL = `NOT EXISTS(SELECT 1 FROM run_incidents i WHERE i.run_id=runs.id AND i.receipt_id IS NULL)`

// Each indexed branch selects at most one victim before the final comparison;
// sorting the complete union would scan all eligible history under pressure.
// Clocks have identical units: expiry milliseconds, order nanoseconds.
// All returned administrative candidates are fully validated before deletion.
func selectRetentionVictim(ctx context.Context, tx *sql.Tx, now int64, pressure bool) *sql.Row {
	terminalFilter, dispositionFilter, order := " AND expires_at<=?", " AND d.retention_expires_at<=?", "expiry"
	terminalOrder, dispositionOrder := "expires_at,id", "d.retention_expires_at,d.run_id"
	args := []any{now, now}
	if pressure {
		terminalFilter, dispositionFilter, order, args = "", "", "ordered", nil
		terminalOrder, dispositionOrder = "completed_at,id", "d.resolved_at,d.run_id"
	}
	return tx.QueryRowContext(ctx, `SELECT id,charged,expiry FROM (
 SELECT * FROM (
 SELECT id,accounted_bytes AS charged,expires_at AS expiry,completed_at AS ordered FROM runs
 WHERE evicting=0 AND terminal=1 AND `+acceptedCleanupSQL+` AND `+undisposedRunSQL+` AND `+unprotectedRunSQL+terminalFilter+`
 ORDER BY `+terminalOrder+` LIMIT 1)
 UNION ALL
 SELECT * FROM (
 SELECT d.run_id,runs.accounted_bytes,d.retention_expires_at,d.resolved_at FROM run_resolutions d JOIN runs ON runs.id=d.run_id
 WHERE d.payload_state='retained' AND `+unprotectedRunSQL+dispositionFilter+`
 ORDER BY `+dispositionOrder+` LIMIT 1))
 ORDER BY `+order+`,id LIMIT 1`, args...)
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
		// Admission already supplies its bounded operation context. Retention
		// may require more work than a streaming append's 250-ms allowance.
		tx, err := sqliteutil.BeginImmediateContext(ctx, s.db, "runs")
		if err != nil {
			return metadataFailure(ctx, "starting admission retention")
		}
		report, err := retain(ctx, tx, limit, needed, true)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("retaining admission capacity: %w", errors.Join(err, ctx.Err()))
		}
		err = tx.Commit()
		if err != nil {
			return metadataFailure(ctx, "committing admission retention")
		}
		if !report.More {
			return nil
		}
		if report.Expired+report.Evicted == 0 && report.DeletedRows == 0 {
			return quotaError()
		}
	}
}

// Maintain advances bounded payload eviction and unreferenced audit pruning in
// one transaction: a failure rolls back both kinds of progress for this pass.
// It then attempts a passive checkpoint. Busy readers/writers retain their WAL;
// no sidecar is ever removed.
func (s *Store) Maintain(ctx context.Context) (RetentionReport, error) {
	if s.recoveryReadOnly != nil {
		return RetentionReport{}, s.recoveryReadOnly
	}
	// Housekeeping uses the bounded metadata budget, including any earlier
	// caller deadline. Capture append and helper finalization keep WriteTimeout.
	ctx, cancel := context.WithTimeout(ctx, recoveryTimeout)
	defer cancel()
	tx, err := sqliteutil.BeginImmediateContext(ctx, s.db, "runs")
	if err != nil {
		return RetentionReport{}, metadataFailure(ctx, "starting run maintenance")
	}
	defer tx.Rollback()
	report, err := retain(ctx, tx, s.policy.MaxRetainedBytes, 0, true)
	if err != nil {
		return report, fmt.Errorf("maintaining retained runs: %w", errors.Join(err, ctx.Err()))
	}
	deleted, pending, err := pruneRecovery(ctx, tx, min(retentionBatch, 2*evictionRows-int(report.DeletedRows)))
	if err != nil {
		return report, fmt.Errorf("maintaining recovery audit: %w", errors.Join(err, ctx.Err()))
	}
	report.DeletedRows += deleted
	report.PendingDeletion = report.PendingDeletion || pending
	report.AccountedBytes, err = refreshIndexDebit(ctx, tx)
	if err != nil {
		return report, err
	}
	report.More = report.AccountedBytes > s.policy.MaxRetainedBytes
	if err := tx.Commit(); err != nil {
		return report, metadataFailure(ctx, "committing run maintenance")
	}
	var busy, log, checkpointed int
	if err := s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &log, &checkpointed); err != nil {
		return report, metadataFailure(ctx, "checkpointing run maintenance")
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
