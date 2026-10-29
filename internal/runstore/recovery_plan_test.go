package runstore

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/sqliteutil"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func planFixture(t *testing.T) (*Store, *Owner, func()) {
	t.Helper()
	root := t.TempDir()
	path, err := config.ResolveRunDBPath(root)
	require.NoError(t, err)
	require.NoError(t, initialize(t.Context(), path, testKey, root, defaultPolicyConfig()))
	s, err := openAttached(t.Context(), path, testKey, root, unix.LOCK_SH)
	require.NoError(t, err)
	o, err := s.ClaimOwner(t.Context())
	require.NoError(t, err)
	closed := false
	close := func() {
		if !closed {
			closed = true
			require.NoError(t, o.Lease.Close())
			require.NoError(t, s.closeHandles())
		}
	}
	t.Cleanup(close)
	return s, o, close
}

func planOptions() RecoveryPlanOptions {
	return RecoveryPlanOptions{Limit: 64, Timeout: DefaultRecoveryTimeout}
}

func planPending(t *testing.T, s *Store, o *Owner) Admitted {
	t.Helper()
	r, err := s.Admit(t.Context(), o, executor.CleanupLinuxDescendants, Program{Request: executor.ExecRequest{Code: "PRIVATE_PROGRAM"}})
	require.NoError(t, err)
	require.NoError(t, s.Pending(t.Context(), r.ID, o.ID, r.Cleanup.Scope))
	require.NoError(t, s.Started(t.Context(), r.ID, "run"))
	require.NoError(t, s.Append(t.Context(), r.ID, executor.OutputEvent{Sequence: 1, Phase: "run", Stream: "stdout", Data: []byte("prefix\npassword=unfinished")}))
	return r
}

func mutatePlanStore(t *testing.T, path, query string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite3", sqliteutil.EncryptedDSN(path, testKey)+"&mode=rw")
	require.NoError(t, err)
	_, err = db.Exec(query, args...)
	require.NoError(t, err)
	require.NoError(t, db.Close())
}

func TestRecoveryPlanStableCompleteAndNonmutating(t *testing.T) {
	s, o, close := planFixture(t)
	runs := []Admitted{}
	for range 3 {
		runs = append(runs, planPending(t, s, o))
	}
	require.NoError(t, s.QuarantineRun(t.Context(), RuntimeIncident{RunID: runs[0].ID, Generation: o.ID, Slot: o.Slot, Cause: IncidentSupervisorLost, Stage: StageHelperWait}))
	close()
	files := inspectionFiles(t, filepath.Dir(s.path))
	before, err := os.ReadFile(s.path)
	require.NoError(t, err)
	options := planOptions()
	options.Limit = 1
	first, err := PlanRecovery(t.Context(), s.path, testKey, options)
	require.NoError(t, err)
	require.Equal(t, "ready", first.Status)
	require.True(t, first.Complete)
	require.NotEmpty(t, first.Token)
	require.EqualValues(t, 3, first.Counts.Dispositions)
	require.EqualValues(t, 2, first.Counts.NewIncidents)
	require.True(t, first.Capacity.Sufficient)
	require.NotEmpty(t, first.NextCursor)
	require.Contains(t, first.NextCommand, "recover --cursor")
	seen := map[string]bool{}
	page := first
	for {
		for _, subject := range page.Subjects {
			require.False(t, seen[subject.Key])
			seen[subject.Key] = true
			require.Equal(t, "dispose_run", subject.Action)
		}
		if page.NextCursor == "" {
			break
		}
		options.Cursor = page.NextCursor
		options.Timeout = 15 * time.Minute
		page, err = PlanRecovery(t.Context(), s.path, testKey, options)
		require.NoError(t, err)
		require.Equal(t, first.Token, page.Token)
		if page.NextCursor != "" {
			require.Contains(t, page.NextCommand, "--timeout 15m0s")
		}
	}
	require.Len(t, seen, 3)
	all, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
	require.NoError(t, err)
	require.Equal(t, first.Token, all.Token)
	for _, subject := range all.Subjects {
		if subject.Evidence.RunID != runs[0].ID {
			require.Empty(t, subject.Evidence.IncidentID)
			require.Nil(t, subject.Evidence.DetectedAt)
		}
	}
	encoded, err := json.Marshal(all)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "PRIVATE_PROGRAM")
	require.NotContains(t, string(encoded), "unfinished")
	after, err := os.ReadFile(s.path)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, files, inspectionFiles(t, filepath.Dir(s.path)))
}

