package runstore

import (
	"context"
	"database/sql"
	"math"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/sqliteutil"
	"github.com/stretchr/testify/require"
)

func ageBatchClosure(t *testing.T, s *Store, id string) {
	t.Helper()
	_, err := s.db.Exec("UPDATE run_batch_resolutions SET closed_at=closed_at-?,retention_expires_at=retention_expires_at-? WHERE batch_id=?", (48 * time.Hour).Nanoseconds(), (48 * time.Hour).Milliseconds(), id)
	require.NoError(t, err)
}

func pruneFixture(t *testing.T, s *Store, budget int) (int64, bool) {
	t.Helper()
	tx, err := sqliteutil.BeginImmediateContext(t.Context(), s.db, "runs")
	require.NoError(t, err)
	defer tx.Rollback()
	n, more, err := pruneRecovery(t.Context(), tx, budget)
	require.NoError(t, err)
	_, err = refreshIndexDebit(t.Context(), tx)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	assertRecoveryAccounting(t, s)
	return n, more
}

func TestRecoveryGCBatchDependencyOrders(t *testing.T) {
	for _, order := range []string{"output first", "manifest first"} {
		t.Run(order, func(t *testing.T) {
			s, o, b, children, closeStore := recoveryBatchFixture(t, executor.CleanupLinuxDescendants, 1)
			startRecoveryChild(t, s, o, children[0])
			a := applyRetentionFixture(t, s, closeStore)
			fresh := attachBatchFixture(t, s)
			before := applyHistory(t, fresh.db, children[0].ID)
			if order == "output first" {
				ageRetentionFixture(t, fresh, 48*time.Hour)
				report, err := fresh.Maintain(t.Context())
				require.NoError(t, err)
				require.Equal(t, 1, report.Expired)
				status, err := fresh.BatchStatus(t.Context(), b.BatchID)
				require.NoError(t, err)
				require.Equal(t, executor.CleanupPending, status.Cleanup.State)
				require.Nil(t, status.CompletedAt)
				require.Equal(t, 1, status.RecoveredCount)
				_, err = fresh.ReadRaw(t.Context(), children[0].ID, "stdout", "", 100)
				requireErrorCode(t, err, "run_unavailable")
			} else {
				ageBatchClosure(t, fresh, b.BatchID)
				_, err := fresh.BatchStatus(t.Context(), b.BatchID)
				requireErrorCode(t, err, "batch_unavailable")
				_, err = fresh.List(t.Context(), ListOptions{BatchID: b.BatchID, Limit: 1})
				requireErrorCode(t, err, "batch_unavailable")
				report, err := fresh.Maintain(t.Context())
				require.NoError(t, err)
				require.Equal(t, 1, report.Expired)
				require.Equal(t, before, applyHistory(t, fresh.db, children[0].ID))
				_, err = fresh.ReadRaw(t.Context(), children[0].ID, "stdout", "", 100)
				require.NoError(t, err)
			}
			// Neither policy changes nor quota pressure can prune a dependency.
			_, err := fresh.db.Exec("UPDATE run_policy SET ttl_hours=1,revision=revision+1")
			require.NoError(t, err)
			n, _ := pruneFixture(t, fresh, 64)
			require.Zero(t, n)
			receipt, err := readRecoveryReceipt(t.Context(), fresh.db, a.Receipt.ID, "")
			require.NoError(t, err)
			require.False(t, receipt.Pruning)
			if order == "output first" {
				ageBatchClosure(t, fresh, b.BatchID)
			} else {
				ageRetentionFixture(t, fresh, 48*time.Hour)
			}
			_, err = fresh.Maintain(t.Context())
			require.NoError(t, err)
			assertRecoveryAccounting(t, fresh)
			for _, table := range []string{"run_batches", "run_batch_children", "run_batch_resolutions", "runs", "run_resolutions", "run_incidents", "run_incident_resolutions", "run_recovery_receipts"} {
				var n int
				require.NoError(t, fresh.db.QueryRow("SELECT count(*) FROM "+table).Scan(&n))
				require.Zero(t, n, table)
			}
			_, err = fresh.Reconcile(t.Context())
			require.NoError(t, err)
		})
	}
}

