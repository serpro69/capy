package runstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/executor"
	"github.com/stretchr/testify/require"
)

func retentionStore(t *testing.T, limit int64) (*Store, *Owner) {
	t.Helper()
	path, err := config.ResolveRunDBPath(t.TempDir())
	require.NoError(t, err)
	s, err := OpenWithPolicy(t.Context(), path, testKey, true, PolicyConfig{TTLHours: 24, MaxRunBytes: min(1<<20, limit), MaxRetainedBytes: limit})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	o, err := s.ClaimOwner(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() {
		if _, err := o.Lease.Stat(); err == nil {
			require.NoError(t, o.Lease.Close())
		}
	})
	return s, o
}

func requireErrorCode(t *testing.T, err error, code string, message ...any) {
	t.Helper()
	var typed *Error
	require.True(t, errors.As(err, &typed), "%v %v", err, message)
	require.Equal(t, code, typed.Code, message...)
}

func retentionRun(t *testing.T, s *Store, o *Owner, data string, terminal bool) Admitted {
	t.Helper()
	r, err := s.Admit(t.Context(), o, executor.CleanupLinuxDescendants, Program{})
	require.NoError(t, err)
	require.NoError(t, s.Pending(t.Context(), r.ID, o.ID, executor.CleanupLinuxDescendants))
	require.NoError(t, s.Started(t.Context(), r.ID))
	if data != "" {
		require.NoError(t, s.Append(t.Context(), r.ID, executor.OutputEvent{Sequence: 1, Phase: "run", Stream: "stdout", Data: []byte(data)}))
	}
	if terminal {
		code := 0
		require.NoError(t, s.Finish(t.Context(), r.ID, Completion{Cleanup: executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: executor.CleanupRetired}, Started: true, ExitCode: &code, ObservedBytes: int64(len(data))}))
	}
	return r
}

func accounted(t *testing.T, s *Store) int64 {
	t.Helper()
	var total, sum int64
	require.NoError(t, s.db.QueryRowContext(t.Context(), "SELECT accounted_bytes,index_bytes+(SELECT COALESCE(sum(accounted_bytes),0) FROM runs)+(SELECT COALESCE(sum(accounted_bytes),0) FROM run_batches)+(SELECT COALESCE(sum(charged_bytes),0) FROM run_incidents) FROM run_meta").Scan(&total, &sum))
	require.Equal(t, sum, total, "global debit must equal retained per-run debits plus allocated index pages")
	return total
}

func TestCaptureEvictsForConservativePageGrowth(t *testing.T) {
	s, o := retentionStore(t, 8<<20)
	if s.policy.AccountingMode != "conservative" {
		t.Skip("requires conservative allocated-page accounting")
	}
	old := retentionRun(t, s, o, "old diagnostic marker\n", true)
	active := retentionRun(t, s, o, "", false)
	searchToReady(t, s, o, old.ID, "diagnostic")
	data := []byte(strings.Repeat("x", executor.EventBlockBytes))
	logical := int64(2*len(data) + eventOverhead + projectionOverhead)
	// Leave exactly enough logical room. Once indexing exists, conservative
	// accounting also charges the new payload's allocated SQLite pages.
	s.policy.MaxRetainedBytes = accounted(t, s) + logical
	require.NoError(t, s.Append(t.Context(), active.ID, executor.OutputEvent{
		Sequence: 1, Phase: "run", Stream: "stdout", Data: data,
	}))
	_, err := s.Load(t.Context(), old.ID)
	requireErrorCode(t, err, "run_unavailable")
	page, err := s.ReadRaw(t.Context(), active.ID, "stdout", "", len(data))
	require.NoError(t, err)
	require.Len(t, page.Fragments, 1)
	require.Equal(t, data, page.Fragments[0].Data)
	require.LessOrEqual(t, accounted(t, s), s.policy.MaxRetainedBytes)
}