func TestRecoveryPlanEmptySafeAndTerminalIncident(t *testing.T) {
	for _, mode := range []string{"empty", "safe", "terminal incident", "legacy marker"} {
		t.Run(mode, func(t *testing.T) {
			s, o, close := planFixture(t)
			var r Admitted
			if mode == "safe" {
				var err error
				r, err = s.Admit(t.Context(), o, executor.CleanupLinuxDescendants, Program{})
				require.NoError(t, err)
			}
			if mode == "terminal incident" {
				r = planPending(t, s, o)
				require.NoError(t, s.QuarantineRun(t.Context(), RuntimeIncident{RunID: r.ID, Generation: o.ID, Slot: o.Slot, Cause: IncidentFinalizationUnpersisted, Stage: StageFinalization}))
				require.NoError(t, s.Finish(t.Context(), r.ID, Completion{Cleanup: executor.CleanupEvidence{Scope: r.Cleanup.Scope, State: executor.CleanupRetired}, Started: true}))
			}
			if mode == "legacy marker" {
				require.NoError(t, s.Quarantine(t.Context()))
			}
			close()
			view, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
			require.NoError(t, err)
			switch mode {
			case "empty", "safe":
				require.Equal(t, "no_recovery_needed", view.Status)
				require.Empty(t, view.Token)
				require.Nil(t, view.Capacity)
				if mode == "safe" {
					require.EqualValues(t, 1, view.Counts.SafeReconciliation)
					require.Equal(t, "reconcile", view.Subjects[0].Action)
				}
			case "terminal incident":
				require.Zero(t, view.Counts.Dispositions)
				require.EqualValues(t, 1, view.Counts.IncidentOnly)
				require.Nil(t, view.Subjects[0].EstimatedExpiry)
			case "legacy marker":
				require.EqualValues(t, 1, view.Counts.StoreConcerns)
				require.Empty(t, view.Subjects[0].Evidence.RunID)
				require.Empty(t, view.Subjects[0].Evidence.IncidentID)
			}
		})
	}
}

func TestRecoveryPlanRefusesLiveLegacyMissingAndWrongKey(t *testing.T) {
	s, _, close := planFixture(t)
	_, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
	requireErrorCode(t, err, "storage_busy")
	close()
	_, err = PlanRecovery(t.Context(), s.path, "wrong-key", planOptions())
	requireErrorCode(t, err, "wrong_key")
	_, err = PlanRecovery(t.Context(), s.path, "", planOptions())
	requireErrorCode(t, err, "credential_unavailable")
	missing, _ := config.ResolveRunDBPath(t.TempDir())
	_, err = PlanRecovery(t.Context(), missing, testKey, planOptions())
	requireErrorCode(t, err, "run_unavailable")
	require.NoDirExists(t, filepath.Dir(missing))
	f := newLegacyFixture(t)
	f.close(t)
	before := inspectionFiles(t, filepath.Dir(f.path))
	v, err := PlanRecovery(t.Context(), f.path, testKey, planOptions())
	requireErrorCode(t, err, "upgrade_required")
	require.Empty(t, v.Token)
	require.Equal(t, before, inspectionFiles(t, filepath.Dir(f.path)))
}

