package runstore

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/serpro69/capy/internal/executor"
)

// Historical markers have no recorded causal run or detection timestamp. Only
// normal attachment/reconciliation publishes their durable fallback attribution.
func (s *Store) recordLegacyMarkers(ctx context.Context, validated *recoveryValidation) error {
	if s.policy.SchemaVersion < 10 {
		return nil
	} // stage-only legacy fixture handles
	const missing = `(SELECT admission_blocked FROM run_meta WHERE singleton=1)=1
 AND NOT EXISTS(SELECT 1 FROM run_incidents WHERE receipt_id IS NULL)
 AND NOT EXISTS(SELECT 1 FROM runs WHERE terminal=0 AND cleanup='pending' AND ` + undisposedRunSQL + `)`
	const owners = `SELECT o.slot,CASE WHEN length(o.generation)=36 THEN o.generation ELSE NULL END FROM run_owners o WHERE o.cleanup_unverified=1
 AND NOT EXISTS(SELECT 1 FROM run_incidents i WHERE i.slot=o.slot AND i.receipt_id IS NULL)
 AND NOT EXISTS(SELECT 1 FROM runs r WHERE r.slot=o.slot AND r.terminal=0 AND r.cleanup='pending' AND NOT EXISTS(SELECT 1 FROM run_resolutions d WHERE d.run_id=r.id))`
	var work bool
	if err := s.db.QueryRowContext(ctx, "SELECT ("+missing+") OR EXISTS("+owners+")").Scan(&work); err != nil {
		return storageError()
	}
	if !work {
		return nil
	}
	// A rare legacy-marker publication also validates all immutable authority
	// without blocking capture writers. recovery.lock prevents concurrent
	// registry publication; the epoch recheck fences audit membership changes.
	var slots [OwnerSlots]int
	for slot := range slots {
		slots[slot] = slot
	}
	tx, err := beginRecoveryWrite(ctx, s.db, func(ctx context.Context) (int64, error) {
		return validated.snapshot(ctx, s, slots[:]...)
	})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	changed := false
	if err := tx.QueryRowContext(ctx, "SELECT "+missing).Scan(&work); err != nil {
		return storageError()
	}
	if work {
		changed, err = observeIncident(ctx, tx, storeIncident())
		if err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, owners+" ORDER BY o.slot LIMIT 64")
	if err != nil {
		return storageError()
	}
	subjects := []incident{}
	for rows.Next() {
		var slot int
		var generation string
		if err := rows.Scan(&slot, &generation); err != nil {
			rows.Close()
			return storageError()
		}
		subjects = append(subjects, ownerIncident(slot, generation, incidentLegacy))
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return storageError()
	}
	for _, i := range subjects {
		c, err := observeIncident(ctx, tx, i)
		if err != nil {
			return err
		}
		changed = changed || c
	}
	if err := finishIncidentChanges(ctx, tx, changed); err != nil {
		return err
	}
	return metadataFailureIf(ctx, tx.Commit())
}

type recoveryCandidate struct {
	id, generation, phase string
	slot                  int
	result                []byte
	cleanup               executor.CleanupEvidence
	terminal              bool
}

