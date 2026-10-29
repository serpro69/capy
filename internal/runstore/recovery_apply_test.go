package runstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/sqliteutil"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func applyOptions(token string) recoveryApplyOptions {
	return recoveryApplyOptions{Plan: token, ConfirmStopped: true, Basis: "groups_stopped", Explanation: "Stopped the originating execution groups", Timeout: DefaultRecoveryTimeout}
}

func applyTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", sqliteutil.EncryptedDSN(path, testKey)+"&mode=rw&_foreign_keys=on")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func applyErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var typed *Error
	require.ErrorAs(t, err, &typed)
	require.Equal(t, code, typed.Code)
}

// Compare original execution, program, payload and sanitizer checkpoint bytes.
// Administrative records and mutable registry metadata are intentionally separate.
func applyHistory(t *testing.T, q runQuerier, id string) []byte {
	t.Helper()
	var value string
	err := q.QueryRowContext(t.Context(), `SELECT json_array(hex(program),hex(result),generation,slot,cleanup_scope,cleanup,terminal,created_at,completed_at,expires_at,last_phase,stdout_bytes,stderr_bytes,stdout_lines,stderr_lines,
 (SELECT group_concat(hex(data),'|') FROM run_events WHERE run_id=runs.id ORDER BY seq),
 (SELECT group_concat(hex(checkpoint)||':'||covered||':'||observed,'|') FROM run_streams WHERE run_id=runs.id ORDER BY stream),
 (SELECT group_concat(hex(text),'|') FROM run_projection WHERE run_id=runs.id ORDER BY stream,raw_start)) FROM runs WHERE id=?`, id).Scan(&value)
	require.NoError(t, err)
	return []byte(value)
}

func TestRecoveryApplyStandalonePreservesHistoryAndReusesOwner(t *testing.T) {
	s, o, closeStore := planFixture(t)
	sibling := planPending(t, s, o)
	require.NoError(t, s.Finish(t.Context(), sibling.ID, Completion{Cleanup: executor.CleanupEvidence{Scope: sibling.Cleanup.Scope, State: executor.CleanupRetired}, Started: true}))
	r := planPending(t, s, o)
	require.NoError(t, s.QuarantineRun(t.Context(), RuntimeIncident{RunID: r.ID, Generation: o.ID, Slot: o.Slot, Cause: IncidentSupervisorLost, Stage: StageHelperWait}))
	before, siblingBefore := applyHistory(t, s.db, r.ID), applyHistory(t, s.db, sibling.ID)
	var original []byte
	require.NoError(t, s.db.QueryRow("SELECT result FROM runs WHERE id=?", r.ID).Scan(&original))
	closeStore()
	plan, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
	require.NoError(t, err)
	view, err := applyRecovery(t.Context(), s.path, testKey, applyOptions(plan.Token), nil)
	require.NoError(t, err)
	require.Equal(t, "applied", view.Status)
	require.False(t, view.AdmissionBlocked)
	require.EqualValues(t, 1, view.Receipt.RunCount)
	require.Equal(t, *plan.RecoveryRevision+1, view.RecoveryRevision)
	view2, err := applyRecovery(t.Context(), s.path, testKey, applyOptions(plan.Token), nil)
	require.NoError(t, err)
	require.Equal(t, "already_applied", view2.Status)
	require.Equal(t, view.Receipt, view2.Receipt)
	empty, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
	require.NoError(t, err)
	require.Equal(t, "no_recovery_needed", empty.Status)

	fresh, err := openAttached(t.Context(), s.path, testKey, s.owner, unix.LOCK_SH)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, fresh.closeHandles()) })
	owner, err := fresh.ClaimOwner(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Lease.Close()) })
	require.Equal(t, o.Slot, owner.Slot)
	require.NotEqual(t, o.ID, owner.ID)
	require.NoError(t, fresh.GuardAdmission(t.Context()))
	_, err = fresh.Admit(t.Context(), owner, executor.CleanupLinuxDescendants, Program{})
	require.NoError(t, err)
	require.Equal(t, before, applyHistory(t, fresh.db, r.ID))
	require.Equal(t, siblingBefore, applyHistory(t, fresh.db, sibling.ID))
	d, err := readRunResolution(t.Context(), fresh.db, r.ID)
	require.NoError(t, err)
	require.Equal(t, original, d.Original)
	digest := sha256.Sum256(original)
	require.Equal(t, digest[:], d.Digest)
	require.Equal(t, o.ID, d.Generation)
	for _, mutate := range []func() error{
		func() error { return fresh.Pending(t.Context(), r.ID, o.ID, r.Cleanup.Scope) },
		func() error { return fresh.Started(t.Context(), r.ID, "run") },
		func() error {
			return fresh.Finish(t.Context(), r.ID, Completion{Cleanup: executor.CleanupEvidence{Scope: r.Cleanup.Scope, State: executor.CleanupRetired}})
		},
		func() error {
			return fresh.Append(t.Context(), r.ID, executor.OutputEvent{Sequence: 2, Phase: "run", Stream: "stdout", Data: []byte("forbidden")})
		},
	} {
		applyErrorCode(t, mutate(), "run_unavailable")
	}
	_, err = fresh.ClaimIndex(t.Context(), owner, r.ID)
	applyErrorCode(t, err, "run_unavailable")
	tx, err := sqliteutil.BeginImmediateContext(t.Context(), fresh.db, "runs")
	require.NoError(t, err)
	applyErrorCode(t, saveIndexResult(t.Context(), tx, Admitted{ID: r.ID}), "run_unavailable")
	require.NoError(t, tx.Rollback())
	require.Equal(t, before, applyHistory(t, fresh.db, r.ID))
}