func TestRecoveryPlanChangedEvidenceAndTargetStale(t *testing.T) {
	for _, mode := range []string{"phase", "proof", "policy", "marker", "prefix", "database inode", "lock inode", "move", "copy"} {
		t.Run(mode, func(t *testing.T) {
			s, o, close := planFixture(t)
			r := planPending(t, s, o)
			planPending(t, s, o)
			close()
			opts := planOptions()
			opts.Limit = 1
			first, err := PlanRecovery(t.Context(), s.path, testKey, opts)
			require.NoError(t, err)
			path := s.path
			switch mode {
			case "phase":
				mutatePlanStore(t, path, "UPDATE runs SET last_phase='compile' WHERE id=?", r.ID)
			case "proof":
				mutatePlanStore(t, path, "UPDATE runs SET cleanup='retired' WHERE id=?", r.ID)
			case "policy":
				mutatePlanStore(t, path, "UPDATE run_policy SET revision=revision+1")
			case "marker":
				mutatePlanStore(t, path, "UPDATE run_meta SET admission_blocked=1")
			case "prefix":
				mutatePlanStore(t, path, "UPDATE run_streams SET covered=0 WHERE run_id=? AND stream='stdout'", r.ID)
			case "database inode", "lock inode":
				target := path
				if mode == "lock inode" {
					target = filepath.Join(filepath.Dir(path), "recovery.lock")
				}
				data, err := os.ReadFile(target)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(target+".replacement", data, 0600))
				require.NoError(t, os.Rename(target+".replacement", target))
			case "move":
				dest := filepath.Join(t.TempDir(), "moved")
				require.NoError(t, os.Rename(s.owner, dest))
				path = filepath.Join(dest, ".capy", "runs", "runs.db")
			case "copy":
				dest := t.TempDir()
				require.NoError(t, os.CopyFS(dest, os.DirFS(s.owner)))
				path = filepath.Join(dest, ".capy", "runs", "runs.db")
				require.NoError(t, filepath.WalkDir(filepath.Join(dest, ".capy"), func(p string, d os.DirEntry, err error) error {
					if err != nil {
						return err
					}
					mode := os.FileMode(0600)
					if d.IsDir() {
						mode = 0700
					}
					return os.Chmod(p, mode)
				}))
			}
			opts.Cursor = first.NextCursor
			view, err := PlanRecovery(t.Context(), path, testKey, opts)
			requireErrorCode(t, err, "recovery_plan_stale")
			require.Empty(t, view.Token)
		})
	}
}

