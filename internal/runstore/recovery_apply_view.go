package runstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/serpro69/capy/internal/sanitize"
)

// RecoveryAdmission is a snapshot of the cleanup gate, not a promise that a
// future command will pass capacity, credential or platform checks.
type RecoveryAdmission struct {
	Blocked             bool  `json:"blocked"`
	UnresolvedIncidents int64 `json:"unresolved_incidents"`
	PendingRuns         int64 `json:"pending_runs"`
	SafeReconciliation  int64 `json:"safe_reconciliation"`
	Revision            int64 `json:"recovery_revision"`
}

// RecoveryApplication is captured once before rendering. Missing admission
// means it was not established; rolled-back/uncertain writes are never healthy.
type RecoveryApplication struct {
	Version         int                   `json:"version"`
	Kind            string                `json:"kind"`
	Status          string                `json:"status"`
	Recovery        *InspectionReceipt    `json:"recovery,omitempty"`
	Admission       *RecoveryAdmission    `json:"admission,omitempty"`
	Capacity        *RecoveryPlanCapacity `json:"capacity_sample,omitempty"`
	Plan            string                `json:"plan,omitempty"`
	NextTimeout     string                `json:"next_timeout,omitempty"`
	NextCommand     string                `json:"next_command,omitempty"`
	NextStep        string                `json:"next_step"`
	MetadataOmitted bool                  `json:"metadata_omitted,omitempty"`
	Error           *Error                `json:"error,omitempty"`
}

// ApplyRecovery is the CLI-only administrative entry point. It never attaches
// an execution service, retries a transaction, or resubmits application work.
func ApplyRecovery(ctx context.Context, path, key string, options RecoveryApplyOptions) (RecoveryApplication, error) {
	return applyRecoveryView(ctx, path, key, options, nil)
}

func applyRecoveryView(ctx context.Context, path, key string, options RecoveryApplyOptions, hooks *recoveryApplyHooks) (RecoveryApplication, error) {
	view := RecoveryApplication{Version: EnvelopeVersion, Kind: "recovery_apply"}
	options, plan, err := normalizeRecoveryAttestation(options)
	if err != nil {
		view.Status = "invalid_argument"
		errors.As(err, &view.Error)
		view.NextStep = "Use capy runs recover --help; no recovery was applied."
		return view, err
	}
	// Canonicalize before using token text in commands (base64 accepts CR/LF).
	encoded, _ := json.Marshal(plan) // Fixed scalar fields cannot fail encoding.
	options.Plan = base64.RawURLEncoding.EncodeToString(encoded)
	result, err := applyRecovery(ctx, path, key, options, hooks)
	return recoveryApplicationView(path, options, result, err), err
}

