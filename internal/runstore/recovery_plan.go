package runstore

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

const DefaultRecoveryTimeout = 5 * time.Minute

const recoveryPageSelection = "Select the same physical owner with --project-dir; continue using next_cursor and next_timeout. "

type RecoveryPlanOptions struct {
	Cursor  string
	Limit   int
	Timeout time.Duration
}

func (o RecoveryPlanOptions) Validate() error {
	if o.Timeout <= 0 || o.Limit < 1 || o.Limit > 64 || len(o.Cursor) > 1024 {
		return failure("invalid_argument", "recovery planning requires a positive finite timeout, limit 1–64 and a cursor of at most 1024 bytes")
	}
	if o.Cursor != "" {
		_, err := decodeRecoveryCursor(o.Cursor)
		return err
	}
	return nil
}

type RecoveryPlanCounts struct {
	Subjects           int64 `json:"subjects"`
	Dispositions       int64 `json:"dispositions"`
	IncidentOnly       int64 `json:"incident_only,omitempty"`
	OwnerConcerns      int64 `json:"owner_concerns,omitempty"`
	StoreConcerns      int64 `json:"store_concerns,omitempty"`
	SafeReconciliation int64 `json:"safe_reconciliation,omitempty"`
	NewIncidents       int64 `json:"new_incidents,omitempty"`
	Batches            int64 `json:"affected_batches,omitempty"`
}

func (c RecoveryPlanCounts) administrative() int64 {
	return c.Dispositions + c.IncidentOnly + c.OwnerConcerns + c.StoreConcerns
}

type RecoveryPlanSubject struct {
	Key                  string            `json:"key"`
	Action               string            `json:"action"`
	Evidence             InspectionSubject `json:"evidence"`
	TTLHours             int               `json:"ttl_hours,omitempty"`
	EstimatedExpiry      *time.Time        `json:"estimated_expiry,omitempty"`
	EstimatedAuditExpiry *time.Time        `json:"estimated_audit_expiry,omitempty"`
}

type RecoveryPlanCapacity struct {
	MetadataBytes  int64  `json:"metadata_bytes"`
	AvailableBytes int64  `json:"available_bytes"`
	DiskNeeded     uint64 `json:"disk_needed"`
	DiskAvailable  uint64 `json:"disk_available"`
	Sufficient     bool   `json:"sufficient"`
}

// RecoveryPlan is a captured preview, not permission to execute or apply. Only
// a complete validated inventory can carry a token. Preview clocks/capacity are
// samples; actual retention and capacity must be checked by eventual apply.
type RecoveryPlan struct {
	Version           int                   `json:"version"`
	Kind              string                `json:"kind"`
	Status            string                `json:"status"`
	StoreID           string                `json:"store_id,omitempty"`
	RecoveryRevision  *int64                `json:"recovery_revision,omitempty"`
	Complete          bool                  `json:"complete"`
	Token             string                `json:"plan,omitempty"`
	Counts            RecoveryPlanCounts    `json:"counts"`
	Subjects          []RecoveryPlanSubject `json:"subjects,omitempty"`
	Capacity          *RecoveryPlanCapacity `json:"capacity,omitempty"`
	ObservedAt        time.Time             `json:"observed_at"`
	EffectiveTimeout  string                `json:"effective_timeout"`
	Omitted           bool                  `json:"omitted"`
	MetadataOmitted   bool                  `json:"metadata_omitted,omitempty"`
	NextCursor        string                `json:"next_cursor,omitempty"`
	NextCommand       string                `json:"next_command,omitempty"`
	NextTimeout       string                `json:"next_timeout,omitempty"`
	NextStep          string                `json:"next_step"`
	Error             *Error                `json:"error,omitempty"`
	position          int64
	fingerprint       string
	command           string
	timeout           time.Duration
	batchReservations int64
}

type recoveryPlanCursor struct {
	Version  int    `json:"v"`
	Store    string `json:"store"`
	Hash     string `json:"hash"`
	Position int64  `json:"position"`
}

