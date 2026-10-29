package runstore

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Recovery describes administrative history, not a new execution outcome.
// BlocksAdmission refers to this concern, not other incidents or resource gates.
// DispositionID is the unique run ID key of a run disposition, not a receipt ID.
type Recovery struct {
	State               string     `json:"state"`
	IncidentID          string     `json:"incident_id,omitempty"`
	ReceiptID           string     `json:"receipt_id,omitempty"`
	DispositionID       string     `json:"disposition_id,omitempty"`
	ResolvedAt          *time.Time `json:"resolved_at,omitempty"`
	Basis               string     `json:"basis,omitempty"`
	RetentionExpiresAt  *time.Time `json:"retention_expires_at,omitempty"`
	OperationallyClosed bool       `json:"operationally_closed"`
	CaptureClosed       bool       `json:"capture_closed"`
	BlocksAdmission     bool       `json:"blocks_admission"`
}

// projectRunHistory mutates only the returned view. It must be called after
// decoding the stored outcome and validating authoritative cleanup columns.
// The supplied query belongs to the caller's snapshot/attachment; no new store,
// lease, maintenance, index work or execution completion is introduced here.
func projectRunHistory(ctx context.Context, q runQuerier, id, generation string, result *Result, terminal, unverified bool, now time.Time) (bool, error) {
	if result.OwnerID != generation {
		return false, recoveryInconsistent("run result generation")
	}
	d, err := readRunResolution(ctx, q, id)
	if err != nil {
		return false, err
	}
	if d != nil {
		resolved := time.Unix(0, d.ResolvedAt).UTC()
		expires := time.UnixMilli(d.ExpiresAt).UTC()
		result.Recovery = &Recovery{State: "operator_resolved", IncidentID: d.IncidentID, ReceiptID: d.ReceiptID, DispositionID: d.RunID,
			ResolvedAt: &resolved, Basis: d.Receipt.Basis, RetentionExpiresAt: &expires, OperationallyClosed: true, CaptureClosed: true}
		// Capture did not complete with accepted execution proof. Preserve the
		// recorded exit, completion, persistence flag, reason and original expiry.
		if result.Capture.State != "not_started" {
			result.Capture.State = "incomplete"
		}
		return now.Before(expires), nil
	}
	// The historical generation is durable even after its registry slot is
	// reused. Prefer an open episode over an older resolved one for this run.
	i, err := scanIncident(q.QueryRowContext(ctx, "SELECT "+incidentColumns+" FROM run_incidents WHERE run_id=? AND generation=? ORDER BY receipt_id IS NULL DESC,detected_at DESC,id DESC LIMIT 1", id, generation))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, metadataReadError(err)
	}
	if err == nil {
		result.Recovery = &Recovery{State: "unresolved", IncidentID: i.ID, BlocksAdmission: i.ReceiptID == "", OperationallyClosed: terminal, CaptureClosed: terminal}
		if i.ReceiptID != "" {
			receipt, err := readRecoveryReceipt(ctx, q, i.ReceiptID, "")
			if err != nil {
				return false, err
			}
			resolved := time.Unix(0, receipt.AppliedAt).UTC()
			result.Recovery.State, result.Recovery.ReceiptID = "operator_resolved", receipt.ID
			result.Recovery.ResolvedAt, result.Recovery.Basis = &resolved, receipt.Basis
		} else {
			unverified = true
		}
	}
	// A completed result without accepted terminal cleanup also remains
	// uncertain while its live owner lease prevents reconciliation.
	unverified = unverified || !terminal && result.CompletedAt != nil
	if unverified && !terminal {
		markUnverified(result)
		if result.Recovery == nil {
			result.Recovery = &Recovery{State: "unresolved", BlocksAdmission: true}
		}
	}
	return result.ExpiresAt == nil || now.Before(*result.ExpiresAt), nil
}
