package platform

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"
	"unicode"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/execution"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/serpro69/capy/internal/sanitize"
)

// CheckRuns is shared by CLI doctor, MCP doctor and MCP stats. The caller must
// resolve the run owner first; a credential-resolution failure is an empty key,
// never a fallback credential. No diagnostic error includes backend error text.
func CheckRuns(ctx context.Context, project, key string, cfg config.RunsConfig) []CheckResult {
	capability := CheckRunCapability()
	path, err := config.ResolveRunDBPath(project)
	if err != nil {
		return []CheckResult{capability, {Name: "Run store", Status: Fail, Detail: "selected run owner is unavailable; check --project-dir and installation bindings"}}
	}
	results := []CheckResult{
		{Name: "Run path", Status: Pass, Detail: diagnosticPath(path)},
		capability,
		{Name: "Run capacity", Status: Pass, Detail: fmt.Sprintf("configured per owner: max_active=%d, max_pending=%d; foreign-owner live capacity is not inferred from saved metadata", cfg.MaxActive, cfg.MaxPending)},
	}
	report, err := runstore.Inspect(ctx, path, key)
	if err != nil {
		code, repair := "storage_unavailable", "check selected path and permissions; no data was discarded"
		var typed *runstore.Error
		if errors.As(err, &typed) {
			switch typed.Code {
			case "wrong_key":
				code, repair = typed.Code, "restore the matching selected credential and reload; no data was discarded"
			case "policy_mismatch":
				code, repair = typed.Code, "stop and reload owners with a compatible capy version and accounting build"
			case "schema_mismatch":
				// Use authenticated numeric metadata, never backend/stored error text.
				code = typed.Code
				repair = fmt.Sprintf("stored schema=%d, supported schema=%d; use a compatible capy binary; no data was discarded", report.SchemaVersion, report.SupportedSchemaVersion)
			case "credential_unavailable":
				code, repair = typed.Code, "repair the selected knowledge credential and reload; no fallback key was tried"
			case "storage_busy":
				code, repair = typed.Code, "retry inspection after concurrent lifecycle work completes; a no-WAL snapshot requires a brief exclusive lease, so stop attached store users if contention persists"
			case "recovery_inconsistent":
				code, repair = typed.Code, "inspection details are incomplete; preserve history and repair inconsistent run metadata separately; operator confirmation cannot override it"
			}
		}
		status := Warn
		if report.AdmissionBlocked || report.Inspection.Counts.QuarantineMarkers > 0 {
			status = Fail
			repair = "recorded quarantine blocks admission; " + repair
		}
		return append(results, CheckResult{Name: "Run store", Status: status, Detail: code + "; " + repair})
	}
	if !report.Exists {
		return append(results, CheckResult{Name: "Run store", Status: Pass, Detail: "not initialized; inspection created no run store"})
	}
	view := report.Inspection
	status, detail := Pass, fmt.Sprintf("readable; stored schema=%d, supported schema=%d; admission unverified; inspection does not prove sandbox writability or hook activation", report.SchemaVersion, report.SupportedSchemaVersion)
	if report.SchemaVersion != report.SupportedSchemaVersion {
		status, detail = Warn, fmt.Sprintf("upgrade_required; stored schema=%d, supported schema=%d; legacy read-only diagnostics; inspection does not migrate", report.SchemaVersion, report.SupportedSchemaVersion)
	}
	if view.Admission == "recorded_blocked" {
		status, detail = Fail, "cleanup_unverified; recorded quarantine blocks admission; inspect the selected owner's blockers before stopping affected sessions and old execution groups"
	}
	results = append(results, CheckResult{Name: "Run store", Status: status, Detail: detail})
	status = Pass
	if view.Counts.QuarantineMarkers > 0 {
		status = Fail
	} else if view.Counts.UnattributedPending > 0 {
		status = Warn
	}
	results = append(results, CheckResult{Name: "Run blockers", Status: status, Detail: fmt.Sprintf("recorded quarantine markers=%d; inferred candidates=%d; unattributed pending=%d (not confirmed incidents or proof of live work); details=%s; %s",
		view.Counts.QuarantineMarkers, view.Counts.InferredCandidates, view.Counts.UnattributedPending, view.Details, view.NextStep)})
	if view.InspectCommand != "" {
		results = append(results, CheckResult{Name: "Run inspection command", Status: Pass, Detail: view.InspectCommand})
	}
	for _, subject := range view.Subjects {
		if subject.RunID == "" {
			continue
		}
		results = append(results, CheckResult{Name: "Run observation " + subject.RunID, Status: Warn,
			Detail: fmt.Sprintf("%s; attribution=%s; last recorded state=%s phase=%s; missing=%s; inspect --run-id %s for this owner",
				subject.Reason, subject.Attribution, subject.State, subject.Phase, strings.Join(subject.MissingEvidence, ","), subject.RunID)})
	}
	p := report.Policy
	status, detail = Pass, fmt.Sprintf("revision=%d schema=%d redaction=%d accounting=%s ttl_hours=%d max_run_bytes=%d max_retained_bytes=%d", p.Revision, p.SchemaVersion, p.RedactionVersion, p.AccountingMode, p.TTLHours, p.MaxRunBytes, p.MaxRetainedBytes)
	if p.TTLHours != cfg.TTLHours || p.MaxRunBytes != cfg.MaxRunBytes || p.MaxRetainedBytes != cfg.MaxRetainedBytes {
		status = Warn
		detail += "; configured policy differs: stop all owners/readers and supervisors, then reload matching configuration; inspection did not publish a revision"
	}
	results = append(results,
		CheckResult{Name: "Run policy", Status: status, Detail: detail},
		CheckResult{Name: "Run usage", Status: Pass, Detail: fmt.Sprintf("accounted=%d bytes (includes index debit=%d); physical DB=%d WAL=%d other sidecars=%d bytes; quota is not a filesystem ceiling", report.AccountedBytes, report.IndexBytes, report.DBBytes, report.WALBytes, report.SidecarBytes)},
		CheckResult{Name: "Run inventory", Status: Pass, Detail: fmt.Sprintf("%d retained, %d unfinished records (not proof of live processes); showing %d recent runs; capy runs inspect pages blocker observations independently of this recent sample", report.RetainedRuns, report.UnfinishedRuns, len(report.Recent))},
	)
	for _, run := range report.Recent {
		expiry := "unset"
		if run.ExpiresAt != nil {
			expiry = run.ExpiresAt.Format(time.RFC3339)
		}
		childStatus := ""
		if run.ExitCode != nil {
			childStatus += fmt.Sprintf(" exit=%d", *run.ExitCode)
		}
		if run.Signal != nil {
			childStatus += fmt.Sprintf(" signal=%d", *run.Signal)
		}
		results = append(results, CheckResult{Name: "Run " + run.ID, Status: Pass, Detail: fmt.Sprintf("state=%s%s cleanup=%s/%s capture=%s redaction=%s index=%s observed=%d captured=%d bytes expires=%s; status/read by run ID",
			run.State, childStatus, run.Cleanup.Scope, run.Cleanup.State, run.Capture, run.Redaction, run.Index, run.ObservedBytes, run.CapturedBytes, expiry)})
	}
	return results
}