func TestRecoveryApplyAtomicFailures(t *testing.T) {
	for _, mode := range []string{"write", "capacity", "before commit", "commit rejected", "lost commit response"} {
		t.Run(mode, func(t *testing.T) {
			s, o, closeStore := planFixture(t)
			r := planPending(t, s, o)
			require.NoError(t, s.Quarantine(t.Context()))
			before := applyHistory(t, s.db, r.ID)
			if mode == "write" {
				_, err := s.db.Exec("CREATE TRIGGER fail_resolution BEFORE INSERT ON run_resolutions BEGIN SELECT RAISE(ABORT,'PRIVATE_BACKEND'); END")
				require.NoError(t, err)
			}
			if mode == "capacity" {
				_, err := s.db.Exec("CREATE TRIGGER exhaust_audit AFTER INSERT ON run_resolutions BEGIN UPDATE run_meta SET accounted_bytes=(SELECT max_retained_bytes FROM run_policy)+1; END")
				require.NoError(t, err)
			}
			closeStore()
			plan, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
			require.NoError(t, err)
			hooks := &recoveryApplyHooks{}
			switch mode {
			case "before commit":
				hooks.BeforeCommit = func(context.Context, *sql.Tx) error { return errors.New("PRIVATE_BACKEND") }
			case "commit rejected":
				hooks.Commit = func(*sql.Tx) error { return errors.New("PRIVATE_BACKEND") }
			case "lost commit response":
				hooks.Commit = func(tx *sql.Tx) error { require.NoError(t, tx.Commit()); return errors.New("PRIVATE_BACKEND") }
			}
			_, err = applyRecovery(t.Context(), s.path, testKey, applyOptions(plan.Token), hooks)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "PRIVATE_BACKEND")
			if mode == "capacity" {
				applyErrorCode(t, err, "recovery_capacity")
			}
			if strings.Contains(mode, "commit rejected") || mode == "lost commit response" {
				applyErrorCode(t, err, "recovery_outcome_unknown")
			}
			db := applyTestDB(t, s.path)
			require.Equal(t, before, applyHistory(t, db, r.ID))
			var receipts, dispositions, members int
			var blocked bool
			require.NoError(t, db.QueryRow("SELECT admission_blocked,(SELECT count(*) FROM run_recovery_receipts),(SELECT count(*) FROM run_resolutions),(SELECT count(*) FROM run_incident_resolutions) FROM run_meta").Scan(&blocked, &receipts, &dispositions, &members))
			want := 0
			if mode == "lost commit response" {
				want = 1
			}
			require.Equal(t, want, receipts)
			require.Equal(t, want, dispositions)
			require.Equal(t, want, members)
			require.Equal(t, want == 0, blocked)
			require.NoError(t, db.Close())
			if mode == "lost commit response" {
				lookup, err := InspectRuns(t.Context(), s.path, testKey, InspectionOptions{Plan: plan.Token, Limit: 64})
				require.NoError(t, err)
				require.NotNil(t, lookup.Receipt)
				again, err := applyRecovery(t.Context(), s.path, testKey, applyOptions(plan.Token), nil)
				require.NoError(t, err)
				require.Equal(t, lookup.Receipt.ID, again.Receipt.ID)
			}
		})
	}
}

