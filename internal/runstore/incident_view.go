package runstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	"github.com/google/uuid"
	"github.com/serpro69/capy/internal/executor"
)

// InspectionSubject contains allowlisted metadata only. Legacy marker subjects
// are not persisted incidents and have neither invented UUIDs nor detection dates.
type InspectionSubject struct {
	Kind            string                    `json:"subject"`
	RunID           string                    `json:"run_id,omitempty"`
	Generation      string                    `json:"generation,omitempty"`
	Slot            *int                      `json:"slot,omitempty"`
	Attribution     string                    `json:"attribution"`
	Reason          string                    `json:"reason"`
	BlocksAdmission bool                      `json:"recorded_quarantine"`
	State           string                    `json:"last_recorded_state,omitempty"`
	Phase           string                    `json:"last_durable_phase,omitempty"`
	Cleanup         *executor.CleanupEvidence `json:"cleanup,omitempty"`
	MissingEvidence []string                  `json:"missing_evidence,omitempty"`
	OutputAvailable *bool                     `json:"output_available,omitempty"`
}

type inspectionRegistry struct {
	Slot       int    `json:"slot"`
	Generation string `json:"generation"`
	Blocked    bool   `json:"blocked"`
}

type inspectionCursor struct {
	Version int    `json:"v"`
	Store   string `json:"store"`
	Schema  int    `json:"schema"`
	Markers string `json:"markers"`
	RunID   string `json:"run,omitempty"`
	Cutoff  int64  `json:"cutoff"`
	Marker  int    `json:"marker"`
	Created int64  `json:"created"`
	ID      string `json:"id,omitempty"`
}

// The versioned token identifies a plan hash, not an authorization credential.
// Task 7's canonical builder supplies the hash over all physical/logical binding.
type inspectionPlan struct {
	Version int    `json:"v"`
	Store   string `json:"store"`
	Hash    string `json:"hash"`
}

func decodeInspectionToken(token string, value any) error {
	if len(token) == 0 || len(token) > 1024 {
		return failure("invalid_argument", "invalid inspection token")
	}
	data, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return failure("invalid_argument", "invalid inspection token")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || decoder.Decode(new(any)) != io.EOF {
		return failure("invalid_argument", "invalid inspection token")
	}
	return nil
}

func decodeInspectionPlan(token string) (inspectionPlan, error) {
	var plan inspectionPlan
	err := decodeInspectionToken(token, &plan)
	hash, hashErr := hex.DecodeString(plan.Hash)
	if err != nil || plan.Version != 1 || uuid.Validate(plan.Store) != nil || hashErr != nil || len(hash) != sha256.Size {
		return plan, failure("invalid_argument", "invalid recovery plan token")
	}
	return plan, nil
}

func receiptUnavailable() error {
	return failure("receipt_unavailable", "no retained receipt matches; absence does not prove nonapplication or validate an unapplied plan; inspect current blockers and review unapplied plans through recover with a compatible recovery-capable binary")
}

func staleInspection() error {
	return failure("inspection_stale", "inspection store or registry changed; restart capy runs inspect without the cursor")
}

