package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/runstore"
	"github.com/serpro69/capy/internal/store"
	"github.com/stretchr/testify/require"
)

func batchMarker(t *testing.T, s *Server, name string) {
	t.Helper()
	require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(s.projectDir, name)); return err == nil }, 10*time.Second, 10*time.Millisecond)
}
func releaseBatchMarker(t *testing.T, s *Server, name string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(s.projectDir, name), nil, 0600))
}
func TestParallelBatchSharedCapacity(t *testing.T) {
	s := newHandoffServer(t, "")
	s.config.Runs.MaxActive = 4
	standalone := []string{}
	for i := 0; i < 2; i++ {
		r := callTool(t, s, map[string]any{"language": "shell", "code": fmt.Sprintf("touch standalone%d; while [ ! -e release ]; do sleep .01; done", i), "wait_ms": 0})
		require.False(t, r.IsError, resultText(r))
		standalone = append(standalone, *capturedResult(t, r).RunID)
		batchMarker(t, s, fmt.Sprintf("standalone%d", i))
	}
	commands := make([]any, 8)
	for i := range commands {
		commands[i] = fmt.Sprintf("touch child%d; while [ ! -e release ]; do sleep .01; done; printf child%d", i, i)
	}
	r := callCapturedBatch(t, s, map[string]any{"commands": commands, "concurrency": 8, "wait_ms": 0})
	require.False(t, r.IsError, resultText(r))
	b := batchResult(t, r)
	require.Equal(t, 8, b.RequestedConcurrency)
	require.Equal(t, 4, b.EffectiveConcurrency)
	batchMarker(t, s, "child0")
	batchMarker(t, s, "child1")
	status, err := s.executionService().BatchStatus(t.Context(), b.BatchID)
	require.NoError(t, err)
	require.Equal(t, 2, status.CurrentConcurrency)
	require.Equal(t, 6, status.Counts["queued"])
	blocked := callTool(t, s, map[string]any{"language": "shell", "code": "touch bypass"})
	require.True(t, blocked.IsError)
	capacity := capturedResult(t, blocked).Capacity
	require.NotNil(t, capacity)
	require.Equal(t, 4, capacity.ActiveCount)
	require.Equal(t, 6, capacity.PendingCount)
	require.Len(t, capacity.Runs, 4)
	require.NoFileExists(t, filepath.Join(s.projectDir, "child2"))
	require.NoFileExists(t, filepath.Join(s.projectDir, "bypass"))
	releaseBatchMarker(t, s, "release")
	final := waitBatch(t, s, b.BatchID)
	require.Zero(t, final.FailedCount)
	require.Zero(t, final.IncompleteCount)
	children := batchChildren(t, s, b.BatchID)
	require.Len(t, children, 8)
	for _, id := range standalone {
		waitRunTerminal(t, s, id)
	}
	for i, c := range children {
		require.Equal(t, i, *c.Ordinal)
		require.Equal(t, "exited", c.State)
		require.True(t, c.OutputAvailable)
	}
	require.Nil(t, s.store, "batch output must never create knowledge sources")
}

func TestParallelBatchPendingCancellationAndAtomicAdmission(t *testing.T) {
	s := newHandoffServer(t, "")
	s.config.Runs.MaxActive = 3
	s.config.Runs.MaxPending = 4
	b := batchResult(t, callCapturedBatch(t, s, map[string]any{"commands": []any{"touch first; while [ ! -e release ]; do sleep .01; done", "touch second", "touch third", "touch fourth"}, "concurrency": 1, "wait_ms": 0}))
	batchMarker(t, s, "first")
	// Its concurrency ceiling leaves two owner slots free, but its queued
	// inventory leaves insufficient pending capacity for this entire batch.
	rejected := callCapturedBatch(t, s, map[string]any{"commands": []any{"touch rejected1", "touch rejected2"}, "concurrency": 2})
	require.True(t, rejected.IsError)
	require.Contains(t, resultText(rejected), "pending")
	single := callTool(t, s, map[string]any{"language": "shell", "code": "printf free-slot"})
	require.False(t, single.IsError, resultText(single))
	children := batchChildren(t, s, b.BatchID)
	cancel := callRuns(t, s, map[string]any{"action": "cancel", "run_id": children[1].RunID})
	require.False(t, cancel.IsError)
	releaseBatchMarker(t, s, "release")
	waitBatch(t, s, b.BatchID)
	children = batchChildren(t, s, b.BatchID)
	require.Equal(t, "cancelled", children[1].State)
	require.Nil(t, children[1].ExitCode)
	require.NoFileExists(t, filepath.Join(s.projectDir, "second"))
	require.NoFileExists(t, filepath.Join(s.projectDir, "rejected1"))
	batchMarker(t, s, "fourth")
}

