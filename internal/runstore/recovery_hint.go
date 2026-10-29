package runstore

import (
	"context"
	"errors"
	"time"

	"github.com/serpro69/capy/internal/sanitize"
)

// RecoveryHint describes a refused admission, never a cleanup proof. Optional
// attribution is bounded independently of the authoritative refusal/action.
type RecoveryHint struct {
	NoAdmission       bool               `json:"no_admission"`
	Scope             string             `json:"scope"`
	Action            string             `json:"action"`
	NextStep          string             `json:"next_step"`
	InspectCommand    string             `json:"inspect_command,omitempty"`
	DetailUnavailable bool               `json:"detail_unavailable,omitempty"`
	IncidentCount     *int64             `json:"incident_count,omitempty"`
	Subject           *InspectionSubject `json:"subject,omitempty"`
}

// AdmissionError adds repair guidance without querying or opening storage. It
// is safe with an occupied pool, registry lock, expired context or local latch.
// Unknown error types are not exposed as backend text at the public boundary.
func AdmissionError(err error, owner string) *Error {
	var typed *Error
	if !errors.As(err, &typed) {
		return &Error{Code: "storage_unavailable", Message: "run admission failed before execution", cause: err}
	}
	e := *typed
	e.cause = err
	if e.RecoveryHint != nil {
		return &e
	}
	h := &RecoveryHint{NoAdmission: true, Scope: "store", Action: "inspect", DetailUnavailable: true}
	switch e.Code {
	case "cleanup_unverified":
		e.Message = "No command was admitted: unverified cleanup blocks execution."
		h.NextStep = "Inspect this owner with capy runs inspect (MCP: capy_runs action=inspect). Retained logs remain subject to retention."
	case "wrong_key", "credential_unavailable":
		e.Message = "No command was admitted: the selected run credential is unavailable or does not authenticate the store."
		h.Action = "restore_credential"
		h.NextStep = "Restore the selected matching credential and reload affected processes, then inspect this owner with capy runs inspect."
	case "storage_busy", "owner_slots_exhausted":
		e.Message = "No command was admitted: run storage leases are busy."
		if e.Code == "owner_slots_exhausted" {
			e.Message = "No command was admitted: all run owner slots are occupied or awaiting bounded recovery."
		}
		h.Action = "stop_store_users"
		h.NextStep = "Retry capy runs inspect for this owner after current operations finish; stop attached users if contention persists. Free leases do not prove old groups stopped."
	case "schema_upgrade_busy":
		e.Message = "No command was admitted: run schema upgrade needs exclusive access."
		h.Action = "stop_store_users"
		h.NextStep = "Stop sessions, readers and helpers; inspect this owner with capy runs inspect, then use capy runs upgrade from an ordinary terminal."
	case "upgrade_required", "schema_mismatch":
		e.Message = "No command was admitted: the run schema requires a compatible binary."
		h.Action = "upgrade_required"
		h.NextStep = "Inspect this owner with capy runs inspect; stop store users and use a compatible binary for capy runs upgrade from an ordinary terminal."
	case "recovery_plan_stale":
		e.Message = "No command was admitted: the reviewed recovery plan is stale."
		h.Action = "review_plan"
		h.NextStep = "Inspect this owner with capy runs inspect and review a fresh offline plan; old confirmation does not cover changed incidents."
	case "recovery_inconsistent", "recovery_revision_exhausted":
		e.Message = "No command was admitted: recovery evidence or its revision is inconsistent."
		h.Action = "repair_evidence"
		h.NextStep = "Preserve history and inspect this owner with capy runs inspect. Repair the evidence separately; operator confirmation cannot override it."
	case "schema_upgrade_failed":
		e.Message = "No command was admitted: run schema upgrade did not complete."
		h.Action = "repair_upgrade"
		h.NextStep = "Inspect this owner with capy runs inspect. Preserve malformed/inconsistent history for separate repair; otherwise fix the reported cause and retry offline capy runs upgrade with a compatible binary."
	case "recovery_capacity", "schema_upgrade_capacity":
		e.Message = "No command was admitted: preserving recovery metadata needs more capacity."
		h.Action = "increase_capacity"
		h.NextStep = "Inspect this owner with capy runs inspect. Free filesystem space or increase runs.max_retained_bytes and use offline capy runs upgrade; preserve logs."
	case "incident_publication_failed":
		e.Message = "No command was admitted: incident attribution could not be persisted."
		h.Action = "repair_storage"
		h.NextStep = "Preserve history; inspect this owner with capy runs inspect and repair storage availability before offline recovery."
	case "storage_unavailable":
		e.Message = "No command was admitted: run storage is unavailable."
		h.Action = "repair_storage"
		h.NextStep = "Inspect this owner with capy runs inspect and check its .capy/runs path and permissions; preserve retained history."
	default:
		return &e
	}
	// Detail is opted into by a producer that supplies safe diagnostic text;
	// a typed code alone never authorizes exposing its original error message.
	if e.Detail != "" {
		e.Detail = sanitize.StripSecrets(e.Detail)
		if len(e.Detail) > 512 {
			e.Detail = string([]rune(e.Detail)[:128])
		}
	}
	_, h.InspectCommand = inspectionOwner(owner)
	if owner == "" {
		h.InspectCommand = ""
	}
	e.RecoveryHint = h
	return &e
}

