package runstore

import (
	"encoding/json"
	"github.com/google/uuid"
	"github.com/serpro69/capy/internal/sqliteutil"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/executor"
	"github.com/stretchr/testify/require"
)

func TestBatchManifestSurvivesChildEviction(t *testing.T) {
	s, o := retentionStore(t, 4<<20)
	b, children, err := s.AdmitBatch(t.Context(), o, BatchInput{Concurrency: 1, Queries: []string{"diagnostic"}, Commands: []BatchCommand{{Label: "same", Command: "one"}, {Label: "same", Command: "two"}}})
	require.NoError(t, err)
	require.Len(t, children, 2)
	require.NotEqual(t, children[0].ID, children[1].ID)
	for i, child := range children {
		require.NoError(t, s.Pending(t.Context(), child.ID, o.ID))
		require.NoError(t, s.Started(t.Context(), child.ID))
		require.NoError(t, s.Append(t.Context(), child.ID, executor.OutputEvent{Sequence: 1, Phase: "run", Stream: "stdout", Data: []byte("diagnostic output")}))
		code := i * 7
		require.NoError(t, s.Finish(t.Context(), child.ID, Completion{Started: true, Retired: true, ExitCode: &code, ObservedBytes: 17}))
	}
	status, err := s.BatchStatus(t.Context(), b.BatchID)
	require.NoError(t, err)
	require.Equal(t, "completed", status.State)
	require.Equal(t, 1, status.FailedCount)
	require.NotNil(t, status.ExpiresAt)
	before := accounted(t, s)
	expireRun(t, s, children[1].ID)
	page, err := s.List(t.Context(), ListOptions{BatchID: b.BatchID, Limit: 1})
	require.NoError(t, err)
	require.NotNil(t, page.NextCursor)
	require.Equal(t, 0, *page.Entries[0].Ordinal)
	second, err := s.List(t.Context(), ListOptions{BatchID: b.BatchID, Limit: 1, Cursor: *page.NextCursor})
	require.NoError(t, err)
	require.Equal(t, 7, *second.Entries[0].ExitCode)
	require.False(t, second.Entries[0].OutputAvailable)
	_, err = s.Maintain(t.Context())
	require.NoError(t, err)
	require.Less(t, accounted(t, s), before)
	status, err = s.BatchStatus(t.Context(), b.BatchID)
	require.NoError(t, err)
	require.Equal(t, 1, status.FailedCount)
	_, err = s.ReadRaw(t.Context(), children[1].ID, "stdout", "", 10)
	requireErrorCode(t, err, "run_unavailable")
	search, err := s.Search(t.Context(), o, SearchOptions{BatchID: b.BatchID, Limit: 5})
	require.NoError(t, err)
	require.True(t, search.Partial)
	require.Len(t, search.Results[0].Hits, 1)
	// A manifest's own expiry does not delete a retained child's output.
	_, err = s.db.ExecContext(t.Context(), "UPDATE run_batches SET expires_at=? WHERE id=?", time.Now().Add(-time.Hour).UnixMilli(), b.BatchID)
	require.NoError(t, err)
	_, err = s.BatchStatus(t.Context(), b.BatchID)
	requireErrorCode(t, err, "batch_unavailable")
	_, err = s.Load(t.Context(), children[0].ID)
	require.NoError(t, err)
	_, err = s.Maintain(t.Context())
	require.NoError(t, err)
	accounted(t, s)
	var count int
	require.NoError(t, s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM run_batches").Scan(&count))
	require.Zero(t, count)
}

func TestBatchAdmissionQuotaAtomicAndRecovery(t *testing.T) {
	t.Run("atomic quota", func(t *testing.T) {
		s, o := retentionStore(t, 2*runReserve)
		_, _, err := s.AdmitBatch(t.Context(), o, BatchInput{Concurrency: 1, Commands: []BatchCommand{{Command: "one"}, {Command: "two"}}})
		requireErrorCode(t, err, "quota_exhausted")
		for _, table := range []string{"run_batches", "run_batch_children", "runs"} {
			var n int
			require.NoError(t, s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&n))
			require.Zero(t, n)
		}
		require.Zero(t, accounted(t, s))
	})
	t.Run("abandoned queued children", func(t *testing.T) {
		s, o := retentionStore(t, 16<<20)
		commands := make([]BatchCommand, 70)
		for i := range commands {
			commands[i] = BatchCommand{Command: "no spawn"}
		}
		// Raise quota for >64 inventory so bounded recovery needs multiple passes.
		b, children, err := s.AdmitBatch(t.Context(), o, BatchInput{Concurrency: 1, Commands: commands})
		require.NoError(t, err)
		require.NoError(t, o.Lease.Close())
		for range 3 {
			_, err = s.Reconcile(t.Context())
			require.NoError(t, err)
		}
		status, err := s.BatchStatus(t.Context(), b.BatchID)
		require.NoError(t, err)
		require.Equal(t, "interrupted", status.State)
		require.Equal(t, 70, status.Counts["interrupted"])
		require.NotNil(t, status.ExpiresAt)
		row, err := s.Load(t.Context(), children[69].ID)
		require.NoError(t, err)
		require.Nil(t, row.Result.StartedAt)
		require.Equal(t, "not_started", row.Result.Capture.State)
		accounted(t, s)
	})
}

