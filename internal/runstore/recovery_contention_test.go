package runstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/sqliteutil"
	"github.com/stretchr/testify/require"
)

// Exercise the previously recorded Task 8 writer-lock concern with a maximum
// sized batch of retained dispositions and an independent capture connection.
func TestRecoveryRetainedDispositionContention(t *testing.T) {
	t.Run("owner claim", func(t *testing.T) { testRecoveryDispositionContention(t, false) })
	t.Run("legacy marker", func(t *testing.T) { testRecoveryDispositionContention(t, true) })
}

func TestOwnerPublicationWaitsForWriter(t *testing.T) {
	s, old, closeStore := planFixture(t)
	closeStore()
	claimant := attachBatchFixture(t, s)
	peer := attachBatchFixture(t, s)
	writer, err := sqliteutil.BeginImmediateContext(t.Context(), peer.db, "runs")
	require.NoError(t, err)
	defer writer.Rollback()

	// The run connection's SQLite busy handler gives up after 100 ms. A
	// clean slot needs its first write only when publishing the generation.
	ctx, cancel := context.WithTimeout(t.Context(), 400*time.Millisecond)
	defer cancel()
	owner, err := claimant.ClaimOwner(ctx)
	require.Nil(t, owner)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	var generation string
	require.NoError(t, writer.QueryRow("SELECT generation FROM run_owners WHERE slot=?", old.Slot).Scan(&generation))
	require.Equal(t, old.ID, generation)
	// Give the next caller enough time to publish, but hold the writer
	// through another SQLite busy window. It must succeed in this call.
	released := make(chan error, 1)
	timer := time.AfterFunc(300*time.Millisecond, func() { released <- writer.Rollback() })
	defer timer.Stop()
	owner, err = claimant.ClaimOwner(t.Context())
	require.NoError(t, <-released)
	require.NoError(t, err)
	defer owner.Lease.Close()
	require.Equal(t, old.Slot, owner.Slot)
}

func TestReconcileDisposedSlotWhileWriterHeld(t *testing.T) {
	s, old, closeStore := planFixture(t)
	planPending(t, s, old)
	applyRetentionFixture(t, s, closeStore)
	reader := attachBatchFixture(t, s)
	peer := attachBatchFixture(t, s)
	writer, err := sqliteutil.BeginImmediateContext(t.Context(), peer.db, "runs")
	require.NoError(t, err)
	defer writer.Rollback()

	report, err := reader.Reconcile(t.Context())
	require.NoError(t, err, "validating an already disposed slot only needs a read snapshot")
	require.Equal(t, RecoveryReport{ScannedSlots: OwnerSlots}, report)
}

func TestTerminalIncidentRefusesClaimWhileWriterHeld(t *testing.T) {
	s, old, batch, children, closeStore := recoveryBatchFixture(t, executor.CleanupLinuxDescendants, 1)
	child := children[0]
	startRecoveryChild(t, s, old, child)
	require.NoError(t, s.QuarantineRun(t.Context(), RuntimeIncident{
		RunID: child.ID, BatchID: batch.BatchID, Generation: old.ID, Slot: old.Slot,
		Cause: IncidentSupervisorLost, Stage: StageHelperWait,
	}))
	finishRecoveryChild(t, s, child, 0)
	closeStore()

	claimant := attachBatchFixture(t, s)
	peer := attachBatchFixture(t, s)
	writer, err := sqliteutil.BeginImmediateContext(t.Context(), peer.db, "runs")
	require.NoError(t, err)
	defer writer.Rollback()
	_, err = claimant.ClaimOwner(t.Context())
	requireErrorCode(t, err, "cleanup_unverified")
}

