package runstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/serpro69/capy/internal/executor"
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
	Receipt                                                     recoveryReceipt
}

// readRunResolution validates the immutable binding against the retained row,
// receipt and incident membership. Absence is distinct from invalid evidence.
// Evicted payload support is added with the bounded retention consumer (Task 11).
func readRunResolution(ctx context.Context, q runQuerier, id string) (*runResolution, error) {
	d := &runResolution{RunID: id}
	var matches bool
	err := q.QueryRowContext(ctx, `SELECT d.store_id,d.generation,d.slot,COALESCE(d.batch_id,''),d.receipt_id,d.incident_id,d.version,
 CASE WHEN length(d.original_result) BETWEEN 1 AND 8192 THEN d.original_result ELSE NULL END,d.result_sha256,
 d.cleanup_scope,d.cleanup,d.terminal,d.ttl_hours,d.policy_revision,d.resolved_at,d.retention_expires_at,d.audit_expires_at,d.charged_bytes,d.payload_state,
 EXISTS(SELECT 1 FROM runs r JOIN run_meta meta ON meta.singleton=1
 JOIN run_incidents i ON i.id=d.incident_id JOIN run_incident_resolutions m ON m.incident_id=i.id AND m.receipt_id=d.receipt_id
 WHERE r.id=d.run_id AND meta.store_id=d.store_id AND r.generation=d.generation AND r.slot=d.slot
 AND r.result=d.original_result AND r.cleanup_scope=d.cleanup_scope AND r.cleanup=d.cleanup AND r.terminal=d.terminal
 AND json_extract(CAST(r.program AS TEXT),'$.ttl_hours')=d.ttl_hours
 AND json_extract(CAST(r.program AS TEXT),'$.policy_revision')=d.policy_revision
 AND i.subject_kind='run' AND i.run_id=d.run_id AND i.generation=d.generation AND i.slot=d.slot
 AND COALESCE(i.batch_id,'')=COALESCE(d.batch_id,'') AND i.receipt_id=d.receipt_id
 AND i.audit_expires_at=d.audit_expires_at AND m.audit_expires_at=d.audit_expires_at
 AND r.evicting=0 AND d.evicted_at IS NULL AND d.eviction_cause IS NULL)
 FROM run_resolutions d WHERE d.run_id=?`, id).Scan(&d.StoreID, &d.Generation, &d.Slot, &d.BatchID, &d.ReceiptID, &d.IncidentID, &d.Version,
		&d.Original, &d.Digest, &d.Cleanup.Scope, &d.Cleanup.State, &d.Terminal, &d.TTL, &d.Policy, &d.ResolvedAt, &d.ExpiresAt, &d.AuditExpiresAt, &d.ChargedBytes, &d.PayloadState, &matches)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, metadataReadError(err)
	}
	digest := sha256.Sum256(d.Original)
	var original Result
	if !matches || d.Version != 1 || uuid.Validate(id) != nil || uuid.Validate(d.StoreID) != nil || uuid.Validate(d.Generation) != nil ||
		uuid.Validate(d.ReceiptID) != nil || uuid.Validate(d.IncidentID) != nil || d.Slot < 0 || d.Slot >= OwnerSlots ||
		d.TTL < 1 || d.TTL > 2562047 || d.Policy < 1 || d.Terminal || d.Cleanup.State != executor.CleanupPending || d.PayloadState != "retained" ||
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
	if d.Receipt.StoreID != d.StoreID || d.Receipt.AppliedAt != d.ResolvedAt || d.Receipt.AuditExpiresAt < d.AuditExpiresAt || d.Receipt.Pruning {
		return nil, recoveryInconsistent("run disposition receipt")
	}
	var state string
	if err := q.QueryRowContext(ctx, "SELECT state FROM run_search_state WHERE run_id=?", id).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, recoveryInconsistent("missing disposed run index state")
		}
		return nil, metadataReadError(err)
	}
	if !validResolutionIndexState(state) {
		return nil, recoveryInconsistent("disposed run index state")
	}
	return d, nil
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
