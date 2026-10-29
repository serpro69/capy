package runstore

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/sqliteutil"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func recoveryBatchFixture(t *testing.T, scope executor.CleanupScope, count int) (*Store, *Owner, BatchResult, []Admitted, func()) {
	t.Helper()
	s, o, closeStore := planFixture(t)
	commands := make([]BatchCommand, count)
	for i := range commands {
		commands[i] = BatchCommand{Label: "historical child", Command: "fixture only"}
	}
	b, children, err := s.AdmitBatch(t.Context(), o, scope, BatchInput{Concurrency: 2, Commands: commands})
	require.NoError(t, err)
	return s, o, b, children, closeStore
}

func startRecoveryChild(t *testing.T, s *Store, o *Owner, r Admitted) {
	t.Helper()
	require.NoError(t, s.Pending(t.Context(), r.ID, o.ID, r.Cleanup.Scope))
	require.NoError(t, s.Started(t.Context(), r.ID, "run"))
	require.NoError(t, s.Append(t.Context(), r.ID, executor.OutputEvent{Sequence: 1, Phase: "run", Stream: "stdout", Data: []byte("safe retained prefix\npassword=unfinished")}))
}

func finishRecoveryChild(t *testing.T, s *Store, r Admitted, code int) {
	t.Helper()
	state := executor.CleanupRetired
	if r.Cleanup.Scope == executor.CleanupDarwinObservation {
		state = executor.CleanupObserved
	}
	require.NoError(t, s.Finish(t.Context(), r.ID, Completion{Cleanup: executor.CleanupEvidence{Scope: r.Cleanup.Scope, State: state}, Started: true, ExitCode: &code}))
}

func attachBatchFixture(t *testing.T, s *Store) *Store {
	t.Helper()
	st, err := openAttached(t.Context(), s.path, testKey, s.owner, unix.LOCK_SH)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.closeHandles()) })
	return st
}

func assertRecoveryAccounting(t *testing.T, s *Store) int64 {
	t.Helper()
	var actual, expected int64
	require.NoError(t, s.db.QueryRow(`SELECT accounted_bytes,index_bytes+
 (SELECT COALESCE(sum(accounted_bytes),0) FROM runs)+(SELECT COALESCE(sum(accounted_bytes),0) FROM run_batches)+
 (SELECT COALESCE(sum(charged_bytes),0) FROM run_incidents)+(SELECT COALESCE(sum(charged_bytes),0) FROM run_recovery_receipts)+
 (SELECT COALESCE(sum(charged_bytes),0) FROM run_resolutions)+(SELECT COALESCE(sum(charged_bytes),0) FROM run_incident_resolutions)+
 (SELECT COALESCE(sum(charged_bytes),0) FROM run_batch_resolutions) FROM run_meta`).Scan(&actual, &expected))
	require.Equal(t, expected, actual, "each payload, pending batch reserve and independent audit record is charged once")
	return actual
}

