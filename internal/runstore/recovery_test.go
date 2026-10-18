package runstore

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/executor"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func recoveryStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(name, root)
	}
	t.Setenv("CAPY_DB_KEY", testKey)
	t.Setenv("CAPY_VAULT_KEY", "synthetic-vault-key")
	path, err := config.ResolveRunDBPath(root)
	require.NoError(t, err)
	st, err := OpenWithPolicy(t.Context(), path, testKey, true, PolicyConfig{TTLHours: 7, MaxRunBytes: 1 << 20, MaxRetainedBytes: 512 << 20})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	return st
}

func admitRecovery(t *testing.T, st *Store, owner *Owner) Admitted {
	t.Helper()
	row, err := st.Admit(t.Context(), owner, Program{TTLHours: 7, MaxRunBytes: 1 << 20})
	require.NoError(t, err)
	return row
}

func TestRecoveryScansHighSlotsOnOpen(t *testing.T) {
	st := recoveryStore(t)
	owners := make([]*Owner, OwnerSlots)
	for i := range owners {
		var err error
		owners[i], err = st.ClaimOwner(t.Context())
		require.NoError(t, err)
		t.Cleanup(func() { owners[i].Lease.Close() })
		require.Equal(t, i, owners[i].Slot)
	}
	_, err := st.ClaimOwner(t.Context())
	require.ErrorContains(t, err, "all run owner slots")
	high := owners[OwnerSlots-1]
	neverStarted := admitRecovery(t, st, high)
	retired := admitRecovery(t, st, high)
	require.NoError(t, st.Pending(t.Context(), retired.ID, high.ID))
	require.NoError(t, st.Started(t.Context(), retired.ID))
	data := []byte("visible!\npassword=unfinished")
	require.NoError(t, st.Append(t.Context(), retired.ID, executor.OutputEvent{Sequence: 1, Phase: "run", Stream: "stdout", Data: data}))
	// Model a durable retirement proof without terminal metadata; recovery
	// must preserve the exact raw prefix and finalize pending redaction safely.
	_, err = st.db.ExecContext(t.Context(), "UPDATE runs SET cleanup='retired' WHERE id=?", retired.ID)
	require.NoError(t, err)
	require.NoError(t, high.Lease.Close())
	before := time.Now()
	reopened, err := Open(t.Context(), st.path, testKey, false)
	require.NoError(t, err)
	defer reopened.Close()
	for _, row := range []Admitted{neverStarted, retired} {
		got, err := reopened.Load(t.Context(), row.ID)
		require.NoError(t, err)
		require.Equal(t, "interrupted", got.Result.State)
		require.Equal(t, "owner_lost", *got.Result.Reason)
		require.Nil(t, got.Result.ExitCode)
		require.Nil(t, got.Result.Signal)
		require.True(t, got.Result.Capture.TerminalPersisted)
		require.True(t, got.Result.CompletedAt.After(before))
		require.Equal(t, 7*time.Hour, got.Result.ExpiresAt.Sub(*got.Result.CompletedAt))
	}
	got, err := reopened.ReadRaw(t.Context(), retired.ID, "stdout", "", 4096)
	require.NoError(t, err)
	require.Equal(t, data, got.Fragments[0].Data)
	require.Equal(t, "incomplete", got.Run.Capture.State)
	page, err := reopened.ReadProjection(t.Context(), retired.ID, "stdout", "", 4096)
	require.NoError(t, err)
	encoded, err := json.Marshal(page)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "unfinished")
	first, err := reopened.Load(t.Context(), neverStarted.ID)
	require.NoError(t, err)
	report, err := reopened.Reconcile(t.Context())
	require.NoError(t, err)
	require.Equal(t, OwnerSlots, report.ScannedSlots)
	require.Zero(t, report.RecoveredRuns)
	again, err := reopened.Load(t.Context(), neverStarted.ID)
	require.NoError(t, err)
	require.Equal(t, first.Result, again.Result, "recovery must not renew TTL or alter its first result")
	// Lower slots stay live and must not be rebound or rewritten.
	for _, owner := range owners[:OwnerSlots-1] {
		var generation string
		require.NoError(t, reopened.db.QueryRowContext(t.Context(), "SELECT generation FROM run_owners WHERE slot=?", owner.Slot).Scan(&generation))
		require.Equal(t, owner.ID, generation)
	}
}

