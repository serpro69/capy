package runstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/sqliteutil"
	"golang.org/x/sys/unix"
)

const recoveryBatch = 64
const recoveryTimeout = 3 * time.Second
const maintenanceInterval = 60 * time.Second

// RecoveryReport distinguishes incomplete bounded work from unverified process
// cleanup. Neither condition authorizes reusing the affected slot.
type RecoveryReport struct {
	ScannedSlots, RecoveredRuns, ReleasedClaims, UnverifiedSlots, DeferredSlots int
}

// Reconcile visits the complete fixed registry without waiting for held leases.
// Each free slot stays locked through generation/proof checks and bounded
// transactions. Quarantined stores stay readable; admission checks the persisted
// quarantine separately. No recovery path signals a process or resumes code.
func (s *Store) Reconcile(ctx context.Context) (RecoveryReport, error) {
	ctx, cancel := context.WithTimeout(ctx, recoveryTimeout)
	defer cancel()
	lease, err := s.recoveryLease(ctx)
	if err != nil {
		return RecoveryReport{}, err
	}
	defer lease.Close()
	return s.reconcile(ctx)
}

// recoveryLease serializes temporary scan locks with generation publication and
// admission. Otherwise a concurrent scanner could mistake a scanner-held stale
// slot for a live owner and admit work before quarantine has been committed.
// Every caller opens a separate OFD; sharing a descriptor would not serialize
// goroutines. The permanent file is never removed or replaced.
func (s *Store) recoveryLease(ctx context.Context) (*os.File, error) {
	lease, err := openRegular(filepath.Join(filepath.Dir(s.path), "recovery.lock"), false)
	if err != nil {
		return nil, storageError()
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	waited := false
	for {
		if err := ctx.Err(); err != nil {
			lease.Close()
			if waited {
				return nil, errors.Join(failure("storage_busy", "run registry lease is busy"), err)
			}
			return nil, err
		}
		err = unix.Flock(int(lease.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return lease, nil
		}
		if err != unix.EWOULDBLOCK {
			lease.Close()
			return nil, storageError()
		}
		waited = true
		select {
		case <-ctx.Done():
			lease.Close()
			return nil, errors.Join(failure("storage_busy", "run registry lease is busy"), ctx.Err())
		case <-ticker.C:
		}
	}
}

// The caller holds recovery.lock throughout this pass and any following
// admission/generation publication. Capture writes do not take this lock.
func (s *Store) reconcile(ctx context.Context) (RecoveryReport, error) {
	if s.recoveryReadOnly != nil {
		return RecoveryReport{}, s.recoveryReadOnly
	}
	var report RecoveryReport
	for slot := 0; slot < OwnerSlots; slot++ {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		lease, err := s.tryOwnerLease(slot)
		if err != nil {
			return report, err
		}
		report.ScannedSlots++
		if lease == nil {
			continue
		}
		part, err := s.reconcileSlot(ctx, slot)
		closeErr := lease.Close()
		if err != nil || closeErr != nil {
			return report, errors.Join(err, closeErr)
		}
		report.RecoveredRuns += part.RecoveredRuns
		report.ReleasedClaims += part.ReleasedClaims
		report.UnverifiedSlots += part.UnverifiedSlots
		report.DeferredSlots += part.DeferredSlots
	}
	if err := s.recordLegacyMarkers(ctx); err != nil {
		return report, err
	}
	return report, nil
}

// The caller owns this slot's exclusive lease. Queries use its current recorded
// generation, never one cached before acquiring the lease.
func (s *Store) reconcileSlot(ctx context.Context, slot int) (RecoveryReport, error) {
	var report RecoveryReport
	var generation string
	err := s.db.QueryRowContext(ctx, "SELECT generation FROM run_owners WHERE slot=?", slot).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		return report, nil
	}
	if err != nil {
		return report, storageError()
	}
	// Avoid taking SQLite's writer lock for the usual already-clean slot.
	var work bool
	workQuery := `SELECT EXISTS(SELECT 1 FROM runs WHERE slot=? AND terminal=0)
OR EXISTS(SELECT 1 FROM run_work_claims WHERE slot=?)
 OR EXISTS(SELECT 1 FROM run_batch_children c JOIN run_batches b ON b.id=c.batch_id LEFT JOIN runs r ON r.id=c.run_id WHERE b.slot=? AND c.terminal=0 AND r.id IS NULL)`
	args := []any{slot, slot, slot}
	if s.policy.SchemaVersion >= 10 {
		workQuery += " OR EXISTS(SELECT 1 FROM run_resolutions WHERE slot=?)"
		args = append(args, slot)
	}
	err = s.db.QueryRowContext(ctx, workQuery, args...).Scan(&work)
	if err != nil || !work {
		if err != nil {
			return report, storageError()
		}
		return report, nil
	}
	// Recovery may rewrite a retained program's large encrypted record. It is
	// bounded by the existing recovery deadline, not the capture append budget.
	// WithTimeout preserves any earlier caller deadline; capture writes and
	// helper finalization retain their separate 250-ms limits.
	writeCtx, cancel := context.WithTimeout(ctx, recoveryTimeout)
	defer cancel()
	tx, err := sqliteutil.BeginImmediateContext(writeCtx, s.db, "runs")
	if err != nil {
		return report, storageError()
	}
	defer tx.Rollback()
	var current string
	if err := tx.QueryRowContext(writeCtx, "SELECT generation FROM run_owners WHERE slot=?", slot).Scan(&current); err != nil || current != generation {
		return report, storageError()
	}
	if err := validateSlotResolutions(writeCtx, tx, slot); err != nil {
		return report, err
	}
	// Inspect every unfinished proof, even beyond the bounded recovery batch.
	// A generation mismatch also requires operator recovery; never rewrite it.
	var unsafe bool
	err = tx.QueryRowContext(writeCtx, `SELECT EXISTS(SELECT 1 FROM runs WHERE slot=? AND terminal=0 AND `+undisposedRunSQL+` AND (NOT `+acceptedCleanupSQL+` OR generation<>?))
OR EXISTS(SELECT 1 FROM run_work_claims WHERE slot=? AND generation<>?)`, slot, generation, slot, generation).Scan(&unsafe)
	if err != nil {
		return report, storageError()
	}
	changed, recoverable, err := observeSlotRuns(writeCtx, tx, slot, generation)
	if err != nil {
		return report, err
	}
	inconsistent, err := observeSlotInconsistencies(writeCtx, tx, slot, generation)
	if err != nil {
		return report, err
	}
	changed = changed || inconsistent
	var recorded bool
	if err := tx.QueryRowContext(writeCtx, "SELECT EXISTS(SELECT 1 FROM run_incidents WHERE slot=? AND receipt_id IS NULL)", slot).Scan(&recorded); err != nil {
		return report, storageError()
	}
	if unsafe || recorded {
		marked, err := quarantineMarkers(writeCtx, tx, &slot, generation)
		if err != nil {
			return report, err
		}
		changed = changed || marked
		report.UnverifiedSlots = 1
	}
	// Safe siblings have their own proof. A pending sibling still prevents
	// reuse, but cannot prevent recording these independent outcomes/TTLs.
	ids, err := recoveryIDs(writeCtx, tx, `SELECT id FROM runs WHERE slot=? AND generation=? AND terminal=0 AND `+acceptedCleanupSQL+` AND NOT EXISTS(SELECT 1 FROM run_incidents i WHERE i.run_id=runs.id AND i.receipt_id IS NULL AND i.reason IN ('generation_mismatch','invalid_proof','invalid_manifest','structural_inconsistency')) ORDER BY id LIMIT ?`, slot, generation)
	if err != nil {
		return report, err
	}
	for _, id := range ids {
		if !recoverable[id] {
			continue
		}
		if err := s.recoverRun(writeCtx, tx, id); err != nil {
			return report, err
		}
		report.RecoveredRuns++
	}
	// Index workers own no external command group. Their matching generation
	// and free exclusive lease prove the work is abandoned even when command
	// cleanup in this generation remains unknown. Preserve those run proofs.
	claims, err := recoveryIDs(writeCtx, tx, "SELECT run_id FROM run_work_claims WHERE slot=? AND generation=? ORDER BY run_id LIMIT ?", slot, generation)
	if err != nil {
		return report, err
	}
	for _, id := range claims {
		if err := s.recoverClaim(writeCtx, tx, id, slot, generation); err != nil {
			return report, err
		}
		report.ReleasedClaims++
	}
	var pending bool
	if err := tx.QueryRowContext(writeCtx, `SELECT EXISTS(SELECT 1 FROM runs WHERE slot=? AND generation=? AND terminal=0 AND `+acceptedCleanupSQL+`)
OR EXISTS(SELECT 1 FROM run_work_claims WHERE slot=? AND generation=?)`, slot, generation, slot, generation).Scan(&pending); err != nil {
		return report, storageError()
	}
	if pending {
		report.DeferredSlots = 1
	}
	if err := finishIncidentChanges(writeCtx, tx, changed); err != nil {
		return report, err
	}
	if err := tx.Commit(); err != nil {
		return RecoveryReport{}, metadataFailure(writeCtx, "committing recovered metadata")
	}
	return report, nil
}

func recoveryIDs(ctx context.Context, tx *sql.Tx, query string, slot int, generation string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, slot, generation, recoveryBatch)
	if err != nil {
		return nil, storageError()
	}
	defer rows.Close()
	ids := make([]string, 0, recoveryBatch)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, storageError()
		}
		ids = append(ids, id)
	}
	if rows.Err() != nil {
		return nil, storageError()
	}
	return ids, nil
}