func (c inspectionCursor) encode() string {
	encoded, _ := json.Marshal(c) // fixed scalar fields cannot fail JSON encoding
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func inspectionScopeSQL(version int) string {
	if version == 8 {
		return "'linux_descendants_v1'" // schema 8 was Linux-only; never infer host scope
	}
	return "CASE WHEN length(CAST(cleanup_scope AS BLOB))<=64 THEN cleanup_scope ELSE NULL END"
}

func inspectLegacySubjects(ctx context.Context, tx *sql.Tx, view *Inspection, options InspectionOptions, blocked bool) error {
	if options.Plan != "" {
		plan, err := decodeInspectionPlan(options.Plan)
		if err != nil {
			return err
		}
		if plan.Store != view.StoreID {
			return failure("invalid_argument", "plan token belongs to another run store")
		}
	}
	if options.Plan != "" || options.ReceiptID != "" {
		// Schemas 8/9 cannot contain receipts. Do not scan runs or reconstruct a
		// plan. Task 3 adds indexed retained-receipt lookup for schema 10, without
		// an expiry filter (GC, not the clock, defines audit membership).
		return receiptUnavailable()
	}
	rows, err := tx.QueryContext(ctx, `SELECT slot,CASE WHEN length(generation)=36 THEN generation ELSE NULL END,cleanup_unverified
		FROM run_owners ORDER BY slot LIMIT 65`)
	if err != nil {
		return storageError()
	}
	registry := make([]inspectionRegistry, 0, OwnerSlots)
	markers := make([]InspectionSubject, 0, OwnerSlots+1)
	if blocked {
		markers = append(markers, InspectionSubject{Kind: "store", Attribution: "unattributed", Reason: "legacy_unattributed", BlocksAdmission: true, MissingEvidence: []string{"causal_run", "detection_time"}})
	}
	for rows.Next() {
		var owner inspectionRegistry
		if err := rows.Scan(&owner.Slot, &owner.Generation, &owner.Blocked); err != nil ||
			owner.Slot < 0 || owner.Slot >= OwnerSlots || uuid.Validate(owner.Generation) != nil || len(registry) >= OwnerSlots {
			rows.Close()
			return storageError()
		}
		registry = append(registry, owner)
		if owner.Blocked {
			slot := owner.Slot
			markers = append(markers, InspectionSubject{Kind: "owner", Slot: &slot, Generation: owner.Generation,
				Attribution: "unattributed", Reason: "legacy_unattributed", BlocksAdmission: true, MissingEvidence: []string{"causal_run", "detection_time"}})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return storageError()
	}
	if err := rows.Close(); err != nil {
		return storageError()
	}
	view.Counts.QuarantineMarkers = int64(len(markers))
	if len(markers) > 0 {
		view.Admission = "recorded_blocked"
	}
	// Never advertise an unavailable apply/upgrade command in this staged binary.
	// Both legacy schemas require schema 10 for attributed operator recovery.
	view.Action = "inspect"
	view.NextStep = "Select this physical owner with --project-dir to inspect again. No recorded quarantine is not a guarantee of admission; pending observations do not prove live or abandoned work."
	if len(markers) > 0 || view.SchemaVersion < schemaVersion {
		view.Action = "upgrade_required"
		view.NextStep = "Recovery needs a schema-10-capable binary with offline runs upgrade. Stop affected sessions and old execution groups before administration."
	}
	var cutoff int64
	if err := tx.QueryRowContext(ctx, "SELECT last_created FROM run_meta WHERE singleton=1").Scan(&cutoff); err != nil || cutoff < 0 {
		return storageError()
	}
	encoded, err := json.Marshal(struct {
		Blocked bool                 `json:"blocked"`
		Owners  []inspectionRegistry `json:"owners"`
	}{blocked, registry})
	if err != nil {
		return storageError()
	}
	markerHash := fmt.Sprintf("%x", sha256.Sum256(encoded))
	cursor := inspectionCursor{Version: 1, Store: view.StoreID, Schema: view.SchemaVersion, Markers: markerHash, Cutoff: cutoff, RunID: options.RunID}
	if options.Cursor != "" {
		if err := decodeInspectionToken(options.Cursor, &cursor); err != nil || cursor.Version != 1 || cursor.Marker < 0 || cursor.Marker > OwnerSlots+1 ||
			cursor.Cutoff < 0 || cursor.Created < 0 || cursor.Created > cursor.Cutoff || (cursor.ID != "" && uuid.Validate(cursor.ID) != nil) {
			return failure("cursor_invalid", "invalid inspection cursor")
		}
		if cursor.Store != view.StoreID || cursor.Schema != view.SchemaVersion || cursor.Markers != markerHash || cursor.RunID != options.RunID || cursor.Cutoff > cutoff {
			return staleInspection()
		}
	}
	// Markers are stable across accepted legacy pages. Runs can finish/disappear
	// between pages: totals and evidence are explicitly per-page observations.
	const inferred = `EXISTS(SELECT 1 FROM run_owners o WHERE o.slot=r.slot AND o.cleanup_unverified=1
		AND (r.cleanup='pending' OR o.generation<>r.generation))`
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(sum(`+inferred+`),0),COALESCE(sum(NOT `+inferred+`),0)
		FROM runs r WHERE terminal=0 AND created_at<=?`, cursor.Cutoff).
		Scan(&view.Counts.InferredCandidates, &view.Counts.UnattributedPending); err != nil {
		return storageError()
	}
	view.Counts.Subjects = view.Counts.QuarantineMarkers + view.Counts.InferredCandidates + view.Counts.UnattributedPending
	view.positions = []string{cursor.encode()}
	if options.RunID == "" {
		for cursor.Marker < len(markers) && len(view.Subjects) < options.Limit {
			view.Subjects = append(view.Subjects, markers[cursor.Marker])
			cursor.Marker++
			view.positions = append(view.positions, cursor.encode())
		}
	}
	// SQL fragments are version-selected constants. User selectors remain bound.
	query := `SELECT CASE WHEN length(r.id)=36 THEN r.id ELSE NULL END,
		CASE WHEN length(r.generation)=36 THEN r.generation ELSE NULL END,r.slot,r.created_at,
		CASE WHEN length(r.result)<=65536 THEN r.result ELSE NULL END,` + inspectionScopeSQL(view.SchemaVersion) + `,
		CASE WHEN length(CAST(r.cleanup AS BLOB))<=16 THEN r.cleanup ELSE NULL END,
		r.terminal,r.evicting,r.expires_at,` + inferred + `,
		EXISTS(SELECT 1 FROM run_owners o WHERE o.slot=r.slot AND o.generation=r.generation)
		FROM runs r WHERE r.created_at<=? AND (r.created_at,r.id)>(?,?)`
	args := []any{cursor.Cutoff, cursor.Created, cursor.ID}
	if options.RunID != "" {
		query += " AND r.id=?"
		args = append(args, options.RunID)
	} else {
		query += " AND r.terminal=0"
	}
	query += " ORDER BY r.created_at,r.id LIMIT ?"
	args = append(args, options.Limit-len(view.Subjects)+1)
	rows, err = tx.QueryContext(ctx, query, args...)
	if err != nil {
		return storageError()
	}
	defer rows.Close()
	more := options.RunID == "" && cursor.Marker < len(markers)
	for rows.Next() {
		if len(view.Subjects) == options.Limit {
			more = true
			break
		}
		var subject InspectionSubject
		var slot int
		var created int64
		var data []byte
		var cleanup executor.CleanupEvidence
		var terminal, evicting, attributed, matching bool
		var expiry sql.NullInt64
		if err := rows.Scan(&subject.RunID, &subject.Generation, &slot, &created, &data, &cleanup.Scope, &cleanup.State,
			&terminal, &evicting, &expiry, &attributed, &matching); err != nil {
			return failure("recovery_inconsistent", "retained run metadata is inconsistent; inspection details are incomplete; preserve history and repair the metadata separately")
		}
		var result Result
		if uuid.Validate(subject.RunID) != nil || uuid.Validate(subject.Generation) != nil || slot < 0 || slot >= OwnerSlots || len(data) == 0 ||
			decodeRunResult(data, cleanup, &result) != nil {
			return failure("recovery_inconsistent", "retained run identity, cleanup or result metadata is inconsistent; preserve history and repair that evidence separately; inspection details are incomplete")
		}
		subject.Kind, subject.Slot, subject.Cleanup = "run", &slot, &cleanup
		subject.Attribution, subject.Reason = "unattributed", "unattributed_pending"
		if attributed {
			subject.Attribution, subject.Reason, subject.BlocksAdmission = "inferred", "pending_cleanup", true
		}
		if terminal {
			subject.Reason = "recorded_terminal"
		}
		if validateTerminal(cleanup, result, terminal) != nil || (!terminal && !matching) {
			subject.Reason = "recovery_inconsistent"
		}
		subject.State = diagnosticState(result.State, "admitted", "queued", "running", "exited", "start_failed", "timed_out", "cancelled", "interrupted", "skipped")
		subject.Phase = "unknown" // not persisted until schema 10
		if cleanup.State == executor.CleanupPending {
			subject.MissingEvidence = append(subject.MissingEvidence, "accepted_cleanup")
		}
		if !terminal {
			subject.MissingEvidence = append(subject.MissingEvidence, "terminal_outcome")
		}
		available := !evicting && (!terminal || expiry.Valid && expiry.Int64 > view.ObservedAt.UnixMilli())
		subject.OutputAvailable = &available
		view.Subjects = append(view.Subjects, subject)
		cursor.Created, cursor.ID = created, subject.RunID
		view.positions = append(view.positions, cursor.encode())
	}
	if rows.Err() != nil {
		return storageError()
	}
	if options.RunID != "" && len(view.Subjects) == 0 && options.Cursor == "" {
		return failure("run_unavailable", "no retained run matches this ID")
	}
	view.Details = "complete"
	if more {
		view.Omitted, view.Details, view.NextCursor = true, "partial", cursor.encode()
	}
	return nil
}