func TestRecoveryQuarantinesUnknownCleanupWithoutExpiringEvidence(t *testing.T) {
	st := recoveryStore(t)
	owner, err := st.ClaimOwner(t.Context())
	require.NoError(t, err)
	row := admitRecovery(t, st, owner)
	require.NoError(t, st.Pending(t.Context(), row.ID, owner.ID))
	require.NoError(t, st.Started(t.Context(), row.ID))
	require.NoError(t, st.Append(t.Context(), row.ID, executor.OutputEvent{Sequence: 1, Phase: "run", Stream: "stdout", Data: []byte("retained-prefix")}))
	// An unverified terminal write must not hide its proof at its old expiry.
	_, err = st.db.ExecContext(t.Context(), `UPDATE runs SET result=json_set(CAST(result AS TEXT),'$.expires_at','2000-01-01T00:00:00Z') WHERE id=?`, row.ID)
	require.NoError(t, err)
	var before []byte
	require.NoError(t, st.db.QueryRowContext(t.Context(), "SELECT result FROM runs WHERE id=?", row.ID).Scan(&before))
	require.NoError(t, owner.Lease.Close())
	report, err := st.Reconcile(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, report.UnverifiedSlots)
	_, err = st.ClaimOwner(t.Context())
	require.ErrorContains(t, err, "unverified cleanup")
	got, err := st.Load(t.Context(), row.ID)
	require.NoError(t, err)
	require.Equal(t, "cleanup_unverified", got.Result.Error.Code)
	require.Equal(t, "pending", got.Cleanup)
	require.Nil(t, got.Result.ExpiresAt)
	require.Equal(t, "incomplete", got.Result.Capture.State)
	page, err := st.ReadRaw(t.Context(), row.ID, "stdout", "", 4096)
	require.NoError(t, err)
	require.Equal(t, []byte("retained-prefix"), page.Fragments[0].Data)
	listed, err := st.List(t.Context(), ListOptions{Limit: 16})
	require.NoError(t, err)
	require.Len(t, listed.Entries, 1)
	require.Equal(t, "cleanup_unverified", listed.Entries[0].Error.Code)
	var after []byte
	require.NoError(t, st.db.QueryRowContext(t.Context(), "SELECT result FROM runs WHERE id=?", row.ID).Scan(&after))
	require.Equal(t, before, after, "unknown proof/result evidence is never rewritten")
}

func TestRecoveryBoundsBatchesAndRunsPeriodically(t *testing.T) {
	st := recoveryStore(t)
	owner, err := st.ClaimOwner(t.Context())
	require.NoError(t, err)
	for range recoveryBatch + 1 {
		admitRecovery(t, st, owner)
	}
	require.NoError(t, owner.Lease.Close())
	report, err := st.Reconcile(t.Context())
	require.NoError(t, err)
	require.Equal(t, OwnerSlots, report.ScannedSlots)
	require.Equal(t, recoveryBatch, report.RecoveredRuns)
	require.Equal(t, 1, report.DeferredSlots)
	// Drive the production maintenance loop with a controlled tick, without
	// waiting a minute or relying on a query/admission to trigger recovery.
	st.maintenanceCancel()
	<-st.maintenanceDone
	ctx, cancel := context.WithCancel(context.Background())
	st.maintenanceCancel, st.maintenanceDone = cancel, make(chan struct{})
	ticks := make(chan time.Time)
	go st.maintenance(ctx, ticks)
	ticks <- time.Now()
	require.Eventually(t, func() bool {
		var unfinished int
		return st.db.QueryRowContext(t.Context(), "SELECT count(*) FROM runs WHERE terminal=0").Scan(&unfinished) == nil && unfinished == 0
	}, 3*time.Second, time.Millisecond)
	require.NoError(t, st.Close())
	select {
	case <-st.maintenanceDone:
	default:
		t.Fatal("maintenance was not joined")
	}
}