func TestBatchRecoveryMixedHistoryAndDelayedClosure(t *testing.T) {
	for _, scope := range []executor.CleanupScope{executor.CleanupLinuxDescendants, executor.CleanupDarwinObservation} {
		t.Run(string(scope), func(t *testing.T) {
			s, o, b, children, closeStore := recoveryBatchFixture(t, scope, 5)
			for _, i := range []int{0, 1, 2} {
				startRecoveryChild(t, s, o, children[i])
			}
			finishRecoveryChild(t, s, children[0], 0)
			finishRecoveryChild(t, s, children[1], 7)
			require.NoError(t, s.Finish(t.Context(), children[3].ID, Completion{Cleanup: children[3].Cleanup, Reason: "batch_stopped"}))
			_, err := s.Search(t.Context(), o, SearchOptions{RunID: children[2].ID, Queries: []string{"retained"}, Limit: 1})
			require.NoError(t, err)
			require.NoError(t, s.QuarantineRun(t.Context(), RuntimeIncident{RunID: children[2].ID, BatchID: b.BatchID, Generation: o.ID, Slot: o.Slot, Cause: IncidentSupervisorLost, Stage: StageHelperWait}))
			before := make([][]byte, 4)
			for i := range before {
				before[i] = applyHistory(t, s.db, children[i].ID)
			}
			var manifest []byte
			require.NoError(t, s.db.QueryRow("SELECT result FROM run_batch_children WHERE run_id=?", children[2].ID).Scan(&manifest))
			var debit int64
			require.NoError(t, s.db.QueryRow("SELECT accounted_bytes FROM run_batches WHERE id=?", b.BatchID).Scan(&debit))
			closeStore()
			plan, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
			require.NoError(t, err)
			require.EqualValues(t, 1, plan.Counts.Batches)
			require.EqualValues(t, 1, plan.Counts.Dispositions)
			require.EqualValues(t, 1, plan.Counts.SafeReconciliation)
			applied, err := applyRecovery(t.Context(), s.path, testKey, applyOptions(plan.Token), nil)
			require.NoError(t, err)
			fresh := attachBatchFixture(t, s)
			status, err := fresh.BatchStatus(t.Context(), b.BatchID)
			require.NoError(t, err)
			require.Equal(t, 1, status.RecoveredCount)
			require.Equal(t, 0, status.CurrentConcurrency)
			require.Equal(t, "running", status.State, "execution state remains historical")
			require.Equal(t, 1, status.Counts["running"])
			require.Equal(t, 1, status.Counts["queued"])
			require.Nil(t, status.CompletedAt)
			require.Nil(t, status.ExpiresAt)
			require.Equal(t, executor.CleanupPending, status.Cleanup.State)
			require.Equal(t, "partially_resolved", status.Recovery.State)
			require.False(t, status.Recovery.OperationallyClosed)
			require.Nil(t, status.Recovery.ClosedAt)
			var reserved int64
			require.NoError(t, fresh.db.QueryRow("SELECT accounted_bytes FROM run_batches WHERE id=?", b.BatchID).Scan(&reserved))
			require.Equal(t, debit+recoveryBatchReserve, reserved)
			assertRecoveryAccounting(t, fresh)
			page, err := fresh.List(t.Context(), ListOptions{BatchID: b.BatchID, Limit: 64})
			require.NoError(t, err)
			require.Len(t, page.Entries, 5)
			require.Equal(t, applied.Receipt.ID, page.Entries[2].Recovery.ReceiptID)
			require.Equal(t, "incomplete", page.Entries[2].CaptureState)
			require.Nil(t, page.Entries[2].ExitCode)
			report, err := fresh.Reconcile(t.Context())
			require.NoError(t, err)
			require.Equal(t, 1, report.RecoveredRuns)
			after, err := fresh.BatchStatus(t.Context(), b.BatchID)
			require.NoError(t, err)
			require.True(t, after.Recovery.OperationallyClosed)
			require.NotNil(t, after.Recovery.ClosedAt)
			require.Equal(t, applied.Receipt.ID, after.Recovery.ReceiptID)
			require.Equal(t, "operator_resolved", after.Recovery.State)
			require.Nil(t, after.CompletedAt)
			require.Nil(t, after.ExpiresAt)
			require.Equal(t, executor.CleanupPending, after.Cleanup.State)
			for i := range before {
				require.Equal(t, before[i], applyHistory(t, fresh.db, children[i].ID))
			}
			var kept []byte
			require.NoError(t, fresh.db.QueryRow("SELECT result FROM run_batch_children WHERE run_id=?", children[2].ID).Scan(&kept))
			require.Equal(t, manifest, kept)
			require.NoError(t, fresh.db.QueryRow("SELECT accounted_bytes FROM run_batches WHERE id=?", b.BatchID).Scan(&reserved))
			require.Equal(t, debit, reserved, "closure transfers, rather than double charges, its reservation")
			used := assertRecoveryAccounting(t, fresh)
			var epoch int64
			require.NoError(t, fresh.db.QueryRow("SELECT recovery_revision FROM run_meta").Scan(&epoch))
			require.Equal(t, applied.RecoveryRevision+1, epoch)
			_, err = fresh.Reconcile(t.Context())
			require.NoError(t, err)
			again, err := fresh.BatchStatus(t.Context(), b.BatchID)
			require.NoError(t, err)
			require.Equal(t, after.Recovery, again.Recovery)
			require.Equal(t, used, assertRecoveryAccounting(t, fresh))
			var repeated int64
			require.NoError(t, fresh.db.QueryRow("SELECT recovery_revision FROM run_meta").Scan(&repeated))
			require.Equal(t, epoch, repeated)
			owner, err := fresh.ClaimOwner(t.Context())
			require.NoError(t, err)
			require.Equal(t, o.Slot, owner.Slot)
			require.NotEqual(t, o.ID, owner.ID)
			require.NoError(t, owner.Lease.Close())
			again, err = fresh.BatchStatus(t.Context(), b.BatchID)
			require.NoError(t, err)
			require.Equal(t, after.Recovery, again.Recovery)
			applyErrorCode(t, fresh.Dispatch(t.Context(), children[2].ID, o.ID, time.Now().Add(time.Minute)), "run_unavailable")
		})
	}
}

