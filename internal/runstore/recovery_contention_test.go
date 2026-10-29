package runstore

import (
	"testing"
	"time"

	"github.com/serpro69/capy/internal/executor"
	"github.com/stretchr/testify/require"
)

// Exercise the previously recorded Task 8 writer-lock concern with a maximum
// sized batch of retained dispositions and an independent capture connection.
func TestRecoveryRetainedDispositionContention(t *testing.T) {
	t.Run("owner claim", func(t *testing.T) { testRecoveryDispositionContention(t, false) })
	t.Run("legacy marker", func(t *testing.T) { testRecoveryDispositionContention(t, true) })
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
		requireErrorCode(t, claimed.err, "cleanup_unverified")
		var n int
		require.NoError(t, fresh.db.QueryRow("SELECT count(*) FROM run_incidents WHERE subject_kind='owner' AND receipt_id IS NULL").Scan(&n))
		require.Equal(t, 1, n, "legacy publication was actually exercised")
	} else {
		require.NoError(t, claimed.err)
	}
	require.NoError(t, appendErr)
	require.Less(t, claimed.elapsed, recoveryTimeout)
	// Append enforces its production 250-ms deadline, including lock wait.
}