func TestParallelBatchDeadlinesAndCancel(t *testing.T) {
	for _, mode := range []string{"queue cutoff", "batch cancel"} {
		t.Run(mode, func(t *testing.T) {
			s := newHandoffServer(t, "")
			s.config.Runs.MaxActive = 2
			timeout := 1500
			if mode == "batch cancel" {
				timeout = 20000
			}
			r := callCapturedBatch(t, s, map[string]any{"commands": []any{"touch first; sleep 10", "touch second; sleep 10", "touch forbidden"}, "concurrency": 2, "timeout": timeout, "wait_ms": 0})
			require.False(t, r.IsError, resultText(r))
			b := batchResult(t, r)
			if mode == "batch cancel" {
				batchMarker(t, s, "first")
				cr := callRuns(t, s, map[string]any{"action": "cancel", "batch_id": b.BatchID})
				require.False(t, cr.IsError)
			}
			final := waitBatch(t, s, b.BatchID)
			require.Positive(t, final.FailedCount)
			children := batchChildren(t, s, b.BatchID)
			require.Len(t, children, 3)
			if mode == "queue cutoff" {
				require.Equal(t, "skipped", children[2].State)
				row, err := s.executionService().Status(t.Context(), children[2].RunID)
				require.NoError(t, err)
				require.Equal(t, "queue_timeout", *row.Reason)
			} else {
				require.Equal(t, "cancelled", children[2].State)
			}
			require.Nil(t, children[2].ExitCode)
			require.NoFileExists(t, filepath.Join(s.projectDir, "forbidden"))
		})
	}
}

func TestParallelBatchGlobalRecoveryAndNoKnowledgeWrites(t *testing.T) {
	s := newHandoffServer(t, "")
	for _, fixture := range []struct {
		label string
		kind  store.SourceKind
	}{{"earlier durable", store.KindDurable}, {"batch:old", store.KindEphemeral}, {"legacy session", store.KindSession}} {
		_, err := s.getStore().Index("# reference\n\nsharedneedle historical", fixture.label, "markdown", fixture.kind)
		require.NoError(t, err)
	}
	r := callCapturedBatch(t, s, map[string]any{"commands": []any{"printf sharedneedle", "printf stderr >&2; exit 7"}, "concurrency": 2, "queries": []any{"sharedneedle"}, "query_scope": "global"})
	require.True(t, r.IsError)
	b := batchResult(t, r)
	require.Equal(t, "completed", b.State)
	require.NotNil(t, b.Search)
	require.Equal(t, "global", b.SearchAction.QueryScope)
	require.NotEmpty(t, b.Search.Results[0].Hits)
	require.Len(t, b.Search.Knowledge[0].Hits, 2)
	require.Nil(t, s.vault)
	sources, err := s.getStore().ListSources()
	require.NoError(t, err)
	require.Len(t, sources, 3)
	recovered := callRuns(t, s, map[string]any{"action": "search", "batch_id": b.BatchID, "query_scope": "global"})
	require.False(t, recovered.IsError, resultText(recovered))
	page := recovered.StructuredContent.(runstore.SearchPage)
	require.Len(t, page.Knowledge[0].Hits, 2)
	s.config.Runs.MaxResponseBytes = 2048
	bounded := callRuns(t, s, map[string]any{"action": "search", "batch_id": b.BatchID, "query_scope": "global"})
	assertRunBudget(t, bounded, 2048)
	batchOnly := callRuns(t, s, map[string]any{"action": "search", "batch_id": b.BatchID})
	require.Empty(t, batchOnly.StructuredContent.(runstore.SearchPage).Knowledge)
}

