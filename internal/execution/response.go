package execution

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/serpro69/capy/internal/runstore"
	"github.com/serpro69/capy/internal/sanitize"
)

// Budget measures the complete transport encoding, including fallback text and
// escaping. Builders must preserve essential fields and use the allowance only
// for optional text or a recoverable page. At most 18 bounded builds are needed.
func Budget[T any](limit int, build func(int) (T, error), encode func(any) ([]byte, error)) (T, error) {
	var zero T
	if limit < 2048 || limit > 65536 {
		return zero, errors.New("invalid response budget")
	}
	if encode == nil {
		encode = json.Marshal
	}
	for allowance := limit / 2; ; allowance /= 2 {
		result, err := build(allowance)
		if err != nil {
			return zero, err
		}
		data, err := encode(result)
		if err != nil {
			return zero, err
		}
		if len(data) <= limit {
			return result, nil
		}
		if allowance == 0 {
			return zero, errors.New("essential response exceeds budget")
		}
	}
}

// SafeMessage redacts the complete value before shortening it. Never truncate
// the input before recognition, or a partial credential could become public.
func SafeMessage(message string, allowance int) string {
	safe := sanitize.StripSecrets(message)
	if len(safe) <= allowance {
		return safe
	}
	for allowance > 0 && !utf8.RuneStart(safe[allowance]) {
		allowance--
	}
	return safe[:allowance]
}

func BoundResult(result runstore.Result, allowance int) runstore.Result {
	if result.Recovery != nil && allowance < 256 && (result.Redaction.CoverageCursor != nil || result.Index.CoverageCursor != nil) {
		// Coverage summaries are optional response metadata, recoverable through
		// read/search. Keep the raw committed boundary, all execution facts and
		// recovery authority; never alter stored snapshots or read continuations.
		result.Redaction.CoverageCursor, result.Index.CoverageCursor = nil, nil
		result.MetadataOmitted = true
	}
	if result.Capacity != nil {
		capacity := *result.Capacity
		count := min(len(capacity.Runs), allowance/128)
		capacity.Runs = capacity.Runs[:count]
		capacity.Omitted = count < len(result.Capacity.Runs)
		result.Capacity = &capacity
	}
	result = boundPreview(result, allowance)
	if result.Error != nil {
		safe := BoundError(result.Error, allowance)
		if safe.Message != result.Error.Message || safe.Detail != result.Error.Detail || safe.RecoveryHint != nil && (safe.RecoveryHint.Subject == nil && result.Error.RecoveryHint.Subject != nil || safe.RecoveryHint.InspectCommand != result.Error.RecoveryHint.InspectCommand) {
			result.MetadataOmitted = true
		}
		result.Error = safe
	}
	if result.Index.Error != nil {
		safe := *result.Index.Error
		safe.Message = SafeMessage(safe.Message, allowance)
		if safe.Message != result.Index.Error.Message {
			result.MetadataOmitted = true
		}
		result.Index.Error = &safe
	}
	return result
}

// RunRecoveryText describes administrative closure without inferring process
// success or cleanup proof. Recovery identifiers remain essential structured
// metadata even when optional previews/messages are shortened by Budget.
func RunRecoveryText(recovery *runstore.Recovery) string {
	if recovery == nil {
		return ""
	}
	if recovery.State != "operator_resolved" {
		return "Recovery concern unresolved; inspect this owner's recorded blockers.\n"
	}
	text := fmt.Sprintf("Operator resolution: receipt %s; basis %s. Original execution evidence is unchanged.\n", recovery.ReceiptID, recovery.Basis)
	if recovery.DispositionID != "" && recovery.CaptureClosed {
		text += "Capture is administratively closed: stop polling at end_of_available; EOF still reflects the original execution evidence.\n"
	}
	if recovery.RetentionExpiresAt != nil {
		text += "Retained output deadline: " + recovery.RetentionExpiresAt.UTC().Format(time.RFC3339Nano) + "; quota may evict sooner.\n"
	}
	return text
}

// BoundRead keeps the page's continuation and byte coordinates authoritative.
// A compact historical read can omit the nested status-only capture boundary;
// it is still available through status, and never substitutes for NextCursor.
func BoundRead(page runstore.ReadPage, allowance int) runstore.ReadPage {
	page.Run = BoundResult(page.Run, allowance)
	if page.Run.Recovery != nil && allowance < 256 && page.Run.Capture.LastCommittedCursor != nil {
		page.Run.Capture.LastCommittedCursor = nil
		page.Run.MetadataOmitted = true
	}
	return page
}

// BoundList preserves membership, continuation, index state and recovery
// authority. Index cursors/error prose are also available through status/search;
// one rich historical entry must fit after pagination reaches its minimum.
func BoundList(page runstore.ListPage, allowance int) runstore.ListPage {
	entries := make([]runstore.Summary, len(page.Entries))
	copy(entries, page.Entries)
	page.Entries = entries
	for i := range page.Entries {
		entry := &page.Entries[i]
		if entry.Index == nil {
			continue
		}
		bounded := BoundResult(runstore.Result{Recovery: entry.Recovery, Index: *entry.Index}, allowance/max(1, len(entries)))
		entry.Index = &bounded.Index
		entry.MetadataOmitted = entry.MetadataOmitted || bounded.MetadataOmitted
		label := SafeMessage(entry.Label, allowance/max(1, len(entries)))
		entry.MetadataOmitted = entry.MetadataOmitted || label != entry.Label
		entry.Label = label
	}
	return page
}

// BoundError retains the refusal and next action even when optional attribution
// and safely quoted owner commands cannot fit the complete transport envelope.
func BoundError(err *runstore.Error, allowance int) *runstore.Error {
	safe := *err
	safe.Message = SafeMessage(safe.Message, allowance)
	safe.Detail = SafeMessage(safe.Detail, allowance)
	if err.RecoveryHint != nil {
		hint := *err.RecoveryHint
		if allowance < 512 {
			hint.Subject = nil
		}
		if allowance < 256 {
			hint.InspectCommand = ""
		}
		// Fixed producer text, never user/backend data. It cannot disappear with
		// the optional message allowance, unlike ordinary diagnostic prose.
		if safe.Message != err.Message {
			safe.Message = "No command was admitted."
		}
		safe.RecoveryHint = &hint
	}
	return &safe
}

// RecoveryText is the text-only transport's essential next action. Detailed
// attribution stays in structured data; the same-owner selection is explicit.
func RecoveryText(err *runstore.Error) string {
	if err == nil || err.RecoveryHint == nil {
		return ""
	}
	h := err.RecoveryHint
	text := " " + h.NextStep
	if err.Detail != "" {
		text += " " + err.Detail
	}
	if h.InspectCommand != "" {
		text += " Next: " + h.InspectCommand
	}
	return text
}

func boundPreview(result runstore.Result, allowance int) runstore.Result {
	if len(result.Preview) > allowance {
		end := allowance
		for end > 0 && !utf8.RuneStart(result.Preview[end]) {
			end--
		}
		result.Preview = result.Preview[:end]
		result.MetadataOmitted = true
	}
	return result
}

func ExecutionFailed(result runstore.Result) bool {
	switch result.State {
	case "admitted", "queued", "running":
		return result.Capture.State == "incomplete"
	case "exited":
		return result.ExitCode == nil || *result.ExitCode != 0 || result.Signal != nil || result.Capture.State != "complete"
	default:
		return true
	}
}
