package runstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/serpro69/capy/internal/sqliteutil"
	"golang.org/x/sys/unix"
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
}

// RunDiagnostic excludes invocation, output, cursors and stored error strings.
type RunDiagnostic struct {
	ID                               string
	State, Capture, Redaction, Index string
	ExitCode, Signal                 *int
	ObservedBytes, CapturedBytes     int64
	ExpiresAt                        *time.Time
}

// Inspect never initializes a cache, claims an owner, changes policy, runs
// maintenance or creates locks. A shared lease protects its read-only SQLite
// connection from reset. The caller supplies the already-selected credential.
func Inspect(ctx context.Context, path, key string) (report Diagnostics, err error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if !filepath.IsAbs(path) || filepath.Base(path) != "runs.db" ||
		filepath.Base(filepath.Dir(path)) != "runs" ||
		filepath.Base(filepath.Dir(filepath.Dir(path))) != ".capy" {
		return report, storageError()
	}
	owner := filepath.Dir(filepath.Dir(filepath.Dir(path)))
	if physical, e := filepath.EvalSymlinks(owner); e != nil || physical != owner {
		return report, storageError()
	}
	report.Exists, err = inspectStore(path)
	if err != nil || !report.Exists {
		return report, err
	}
	if key == "" {
		return report, failure("credential_unavailable", "selected run credential is unavailable")
	}
	life, err := openRegular(filepath.Join(filepath.Dir(path), "lifecycle.lock"), false)
	if err != nil {
		return report, storageError()
	}
	defer func() {
		if closeErr := life.Close(); closeErr != nil {
			err = storageError()
		}
	}()
	if err := unix.Flock(int(life.Fd()), unix.LOCK_SH|unix.LOCK_NB); err != nil {
		return report, failure("storage_busy", "run store lifecycle is busy")
	}
	if exists, err := inspectStore(path); err != nil || !exists {
		return report, storageError()
	}
	db, err := sqliteutil.OpenWithCanary(ctx, sqliteutil.EncryptedDSN(path, key)+"&mode=ro&_busy_timeout=100", path, "selected run credential")
	if err != nil {
		return report, openError(err, path)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			err = storageError()
		}
	}()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	var version int
	var id string
	if err := db.QueryRowContext(ctx, "SELECT version,store_id FROM run_meta WHERE singleton=1").Scan(&version, &id); err != nil {
		return report, storageError()
	}
	if uuid.Validate(id) != nil {
		return report, storageError()
	}
	report.SchemaVersion, report.SupportedSchemaVersion = version, schemaVersion
	if version != schemaVersion {
		return report, newSchemaMismatch(version, schemaIdentity{id: id})
	}
	report.Policy, err = readPolicy(ctx, db)
	if err != nil {
		return report, err
	}
	// One read transaction keeps counters and recent metadata consistent while
	// other owners append. No payload/program column is selected.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return report, storageError()
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, "SELECT accounted_bytes,index_bytes,admission_blocked FROM run_meta WHERE singleton=1").
		Scan(&report.AccountedBytes, &report.IndexBytes, &report.AdmissionBlocked); err != nil {
		return report, storageError()
	}
	now := time.Now().UnixMilli()
	if err := tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(terminal=0),0) FROM runs
		WHERE evicting=0 AND (terminal=0 OR expires_at>?)`, now).Scan(&report.RetainedRuns, &report.UnfinishedRuns); err != nil {
		return report, storageError()
	}
	report.Recent, err = diagnosticRuns(ctx, tx, now)
	if err != nil {
		return report, err
	}
	if err := tx.Commit(); err != nil {
		return report, storageError()
	}
	// File sizes are a separate, approximate physical snapshot, never quota.
	for _, file := range []struct {
		suffix string
		size   *int64
	}{{"", &report.DBBytes}, {"-wal", &report.WALBytes}, {"-shm", &report.SidecarBytes}, {"-journal", &report.SidecarBytes}} {
		info, err := os.Lstat(path + file.suffix)
		if errors.Is(err, os.ErrNotExist) && file.suffix != "" {
			continue
		}
		if err != nil || !safeRegular(info) {
			return report, storageError()
		}
		*file.size += info.Size()
	}
	return report, nil
}

func diagnosticRuns(ctx context.Context, tx *sql.Tx, now int64) ([]RunDiagnostic, error) {
	// Bound each decoded metadata row even if the authenticated DB is damaged.
	rows, err := tx.QueryContext(ctx, `SELECT id,CASE WHEN length(result)<=65536 THEN result ELSE NULL END,expires_at
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
		if err := rows.Scan(&id, &data, &expiry); err != nil {
			return nil, storageError()
		}
		var result Result
		if uuid.Validate(id) != nil || len(data) == 0 || json.Unmarshal(data, &result) != nil {
			return nil, storageError()
		}
		entry := RunDiagnostic{
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