func TestRecoveryPlanInconsistentAndCapacityRefuseWithoutToken(t *testing.T) {
	for _, mode := range []string{"generation", "proof", "claim", "incident", "quota"} {
		t.Run(mode, func(t *testing.T) {
			s, o, close := planFixture(t)
			r := planPending(t, s, o)
			close()
			switch mode {
			case "generation":
				mutatePlanStore(t, s.path, "UPDATE run_owners SET generation=? WHERE slot=?", "00000000-0000-0000-0000-000000000001", o.Slot)
			case "proof":
				mutatePlanStore(t, s.path, "UPDATE runs SET terminal=1 WHERE id=?", r.ID)
			case "claim":
				mutatePlanStore(t, s.path, "INSERT INTO run_work_claims(run_id,slot,generation) VALUES(?,?,?)", r.ID, o.Slot, "00000000-0000-0000-0000-000000000001")
			case "incident":
				mutatePlanStore(t, s.path, "UPDATE runs SET result=? WHERE id=?", []byte("PRIVATE_MALFORMED_RESULT"), r.ID)
			case "quota":
				mutatePlanStore(t, s.path, "UPDATE run_policy SET max_run_bytes=1,max_retained_bytes=(SELECT accounted_bytes FROM run_meta)")
			}
			before, err := os.ReadFile(s.path)
			require.NoError(t, err)
			v, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
			code := "recovery_inconsistent"
			if mode == "quota" {
				code = "recovery_capacity"
			}
			requireErrorCode(t, err, code)
			require.Empty(t, v.Token)
			encoded, _ := json.Marshal(v)
			require.NotContains(t, string(encoded), "PRIVATE_MALFORMED_RESULT")
			after, err := os.ReadFile(s.path)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestRecoveryPlanTimeoutAndLongerBudget(t *testing.T) {
	s, o, close := planFixture(t)
	planPending(t, s, o)
	close()
	opts := planOptions()
	opts.Timeout = time.Second
	entered := false
	// The same controlled scan costs >5 seconds across its four checkpoints;
	// the longer run would fail if an interactive/ordinary recovery cap leaked in.
	slow := func(ctx context.Context) error {
		entered = true
		timer := time.NewTimer(1400 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	v, err := planRecovery(t.Context(), s.path, testKey, opts, slow)
	require.True(t, entered)
	requireErrorCode(t, err, "recovery_inventory_incomplete")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Empty(t, v.Token)
	require.Contains(t, v.NextCommand, "--timeout 15m0s")
	opts.Timeout = 10 * time.Second
	v, err = planRecovery(t.Context(), s.path, testKey, opts, slow)
	require.NoError(t, err)
	require.NotEmpty(t, v.Token)
	v, err = planRecovery(t.Context(), s.path, testKey, opts, func(context.Context) error { return errors.New("PRIVATE_SQLITE_ERROR") })
	requireErrorCode(t, err, "recovery_inventory_incomplete")
	require.NotContains(t, err.Error(), "PRIVATE_SQLITE_ERROR")
	require.NotContains(t, v.Error.Message, "PRIVATE_SQLITE_ERROR")
	require.Empty(t, v.Token)
	for _, duration := range []time.Duration{0, -1} {
		opts.Timeout = duration
		_, err := PlanRecovery(t.Context(), s.path, testKey, opts)
		requireErrorCode(t, err, "invalid_argument")
	}
	_, err = decodeRecoveryCursor(strings.Repeat("x", 1025))
	require.Error(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	v, err = PlanRecovery(ctx, s.path, testKey, planOptions())
	require.True(t, errors.Is(err, context.Canceled))
	require.Empty(t, v.Token)
	require.Contains(t, v.Error.Message, "cancelled")
	require.NotContains(t, v.Error.Message, "within its effective timeout")
	require.Empty(t, v.NextCommand)
}

func TestRecoveryPlanCapacityPagesAndTimeoutCursor(t *testing.T) {
	s, o, close := planFixture(t)
	for range 3 {
		planPending(t, s, o)
	}
	close()
	mutatePlanStore(t, s.path, "UPDATE run_policy SET max_run_bytes=1,max_retained_bytes=(SELECT accounted_bytes FROM run_meta)")
	opts := planOptions()
	opts.Limit = 1
	first, err := PlanRecovery(t.Context(), s.path, testKey, opts)
	requireErrorCode(t, err, "recovery_capacity")
	require.True(t, first.Complete)
	require.Empty(t, first.Token)
	require.NotEmpty(t, first.NextCursor)
	require.Contains(t, first.NextStep, "increase runs.max_retained_bytes")
	opts.Cursor = first.NextCursor
	second, err := PlanRecovery(t.Context(), s.path, testKey, opts)
	requireErrorCode(t, err, "recovery_capacity")
	require.True(t, second.Complete)
	require.NotEqual(t, first.Subjects[0].Key, second.Subjects[0].Key)
	// A timed-out continuation keeps its normalized cursor and adds time, not
	// a hidden restart at page one. CR/LF accepted by base64 is never echoed.
	opts.Cursor = first.NextCursor[:10] + "\n" + first.NextCursor[10:]
	opts.Timeout = time.Nanosecond
	v, err := PlanRecovery(t.Context(), s.path, testKey, opts)
	requireErrorCode(t, err, "recovery_inventory_incomplete")
	require.Contains(t, v.NextCommand, "--cursor "+first.NextCursor)
	require.NotContains(t, v.NextCommand, "\n")
}

func TestRecoveryPlanUnprintableOwnerRetainsRetryArguments(t *testing.T) {
	s, o, close := planFixture(t)
	planPending(t, s, o)
	planPending(t, s, o)
	close()
	dest := filepath.Join(t.TempDir(), strings.Repeat("x", 170), strings.Repeat("y", 170))
	require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0700))
	require.NoError(t, os.Rename(s.owner, dest))
	path := filepath.Join(dest, ".capy", "runs", "runs.db")
	opts := planOptions()
	opts.Limit = 1
	opts.Timeout = 15 * time.Minute
	first, err := PlanRecovery(t.Context(), path, testKey, opts)
	require.NoError(t, err)
	require.Empty(t, first.NextCommand)
	require.NotEmpty(t, first.NextCursor)
	require.Equal(t, "15m0s", first.NextTimeout)
	opts.Cursor = first.NextCursor
	opts.Timeout = time.Nanosecond
	v, err := PlanRecovery(t.Context(), path, testKey, opts)
	requireErrorCode(t, err, "recovery_inventory_incomplete")
	require.Empty(t, v.Token)
	require.Empty(t, v.NextCommand)
	require.Equal(t, first.NextCursor, v.NextCursor)
	require.Equal(t, "15m0s", v.NextTimeout)
	require.Contains(t, v.NextStep, "--project-dir")
}

func TestRecoveryPlanAfterSupportedLegacyUpgrade(t *testing.T) {
	for _, schema := range []int{8, 9} {
		t.Run(fmt.Sprint(schema), func(t *testing.T) {
			f := newLegacyFixture(t)
			batch := f.batch(t)
			pending := f.run(t, executor.CleanupPending, false, batch, "historical prefix\n")
			f.child(t, pending, 0, false)
			terminal := f.run(t, executor.CleanupRetired, true, batch, "successful sibling\n")
			f.child(t, terminal, 1, true)
			safe := f.run(t, executor.CleanupNotSpawned, false, batch, "")
			f.child(t, safe, 2, false)
			f.run(t, executor.CleanupRetired, true, "", "unrelated retained history")
			identity := f.identity(t)
			f.close(t)
			if schema == 9 {
				require.NoError(t, upgradeLegacyStage(t.Context(), f.path, testKey, f.owner, identity))
			}
			upgraded, err := Upgrade(t.Context(), f.path, testKey, f.settings)
			require.NoError(t, err)
			require.Equal(t, 10, upgraded.SchemaVersion)
			before, err := os.ReadFile(f.path)
			require.NoError(t, err)
			v, err := PlanRecovery(t.Context(), f.path, testKey, planOptions())
			require.NoError(t, err)
			require.Equal(t, "ready", v.Status)
			require.EqualValues(t, 1, v.Counts.Dispositions)
			require.EqualValues(t, 1, v.Counts.SafeReconciliation)
			require.NotEmpty(t, v.Token)
			after, err := os.ReadFile(f.path)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestRecoveryPlanRetainedWALIsByteImmutable(t *testing.T) {
	s, o, _ := planFixture(t)
	planPending(t, s, o)
	// Copy a quiescent encrypted snapshot while the fixture connection retains
	// WAL/SHM. The copied lock files have no holders, like an abandoned store.
	dest := t.TempDir()
	require.NoError(t, os.CopyFS(dest, os.DirFS(s.owner)))
	root := filepath.Join(dest, ".capy")
	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		mode := os.FileMode(0600)
		if d.IsDir() {
			mode = 0700
		}
		return os.Chmod(path, mode)
	}))
	path := filepath.Join(root, "runs", "runs.db")
	require.FileExists(t, path+"-wal")
	snapshot := func() map[string]string {
		out := map[string]string{}
		require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[path] = string(data)
			return nil
		}))
		return out
	}
	before := snapshot()
	v, err := PlanRecovery(t.Context(), path, testKey, planOptions())
	require.NoError(t, err)
	require.NotEmpty(t, v.Token)
	require.Equal(t, before, snapshot())
	require.NoError(t, os.Remove(path+"-shm"))
	before = snapshot()
	v, err = PlanRecovery(t.Context(), path, testKey, planOptions())
	requireErrorCode(t, err, "storage_unavailable")
	require.Empty(t, v.Token)
	require.NoFileExists(t, path+"-shm")
	require.Equal(t, before, snapshot(), "missing SHM cannot trigger reconstruction or file creation")
}

func TestRecoveryPlanTokenTargetBindingSurvivesReceiptLookup(t *testing.T) {
	s, o, close := planFixture(t)
	planPending(t, s, o)
	close()
	v, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
	require.NoError(t, err)
	plan, err := decodeInspectionPlan(v.Token)
	require.NoError(t, err)
	require.Len(t, plan.Target, 64)
	require.Len(t, plan.Snapshot, 64)
	for _, field := range []string{"target", "snapshot", "hash", "store"} {
		t.Run(field, func(t *testing.T) {
			changed := plan
			switch field {
			case "target":
				changed.Target = strings.Repeat("0", 64)
			case "snapshot":
				changed.Snapshot = strings.Repeat("0", 64)
			case "hash":
				changed.Hash = strings.Repeat("0", 64)
			case "store":
				changed.Store = "00000000-0000-0000-0000-000000000001"
			}
			data, _ := json.Marshal(changed)
			_, err := decodeInspectionPlan(base64.RawURLEncoding.EncodeToString(data))
			require.Error(t, err)
		})
	}
	// Receipt lookup needs only the recorded plan hash; it does not require
	// old target binding to match the current location or reconstruct evidence.
	id := "00000000-0000-0000-0000-000000000002"
	mutatePlanStore(t, s.path, `INSERT INTO run_recovery_receipts(id,plan_hash,store_id,version,applied_at,method,basis,uid,explanation,incident_count,run_count,owner_count,store_count,charged_bytes,audit_expires_at) VALUES(?,?,?,1,1,'operator','groups_stopped',0,'fixture',0,0,0,0,0,2)`, id, plan.Hash, s.id)
	dest := filepath.Join(t.TempDir(), "moved")
	require.NoError(t, os.Rename(s.owner, dest))
	inspection, err := InspectRuns(t.Context(), filepath.Join(dest, ".capy", "runs", "runs.db"), testKey, InspectionOptions{Plan: v.Token, Limit: 1})
	require.NoError(t, err)
	require.Equal(t, id, inspection.Receipt.ID)
	legacy, _ := json.Marshal(inspectionPlan{Version: 1, Store: plan.Store, Hash: plan.Hash})
	_, err = decodeInspectionPlan(base64.RawURLEncoding.EncodeToString(legacy))
	require.NoError(t, err, "legacy receipt lookup remains valid; future apply must require target and snapshot")
}

func TestRecoveryPlanStreamsWholeBatchInventory(t *testing.T) {
	s, o, close := planFixture(t)
	commands := make([]BatchCommand, 70)
	for i := range commands {
		commands[i] = BatchCommand{Command: "PRIVATE_COMMAND"}
	}
	batch, children, err := s.AdmitBatch(t.Context(), o, executor.CleanupLinuxDescendants, BatchInput{Concurrency: 1, Commands: commands})
	require.NoError(t, err)
	for _, child := range children {
		require.NoError(t, s.Pending(t.Context(), child.ID, o.ID, child.Cleanup.Scope))
	}
	close()
	opts := planOptions()
	opts.Limit = 1
	v, err := PlanRecovery(t.Context(), s.path, testKey, opts)
	require.NoError(t, err)
	require.EqualValues(t, 70, v.Counts.Dispositions)
	require.EqualValues(t, 1, v.Counts.Batches)
	require.NotEmpty(t, v.Token)
	// An omitted subject still belongs to the complete fingerprint.
	mutatePlanStore(t, s.path, "UPDATE runs SET last_phase='compile' WHERE id=?", children[69].ID)
	opts.Cursor = v.NextCursor
	v, err = PlanRecovery(t.Context(), s.path, testKey, opts)
	requireErrorCode(t, err, "recovery_plan_stale")
	require.Empty(t, v.Token)
	// A missing nonterminal child cannot become acknowledgement-ready.
	mutatePlanStore(t, s.path, "DELETE FROM runs WHERE id=?", children[69].ID)
	opts.Cursor = ""
	v, err = PlanRecovery(t.Context(), s.path, testKey, opts)
	requireErrorCode(t, err, "recovery_inconsistent")
	require.Empty(t, v.Token)
	require.NotContains(t, v.Error.Message, batch.BatchID)
}

func TestRecoveryPlanValidClaimSafeBatchAndTargetRecheck(t *testing.T) {
	for _, mode := range []string{"claim", "safe batch", "replaced during scan", "missing lock", "unsafe lock"} {
		t.Run(mode, func(t *testing.T) {
			s, o, close := planFixture(t)
			r := planPending(t, s, o)
			if mode == "claim" {
				_, err := s.db.Exec("INSERT INTO run_work_claims(run_id,slot,generation) VALUES(?,?,?)", r.ID, o.Slot, o.ID)
				require.NoError(t, err)
			}
			if mode == "safe batch" {
				_, _, err := s.AdmitBatch(t.Context(), o, executor.CleanupLinuxDescendants, BatchInput{Concurrency: 1, Commands: []BatchCommand{{Command: "safe"}}})
				require.NoError(t, err)
			}
			close()
			lock := filepath.Join(filepath.Dir(s.path), "recovery.lock")
			if mode == "missing lock" {
				require.NoError(t, os.Remove(lock))
			}
			if mode == "unsafe lock" {
				require.NoError(t, os.Chmod(lock, 0644))
			}
			var step func(context.Context) error
			if mode == "replaced during scan" {
				done := false
				step = func(context.Context) error {
					if !done {
						done = true
						require.NoError(t, os.WriteFile(lock+".replacement", nil, 0600))
						require.NoError(t, os.Rename(lock+".replacement", lock))
					}
					return nil
				}
			}
			v, err := planRecovery(t.Context(), s.path, testKey, planOptions(), step)
			switch mode {
			case "claim", "safe batch":
				require.NoError(t, err)
				require.NotEmpty(t, v.Token)
				if mode == "safe batch" {
					require.EqualValues(t, 1, v.Counts.SafeReconciliation)
				}
			case "replaced during scan":
				requireErrorCode(t, err, "recovery_plan_stale")
				require.Empty(t, v.Token)
			default:
				requireErrorCode(t, err, "recovery_inconsistent")
				require.Empty(t, v.Token)
			}
		})
	}
}