func TestBatchRecoveryClosesAtApplyAndReceiptRetry(t *testing.T) {
	s, o, b, children, closeStore := recoveryBatchFixture(t, executor.CleanupLinuxDescendants, 2)
	for _, r := range children {
		startRecoveryChild(t, s, o, r)
	}
	closeStore()
	plan, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
	require.NoError(t, err)
	applied, err := applyRecovery(t.Context(), s.path, testKey, applyOptions(plan.Token), nil)
	require.NoError(t, err)
	again, err := applyRecovery(t.Context(), s.path, testKey, applyOptions(plan.Token), nil)
	require.NoError(t, err)
	require.Equal(t, applied.Receipt, again.Receipt)
	empty, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
	require.NoError(t, err)
	require.Equal(t, "no_recovery_needed", empty.Status)
	fresh := attachBatchFixture(t, s)
	status, err := fresh.BatchStatus(t.Context(), b.BatchID)
	require.NoError(t, err)
	require.Equal(t, 2, status.RecoveredCount)
	require.Zero(t, status.CurrentConcurrency)
	require.Equal(t, 2, status.Counts["running"])
	require.Nil(t, status.CompletedAt)
	require.True(t, status.Recovery.OperationallyClosed)
	require.Equal(t, time.Unix(0, applied.Receipt.AppliedAt).UTC(), *status.Recovery.ClosedAt)
	assertRecoveryAccounting(t, fresh)
}

func TestBatchRecoveryDelayedClosureSnapshot(t *testing.T) {
	s, o, b, children, closeStore := recoveryBatchFixture(t, executor.CleanupDarwinObservation, 2)
	startRecoveryChild(t, s, o, children[0])
	closeStore()
	plan, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
	require.NoError(t, err)
	_, err = applyRecovery(t.Context(), s.path, testKey, applyOptions(plan.Token), nil)
	require.NoError(t, err)
	reader, writer := attachBatchFixture(t, s), attachBatchFixture(t, s)
	tx, err := reader.db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	require.NoError(t, err)
	defer tx.Rollback()
	before, _, _, err := batchMetadata(t.Context(), tx, b.BatchID)
	require.NoError(t, err)
	require.NoError(t, writer.Finish(t.Context(), children[1].ID, Completion{Cleanup: children[1].Cleanup, Reason: "batch_stopped"}))
	copies, _, err := batchChildren(t.Context(), tx, b.BatchID)
	require.NoError(t, err)
	require.NoError(t, projectBatchRecovery(t.Context(), tx, &before, copies))
	require.False(t, before.Recovery.OperationallyClosed)
	require.Nil(t, before.Recovery.ClosedAt)
	require.NoError(t, tx.Commit())
	after, err := reader.BatchStatus(t.Context(), b.BatchID)
	require.NoError(t, err)
	require.True(t, after.Recovery.OperationallyClosed)
	require.Nil(t, after.CompletedAt)
	var epoch int64
	require.NoError(t, reader.db.QueryRow("SELECT recovery_revision FROM run_meta").Scan(&epoch))
	require.EqualValues(t, 2, epoch, "apply and later ordinary finalization each advance once")
}

