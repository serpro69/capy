package server

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/serpro69/capy/internal/sqliteutil"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func inspectionResult(t *testing.T, s *Server, args map[string]any) runstore.Inspection {
	t.Helper()
	r := callRuns(t, s, args)
	require.False(t, r.IsError, resultText(r))
	encoded, err := json.Marshal(r)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), s.config.Runs.MaxResponseBytes)
	view, ok := r.StructuredContent.(runstore.Inspection)
	require.True(t, ok)
	require.Equal(t, "inspection", view.Kind)
	require.Equal(t, runstore.EnvelopeVersion, view.Version)
	require.Nil(t, s.runs, "inspection must not attach the execution service")
	return view
}

func inspectionError(t *testing.T, s *Server, args map[string]any, code string) runstore.Error {
	t.Helper()
	r := callRuns(t, s, args)
	require.True(t, r.IsError, resultText(r))
	encoded, err := json.Marshal(r)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), s.config.Runs.MaxResponseBytes)
	problem := r.StructuredContent.(map[string]any)["error"].(runstore.Error)
	require.Equal(t, code, problem.Code)
	require.Nil(t, s.runs)
	return problem
}

func inspectionStore(t *testing.T, s *Server) (*runstore.Store, *runstore.Owner, *sql.DB) {
	t.Helper()
	path, err := config.ResolveRunDBPath(s.projectDir)
	require.NoError(t, err)
	st, err := runstore.Open(t.Context(), path, s.knowledgeKey, true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	owner, err := st.ClaimOwner(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Lease.Close()) })
	db, err := sql.Open("sqlite3", sqliteutil.EncryptedDSN(path, s.knowledgeKey)+"&mode=rw")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return st, owner, db
}

// With one retained connection, data_version observes every commit by another
// connection, including writes that would leave the same logical values behind.
func inspectionDataVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var version int
	require.NoError(t, db.QueryRow("PRAGMA data_version").Scan(&version))
	return version
}

func TestRunsInspectMissingAndArgumentValidation(t *testing.T) {
	s := newTestServer(t, nil)
	s.config.Runs.MaxResponseBytes = 2048
	view := inspectionResult(t, s, map[string]any{"action": "inspect"})
	require.False(t, view.Exists)
	require.Equal(t, "not_initialized", view.Admission)
	require.Contains(t, view.NextStep, `capy_runs(action="inspect")`)
	require.NotContains(t, view.NextStep, "--project-dir")
	require.Nil(t, s.store)
	require.Nil(t, s.vault)
	id := uuid.NewString()
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"run type", map[string]any{"run_id": 7}},
		{"receipt type", map[string]any{"receipt_id": nil}},
		{"plan type", map[string]any{"plan": true}},
		{"cursor type", map[string]any{"cursor": 1}},
		{"invalid run", map[string]any{"run_id": "not-a-uuid"}},
		{"invalid receipt", map[string]any{"receipt_id": "not-a-uuid"}},
		{"invalid plan", map[string]any{"plan": "invalid"}},
		{"empty run", map[string]any{"run_id": ""}},
		{"empty receipt", map[string]any{"receipt_id": ""}},
		{"empty plan", map[string]any{"plan": ""}},
		{"two selectors", map[string]any{"run_id": id, "receipt_id": id}},
		{"oversized cursor", map[string]any{"cursor": strings.Repeat("x", 1025)}},
		{"oversized plan", map[string]any{"plan": strings.Repeat("x", 1025)}},
		{"zero limit", map[string]any{"limit": 0}},
		{"large limit", map[string]any{"limit": 65}},
		{"fractional limit", map[string]any{"limit": 1.5}},
		{"null limit", map[string]any{"limit": nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.args["action"] = "inspect"
			inspectionError(t, s, tc.args, "invalid_argument")
		})
	}
	for _, name := range []string{"apply", "confirm_stopped", "cleanup_basis", "reason", "recovery_id", "batch_id", "raw", "encoding", "stream", "states", "queries", "query_scope", "max_bytes", "start_line", "project_dir"} {
		t.Run(name, func(t *testing.T) {
			inspectionError(t, s, map[string]any{"action": "inspect", name: false}, "invalid_argument")
		})
	}
	inspectionError(t, s, map[string]any{"action": "inspect", "receipt_id": id}, "receipt_unavailable")
	require.NoDirExists(t, filepath.Join(s.projectDir, ".capy", "runs"))
	descriptor := toolRuns()
	action := descriptor.InputSchema.Properties["action"].(map[string]any)
	require.Contains(t, action["enum"], "inspect")
	require.Contains(t, descriptor.InputSchema.Properties, "receipt_id")
	require.Contains(t, descriptor.InputSchema.Properties, "plan")
	for _, name := range []string{"apply", "confirm_stopped", "cleanup_basis", "recovery_id"} {
		require.NotContains(t, descriptor.InputSchema.Properties, name)
	}
}

