package runstore

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/sqliteutil"
	"github.com/stretchr/testify/require"
)

func appendSearchText(t *testing.T, s *Store, id, stream, data string, seq *uint64) {
	t.Helper()
	for len(data) > 0 {
		n := min(len(data), executor.EventBlockBytes)
		*seq++
		require.NoError(t, s.Append(t.Context(), id, executor.OutputEvent{Sequence: *seq, Phase: "run", Stream: stream, Data: []byte(data[:n])}))
		data = data[n:]
	}
}

func finishSearchRun(t *testing.T, s *Store, id string, bytes int) {
	t.Helper()
	code := 1
	require.NoError(t, s.Finish(t.Context(), id, Completion{Started: true, Retired: true, ExitCode: &code, ObservedBytes: int64(bytes)}))
}

func searchToReady(t *testing.T, s *Store, o *Owner, id, query string) SearchPage {
	t.Helper()
	var page SearchPage
	for range 100 {
		var err error
		page, err = s.Search(t.Context(), o, SearchOptions{RunID: id, Queries: []string{query}, Limit: 5})
		require.NoError(t, err)
		require.Nil(t, page.Error, "%+v", page.Error)
		if !page.Partial {
			return page
		}
	}
	t.Fatal("index failed to finish bounded progress")
	return page
}

func TestRunSearchLazyMiddleRecoveryAndRedaction(t *testing.T) {
	s, o := retentionStore(t, 16<<20)
	run := retentionRun(t, s, o, "", false)
	secret := "sk-" + strings.Repeat("a", 48)
	text := strings.Repeat("passing test line\n", 9000) + "MIDDLE_FAILURE diagnostic detail\n" + secret + "\n<private>NEVER_INDEX_PRIVATE</private>\n" + strings.Repeat("following test line\n", 5000)
	var seq uint64
	appendSearchText(t, s, run.ID, "stdout", text, &seq)
	finishSearchRun(t, s, run.ID, len(text))
	for _, table := range []string{"run_search_windows", "run_search_fts", "run_search_trigram", "run_search_progress"} {
		var count int
		require.NoError(t, s.db.QueryRow("SELECT count(*) FROM "+table).Scan(&count))
		require.Zero(t, count, "ordinary no-intent capture must not index")
	}
	initial, err := s.Search(t.Context(), o, SearchOptions{RunID: run.ID, Queries: []string{"MIDDLE_FAILURE"}, Limit: 5})
	require.NoError(t, err)
	require.Nil(t, initial.Error)
	require.True(t, initial.Partial)
	require.Equal(t, "building", initial.Index.State)
	require.Less(t, initial.Coverage["stdout"], int64(len(text)))
	page := searchToReady(t, s, o, run.ID, "MIDDLE_FAILURE")
	require.NotEmpty(t, page.Results[0].Hits)
	hit := page.Results[0].Hits[0]
	require.Contains(t, hit.Text, "MIDDLE_FAILURE")
	require.Greater(t, hit.RawStart, int64(100<<10))
	read, err := s.ReadProjection(t.Context(), run.ID, hit.Stream, hit.ReadCursor, searchWindowBytes)
	require.NoError(t, err)
	var recovered strings.Builder
	for _, part := range read.Fragments {
		recovered.WriteString(part.Text)
	}
	require.Contains(t, recovered.String(), "MIDDLE_FAILURE")
	for _, table := range []string{"run_search_fts", "run_search_trigram"} {
		var leaked int
		require.NoError(t, s.db.QueryRow("SELECT count(*) FROM "+table+" WHERE instr(content,?)>0 OR instr(content,'NEVER_INDEX_PRIVATE')>0", secret).Scan(&leaked))
		require.Zero(t, leaked)
	}
	var raw bytes.Buffer
	cursor := ""
	for {
		p, err := s.ReadRaw(t.Context(), run.ID, "stdout", cursor, 65536)
		require.NoError(t, err)
		for _, f := range p.Fragments {
			raw.Write(f.Data)
		}
		if p.EOF {
			break
		}
		cursor = *p.NextCursor
	}
	require.Equal(t, text, raw.String())
	before := accounted(t, s)
	page = searchToReady(t, s, o, run.ID, "MIDDLE_FAILURE")
	require.Equal(t, before, accounted(t, s), "repeat search must not grow FTS")
	require.NoError(t, o.Lease.Close())
	require.NoError(t, s.Close())
	stored, err := os.ReadFile(s.path)
	require.NoError(t, err)
	require.NotContains(t, string(stored), "MIDDLE_FAILURE")
	other, err := Open(t.Context(), s.path, testKey, false)
	require.NoError(t, err)
	defer other.Close()
	o2, err := other.ClaimOwner(t.Context())
	require.NoError(t, err)
	defer o2.Lease.Close()
	page = searchToReady(t, other, o2, run.ID, "MIDDLE_FAILURE")
	require.NotEmpty(t, page.Results[0].Hits)
}