func (s *Store) recoverRun(ctx context.Context, tx *sql.Tx, id string) error {
	if err := rejectDisposedMutation(ctx, tx, id); err != nil {
		return err
	}
	// Read directly: expiry must not hide unfinished proof records from recovery.
	var result []byte
	row := Admitted{ID: id}
	if err := tx.QueryRowContext(ctx, "SELECT json_extract(CAST(program AS TEXT),'$.ttl_hours'),result,generation,cleanup_scope,cleanup FROM runs WHERE id=?", id).Scan(&row.Program.TTLHours, &result, &row.Generation, &row.Cleanup.Scope, &row.Cleanup.State); err != nil {
		return metadataFailure(ctx, "reading recovery metadata")
	}
	if decodeRunResult(result, row.Cleanup, &row.Result) != nil || row.Program.TTLHours <= 0 || int64(row.Program.TTLHours) > (1<<63-1)/int64(time.Hour) {
		return storageError()
	}
	if !CleanupAccepted(row.Cleanup) {
		return failure("cleanup_unverified", "run recovery requires accepted scoped evidence")
	}
	if err := validateRunManifest(ctx, tx, id, row.Cleanup, false); err != nil {
		return err
	}
	row.Result.Cleanup = row.Cleanup
	for _, stream := range []string{"stdout", "stderr"} {
		if err := s.project(ctx, tx, &row, stream, nil, true); err != nil {
			return err
		}
	}
	if err := s.projectionState(ctx, tx, &row); err != nil {
		return err
	}
	if row.chargeBytes > runReserve/2 {
		return storageError()
	}
	reason := "owner_lost"
	row.Result.State, row.Result.Reason = "interrupted", &reason
	row.Result.ExitCode, row.Result.Signal = nil, nil
	row.Result.Error = nil
	now := time.Now().UTC()
	if row.Result.CompletedAt == nil {
		row.Result.CompletedAt = &now
	}
	if row.Result.ExpiresAt == nil {
		expires := row.Result.CompletedAt.Add(time.Duration(row.Program.TTLHours) * time.Hour)
		row.Result.ExpiresAt = &expires
	}
	row.Result.Capture.TerminalPersisted = true
	row.Result.Capture.State = "incomplete"
	if row.Cleanup.State == executor.CleanupNotSpawned {
		row.Result.Capture.State = "not_started"
	}
	row.Result.Capture.Reason = &reason
	encoded, err := json.Marshal(row.Result)
	if err != nil {
		return storageError()
	}
	if _, err := tx.ExecContext(ctx, "UPDATE runs SET result=?,terminal=1,completed_at=?,expires_at=? WHERE id=? AND generation=? AND cleanup_scope=?", encoded, row.Result.CompletedAt.UnixNano(), row.Result.ExpiresAt.UnixMilli()+1, id, row.Generation, row.Cleanup.Scope); err != nil {
		return metadataFailure(ctx, "writing recovered metadata")
	}
	return syncBatchChild(ctx, tx, row.Result, true)
}

// Keep backend/stored text private while exposing the failed phase and causal
// cancellation. These diagnostics distinguish a deadline from path permissions.
func metadataFailure(ctx context.Context, phase string) error {
	return fmt.Errorf("%s: %w", phase, errors.Join(storageError(), ctx.Err()))
}

func (s *Store) maintenance(ctx context.Context, ticks <-chan time.Time) {
	defer close(s.maintenanceDone)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			if _, err := s.Reconcile(ctx); err != nil && ctx.Err() == nil {
				// The caller owns diagnostics and its response budget. Keep one
				// error for Close to return after joining this worker, rather than
				// writing unbudgeted recurring stderr from a long CLI invocation.
				s.maintenanceErr = err
			}
			if _, err := s.Maintain(ctx); err != nil && ctx.Err() == nil {
				s.maintenanceErr = err
			}
		}
	}
}