func TestRecoveryGCBatchUnclosedAndTerminalClocks(t *testing.T) {
	s, o, b, children, closeStore := recoveryBatchFixture(t, executor.CleanupLinuxDescendants, 2)
	startRecoveryChild(t, s, o, children[0]) // child 1 remains queued/safely unspawned
	applyRetentionFixture(t, s, closeStore)
	fresh := attachBatchFixture(t, s)
	ageRetentionFixture(t, fresh, 48*time.Hour)
	report := retainFixture(t, fresh, 1, true)
	require.True(t, report.More)
	status, err := fresh.BatchStatus(t.Context(), b.BatchID)
	require.NoError(t, err)
	require.False(t, status.Recovery.OperationallyClosed)
	n, _ := pruneFixture(t, fresh, 64)
	require.Zero(t, n)
	_, err = fresh.Reconcile(t.Context())
	require.NoError(t, err)
	status, err = fresh.BatchStatus(t.Context(), b.BatchID)
	require.NoError(t, err)
	require.True(t, status.Recovery.OperationallyClosed)
	deadline := *status.Recovery.RetentionExpiresAt
	_, err = fresh.Reconcile(t.Context())
	require.NoError(t, err)
	status, err = fresh.BatchStatus(t.Context(), b.BatchID)
	require.NoError(t, err)
	require.Equal(t, deadline, *status.Recovery.RetentionExpiresAt)
	require.Nil(t, status.CompletedAt)
	require.Nil(t, status.ExpiresAt)
	// Quota can remove a now-closed manifest, preserving its audit deadline.
	report = retainFixture(t, fresh, 1, false)
	require.Greater(t, report.Evicted, 0)
	assertRecoveryAccounting(t, fresh)
}

func TestRecoveryGCDeadlineAfterEarlyPayloadEviction(t *testing.T) {
	s, o, closeStore := planFixture(t)
	r := planPending(t, s, o)
	a := applyRetentionFixture(t, s, closeStore)
	fresh := attachBatchFixture(t, s)
	retainFixture(t, fresh, 1, false)
	n, _ := pruneFixture(t, fresh, 64)
	require.Zero(t, n, "quota eviction cannot shorten audit TTL")
	d, err := readRunResolution(t.Context(), fresh.db, r.ID)
	require.NoError(t, err)
	require.Equal(t, "evicted", d.PayloadState)
	receipt, err := readRecoveryReceipt(t.Context(), fresh.db, a.Receipt.ID, "")
	require.NoError(t, err)
	require.Equal(t, *a.Receipt, receipt)
}

func TestRecoveryGCBoundedRestartAndOldToken(t *testing.T) {
	s, o, b, children, closeStore := recoveryBatchFixture(t, executor.CleanupLinuxDescendants, 70)
	for _, r := range children {
		startRecoveryChild(t, s, o, r)
	}
	closeStore()
	p, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
	require.NoError(t, err)
	a, err := applyRecovery(t.Context(), s.path, testKey, applyOptions(p.Token), nil)
	require.NoError(t, err)
	fresh := attachBatchFixture(t, s)
	ageRetentionFixture(t, fresh, 48*time.Hour)
	ageBatchClosure(t, fresh, b.BatchID)
	// Payload maintenance commits first; no audit GC in this internal helper.
	for range 4 {
		retainFixture(t, fresh, fresh.policy.MaxRetainedBytes, true)
	}
	view, err := InspectRuns(t.Context(), fresh.path, testKey, InspectionOptions{ReceiptID: a.Receipt.ID, Limit: 1})
	require.NoError(t, err)
	require.NotEmpty(t, view.NextCursor)
	epoch := incidentEpoch(t, fresh)
	report, err := fresh.Maintain(t.Context())
	require.NoError(t, err)
	require.EqualValues(t, 64, report.DeletedRows)
	require.True(t, report.PendingDeletion)
	require.Equal(t, epoch+1, incidentEpoch(t, fresh))
	var remainingID string
	require.NoError(t, fresh.db.QueryRow("SELECT run_id FROM run_resolutions LIMIT 1").Scan(&remainingID))
	_, err = fresh.Load(t.Context(), remainingID)
	requireErrorCode(t, err, "run_unavailable")
	_, err = fresh.ReadRaw(t.Context(), remainingID, "stdout", "", 100)
	requireErrorCode(t, err, "run_unavailable")
	_, err = fresh.ReadProjection(t.Context(), remainingID, "stdout", "", 100)
	requireErrorCode(t, err, "run_unavailable")
	_, err = InspectRuns(t.Context(), fresh.path, testKey, InspectionOptions{ReceiptID: a.Receipt.ID, Cursor: view.NextCursor, Limit: 1})
	requireErrorCode(t, err, "inspection_stale")
	view, err = InspectRuns(t.Context(), fresh.path, testKey, InspectionOptions{ReceiptID: a.Receipt.ID, Limit: 64})
	require.NoError(t, err)
	require.True(t, view.Receipt.Pruning)
	require.Equal(t, "partial", view.Details)
	for _, subject := range view.Subjects {
		require.False(t, subject.EvidenceInconsistent)
	}
	// Restart and retry while some dispositions and all memberships remain.
	require.NoError(t, fresh.closeHandles())
	again, err := applyRecovery(t.Context(), s.path, testKey, applyOptions(p.Token), nil)
	require.NoError(t, err)
	require.True(t, again.Receipt.Pruning)
	require.Equal(t, a.Receipt.ID, again.Receipt.ID)
	fresh = attachBatchFixture(t, s)
	_, err = fresh.Reconcile(t.Context())
	require.NoError(t, err, "partial audit GC cannot re-quarantine historical evidence")
	for range 5 {
		report, err = fresh.Maintain(t.Context())
		require.NoError(t, err)
		require.LessOrEqual(t, report.DeletedRows, int64(retentionBatch))
		assertRecoveryAccounting(t, fresh)
		if !report.PendingDeletion {
			break
		}
	}
	_, err = readRecoveryReceipt(t.Context(), fresh.db, a.Receipt.ID, "")
	requireErrorCode(t, err, "receipt_unavailable")
	require.NoError(t, fresh.closeHandles())
	_, err = applyRecovery(t.Context(), s.path, testKey, applyOptions(p.Token), nil)
	requireErrorCode(t, err, "recovery_plan_stale")
	fresh = attachBatchFixture(t, s)
	var count int
	require.NoError(t, fresh.db.QueryRow("SELECT count(*) FROM run_recovery_receipts").Scan(&count))
	require.Zero(t, count)
}