func TestRunSearchStreamProgressAndSplitToken(t *testing.T) {
	s, o := retentionStore(t, 4<<20)
	run := retentionRun(t, s, o, "", false)
	var seq uint64
	appendSearchText(t, s, run.ID, "stdout", "<priv", &seq)
	appendSearchText(t, s, run.ID, "stderr", "independent fail", &seq)
	first, err := s.Search(t.Context(), o, SearchOptions{RunID: run.ID, Queries: []string{"independent"}, Limit: 5})
	require.NoError(t, err)
	require.True(t, first.Partial)
	require.Zero(t, first.Coverage["stdout"])
	require.NotEmpty(t, first.Results[0].Hits)
	appendSearchText(t, s, run.ID, "stderr", "ure_marker next\n", &seq)
	appendSearchText(t, s, run.ID, "stdout", "ate>never_release</private>public\n", &seq)
	finishSearchRun(t, s, run.ID, 80)
	final := searchToReady(t, s, o, run.ID, "failure_marker")
	require.NotEmpty(t, final.Results[0].Hits)
	require.Contains(t, final.Results[0].Hits[0].Text, "failure_marker")
	require.Equal(t, "stderr", final.Results[0].Hits[0].Stream)
	for _, group := range final.Results {
		for _, hit := range group.Hits {
			require.NotContains(t, hit.Text, "never_release")
		}
	}
}

func TestRunSearchQuotaRollbackPreservesAllLogs(t *testing.T) {
	s, o := retentionStore(t, 2*runReserve+6000)
	first := retentionRun(t, s, o, "retained earlier\n", true)
	second := retentionRun(t, s, o, "failure marker\n", true)
	before := accounted(t, s)
	page, err := s.Search(t.Context(), o, SearchOptions{RunID: second.ID, Queries: []string{"failure"}, Limit: 5})
	require.NoError(t, err)
	require.NotNil(t, page.Error)
	require.Equal(t, "index_quota_blocked", page.Error.Code)
	require.Equal(t, "quota_blocked", page.Index.State)
	require.True(t, page.Partial)
	require.Equal(t, before, accounted(t, s))
	for _, id := range []string{first.ID, second.ID} {
		r, err := s.ReadProjection(t.Context(), id, "stdout", "", 4096)
		require.NoError(t, err)
		require.NotEmpty(t, r.Fragments)
	}
	for _, table := range []string{"run_search_windows", "run_search_fts", "run_search_trigram", "run_search_progress", "run_work_claims"} {
		var count int
		require.NoError(t, s.db.QueryRow("SELECT count(*) FROM "+table).Scan(&count))
		require.Zero(t, count, "rollback must include postings/progress/claims")
	}
}

