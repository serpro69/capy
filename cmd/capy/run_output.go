package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	"github.com/serpro69/capy/internal/execution"
	"github.com/serpro69/capy/internal/platform"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/spf13/cobra"
)

type runQueryResult struct {
	Version int                   `json:"version"`
	Kind    string                `json:"kind"`
	Batch   *runstore.BatchResult `json:"batch,omitempty"`
	Run     *runstore.Result      `json:"run,omitempty"`
	Error   *runstore.Error       `json:"error,omitempty"`
}

func (r runQueryResult) MarshalJSON() ([]byte, error) {
	type plain runQueryResult
	if r.Run != nil || r.Batch != nil {
		return json.Marshal(plain(r))
	}
	return json.Marshal(struct {
		plain
		Cleanup any `json:"cleanup"`
	}{plain: plain(r)})
}

func runTarget(cmd *cobra.Command) (*knowledgeTarget, error) {
	explicit, err := cmd.Flags().GetString("project-dir")
	if err != nil {
		return nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	owner, err := platform.ResolveRunOwner(cwd, explicit)
	if err != nil {
		return nil, err
	}
	return loadProjectKnowledgeTarget(owner)
}

func commandRunError(err error) *runstore.Error {
	var typed *runstore.Error
	if errors.As(err, &typed) {
		copy := *typed
		return &copy
	}
	return &runstore.Error{Code: "storage_unavailable", Message: err.Error()}
}
func runExit(code int) error {
	if code == 0 {
		return nil
	}
	return &exitError{code: code, err: errors.New("captured run command failed")}
}
func terminalText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return '�'
		}
		return r
	}, s)
}
func runText(result runstore.Result) string {
	var b strings.Builder
	if result.RunID == nil {
		b.WriteString("Execution not started.\n")
	} else {
		fmt.Fprintf(&b, "Run %s: %s; capture %s (%d/%d bytes).\n", *result.RunID, result.State, result.Capture.State, result.Capture.CapturedBytes, result.Capture.ObservedBytes)
		fmt.Fprintf(&b, "Cleanup: %s / %s.\n", result.Cleanup.Scope, result.Cleanup.State)
	}
	if result.ExitCode != nil {
		fmt.Fprintf(&b, "Exit code: %d.\n", *result.ExitCode)
	}
	if result.Signal != nil {
		fmt.Fprintf(&b, "Signal: %d.\n", *result.Signal)
	}
	if result.Error != nil {
		fmt.Fprintf(&b, "%s: %s\n", result.Error.Code, terminalText(result.Error.Message))
	}
	if result.Index.State != "" && result.Index.State != "not_requested" {
		fmt.Fprintf(&b, "Index: %s.\n", result.Index.State)
		if result.Index.Error != nil {
			fmt.Fprintf(&b, "%s: %s\n", result.Index.Error.Code, terminalText(result.Index.Error.Message))
		}
	}
	if result.Preview != "" {
		b.WriteString(terminalText(result.Preview))
		b.WriteByte('\n')
	}
	if result.RunID != nil {
		fmt.Fprintf(&b, "Recover: capy runs status --run-id %s; capy runs read --run-id %s. Do not repeat execution to recover output.\n", *result.RunID, *result.RunID)
	}
	return b.String()
}
func encodeRunText(value any) ([]byte, error) {
	var b strings.Builder
	switch v := value.(type) {
	case runstore.UpgradeResult:
		if v.Policy != nil && v.AdmissionBlocked != nil {
			fmt.Fprintf(&b, "Run upgrade: schema %d → %d; policy revision %d → %d; retained quota %d bytes.\n", v.FromSchema, v.SchemaVersion, v.PreviousPolicyRevision, v.Policy.Revision, v.Policy.MaxRetainedBytes)
			if v.QuotaIncreased {
				b.WriteString("Quota increase committed and retained independently of migration.\n")
			}
			fmt.Fprintf(&b, "Recorded admission block=%t. No command admitted; quarantine and history preserved.\n", *v.AdmissionBlocked)
		} else {
			b.WriteString("Run upgrade: authenticated schema/policy or admission state unavailable. No command admitted.\n")
		}
		if v.Error != nil {
			fmt.Fprintf(&b, "%s: %s\n", v.Error.Code, terminalText(v.Error.Message))
		}
		b.WriteString("Inspect with capy runs inspect; reload compatible sessions after upgrade.\n")
	case runstore.Inspection:
		fmt.Fprintf(&b, "Run inspection: %s; store=%s schema=%d. Owner: %s\n", v.Admission, v.StoreID, v.SchemaVersion, v.Owner)
		fmt.Fprintf(&b, "Incidents: recorded=%d inferred=%d unattributed=%d unresolved=%d.\n", v.Counts.RecordedIncidents, v.Counts.InferredIncidents, v.Counts.UnattributedIncidents, v.Counts.UnresolvedIncidents)
		if v.Receipt != nil {
			fmt.Fprintf(&b, "Receipt %s: %s/%s; pruning=%t. %s\n", v.Receipt.ID, v.Receipt.Method, v.Receipt.Basis, v.Receipt.Pruning, terminalText(v.Receipt.Explanation))
		}
		fmt.Fprintf(&b, "Recorded quarantine markers=%d; inferred candidates=%d; unattributed pending=%d. Details: %s; omitted=%t.\n",
			v.Counts.QuarantineMarkers, v.Counts.InferredCandidates, v.Counts.UnattributedPending, v.Details, v.Omitted)
		fmt.Fprintf(&b, "%s.\n", v.Consistency)
		for _, subject := range v.Subjects {
			fmt.Fprintf(&b, "%s %s: %s (%s); recorded_quarantine=%t", subject.Kind, subject.RunID, subject.Reason, subject.Attribution, subject.BlocksAdmission)
			if subject.IncidentID != "" {
				fmt.Fprintf(&b, "; incident=%s revision=%d stage=%s", subject.IncidentID, subject.IncidentRevision, subject.DetectionStage)
			}
			if subject.ReceiptID != "" {
				fmt.Fprintf(&b, "; receipt=%s", subject.ReceiptID)
			}
			if subject.EvidenceInconsistent {
				b.WriteString("; inconsistent evidence: operator confirmation cannot override")
			}
			if subject.Slot != nil {
				fmt.Fprintf(&b, "; slot=%d generation=%s", *subject.Slot, subject.Generation)
			}
			if subject.Cleanup != nil {
				fmt.Fprintf(&b, "; last recorded state=%s phase=%s cleanup=%s/%s", subject.State, subject.Phase, subject.Cleanup.Scope, subject.Cleanup.State)
			}
			if len(subject.MissingEvidence) > 0 {
				fmt.Fprintf(&b, "; missing=%s", strings.Join(subject.MissingEvidence, ","))
			}
			if subject.OutputAvailable != nil {
				fmt.Fprintf(&b, "; output_available=%t", *subject.OutputAvailable)
			}
			b.WriteByte('\n')
		}
		if v.NextCursor != "" {
			fmt.Fprintf(&b, "Continue with the same owner and selector: --cursor %s\n", v.NextCursor)
		}
		fmt.Fprintf(&b, "%s: %s\n", v.Action, v.NextStep)
		if v.InspectCommand != "" {
			fmt.Fprintf(&b, "Inspect command: %s\n", v.InspectCommand)
		}
	case runstore.Result:
		b.WriteString(runText(v))
	case runQueryResult:
		if v.Batch != nil {
			fmt.Fprintf(&b, "Batch %s: %s; failures=%d incomplete=%d. Recover: capy runs list --batch-id %s.\n", v.Batch.BatchID, v.Batch.State, v.Batch.FailedCount, v.Batch.IncompleteCount, v.Batch.BatchID)
			fmt.Fprintf(&b, "Cleanup: %s / %s.\n", v.Batch.Cleanup.Scope, v.Batch.Cleanup.State)
			if v.Batch.Error != nil {
				fmt.Fprintf(&b, "%s: %s\n", v.Batch.Error.Code, terminalText(v.Batch.Error.Message))
			}
		}
		if v.Run != nil {
			b.WriteString(runText(*v.Run))
		}
		if v.Error != nil {
			fmt.Fprintf(&b, "%s: %s\n", v.Error.Code, terminalText(v.Error.Message))
		}
	case runstore.ReadPage:
		fmt.Fprintf(&b, "Run %s: %s; capture %s.\n", v.RunID, v.Run.State, v.Run.Capture.State)
		fmt.Fprintf(&b, "Cleanup: %s / %s.\n", v.Run.Cleanup.Scope, v.Run.Cleanup.State)
		if v.Raw {
			b.WriteString("Unsanitized raw bytes (base64).\n")
		}
		for _, f := range v.Fragments {
			fmt.Fprintf(&b, "[%s byte %d line %d] ", f.Stream, f.ByteOffset, f.StartLine)
			if v.Raw {
				b.WriteString(base64.StdEncoding.EncodeToString(f.Data))
			} else {
				b.WriteString(terminalText(f.Text))
			}
			b.WriteByte('\n')
		}
		if v.RedactionPending {
			b.WriteString("Redaction pending; poll this cursor or read an independent stream.\n")
		}
		if v.NextCursor != nil {
			fmt.Fprintf(&b, "next_cursor: %s\n", *v.NextCursor)
		}
		fmt.Fprintf(&b, "end_of_available=%t eof=%t\n", v.EndOfAvailable, v.EOF)
	case runstore.ListPage:
		for _, entry := range v.Entries {
			fmt.Fprintf(&b, "%s %s capture=%s owner=%s output_available=%t\n", entry.RunID, entry.State, entry.CaptureState, entry.OwnerType, entry.OutputAvailable)
			fmt.Fprintf(&b, "  cleanup=%s/%s\n", entry.Cleanup.Scope, entry.Cleanup.State)
			if entry.Ordinal != nil {
				fmt.Fprintf(&b, "  ordinal=%d label=%s\n", *entry.Ordinal, terminalText(entry.Label))
			}
		}
		if len(v.Entries) == 0 {
			b.WriteString("No matching runs.\n")
		}
		if v.NextCursor != nil {
			fmt.Fprintf(&b, "next_cursor: %s\n", *v.NextCursor)
		}
	case runstore.SearchPage:
		selector := v.RunID
		if v.BatchID != "" {
			selector = v.BatchID
		}
		fmt.Fprintf(&b, "Run/batch %s: index=%s partial=%t; searched stdout=%d stderr=%d raw bytes.\n", selector, v.Index.State, v.Partial, v.Coverage["stdout"], v.Coverage["stderr"])
		fmt.Fprintf(&b, "Cleanup: %s / %s.\n", v.Cleanup.Scope, v.Cleanup.State)
		if v.Error != nil {
			fmt.Fprintf(&b, "%s: %s\n", v.Error.Code, terminalText(v.Error.Message))
		}
		for _, group := range v.Results {
			for _, hit := range group.Hits {
				fmt.Fprintf(&b, "[query %d run %s %s bytes %d–%d line %d] %s\nread_cursor: %s\n", group.Query, hit.RunID, hit.Stream, hit.RawStart, hit.RawEnd, hit.StartLine, terminalText(hit.Text), hit.ReadCursor)
			}
		}
		for _, group := range v.Knowledge {
			for _, hit := range group.Hits {
				fmt.Fprintf(&b, "[query %d knowledge source %s] %s\n%s\n", group.Query, terminalText(hit.Source), terminalText(hit.Title), terminalText(hit.Text))
			}
		}
		if v.KnowledgeError != nil {
			fmt.Fprintf(&b, "%s: %s\n", v.KnowledgeError.Code, terminalText(v.KnowledgeError.Message))
		}
		if v.Omitted {
			b.WriteString("Results shortened to the response budget; use read_cursor with the same run and stream.\n")
		}
		if v.Partial {
			b.WriteString("Only the reported prefix was searched; repeat search to resume indexing, or read retained output.\n")
		}
	default:
		return nil, errors.New("unknown run response")
	}
	return []byte(b.String()), nil
}