func TestBatchListSelectorAndFilters(t *testing.T) {
	s, o := retentionStore(t, 4<<20)
	input := BatchInput{Concurrency: 1, Commands: []BatchCommand{{Command: "one"}, {Command: "two"}, {Command: "three"}}}
	b, children, err := s.AdmitBatch(t.Context(), o, input)
	require.NoError(t, err)
	require.NoError(t, s.Finish(t.Context(), children[1].ID, Completion{Retired: true, Reason: "queue_timeout"}))
	first, err := s.List(t.Context(), ListOptions{BatchID: b.BatchID, Limit: 1, States: []string{"queued"}})
	require.NoError(t, err)
	require.NotNil(t, first.NextCursor)
	next, err := s.List(t.Context(), ListOptions{BatchID: b.BatchID, Limit: 1, Cursor: *first.NextCursor})
	require.NoError(t, err)
	require.Equal(t, 2, *next.Entries[0].Ordinal)
	other, _, err := s.AdmitBatch(t.Context(), o, input)
	require.NoError(t, err)
	_, err = s.List(t.Context(), ListOptions{BatchID: other.BatchID, Limit: 1, Cursor: *first.NextCursor})
	requireErrorCode(t, err, "cursor_invalid")
	_, err = s.List(t.Context(), ListOptions{BatchID: b.BatchID, Limit: 1, Cursor: *first.NextCursor, States: []string{"skipped"}})
	requireErrorCode(t, err, "cursor_invalid")
}

func TestBatchUnverifiedChildrenExposeRecoveryError(t *testing.T) {
	s, o := retentionStore(t, 4<<20)
	b, children, err := s.AdmitBatch(t.Context(), o, BatchInput{Concurrency: 1, Commands: []BatchCommand{{Command: "uncertain"}, {Command: "safe"}}})
	require.NoError(t, err)
	require.NoError(t, s.Pending(t.Context(), children[0].ID, o.ID))
	require.NoError(t, s.Started(t.Context(), children[0].ID))
	require.NoError(t, o.Lease.Close())
	_, err = s.Reconcile(t.Context())
	require.NoError(t, err)
	status, err := s.BatchStatus(t.Context(), b.BatchID)
	require.NoError(t, err)
	require.NotNil(t, status.Error)
	require.Equal(t, "cleanup_unverified", status.Error.Code)
	list, err := s.List(t.Context(), ListOptions{BatchID: b.BatchID, Limit: 16})
	require.NoError(t, err)
	require.Len(t, list.Entries, 2)
	require.NotNil(t, list.Entries[0].Error)
	require.Equal(t, "cleanup_unverified", list.Entries[0].Error.Code)
	require.Equal(t, "incomplete", list.Entries[0].CaptureState)
	require.Nil(t, list.Entries[1].Error)
	require.Equal(t, "interrupted", list.Entries[1].State)
}

func TestRunSearchSynonymGroupsMultipleQueries(t *testing.T) {
	s, o := retentionStore(t, 4<<20)
	row := retentionRun(t, s, o, "go version release; database connection; db connection", true)
	page, err := s.Search(t.Context(), o, SearchOptions{RunID: row.ID, Queries: []string{"go version", "database connection"}, Limit: 5})
	require.NoError(t, err)
	require.Nil(t, page.Error)
	require.Len(t, page.Results, 2)
	for _, group := range page.Results {
		require.NotEmpty(t, group.Hits)
	}
}