func decodeRecoveryCursor(value string) (recoveryPlanCursor, error) {
	var c recoveryPlanCursor
	if len(value) == 0 || len(value) > 1024 {
		return c, failure("cursor_invalid", "invalid recovery plan cursor")
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	// New planning cursors have a fixed-width wire format: version, canonical
	// store UUID text, snapshot SHA-256 and unsigned big-endian page position.
	// Keeping them compact preserves exact same-owner commands at 2 KiB.
	if err != nil || len(data) != 77 || data[0] != 1 {
		return c, failure("cursor_invalid", "invalid recovery plan cursor")
	}
	position := binary.BigEndian.Uint64(data[69:])
	c = recoveryPlanCursor{Version: 1, Store: string(data[1:37]), Hash: hex.EncodeToString(data[37:69])}
	if uuid.Validate(c.Store) != nil || position > math.MaxInt64 {
		return c, failure("cursor_invalid", "invalid recovery plan cursor")
	}
	c.Position = int64(position)
	return c, nil
}

func (c recoveryPlanCursor) encode() string {
	// All callers supply a freshly computed or decoded/validated digest.
	digest, _ := hex.DecodeString(c.Hash)
	data := make([]byte, 77)
	data[0] = byte(c.Version)
	copy(data[1:37], c.Store)
	copy(data[37:69], digest)
	binary.BigEndian.PutUint64(data[69:], uint64(c.Position))
	return base64.RawURLEncoding.EncodeToString(data)
}

// PlanRecovery executes once. Pagination always recomputes the full fingerprint
// under fresh exclusive leases; formatting never opens storage or scans again.
func PlanRecovery(ctx context.Context, path, key string, options RecoveryPlanOptions) (view RecoveryPlan, retErr error) {
	return planRecovery(ctx, path, key, options, nil)
}

func planRecovery(ctx context.Context, path, key string, options RecoveryPlanOptions, step func(context.Context) error) (view RecoveryPlan, retErr error) {
	view = RecoveryPlan{Version: EnvelopeVersion, Kind: "recovery_plan", Status: "unavailable", ObservedAt: time.Now().UTC(), timeout: options.Timeout}
	if err := options.Validate(); err != nil {
		errors.As(err, &view.Error)
		view.Status = "invalid_argument"
		return view, err
	}
	if options.Cursor != "" {
		cursor, err := decodeRecoveryCursor(options.Cursor)
		if err != nil {
			return view, err
		}
		// Re-encode before emitting a shell command: base64 decoding accepts
		// CR/LF, but user-supplied cursor text must never become shell syntax.
		options.Cursor = cursor.encode()
	}
	ctx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	view.EffectiveTimeout = max(time.Duration(0), time.Until(deadline)).String()
	defer func() {
		if retErr == nil {
			return
		}
		var typed *Error
		if !errors.As(retErr, &typed) {
			typed = &Error{Code: "recovery_inventory_incomplete", Message: "could not read the complete offline inventory; preserve history and repair storage availability"}
		}
		if typed.Code == "recovery_capacity" && view.Complete {
			view.Token = ""
			safe := *typed
			safe.cause = retErr
			view.Status, view.Error, retErr = typed.Code, &safe, &safe
			view.NextStep = "Complete preview; no applicable token. Review remaining pages, free space or increase runs.max_retained_bytes via offline capy runs upgrade, then obtain a fresh plan."
			view = BoundRecoveryPlan(view, len(view.Subjects))
			return
		}
		view.Token, view.NextCursor, view.NextCommand, view.NextTimeout = "", "", "", ""
		view.Complete = false
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			retErr = errors.Join(failure("recovery_inventory_incomplete", "offline inventory did not complete within its effective timeout; no applicable token was produced"), ctx.Err())
			next := 15 * time.Minute
			if options.Timeout >= next/3 {
				next = time.Duration(math.MaxInt64)
				if options.Timeout <= time.Duration(math.MaxInt64)/3 {
					next = 3 * options.Timeout
				}
			}
			view.NextStep = "Keep store users stopped and retry the full plan with a longer --timeout; an earlier caller deadline must also be raised."
			view.NextCommand = recoveryCommand(path) + " --timeout " + next.String()
			if options.Cursor != "" {
				view.NextCommand += " --cursor " + options.Cursor
			}
			if recoveryCommand(path) == "" {
				view.NextCommand = ""
				view.NextCursor, view.NextTimeout = options.Cursor, next.String()
				view.NextStep += " Select this same physical owner explicitly with --project-dir."
			}
		} else if errors.Is(ctx.Err(), context.Canceled) {
			retErr = errors.Join(failure("recovery_inventory_incomplete", "offline inventory was cancelled; no applicable token was produced"), ctx.Err())
			view.NextStep = "Planning was cancelled. Keep store users stopped and retry the same review when ready; increasing the timeout is not required."
		} else {
			view.NextStep = "Inspect the same selected owner with capy runs inspect and address the reported refusal; no recovery was applied."
		}
		if !errors.As(retErr, &typed) {
			typed = &Error{Code: "recovery_inventory_incomplete", Message: "could not read the complete offline inventory; preserve history and repair storage availability"}
		}
		// Preserve the causal chain for callers without exporting backend text
		// through either the returned error or the captured public envelope.
		safe := *typed
		safe.cause = retErr
		view.Error = &safe
		retErr = &safe
		view.Status = typed.Code
	}()
	a, err := openRecoveryAdmin(ctx, path, key)
	if err != nil {
		return view, err
	}
	defer func() { retErr = errors.Join(retErr, a.close()) }()
	view.StoreID, view.command = a.id, recoveryCommand(path)
	var cursor recoveryPlanCursor
	if options.Cursor != "" {
		cursor, err = decodeRecoveryCursor(options.Cursor)
		if err != nil {
			return view, err
		}
		if cursor.Store != a.id {
			return view, staleRecoveryPlan()
		}
	}
	view.position = cursor.Position
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return view, err
	}
	defer tx.Rollback()
	if err := buildRecoveryInventory(ctx, tx, a, &view, options.Limit, step); err != nil {
		return view, err
	}
	if cursor.Hash != "" && cursor.Hash != view.fingerprint {
		return view, staleRecoveryPlan()
	}
	if cursor.Position > view.Counts.Subjects {
		return view, failure("cursor_invalid", "recovery cursor position exceeds the complete inventory")
	}
	if err := a.verifyTarget(); err != nil {
		return view, err
	}
	if err := tx.Commit(); err != nil {
		return view, err
	}
	if err := ctx.Err(); err != nil {
		return view, err
	}
	view.Complete = true
	view.NextStep = "Preview only; application is unavailable in this binary. Leases do not prove old groups stopped. Expiry estimates use preview time; later quota pressure may evict output sooner."
	if view.Counts.administrative() == 0 {
		view.Status = "no_recovery_needed"
		view.NextStep = "No administrative recovery is needed; any proven-safe backlog remains for ordinary reconciliation. No token or audit record was created."
	} else {
		if view.Capacity == nil || !view.Capacity.Sufficient {
			return view, failure("recovery_capacity", "insufficient sampled metadata or filesystem headroom; free space or increase runs.max_retained_bytes and use offline capy runs upgrade, then obtain a fresh plan; no logs were evicted")
		}
		view.Status = "ready"
		b, _ := json.Marshal(inspectionPlan{Version: 1, Store: a.id, Hash: recoveryPlanHash(a.id, a.binding, view.fingerprint), Target: a.binding, Snapshot: view.fingerprint})
		view.Token = base64.RawURLEncoding.EncodeToString(b)
	}
	view = BoundRecoveryPlan(view, len(view.Subjects))
	return view, nil
}