func TestParallelBatchInputAndStreamParity(t *testing.T) {
	s := newHandoffServer(t, "")
	for _, value := range []any{0, 9, 1.5, "2"} {
		r := callCapturedBatch(t, s, map[string]any{"commands": []any{"touch forbidden"}, "concurrency": value})
		require.True(t, r.IsError)
	}
	commands := []any{"cat <<'END'\n$HOME literal\nEND", "printf err >&2; exit 7", "true"}
	r := callCapturedBatch(t, s, map[string]any{"commands": commands, "concurrency": 3})
	require.True(t, r.IsError)
	children := batchChildren(t, s, batchResult(t, r).BatchID)
	raw, err := s.executionService().ReadRaw(t.Context(), children[0].RunID, "stdout", "", 4096)
	require.NoError(t, err)
	require.Equal(t, "$HOME literal\n", string(raw.Fragments[0].Data))
	raw, err = s.executionService().ReadRaw(t.Context(), children[1].RunID, "stderr", "", 4096)
	require.NoError(t, err)
	require.Equal(t, "err", string(raw.Fragments[0].Data))
	require.Equal(t, 7, *children[1].ExitCode)
	require.Equal(t, "complete", children[2].CaptureState)
	for _, q := range []any{nil, `["ok",null]`, []any{strings.Repeat("x", 513)}, []any{" "}} {
		r := callCapturedBatch(t, s, map[string]any{"commands": []any{"touch invalid-query"}, "concurrency": 2, "queries": q})
		require.True(t, r.IsError)
	}
	require.NoFileExists(t, filepath.Join(s.projectDir, "invalid-query"))
}

func TestParallelBatchSmallBudgetGlobalQueries(t *testing.T) {
	s := newHandoffServer(t, "")
	s.config.Runs.MaxResponseBytes = 2048
	queries := make([]any, 8)
	for i := range queries {
		queries[i] = "sharedneedle"
	}
	r := callCapturedBatch(t, s, map[string]any{"commands": []any{"printf sharedneedle"}, "concurrency": 8, "queries": queries, "query_scope": "global"})
	assertRunBudget(t, r, 2048)
	b := batchResult(t, r)
	require.NotEmpty(t, b.BatchID)
	require.NotNil(t, b.SearchAction)
	recovered := callRuns(t, s, map[string]any{"action": "search", "batch_id": b.BatchID, "query_scope": "global"})
	assertRunBudget(t, recovered, 2048)
	require.Equal(t, b.BatchID, recovered.StructuredContent.(runstore.SearchPage).BatchID)
}

func TestParallelBatchSpawnDeadlineExcludesQueueWait(t *testing.T) {
	s := newHandoffServer(t, "")
	s.config.Runs.MaxActive = 1
	r := callCapturedBatch(t, s, map[string]any{"commands": []any{"touch first; while [ ! -e release-first ]; do sleep .01; done", "touch second; while [ ! -e release-second ]; do sleep .01; done"}, "concurrency": 2, "timeout": 30000, "wait_ms": 0})
	require.False(t, r.IsError)
	b := batchResult(t, r)
	batchMarker(t, s, "first")
	children := batchChildren(t, s, b.BatchID)
	require.Len(t, children, 2)
	physical, err := filepath.EvalSymlinks(s.projectDir)
	require.NoError(t, err)
	st, err := runstore.Open(t.Context(), filepath.Join(physical, ".capy", "runs", "runs.db"), s.knowledgeKey, false)
	require.NoError(t, err)
	defer st.Close()
	first, err := st.Load(t.Context(), children[0].RunID)
	require.NoError(t, err)
	queued, err := st.Load(t.Context(), children[1].RunID)
	require.NoError(t, err)
	require.Equal(t, "queued", queued.Result.State)
	releasedAt := time.Now()
	releaseBatchMarker(t, s, "release-first")
	batchMarker(t, s, "second")
	second, err := st.Load(t.Context(), children[1].RunID)
	require.NoError(t, err)
	require.True(t, second.Program.Deadline.After(first.Program.Deadline))
	require.False(t, second.Program.Deadline.Before(releasedAt.Add(30*time.Second)), "parallel deadline must start at its spawn reservation")
	releaseBatchMarker(t, s, "release-second")
	final := waitBatch(t, s, b.BatchID)
	require.Zero(t, final.FailedCount)
}

func TestParallelBatchKnowledgeFailurePreservesRuns(t *testing.T) {
	s := newHandoffServer(t, "")
	s.config.Store.Path = t.TempDir()
	r := callCapturedBatch(t, s, map[string]any{"commands": []any{"printf distinctfailuremarker"}, "concurrency": 2, "queries": []any{"distinctfailuremarker"}, "query_scope": "global"})
	require.True(t, r.IsError)
	b := batchResult(t, r)
	require.NotEmpty(t, b.BatchID)
	require.Equal(t, "completed", b.State)
	require.NotNil(t, b.Search)
	require.NotNil(t, b.Search.KnowledgeError)
	require.True(t, b.Search.Partial)
	require.NotEmpty(t, b.Search.Results[0].Hits)
	require.Nil(t, b.Search.Error, "knowledge failure must not corrupt run index status")
}