// Reader-state fixture only. Task 11 owns bounded deletion and its quota debit;
// this transaction supplies the exact final-eviction states those readers need.
func evictRecoveryFixture(t *testing.T, s *Store, id string) {
	t.Helper()
	tx, err := sqliteutil.BeginImmediateContext(t.Context(), s.db, "runs")
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.Exec("UPDATE runs SET evicting=1 WHERE id=?", id)
	require.NoError(t, err)
	_, err = tx.Exec("UPDATE run_resolutions SET payload_state='evicting',eviction_cause='quota' WHERE run_id=?", id)
	require.NoError(t, err)
	_, err = tx.Exec("DELETE FROM runs WHERE id=?", id)
	require.NoError(t, err)
	_, err = tx.Exec("UPDATE run_resolutions SET payload_state='evicted',evicted_at=? WHERE run_id=?", time.Now().UnixNano(), id)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
}

func TestBatchRecoveryEvictedChildRequiresExactDurableEvidence(t *testing.T) {
	for _, mode := range []string{"valid evicted", "valid evicting", "missing retained", "missing evicting", "present evicted", "manifest bytes", "digest", "batch binding"} {
		t.Run(mode, func(t *testing.T) {
			s, o, b, children, closeStore := recoveryBatchFixture(t, executor.CleanupLinuxDescendants, 1)
			r := children[0]
			startRecoveryChild(t, s, o, r)
			closeStore()
			plan, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
			require.NoError(t, err)
			_, err = applyRecovery(t.Context(), s.path, testKey, applyOptions(plan.Token), nil)
			require.NoError(t, err)
			fresh := attachBatchFixture(t, s)
			if mode == "present evicted" {
				_, err = fresh.db.Exec("UPDATE run_resolutions SET payload_state='evicted',evicted_at=?,eviction_cause='quota' WHERE run_id=?", time.Now().UnixNano(), r.ID)
				require.NoError(t, err)
			} else if mode == "valid evicting" {
				tx, err := sqliteutil.BeginImmediateContext(t.Context(), fresh.db, "runs")
				require.NoError(t, err)
				_, err = tx.Exec("UPDATE runs SET evicting=1 WHERE id=?", r.ID)
				require.NoError(t, err)
				_, err = tx.Exec("UPDATE run_resolutions SET payload_state='evicting',eviction_cause='quota' WHERE run_id=?", r.ID)
				require.NoError(t, err)
				require.NoError(t, tx.Commit())
			} else {
				evictRecoveryFixture(t, fresh, r.ID)
			}
			switch mode {
			case "missing retained":
				_, err = fresh.db.Exec("UPDATE run_resolutions SET payload_state='retained',evicted_at=NULL,eviction_cause=NULL WHERE run_id=?", r.ID)
			case "missing evicting":
				_, err = fresh.db.Exec("UPDATE run_resolutions SET payload_state='evicting',evicted_at=NULL WHERE run_id=?", r.ID)
			case "manifest bytes":
				_, err = fresh.db.Exec("UPDATE run_batch_children SET result=CAST(json_set(CAST(result AS TEXT),'$.capture.captured_bytes',999) AS BLOB) WHERE run_id=?", r.ID)
			case "digest":
				_, err = fresh.db.Exec("UPDATE run_resolutions SET result_sha256=zeroblob(32) WHERE run_id=?", r.ID)
			case "batch binding":
				_, err = fresh.db.Exec("UPDATE run_resolutions SET batch_id='00000000-0000-0000-0000-000000000001' WHERE run_id=?", r.ID)
			}
			require.NoError(t, err)
			status, err := fresh.BatchStatus(t.Context(), b.BatchID)
			if mode != "valid evicted" && mode != "valid evicting" {
				applyErrorCode(t, err, "recovery_inconsistent")
				_, err = fresh.List(t.Context(), ListOptions{BatchID: b.BatchID, Limit: 1})
				applyErrorCode(t, err, "recovery_inconsistent")
				return
			}
			require.NoError(t, err)
			require.True(t, status.Recovery.OperationallyClosed)
			require.Equal(t, 1, status.RecoveredCount)
			require.Equal(t, executor.CleanupPending, status.Cleanup.State)
			require.Nil(t, status.CompletedAt)
			page, err := fresh.List(t.Context(), ListOptions{BatchID: b.BatchID, Limit: 1})
			require.NoError(t, err)
			require.False(t, page.Entries[0].OutputAvailable)
			require.NotNil(t, page.Entries[0].Recovery)
			require.Equal(t, "running", page.Entries[0].State)
			require.Nil(t, page.Entries[0].ExitCode)
			owner, err := fresh.ClaimOwner(t.Context())
			require.NoError(t, err)
			require.NoError(t, owner.Lease.Close())
			var incidents int
			require.NoError(t, fresh.db.QueryRow("SELECT count(*) FROM run_incidents WHERE receipt_id IS NULL").Scan(&incidents))
			require.Zero(t, incidents)
			require.NoError(t, fresh.closeHandles())
			v, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
			require.NoError(t, err)
			require.Equal(t, "no_recovery_needed", v.Status)
		})
	}
}