func TestRecoveryValidationRechecksAuthority(t *testing.T) {
	for _, changedEpoch := range []bool{false, true} {
		name := "new registry operation"
		if changedEpoch {
			name = "changed epoch"
		}
		t.Run(name, func(t *testing.T) {
			s, old, closeStore := planFixture(t)
			r := planPending(t, s, old)
			applyRetentionFixture(t, s, closeStore)
			fresh := attachBatchFixture(t, s)
			if !changedEpoch {
				owner, err := fresh.ClaimOwner(t.Context())
				require.NoError(t, err)
				require.NoError(t, owner.Lease.Close())
			}
			registry, err := fresh.recoveryLease(t.Context())
			require.NoError(t, err)
			defer registry.Close()
			validated := &recoveryValidation{}
			_, err = validated.snapshot(t.Context(), fresh, old.Slot)
			require.NoError(t, err)
			// Deliberately corrupt an immutable row to make a fresh validation
			// observable. No production writer is allowed to make this change.
			_, err = fresh.db.Exec("UPDATE runs SET result=? WHERE id=?", []byte("invalid"), r.ID)
			require.NoError(t, err)
			if changedEpoch {
				_, err = fresh.db.Exec("UPDATE run_meta SET recovery_revision=recovery_revision+1")
				require.NoError(t, err)
				_, err = validated.snapshot(t.Context(), fresh, old.Slot)
				requireErrorCode(t, err, "recovery_inconsistent")
			} else {
				require.NoError(t, registry.Close())
				_, err = fresh.ClaimOwner(t.Context())
				requireErrorCode(t, err, "recovery_inconsistent")
			}
		})
	}
}

func testRecoveryDispositionContention(t *testing.T, legacy bool) {
	t.Helper()
	s, old, _, children, closeStore := recoveryBatchFixture(t, executor.CleanupLinuxDescendants, 256)
	for _, r := range children {
		startRecoveryChild(t, s, old, r)
	}
	applyRetentionFixture(t, s, closeStore)
	fresh := attachBatchFixture(t, s)
	// Keep the historical slot unavailable while establishing a healthy writer
	// in a different slot, then release it for the measured owner claim.
	lease, err := fresh.tryOwnerLease(old.Slot)
	require.NoError(t, err)
	require.NotNil(t, lease)
	defer lease.Close()
	writer, err := fresh.ClaimOwner(t.Context())
	require.NoError(t, err)
	defer writer.Lease.Close()
	r := planPending(t, fresh, writer)
	require.NoError(t, lease.Close())
	if legacy {
		_, err := fresh.db.Exec("UPDATE run_owners SET cleanup_unverified=1 WHERE slot=?", old.Slot)
		require.NoError(t, err)
	}
	claimant := attachBatchFixture(t, s)
	type result struct {
		owner   *Owner
		elapsed time.Duration
		err     error
	}
	started, done := make(chan struct{}), make(chan result, 1)
	go func() {
		start := time.Now()
		close(started)
		o, err := claimant.ClaimOwner(t.Context())
		done <- result{o, time.Since(start), err}
	}()
	<-started
	var worst time.Duration
	var appendErr error
	var claimed result
	finished := false
	writes := 0
	for seq := uint64(2); seq < 2002; seq++ {
		select {
		case claimed = <-done:
			finished = true
		default:
		}
		if finished {
			break
		}
		start := time.Now()
		appendErr = fresh.Append(t.Context(), r.ID, executor.OutputEvent{Sequence: seq, Phase: "run", Stream: "stdout", Data: []byte("concurrent capture\n")})
		writes++
		worst = max(worst, time.Since(start))
		if appendErr != nil {
			break
		}
	}
	if !finished {
		claimed = <-done
	}
	if claimed.owner != nil {
		defer claimed.owner.Lease.Close()
	}
	t.Logf("256 retained dispositions: claim=%s, worst capture append=%s, writes=%d", claimed.elapsed, worst, writes)
	if legacy {
		requireErrorCode(t, claimed.err, "cleanup_unverified", "claim cause: %v", errors.Unwrap(claimed.err))
		var n int
		require.NoError(t, fresh.db.QueryRow("SELECT count(*) FROM run_incidents WHERE subject_kind='owner' AND receipt_id IS NULL").Scan(&n))
		require.Equal(t, 1, n, "legacy publication was actually exercised")
	} else {
		require.NoError(t, claimed.err, "claim cause: %v", errors.Unwrap(claimed.err))
	}
	require.NoError(t, appendErr)
	require.Positive(t, writes, "the capture writer must participate in the contention test")
	require.Less(t, claimed.elapsed, recoveryTimeout)
	// Append enforces its production 250-ms deadline, including lock wait.
}
