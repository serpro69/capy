package runstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/serpro69/capy/internal/sanitize"
	"github.com/serpro69/capy/internal/sqliteutil"
)

// Private until batch, retention and search consumers are integrated (Task 14).
type recoveryApplyOptions struct {
	Plan, Basis, Explanation string
	ConfirmStopped           bool
	Timeout                  time.Duration
}

type recoveryApplyResult struct {
	Status              string
	Receipt             *recoveryReceipt
	AdmissionBlocked    bool
	UnresolvedIncidents int64
	PendingRuns         int64
	SafeReconciliation  int64
	RecoveryRevision    int64
}

// Per-call fault injection avoids global hooks and covers both sides of commit.
type recoveryApplyHooks struct {
	Inventory    func(context.Context) error
	BeforeCommit func(context.Context, *sql.Tx) error
	Commit       func(*sql.Tx) error
}

func normalizeRecoveryAttestation(o recoveryApplyOptions) (recoveryApplyOptions, inspectionPlan, error) {
	plan, err := decodeInspectionPlan(o.Plan)
	if err != nil || plan.Target == "" || plan.Snapshot == "" || !o.ConfirmStopped || o.Timeout <= 0 ||
		(o.Basis != "groups_stopped" && o.Basis != "host_restarted") || len(o.Explanation) > 4096 || !utf8.ValidString(o.Explanation) {
		return o, plan, failure("invalid_argument", "apply requires a complete plan, stopped-process confirmation, groups_stopped or host_restarted basis, a bounded explanation and a positive finite timeout")
	}
	// Normalize whitespace and remove controls before the existing secret
	// stripper. Neither raw explanations nor backend errors enter public errors.
	o.Explanation = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, o.Explanation))
	o.Explanation = strings.Join(strings.Fields(sanitize.StripSecrets(o.Explanation)), " ")
	if len(o.Explanation) == 0 || len(o.Explanation) > 1024 {
		return o, plan, failure("invalid_argument", "normalized recovery explanation must contain 1–1024 UTF-8 bytes")
	}
	return o, plan, nil
}