func TestRunSearchClaimRecoveryAndLayerFailure(t *testing.T) {
	s, o := retentionStore(t, 8<<20)
	run := retentionRun(t, s, o, strings.Repeat("passing ", 1000)+"diagnostic failure\n", true)
	claimed, err := s.ClaimIndex(t.Context(), o, run.ID)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, s.indexBatch(t.Context(), o, run.ID))
	// Persist a building claim as if the worker vanished between batches.
	_, err = s.db.Exec("UPDATE runs SET result=json_set(result,'$.index.state','building') WHERE id=?", run.ID)
	require.NoError(t, err)
	o2, err := s.ClaimOwner(t.Context())
	require.NoError(t, err)
	defer o2.Lease.Close()
	busy, err := s.Search(t.Context(), o2, SearchOptions{RunID: run.ID, Queries: []string{"diagnostic"}, Limit: 5})
	require.NoError(t, err)
	require.True(t, busy.Partial)
	require.NoError(t, o.Lease.Close())
	_, err = s.Reconcile(t.Context())
	require.NoError(t, err)
	row, err := s.Load(t.Context(), run.ID)
	require.NoError(t, err)
	require.Equal(t, "index_interrupted", row.Result.Index.Error.Code)
	page := searchToReady(t, s, o2, run.ID, "diagnostic")
	require.NotEmpty(t, page.Results[0].Hits)
	_, err = s.db.Exec("DROP TABLE run_search_trigram")
	require.NoError(t, err)
	page, err = s.Search(t.Context(), o2, SearchOptions{RunID: run.ID, Queries: []string{"diagnostic"}, Limit: 5})
	require.NoError(t, err)
	require.Equal(t, "index_search_failed", page.Error.Code)
	require.True(t, page.Partial)
	require.Empty(t, page.Results)
	_, err = s.ReadRaw(t.Context(), run.ID, "stdout", "", 65536)
	require.NoError(t, err)
}

func TestRunSearchIndexDebitAndBoundedEviction(t *testing.T) {
	s, o := retentionStore(t, 16<<20)
	run := retentionRun(t, s, o, "", false)
	var seq uint64
	data := strings.Repeat("sample diagnostic failure and passing tests\n", 6000)
	appendSearchText(t, s, run.ID, "stdout", data, &seq)
	finishSearchRun(t, s, run.ID, len(data))
	searchToReady(t, s, o, run.ID, "diagnostic")
	var debit, pages, free, size int64
	require.NoError(t, s.db.QueryRow("SELECT index_bytes FROM run_meta").Scan(&debit))
	require.Positive(t, debit)
	if s.policy.AccountingMode == "dbstat" {
		var allocated int64
		require.NoError(t, s.db.QueryRow("SELECT SUM(pgsize) FROM dbstat WHERE name GLOB 'run_search_*' OR name='sqlite_autoindex_run_search_progress_1'").Scan(&allocated))
		require.Equal(t, allocated, debit, "must include actual posting/shadow allocation")
	} else {
		require.NoError(t, s.db.QueryRow("PRAGMA page_count").Scan(&pages))
		require.NoError(t, s.db.QueryRow("PRAGMA freelist_count").Scan(&free))
		require.NoError(t, s.db.QueryRow("PRAGMA page_size").Scan(&size))
		require.Equal(t, (pages-free)*size, debit)
	}
	before := accounted(t, s)
	expireRun(t, s, run.ID)
	passes := 0
	for range 100 {
		report, err := s.Maintain(t.Context())
		require.NoError(t, err)
		require.LessOrEqual(t, report.DeletedRows, int64(2*evictionRows))
		passes++
		if !report.PendingDeletion {
			break
		}
	}
	require.Greater(t, passes, 1, "index deletion must resume bounded batches")
	for _, table := range []string{"runs", "run_search_windows", "run_search_fts", "run_search_trigram", "run_search_progress"} {
		var n int
		require.NoError(t, s.db.QueryRow("SELECT count(*) FROM "+table).Scan(&n))
		require.Zero(t, n, "orphan in %s", table)
	}
	require.Less(t, accounted(t, s), before)
}

func TestRunSearchCancelledClaimAndUnavailableProjection(t *testing.T) {
	s, o := retentionStore(t, 4<<20)
	run := retentionRun(t, s, o, "safe marker\n", true)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := s.Search(ctx, o, SearchOptions{RunID: run.ID, Queries: []string{"marker"}, Limit: 5})
	require.Error(t, err)
	searchToReady(t, s, o, run.ID, "marker")
	_, err = s.db.Exec("UPDATE runs SET result=json_set(result,'$.redaction.state','unavailable') WHERE id=?", run.ID)
	require.NoError(t, err)
	_, err = s.Search(t.Context(), o, SearchOptions{RunID: run.ID, Queries: []string{"marker"}, Limit: 5})
	requireErrorCode(t, err, "redaction_unavailable")
}

