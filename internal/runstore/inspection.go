package runstore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/serpro69/capy/internal/sanitize"
	"github.com/serpro69/capy/internal/sqliteutil"
	"golang.org/x/sys/unix"
)

// InspectionOptions selects diagnostic metadata, never execution or recovery.
type InspectionOptions struct {
	RunID, ReceiptID, Plan, Cursor string
	Limit                          int
}

// Inspection separates recorded quarantine from current unfinished observations.
// Legacy stores have no recovery epoch or durable incident/receipt membership.
type Inspection struct {
	Version                int                 `json:"version"`
	Kind                   string              `json:"kind"`
	Exists                 bool                `json:"exists"`
	Owner                  string              `json:"owner,omitempty"`
	StoreID                string              `json:"store_id,omitempty"`
	SchemaVersion          int                 `json:"schema_version,omitempty"`
	SupportedSchemaVersion int                 `json:"supported_schema_version"`
	RecoveryRevision       *int64              `json:"recovery_revision,omitempty"`
	ObservedAt             time.Time           `json:"observed_at"`
	Admission              string              `json:"admission"`
	Counts                 InspectionCounts    `json:"counts"`
	Subjects               []InspectionSubject `json:"subjects,omitempty"`
	Details                string              `json:"details"`
	Omitted                bool                `json:"omitted"`
	NextCursor             string              `json:"next_cursor,omitempty"`
	Consistency            string              `json:"consistency"`
	Action                 string              `json:"action"`
	InspectCommand         string              `json:"inspect_command,omitempty"`
	NextStep               string              `json:"next_step"`
	// Positions allow response budgeting to shorten an already captured page
	// without querying again or skipping undisplayed subjects.
	positions []string
}

type InspectionCounts struct {
	QuarantineMarkers   int64 `json:"quarantine_markers"`
	InferredCandidates  int64 `json:"inferred_candidates"`
	UnattributedPending int64 `json:"unattributed_pending"`
	Subjects            int64 `json:"subjects"`
}

func (o InspectionOptions) Validate() error {
	if o.Limit < 1 || o.Limit > 64 || len(o.Cursor) > 1024 {
		return failure("invalid_argument", "inspection requires limit 1–64 and a cursor of at most 1024 bytes")
	}
	selectors := 0
	for _, id := range []string{o.RunID, o.ReceiptID} {
		if id != "" {
			selectors++
			if uuid.Validate(id) != nil {
				return failure("invalid_argument", "run-id and receipt-id must be UUIDs")
			}
		}
	}
	if o.Plan != "" {
		selectors++
		if _, err := decodeInspectionPlan(o.Plan); err != nil {
			return err
		}
	}
	if selectors > 1 {
		return failure("invalid_argument", "choose only one of run-id, receipt-id or plan")
	}
	return nil
}

// InspectRuns is the public metadata page. Inspect adds the bounded recent sample
// and usage for doctor/stats through the same opener and read transaction.
func InspectRuns(ctx context.Context, path, key string, options InspectionOptions) (Inspection, error) {
	report, err := inspectSnapshot(ctx, path, key, options, false)
	return report.Inspection, err
}