func TestRecoveryApplyAttestationAndLaterBlocker(t *testing.T) {
	s, o, closeStore := planFixture(t)
	planPending(t, s, o)
	closeStore()
	plan, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
	require.NoError(t, err)
	options := applyOptions(plan.Token)
	options.Explanation = "  Groups\n stopped\tpassword=abcdefghijklmnopqrstuvwx  "
	view, err := applyRecovery(t.Context(), s.path, testKey, options, nil)
	require.NoError(t, err)
	require.NotContains(t, view.Receipt.Explanation, "abcdefghijklmnopqrstuvwx")
	options.Explanation = view.Receipt.Explanation
	options.Timeout = 15 * time.Minute
	again, err := applyRecovery(t.Context(), s.path, testKey, options, nil)
	require.NoError(t, err)
	require.Equal(t, view.Receipt, again.Receipt)
	for _, mode := range []string{"basis", "explanation"} {
		t.Run(mode, func(t *testing.T) {
			conflict := options
			if mode == "basis" {
				conflict.Basis = "host_restarted"
			} else {
				conflict.Explanation = "Different operator explanation"
			}
			got, err := applyRecovery(t.Context(), s.path, testKey, conflict, nil)
			applyErrorCode(t, err, "recovery_receipt_conflict")
			require.Equal(t, view.Receipt.ID, got.Receipt.ID)
		})
	}
	fresh, err := openAttached(t.Context(), s.path, testKey, s.owner, unix.LOCK_SH)
	require.NoError(t, err)
	owner, err := fresh.ClaimOwner(t.Context())
	require.NoError(t, err)
	r := planPending(t, fresh, owner)
	require.NoError(t, fresh.QuarantineRun(t.Context(), RuntimeIncident{RunID: r.ID, Generation: owner.ID, Slot: owner.Slot, Cause: IncidentSupervisorLost, Stage: StageHelperWait}))
	require.NoError(t, owner.Lease.Close())
	require.NoError(t, fresh.closeHandles())
	again, err = applyRecovery(t.Context(), s.path, testKey, options, nil)
	require.NoError(t, err)
	require.Equal(t, view.Receipt, again.Receipt)
	require.True(t, again.AdmissionBlocked)
	require.EqualValues(t, 1, again.UnresolvedIncidents)
}