func TestRunsInspectHealthyPendingDoesNotReconcileOrCountCompletion(t *testing.T) {
	s := newTestServer(t, nil)
	st, owner, db := inspectionStore(t, s)
	row, err := st.Admit(t.Context(), owner, executor.CleanupLinuxDescendants, runstore.Program{})
	require.NoError(t, err)
	require.NoError(t, st.Pending(t.Context(), row.ID, owner.ID, row.Cleanup.Scope))
	before := inspectionDataVersion(t, db)
	stats := s.stats.Snapshot()
	// The server must use its captured credential, not re-read the environment.
	t.Setenv("CAPY_DB_KEY", "synthetic-unrelated-key")
	for _, released := range []bool{false, true} {
		if released {
			require.NoError(t, owner.Lease.Close())
		}
		view := inspectionResult(t, s, map[string]any{"action": "inspect", "run_id": row.ID})
		require.Equal(t, "unverified", view.Admission)
		require.EqualValues(t, 1, view.Counts.UnattributedPending)
		require.Zero(t, view.Counts.RecordedIncidents)
		require.Zero(t, view.Counts.InferredIncidents)
		require.Len(t, view.Subjects, 1)
		require.False(t, view.Subjects[0].BlocksAdmission)
		require.Equal(t, "authorized", view.Subjects[0].Phase)
	}
	require.Equal(t, before, inspectionDataVersion(t, db))
	after := s.stats.Snapshot()
	require.Equal(t, stats.RunsCompleted, after.RunsCompleted)
	require.Equal(t, stats.RunsIncomplete, after.RunsIncomplete)
	require.Equal(t, stats.RunObservedBytes, after.RunObservedBytes)
	require.Equal(t, stats.RunCapturedBytes, after.RunCapturedBytes)
	require.Greater(t, after.BytesReturned["capy_runs"], stats.BytesReturned["capy_runs"])
	s.knowledgeKey = "synthetic-wrong-key"
	inspectionError(t, s, map[string]any{"action": "inspect"}, "wrong_key")
	require.Equal(t, before, inspectionDataVersion(t, db))
}