func TestRecoveryGCAtomicFailure(t *testing.T) {
	for _, fault := range []string{"mark", "delete", "epoch", "cancel"} {
		t.Run(fault, func(t *testing.T) {
			s, o, closeStore := planFixture(t)
			planPending(t, s, o)
			a := applyRetentionFixture(t, s, closeStore)
			fresh := attachBatchFixture(t, s)
			ageRetentionFixture(t, fresh, 48*time.Hour)
			retainFixture(t, fresh, fresh.policy.MaxRetainedBytes, true)
			before := assertRecoveryAccounting(t, fresh)
			ctx := t.Context()
			switch fault {
			case "mark", "delete":
				event := "BEFORE UPDATE OF pruning ON run_recovery_receipts"
				if fault == "delete" {
					event = "BEFORE DELETE ON run_incidents"
				}
				_, err := fresh.db.Exec("CREATE TRIGGER fail_gc " + event + " BEGIN SELECT RAISE(ABORT,'private failure'); END")
				require.NoError(t, err)
			case "epoch":
				_, err := fresh.db.Exec("UPDATE run_meta SET recovery_revision=?", int64(math.MaxInt64))
				require.NoError(t, err)
			case "cancel":
				// Expensive work after the mark and earlier dependency deletions
				// forces cancellation within this transaction, not at attachment.
				_, err := fresh.db.Exec(`CREATE TRIGGER slow_gc BEFORE DELETE ON run_incidents BEGIN
 SELECT sum(x) FROM (WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<100000000) SELECT x FROM n); END`)
				require.NoError(t, err)
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()
			}
			epoch := incidentEpoch(t, fresh)
			_, err := fresh.Maintain(ctx)
			require.Error(t, err)
			if fault == "cancel" {
				require.Contains(t, err.Error(), "maintaining recovery audit")
				require.ErrorIs(t, err, context.DeadlineExceeded)
			}
			require.NotContains(t, err.Error(), "private failure")
			require.Equal(t, before, assertRecoveryAccounting(t, fresh))
			require.Equal(t, epoch, incidentEpoch(t, fresh))
			receipt, err := readRecoveryReceipt(t.Context(), fresh.db, a.Receipt.ID, "")
			require.NoError(t, err)
			require.False(t, receipt.Pruning)
			var n int
			require.NoError(t, fresh.db.QueryRow("SELECT count(*) FROM run_resolutions").Scan(&n))
			require.Equal(t, 1, n)
		})
	}
}