func TestBatchAdmissionRechecksQuarantineAfterCapacityPreparation(t *testing.T) {
	s, o := retentionStore(t, 4*runReserve)
	retentionRun(t, s, o, "", true)
	retentionRun(t, s, o, "", true)
	// Simulate another writer publishing quarantine in the unlocked interval
	// between the preliminary check/capacity preparation and the admission tx.
	_, err := s.db.ExecContext(t.Context(), `CREATE TRIGGER test_quarantine AFTER DELETE ON runs BEGIN UPDATE run_meta SET admission_blocked=1 WHERE singleton=1; END`)
	require.NoError(t, err)
	_, _, err = s.AdmitBatch(t.Context(), o, BatchInput{Concurrency: 1, Commands: []BatchCommand{{Command: "one"}, {Command: "two"}, {Command: "three"}}})
	requireErrorCode(t, err, "cleanup_unverified")
	var n int
	require.NoError(t, s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM run_batches").Scan(&n))
	require.Zero(t, n)
	_, err = s.db.ExecContext(t.Context(), "DROP TRIGGER test_quarantine")
	require.NoError(t, err)
}

func TestActiveBatchManifestAndChildrenProtectedFromQuota(t *testing.T) {
	s, o := retentionStore(t, 3*runReserve)
	b, children, err := s.AdmitBatch(t.Context(), o, BatchInput{Concurrency: 1, Commands: []BatchCommand{{Command: "one"}, {Command: "two"}}})
	require.NoError(t, err)
	_, err = s.Admit(t.Context(), o, Program{})
	requireErrorCode(t, err, "quota_exhausted")
	_, err = s.BatchStatus(t.Context(), b.BatchID)
	require.NoError(t, err)
	for _, child := range children {
		_, err = s.Load(t.Context(), child.ID)
		require.NoError(t, err)
	}
	accounted(t, s)
}

func TestExpiredBatchDeletionResumesBeforeEvictingLiveLogs(t *testing.T) {
	s, o := retentionStore(t, 8<<20)
	live := retentionRun(t, s, o, "keep available", true)
	// These are valid retained summaries after all their child logs were evicted.
	tx, err := sqliteutil.BeginImmediateContext(t.Context(), s.db, "runs")
	require.NoError(t, err)
	expired := time.Now().Add(-time.Hour)
	for range 2 {
		id := uuid.NewString()
		debit := int64(4096 + 256*batchChildReserve)
		_, err = tx.ExecContext(t.Context(), `INSERT INTO run_batches(id,generation,slot,created_at,completed_at,expires_at,ttl_hours,policy_revision,queries,query_scope,requested,effective,accounted_bytes) VALUES(?,?,?,?,?,?,24,1,'[]','batch',1,1,?)`, id, o.ID, o.Slot, expired.Add(-time.Hour).UnixNano(), expired.UnixNano(), expired.UnixMilli(), debit)
		require.NoError(t, err)
		for ordinal := range 256 {
			run := uuid.NewString()
			code := 0
			result, err := json.Marshal(Result{Version: 1, Kind: "run", BatchID: id, RunID: &run, OwnerID: o.ID, OwnerType: "mcp", State: "exited", ExitCode: &code, CompletedAt: &expired, ExpiresAt: &expired, Capture: Capture{State: "complete", TerminalPersisted: true}})
			require.NoError(t, err)
			_, err = tx.ExecContext(t.Context(), "INSERT INTO run_batch_children(batch_id,ordinal,run_id,label,command,result,terminal) VALUES(?,?,?,'label','true',?,1)", id, ordinal, run, result)
			require.NoError(t, err)
		}
		_, err = tx.ExecContext(t.Context(), "UPDATE run_meta SET accounted_bytes=accounted_bytes+?", debit)
		require.NoError(t, err)
	}
	require.NoError(t, tx.Commit())
	for pass := range 2 {
		tx, err = sqliteutil.BeginImmediateContext(t.Context(), s.db, "runs")
		require.NoError(t, err)
		report, err := retain(t.Context(), tx, 1<<20, 0, true)
		require.NoError(t, err)
		require.NoError(t, tx.Commit())
		require.EqualValues(t, 257, report.DeletedRows)
		require.Equal(t, pass == 0, report.More)
		_, err = s.Load(t.Context(), live.ID)
		require.NoError(t, err, "expired manifest must precede live output")
	}
	accounted(t, s)
}

func TestBatchSearchProjectionFailureKeepsRawAvailability(t *testing.T) {
	s, o := retentionStore(t, 4<<20)
	b, children, err := s.AdmitBatch(t.Context(), o, BatchInput{Concurrency: 1, Commands: []BatchCommand{{Command: "one"}}})
	require.NoError(t, err)
	require.NoError(t, s.Pending(t.Context(), children[0].ID, o.ID))
	require.NoError(t, s.Started(t.Context(), children[0].ID))
	require.NoError(t, s.Append(t.Context(), children[0].ID, executor.OutputEvent{Sequence: 1, Phase: "run", Stream: "stdout", Data: []byte("retained raw")}))
	code := 0
	require.NoError(t, s.Finish(t.Context(), children[0].ID, Completion{Started: true, Retired: true, ExitCode: &code, ObservedBytes: 12}))
	_, err = s.db.ExecContext(t.Context(), `UPDATE runs SET result=json_set(CAST(result AS TEXT),'$.redaction.state','unavailable') WHERE id=?`, children[0].ID)
	require.NoError(t, err)
	page, err := s.Search(t.Context(), o, SearchOptions{BatchID: b.BatchID, Queries: []string{"retained"}, Limit: 5})
	require.NoError(t, err)
	require.NotNil(t, page.Error)
	require.True(t, page.Children[0].OutputAvailable)
	raw, err := s.ReadRaw(t.Context(), children[0].ID, "stdout", "", 64)
	require.NoError(t, err)
	require.Equal(t, "retained raw", string(raw.Fragments[0].Data))
}