func TestRunsInspectPaginationStalenessAndPrivacy(t *testing.T) {
	s := newTestServer(t, nil)
	s.config.Runs.MaxResponseBytes = 2048
	st, owner, db := inspectionStore(t, s)
	rows := []runstore.Admitted{}
	for range 4 {
		row, err := st.Admit(t.Context(), owner, executor.CleanupLinuxDescendants, runstore.Program{Request: executor.ExecRequest{Code: "PRIVATE_PROGRAM"}})
		require.NoError(t, err)
		require.NoError(t, st.Pending(t.Context(), row.ID, owner.ID, row.Cleanup.Scope))
		rows = append(rows, row)
	}
	record := func(row runstore.Admitted) {
		t.Helper()
		require.NoError(t, st.QuarantineRun(t.Context(), runstore.RuntimeIncident{RunID: row.ID, Generation: owner.ID, Slot: owner.Slot, Cause: runstore.IncidentStartupUncertain, Stage: runstore.StageStartup}))
	}
	for _, row := range rows[:3] {
		record(row)
	}
	before := inspectionDataVersion(t, db)
	first := inspectionResult(t, s, map[string]any{"action": "inspect", "limit": 64})
	require.Equal(t, "recorded_blocked", first.Admission)
	require.EqualValues(t, 3, first.Counts.RecordedIncidents)
	require.True(t, first.Omitted)
	require.Equal(t, "partial", first.Details)
	require.NotEmpty(t, first.NextCursor)
	seen := make(map[string]bool)
	page := first
	for range 10 {
		for _, subject := range page.Subjects {
			key := subject.Kind + ":" + subject.IncidentID + ":" + subject.RunID
			require.False(t, seen[key], "budgeted continuation duplicated a subject")
			seen[key] = true
		}
		encoded, err := json.Marshal(page)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), "PRIVATE_PROGRAM")
		if page.NextCursor == "" {
			break
		}
		page = inspectionResult(t, s, map[string]any{"action": "inspect", "limit": 64, "cursor": page.NextCursor})
	}
	require.Empty(t, page.NextCursor, "minimum-budget pages must make progress")
	require.Len(t, seen, int(first.Counts.Subjects))
	require.Equal(t, before, inspectionDataVersion(t, db))
	record(rows[3])
	stale := inspectionError(t, s, map[string]any{"action": "inspect", "cursor": first.NextCursor}, "inspection_stale")
	require.Contains(t, stale.Message, `capy_runs(action="inspect") with the same selector and no cursor`)
	require.NotContains(t, stale.Message, "capy runs inspect")
	_, err := db.Exec("UPDATE runs SET result='PRIVATE_BROKEN_RESULT' WHERE id=?", rows[0].ID)
	require.NoError(t, err)
	broken := inspectionResult(t, s, map[string]any{"action": "inspect", "run_id": rows[0].ID})
	require.True(t, broken.Subjects[0].EvidenceInconsistent)
	require.Contains(t, broken.Subjects[0].MissingEvidence, "valid_run_metadata")
	encoded, err := json.Marshal(broken)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "PRIVATE_BROKEN_RESULT")
}