func recoveryCommand(path string) string {
	_, command := inspectionOwner(filepathOwner(path))
	if command == "" {
		return ""
	}
	return strings.TrimSuffix(command, "inspect") + "recover"
}

func filepathOwner(path string) string {
	// All filesystem admission happens in openRecoveryAdmin. This helper only
	// formats an already selected path and is also used for early refusals.
	return filepath.Dir(filepath.Dir(filepath.Dir(path)))
}

func BoundRecoveryPlan(view RecoveryPlan, entries int) RecoveryPlan {
	compact := entries <= 0
	if len(view.Subjects) > 0 {
		entries = max(1, min(entries, len(view.Subjects)))
		view.Subjects = append([]RecoveryPlanSubject(nil), view.Subjects[:entries]...)
	}
	if compact {
		view.MetadataOmitted = true
		if view.Status == "ready" {
			view.NextStep = "Preview only; no apply. Expiry is estimated; quota may evict sooner. Leases do not prove old groups stopped."
		}
		for i := range view.Subjects {
			view.Subjects[i].EstimatedExpiry = nil
			view.Subjects[i].EstimatedAuditExpiry = nil
			e := view.Subjects[i].Evidence
			// The stable key retains generation/slot identity. Full optional
			// evidence is available through inspect for the retained run/incident.
			view.Subjects[i].Evidence = InspectionSubject{Kind: e.Kind, RunID: e.RunID, IncidentID: e.IncidentID, Attribution: e.Attribution, Reason: e.Reason, BlocksAdmission: e.BlocksAdmission}
		}
	}
	if view.Complete && view.position+int64(len(view.Subjects)) < view.Counts.Subjects {
		view.Omitted = true
		view.NextCursor = (recoveryPlanCursor{Version: 1, Store: view.StoreID, Hash: view.fingerprint, Position: view.position + int64(len(view.Subjects))}).encode()
		view.NextTimeout = view.timeout.String()
		if view.command != "" && !compact {
			view.NextCommand = view.command + " --cursor " + view.NextCursor + " --timeout " + view.timeout.String()
		} else {
			view.NextCommand = ""
			if !strings.HasPrefix(view.NextStep, recoveryPageSelection) {
				view.NextStep = recoveryPageSelection + view.NextStep
			}
		}
	} else if view.Complete {
		view.Omitted, view.NextCursor, view.NextCommand, view.NextTimeout = false, "", "", ""
	}
	return view
}