func TestRecoveryGCTerminalBatchOriginalExpiry(t *testing.T) {
	s, o, b, children, closeStore := recoveryBatchFixture(t, executor.CleanupLinuxDescendants, 1)
	r := children[0]
	startRecoveryChild(t, s, o, r)
	require.NoError(t, s.QuarantineRun(t.Context(), RuntimeIncident{RunID: r.ID, BatchID: b.BatchID, Generation: o.ID, Slot: o.Slot, Cause: IncidentSupervisorLost, Stage: StageHelperWait}))
	finishRecoveryChild(t, s, r, 0)
	var completed, expires sql.NullInt64
	require.NoError(t, s.db.QueryRow("SELECT completed_at,expires_at FROM run_batches WHERE id=?", b.BatchID).Scan(&completed, &expires))
	a := applyRetentionFixture(t, s, closeStore)
	require.Zero(t, a.Receipt.RunCount)
	fresh := attachBatchFixture(t, s)
	status, err := fresh.BatchStatus(t.Context(), b.BatchID)
	require.NoError(t, err)
	require.Equal(t, completed.Int64, status.CompletedAt.UnixNano())
	require.Equal(t, expires.Int64, status.ExpiresAt.UnixMilli())
	// Incident-only audit remains pinned even without a run disposition.
	ageRetentionFixture(t, fresh, 48*time.Hour)
	n, _ := pruneFixture(t, fresh, 64)
	require.Zero(t, n)
	retainFixture(t, fresh, 1, false)
	n, more := pruneFixture(t, fresh, 64)
	require.EqualValues(t, 3, n) // membership, incident, root receipt
	require.False(t, more)
}

func TestRecoveryGCAccountingModes(t *testing.T) {
	for _, mode := range []string{"conservative", "dbstat"} {
		t.Run(mode, func(t *testing.T) {
			s, o, b, children, closeStore := recoveryBatchFixture(t, executor.CleanupLinuxDescendants, 1)
			enabled, err := dbstatAvailable(t.Context(), s.db)
			require.NoError(t, err)
			if mode == "dbstat" && !enabled {
				t.Skip("pinned SQLite build does not enable dbstat")
			}
			s.policy.AccountingMode = mode
			_, err = s.db.Exec("UPDATE run_policy SET accounting_mode=?", mode)
			require.NoError(t, err)
			startRecoveryChild(t, s, o, children[0])
			_, err = s.Search(t.Context(), o, SearchOptions{RunID: children[0].ID, Queries: []string{"retained"}, Limit: 1})
			require.NoError(t, err)
			applyRetentionFixture(t, s, closeStore)
			fresh := attachBatchFixture(t, s)
			ageRetentionFixture(t, fresh, 48*time.Hour)
			ageBatchClosure(t, fresh, b.BatchID)
			retainFixture(t, fresh, fresh.policy.MaxRetainedBytes, true)
			// Restart after each individual deletion, including the root last.
			for range 4 {
				before := assertRecoveryAccounting(t, fresh)
				n, _ := pruneFixture(t, fresh, 1)
				require.EqualValues(t, 1, n)
				require.Less(t, assertRecoveryAccounting(t, fresh), before)
				require.NoError(t, fresh.closeHandles())
				fresh = attachBatchFixture(t, s)
			}
			var logical, indexed int64
			require.NoError(t, fresh.db.QueryRow("SELECT accounted_bytes,index_bytes FROM run_meta").Scan(&logical, &indexed))
			require.Equal(t, indexed, logical, "only independently recounted index/schema pages remain")
			n, more := pruneFixture(t, fresh, 1)
			require.Zero(t, n)
			require.False(t, more)
			require.Equal(t, logical, assertRecoveryAccounting(t, fresh))
		})
	}
}