func TestLiveOwnerUnverifiedCleanupDoesNotExpire(t *testing.T) {
	s, o := retentionStore(t, 8<<20)
	row := retentionRun(t, s, o, "recovery evidence\n", false)
	require.NoError(t, s.Finish(t.Context(), row.ID, Completion{Cleanup: executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: executor.CleanupPending}, Started: true}))
	require.NoError(t, s.Quarantine(t.Context()))
	expireRun(t, s, row.ID)
	// Keep the owner lease held: a scanner cannot publish the registry flag.
	report, err := s.Reconcile(t.Context())
	require.NoError(t, err)
	require.Zero(t, report.UnverifiedSlots)
	loaded, err := s.Load(t.Context(), row.ID)
	require.NoError(t, err)
	require.Nil(t, loaded.Result.ExpiresAt)
	require.Equal(t, "cleanup_unverified", loaded.Result.Error.Code)
	list, err := s.List(t.Context(), ListOptions{Limit: 16})
	require.NoError(t, err)
	require.Len(t, list.Entries, 1)
	require.Equal(t, row.ID, list.Entries[0].RunID)
	page, err := s.ReadRaw(t.Context(), row.ID, "stdout", "", 100)
	require.NoError(t, err)
	require.Equal(t, []byte("recovery evidence\n"), page.Fragments[0].Data)
}

func expireRun(t *testing.T, s *Store, id string) {
	t.Helper()
	r, err := s.Load(t.Context(), id)
	require.NoError(t, err)
	expired := time.Now().Add(-time.Hour)
	r.Result.ExpiresAt = &expired
	b, err := json.Marshal(r.Result)
	require.NoError(t, err)
	_, err = s.db.ExecContext(t.Context(), "UPDATE runs SET result=?,expires_at=? WHERE id=?", b, expired.UnixMilli(), id)
	require.NoError(t, err)
}

func TestQuotaIndependentWritersAndFinalizationReserve(t *testing.T) {
	s, o := retentionStore(t, 2*runReserve+10000)
	other, err := Open(t.Context(), s.path, testKey, false)
	require.NoError(t, err)
	defer other.Close()
	o2, err := other.ClaimOwner(t.Context())
	require.NoError(t, err)
	defer o2.Lease.Close()
	r1 := retentionRun(t, s, o, "", false)
	r2 := retentionRun(t, other, o2, "", false)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	start := make(chan struct{})
	for i, st := range []*Store{s, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			id := []string{r1.ID, r2.ID}[i]
			errs[i] = st.Append(t.Context(), id, executor.OutputEvent{Sequence: 1, Phase: "run", Stream: "stdout", Data: []byte(strings.Repeat("x", 3000) + "\n")})
		}()
	}
	close(start)
	wg.Wait()
	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
		} else {
			requireErrorCode(t, err, "quota_exhausted")
		}
	}
	require.Equal(t, 1, successes)
	require.LessOrEqual(t, accounted(t, s), s.Policy().MaxRetainedBytes)
	_, err = s.Admit(t.Context(), o, executor.CleanupLinuxDescendants, Program{})
	requireErrorCode(t, err, "quota_exhausted")
	for _, r := range []Admitted{r1, r2} {
		loaded, err := s.Load(t.Context(), r.ID)
		require.NoError(t, err, "active runs must not be evicted")
		code := 0
		require.NoError(t, s.CaptureFailed(t.Context(), r.ID, "quota_exhausted"))
		require.NoError(t, s.Finish(t.Context(), r.ID, Completion{Cleanup: executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: executor.CleanupRetired}, Started: true, CaptureLost: true, CaptureReason: "quota_exhausted", ExitCode: &code, ObservedBytes: 3001}))
		page, err := s.ReadRaw(t.Context(), r.ID, "stdout", "", 4096)
		require.NoError(t, err)
		require.Equal(t, loaded.Result.Capture.CapturedBytes, page.Run.Capture.CapturedBytes)
		require.True(t, page.Run.Capture.TerminalPersisted)
	}
}