func TestBatchRecoveryApplyRollsBackClosureFailure(t *testing.T) {
	s, o, b, children, closeStore := recoveryBatchFixture(t, executor.CleanupLinuxDescendants, 2)
	for _, r := range children {
		startRecoveryChild(t, s, o, r)
	}
	before := applyHistory(t, s.db, children[0].ID)
	_, err := s.db.Exec("CREATE TRIGGER reject_batch_closure BEFORE INSERT ON run_batch_resolutions BEGIN SELECT RAISE(ABORT,'private storage failure'); END")
	require.NoError(t, err)
	closeStore()
	plan, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
	require.NoError(t, err)
	_, err = applyRecovery(t.Context(), s.path, testKey, applyOptions(plan.Token), nil)
	require.Error(t, err)
	fresh := attachBatchFixture(t, s)
	require.Equal(t, before, applyHistory(t, fresh.db, children[0].ID))
	for _, table := range []string{"run_resolutions", "run_recovery_receipts", "run_incident_resolutions", "run_batch_resolutions"} {
		var count int
		require.NoError(t, fresh.db.QueryRow("SELECT count(*) FROM "+table).Scan(&count))
		require.Zero(t, count)
	}
	base, charged, err := batchRecoveryDebit(t.Context(), fresh.db, b.BatchID)
	require.NoError(t, err)
	require.Equal(t, base, charged)
	assertRecoveryAccounting(t, fresh)
}

func TestBatchRecoveryAcrossBoundedReconciliation(t *testing.T) {
	s, o, b, children, closeStore := recoveryBatchFixture(t, executor.CleanupLinuxDescendants, 70)
	startRecoveryChild(t, s, o, children[0])
	closeStore()
	plan, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
	require.NoError(t, err)
	_, err = applyRecovery(t.Context(), s.path, testKey, applyOptions(plan.Token), nil)
	require.NoError(t, err)
	fresh := attachBatchFixture(t, s)
	first, err := fresh.Reconcile(t.Context())
	require.NoError(t, err)
	require.Equal(t, 64, first.RecoveredRuns)
	status, err := fresh.BatchStatus(t.Context(), b.BatchID)
	require.NoError(t, err)
	require.False(t, status.Recovery.OperationallyClosed)
	second, err := fresh.Reconcile(t.Context())
	require.NoError(t, err)
	require.Equal(t, 5, second.RecoveredRuns)
	status, err = fresh.BatchStatus(t.Context(), b.BatchID)
	require.NoError(t, err)
	require.True(t, status.Recovery.OperationallyClosed)
	require.Nil(t, status.CompletedAt)
	require.Equal(t, 69, status.Counts["interrupted"])
	require.Equal(t, 1, status.RecoveredCount)
	assertRecoveryAccounting(t, fresh)
}

func TestBatchRecoveryInconsistentManifestHasNoPartialApply(t *testing.T) {
	s, o, _, children, closeStore := recoveryBatchFixture(t, executor.CleanupLinuxDescendants, 2)
	for _, r := range children {
		startRecoveryChild(t, s, o, r)
	}
	closeStore()
	plan, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
	require.NoError(t, err)
	mutatePlanStore(t, s.path, "UPDATE run_batch_children SET result=CAST(json_set(CAST(result AS TEXT),'$.capture.captured_bytes',999) AS BLOB) WHERE run_id=?", children[1].ID)
	_, err = applyRecovery(t.Context(), s.path, testKey, applyOptions(plan.Token), nil)
	applyErrorCode(t, err, "recovery_inconsistent")
	_, err = PlanRecovery(t.Context(), s.path, testKey, planOptions())
	applyErrorCode(t, err, "recovery_inconsistent")
	fresh := attachBatchFixture(t, s)
	var count int
	require.NoError(t, fresh.db.QueryRow("SELECT count(*) FROM run_resolutions").Scan(&count))
	require.Zero(t, count)
}

