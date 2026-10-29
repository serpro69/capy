package runstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/sqliteutil"
	"github.com/stretchr/testify/require"
)

func applyRetentionFixture(t *testing.T, s *Store, closeStore func()) recoveryApplyResult {
	t.Helper()
	closeStore()
	p, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
	require.NoError(t, err)
	a, err := applyRecovery(t.Context(), s.path, testKey, applyOptions(p.Token), nil)
	require.NoError(t, err)
	return a
}

// Model an older, internally consistent administrative clock without sleeping
// through an admission TTL or changing any original execution evidence.
func ageRetentionFixture(t *testing.T, s *Store, age time.Duration) {
	t.Helper()
	tx, err := sqliteutil.BeginImmediateContext(t.Context(), s.db, "runs")
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.Exec("UPDATE run_recovery_receipts SET applied_at=applied_at-?,audit_expires_at=audit_expires_at-?", age.Nanoseconds(), age.Milliseconds())
	require.NoError(t, err)
	_, err = tx.Exec("UPDATE run_resolutions SET resolved_at=resolved_at-?,retention_expires_at=retention_expires_at-?,audit_expires_at=audit_expires_at-?", age.Nanoseconds(), age.Milliseconds(), age.Milliseconds())
	require.NoError(t, err)
	for _, table := range []string{"run_incidents", "run_incident_resolutions"} {
		_, err = tx.Exec("UPDATE "+table+" SET audit_expires_at=audit_expires_at-?", age.Milliseconds())
		require.NoError(t, err)
	}
	require.NoError(t, tx.Commit())
}

func retainFixture(t *testing.T, s *Store, limit int64, expire bool) RetentionReport {
	t.Helper()
	tx, err := sqliteutil.BeginImmediateContext(t.Context(), s.db, "runs")
	require.NoError(t, err)
	defer tx.Rollback()
	report, err := retain(t.Context(), tx, limit, 0, expire)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	return report
}

func TestRecoveryRetentionFixedClocksAndQuotaOrder(t *testing.T) {
	s, o, closeStore := planFixture(t)
	old := retentionRun(t, s, o, "older completed\n", true)
	r := planPending(t, s, o)
	before, oldBefore := applyHistory(t, s.db, r.ID), applyHistory(t, s.db, old.ID)
	closeStore()
	p, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
	require.NoError(t, err)
	a, err := applyRecovery(t.Context(), s.path, testKey, applyOptions(p.Token), nil)
	require.NoError(t, err)
	again, err := applyRecovery(t.Context(), s.path, testKey, applyOptions(p.Token), nil)
	require.NoError(t, err)
	require.Equal(t, a.Receipt, again.Receipt)
	fresh := attachBatchFixture(t, s)
	d, err := readRunResolution(t.Context(), fresh.db, r.ID)
	require.NoError(t, err)
	require.Equal(t, time.Unix(0, a.Receipt.AppliedAt).Add(time.Duration(r.Program.TTLHours)*time.Hour).UnixMilli()+1, d.ExpiresAt)
	owner, err := fresh.ClaimOwner(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Lease.Close()) })
	newer := retentionRun(t, fresh, owner, "newer completed\n", true)
	// A policy publication changes neither admission-recorded retention clock.
	_, err = fresh.db.Exec("UPDATE run_policy SET ttl_hours=1,revision=revision+1")
	require.NoError(t, err)
	for range 2 {
		_, err := fresh.ReadRaw(t.Context(), r.ID, "stdout", "", 10)
		require.NoError(t, err)
		_, err = fresh.ReadProjection(t.Context(), r.ID, "stdout", "", 10)
		require.NoError(t, err)
	}
	after, err := readRunResolution(t.Context(), fresh.db, r.ID)
	require.NoError(t, err)
	require.Equal(t, d, after)
	require.Equal(t, before, applyHistory(t, fresh.db, r.ID))
	require.Equal(t, oldBefore, applyHistory(t, fresh.db, old.ID))
	// Quota ordering is old completion, recovery, then newer completion.
	report := retainFixture(t, fresh, assertRecoveryAccounting(t, fresh)-1, false)
	require.Equal(t, 1, report.Evicted)
	_, err = fresh.Load(t.Context(), old.ID)
	requireErrorCode(t, err, "run_unavailable")
	_, err = fresh.Load(t.Context(), r.ID)
	require.NoError(t, err)
	report = retainFixture(t, fresh, assertRecoveryAccounting(t, fresh)-1, false)
	require.Equal(t, 1, report.Evicted)
	d, err = readRunResolution(t.Context(), fresh.db, r.ID)
	require.NoError(t, err)
	require.Equal(t, "evicted", d.PayloadState)
	require.Equal(t, "quota", d.EvictionCause)
	require.Equal(t, after.Original, d.Original)
	require.Equal(t, after.ExpiresAt, d.ExpiresAt)
	_, err = fresh.Load(t.Context(), newer.ID)
	require.NoError(t, err)
	assertRecoveryAccounting(t, fresh)
}