func TestRetentionExpiryPressureAndImmutableTTL(t *testing.T) {
	s, o := retentionStore(t, 3*runReserve+10000)
	old := retentionRun(t, s, o, "old", true)
	expired := retentionRun(t, s, o, "expired", true)
	active := retentionRun(t, s, o, "active", false)
	loaded, err := s.Load(t.Context(), old.ID)
	require.NoError(t, err)
	for range 3 {
		page, err := s.ReadProjection(t.Context(), old.ID, "stdout", "", 100)
		require.NoError(t, err)
		require.Equal(t, loaded.Result.ExpiresAt, page.Run.ExpiresAt)
	}
	expireRun(t, s, expired.ID)
	_, err = s.Load(t.Context(), expired.ID)
	requireErrorCode(t, err, "run_unavailable")
	_, err = s.ReadRaw(t.Context(), expired.ID, "stdout", "", 100)
	requireErrorCode(t, err, "run_unavailable")
	_, err = s.ReadProjection(t.Context(), expired.ID, "stdout", "", 100)
	requireErrorCode(t, err, "run_unavailable")
	list, err := s.List(t.Context(), ListOptions{Limit: 64})
	require.NoError(t, err)
	require.Len(t, list.Entries, 2)
	newer := retentionRun(t, s, o, "newer", true)
	_, err = s.Load(t.Context(), expired.ID)
	requireErrorCode(t, err, "run_unavailable")
	_, err = s.Load(t.Context(), old.ID)
	require.NoError(t, err, "expired run must be evicted before older unexpired run")
	retentionRun(t, s, o, "last", true)
	_, err = s.Load(t.Context(), old.ID)
	requireErrorCode(t, err, "run_unavailable")
	for _, id := range []string{active.ID, newer.ID} {
		_, err = s.Load(t.Context(), id)
		require.NoError(t, err)
	}
	require.LessOrEqual(t, accounted(t, s), s.Policy().MaxRetainedBytes)
}

func TestLowerQuotaMaintenanceAndIdleReopen(t *testing.T) {
	s, o := retentionStore(t, 4*runReserve+10000)
	first := retentionRun(t, s, o, "first", true)
	second := retentionRun(t, s, o, "second", true)
	third := retentionRun(t, s, o, "third", true)
	expireRun(t, s, third.ID)
	// Stop the worker and close raw handles to model an idle process exit
	// without shutdown maintenance; ordinary reopen must enforce overdue TTL.
	s.maintenanceCancel()
	<-s.maintenanceDone
	require.NoError(t, o.Lease.Close())
	require.NoError(t, s.closeHandles())
	s.closeOnce.Do(func() {})
	settings := PolicyConfig{TTLHours: 1, MaxRunBytes: 100, MaxRetainedBytes: runReserve + 10000}
	updated, err := OpenWithPolicy(t.Context(), s.path, testKey, false, settings)
	require.NoError(t, err)
	defer updated.Close()
	_, err = updated.Load(t.Context(), first.ID)
	requireErrorCode(t, err, "run_unavailable")
	_, err = updated.Load(t.Context(), third.ID)
	requireErrorCode(t, err, "run_unavailable")
	kept, err := updated.Load(t.Context(), second.ID)
	require.NoError(t, err)
	require.Equal(t, 24*time.Hour, kept.Result.ExpiresAt.Sub(*kept.Result.CompletedAt))
	require.LessOrEqual(t, accounted(t, updated), settings.MaxRetainedBytes)
}

func TestKeylessResetPreservesLocksAndInvalidatesCursors(t *testing.T) {
	s, o := retentionStore(t, 1<<20)
	r := retentionRun(t, s, o, "recoverable", true)
	retentionRun(t, s, o, "second", true)
	page, err := s.ReadRaw(t.Context(), r.ID, "stdout", "", 1)
	require.NoError(t, err)
	list, err := s.List(t.Context(), ListOptions{Limit: 1})
	require.NoError(t, err)
	root := filepath.Dir(s.path)
	keep := filepath.Join(root, "unrelated")
	require.NoError(t, os.WriteFile(keep, []byte("keep"), 0600))
	before, err := os.Stat(filepath.Join(root, "lifecycle.lock"))
	require.NoError(t, err)
	requireErrorCode(t, Reset(t.Context(), s.path), "storage_busy")
	require.NoError(t, o.Lease.Close())
	requireErrorCode(t, Reset(t.Context(), s.path), "storage_busy", "read handle still owns lifecycle")
	require.NoError(t, s.Close())
	require.NoError(t, Reset(t.Context(), s.path))
	require.NoFileExists(t, s.path)
	require.FileExists(t, keep)
	after, err := os.Stat(filepath.Join(root, "lifecycle.lock"))
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after))
	entries, err := os.ReadDir(filepath.Join(root, "locks"))
	require.NoError(t, err)
	require.Len(t, entries, OwnerSlots)
	next, err := Open(t.Context(), s.path, "new-synthetic-key", true)
	require.NoError(t, err)
	defer next.Close()
	require.NotEqual(t, s.ID(), next.ID())
	_, err = next.ReadRaw(t.Context(), r.ID, "stdout", *page.NextCursor, 100)
	requireErrorCode(t, err, "cursor_stale")
	_, err = next.List(t.Context(), ListOptions{Limit: 1, Cursor: *list.NextCursor})
	requireErrorCode(t, err, "cursor_stale")
}