func encodeRunJSON(value any) ([]byte, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Render before writing once. The final byte budget includes the JSON newline,
// text diagnostics and recovery cursor, using the same budgeter as MCP.
func writeRunResponse(cmd *cobra.Command, limit int, jsonOutput bool, code int, build func(int) (any, error)) error {
	encode := encodeRunText
	if jsonOutput {
		encode = encodeRunJSON
	}
	value, err := execution.Budget(limit, build, encode)
	if err != nil {
		code = 1
		value = runQueryResult{Version: runstore.EnvelopeVersion, Kind: "query", Error: &runstore.Error{Code: "response_unavailable", Message: "result unavailable; recover existing runs before retrying execution"}}
	}
	b, err := encode(value)
	if err != nil {
		return runExit(74)
	}
	n, err := cmd.OutOrStdout().Write(b)
	if err != nil || n != len(b) {
		return &exitError{code: 74, err: io.ErrShortWrite}
	}
	return runExit(code)
}
func writeExecutionRejection(cmd *cobra.Command, jsonOutput bool, limit, exit int, code, message string) error {
	result := runstore.Rejection(code, message)
	return writeRunResponse(cmd, limit, jsonOutput, exit, func(n int) (any, error) { return execution.BoundResult(result, n), nil })
}
func writeQueryFailure(cmd *cobra.Command, jsonOutput bool, limit, exit int, err error) error {
	failure := commandRunError(err)
	return writeRunResponse(cmd, limit, jsonOutput, exit, func(n int) (any, error) {
		safe := *failure
		safe.Message = execution.SafeMessage(safe.Message, n)
		return runQueryResult{Version: runstore.EnvelopeVersion, Kind: "query", Error: &safe}, nil
	})
}