func inspectSnapshot(ctx context.Context, path, key string, options InspectionOptions, diagnostics bool) (report Diagnostics, retErr error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	view := &report.Inspection
	*view = Inspection{Version: EnvelopeVersion, Kind: "inspection", SupportedSchemaVersion: schemaVersion,
		ObservedAt: time.Now().UTC(), Admission: "unverified", Details: "unavailable", Action: "inspect",
		Consistency: "legacy_markers_and_cutoff; run observations are current to each page; no audit epoch or liveness proof"}
	if err := options.Validate(); err != nil {
		return report, err
	}
	if !filepath.IsAbs(path) || filepath.Base(path) != "runs.db" ||
		filepath.Base(filepath.Dir(path)) != "runs" || filepath.Base(filepath.Dir(filepath.Dir(path))) != ".capy" {
		return report, storageError()
	}
	owner := filepath.Dir(filepath.Dir(filepath.Dir(path)))
	if physical, err := filepath.EvalSymlinks(owner); err != nil || physical != owner {
		return report, storageError()
	}
	view.Owner, view.InspectCommand = inspectionOwner(owner)
	view.NextStep = "Select this physical owner with --project-dir, then run capy runs inspect."
	exists, err := inspectStore(path)
	report.Exists, view.Exists = exists, exists
	if err != nil {
		return report, err
	}
	if !exists {
		if options.Cursor != "" {
			return report, staleInspection()
		}
		if options.ReceiptID != "" || options.Plan != "" {
			return report, receiptUnavailable()
		}
		if options.RunID != "" {
			return report, failure("run_unavailable", "no retained run matches this ID")
		}
		view.Admission, view.Action, view.Details = "not_initialized", "not_initialized", "complete"
		return report, nil
	}
	if key == "" {
		return report, failure("credential_unavailable", "restore the selected run credential and reload; no fallback key was tried")
	}
	life, err := openRegular(filepath.Join(filepath.Dir(path), "lifecycle.lock"), false)
	if err != nil {
		return report, storageError()
	}
	defer func() {
		if life.Close() != nil {
			retErr = storageError()
		}
	}()
	// Prefer shared access. A closed WAL database lacking sidecars needs an
	// immutable connection, and that is safe only while all cooperating users
	// are excluded by a fresh exclusive lease (never convert a shared flock).
	immutable := false
	walInfo, walErr := os.Lstat(path + "-wal")
	if errors.Is(walErr, os.ErrNotExist) {
		if err := unix.Flock(int(life.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			return report, failure("storage_busy", "inspection without WAL sidecars needs exclusive lifecycle access; stop attached store users and retry capy runs inspect")
		}
		if _, err := os.Lstat(path + "-wal"); !errors.Is(err, os.ErrNotExist) {
			return report, failure("storage_busy", "WAL state changed during inspection attachment; retry capy runs inspect")
		}
		if info, err := os.Lstat(path + "-journal"); err == nil && info.Size() != 0 || err != nil && !errors.Is(err, os.ErrNotExist) {
			return report, failure("storage_unavailable", "inspection cannot ignore a retained rollback journal; preserve history and use a compatible database reader")
		}
		immutable = true
	} else if walErr != nil || !safeRegular(walInfo) {
		return report, storageError()
	} else if err := unix.Flock(int(life.Fd()), unix.LOCK_SH|unix.LOCK_NB); err != nil {
		return report, failure("storage_busy", "run lifecycle is busy; retry inspection after the exclusive operation finishes")
	}
	if exists, err := inspectStore(path); err != nil || !exists {
		return report, storageError()
	}
	if !immutable {
		if _, err := os.Lstat(path + "-wal"); errors.Is(err, os.ErrNotExist) {
			return report, failure("storage_busy", "WAL closed during inspection attachment; retry capy runs inspect")
		} else if err != nil {
			return report, storageError()
		}
	}
	sqliteOptions, err := inspectionSQLiteOptions()
	if err != nil {
		return report, err
	}
	if immutable {
		sqliteOptions += "&immutable=1"
	}
	db, err := sqliteutil.OpenWithCanary(ctx, sqliteutil.EncryptedDSN(path, key)+sqliteOptions, path, "selected run credential")
	if err != nil {
		if !immutable {
			if _, walErr := os.Lstat(path + "-wal"); errors.Is(walErr, os.ErrNotExist) {
				return report, failure("storage_busy", "WAL closed during inspection attachment; retry capy runs inspect")
			}
		}
		if sqliteutil.IsWrongPassphrase(err) || sqliteutil.IsSQLiteCorruption(err) {
			return report, failure("wrong_key", "run store cannot be authenticated; restore the matching selected credential and reload; history was not discarded")
		}
		return report, storageError()
	}
	defer func() {
		if db.Close() != nil {
			retErr = storageError()
		}
	}()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.QueryRowContext(ctx, "SELECT version,store_id FROM run_meta WHERE singleton=1").Scan(&view.SchemaVersion, &view.StoreID); err != nil || uuid.Validate(view.StoreID) != nil {
		return report, storageError()
	}
	report.SchemaVersion, report.SupportedSchemaVersion = view.SchemaVersion, schemaVersion
	if view.SchemaVersion != 8 && view.SchemaVersion != 9 && view.SchemaVersion != 10 {
		return report, failure("schema_mismatch", "unsupported run schema; use a compatible capy binary; inspection does not migrate")
	}
	// Policy cannot change while the shared lifecycle lease is held. All mutable
	// diagnostic membership and values below are read in one transaction.
	report.Policy, err = readPolicyVersion(ctx, db, view.SchemaVersion)
	if err != nil {
		return report, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return report, storageError()
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, "SELECT accounted_bytes,index_bytes,admission_blocked FROM run_meta WHERE singleton=1").
		Scan(&report.AccountedBytes, &report.IndexBytes, &report.AdmissionBlocked); err != nil {
		return report, storageError()
	}
	if err := inspectLegacySubjects(ctx, tx, view, options, report.AdmissionBlocked); err != nil {
		return report, err
	}
	if diagnostics {
		now := view.ObservedAt.UnixMilli()
		if err := tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(terminal=0),0) FROM runs
			WHERE evicting=0 AND (terminal=0 OR expires_at>?)`, now).Scan(&report.RetainedRuns, &report.UnfinishedRuns); err != nil {
			return report, storageError()
		}
		report.Recent, err = diagnosticRuns(ctx, tx, now, view.SchemaVersion)
		if err != nil {
			return report, err
		}
	}
	if err := tx.Commit(); err != nil {
		return report, storageError()
	}
	if diagnostics {
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
	}
	return report, nil
}

func inspectionOwner(owner string) (string, string) {
	safe := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return '�'
		}
		return r
	}, sanitize.StripSecrets(owner))
	if len(safe) > 256 || safe != owner {
		return string([]rune(safe)[:min(len([]rune(safe)), 128)]), ""
	}
	command := "capy --project-dir '" + strings.ReplaceAll(owner, "'", "'\"'\"'") + "' runs inspect"
	if len(command) > 400 {
		return safe, ""
	}
	return safe, command
}

// BoundInspection only formats a captured result. It retains a continuation at
// the last displayed entry, including when a response budget cuts a page short.
func BoundInspection(view Inspection, entries int) Inspection {
	if entries <= 0 {
		// Even the smallest envelope must make pagination progress. Path display
		// and explanatory prose can shrink; the next subject cannot disappear.
		view.Owner = ""
		view.InspectCommand = ""
		view.Consistency = "legacy markers; run observations per page; no liveness or admission guarantee"
		view.NextStep = "For the same selected owner, run capy runs inspect."
		if view.Action == "upgrade_required" {
			view.NextStep += " Stop store users, then run capy runs upgrade from an ordinary terminal; quarantine is preserved."
		}
	}
	entries = max(1, entries)
	if entries < len(view.Subjects) {
		view.Subjects = view.Subjects[:entries]
		view.NextCursor = view.positions[entries]
		view.Omitted, view.Details = true, "partial"
	}
	return view
}