func TestOwnerRegistryHasFixedFilesUnderChurn(t *testing.T) {
	st := recoveryStore(t)
	before := make([]os.FileInfo, OwnerSlots)
	for i := range OwnerSlots {
		var err error
		before[i], err = os.Stat(st.OwnerLeasePath(i))
		require.NoError(t, err)
	}
	for range 1000 {
		owner, err := st.ClaimOwner(t.Context())
		require.NoError(t, err)
		require.Equal(t, 0, owner.Slot)
		require.NoError(t, owner.Lease.Close())
	}
	entries, err := os.ReadDir(filepath.Join(filepath.Dir(st.path), "locks"))
	require.NoError(t, err)
	require.Len(t, entries, OwnerSlots)
	for i := range OwnerSlots {
		after, err := os.Stat(st.OwnerLeasePath(i))
		require.NoError(t, err)
		require.True(t, os.SameFile(before[i], after))
	}
	probe, err := openRegular(filepath.Join(filepath.Dir(st.path), "lifecycle.lock"), false)
	require.NoError(t, err)
	defer probe.Close()
	require.ErrorIs(t, unix.Flock(int(probe.Fd()), unix.LOCK_EX|unix.LOCK_NB), unix.EWOULDBLOCK)
	require.NoError(t, st.Close())
	require.NoError(t, unix.Flock(int(probe.Fd()), unix.LOCK_EX|unix.LOCK_NB))
}

func TestRecoveryReleasesOnlyAbandonedGenerationClaims(t *testing.T) {
	st := recoveryStore(t)
	runner, err := st.ClaimOwner(t.Context())
	require.NoError(t, err)
	defer runner.Lease.Close()
	row := admitRecovery(t, st, runner)
	require.NoError(t, st.Finish(t.Context(), row.ID, Completion{Retired: true}))
	worker, err := st.ClaimOwner(t.Context())
	require.NoError(t, err)
	claimed, err := st.ClaimIndex(t.Context(), worker, row.ID)
	require.NoError(t, err)
	require.True(t, claimed)
	_, err = st.db.ExecContext(t.Context(), `UPDATE runs SET result=json_set(CAST(result AS TEXT),'$.index.coverage_cursor','retained-coverage') WHERE id=?`, row.ID)
	require.NoError(t, err)
	report, err := st.Reconcile(t.Context())
	require.NoError(t, err)
	require.Zero(t, report.ReleasedClaims, "a live worker's claim is protected by its lease")
	require.NoError(t, worker.Lease.Close())
	report, err = st.Reconcile(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, report.ReleasedClaims)
	got, err := st.Load(t.Context(), row.ID)
	require.NoError(t, err)
	require.Equal(t, "index_interrupted", got.Result.Index.Error.Code)
	require.Equal(t, "retained-coverage", *got.Result.Index.CoverageCursor)
	replacement, err := st.ClaimOwner(t.Context())
	require.NoError(t, err)
	defer replacement.Lease.Close()
	require.Equal(t, worker.Slot, replacement.Slot)
	require.NotEqual(t, worker.ID, replacement.ID)
	claimed, err = st.ClaimIndex(t.Context(), replacement, row.ID)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, st.ReleaseIndex(t.Context(), worker, row.ID))
	report, err = st.Reconcile(t.Context())
	require.NoError(t, err)
	require.Zero(t, report.ReleasedClaims)
	claimed, err = st.ClaimIndex(t.Context(), runner, row.ID)
	require.NoError(t, err)
	require.False(t, claimed, "stale release/recovery cannot steal the replacement's claim")
	require.NoError(t, st.ReleaseIndex(t.Context(), replacement, row.ID))
}

