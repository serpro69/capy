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
				code, repair = typed.Code, "restore the selected credential or stop owners and old execution groups, then explicitly run capy runs reset --discard for this owner (permanently deletes run logs)"
			case "policy_mismatch":
				code, repair = typed.Code, "stop and reload owners with a compatible capy version and accounting build"
			case "schema_mismatch":
				// Use authenticated numeric metadata, never backend/stored error text.
				code = typed.Code
				repair = fmt.Sprintf("stored schema=%d, supported schema=%d; use a compatible capy binary; no data was discarded", report.SchemaVersion, report.SupportedSchemaVersion)
				if report.SchemaVersion == 8 && report.SupportedSchemaVersion == 9 {
					repair = "stored schema=8, supported schema=9; stop all run owners/readers and supervisors, then start captured execution to upgrade; inspection does not migrate or discard logs"
				}
			case "credential_unavailable":
				code, repair = typed.Code, "repair the selected knowledge credential and reload; no fallback key was tried"
			case "storage_busy":
				code, repair = typed.Code, "retry inspection after the current reset, policy change or schema upgrade"
			}
		}
		return append(results, CheckResult{Name: "Run store", Status: Warn, Detail: code + "; " + repair})
	}
	if !report.Exists {
		return append(results, CheckResult{Name: "Run store", Status: Pass, Detail: "not initialized; inspection created no run store"})
	}
	status, detail := Pass, fmt.Sprintf("readable; stored schema=%d, supported schema=%d; inspection does not prove sandbox writability or hook activation", report.SchemaVersion, report.SupportedSchemaVersion)
	if report.AdmissionBlocked {
		status, detail = Fail, "cleanup_unverified; stop owners and old execution groups before explicit capy runs reset --discard (permanently deletes run logs); never reset as an automatic retry"
	}
	results = append(results, CheckResult{Name: "Run store", Status: status, Detail: detail})
	p := report.Policy
	status, detail = Pass, fmt.Sprintf("revision=%d schema=%d redaction=%d accounting=%s ttl_hours=%d max_run_bytes=%d max_retained_bytes=%d", p.Revision, p.SchemaVersion, p.RedactionVersion, p.AccountingMode, p.TTLHours, p.MaxRunBytes, p.MaxRetainedBytes)
	if p.TTLHours != cfg.TTLHours || p.MaxRunBytes != cfg.MaxRunBytes || p.MaxRetainedBytes != cfg.MaxRetainedBytes {
		status = Warn
		detail += "; configured policy differs: stop all owners/readers and supervisors, then reload matching configuration; inspection did not publish a revision"
	}
	results = append(results,
		CheckResult{Name: "Run policy", Status: status, Detail: detail},
		CheckResult{Name: "Run usage", Status: Pass, Detail: fmt.Sprintf("accounted=%d bytes (includes index debit=%d); physical DB=%d WAL=%d other sidecars=%d bytes; quota is not a filesystem ceiling", report.AccountedBytes, report.IndexBytes, report.DBBytes, report.WALBytes, report.SidecarBytes)},
		CheckResult{Name: "Run inventory", Status: Pass, Detail: fmt.Sprintf("%d retained, %d unfinished records (not proof of live processes); showing %d recent runs; use capy runs list or capy_runs(action=list) for recovery", report.RetainedRuns, report.UnfinishedRuns, len(report.Recent))},
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
		if unicode.IsControl(r) {
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