func TestRecoveryRetentionBoundedIndexedPayload(t *testing.T) {
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
			r := children[0]
			startRecoveryChild(t, s, o, r)
			seq := uint64(1)
			appendSearchText(t, s, r.ID, "stdout", "\n"+strings.Repeat("diagnostic recovery retained text\n", 6000), &seq)
			// Populate real safe-prefix FTS windows while the original owner is
			// healthy. Disposed indexing itself belongs to Task 13.
			for range 10 {
				_, err := s.Search(t.Context(), o, SearchOptions{RunID: r.ID, Queries: []string{"diagnostic"}, Limit: 1})
				require.NoError(t, err)
			}
			var windows int
			require.NoError(t, s.db.QueryRow("SELECT count(*) FROM run_search_windows").Scan(&windows))
			require.Greater(t, windows, 32)
			a := applyRetentionFixture(t, s, closeStore)
			fresh := attachBatchFixture(t, s)
			ageRetentionFixture(t, fresh, 48*time.Hour)
			d, err := readRunResolution(t.Context(), fresh.db, r.ID)
			require.NoError(t, err)
			var debit int64
			require.NoError(t, fresh.db.QueryRow("SELECT accounted_bytes FROM runs WHERE id=?", r.ID).Scan(&debit))
			assertRecoveryAccounting(t, fresh)
			report, err := fresh.Maintain(t.Context())
			require.NoError(t, err)
			require.True(t, report.PendingDeletion)
			require.LessOrEqual(t, report.DeletedRows, int64(2*evictionRows))
			partial, err := readRunResolution(t.Context(), fresh.db, r.ID)
			require.NoError(t, err)
			require.Equal(t, "evicting", partial.PayloadState)
			require.Equal(t, "ttl", partial.EvictionCause)
			require.False(t, partial.EvictedAt.Valid)
			require.Equal(t, d.Original, partial.Original)
			var charged int64
			require.NoError(t, fresh.db.QueryRow("SELECT accounted_bytes FROM runs WHERE id=?", r.ID).Scan(&charged))
			require.Equal(t, debit, charged, "partial deletion retains the entire logical payload debit")
			assertRecoveryAccounting(t, fresh)
			// Close/reopen after committed progress. No maintenance on attachment;
			// the durable tombstone must be sufficient to finish after restart.
			require.NoError(t, fresh.closeHandles())
			fresh = attachBatchFixture(t, s)
			for range 100 {
				report, err = fresh.Maintain(t.Context())
				require.NoError(t, err)
				require.LessOrEqual(t, report.DeletedRows, int64(2*evictionRows))
				assertRecoveryAccounting(t, fresh)
				if !report.PendingDeletion {
					break
				}
			}
			for _, table := range []string{"runs", "run_events", "run_projection", "run_streams", "run_search_state", "run_search_progress", "run_search_windows", "run_search_fts", "run_search_trigram"} {
				var n int
				require.NoError(t, fresh.db.QueryRow("SELECT count(*) FROM "+table).Scan(&n))
				require.Zero(t, n, table)
			}
			evicted, err := readRunResolution(t.Context(), fresh.db, r.ID)
			require.NoError(t, err)
			require.Equal(t, "evicted", evicted.PayloadState)
			require.GreaterOrEqual(t, evicted.EvictedAt.Int64, d.ResolvedAt)
			require.Equal(t, d.Original, evicted.Original)
			require.Equal(t, d.Digest, evicted.Digest)
			require.Equal(t, executor.CleanupPending, evicted.Cleanup.State)
			page, err := fresh.List(t.Context(), ListOptions{BatchID: b.BatchID, Limit: 64})
			require.NoError(t, err)
			require.Len(t, page.Entries, 1)
			require.False(t, page.Entries[0].OutputAvailable)
			require.Equal(t, a.Receipt.ID, page.Entries[0].Recovery.ReceiptID)
			_, err = fresh.Load(t.Context(), r.ID)
			requireErrorCode(t, err, "run_unavailable")
			require.Contains(t, err.Error(), "capy runs inspect")
			inspection, err := InspectRuns(t.Context(), fresh.path, testKey, InspectionOptions{RunID: r.ID, Limit: 64})
			require.NoError(t, err)
			require.Len(t, inspection.Subjects, 1)
			require.False(t, inspection.Subjects[0].EvidenceInconsistent)
			require.False(t, *inspection.Subjects[0].OutputAvailable)
			require.Equal(t, a.Receipt.ID, inspection.Subjects[0].ReceiptID)
			assertRecoveryAccounting(t, fresh)
		})
	}
}