func TestRecoveryGCInvalidatesValidatedOwnerSnapshot(t *testing.T) {
	s, o, closeStore := planFixture(t)
	planPending(t, s, o)
	applyRetentionFixture(t, s, closeStore)
	fresh := attachBatchFixture(t, s)
	ageRetentionFixture(t, fresh, 48*time.Hour)
	retainFixture(t, fresh, fresh.policy.MaxRetainedBytes, true)
	validated := &recoveryValidation{}
	epoch, err := validated.snapshot(t.Context(), fresh, o.Slot)
	require.NoError(t, err)
	// Audit pruning between validation and writer acquisition removes the
	// authority that was validated. The old snapshot cannot authorize mutation.
	pruneFixture(t, fresh, 1)
	tx, err := sqliteutil.BeginImmediateContext(t.Context(), fresh.db, "runs")
	require.NoError(t, err)
	defer tx.Rollback()
	require.ErrorIs(t, recheckRecoveryRevision(t.Context(), tx, epoch), errRecoverySnapshotChanged)
	require.NoError(t, tx.Rollback())
	epoch, err = validated.snapshot(t.Context(), fresh, o.Slot)
	require.NoError(t, err)
	tx, err = sqliteutil.BeginImmediateContext(t.Context(), fresh.db, "runs")
	require.NoError(t, err)
	defer tx.Rollback()
	require.NoError(t, recheckRecoveryRevision(t.Context(), tx, epoch))
}

func TestRecoveryGCRejectsReferencedPruning(t *testing.T) {
	s, o, closeStore := planFixture(t)
	r := planPending(t, s, o)
	a := applyRetentionFixture(t, s, closeStore)
	fresh := attachBatchFixture(t, s)
	// Impossible under normal GC: model damaged audit metadata without hiding
	// retained execution evidence behind a blanket pruning exclusion.
	_, err := fresh.db.Exec("UPDATE run_recovery_receipts SET pruning=1 WHERE id=?", a.Receipt.ID)
	require.NoError(t, err)
	_, err = fresh.Load(t.Context(), r.ID)
	requireErrorCode(t, err, "recovery_inconsistent")
	_, err = fresh.Reconcile(t.Context())
	requireErrorCode(t, err, "recovery_inconsistent")
	view, err := InspectRuns(t.Context(), fresh.path, testKey, InspectionOptions{RunID: r.ID, Limit: 1})
	require.NoError(t, err)
	require.True(t, view.Subjects[0].EvidenceInconsistent)
	before := assertRecoveryAccounting(t, fresh)
	_, err = fresh.Maintain(t.Context())
	requireErrorCode(t, err, "recovery_inconsistent")
	require.Equal(t, before, assertRecoveryAccounting(t, fresh))
}

func TestRecoveryGCRetriesChangedSnapshot(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "owner claim"
		if legacy {
			name = "legacy marker"
		}
		t.Run(name, func(t *testing.T) {
			s, o, closeStore := planFixture(t)
			planPending(t, s, o)
			applyRetentionFixture(t, s, closeStore)
			fresh := attachBatchFixture(t, s)
			peer := attachBatchFixture(t, s)
			ageRetentionFixture(t, fresh, 48*time.Hour)
			retainFixture(t, fresh, fresh.policy.MaxRetainedBytes, true)
			slots := []int{o.Slot}
			if legacy {
				_, err := fresh.db.Exec("UPDATE run_owners SET cleanup_unverified=1 WHERE slot=?", o.Slot)
				require.NoError(t, err)
				slots = make([]int, OwnerSlots)
				for slot := range slots {
					slots[slot] = slot
				}
			}
			registry, err := fresh.recoveryLease(t.Context())
			require.NoError(t, err)
			defer registry.Close()
			attempts := 0
			validated := &recoveryValidation{}
			// Exercise the transaction-acquisition path used by both production
			// callers, with real peer GC precisely between validation and begin.
			tx, err := beginRecoveryWrite(t.Context(), fresh.db, func(ctx context.Context) (int64, error) {
				epoch, err := validated.snapshot(ctx, fresh, slots...)
				if err != nil {
					return 0, err
				}
				attempts++
				if attempts == 1 {
					n, _ := pruneFixture(t, peer, 1)
					require.EqualValues(t, 1, n)
				}
				return epoch, nil
			})
			require.NoError(t, err)
			require.Equal(t, 2, attempts)
			require.NoError(t, tx.Rollback())
			require.NoError(t, registry.Close())
			owner, err := fresh.ClaimOwner(t.Context())
			if legacy {
				// The genuine marker remains authoritative; publication succeeds
				// and refuses cleanup, rather than leaking a transient epoch error.
				requireErrorCode(t, err, "cleanup_unverified")
				var n int
				require.NoError(t, fresh.db.QueryRow("SELECT count(*) FROM run_incidents WHERE subject_kind='owner' AND receipt_id IS NULL").Scan(&n))
				require.Equal(t, 1, n)
			} else {
				require.NoError(t, err)
				require.NoError(t, owner.Lease.Close())
			}
		})
	}
}