func TestRecoveryApplyRefusalsAndSnapshotBinding(t *testing.T) {
	for _, mode := range []string{"wrong key", "held lease", "no confirmation", "legacy token", "changed prefix", "changed policy", "unsupported generation", "capacity"} {
		t.Run(mode, func(t *testing.T) {
			s, o, closeStore := planFixture(t)
			r := planPending(t, s, o)
			closeStore()
			plan, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
			require.NoError(t, err)
			options, key, code := applyOptions(plan.Token), testKey, "recovery_plan_stale"
			switch mode {
			case "wrong key":
				key, code = "wrong synthetic key", "wrong_key"
			case "held lease":
				active, err := openAttached(t.Context(), s.path, testKey, s.owner, unix.LOCK_SH)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, active.closeHandles()) })
				code = "storage_busy"
			case "no confirmation":
				options.ConfirmStopped, code = false, "invalid_argument"
			case "legacy token":
				p, err := decodeInspectionPlan(plan.Token)
				require.NoError(t, err)
				p.Target, p.Snapshot = "", ""
				b, err := json.Marshal(p)
				require.NoError(t, err)
				options.Plan, code = base64.RawURLEncoding.EncodeToString(b), "invalid_argument"
			case "changed prefix":
				mutatePlanStore(t, s.path, "UPDATE run_streams SET checkpoint=? WHERE run_id=? AND stream='stdout'", []byte("changed"), r.ID)
			case "changed policy":
				mutatePlanStore(t, s.path, "UPDATE run_policy SET revision=revision+1")
			case "unsupported generation":
				mutatePlanStore(t, s.path, "UPDATE run_owners SET generation=? WHERE slot=?", "00000000-0000-0000-0000-000000000001", o.Slot)
				code = "recovery_inconsistent"
			case "capacity":
				// A quota change first invalidates the reviewed plan. Fresh planning
				// then refuses capacity without evicting the retained pending row.
				mutatePlanStore(t, s.path, "UPDATE run_meta SET accounted_bytes=(SELECT max_retained_bytes FROM run_policy)-1")
			}
			_, err = applyRecovery(t.Context(), s.path, key, options, nil)
			applyErrorCode(t, err, code)
			db := applyTestDB(t, s.path)
			var count int
			require.NoError(t, db.QueryRow("SELECT count(*) FROM run_recovery_receipts").Scan(&count))
			require.Zero(t, count)
		})
	}
}

func TestRecoveryApplyDeadlinesAndPruningReceipt(t *testing.T) {
	t.Run("extended deadline", func(t *testing.T) {
		s, o, closeStore := planFixture(t)
		planPending(t, s, o)
		closeStore()
		plan, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
		require.NoError(t, err)
		hook := func(delay time.Duration) *recoveryApplyHooks {
			first := true
			return &recoveryApplyHooks{Inventory: func(ctx context.Context) error {
				if !first {
					return nil
				}
				first = false
				timer := time.NewTimer(delay)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-timer.C:
					return nil
				}
			}}
		}
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		defer cancel()
		_, err = applyRecovery(ctx, s.path, testKey, applyOptions(plan.Token), hook(time.Second))
		applyErrorCode(t, err, "recovery_timeout")
		view, err := applyRecovery(t.Context(), s.path, testKey, applyOptions(plan.Token), hook(5100*time.Millisecond))
		require.NoError(t, err)
		require.Equal(t, "applied", view.Status)
	})
	t.Run("pruning summary", func(t *testing.T) {
		s, _, closeStore := planFixture(t)
		require.NoError(t, s.Quarantine(t.Context()))
		closeStore()
		plan, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
		require.NoError(t, err)
		view, err := applyRecovery(t.Context(), s.path, testKey, applyOptions(plan.Token), nil)
		require.NoError(t, err)
		mutatePlanStore(t, s.path, "UPDATE run_recovery_receipts SET pruning=1")
		mutatePlanStore(t, s.path, "DELETE FROM run_incident_resolutions")
		mutatePlanStore(t, s.path, "DELETE FROM run_incidents")
		again, err := applyRecovery(t.Context(), s.path, testKey, applyOptions(plan.Token), nil)
		require.NoError(t, err)
		require.Equal(t, view.Receipt.ID, again.Receipt.ID)
		require.True(t, again.Receipt.Pruning)
		require.EqualValues(t, 1, again.Receipt.IncidentCount)
	})
}

func TestRecoveryApplyRejectsChangedDisposition(t *testing.T) {
	for _, mutation := range []string{"result", "digest", "generation", "missing index"} {
		t.Run(mutation, func(t *testing.T) {
			s, o, closeStore := planFixture(t)
			planPending(t, s, o)
			closeStore()
			plan, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
			require.NoError(t, err)
			_, err = applyRecovery(t.Context(), s.path, testKey, applyOptions(plan.Token), nil)
			require.NoError(t, err)
			query := map[string]string{"result": "UPDATE runs SET result=CAST(result AS TEXT)||' '", "digest": "UPDATE run_resolutions SET result_sha256=zeroblob(32)", "generation": "UPDATE runs SET generation='00000000-0000-0000-0000-000000000001'", "missing index": "DELETE FROM run_search_state"}[mutation]
			mutatePlanStore(t, s.path, query)
			fresh, err := openAttached(t.Context(), s.path, testKey, s.owner, unix.LOCK_SH)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, fresh.closeHandles()) })
			_, err = fresh.ClaimOwner(t.Context())
			applyErrorCode(t, err, "recovery_inconsistent")
		})
	}
}