// admissionFailure uses the existing connection, including inside an admission
// transaction. Optional metadata gets at most 100 ms and can never change the
// original error code or turn a failed detail query into admission permission.
func (s *Store) admissionFailure(ctx context.Context, q runQuerier, code, message string) error {
	e := AdmissionError(failure(code, message), s.owner)
	h := e.RecoveryHint
	if h == nil || s.policy.SchemaVersion < 10 {
		return e
	}
	ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	var count int64
	if err := q.QueryRowContext(ctx, "SELECT count(*) FROM run_incidents WHERE receipt_id IS NULL").Scan(&count); err != nil {
		return e
	}
	h.IncidentCount = &count
	if count == 0 {
		return e // marker-only refusal: causal attribution remains unavailable
	}
	i, err := scanIncident(q.QueryRowContext(ctx, "SELECT "+incidentColumns+" FROM run_incidents WHERE receipt_id IS NULL ORDER BY id LIMIT 1"))
	if err != nil {
		return e
	}
	subject := incidentSummary(i)
	h.Subject, h.DetailUnavailable = &subject, false
	return e
}

// RuntimeAdmissionError preserves the first known live-owner cause without
// claiming that an unsuccessful incident write became durable.
func RuntimeAdmissionError(owner string, subject *RuntimeIncident, durable bool) *Error {
	e := AdmissionError(failure("cleanup_unverified", ""), owner)
	h := e.RecoveryHint
	h.Scope = "owner"
	if subject == nil {
		return e
	}
	i := incident{Kind: "run", Key: "run:" + subject.RunID + ":" + subject.Generation,
		RunID: subject.RunID, Generation: subject.Generation, Slot: &subject.Slot, BatchID: subject.BatchID,
		Cause: subject.Cause, Stage: subject.Stage, Phase: "unknown", Attribution: attributionRecorded}
	if !i.valid() {
		return e
	}
	s := incidentSummary(i)
	s.Attribution = "volatile"
	if durable {
		s.Attribution = "recorded"
	}
	h.Subject = &s
	// This marks persistence uncertainty, not inventory completeness. A cached
	// subject is only one observation; no total count is claimed for this owner.
	h.DetailUnavailable = !durable
	return e
}

// Keep the same allowlisted subject vocabulary in inspection and admission.
func incidentSummary(i incident) InspectionSubject {
	s := InspectionSubject{Kind: i.Kind, IncidentID: i.ID, IncidentRevision: i.Revision, RunID: i.RunID, ReceiptID: i.ReceiptID, Generation: i.Generation, Slot: i.Slot, Attribution: string(i.Attribution), Reason: string(i.Cause), Phase: i.Phase, DetectionStage: string(i.Stage), BlocksAdmission: i.ReceiptID == "", EvidenceInconsistent: i.Cause.rank() == 4}
	if i.DetectedAt != nil {
		detected := time.Unix(0, *i.DetectedAt).UTC()
		s.DetectedAt = &detected
	}
	return s
}