// Projection is pure: failure/receipt combinations can be rendered without
// rerunning application or sampling different current blockers.
func recoveryApplicationView(path string, options RecoveryApplyOptions, result recoveryApplyResult, err error) RecoveryApplication {
	view := RecoveryApplication{Version: EnvelopeVersion, Kind: "recovery_apply"}
	view.Status = result.Status
	if result.Receipt != nil {
		r := result.Receipt
		view.Recovery = &InspectionReceipt{ID: r.ID, AppliedAt: r.AppliedAt, Method: r.Method, Basis: r.Basis, Explanation: sanitize.StripSecrets(r.Explanation), IncidentCount: r.IncidentCount, RunCount: r.RunCount, AuditExpiresAt: r.AuditExpiresAt, Pruning: r.Pruning}
	}
	_, inspect := inspectionOwner(filepathOwner(path))
	if err == nil {
		view.Admission = &RecoveryAdmission{Blocked: result.AdmissionBlocked, UnresolvedIncidents: result.UnresolvedIncidents, PendingRuns: result.PendingRuns, SafeReconciliation: result.SafeReconciliation, Revision: result.RecoveryRevision}
		view.NextStep = "Original execution/cleanup uncertainty is unchanged. Disposed output expires at recovery time + admission TTL; quota may evict sooner. Unrelated expiry is unchanged. Inspect the receipt for deadlines. Restart compatible sessions, inspect readiness, then deliberately run a harmless captured verification command. Recovery never replays work; later blockers still require review."
	} else {
		if !errors.As(err, &view.Error) {
			view.Error = &Error{Code: "storage_unavailable", Message: "recovery result unavailable; inspect the retained receipt before retrying"}
		}
		view.Status = view.Error.Code
		view.NextStep = "Select the same physical owner with --project-dir and inspect current blockers; address the reported refusal before retrying."
		if view.Status == "recovery_capacity" {
			view.Capacity = result.Capacity
		}
	}
	if view.Recovery != nil {
		if inspect != "" {
			view.NextCommand = inspect + " --receipt-id " + view.Recovery.ID
		}
		view.NextStep += " Receipt lookup: capy runs inspect --receipt-id " + view.Recovery.ID + "; select the same physical owner with --project-dir."
	}
	switch view.Status {
	case "recovery_outcome_unknown":
		view.Plan = options.Plan
		view.NextStep = "Commit outcome is unknown. Select the same physical owner with --project-dir; use capy runs inspect --plan with this plan. Receipt absence does not prove nonapplication. An exact retained-plan retry is idempotent; never replay execution."
		if inspect != "" {
			view.NextCommand = inspect + " --plan " + options.Plan
		}
	case "recovery_timeout":
		view.Plan = options.Plan
		view.NextTimeout = longerRecoveryTimeout(options.Timeout).String()
		view.NextStep = "No new recovery changes committed in this attempt. Keep store users stopped. Select the same physical owner with --project-dir; repeat recover --apply --plan with this plan, --confirm-stopped and the same --cleanup-basis/--reason, using next_timeout as --timeout. Raise any earlier caller deadline too."
		if command := recoveryCommand(path); command != "" {
			view.NextCommand = command + " --apply --plan " + options.Plan + " --confirm-stopped --cleanup-basis " + options.Basis + " --reason '" + strings.ReplaceAll(options.Explanation, "'", "'\"'\"'") + "' --timeout " + view.NextTimeout
		}
	}
	if view.Recovery != nil && err != nil {
		view.Plan, view.NextTimeout = "", ""
		if view.Status == "recovery_outcome_unknown" {
			// The transaction returned a receipt only after a confirmed commit
			// (or retained lookup). A subsequent close error cannot undo it.
			copy := *view.Error
			copy.Message = "recovery was applied; administrative close did not finish cleanly; inspect the retained receipt and current blockers"
			view.Error = &copy
		}
		view.NextStep = "The retained receipt records an applied recovery. Current blockers are unavailable; do not infer healthy admission or replay work. Select the same physical owner with --project-dir and use capy runs inspect --receipt-id " + view.Recovery.ID + "."
		view.NextCommand = ""
		if inspect != "" {
			view.NextCommand = inspect + " --receipt-id " + view.Recovery.ID
		}
	}
	if view.Recovery != nil && view.Recovery.Pruning {
		view.NextStep += " Receipt details are being pruned and may be unavailable; removed evidence cannot be reconstructed."
	}
	return view
}

func longerRecoveryTimeout(timeout time.Duration) time.Duration {
	if timeout < 5*time.Minute {
		return 15 * time.Minute
	}
	if timeout > time.Duration(math.MaxInt64)/3 {
		return time.Duration(math.MaxInt64)
	}
	return timeout * 3
}

// BoundRecoveryApplication only shortens optional captured detail. Receipt IDs,
// status, current/unknown admission, retry token and next action are essential.
func BoundRecoveryApplication(view RecoveryApplication, allowance int) RecoveryApplication {
	if allowance < 1024 {
		view.MetadataOmitted = true
		view.NextCommand = ""
		if view.Recovery != nil {
			r := *view.Recovery
			r.Explanation = ""
			view.Recovery = &r
		}
	}
	return view
}