// New/changed subjects advance in batches: previously attributed unchanged
// pending runs cannot starve later rows. All-proof admission checks remain whole
// slot queries; a partial attribution batch never implies complete recovery.
func observeSlotRuns(ctx context.Context, tx *sql.Tx, slot int, generation string) (bool, map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT CASE WHEN length(r.id)=36 THEN r.id ELSE '' END,CASE WHEN length(r.generation)=36 THEN r.generation ELSE '' END,r.slot,CASE WHEN length(r.last_phase)<=16 THEN r.last_phase ELSE '' END,
 CASE WHEN length(r.result)<=8192 THEN r.result ELSE NULL END,CASE WHEN length(r.cleanup_scope)<=64 THEN r.cleanup_scope ELSE '' END,CASE WHEN length(r.cleanup)<=16 THEN r.cleanup ELSE '' END,r.terminal
 FROM runs r WHERE r.slot=? AND r.terminal=0 AND NOT EXISTS(SELECT 1 FROM run_resolutions d WHERE d.run_id=r.id) AND ((`+acceptedCleanupSQL+` AND NOT EXISTS(SELECT 1 FROM run_incidents bad WHERE bad.run_id=r.id AND bad.receipt_id IS NULL AND bad.reason IN ('generation_mismatch','invalid_proof','invalid_manifest','structural_inconsistency'))) OR NOT EXISTS(
 SELECT 1 FROM run_incidents i WHERE i.run_id=r.id AND i.generation=r.generation AND i.receipt_id IS NULL
 AND i.last_phase=CASE WHEN r.last_phase IN ('unknown','admitted','queued','authorized','compile','run') THEN r.last_phase ELSE 'unknown' END AND (r.generation=? OR i.reason='generation_mismatch'))) ORDER BY r.id LIMIT 64`, slot, generation)
	if err != nil {
		return false, nil, storageError()
	}
	candidates := []recoveryCandidate{}
	for rows.Next() {
		var c recoveryCandidate
		if err := rows.Scan(&c.id, &c.generation, &c.slot, &c.phase, &c.result, &c.cleanup.Scope, &c.cleanup.State, &c.terminal); err != nil {
			rows.Close()
			return false, nil, inconsistentIncident()
		}
		candidates = append(candidates, c)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return false, nil, storageError()
	}
	changed := false
	recoverable := map[string]bool{}
	for _, c := range candidates {
		i := incident{Kind: "run", Key: "run:" + c.id + ":" + c.generation, RunID: c.id, Generation: c.generation, Slot: &c.slot, Phase: c.phase, Attribution: attributionInferred, Stage: stageReconciliation}
		var r Result
		switch {
		case uuid.Validate(c.id) != nil || uuid.Validate(c.generation) != nil:
			i = ownerIncident(slot, generation, incidentStructure)
			i.Stage = stageReconciliation
		case !validPhase(c.phase):
			i.Phase = "unknown"
			i.Cause = incidentStructure
		case c.generation != generation:
			i.Cause = incidentGeneration
		case decodeRunResult(c.result, c.cleanup, &r) != nil || validateTerminal(c.cleanup, r, c.terminal) != nil:
			i.Cause = incidentProof
		case r.RunID == nil || *r.RunID != c.id || r.OwnerID != c.generation:
			i.Cause = incidentStructure
		default:
			if r.BatchID != "" {
				if uuid.Validate(r.BatchID) != nil {
					i.Cause = incidentManifest
				} else {
					i.BatchID = r.BatchID
				}
			}
			if i.Cause == "" {
				cause, err := reconciliationManifestCause(ctx, tx, c, r)
				if err != nil {
					return false, nil, err
				}
				i.Cause = cause
			}
			if i.Cause == "" && c.cleanup.State == executor.CleanupPending {
				i.Cause = incidentPending
			}
		}
		if i.Cause == "" {
			recoverable[c.id] = true
			continue
		}
		c, err := observeIncident(ctx, tx, i)
		if err != nil {
			return false, nil, err
		}
		changed = changed || c
	}
	return changed, recoverable, nil
}

func observeSlotInconsistencies(ctx context.Context, tx *sql.Tx, slot int, generation string) (bool, error) {
	subjects := []incident{}
	// Claims belong to their historical indexing owner, not the execution owner.
	rows, err := tx.QueryContext(ctx, "SELECT DISTINCT CASE WHEN length(generation)=36 THEN generation ELSE NULL END FROM run_work_claims WHERE slot=? AND generation<>? LIMIT 64", slot, generation)
	if err != nil {
		return false, storageError()
	}
	for rows.Next() {
		var old string
		if err := rows.Scan(&old); err != nil {
			rows.Close()
			return false, storageError()
		}
		i := ownerIncident(slot, old, incidentGeneration)
		i.Stage = stageReconciliation
		i.Attribution = attributionInferred
		if uuid.Validate(old) != nil {
			i = ownerIncident(slot, generation, incidentStructure)
			i.Stage = stageReconciliation
		}
		subjects = append(subjects, i)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return false, storageError()
	}
	rows, err = tx.QueryContext(ctx, `SELECT CASE WHEN length(c.run_id)=36 THEN c.run_id ELSE NULL END,CASE WHEN length(b.id)=36 THEN b.id ELSE NULL END,CASE WHEN length(b.generation)=36 THEN b.generation ELSE NULL END FROM run_batch_children c JOIN run_batches b ON b.id=c.batch_id
 LEFT JOIN runs r ON r.id=c.run_id WHERE b.slot=? AND c.terminal=0 AND r.id IS NULL
 AND NOT EXISTS(SELECT 1 FROM run_resolutions d WHERE d.run_id=c.run_id)
 AND NOT EXISTS(SELECT 1 FROM run_incidents i WHERE i.run_id=c.run_id AND i.generation=b.generation AND i.receipt_id IS NULL)
 ORDER BY c.run_id LIMIT 64`, slot)
	if err != nil {
		return false, storageError()
	}
	for rows.Next() {
		var id, batch, owner string
		if err := rows.Scan(&id, &batch, &owner); err != nil {
			rows.Close()
			return false, storageError()
		}
		i := incident{Kind: "run", Key: "run:" + id + ":" + owner, RunID: id, BatchID: batch, Slot: &slot, Generation: owner, Phase: "unknown", Cause: incidentManifest, Stage: stageReconciliation, Attribution: attributionInferred}
		if !i.valid() {
			i = ownerIncident(slot, generation, incidentStructure)
			i.Stage = stageReconciliation
		}
		subjects = append(subjects, i)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return false, storageError()
	}
	changed := false
	for _, i := range subjects {
		c, err := observeIncident(ctx, tx, i)
		if err != nil {
			return false, err
		}
		changed = changed || c
	}
	return changed, nil
}

func reconciliationManifestCause(ctx context.Context, tx *sql.Tx, c recoveryCandidate, r Result) (IncidentCause, error) {
	var encoded []byte
	var terminal bool
	var batch, generation string
	err := tx.QueryRowContext(ctx, `SELECT CASE WHEN length(c.result)<=8192 THEN c.result ELSE NULL END,c.terminal,c.batch_id,b.generation
 FROM run_batch_children c JOIN run_batches b ON b.id=c.batch_id WHERE c.run_id=?`, c.id).Scan(&encoded, &terminal, &batch, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		if r.BatchID != "" {
			return incidentManifest, nil
		}
		return "", nil
	}
	if err != nil {
		return "", storageError()
	}
	saved, err := decodeManifest(encoded)
	if err != nil || validateManifestCopy(encoded, c.cleanup, c.terminal, terminal) != nil || saved.RunID == nil || *saved.RunID != c.id || saved.OwnerID != c.generation || generation != c.generation || saved.BatchID != batch || batch != r.BatchID {
		return incidentManifest, nil
	}
	return "", nil
}
