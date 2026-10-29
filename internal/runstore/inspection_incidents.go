package runstore

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/serpro69/capy/internal/sanitize"
)

type InspectionReceipt struct {
	ID             string `json:"receipt_id"`
	AppliedAt      int64  `json:"applied_at"`
	Method         string `json:"method"`
	Basis          string `json:"basis"`
	Explanation    string `json:"explanation,omitempty"`
	IncidentCount  int64  `json:"incident_count"`
	RunCount       int64  `json:"run_count"`
	AuditExpiresAt int64  `json:"audit_expires_at"`
	Pruning        bool   `json:"pruning"`
}

type incidentCursor struct {
	Version      int    `json:"v"`
	Store        string `json:"store"`
	Epoch        int64  `json:"epoch"`
	Selector     string `json:"selector,omitempty"`
	After        string `json:"after,omitempty"`
	Observations bool   `json:"observations,omitempty"`
	Legacy       string `json:"legacy,omitempty"`
}

func (c incidentCursor) encode() string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func inspectIncidentSubjects(ctx context.Context, tx *sql.Tx, view *Inspection, options InspectionOptions, blocked bool) error {
	var epoch int64
	if err := tx.QueryRowContext(ctx, "SELECT recovery_revision FROM run_meta WHERE singleton=1").Scan(&epoch); err != nil {
		return metadataReadError(err)
	}
	if epoch < 0 {
		return inconsistentIncident()
	}
	view.RecoveryRevision = &epoch
	view.Consistency = "recovery epoch binds incident membership; pending/run observations are current to each page; no liveness or admission guarantee"
	view.Action = "inspect"
	view.NextStep = "Inspect retained incident evidence for this owner; operator recovery application is not available in this binary."
	var unresolved bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM run_incidents WHERE receipt_id IS NULL)").Scan(&unresolved); err != nil {
		return storageError()
	}
	if blocked || unresolved {
		view.Admission = "recorded_blocked"
	}
	if err := tx.QueryRowContext(ctx, "SELECT (SELECT admission_blocked FROM run_meta WHERE singleton=1)+(SELECT count(*) FROM run_owners WHERE cleanup_unverified=1)").Scan(&view.Counts.QuarantineMarkers); err != nil {
		return storageError()
	}
	selector, filter := "", ""
	args := []any{}
	if options.RunID != "" {
		selector = "run:" + options.RunID
		filter = " AND run_id=?"
		args = append(args, options.RunID)
	}
	if options.Plan != "" || options.ReceiptID != "" {
		hash := ""
		if options.Plan != "" {
			plan, err := decodeInspectionPlan(options.Plan)
			if err != nil {
				return err
			}
			if plan.Store != view.StoreID {
				return failure("invalid_argument", "plan token belongs to another run store")
			}
			hash = plan.Hash
		}
		receipt, err := readRecoveryReceipt(ctx, tx, options.ReceiptID, hash)
		if err != nil {
			return err
		}
		if receipt.StoreID != view.StoreID {
			return inconsistentIncident()
		}
		view.Receipt = &InspectionReceipt{ID: receipt.ID, AppliedAt: receipt.AppliedAt, Method: receipt.Method, Basis: receipt.Basis, Explanation: sanitize.StripSecrets(receipt.Explanation), IncidentCount: receipt.IncidentCount, RunCount: receipt.RunCount, AuditExpiresAt: receipt.AuditExpiresAt, Pruning: receipt.Pruning}
		selector = "receipt:" + receipt.ID
		filter = " AND receipt_id=?"
		args = append(args, receipt.ID)
	}
	cursor := incidentCursor{Version: 2, Store: view.StoreID, Epoch: epoch, Selector: selector}
	if options.Cursor != "" {
		if decodeInspectionToken(options.Cursor, &cursor) != nil || cursor.Version != 2 || cursor.Epoch < 0 || (cursor.After != "" && uuid.Validate(cursor.After) != nil) || (!cursor.Observations && cursor.Legacy != "") {
			return failure("cursor_invalid", "invalid incident inspection cursor")
		}
		if cursor.Store != view.StoreID || cursor.Epoch != epoch || cursor.Selector != selector {
			return staleInspection()
		}
	}
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(sum(attribution='recorded'),0),COALESCE(sum(attribution='inferred'),0),COALESCE(sum(attribution='unattributed'),0),COALESCE(sum(receipt_id IS NULL),0) FROM run_incidents WHERE 1=1`+filter, args...).Scan(&view.Counts.RecordedIncidents, &view.Counts.InferredIncidents, &view.Counts.UnattributedIncidents, &view.Counts.UnresolvedIncidents)
	if err != nil {
		return storageError()
	}
	incidentTotal := view.Counts.RecordedIncidents + view.Counts.InferredIncidents + view.Counts.UnattributedIncidents
	if view.Counts.UnresolvedIncidents > 0 {
		view.Admission = "recorded_blocked"
	}
	view.positions = []string{cursor.encode()}
	if !cursor.Observations {
		queryArgs := append(append([]any{}, args...), cursor.After, options.Limit+1)
		rows, err := tx.QueryContext(ctx, "SELECT "+incidentColumns+" FROM run_incidents WHERE 1=1"+filter+" AND id>? ORDER BY id LIMIT ?", queryArgs...)
		if err != nil {
			return storageError()
		}
		subjects := []incident{}
		for rows.Next() {
			i, err := scanIncident(rows)
			if err != nil {
				rows.Close()
				return metadataReadError(err)
			}
			subjects = append(subjects, i)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return storageError()
		}
		for _, i := range subjects[:min(len(subjects), options.Limit)] {
			subject, err := incidentSubject(ctx, tx, view, i)
			if err != nil {
				return err
			}
			view.Subjects = append(view.Subjects, subject)
			cursor.After = i.ID
			view.positions = append(view.positions, cursor.encode())
		}
		if len(subjects) > options.Limit {
			view.NextCursor = cursor.encode()
			view.Omitted = true
		}
	}
	// Legacy marker/run observations remain a separate current-page sample. An
	// attributed run is not duplicated as an unattributed pending observation.
	includeObservations := view.Receipt == nil && (options.RunID == "" || incidentTotal == 0)
	if includeObservations {
		observationView := *view
		observationView.Subjects = nil
		observationView.positions = nil
		observationView.NextCursor = ""
		observationView.Omitted = false
		observationView.Counts = InspectionCounts{}
		legacyOptions := InspectionOptions{Limit: options.Limit, RunID: options.RunID}
		if cursor.Observations {
			legacyOptions.Cursor = cursor.Legacy
		}
		if err := inspectLegacySubjects(ctx, tx, &observationView, legacyOptions, blocked); err != nil {
			return err
		}
		view.Counts.QuarantineMarkers = observationView.Counts.QuarantineMarkers
		view.Counts.InferredCandidates = observationView.Counts.InferredCandidates
		view.Counts.UnattributedPending = observationView.Counts.UnattributedPending
		view.Counts.Subjects = incidentTotal + observationView.Counts.Subjects
		if options.RunID != "" {
			view.Counts.Subjects = int64(len(observationView.Subjects))
		}
		if view.NextCursor == "" {
			cursor.Observations = true
			cursor.After = ""
			for i, s := range observationView.Subjects {
				if len(view.Subjects) == options.Limit {
					view.NextCursor = cursor.encode()
					view.Omitted = true
					break
				}
				// A flag is a separate fact, not a second durable incident episode.
				if s.Kind != "run" {
					s.Reason = "quarantine_marker"
					s.Attribution = "recorded"
				}
				view.Subjects = append(view.Subjects, s)
				cursor.Legacy = observationView.positions[i+1]
				view.positions = append(view.positions, cursor.encode())
			}
			if view.NextCursor == "" && observationView.Omitted {
				cursor.Legacy = observationView.NextCursor
				view.NextCursor = cursor.encode()
				view.Omitted = true
			}
		}
	} else {
		view.Counts.Subjects = incidentTotal
	}
	view.Details = "complete"
	for _, subject := range view.Subjects {
		if subject.DetailUnavailable {
			view.Details = "partial"
			view.NextStep = "Retry inspection for unavailable run details; durable incident evidence remains recorded."
		}
	}
	if view.Omitted {
		view.Details = "partial"
	}
	if view.Receipt != nil && view.Receipt.Pruning {
		view.Details = "partial"
		view.NextStep = "Retained receipt is pruning; its immutable summary remains available and removed details cannot be reconstructed."
	}
	return nil
}

func incidentSubject(ctx context.Context, tx *sql.Tx, view *Inspection, i incident) (InspectionSubject, error) {
	s := incidentSummary(i)
	if i.DetectedAt == nil {
		s.MissingEvidence = append(s.MissingEvidence, "detection_time")
	}
	if i.RunID == "" {
		s.MissingEvidence = append(s.MissingEvidence, "causal_run")
		return s, nil
	}
	if i.ReceiptID != "" {
		d, err := readRunResolution(ctx, tx, i.RunID)
		if err != nil {
			// Inspection must still enumerate other incidents when one retained
			// disposition is damaged or its optional detail cannot be read.
			var typed *Error
			if errors.As(err, &typed) && typed.Code == "recovery_inconsistent" {
				s.EvidenceInconsistent = true
				s.MissingEvidence = append(s.MissingEvidence, "valid_disposition")
			} else {
				s.DetailUnavailable = true
				s.MissingEvidence = append(s.MissingEvidence, "disposition_unavailable")
			}
			return s, nil
		}
		if d != nil {
			if d.IncidentID != i.ID || d.ReceiptID != i.ReceiptID {
				s.EvidenceInconsistent = true
				s.MissingEvidence = append(s.MissingEvidence, "matching_disposition")
				return s, nil
			}
			var original Result
			if err := decodeRunResult(d.Original, d.Cleanup, &original); err != nil {
				return s, err
			}
			s.State = diagnosticState(original.State, "admitted", "queued", "running", "exited", "start_failed", "timed_out", "cancelled", "interrupted", "skipped")
			s.Cleanup = &d.Cleanup
			s.MissingEvidence = append(s.MissingEvidence, "accepted_cleanup", "terminal_outcome")
			available := d.PayloadState == "retained" && view.ObservedAt.Before(time.UnixMilli(d.ExpiresAt))
			s.OutputAvailable = &available
			return s, nil
		}
	}
	// One indexed run lookup in the same read snapshot, without recursively
	// counting the whole inventory for every incident on this page.
	row, _, err := scanInspectionRun(tx.QueryRowContext(ctx, "SELECT "+inspectionRunColumns(view.SchemaVersion)+" FROM runs r WHERE r.id=?", i.RunID), view)
	if err != nil {
		var typed *Error
		if errors.As(err, &typed) {
			switch typed.Code {
			case "run_unavailable":
				available := false
				s.OutputAvailable = &available
				// Resolved incident-only terminal history can expire normally.
				// Open incidents still require their protected run evidence.
				s.EvidenceInconsistent = i.ReceiptID == ""
				if s.EvidenceInconsistent {
					s.MissingEvidence = append(s.MissingEvidence, "run_metadata")
				}
			case "recovery_inconsistent":
				s.EvidenceInconsistent = true
				s.MissingEvidence = append(s.MissingEvidence, "valid_run_metadata")
			default:
				s.DetailUnavailable = true
				s.MissingEvidence = append(s.MissingEvidence, "run_metadata_unavailable")
			}
			return s, nil
		}
		return s, err
	}

	s.State, s.Cleanup, s.OutputAvailable = row.State, row.Cleanup, row.OutputAvailable
	if i.ReceiptID == "" {
		// This reports physical retention. A mismatched incident generation is
		// flagged below and cannot authorize expired reads or operator recovery;
		// retention nevertheless pins those bytes for separate evidence repair.
		var available bool
		if err := tx.QueryRowContext(ctx, "SELECT evicting=0 FROM runs WHERE id=?", i.RunID).Scan(&available); err != nil {
			return s, metadataReadError(err)
		}
		s.OutputAvailable = &available
	}
	s.MissingEvidence = append(s.MissingEvidence, row.MissingEvidence...)
	s.EvidenceInconsistent = s.EvidenceInconsistent || row.Reason == "recovery_inconsistent" || row.Generation != i.Generation || row.Slot == nil || *row.Slot != *i.Slot
	return s, nil
}
