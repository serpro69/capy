package runstore

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
	"github.com/serpro69/capy/internal/executor"
)

const diagnosticRunLimit = 8

// Diagnostics is a read-only snapshot. Unfinished records are not proof of live
// processes: inspection deliberately does not reconcile owners or retire runs.
type Diagnostics struct {
	Exists                 bool
	SchemaVersion          int // authenticated run_meta version; also available on schema mismatch
	SupportedSchemaVersion int
	Policy                 Policy
	AccountedBytes         int64
	IndexBytes             int64
	DBBytes                int64
	WALBytes               int64
	SidecarBytes           int64
	RetainedRuns           int64
	UnfinishedRuns         int64
	AdmissionBlocked       bool
	Recent                 []RunDiagnostic
	Inspection             Inspection
}

// RunDiagnostic excludes invocation, output, cursors and stored error strings.
type RunDiagnostic struct {
	Cleanup                          executor.CleanupEvidence
	ID                               string
	State, Capture, Redaction, Index string
	ExitCode, Signal                 *int
	ObservedBytes, CapturedBytes     int64
	ExpiresAt                        *time.Time
}

// Inspect never initializes a cache, claims an owner, changes policy, runs
// maintenance or creates locks. Lifecycle ownership protects its read-only
// connection; the no-WAL immutable fallback requires exclusive ownership.
// The caller supplies the already-selected credential.
func Inspect(ctx context.Context, path, key string) (report Diagnostics, err error) {
	return inspectSnapshot(ctx, path, key, InspectionOptions{Limit: diagnosticRunLimit}, true)
}

func diagnosticRuns(ctx context.Context, tx *sql.Tx, now int64, version int) ([]RunDiagnostic, error) {
	// Bound each decoded metadata row even if the authenticated DB is damaged.
	rows, err := tx.QueryContext(ctx, `SELECT id,CASE WHEN length(result)<=65536 THEN result ELSE NULL END,expires_at,
		`+inspectionScopeSQL(version)+`,
		CASE WHEN length(CAST(cleanup AS BLOB))<=16 THEN cleanup ELSE NULL END
		FROM runs WHERE evicting=0 AND (terminal=0 OR expires_at>?)
		ORDER BY created_at DESC,id DESC LIMIT ?`, now, diagnosticRunLimit)
	if err != nil {
		return nil, storageError()
	}
	defer rows.Close()
	var recent []RunDiagnostic
	for rows.Next() {
		var id string
		var data []byte
		var expiry sql.NullInt64
		var cleanup executor.CleanupEvidence
		if err := rows.Scan(&id, &data, &expiry, &cleanup.Scope, &cleanup.State); err != nil {
			return nil, failure("recovery_inconsistent", "recent run metadata is inconsistent; inspection details are incomplete; preserve history and repair the metadata separately")
		}
		var result Result
		if uuid.Validate(id) != nil || len(data) == 0 || decodeRunResult(data, cleanup, &result) != nil {
			return nil, failure("recovery_inconsistent", "recent run metadata is inconsistent; inspection details are incomplete; preserve history and repair the metadata separately")
		}
		entry := RunDiagnostic{
			Cleanup:       cleanup,
			ID:            id,
			State:         diagnosticState(result.State, "admitted", "queued", "running", "exited", "start_failed", "timed_out", "cancelled", "interrupted", "skipped"),
			Capture:       diagnosticState(result.Capture.State, "not_started", "recording", "complete", "incomplete"),
			Redaction:     diagnosticState(result.Redaction.State, "ready", "pending", "unavailable"),
			Index:         diagnosticState(result.Index.State, "not_requested", "building", "ready", "quota_blocked", "failed"),
			ObservedBytes: max(0, result.Capture.ObservedBytes), CapturedBytes: max(0, result.Capture.CapturedBytes),
			ExitCode: result.ExitCode, Signal: result.Signal,
		}
		if expiry.Valid {
			at := time.UnixMilli(expiry.Int64).UTC()
			entry.ExpiresAt = &at
		}
		recent = append(recent, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, storageError()
	}
	return recent, nil
}

func diagnosticState(value string, allowed ...string) string {
	for _, state := range allowed {
		if value == state {
			return state
		}
	}
	return "unknown"
}