func TestRecoveryApplyIncidentOnlyAndSafeWork(t *testing.T) {
	for _, mode := range []string{"terminal incident", "safe incident", "owner marker", "maximum ttl"} {
		t.Run(mode, func(t *testing.T) {
			s, o, closeStore := planFixture(t)
			if mode == "maximum ttl" {
				s.policy.TTLHours = 2562047
				_, err := s.db.Exec("UPDATE run_policy SET ttl_hours=?", s.policy.TTLHours)
				require.NoError(t, err)
			}
			r := planPending(t, s, o)
			require.NoError(t, s.QuarantineRun(t.Context(), RuntimeIncident{RunID: r.ID, Generation: o.ID, Slot: o.Slot, Cause: IncidentFinalizationUnpersisted, Stage: StageFinalization}))
			if mode == "terminal incident" {
				require.NoError(t, s.Finish(t.Context(), r.ID, Completion{Cleanup: executor.CleanupEvidence{Scope: r.Cleanup.Scope, State: executor.CleanupRetired}, Started: true}))
			}
			if mode == "safe incident" {
				// Accepted evidence was persisted before final outcome recording.
				require.NoError(t, s.update(t.Context(), r.ID, func(_ *sql.Tx, row *Admitted) error { row.Cleanup.State = executor.CleanupRetired; return nil }))
			}
			if mode == "owner marker" {
				_, err := s.db.Exec("INSERT INTO run_owners(slot,generation,cleanup_unverified) VALUES(63,'00000000-0000-0000-0000-000000000063',1)")
				require.NoError(t, err)
			}
			before := applyHistory(t, s.db, r.ID)
			closeStore()
			inspection, err := InspectRuns(t.Context(), s.path, testKey, InspectionOptions{Limit: 1})
			require.NoError(t, err)
			plan, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
			require.NoError(t, err)
			view, err := applyRecovery(t.Context(), s.path, testKey, applyOptions(plan.Token), nil)
			require.NoError(t, err)
			if mode == "terminal incident" || mode == "safe incident" {
				require.Zero(t, view.Receipt.RunCount)
			}
			if mode == "safe incident" {
				require.EqualValues(t, 1, view.SafeReconciliation)
			}
			if mode == "owner marker" {
				require.EqualValues(t, 1, view.Receipt.OwnerCount)
			}
			if inspection.NextCursor != "" {
				_, err := InspectRuns(t.Context(), s.path, testKey, InspectionOptions{Limit: 1, Cursor: inspection.NextCursor})
				applyErrorCode(t, err, "inspection_stale")
			}
			db := applyTestDB(t, s.path)
			require.Equal(t, before, applyHistory(t, db, r.ID))
			if mode == "maximum ttl" {
				d, err := readRunResolution(t.Context(), db, r.ID)
				require.NoError(t, err)
				require.Greater(t, time.UnixMilli(d.ExpiresAt).Year(), 2262)
			}
		})
	}
}