// CheckRunCapability checks only compiled/OS support. On Linux it does not enable
// subreaping; on Darwin it reads the host version without launching a probe.
func CheckRunCapability() CheckResult {
	return checkRunCapability(runtime.GOOS, execution.Supported())
}

func checkRunCapability(goos string, unsupported error) CheckResult {
	result := CheckResult{Name: "Run execution", Status: Pass}
	if unsupported != nil {
		result.Status = Fail
		result.Detail = "unsupported host/build; capture requires Linux or macOS 26 on Apple Silicon with CGO; align CLI/PATH/MCP binaries and reload; no execution probe was run"
		return result
	}
	switch goos {
	case "linux":
		result.Detail = "available; cleanup scope=linux_descendants_v1, completed state=retired"
	case "darwin":
		result.Detail = "available on macOS 26 Apple Silicon; cleanup scope=darwin_process_group_observation_v1, completed state=observed; escaped or racing descendants may survive; observed does not prove host quiescence"
	default:
		result.Status = Fail
		result.Detail = "unsupported execution platform"
	}
	result.Detail += "; capability only, no execution probe; storage health and hook activation are separate"
	return result
}

func diagnosticPath(path string) string {
	path = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return '�'
		}
		return r
	}, sanitize.StripSecrets(path))
	runes := []rune(path)
	if len(runes) > 256 {
		return string(runes[:256]) + "… (path shortened)"
	}
	return path
}