func TestRecoveryRefusesMissingOrReplacedLock(t *testing.T) {
	for _, mode := range []string{"missing", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			st := recoveryStore(t)
			require.NoError(t, st.Close())
			lock := st.OwnerLeasePath(OwnerSlots - 1)
			require.NoError(t, os.Remove(lock))
			if mode == "symlink" {
				require.NoError(t, os.Symlink(st.OwnerLeasePath(0), lock))
			}
			reopened, err := Open(t.Context(), st.path, testKey, false)
			require.Error(t, err)
			require.Nil(t, reopened)
			if mode == "missing" {
				require.NoFileExists(t, lock)
			}
		})
	}
}

func TestRecoveryRollsBackFailedGenerationBatch(t *testing.T) {
	st := recoveryStore(t)
	owner, err := st.ClaimOwner(t.Context())
	require.NoError(t, err)
	good := admitRecovery(t, st, owner)
	bad := admitRecovery(t, st, owner)
	if good.ID > bad.ID {
		good, bad = bad, good
	}
	_, err = st.db.ExecContext(t.Context(), `UPDATE runs SET program=json_set(CAST(program AS TEXT),'$.ttl_hours',0) WHERE id=?`, bad.ID)
	require.NoError(t, err)
	require.NoError(t, owner.Lease.Close())
	report, err := st.Reconcile(t.Context())
	require.Error(t, err)
	require.Zero(t, report.RecoveredRuns)
	row, err := st.Load(t.Context(), good.ID)
	require.NoError(t, err)
	require.Equal(t, "admitted", row.Result.State)
	require.Nil(t, row.Result.ExpiresAt)
	// Restore the deliberately invalid fixture so shutdown can complete its
	// recovery pass; production never silently repairs invalid stored policy.
	_, err = st.db.ExecContext(t.Context(), `UPDATE runs SET program=json_set(CAST(program AS TEXT),'$.ttl_hours',7) WHERE id=?`, bad.ID)
	require.NoError(t, err)
}

func TestRecoveryScannerCannotMasqueradeAsLiveOwner(t *testing.T) {
	st := recoveryStore(t)
	other, err := Open(t.Context(), st.path, testKey, false)
	require.NoError(t, err)
	defer other.Close()
	stale, err := st.ClaimOwner(t.Context())
	require.NoError(t, err)
	live, err := other.ClaimOwner(t.Context())
	require.NoError(t, err)
	defer live.Lease.Close()
	row := admitRecovery(t, st, stale)
	require.NoError(t, st.Pending(t.Context(), row.ID, stale.ID))
	require.NoError(t, stale.Lease.Close())
	registry, err := st.recoveryLease(t.Context())
	require.NoError(t, err)
	defer registry.Close()
	scan, err := st.tryOwnerLease(stale.Slot)
	require.NoError(t, err)
	require.NotNil(t, scan)
	defer scan.Close()
	// Scanner A is paused immediately after acquiring the stale slot, before
	// quarantine exists. Scanner/admission B must not treat that flock as a
	// healthy owner, even when B already has an execution generation.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	_, err = other.Admit(ctx, live, Program{TTLHours: 24, MaxRunBytes: 100})
	cancel()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	ctx, cancel = context.WithTimeout(t.Context(), 30*time.Millisecond)
	_, err = other.ClaimOwner(ctx)
	cancel()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	got, err := other.Load(t.Context(), row.ID)
	require.NoError(t, err, "read recovery needs no registry/execution reservation")
	require.Nil(t, got.Result.Error)
	report, err := st.reconcileSlot(t.Context(), stale.Slot)
	require.NoError(t, err)
	require.Equal(t, 1, report.UnverifiedSlots)
	require.NoError(t, scan.Close())
	require.NoError(t, registry.Close())
	_, err = other.Admit(t.Context(), live, Program{TTLHours: 24, MaxRunBytes: 100})
	require.ErrorContains(t, err, "unverified cleanup")
	var count int
	require.NoError(t, st.db.QueryRowContext(t.Context(), "SELECT count(*) FROM runs").Scan(&count))
	require.Equal(t, 1, count)
}

