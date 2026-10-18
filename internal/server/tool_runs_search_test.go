package server

import (
	"strings"
	"testing"

	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
)

func TestRunSearchBypassesKnowledgeThrottleAndBoundsResponse(t *testing.T) {
	s := newTestServer(t, nil)
	s.config.Runs.MaxResponseBytes = 2048
	r := callTool(t, s, map[string]any{"language": "shell", "code": "printf 'failure-marker diagnostic details\\n'; exit 7"})
	run := capturedResult(t, r)
	require.Equal(t, "not_requested", run.Index.State)
	s.throttle.mu.Lock()
	s.throttle.count = s.config.Search.BlockAfter + 1
	count := s.throttle.count
	s.throttle.mu.Unlock()
	response := callRuns(t, s, map[string]any{"action": "search", "run_id": *run.RunID, "queries": []any{"failure-marker"}, "limit": 20})
	require.False(t, response.IsError, resultText(response))
	assertRunBudget(t, response, 2048)
	page, ok := response.StructuredContent.(runstore.SearchPage)
	require.True(t, ok)
	require.Equal(t, "ready", page.Index.State)
	require.NotEmpty(t, page.Results[0].Hits)
	hit := page.Results[0].Hits[0]
	read := callRuns(t, s, map[string]any{"action": "read", "run_id": *run.RunID, "stream": hit.Stream, "cursor": hit.ReadCursor})
	require.False(t, read.IsError)
	require.Contains(t, read.StructuredContent.(runstore.ReadPage).Fragments[0].Text, "failure-marker")
	s.throttle.mu.Lock()
	require.Equal(t, count, s.throttle.count)
	s.throttle.mu.Unlock()
	require.Nil(t, s.store, "run search must never open knowledge storage")
	for name, args := range map[string]map[string]any{
		"empty queries":    {"queries": []any{}},
		"wrong element":    {"queries": []any{1}},
		"oversized query":  {"queries": []any{strings.Repeat("x", 513)}},
		"fractional limit": {"queries": []any{"marker"}, "limit": 1.5},
		"raw bypass":       {"queries": []any{"marker"}, "raw": true},
		"scope widening":   {"queries": []any{"marker"}, "query_scope": "global"},
		"two selectors":    {"queries": []any{"marker"}, "batch_id": *run.RunID},
	} {
		t.Run(name, func(t *testing.T) {
			args["action"] = "search"
			args["run_id"] = *run.RunID
			bad := callRuns(t, s, args)
			require.True(t, bad.IsError)
			assertRunBudget(t, bad, 2048)
		})
	}
}

func TestRunIntentQuotaFailureKeepsSuccessfulExecution(t *testing.T) {
	s := newTestServer(t, nil)
	s.config.Runs.MaxRunBytes = 1024
	s.config.Runs.MaxRetainedBytes = 70 << 10
	r := callTool(t, s, map[string]any{"language": "shell", "code": "printf 'retained marker\\n'", "intent": "marker"})
	require.False(t, r.IsError, resultText(r))
	run := capturedResult(t, r)
	require.Equal(t, "complete", run.Capture.State)
	require.Equal(t, "quota_blocked", run.Index.State)
	require.Equal(t, "index_quota_blocked", run.Index.Error.Code)
	require.Equal(t, "retained marker\n", capturedOutput(t, s, r, "stdout"))
}