func TestRunsInspectReceiptSelectorsAndPruning(t *testing.T) {
	s := newTestServer(t, nil)
	s.config.Runs.MaxResponseBytes = 2048
	st, _, db := inspectionStore(t, s)
	receipt, incident, hash := uuid.NewString(), uuid.NewString(), strings.Repeat("a", 64)
	_, err := db.Exec(`INSERT INTO run_recovery_receipts(id,plan_hash,store_id,version,applied_at,method,basis,uid,explanation,incident_count,run_count,owner_count,store_count,charged_bytes,audit_expires_at)
 VALUES(?,?,?,1,1,'operator','groups_stopped',0,?,1,0,0,1,0,1)`, receipt, hash, st.ID(), strings.Repeat("reviewed ", 100))
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO run_incidents(id,version,subject_kind,subject_key,reason,attribution,stage,last_phase,revision,receipt_id,charged_bytes)
 VALUES(?,1,'store','store','legacy_unattributed','unattributed','legacy_inspection','unknown',1,?,0)`, incident, receipt)
	require.NoError(t, err)
	planBytes, err := json.Marshal(map[string]any{"v": 1, "store": st.ID(), "hash": hash})
	require.NoError(t, err)
	plan := base64.RawURLEncoding.EncodeToString(planBytes)
	before := inspectionDataVersion(t, db)
	for _, args := range []map[string]any{{"action": "inspect", "receipt_id": receipt}, {"action": "inspect", "plan": plan}} {
		view := inspectionResult(t, s, args)
		require.NotNil(t, view.Receipt)
		require.Equal(t, receipt, view.Receipt.ID)
		require.EqualValues(t, 1, view.Receipt.AuditExpiresAt, "retained audit is visible after its deadline")
		require.Len(t, view.Subjects, 1)
		require.Equal(t, incident, view.Subjects[0].IncidentID)
		require.False(t, view.Subjects[0].BlocksAdmission)
	}
	inspectionError(t, s, map[string]any{"action": "inspect", "receipt_id": incident}, "receipt_unavailable")
	inspectionError(t, s, map[string]any{"action": "inspect", "receipt_id": receipt, "plan": plan}, "invalid_argument")
	require.Equal(t, before, inspectionDataVersion(t, db))
	_, err = db.Exec("UPDATE run_recovery_receipts SET pruning=1 WHERE id=?", receipt)
	require.NoError(t, err)
	view := inspectionResult(t, s, map[string]any{"action": "inspect", "receipt_id": receipt})
	require.True(t, view.Receipt.Pruning)
	require.Equal(t, "partial", view.Details)
}

func TestRunsInspectClosedStoreCreatesNoSidecars(t *testing.T) {
	s := newTestServer(t, nil)
	path, err := config.ResolveRunDBPath(s.projectDir)
	require.NoError(t, err)
	st, err := runstore.Open(t.Context(), path, s.knowledgeKey, true)
	require.NoError(t, err)
	require.NoError(t, st.Close())
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	inspectionResult(t, s, map[string]any{"action": "inspect"})
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		require.NoFileExists(t, path+suffix)
	}
}

func TestRunsInspectReceiptRunIncidentsMinimumBudget(t *testing.T) {
	s := newTestServer(t, nil)
	s.config.Runs.MaxResponseBytes = 2048
	st, owner, db := inspectionStore(t, s)
	runs := []runstore.Admitted{}
	for range 2 {
		row, err := st.Admit(t.Context(), owner, executor.CleanupLinuxDescendants, runstore.Program{})
		require.NoError(t, err)
		require.NoError(t, st.Pending(t.Context(), row.ID, owner.ID, row.Cleanup.Scope))
		require.NoError(t, st.Started(t.Context(), row.ID, "run"))
		runs = append(runs, row)
	}
	for _, row := range runs {
		require.NoError(t, st.QuarantineRun(t.Context(), runstore.RuntimeIncident{RunID: row.ID, Generation: owner.ID, Slot: owner.Slot, Cause: runstore.IncidentFinalizationUnpersisted, Stage: runstore.StageFinalization}))
	}
	receipt := uuid.NewString()
	_, err := db.Exec(`INSERT INTO run_recovery_receipts(id,plan_hash,store_id,version,applied_at,method,basis,uid,explanation,incident_count,run_count,owner_count,store_count,charged_bytes,audit_expires_at)
 VALUES(?,?,?,1,?,'operator','groups_stopped',501,?,2,2,0,0,0,?)`, receipt, strings.Repeat("b", 64), st.ID(), time.Now().UnixNano(), strings.Repeat("reviewed ", 100), time.Now().Add(24*time.Hour).UnixNano())
	require.NoError(t, err)
	_, err = db.Exec("UPDATE run_incidents SET receipt_id=?", receipt)
	require.NoError(t, err)
	before := inspectionDataVersion(t, db)
	seen := make(map[string]bool)
	cursor := ""
	complete := false
	for page := 0; page < 3; page++ {
		view := inspectionResult(t, s, map[string]any{"action": "inspect", "receipt_id": receipt, "cursor": cursor, "limit": 64})
		require.Equal(t, receipt, view.Receipt.ID)
		require.EqualValues(t, 2, view.Counts.Subjects)
		require.NotEmpty(t, view.Subjects)
		if page == 0 {
			require.NotEmpty(t, view.NextCursor)
		}
		for _, subject := range view.Subjects {
			require.False(t, seen[subject.RunID])
			seen[subject.RunID] = true
			require.Equal(t, owner.ID, subject.Generation)
			require.Equal(t, owner.Slot, *subject.Slot)
			require.NotNil(t, subject.DetectedAt)
			require.Equal(t, "finalization", subject.DetectionStage)
			require.Equal(t, "run", subject.Phase)
			require.Equal(t, executor.CleanupPending, subject.Cleanup.State)
			require.Contains(t, subject.MissingEvidence, "accepted_cleanup")
			require.Contains(t, subject.MissingEvidence, "terminal_outcome")
		}
		if view.NextCursor == "" {
			complete = true
			break
		}
		require.NotEqual(t, cursor, view.NextCursor)
		require.True(t, view.Omitted)
		cursor = view.NextCursor
	}
	require.Len(t, seen, 2)
	require.True(t, complete)
	require.Equal(t, before, inspectionDataVersion(t, db))
}

func TestRunsInspectCredentialAndLifecycleFailures(t *testing.T) {
	for _, mode := range []string{"empty credential", "busy lifecycle"} {
		t.Run(mode, func(t *testing.T) {
			s := newTestServer(t, nil)
			s.config.Runs.MaxResponseBytes = 2048
			path, err := config.ResolveRunDBPath(s.projectDir)
			require.NoError(t, err)
			st, err := runstore.Open(t.Context(), path, s.knowledgeKey, true)
			require.NoError(t, err)
			require.NoError(t, st.Close())
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			code := "credential_unavailable"
			if mode == "empty credential" {
				s.knowledgeKey = ""
			} else {
				lease, err := os.OpenFile(filepath.Join(filepath.Dir(path), "lifecycle.lock"), os.O_RDWR, 0)
				require.NoError(t, err)
				require.NoError(t, unix.Flock(int(lease.Fd()), unix.LOCK_EX|unix.LOCK_NB))
				t.Cleanup(func() {
					require.NoError(t, unix.Flock(int(lease.Fd()), unix.LOCK_UN))
					require.NoError(t, lease.Close())
				})
				code = "storage_busy"
			}
			problem := inspectionError(t, s, map[string]any{"action": "inspect"}, code)
			if mode == "busy lifecycle" {
				require.Contains(t, problem.Message, `capy_runs(action="inspect")`)
				require.NotContains(t, problem.Message, "capy runs inspect")
			}
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestRunsInspectNestedObservationCursorMinimumBudget(t *testing.T) {
	s := newTestServer(t, nil)
	s.config.Runs.MaxResponseBytes = 2048
	st, owner, db := inspectionStore(t, s)
	for range 2 {
		row, err := st.Admit(t.Context(), owner, executor.CleanupDarwinObservation, runstore.Program{})
		require.NoError(t, err)
		require.NoError(t, st.Pending(t.Context(), row.ID, owner.ID, row.Cleanup.Scope))
	}
	// A valid but inconsistent generation produces the largest supported pending
	// observation fields. Unsupported cleanup scopes fail validation, never leak.
	_, err := db.Exec("UPDATE run_owners SET generation=?,cleanup_unverified=1; UPDATE run_meta SET admission_blocked=1; UPDATE runs SET result=json_set(result,'$.state','interrupted')", uuid.NewString())
	require.NoError(t, err)
	before := inspectionDataVersion(t, db)
	seen := make(map[string]bool)
	cursor := ""
	complete, nestedRunPage := false, false
	for range 8 {
		view := inspectionResult(t, s, map[string]any{"action": "inspect", "cursor": cursor, "limit": 64})
		for _, subject := range view.Subjects {
			if subject.Kind != "run" {
				continue
			}
			require.False(t, seen[subject.RunID])
			seen[subject.RunID] = true
			require.True(t, subject.EvidenceInconsistent)
			require.Equal(t, "interrupted", subject.State)
			require.Equal(t, "authorized", subject.Phase)
			require.Equal(t, executor.CleanupDarwinObservation, subject.Cleanup.Scope)
			require.Len(t, subject.MissingEvidence, 2)
			nestedRunPage = nestedRunPage || view.NextCursor != ""
		}
		if view.NextCursor == "" {
			complete = true
			break
		}
		require.NotEqual(t, cursor, view.NextCursor)
		cursor = view.NextCursor
	}
	require.True(t, complete)
	require.True(t, nestedRunPage)
	require.Len(t, seen, 2)
	require.Equal(t, before, inspectionDataVersion(t, db))
}