func TestRecoveryMixedProofsAndClaims(t *testing.T) {
	st := recoveryStore(t)
	owner, err := st.ClaimOwner(t.Context())
	require.NoError(t, err)
	unsafe := admitRecovery(t, st, owner)
	safe := admitRecovery(t, st, owner)
	require.NoError(t, st.Pending(t.Context(), unsafe.ID, owner.ID))
	claimed, err := st.ClaimIndex(t.Context(), owner, safe.ID)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, owner.Lease.Close())
	report, err := st.Reconcile(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, report.UnverifiedSlots)
	require.Equal(t, 1, report.RecoveredRuns)
	require.Equal(t, 1, report.ReleasedClaims)
	got, err := st.Load(t.Context(), safe.ID)
	require.NoError(t, err)
	require.Equal(t, "interrupted", got.Result.State)
	require.Nil(t, got.Result.Error)
	require.Equal(t, 7*time.Hour, got.Result.ExpiresAt.Sub(*got.Result.CompletedAt))
	require.Equal(t, "index_interrupted", got.Result.Index.Error.Code)
	bad, err := st.Load(t.Context(), unsafe.ID)
	require.NoError(t, err)
	require.Equal(t, "pending", bad.Cleanup)
	require.Nil(t, bad.Result.ExpiresAt)
	require.Equal(t, "cleanup_unverified", bad.Result.Error.Code)
	_, err = st.ClaimOwner(t.Context())
	require.ErrorContains(t, err, "unverified cleanup")
}

func TestRecoveryCleanSlotProbeSkipsCompletedHistory(t *testing.T) {
	st := recoveryStore(t)
	for range 100 {
		owner, err := st.ClaimOwner(t.Context())
		require.NoError(t, err)
		row := admitRecovery(t, st, owner)
		require.NoError(t, st.Finish(t.Context(), row.ID, Completion{Retired: true}))
		require.NoError(t, owner.Lease.Close())
	}
	rows, err := st.db.QueryContext(t.Context(), "EXPLAIN QUERY PLAN SELECT EXISTS(SELECT 1 FROM runs WHERE slot=? AND terminal=0)", 0)
	require.NoError(t, err)
	defer rows.Close()
	var plans strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
		plans.WriteString(detail)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Contains(t, plans.String(), "runs_unfinished")
	report, err := st.Reconcile(t.Context())
	require.NoError(t, err)
	require.Zero(t, report.RecoveredRuns)
}

func TestMaintenanceFailureReturnsThroughClose(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(name, root)
	}
	path, err := config.ResolveRunDBPath(root)
	require.NoError(t, err)
	st, err := Open(t.Context(), path, testKey, true)
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() }) // the close error is asserted below
	owner, err := st.ClaimOwner(t.Context())
	require.NoError(t, err)
	row := admitRecovery(t, st, owner)
	require.NoError(t, owner.Lease.Close())
	_, err = st.db.ExecContext(t.Context(), `UPDATE runs SET program=json_set(CAST(program AS TEXT),'$.ttl_hours',0) WHERE id=?`, row.ID)
	require.NoError(t, err)
	st.maintenanceCancel()
	<-st.maintenanceDone
	ctx, cancel := context.WithCancel(context.Background())
	st.maintenanceCancel, st.maintenanceDone = cancel, make(chan struct{})
	ticks := make(chan time.Time)
	go st.maintenance(ctx, ticks)
	ticks <- time.Now()
	ticks <- time.Now() // receiving again proves the first failed pass completed
	cancel()
	<-st.maintenanceDone
	require.Error(t, st.maintenanceErr)
	_, err = st.db.ExecContext(t.Context(), `UPDATE runs SET program=json_set(CAST(program AS TEXT),'$.ttl_hours',7) WHERE id=?`, row.ID)
	require.NoError(t, err)
	require.ErrorContains(t, st.Close(), "run storage is unavailable", "later recovery must not hide an asynchronous failure")
}