func TestRecoveryRetentionAtomicEvictionFailure(t *testing.T) {
	for _, stage := range []string{"tombstone", "parent deletion", "cancellation"} {
		t.Run(stage, func(t *testing.T) {
			s, o, closeStore := planFixture(t)
			r := planPending(t, s, o)
			applyRetentionFixture(t, s, closeStore)
			fresh := attachBatchFixture(t, s)
			ageRetentionFixture(t, fresh, 48*time.Hour)
			before := applyHistory(t, fresh.db, r.ID)
			used := assertRecoveryAccounting(t, fresh)
			trigger := "BEFORE UPDATE OF payload_state ON run_resolutions WHEN NEW.payload_state='evicting'"
			if stage != "tombstone" {
				trigger = "BEFORE DELETE ON runs"
			}
			if stage == "cancellation" {
				slowRetention(t, fresh)
			} else {
				_, err := fresh.db.Exec("CREATE TRIGGER refuse_eviction " + trigger + " BEGIN SELECT RAISE(ABORT,'private injected eviction failure'); END")
				require.NoError(t, err)
			}
			ctx := t.Context()
			if stage == "cancellation" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()
			}
			_, err := fresh.Maintain(ctx)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "private injected")
			require.Equal(t, before, applyHistory(t, fresh.db, r.ID))
			require.Equal(t, used, assertRecoveryAccounting(t, fresh))
			d, err := readRunResolution(t.Context(), fresh.db, r.ID)
			require.NoError(t, err)
			require.Equal(t, "retained", d.PayloadState)
			require.False(t, d.EvictedAt.Valid)
		})
	}
}