func TestBatchRecoveryClosureFailureDoesNotFinalizeSafeSibling(t *testing.T) {
	s, o, b, children, closeStore := recoveryBatchFixture(t, executor.CleanupLinuxDescendants, 2)
	startRecoveryChild(t, s, o, children[0])
	closeStore()
	plan, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
	require.NoError(t, err)
	_, err = applyRecovery(t.Context(), s.path, testKey, applyOptions(plan.Token), nil)
	require.NoError(t, err)
	fresh := attachBatchFixture(t, s)
	before := applyHistory(t, fresh.db, children[1].ID)
	_, err = fresh.db.Exec("CREATE TRIGGER reject_late_closure BEFORE INSERT ON run_batch_resolutions BEGIN SELECT RAISE(ABORT,'private'); END")
	require.NoError(t, err)
	_, err = fresh.Reconcile(t.Context())
	require.Error(t, err)
	applyErrorCode(t, err, "storage_unavailable")
	require.NotContains(t, err.Error(), "private")
	require.Equal(t, before, applyHistory(t, fresh.db, children[1].ID))
	_, err = fresh.db.Exec("DROP TRIGGER reject_late_closure")
	require.NoError(t, err)
	_, err = fresh.Reconcile(t.Context())
	require.NoError(t, err)
	status, err := fresh.BatchStatus(t.Context(), b.BatchID)
	require.NoError(t, err)
	require.True(t, status.Recovery.OperationallyClosed)
	assertRecoveryAccounting(t, fresh)
}

func TestBatchRecoveryAcceptedBacklogIsNotCurrentExecution(t *testing.T) {
	s, o, b, children, _ := recoveryBatchFixture(t, executor.CleanupLinuxDescendants, 1)
	r := children[0]
	startRecoveryChild(t, s, o, r)
	_, err := s.db.Exec("UPDATE runs SET cleanup='retired' WHERE id=?", r.ID)
	require.NoError(t, err)
	_, err = s.db.Exec("UPDATE run_batch_children SET result=CAST(json_set(CAST(result AS TEXT),'$.cleanup.state','retired') AS BLOB) WHERE run_id=?", r.ID)
	require.NoError(t, err)
	status, err := s.BatchStatus(t.Context(), b.BatchID)
	require.NoError(t, err)
	require.Equal(t, 1, status.Counts["running"])
	require.Zero(t, status.CurrentConcurrency)
	require.Nil(t, status.CompletedAt)
}

func TestBatchRecoveryManifestCannotSupplyAdministrativeAuthority(t *testing.T) {
	for _, recovery := range []string{`{"state":"operator_resolved","receipt_id":"00000000-0000-0000-0000-000000000001","operationally_closed":true}`, `"malformed"`, `null`} {
		t.Run(recovery, func(t *testing.T) {
			s, o, b, children, _ := recoveryBatchFixture(t, executor.CleanupLinuxDescendants, 1)
			r := children[0]
			startRecoveryChild(t, s, o, r)
			_, err := s.db.Exec("UPDATE run_batch_children SET result=CAST(json_set(CAST(result AS TEXT),'$.recovery',json(?)) AS BLOB) WHERE run_id=?", recovery, r.ID)
			require.NoError(t, err)
			status, err := s.BatchStatus(t.Context(), b.BatchID)
			require.NoError(t, err)
			require.Nil(t, status.Recovery)
			require.Zero(t, status.RecoveredCount)
			require.Equal(t, 1, status.CurrentConcurrency)
			page, err := s.List(t.Context(), ListOptions{BatchID: b.BatchID, Limit: 1})
			require.NoError(t, err)
			require.Nil(t, page.Entries[0].Recovery)
		})
	}
}

