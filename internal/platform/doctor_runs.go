package platform

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/serpro69/capy/internal/sanitize"
)

// CheckRuns is shared by CLI doctor, MCP doctor and MCP stats. The caller must
// resolve the run owner first; a credential-resolution failure is an empty key,
// never a fallback credential. No diagnostic error includes backend error text.
func CheckRuns(ctx context.Context, project, key string, cfg config.RunsConfig) []CheckResult {
	path, err := config.ResolveRunDBPath(project)
	if err != nil {
		return []CheckResult{{Name: "Run store", Status: Fail, Detail: "selected run owner is unavailable; check --project-dir and installation bindings"}}
	}
	results := []CheckResult{
		{Name: "Run path", Status: Pass, Detail: diagnosticPath(path)},
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
			case "credential_unavailable":
				code, repair = typed.Code, "repair the selected knowledge credential and reload; no fallback key was tried"
			case "storage_busy":
				code, repair = typed.Code, "retry inspection after the current reset or policy change"
			}
		}
		return append(results, CheckResult{Name: "Run store", Status: Warn, Detail: code + "; " + repair})
	}
	if !report.Exists {
		return append(results, CheckResult{Name: "Run store", Status: Pass, Detail: "not initialized; inspection created no run store"})
	}
	status, detail := Pass, "readable; inspection does not prove sandbox writability or hook activation"
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
		results = append(results, CheckResult{Name: "Run " + run.ID, Status: Pass, Detail: fmt.Sprintf("state=%s%s capture=%s redaction=%s index=%s observed=%d captured=%d bytes expires=%s; status/read by run ID",
			run.State, childStatus, run.Capture, run.Redaction, run.Index, run.ObservedBytes, run.CapturedBytes, expiry)})
	}
	return results
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