func applyRecovery(ctx context.Context, path, key string, options recoveryApplyOptions, hooks *recoveryApplyHooks) (view recoveryApplyResult, retErr error) {
	options, plan, err := normalizeRecoveryAttestation(options)
	if err != nil {
		return view, err
	}
	ctx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	commitAttempted := false
	defer func() {
		if retErr == nil {
			return
		}
		if commitAttempted {
			retErr = failure("recovery_outcome_unknown", "recovery commit outcome is uncertain; inspect the receipt using capy runs inspect --plan with the same token before retrying; never replay execution")
			view.Status = "recovery_outcome_unknown"
			return
		}
		if ctx.Err() != nil {
			retErr = &Error{Code: "recovery_timeout", Message: "recovery did not commit; retry the same apply token and attestation with a longer --timeout if its deadline expired; raise any earlier caller deadline too", cause: ctx.Err()}
		} else {
			var typed *Error
			if !errors.As(retErr, &typed) {
				retErr = storageError()
			}
		}
	}()
	a, err := openRecoveryAdmin(ctx, path, key)
	if err != nil {
		return view, err
	}
	defer func() { retErr = errors.Join(retErr, a.close()) }()
	if plan.Store != a.id || plan.Target != a.binding {
		return view, staleRecoveryPlan()
	}
	if err := a.enableWrites(ctx, key); err != nil {
		return view, err
	}
	tx, err := sqliteutil.BeginImmediateContext(ctx, a.db, "runs")
	if err != nil {
		return view, err
	}
	defer tx.Rollback()
	receipt, err := readRecoveryReceipt(ctx, tx, "", plan.Hash)
	if err == nil {
		if receipt.StoreID != a.id {
			return view, recoveryInconsistent("receipt store binding")
		}
		view.Receipt = &receipt
		if receipt.Method != "operator" || receipt.Basis != options.Basis || receipt.Explanation != options.Explanation {
			return view, failure("recovery_receipt_conflict", "this plan already has a different recorded attestation; inspect the retained receipt ID; the recorded action was not replaced")
		}
		view.Status = "already_applied"
		// Pruning receipts are immutable summaries. Never reconstruct members.
		if err := currentRecoveryBlockers(ctx, tx, &view); err != nil {
			return view, err
		}
		if err := a.verifyTarget(); err != nil {
			return view, err
		}
		return view, ctx.Err()
	}
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != "receipt_unavailable" {
		return view, err
	}
	preview := RecoveryPlan{ObservedAt: time.Now().UTC()}
	var step func(context.Context) error
	if hooks != nil {
		step = hooks.Inventory
	}
	if err := buildRecoveryInventory(ctx, tx, a, &preview, 0, step); err != nil {
		return view, err
	}
	if preview.fingerprint != plan.Snapshot || preview.Counts.administrative() == 0 {
		return view, staleRecoveryPlan()
	}
	if preview.Capacity == nil || !preview.Capacity.Sufficient {
		return view, recoveryApplyCapacity()
	}
	if err := a.verifyTarget(); err != nil {
		return view, err
	}
	now := time.Now().UTC()
	receipt = recoveryReceipt{
		ID: uuid.NewString(), PlanHash: plan.Hash, StoreID: a.id, Version: 1,
		AppliedAt: now.UnixNano(), Method: "operator", Basis: options.Basis,
		Explanation: options.Explanation, UID: int64(os.Getuid()),
		IncidentCount: preview.Counts.administrative(), RunCount: preview.Counts.Dispositions,
		OwnerCount: preview.Counts.OwnerConcerns, StoreCount: preview.Counts.StoreConcerns,
		ChargedBytes: recoveryReceiptReserve,
	}
	// Reserve independently retained audit metadata, excluding new incidents
	// whose existing insertion helper reserves its own debit. No retention call.
	debit := preview.Capacity.MetadataBytes - preview.Counts.NewIncidents*recoveryIncidentReserve
	if err := reserveRecoveryAudit(ctx, tx, debit); err != nil {
		return view, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO run_recovery_receipts(id,plan_hash,store_id,version,applied_at,method,basis,uid,explanation,incident_count,run_count,owner_count,store_count,charged_bytes,audit_expires_at)
 VALUES(?,?,?,1,?,'operator',?,?,?,?,?,?,?,?,0)`, receipt.ID, receipt.PlanHash, receipt.StoreID, receipt.AppliedAt, receipt.Basis, receipt.UID, receipt.Explanation, receipt.IncidentCount, receipt.RunCount, receipt.OwnerCount, receipt.StoreCount, receipt.ChargedBytes)
	if err != nil {
		return view, err
	}
	walker := &recoveryInventory{ctx: ctx, tx: tx, admin: a, view: &RecoveryPlan{}, encoder: json.NewEncoder(io.Discard), step: step}
	walker.visit = func(i incident, action string, ttl int) error {
		if action == "reconcile" {
			return nil
		}
		// Deadlines use Unix milliseconds rounded up, like runs.expires_at;
		// application/detection clocks use nanoseconds. This supports max TTL.
		expiry := now.Add(time.Duration(ttl)*time.Hour).UnixMilli() + 1
		receipt.AuditExpiresAt = max(receipt.AuditExpiresAt, expiry)
		if i.ID == "" {
			if _, err := observeIncident(ctx, tx, i); err != nil {
				return err
			}
			var err error
			i, err = scanIncident(tx.QueryRowContext(ctx, "SELECT "+incidentColumns+" FROM run_incidents WHERE subject_key=? AND receipt_id IS NULL", i.Key))
			if err != nil {
				return err
			}
		}
		snapshot, err := json.Marshal(i)
		if err != nil || len(snapshot) > 2048 {
			return recoveryInconsistent("incident snapshot bound")
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO run_incident_resolutions(receipt_id,incident_id,subject_snapshot,audit_expires_at,charged_bytes) VALUES(?,?,?,?,?)", receipt.ID, i.ID, snapshot, expiry, recoveryMembershipReserve); err != nil {
			return err
		}
		if action == "dispose_run" {
			if err := insertRunResolution(ctx, tx, a.id, receipt, i, ttl, expiry); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, "UPDATE run_incidents SET receipt_id=?,audit_expires_at=? WHERE id=? AND receipt_id IS NULL", receipt.ID, expiry, i.ID)
		return err
	}
	var blocked bool
	if err := tx.QueryRowContext(ctx, "SELECT admission_blocked FROM run_meta WHERE singleton=1").Scan(&blocked); err != nil {
		return view, err
	}
	if err := walker.registry(); err != nil {
		return view, err
	}
	if err := walker.markers(blocked); err != nil {
		return view, err
	}
	if err := walker.runs(); err != nil {
		return view, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE run_recovery_receipts SET audit_expires_at=? WHERE id=?", receipt.AuditExpiresAt, receipt.ID); err != nil {
		return view, err
	}
	if err := closeAppliedBatches(ctx, tx, receipt.ID, now); err != nil {
		return view, err
	}
	if err := clearResolvedMarkers(ctx, tx); err != nil {
		return view, err
	}
	if err := finishIncidentChanges(ctx, tx, true); err != nil {
		return view, err
	}
	if hooks != nil && hooks.BeforeCommit != nil {
		if err := hooks.BeforeCommit(ctx, tx); err != nil {
			return view, err
		}
	}
	if err := currentRecoveryBlockers(ctx, tx, &view); err != nil {
		return view, err
	}
	if view.AdmissionBlocked {
		return view, recoveryInconsistent("unresolved apply inventory")
	}
	if err := a.verifyTarget(); err != nil {
		return view, err
	}
	if err := ctx.Err(); err != nil {
		return view, err
	}
	commitAttempted = true
	if hooks != nil && hooks.Commit != nil {
		err = hooks.Commit(tx)
	} else {
		err = tx.Commit()
	}
	if err != nil {
		return view, err
	}
	view.Status, view.Receipt = "applied", &receipt
	return view, nil
}

func recoveryApplyCapacity() error {
	return failure("recovery_capacity", "recovery metadata exceeds quota or filesystem headroom; free space or increase runs.max_retained_bytes through offline capy runs upgrade and review a fresh plan; no history was evicted")
}

func reserveRecoveryAudit(ctx context.Context, tx *sql.Tx, debit int64) error {
	used, err := refreshIndexDebit(ctx, tx)
	if err != nil {
		return err
	}
	var limit int64
	if err := tx.QueryRowContext(ctx, "SELECT max_retained_bytes FROM run_policy WHERE singleton=1").Scan(&limit); err != nil {
		return err
	}
	if debit < 0 || debit > limit || used > limit-debit {
		return recoveryApplyCapacity()
	}
	_, err = tx.ExecContext(ctx, "UPDATE run_meta SET accounted_bytes=accounted_bytes+? WHERE singleton=1", debit)
	return err
}

func insertRunResolution(ctx context.Context, tx *sql.Tx, store string, receipt recoveryReceipt, i incident, ttl int, expiry int64) error {
	if err := reserveBatchRecovery(ctx, tx, i.BatchID); err != nil {
		return err
	}
	var encoded []byte
	var policy int64
	var scope, state string
	var terminal bool
	if err := tx.QueryRowContext(ctx, "SELECT result,cleanup_scope,cleanup,terminal,json_extract(CAST(program AS TEXT),'$.policy_revision') FROM runs WHERE id=? AND generation=? AND slot=?", i.RunID, i.Generation, *i.Slot).Scan(&encoded, &scope, &state, &terminal, &policy); err != nil {
		return err
	}
	digest := sha256.Sum256(encoded)
	_, err := tx.ExecContext(ctx, `INSERT INTO run_resolutions(run_id,store_id,generation,slot,batch_id,receipt_id,incident_id,version,original_result,result_sha256,cleanup_scope,cleanup,terminal,ttl_hours,policy_revision,resolved_at,retention_expires_at,audit_expires_at,charged_bytes,payload_state)
 VALUES(?,?,?,?,?,?,?,1,?,?,?,?,?,?,?,?,?,?,?,'retained')`, i.RunID, store, i.Generation, *i.Slot, optionalString(i.BatchID), receipt.ID, i.ID, encoded, digest[:], scope, state, terminal, ttl, policy, receipt.AppliedAt, expiry, expiry, recoveryDispositionReserve)
	if err != nil {
		return err
	}
	var result Result
	if json.Unmarshal(encoded, &result) != nil {
		return recoveryInconsistent("index snapshot")
	}
	index, err := resolutionIndex(result.Index)
	if err != nil {
		return err
	}
	var code any
	if index.Error != nil {
		code = index.Error.Code
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO run_search_state(run_id,state,cursor,error_code) VALUES(?,?,?,?)", i.RunID, index.State, index.CoverageCursor, code)
	return err
}

func clearResolvedMarkers(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `UPDATE run_owners SET cleanup_unverified=0 WHERE
 NOT EXISTS(SELECT 1 FROM run_incidents i WHERE i.slot=run_owners.slot AND i.receipt_id IS NULL)
 AND NOT EXISTS(SELECT 1 FROM runs WHERE slot=run_owners.slot AND terminal=0 AND `+undisposedRunSQL+` AND (NOT `+acceptedCleanupSQL+` OR generation<>run_owners.generation))`)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE run_meta SET admission_blocked=(
 EXISTS(SELECT 1 FROM run_incidents WHERE receipt_id IS NULL) OR EXISTS(SELECT 1 FROM run_owners WHERE cleanup_unverified=1)
 OR EXISTS(SELECT 1 FROM runs WHERE terminal=0 AND NOT `+acceptedCleanupSQL+` AND `+undisposedRunSQL+`)) WHERE singleton=1`)
	return err
}

func currentRecoveryBlockers(ctx context.Context, q runQuerier, view *recoveryApplyResult) error {
	err := q.QueryRowContext(ctx, `SELECT admission_blocked OR EXISTS(SELECT 1 FROM run_owners WHERE cleanup_unverified=1),recovery_revision,
 (SELECT count(*) FROM run_incidents WHERE receipt_id IS NULL),
 (SELECT count(*) FROM runs WHERE terminal=0 AND NOT `+acceptedCleanupSQL+` AND `+undisposedRunSQL+`),
 (SELECT count(*) FROM runs WHERE terminal=0 AND `+acceptedCleanupSQL+` AND `+undisposedRunSQL+`)
 FROM run_meta WHERE singleton=1`).Scan(&view.AdmissionBlocked, &view.RecoveryRevision, &view.UnresolvedIncidents, &view.PendingRuns, &view.SafeReconciliation)
	view.AdmissionBlocked = view.AdmissionBlocked || view.UnresolvedIncidents > 0 || view.PendingRuns > 0
	return err
}