func TestRecoveryApplyCompleteInventoryAndCancellationAfterWrites(t *testing.T) {
	s, o, closeStore := planFixture(t)
	for range 66 {
		planPending(t, s, o)
	}
	closeStore()
	opts := planOptions()
	opts.Limit = 1
	plan, err := PlanRecovery(t.Context(), s.path, testKey, opts)
	require.NoError(t, err)
	require.EqualValues(t, 66, plan.Counts.Dispositions)
	require.Len(t, plan.Subjects, 1)
	ctx, cancel := context.WithCancel(t.Context())
	_, err = applyRecovery(ctx, s.path, testKey, applyOptions(plan.Token), &recoveryApplyHooks{BeforeCommit: func(context.Context, *sql.Tx) error { cancel(); return ctx.Err() }})
	applyErrorCode(t, err, "recovery_timeout")
	again, err := PlanRecovery(t.Context(), s.path, testKey, opts)
	require.NoError(t, err)
	require.Equal(t, plan.Token, again.Token)
	view, err := applyRecovery(t.Context(), s.path, testKey, applyOptions(plan.Token), nil)
	require.NoError(t, err)
	require.EqualValues(t, 66, view.Receipt.RunCount)
	require.EqualValues(t, 66, view.Receipt.IncidentCount)
	db := applyTestDB(t, s.path)
	var used, charged int64
	require.NoError(t, db.QueryRow(`SELECT accounted_bytes,
 (SELECT sum(accounted_bytes) FROM runs)+(SELECT sum(charged_bytes) FROM run_incidents)+(SELECT sum(charged_bytes) FROM run_recovery_receipts)+(SELECT sum(charged_bytes) FROM run_incident_resolutions)+(SELECT sum(charged_bytes) FROM run_resolutions) FROM run_meta`).Scan(&used, &charged))
	require.Equal(t, charged, used, "all audit debits are independently accounted exactly once")
}

func TestRecoveryApplyClaimAndPhysicalReceiptBinding(t *testing.T) {
	s, o, closeStore := planFixture(t)
	r := planPending(t, s, o)
	claimed, err := s.ClaimIndex(t.Context(), o, r.ID)
	require.NoError(t, err)
	require.True(t, claimed)
	before := applyHistory(t, s.db, r.ID)
	closeStore()
	plan, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
	require.NoError(t, err)
	view, err := applyRecovery(t.Context(), s.path, testKey, applyOptions(plan.Token), nil)
	require.NoError(t, err)
	fresh, err := openAttached(t.Context(), s.path, testKey, s.owner, unix.LOCK_SH)
	require.NoError(t, err)
	owner, err := fresh.ClaimOwner(t.Context())
	require.NoError(t, err)
	require.Equal(t, before, applyHistory(t, fresh.db, r.ID))
	var state, code string
	require.NoError(t, fresh.db.QueryRow("SELECT state,error_code FROM run_search_state WHERE run_id=?", r.ID).Scan(&state, &code))
	require.Equal(t, "failed", state)
	require.Equal(t, "index_unavailable", code)
	require.NoError(t, owner.Lease.Close())
	require.NoError(t, fresh.closeHandles())
	// Same UUID/content at the old path still has a different physical inode.
	data, err := os.ReadFile(s.path)
	require.NoError(t, err)
	require.NoError(t, os.Rename(s.path, s.path+".old"))
	require.NoError(t, os.WriteFile(s.path, data, 0600))
	_, err = applyRecovery(t.Context(), s.path, testKey, applyOptions(plan.Token), nil)
	applyErrorCode(t, err, "recovery_plan_stale")
	lookup, err := InspectRuns(t.Context(), s.path, testKey, InspectionOptions{Plan: plan.Token, Limit: 64})
	require.NoError(t, err)
	require.Equal(t, view.Receipt.ID, lookup.Receipt.ID, "read-only historical lookup does not require the old physical binding")
}

func TestRecoveryApplyNormalizesQuotaBlockedIndex(t *testing.T) {
	s, o, closeStore := planFixture(t)
	r := planPending(t, s, o)
	claimed, err := s.ClaimIndex(t.Context(), o, r.ID)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, s.failIndex(t.Context(), o, r.ID, &Error{Code: "index_quota_blocked", Message: "quota"}))
	require.NoError(t, s.ReleaseIndex(t.Context(), o, r.ID))
	before := applyHistory(t, s.db, r.ID)
	closeStore()
	plan, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
	require.NoError(t, err)
	_, err = applyRecovery(t.Context(), s.path, testKey, applyOptions(plan.Token), nil)
	require.NoError(t, err)
	db := applyTestDB(t, s.path)
	require.Equal(t, before, applyHistory(t, db, r.ID))
	var state, code string
	require.NoError(t, db.QueryRow("SELECT state,error_code FROM run_search_state WHERE run_id=?", r.ID).Scan(&state, &code))
	require.Equal(t, "failed", state)
	require.Equal(t, "index_capacity", code)
}