func TestResetRejectsUnsafePathsBeforeDeleting(t *testing.T) {
	for _, target := range []string{"runs.db-wal", "recovery.lock", "locks/owner-63.lock"} {
		t.Run(target, func(t *testing.T) {
			s, o := retentionStore(t, 1<<20)
			require.NoError(t, o.Lease.Close())
			require.NoError(t, s.Close())
			name := filepath.Join(filepath.Dir(s.path), target)
			if err := os.Remove(name); err != nil {
				require.True(t, os.IsNotExist(err))
			}
			destination := filepath.Join(t.TempDir(), "untouched")
			require.NoError(t, os.WriteFile(destination, []byte("private"), 0600))
			require.NoError(t, os.Symlink(destination, name))
			require.Error(t, Reset(t.Context(), s.path))
			require.FileExists(t, s.path)
			b, err := os.ReadFile(destination)
			require.NoError(t, err)
			require.Equal(t, "private", string(b))
		})
	}
}

func TestMaintenanceBoundsAndBusyWAL(t *testing.T) {
	s, o := retentionStore(t, 8<<20)
	for range retentionBatch + 1 {
		r := retentionRun(t, s, o, "payload", true)
		expireRun(t, s, r.ID)
		// Admission normally clears prior expiry. Move expiry only after all
		// records exist below so the maintenance pass has a real backlog.
		_, err := s.db.ExecContext(t.Context(), "UPDATE runs SET expires_at=?", time.Now().Add(time.Hour).UnixMilli())
		require.NoError(t, err)
	}
	_, err := s.db.ExecContext(t.Context(), "UPDATE runs SET expires_at=?", time.Now().Add(-time.Hour).UnixMilli())
	require.NoError(t, err)
	other, err := Open(t.Context(), s.path, testKey, false)
	require.NoError(t, err)
	defer other.Close()
	// Open itself clears one bounded batch, leaving exactly one for this pass.
	var count int
	require.NoError(t, s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM runs").Scan(&count))
	require.Equal(t, 1, count)
	tx, err := other.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer tx.Rollback()
	require.NoError(t, tx.QueryRowContext(t.Context(), "SELECT count(*) FROM runs").Scan(&count))
	report, err := s.Maintain(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, report.Expired)
	require.Zero(t, accounted(t, s))
	// The concurrent reader's snapshot is still valid, with WAL data intact.
	require.NoError(t, tx.QueryRowContext(t.Context(), "SELECT count(*) FROM runs").Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, tx.Rollback())
}

func TestPeriodicAndShutdownRetentionReclaimsPages(t *testing.T) {
	s, o := retentionStore(t, 8<<20)
	r := retentionRun(t, s, o, strings.Repeat("x", 16000), true)
	expireRun(t, s, r.ID)
	s.maintenanceCancel()
	<-s.maintenanceDone
	ctx, cancel := context.WithCancel(t.Context())
	s.maintenanceCancel, s.maintenanceDone = cancel, make(chan struct{})
	ticks := make(chan time.Time, 1)
	go s.maintenance(ctx, ticks)
	ticks <- time.Now()
	require.Eventually(t, func() bool {
		var count int
		return s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM runs").Scan(&count) == nil && count == 0
	}, 3*time.Second, time.Millisecond)
	second := retentionRun(t, s, o, strings.Repeat("y", 16000), true)
	expireRun(t, s, second.ID)
	var pagesBefore int
	require.NoError(t, s.db.QueryRowContext(t.Context(), "PRAGMA page_count").Scan(&pagesBefore))
	require.NoError(t, o.Lease.Close())
	require.NoError(t, s.Close())
	reopened, err := Open(t.Context(), s.path, testKey, false)
	require.NoError(t, err)
	defer reopened.Close()
	require.Zero(t, accounted(t, reopened))
	var pagesAfter int
	require.NoError(t, reopened.db.QueryRowContext(t.Context(), "PRAGMA page_count").Scan(&pagesAfter))
	require.Less(t, pagesAfter, pagesBefore)
}

func TestLargeAdmissionMakesProgressAcrossEvictionBatches(t *testing.T) {
	s, o := retentionStore(t, 7<<20)
	for range 100 {
		retentionRun(t, s, o, "", true)
	}
	before := accounted(t, s)
	// Valid <=1MiB shell source; JSON encodes each control byte as six bytes.
	code := "#" + strings.Repeat("<", (1<<20)-1)
	r, err := s.Admit(t.Context(), o, executor.CleanupLinuxDescendants, Program{Request: executor.ExecRequest{Language: executor.Shell, Code: code}})
	require.NoError(t, err)
	loaded, err := s.Load(t.Context(), r.ID)
	require.NoError(t, err)
	require.Equal(t, code, loaded.Program.Request.Code)
	var retained int
	require.NoError(t, s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM runs WHERE terminal=1").Scan(&retained))
	require.Less(t, retained, 100-retentionBatch)
	require.LessOrEqual(t, accounted(t, s), s.Policy().MaxRetainedBytes)
	require.Greater(t, before, int64(6<<20))
}

func TestLargeEvictionPersistsAfterAppendRetryFails(t *testing.T) {
	const limit = 1 << 20
	s, o := retentionStore(t, limit)
	victim := retentionRun(t, s, o, strings.Repeat("\xffz", 3000), true)
	used := accounted(t, s)
	filler := limit - used - runReserve - 4096
	require.Positive(t, filler)
	r, err := s.Admit(t.Context(), o, executor.CleanupLinuxDescendants, Program{Request: executor.ExecRequest{Code: strings.Repeat("x", int(filler))}})
	require.NoError(t, err)
	require.NoError(t, s.Pending(t.Context(), r.ID, o.ID, executor.CleanupLinuxDescendants))
	require.NoError(t, s.Started(t.Context(), r.ID))
	before := accounted(t, s)
	// First tentative append succeeds. After committing a partial eviction,
	// force the retried append to fail, proving that rollback cannot erase the
	// only evidence that periodic maintenance should continue this victim.
	_, err = s.db.ExecContext(t.Context(), `CREATE TRIGGER fail_capture_retry BEFORE INSERT ON run_events
WHEN EXISTS(SELECT 1 FROM runs WHERE evicting=1)
BEGIN SELECT RAISE(ABORT,'injected retry failure'); END`)
	require.NoError(t, err)
	err = s.Append(t.Context(), r.ID, executor.OutputEvent{Sequence: 1, Phase: "run", Stream: "stdout", Data: []byte(strings.Repeat("a", 8192))})
	require.Error(t, err)
	_, err = s.Load(t.Context(), victim.ID)
	requireErrorCode(t, err, "run_unavailable")
	require.Equal(t, before, accounted(t, s), "partial deletion retains its full debit")
	page, err := s.ReadRaw(t.Context(), r.ID, "stdout", "", 100)
	require.NoError(t, err)
	require.Empty(t, page.Fragments, "tentative append must leave no raw/projection holes")
	var count int
	require.NoError(t, s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM run_projection WHERE run_id=?", victim.ID).Scan(&count))
	require.Greater(t, count, evictionRows)
	for range 40 {
		report, err := s.Maintain(t.Context())
		require.NoError(t, err)
		require.LessOrEqual(t, report.DeletedRows, int64(2*evictionRows))
		if !report.PendingDeletion {
			break
		}
	}
	require.NoError(t, s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM runs WHERE id=?", victim.ID).Scan(&count))
	require.Zero(t, count, "maintenance must finish unexpired victim even though usage is below quota")
	require.Less(t, accounted(t, s), before)
	require.NoError(t, s.CaptureFailed(t.Context(), r.ID, "append_failed"))
	zero := 0
	require.NoError(t, s.Finish(t.Context(), r.ID, Completion{Cleanup: executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: executor.CleanupRetired}, Started: true, CaptureLost: true, CaptureReason: "append_failed", ExitCode: &zero, ObservedBytes: 8192}))
}