func TestRecoveryRetentionProtectsLateTerminalIncident(t *testing.T) {
	s, o, b, children, closeStore := recoveryBatchFixture(t, executor.CleanupLinuxDescendants, 2)
	r := children[0]
	startRecoveryChild(t, s, o, r)
	require.NoError(t, s.QuarantineRun(t.Context(), RuntimeIncident{RunID: r.ID, BatchID: b.BatchID, Generation: o.ID, Slot: o.Slot, Cause: IncidentSupervisorLost, Stage: StageHelperWait}))
	finishRecoveryChild(t, s, r, 7)
	searchToReady(t, s, o, r.ID, "retained")
	expireRun(t, s, r.ID)
	// The legacy helper marshals a returned view and omits SQL's millisecond
	// round-up. Keep only stored execution fields, as real expiry does.
	_, err := s.db.Exec("UPDATE runs SET expires_at=expires_at+1,result=CAST(json_remove(CAST(result AS TEXT),'$.recovery') AS BLOB) WHERE id=?", r.ID)
	require.NoError(t, err)
	before := applyHistory(t, s.db, r.ID)
	report := retainFixture(t, s, 1, true)
	require.True(t, report.More)
	require.Zero(t, report.DeletedRows)
	row, err := s.Load(t.Context(), r.ID)
	require.NoError(t, err)
	require.True(t, row.Result.Recovery.BlocksAdmission)
	require.True(t, row.Result.ExpiresAt.Before(time.Now()))
	_, err = s.ReadRaw(t.Context(), r.ID, "stdout", "", 100)
	require.NoError(t, err)
	_, err = s.ReadProjection(t.Context(), r.ID, "stdout", "", 100)
	require.NoError(t, err)
	page, err := s.List(t.Context(), ListOptions{BatchID: b.BatchID, Limit: 64})
	require.NoError(t, err)
	require.True(t, page.Entries[0].OutputAvailable)
	inspection, err := InspectRuns(t.Context(), s.path, testKey, InspectionOptions{RunID: r.ID, Limit: 64})
	require.NoError(t, err)
	require.True(t, *inspection.Subjects[0].OutputAvailable)
	require.Equal(t, before, applyHistory(t, s.db, r.ID))
	var executionBefore, executionAfter string
	const executionSnapshot = "SELECT json_remove(CAST(result AS TEXT),'$.index') FROM runs WHERE id=?"
	require.NoError(t, s.db.QueryRow(executionSnapshot, r.ID).Scan(&executionBefore))
	search, err := s.Search(t.Context(), o, SearchOptions{RunID: r.ID, Queries: []string{"retained"}, Limit: 1})
	require.NoError(t, err)
	require.False(t, search.Partial)
	require.Nil(t, search.Error)
	require.NotEmpty(t, search.Results[0].Hits, "protected retained output must not become an authoritative zero-match result")
	// This is incident-only history, so existing index metadata remains mutable.
	// All execution fields and original expiry must remain unchanged by search.
	require.NoError(t, s.db.QueryRow(executionSnapshot, r.ID).Scan(&executionAfter))
	require.Equal(t, executionBefore, executionAfter)
	before = applyHistory(t, s.db, r.ID)
	a := applyRetentionFixture(t, s, closeStore)
	require.Zero(t, a.Receipt.RunCount, "late proof requires incident-only resolution")
	fresh := attachBatchFixture(t, s)
	require.Equal(t, before, applyHistory(t, fresh.db, r.ID))
	d, err := readRunResolution(t.Context(), fresh.db, r.ID)
	require.NoError(t, err)
	require.Nil(t, d)
	_, err = fresh.Load(t.Context(), r.ID)
	requireErrorCode(t, err, "run_unavailable")
	report, err = fresh.Maintain(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, report.Expired)
	assertRecoveryAccounting(t, fresh)
	inspection, err = InspectRuns(t.Context(), fresh.path, testKey, InspectionOptions{RunID: r.ID, Limit: 64})
	require.NoError(t, err)
	require.False(t, inspection.Subjects[0].EvidenceInconsistent)
	require.False(t, *inspection.Subjects[0].OutputAvailable)
}

func TestRecoveryRetentionCapacityFailurePreservesSibling(t *testing.T) {
	s, o, closeStore := planFixture(t)
	sibling := retentionRun(t, s, o, "must survive administrative capacity refusal\n", true)
	planPending(t, s, o)
	expireRun(t, s, sibling.ID)
	_, err := s.db.Exec("UPDATE runs SET expires_at=expires_at+1 WHERE id=?", sibling.ID)
	require.NoError(t, err)
	before := applyHistory(t, s.db, sibling.ID)
	closeStore()
	p, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
	require.NoError(t, err)
	// Exhaust capacity after preview, without changing its logical fingerprint.
	mutatePlanStore(t, s.path, "UPDATE run_meta SET accounted_bytes=(SELECT max_retained_bytes FROM run_policy)-1")
	_, err = applyRecovery(t.Context(), s.path, testKey, applyOptions(p.Token), nil)
	requireErrorCode(t, err, "recovery_plan_stale")
	// The accounted-byte fingerprint changed. A fresh capacity-limited plan
	// must also preserve the expired sibling rather than retaining to fit.
	p, err = PlanRecovery(t.Context(), s.path, testKey, planOptions())
	requireErrorCode(t, err, "recovery_capacity")
	require.Empty(t, p.Token)
	require.False(t, p.Capacity.Sufficient)
	db := applyTestDB(t, s.path)
	require.Equal(t, before, applyHistory(t, db, sibling.ID))
	var count int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM run_recovery_receipts").Scan(&count))
	require.Zero(t, count)
}