func TestRecoveryApplyStalesIncidentPages(t *testing.T) {
	s, o, closeStore := planFixture(t)
	a, b := planPending(t, s, o), planPending(t, s, o)
	for _, r := range []Admitted{a, b} {
		require.NoError(t, s.QuarantineRun(t.Context(), RuntimeIncident{RunID: r.ID, Generation: o.ID, Slot: o.Slot, Cause: IncidentSupervisorLost, Stage: StageHelperWait}))
	}
	closeStore()
	page, err := InspectRuns(t.Context(), s.path, testKey, InspectionOptions{Limit: 1})
	require.NoError(t, err)
	require.NotEmpty(t, page.NextCursor)
	plan, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
	require.NoError(t, err)
	_, err = applyRecovery(t.Context(), s.path, testKey, applyOptions(plan.Token), nil)
	require.NoError(t, err)
	_, err = InspectRuns(t.Context(), s.path, testKey, InspectionOptions{Limit: 1, Cursor: page.NextCursor})
	applyErrorCode(t, err, "inspection_stale")
}

func TestRecoveryAfterDispositionAttributesLaterMarkers(t *testing.T) {
	s, o, closeStore := planFixture(t)
	r := planPending(t, s, o)
	closeStore()
	plan, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
	require.NoError(t, err)
	_, err = applyRecovery(t.Context(), s.path, testKey, applyOptions(plan.Token), nil)
	require.NoError(t, err)
	fresh, err := openAttached(t.Context(), s.path, testKey, s.owner, unix.LOCK_SH)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, fresh.closeHandles()) })
	_, err = fresh.db.Exec("UPDATE run_owners SET cleanup_unverified=1 WHERE slot=?", o.Slot)
	require.NoError(t, err)
	require.NoError(t, fresh.Quarantine(t.Context()))
	_, err = fresh.Reconcile(t.Context())
	require.NoError(t, err)
	var stores, owners int
	require.NoError(t, fresh.db.QueryRow("SELECT count(*) FROM run_incidents WHERE subject_kind='store' AND receipt_id IS NULL").Scan(&stores))
	require.NoError(t, fresh.db.QueryRow("SELECT count(*) FROM run_incidents WHERE subject_kind='owner' AND receipt_id IS NULL").Scan(&owners))
	require.Equal(t, 1, stores)
	require.Equal(t, 1, owners)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = readRunResolution(ctx, fresh.db, r.ID)
	applyErrorCode(t, err, "storage_unavailable")
	require.ErrorIs(t, err, context.Canceled)
}

func TestRecoveryPlanAndApplyIndexStates(t *testing.T) {
	for _, state := range []string{"not_requested", "building", "partial", "ready", "failed", "quota_blocked", "unknown"} {
		t.Run(state, func(t *testing.T) {
			s, o, closeStore := planFixture(t)
			r := planPending(t, s, o)
			require.NoError(t, s.update(t.Context(), r.ID, func(_ *sql.Tx, row *Admitted) error {
				row.Result.Index = Index{State: state}
				if state == "failed" {
					row.Result.Index.Error = &Error{Code: "index_quota_blocked", Message: "private error"}
				}
				return nil
			}))
			before := applyHistory(t, s.db, r.ID)
			closeStore()
			plan, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
			if state == "unknown" {
				applyErrorCode(t, err, "recovery_inconsistent")
				require.Empty(t, plan.Token)
				return
			}
			require.NoError(t, err)
			_, err = applyRecovery(t.Context(), s.path, testKey, applyOptions(plan.Token), nil)
			require.NoError(t, err)
			db := applyTestDB(t, s.path)
			require.Equal(t, before, applyHistory(t, db, r.ID))
			var got string
			var code sql.NullString
			require.NoError(t, db.QueryRow("SELECT state,error_code FROM run_search_state WHERE run_id=?", r.ID).Scan(&got, &code))
			want := state
			if state == "quota_blocked" {
				want = "failed"
			}
			require.Equal(t, want, got)
			if state == "quota_blocked" || state == "failed" {
				require.Equal(t, "index_capacity", code.String)
			}
		})
	}
}
