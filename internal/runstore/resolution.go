package runstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/sqliteutil"
)

// A disposition is administrative authority, never execution cleanup proof.
// Callers must validate retained dispositions before using this SQL exclusion
// to enumerate unfinished work in the same transaction.
const undisposedRunSQL = `NOT EXISTS(SELECT 1 FROM run_resolutions d WHERE d.run_id=runs.id)`

type runResolution struct {
	RunID, StoreID, Generation, BatchID, ReceiptID, IncidentID  string
	Slot, TTL, Version                                          int
	Policy, ResolvedAt, ExpiresAt, AuditExpiresAt, ChargedBytes int64
	Cleanup                                                     executor.CleanupEvidence
	Terminal                                                    bool
	Original, Digest                                            []byte
	PayloadState                                                string
	EvictedAt                                                   sql.NullInt64
	EvictionCause                                               string
	Receipt                                                     recoveryReceipt
}

// readRunResolution validates the immutable binding against the retained row,
// receipt and incident membership. Absence is distinct from invalid evidence.
// A missing payload is legitimate only after its durable evicted transition.
// This reader does not perform eviction or infer it from an absent row.
func readRunResolution(ctx context.Context, q runQuerier, id string) (*runResolution, error) {
	d := &runResolution{RunID: id}
	var auditMatches, rowMatches, present, evicting bool
	err := q.QueryRowContext(ctx, `SELECT d.store_id,d.generation,d.slot,COALESCE(d.batch_id,''),d.receipt_id,d.incident_id,d.version,
 CASE WHEN length(d.original_result) BETWEEN 1 AND 8192 THEN d.original_result ELSE NULL END,d.result_sha256,
 d.cleanup_scope,d.cleanup,d.terminal,d.ttl_hours,d.policy_revision,d.resolved_at,d.retention_expires_at,d.audit_expires_at,d.charged_bytes,d.payload_state,d.evicted_at,COALESCE(d.eviction_cause,''),
 EXISTS(SELECT 1 FROM run_meta meta
 JOIN run_incidents i ON i.id=d.incident_id JOIN run_incident_resolutions m ON m.incident_id=i.id AND m.receipt_id=d.receipt_id
 WHERE meta.singleton=1 AND meta.store_id=d.store_id
 AND i.subject_kind='run' AND i.run_id=d.run_id AND i.generation=d.generation AND i.slot=d.slot
 AND COALESCE(i.batch_id,'')=COALESCE(d.batch_id,'') AND i.receipt_id=d.receipt_id
 AND i.audit_expires_at=d.audit_expires_at AND m.audit_expires_at=d.audit_expires_at),
 EXISTS(SELECT 1 FROM runs r WHERE r.id=d.run_id AND r.generation=d.generation AND r.slot=d.slot
 AND r.result=d.original_result AND r.cleanup_scope=d.cleanup_scope AND r.cleanup=d.cleanup AND r.terminal=d.terminal
 AND json_extract(CAST(r.program AS TEXT),'$.ttl_hours')=d.ttl_hours
 AND json_extract(CAST(r.program AS TEXT),'$.policy_revision')=d.policy_revision),
 EXISTS(SELECT 1 FROM runs r WHERE r.id=d.run_id),
 EXISTS(SELECT 1 FROM runs r WHERE r.id=d.run_id AND r.evicting=1)
 FROM run_resolutions d WHERE d.run_id=?`, id).Scan(&d.StoreID, &d.Generation, &d.Slot, &d.BatchID, &d.ReceiptID, &d.IncidentID, &d.Version,
		&d.Original, &d.Digest, &d.Cleanup.Scope, &d.Cleanup.State, &d.Terminal, &d.TTL, &d.Policy, &d.ResolvedAt, &d.ExpiresAt, &d.AuditExpiresAt, &d.ChargedBytes, &d.PayloadState, &d.EvictedAt, &d.EvictionCause, &auditMatches, &rowMatches, &present, &evicting)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, metadataReadError(err)
	}
	digest := sha256.Sum256(d.Original)
	var original Result
	if !auditMatches || present && !rowMatches || !d.validPayload(present, evicting) || d.Version != 1 || uuid.Validate(id) != nil || uuid.Validate(d.StoreID) != nil || uuid.Validate(d.Generation) != nil ||
		uuid.Validate(d.ReceiptID) != nil || uuid.Validate(d.IncidentID) != nil || d.Slot < 0 || d.Slot >= OwnerSlots ||
		d.TTL < 1 || d.TTL > 2562047 || d.Policy < 1 || d.Terminal || d.Cleanup.State != executor.CleanupPending ||
		d.ChargedBytes != recoveryDispositionReserve || !bytes.Equal(digest[:], d.Digest) || decodeRunResult(d.Original, d.Cleanup, &original) != nil ||
		validateTerminal(d.Cleanup, original, d.Terminal) != nil || original.RunID == nil || *original.RunID != id || original.OwnerID != d.Generation || original.BatchID != d.BatchID ||
		d.ExpiresAt != time.Unix(0, d.ResolvedAt).Add(time.Duration(d.TTL)*time.Hour).UnixMilli()+1 || d.AuditExpiresAt < d.ExpiresAt {
		return nil, recoveryInconsistent("run disposition binding")
	}
	d.Receipt, err = readRecoveryReceipt(ctx, q, d.ReceiptID, "")
	if err != nil {
		var typed *Error
		if errors.As(err, &typed) && typed.Code == "receipt_unavailable" {
			return nil, recoveryInconsistent("missing disposition receipt")
		}
		return nil, errors.Join(err, ctx.Err())
	}
	if d.Receipt.StoreID != d.StoreID || d.Receipt.AppliedAt != d.ResolvedAt || d.Receipt.AuditExpiresAt < d.AuditExpiresAt {
		return nil, recoveryInconsistent("run disposition receipt")
	}
	if d.Receipt.Pruning {
		var manifest bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_batches WHERE id=?)
 OR EXISTS(SELECT 1 FROM run_batch_children WHERE run_id=?)`, d.BatchID, id).Scan(&manifest); err != nil {
			return nil, metadataReadError(err)
		}
		if present || d.PayloadState != "evicted" || manifest {
			return nil, recoveryInconsistent("referenced pruning disposition")
		}
		// Dispositions are pruned before their memberships/incidents, so a
		// remaining evicted member can still be validated. It authorizes no live
		// consumer; missing-output readers must report unavailable, not damage.
	}
	if present {
		var state string
		err := q.QueryRowContext(ctx, "SELECT state FROM run_search_state WHERE run_id=?", id).Scan(&state)
		if err != nil && !(d.PayloadState == "evicting" && errors.Is(err, sql.ErrNoRows)) {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, recoveryInconsistent("missing disposed run index state")
			}
			return nil, metadataReadError(err)
		}
		if err == nil && !validResolutionIndexState(state) {
			return nil, recoveryInconsistent("disposed run index state")
		}
	}
	if err := validateDispositionManifest(ctx, q, d); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *runResolution) validPayload(present, evicting bool) bool {
	knownCause := d.EvictionCause == "ttl" || d.EvictionCause == "quota"
	switch d.PayloadState {
	case "retained":
		return present && !evicting && !d.EvictedAt.Valid && d.EvictionCause == ""
	case "evicting":
		return present && evicting && !d.EvictedAt.Valid && knownCause
	case "evicted":
		return !present && d.EvictedAt.Valid && d.EvictedAt.Int64 >= d.ResolvedAt && knownCause
	default:
		return false
	}
}

// Retained manifests are independent historical copies. Index/projection views
// may differ, but execution identity, outcome and captured-prefix facts may not.
func sameDispositionExecution(manifest, recorded Result) bool {
	// Released schema 9 (v0.18.2) index writers could persist loadRun's known
	// unverified annotation only in the run row. Recognize that same transform
	// as the schema-8 proof migrator, without rewriting either historical copy.
	// Counts, cursors, identity, state, exit/completion and proof still compare.
	if !(recorded.CompletedAt != nil && CleanupAccepted(recorded.Cleanup)) && recorded.Error != nil && recorded.Error.Code == "cleanup_unverified" {
		markUnverified(&manifest)
		manifest.Error = recorded.Error // diagnostic wording is not cleanup proof
	}
	for _, r := range []*Result{&manifest, &recorded} {
		r.Index, r.Redaction = Index{}, Projection{}
		r.Recovery, r.Capacity, r.Preview, r.MetadataOmitted = nil, nil, "", false
	}
	x, err := json.Marshal(manifest)
	if err != nil {
		return false
	}
	y, err := json.Marshal(recorded)
	return err == nil && bytes.Equal(x, y)
}

func validateDispositionManifest(ctx context.Context, q runQuerier, d *runResolution) error {
	var batch, generation string
	var slot int
	var encoded []byte
	var terminal bool
	err := q.QueryRowContext(ctx, `SELECT c.batch_id,b.generation,b.slot,CASE WHEN length(c.result) BETWEEN 1 AND 8192 THEN c.result ELSE NULL END,c.terminal
 FROM run_batch_children c LEFT JOIN run_batches b ON b.id=c.batch_id WHERE c.run_id=?`, d.RunID).Scan(&batch, &generation, &slot, &encoded, &terminal)
	if errors.Is(err, sql.ErrNoRows) {
		if d.BatchID == "" {
			return nil
		}
		var retained bool
		if err := q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM run_batches WHERE id=?)", d.BatchID).Scan(&retained); err != nil {
			return metadataReadError(err)
		}
		if !retained {
			return nil
		} // the whole manifest may legitimately age out
		return recoveryInconsistent("missing disposition child manifest")
	}
	if err != nil {
		return metadataReadError(err)
	}
	saved, err := decodeManifest(encoded)
	var original Result
	if err != nil || decodeRunResult(d.Original, d.Cleanup, &original) != nil || batch != d.BatchID || generation != d.Generation || slot != d.Slot || terminal != d.Terminal || !sameDispositionExecution(saved, original) {
		return recoveryInconsistent("disposition child manifest binding")
	}
	return nil
}

func validResolutionIndexState(state string) bool {
	return state == "not_requested" || state == "building" || state == "partial" || state == "ready" || state == "failed"
}

// Normalize the existing index writer's view into the bounded side-state
// schema. Planning and application use the same check; no historical blob is
// rewritten and no stored backend message is copied into the side state.
func resolutionIndex(index Index) (Index, error) {
	code := ""
	if index.Error != nil {
		code = "index_unavailable"
		if index.Error.Code == "index_quota_blocked" || index.Error.Code == "index_capacity" {
			code = "index_capacity"
		}
	}
	if index.State == "quota_blocked" {
		index.State, code = "failed", "index_capacity"
	}
	if !validResolutionIndexState(index.State) || index.CoverageCursor != nil && len(*index.CoverageCursor) > 1024 {
		return Index{}, recoveryInconsistent("index snapshot")
	}
	index.Error = nil
	if code != "" {
		index.Error = &Error{Code: code}
	}
	return index, nil
}

// operationallyClosed is deliberately separate from validateTerminal and
// CleanupAccepted. It validates administrative authority even if other fields
// have been changed to appear execution-terminal after disposition.
func operationallyClosed(ctx context.Context, q runQuerier, id string, cleanup executor.CleanupEvidence, result Result, terminal bool) (bool, error) {
	if err := validateTerminal(cleanup, result, terminal); err != nil {
		return false, err
	}
	d, err := readRunResolution(ctx, q, id)
	return err == nil && (terminal || d != nil), err
}

func rejectDisposedMutation(ctx context.Context, q runQuerier, id string) error {
	var disposed bool
	if err := q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM run_resolutions WHERE run_id=?)", id).Scan(&disposed); err != nil {
		return storageError()
	}
	if disposed {
		return failure("run_unavailable", "operator-resolved execution evidence is immutable")
	}
	return nil
}

func validateSlotResolutions(ctx context.Context, tx *sql.Tx, slot int) error {
	after := ""
	for {
		rows, err := tx.QueryContext(ctx, "SELECT run_id FROM run_resolutions WHERE slot=? AND run_id>? ORDER BY run_id LIMIT 64", slot, after)
		if err != nil {
			return storageError()
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return storageError()
			}
			ids = append(ids, id)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return storageError()
		}
		for _, id := range ids {
			if _, err := readRunResolution(ctx, tx, id); err != nil {
				return err
			}
			after = id
		}
		if len(ids) < 64 {
			return nil
		}
	}
}

// The caller holds recovery.lock throughout validation and the subsequent
// write transaction (and the slot lease when reclaiming it). Disposed evidence
// is immutable; ordinary retention may only hide/delete its payload/manifest.
// Audit pruning and any new administrative authority change the recovery epoch,
// rechecked under the writer lock before using the validated exclusion. Keeping
// the full validation in a read snapshot lets unrelated capture writers proceed.
func (s *Store) validateResolutionsSnapshot(ctx context.Context, slots ...int) (int64, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, storageError()
	}
	defer tx.Rollback()
	var epoch int64
	if err := tx.QueryRowContext(ctx, "SELECT recovery_revision FROM run_meta WHERE singleton=1").Scan(&epoch); err != nil {
		return 0, storageError()
	}
	for _, slot := range slots {
		if err := validateSlotResolutions(ctx, tx, slot); err != nil {
			return 0, err
		}
	}
	return epoch, nil
}

func recheckRecoveryRevision(ctx context.Context, tx *sql.Tx, expected int64) error {
	var current int64
	if err := tx.QueryRowContext(ctx, "SELECT recovery_revision FROM run_meta WHERE singleton=1").Scan(&current); err != nil {
		return storageError()
	}
	if current != expected {
		return errRecoverySnapshotChanged
	}
	return nil
}

var errRecoverySnapshotChanged = errors.New("recovery snapshot changed")

// Retry only stale read validation, before any mutation or command admission.
// The caller supplies its existing operation deadline and holds recovery.lock.
// A peer's legitimate audit GC or incident publication must not turn a single
// overlapping commit into a startup failure, or force long writer-lock holds.
func beginRecoveryWrite(ctx context.Context, db *sql.DB, validate func(context.Context) (int64, error)) (*sql.Tx, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(failure("storage_busy", "recovery metadata kept changing; retry after maintenance completes"), err)
		}
		epoch, err := validate(ctx)
		if err != nil {
			return nil, err
		}
		tx, err := sqliteutil.BeginImmediateContext(ctx, db, "runs")
		if err != nil {
			return nil, metadataFailure(ctx, "starting recovery write")
		}
		err = recheckRecoveryRevision(ctx, tx, epoch)
		if err == nil {
			return tx, nil
		}
		rollbackErr := tx.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return nil, metadataFailure(ctx, "rolling back stale recovery validation")
		}
		if !errors.Is(err, errRecoverySnapshotChanged) {
			return nil, err
		}
	}
}