func TestRecoveryRetentionConcurrentStatus(t *testing.T) {
	s, o, closeStore := planFixture(t)
	r := planPending(t, s, o)
	applyRetentionFixture(t, s, closeStore)
	fresh := attachBatchFixture(t, s)
	reader := attachBatchFixture(t, s)
	used := assertRecoveryAccounting(t, fresh)
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() {
		close(ready)
		for range 100 {
			row, err := reader.Load(t.Context(), r.ID)
			if err == nil {
				if row.Result.Recovery == nil || row.Result.Recovery.DispositionID != r.ID {
					done <- errors.New("status lost administrative attribution")
					return
				}
				continue
			}
			var typed *Error
			if !errors.As(err, &typed) || typed.Code != "run_unavailable" {
				done <- err
				return
			}
		}
		done <- nil
	}()
	<-ready
	// Always join the reader even if retention fails its assertions.
	defer func() { require.NoError(t, <-done) }()
	report := retainFixture(t, fresh, used-1, false)
	require.Equal(t, 1, report.Evicted)
}

func TestRecoveryRetentionClockRollback(t *testing.T) {
	s, o, closeStore := planFixture(t)
	r := planPending(t, s, o)
	applyRetentionFixture(t, s, closeStore)
	fresh := attachBatchFixture(t, s)
	// A resolution ahead of the current wall clock models a backward clock
	// adjustment after apply. Quota can still select this unexpired payload.
	ageRetentionFixture(t, fresh, -time.Hour)
	d, err := readRunResolution(t.Context(), fresh.db, r.ID)
	require.NoError(t, err)
	report := retainFixture(t, fresh, assertRecoveryAccounting(t, fresh)-1, false)
	require.Equal(t, 1, report.Evicted)
	evicted, err := readRunResolution(t.Context(), fresh.db, r.ID)
	require.NoError(t, err)
	require.Equal(t, "evicted", evicted.PayloadState)
	require.Equal(t, d.ResolvedAt, evicted.EvictedAt.Int64)
	require.Equal(t, d.ExpiresAt, evicted.ExpiresAt)
	require.Equal(t, d.Original, evicted.Original)
	assertRecoveryAccounting(t, fresh)
}

func TestRecoveryRetentionRejectsChangedBinding(t *testing.T) {
	s, o, closeStore := planFixture(t)
	r := planPending(t, s, o)
	applyRetentionFixture(t, s, closeStore)
	fresh := attachBatchFixture(t, s)
	ageRetentionFixture(t, fresh, 48*time.Hour)
	_, err := fresh.db.Exec("UPDATE run_resolutions SET result_sha256=zeroblob(32) WHERE run_id=?", r.ID)
	require.NoError(t, err)
	before := applyHistory(t, fresh.db, r.ID)
	used := assertRecoveryAccounting(t, fresh)
	_, err = fresh.Maintain(t.Context())
	requireErrorCode(t, err, "recovery_inconsistent")
	require.Equal(t, before, applyHistory(t, fresh.db, r.ID))
	require.Equal(t, used, assertRecoveryAccounting(t, fresh))
	var evicting bool
	require.NoError(t, fresh.db.QueryRow("SELECT evicting FROM runs WHERE id=?", r.ID).Scan(&evicting))
	require.False(t, evicting)
	inspection, err := InspectRuns(t.Context(), fresh.path, testKey, InspectionOptions{RunID: r.ID, Limit: 64})
	require.NoError(t, err)
	require.Len(t, inspection.Subjects, 1)
	require.True(t, inspection.Subjects[0].EvidenceInconsistent)
	require.Contains(t, inspection.Subjects[0].MissingEvidence, "valid_disposition")
}