func TestRunSearchConcurrentCapture(t *testing.T) {
	s, o := retentionStore(t, 16<<20)
	searchRun := retentionRun(t, s, o, "", false)
	var seq uint64
	appendSearchText(t, s, searchRun.ID, "stdout", strings.Repeat("diagnostic marker passing\n", 6000), &seq)
	finishSearchRun(t, s, searchRun.ID, 6000*26)
	writer, err := Open(t.Context(), s.path, testKey, false)
	require.NoError(t, err)
	defer writer.Close()
	wo, err := writer.ClaimOwner(t.Context())
	require.NoError(t, err)
	defer wo.Lease.Close()
	active := retentionRun(t, writer, wo, "", false)
	done := make(chan error, 1)
	go func() {
		for i := 1; i <= 40; i++ {
			if err := writer.Append(t.Context(), active.ID, executor.OutputEvent{Sequence: uint64(i), Phase: "run", Stream: "stdout", Data: []byte(strings.Repeat("capture ", 500) + "\n")}); err != nil {
				done <- err
				return
			}
			time.Sleep(time.Millisecond)
		}
		done <- nil
	}()
	start := time.Now()
	page := searchToReady(t, s, o, searchRun.ID, "diagnostic")
	first := time.Since(start)
	start = time.Now()
	searchToReady(t, s, o, searchRun.ID, "diagnostic")
	repeated := time.Since(start)
	require.NoError(t, <-done)
	finishSearchRun(t, writer, active.ID, 40*4001)
	require.NotEmpty(t, page.Results[0].Hits)
	var indexBytes int64
	require.NoError(t, s.db.QueryRow("SELECT index_bytes FROM run_meta").Scan(&indexBytes))
	info, err := os.Stat(s.path + "-wal")
	require.NoError(t, err)
	t.Logf("concurrent capture: initial=%s repeat=%s accounted=%d index_debit=%d wal=%d mode=%s", first, repeated, accounted(t, s), indexBytes, info.Size(), s.policy.AccountingMode)
}

func TestRunSearchOptionalBatchRollbackKeepsPreviousCoverage(t *testing.T) {
	s, o := retentionStore(t, 8<<20)
	run := retentionRun(t, s, o, "", false)
	var seq uint64
	appendSearchText(t, s, run.ID, "stdout", strings.Repeat("diagnostic\n", 8000), &seq)
	finishSearchRun(t, s, run.ID, 88000)
	options := SearchOptions{RunID: run.ID, Queries: []string{"diagnostic"}, Limit: 5}
	first, err := s.Search(t.Context(), o, options)
	require.NoError(t, err)
	require.True(t, first.Partial)
	// Inject the failure at the posting write, after retained progress exists.
	_, err = s.db.Exec("CREATE TRIGGER fail_search BEFORE INSERT ON run_search_fts_content BEGIN SELECT RAISE(ABORT,'injected'); END")
	require.NoError(t, err)
	failed, err := s.Search(t.Context(), o, options)
	require.NoError(t, err)
	require.NotNil(t, failed.Error)
	require.Equal(t, first.Coverage, failed.Coverage)
	require.NotEmpty(t, failed.Results[0].Hits)
	_, err = s.db.Exec("DROP TRIGGER fail_search")
	require.NoError(t, err)
	searchToReady(t, s, o, run.ID, "diagnostic")
	// The measured debit is already flushed before commit, not just a count
	// of FTS content bytes; recomputing it must not discover uncharged postings.
	tx, err := sqliteutil.BeginImmediateContext(t.Context(), s.db, "runs")
	require.NoError(t, err)
	var before int64
	require.NoError(t, tx.QueryRow("SELECT accounted_bytes FROM run_meta").Scan(&before))
	after, err := refreshIndexDebit(t.Context(), tx)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.NoError(t, tx.Rollback())
}

func TestRunSearchValidation(t *testing.T) {
	s, o := retentionStore(t, 4<<20)
	run := retentionRun(t, s, o, "marker", true)
	for name, options := range map[string]SearchOptions{
		"no selector":   {Queries: []string{"x"}, Limit: 5},
		"two selectors": {RunID: run.ID, BatchID: run.ID, Queries: []string{"x"}, Limit: 5},
		"zero limit":    {RunID: run.ID, Queries: []string{"x"}},
		"blank query":   {RunID: run.ID, Queries: []string{" "}, Limit: 5},
		"long query":    {RunID: run.ID, Queries: []string{strings.Repeat("x", 513)}, Limit: 5},
		"global run":    {RunID: run.ID, Queries: []string{"x"}, Limit: 5, QueryScope: "global"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := s.Search(t.Context(), o, options)
			requireErrorCode(t, err, "invalid_argument")
		})
	}
	_, err := s.Search(t.Context(), o, SearchOptions{BatchID: run.ID, Limit: 5})
	requireErrorCode(t, err, "batch_unavailable")
}