func TestBatchRecoverySchema9UnverifiedAnnotation(t *testing.T) {
	for _, completed := range []bool{false, true} {
		name := "running"
		if completed {
			name = "recorded completion"
		}
		t.Run(name, func(t *testing.T) {
			f := newLegacyFixture(t)
			batch := f.batch(t)
			r := f.run(t, executor.CleanupPending, false, batch, "legacy captured prefix\n")
			if completed {
				now := time.Now().UTC()
				expiry := now.Add(24 * time.Hour)
				r.State, r.CompletedAt, r.ExpiresAt = "interrupted", &now, &expiry
				r.Capture.State, r.Capture.TerminalPersisted = "incomplete", true
				encoded, err := marshalLegacyResult(r)
				require.NoError(t, err)
				_, err = f.db.Exec("UPDATE runs SET result=? WHERE id=?", encoded, *r.RunID)
				require.NoError(t, err)
			}
			f.child(t, r, 0, false)
			identity := f.identity(t)
			f.close(t)
			require.NoError(t, upgradeLegacyStage(t.Context(), f.path, testKey, f.owner, identity))
			f.db = f.connect(t)
			migrationVersions(t, f.db, 9)
			var retained, manifest []byte
			require.NoError(t, f.db.QueryRow("SELECT result FROM runs WHERE id=?", *r.RunID).Scan(&retained))
			require.NoError(t, f.db.QueryRow("SELECT result FROM run_batch_children WHERE run_id=?", *r.RunID).Scan(&manifest))
			var recorded Result
			require.NoError(t, decodeRunResult(retained, executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: executor.CleanupPending}, &recorded))
			// Exact transform persisted by v0.18.2 loadRun -> saveIndexResult.
			markUnverified(&recorded)
			recorded.Error.Message = "last recorded state; owner cleanup is unverified and execution may still exist; stop old execution groups before explicit operator reset"
			recorded.Index = Index{State: "failed", Error: &Error{Code: "index_failed", Message: "legacy index worker failed"}}
			retained, err := json.Marshal(recorded)
			require.NoError(t, err)
			_, err = f.db.Exec("UPDATE runs SET result=? WHERE id=?", retained, *r.RunID)
			require.NoError(t, err)
			_, err = f.db.Exec("UPDATE run_owners SET cleanup_unverified=1; UPDATE run_meta SET admission_blocked=1")
			require.NoError(t, err)
			t.Logf("schema9 retained result: %s", retained)
			t.Logf("schema9 manifest result: %s", manifest)
			f.close(t)
			_, err = Upgrade(t.Context(), f.path, testKey, f.settings)
			require.NoError(t, err)
			view, err := PlanRecovery(t.Context(), f.path, testKey, planOptions())
			require.NoError(t, err)
			// Recognizing the known annotation must not tolerate changed capture.
			mutatePlanStore(t, f.path, "UPDATE run_batch_children SET result=CAST(json_set(CAST(result AS TEXT),'$.capture.captured_bytes',999) AS BLOB) WHERE run_id=?", *r.RunID)
			_, err = PlanRecovery(t.Context(), f.path, testKey, planOptions())
			applyErrorCode(t, err, "recovery_inconsistent")
			mutatePlanStore(t, f.path, "UPDATE run_batch_children SET result=? WHERE run_id=?", manifest, *r.RunID)
			applied, err := applyRecovery(t.Context(), f.path, testKey, applyOptions(view.Token), nil)
			require.NoError(t, err)
			fresh, err := openAttached(t.Context(), f.path, testKey, f.owner, unix.LOCK_SH)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, fresh.closeHandles()) })
			status, err := fresh.BatchStatus(t.Context(), batch)
			require.NoError(t, err)
			require.True(t, status.Recovery.OperationallyClosed)
			require.Nil(t, status.CompletedAt)
			page, err := fresh.List(t.Context(), ListOptions{BatchID: batch, Limit: 1})
			require.NoError(t, err)
			require.Equal(t, applied.Receipt.ID, page.Entries[0].Recovery.ReceiptID)
			var afterRun, afterManifest []byte
			require.NoError(t, fresh.db.QueryRow("SELECT result FROM runs WHERE id=?", *r.RunID).Scan(&afterRun))
			require.NoError(t, fresh.db.QueryRow("SELECT result FROM run_batch_children WHERE run_id=?", *r.RunID).Scan(&afterManifest))
			require.Equal(t, retained, afterRun)
			require.Equal(t, manifest, afterManifest)
			assertRecoveryAccounting(t, fresh)
		})
	}
}