func TestRunSearchRetriesFailedReleaseWithoutOwnerRestart(t *testing.T) {
	s, o := retentionStore(t, 4<<20)
	run := retentionRun(t, s, o, "early marker\n", false)
	_, err := s.db.Exec("CREATE TRIGGER fail_claim_release BEFORE DELETE ON run_work_claims BEGIN SELECT RAISE(ABORT,'injected contention'); END")
	require.NoError(t, err)
	page, err := s.Search(t.Context(), o, SearchOptions{RunID: run.ID, Queries: []string{"early"}, Limit: 5})
	require.NoError(t, err)
	require.NotNil(t, page.Error)
	require.True(t, page.Partial)
	require.NotEmpty(t, page.Results[0].Hits)
	_, err = s.db.Exec("DROP TRIGGER fail_claim_release")
	require.NoError(t, err)
	seq := uint64(1)
	appendSearchText(t, s, run.ID, "stdout", "later diagnostic\n", &seq)
	finishSearchRun(t, s, run.ID, 30)
	page = searchToReady(t, s, o, run.ID, "later diagnostic")
	require.NotEmpty(t, page.Results[0].Hits)
	_, err = o.Lease.Stat()
	require.NoError(t, err, "same live generation must resume without restart")
	var claims int
	require.NoError(t, s.db.QueryRow("SELECT count(*) FROM run_work_claims").Scan(&claims))
	require.Zero(t, claims)
}

func TestRunSearchEvictionStopsAfterPhysicalDebitFalls(t *testing.T) {
	s, o := retentionStore(t, 8<<20)
	if s.policy.AccountingMode != "conservative" {
		t.Skip("conservative all-used-pages regression")
	}
	first := retentionRun(t, s, o, "", false)
	var seq uint64
	appendSearchText(t, s, first.ID, "stdout", strings.Repeat("older retained payload\n", 2200), &seq)
	finishSearchRun(t, s, first.ID, 2200*23)
	second := retentionRun(t, s, o, "newer retained marker\n", true)
	searchToReady(t, s, o, first.ID, "older")
	var firstCharge int64
	require.NoError(t, s.db.QueryRow("SELECT accounted_bytes FROM runs WHERE id=?", first.ID).Scan(&firstCharge))
	limit := accounted(t, s) - firstCharge - 4096
	tx, err := sqliteutil.BeginImmediateContext(t.Context(), s.db, "runs")
	require.NoError(t, err)
	report, err := retain(t.Context(), tx, limit, 0, false)
	require.NoError(t, err)
	require.False(t, report.More)
	require.Equal(t, 1, report.Evicted, "freed physical debit must prevent selecting the newer victim")
	require.NoError(t, tx.Commit())
	_, err = s.Load(t.Context(), second.ID)
	require.NoError(t, err)
}

func TestRunSearchFragmentedProjectionProgress(t *testing.T) {
	s, o := retentionStore(t, 8<<20)
	run := retentionRun(t, s, o, "", false)
	var seq uint64
	data := strings.Repeat(string([]byte{255, 'x'}), 4096) + "\nfragmented diagnostic marker\n"
	appendSearchText(t, s, run.ID, "stdout", data, &seq)
	finishSearchRun(t, s, run.ID, len(data))
	page := searchToReady(t, s, o, run.ID, "fragmented diagnostic")
	require.NotEmpty(t, page.Results[0].Hits)
	require.Equal(t, int64(len(data)), page.Coverage["stdout"])
	hit := page.Results[0].Hits[0]
	read, err := s.ReadProjection(t.Context(), run.ID, hit.Stream, hit.ReadCursor, 4096)
	require.NoError(t, err)
	require.Contains(t, pageText(read), "fragmented diagnostic")
}
