package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/serpro69/capy/internal/security"
	"github.com/stretchr/testify/require"
)

func callCapturedBatch(t *testing.T, s *Server, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	r, err := s.handleBatchExecute(t.Context(), req)
	require.NoError(t, err)
	return r
}
func batchResult(t *testing.T, r *mcp.CallToolResult) runstore.BatchResult {
	t.Helper()
	b, err := json.Marshal(r.StructuredContent)
	require.NoError(t, err)
	var result runstore.BatchResult
	require.NoError(t, json.Unmarshal(b, &result))
	return result
}
func batchChildren(t *testing.T, s *Server, id string) []runstore.Summary {
	t.Helper()
	entries := []runstore.Summary{}
	cursor := ""
	for {
		r := callRuns(t, s, map[string]any{"action": "list", "batch_id": id, "cursor": cursor, "limit": 1})
		require.False(t, r.IsError, resultText(r))
		assertRunBudget(t, r, s.config.Runs.MaxResponseBytes)
		page := r.StructuredContent.(runstore.ListPage)
		entries = append(entries, page.Entries...)
		if page.NextCursor == nil {
			break
		}
		require.NotEqual(t, cursor, *page.NextCursor)
		cursor = *page.NextCursor
	}
	return entries
}
func waitBatch(t *testing.T, s *Server, id string) runstore.BatchResult {
	t.Helper()
	var b runstore.BatchResult
	defer func() {
		if t.Failed() {
			t.Logf("last batch status: %+v", b)
		}
	}()
	require.Eventually(t, func() bool {
		var err error
		b, err = s.executionService().BatchStatus(t.Context(), id)
		return err == nil && b.CompletedAt != nil
	}, 15*time.Second, 10*time.Millisecond)
	return b
}
func TestCapturedBatchInventoryPreflight(t *testing.T) {
	for _, name := range []string{"deny final", "bad final", "empty final", "huge input", "fraction timeout", "fraction concurrency", "background", "pending capacity"} {
		t.Run(name, func(t *testing.T) {
			s := newHandoffServer(t, "")
			s.security = []security.SecurityPolicy{{Deny: []string{"Bash(touch forbidden)"}}}
			args := map[string]any{"commands": []any{"touch earlier", "printf later"}}
			switch name {
			case "deny final":
				args["commands"] = []any{"touch earlier", "touch forbidden"}
			case "bad final":
				args["commands"] = []any{"touch earlier", 42}
			case "empty final":
				args["commands"] = []any{"touch earlier", ""}
			case "huge input":
				args["commands"] = []any{"touch earlier", strings.Repeat("x", 1<<20)}
			case "fraction timeout":
				args["timeout"] = 1.5
			case "fraction concurrency":
				args["concurrency"] = 1.5
			case "background":
				args["background"] = false
			case "pending capacity":
				s.config.Runs.MaxPending = 1
			}
			result := callCapturedBatch(t, s, args)
			require.True(t, result.IsError, resultText(result))
			require.NoFileExists(t, filepath.Join(s.projectDir, "earlier"))
			require.NoFileExists(t, filepath.Join(s.projectDir, "forbidden"))
			require.NoDirExists(t, filepath.Join(s.projectDir, ".capy", "runs"))
		})
	}
}
func TestCapturedBatchCompleteRecoveryAndQueries(t *testing.T) {
	s := newHandoffServer(t, "")
	s.config.Runs.MaxResponseBytes = 2048
	commands := []any{map[string]any{"label": "duplicate", "command": "printf first; printf x >> once"}, map[string]any{"label": "duplicate", "command": "printf 'middlefailure diagnostic\\n'; printf stderr >&2; printf x >> once; exit 7"}, map[string]any{"label": "duplicate", "command": "printf last; printf x >> once"}}
	r := callCapturedBatch(t, s, map[string]any{"commands": commands, "queries": []any{"middlefailure"}})
	require.True(t, r.IsError)
	assertRunBudget(t, r, 2048)
	b := batchResult(t, r)
	require.Equal(t, "completed", b.State)
	require.Equal(t, 1, b.FailedCount)
	require.NotNil(t, b.SearchAction)
	require.Equal(t, "batch", b.SearchAction.QueryScope)
	entries := batchChildren(t, s, b.BatchID)
	require.Len(t, entries, 3)
	for i, entry := range entries {
		require.Equal(t, i, *entry.Ordinal)
		require.Equal(t, "duplicate", entry.Label)
		require.True(t, entry.OutputAvailable)
		require.Equal(t, "exited", entry.State)
		require.Equal(t, "complete", entry.CaptureState)
	}
	require.Equal(t, 7, *entries[1].ExitCode)
	raw, err := s.executionService().ReadRaw(t.Context(), entries[1].RunID, "stderr", "", 100)
	require.NoError(t, err)
	require.Equal(t, "stderr", string(raw.Fragments[0].Data))
	sr := callRuns(t, s, map[string]any{"action": "search", "batch_id": b.BatchID})
	require.False(t, sr.IsError, resultText(sr))
	assertRunBudget(t, sr, 2048)
	// Budgeting can omit all hits; the larger envelope recovers the same search.
	s.config.Runs.MaxResponseBytes = 8192
	sr = callRuns(t, s, map[string]any{"action": "search", "batch_id": b.BatchID})
	require.False(t, sr.IsError, resultText(sr))
	search := sr.StructuredContent.(runstore.SearchPage)
	require.Len(t, search.Results[0].Hits, 1)
	require.Equal(t, entries[1].RunID, search.Results[0].Hits[0].RunID)
	once, err := os.ReadFile(filepath.Join(s.projectDir, "once"))
	require.NoError(t, err)
	require.Equal(t, "xxx", string(once))
	require.Nil(t, s.store)
	require.Nil(t, s.vault)
	require.Equal(t, 1, s.stats.Snapshot().Calls["capy_batch_execute"])
}
func TestCapturedBatchDeadlineAndHandoff(t *testing.T) {
	for _, mode := range []string{"deadline", "handoff", "cancel batch", "cancel queued child"} {
		t.Run(mode, func(t *testing.T) {
			s := newHandoffServer(t, "")
			s.config.Runs.MaxResponseBytes = 2048
			args := map[string]any{"commands": []any{"printf early; touch started; while [ ! -e release ]; do sleep .01; done; printf late", "touch sibling; printf sibling"}, "timeout": 2000, "wait_ms": 0}
			if mode != "deadline" {
				args["timeout"] = 15000
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			req := mcp.CallToolRequest{}
			req.Params.Arguments = args
			r, err := s.handleBatchExecute(ctx, req)
			require.NoError(t, err)
			require.False(t, r.IsError, resultText(r))
			assertRunBudget(t, r, 2048)
			b := batchResult(t, r)
			require.NotEmpty(t, b.BatchID)
			cancel() // ordinary request cleanup after handoff must not cancel this batch
			require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(s.projectDir, "started")); return err == nil }, 5*time.Second, time.Millisecond)
			entries := batchChildren(t, s, b.BatchID)
			switch mode {
			case "handoff":
				require.NoError(t, os.WriteFile(filepath.Join(s.projectDir, "release"), nil, 0600))
			case "cancel batch":
				response := callRuns(t, s, map[string]any{"action": "cancel", "batch_id": b.BatchID})
				require.False(t, response.IsError, resultText(response))
			case "cancel queued child":
				response := callRuns(t, s, map[string]any{"action": "cancel", "run_id": entries[1].RunID})
				require.False(t, response.IsError, resultText(response))
				require.NoError(t, os.WriteFile(filepath.Join(s.projectDir, "release"), nil, 0600))
			}
			final := waitBatch(t, s, b.BatchID)
			entries = batchChildren(t, s, b.BatchID)
			switch mode {
			case "deadline":
				require.Equal(t, "timed_out", entries[0].State)
				require.Equal(t, "skipped", entries[1].State)
				require.Equal(t, "queue_timeout", *entries[1].Reason)
				require.Nil(t, entries[1].ExitCode)
			case "handoff":
				require.Equal(t, "completed", final.State)
				require.Zero(t, final.FailedCount)
				require.FileExists(t, filepath.Join(s.projectDir, "sibling"))
			case "cancel batch":
				require.Equal(t, "cancelled", entries[0].State)
				require.Equal(t, "cancelled", entries[1].State)
			case "cancel queued child":
				require.Equal(t, "exited", entries[0].State)
				require.Equal(t, "cancelled", entries[1].State)
			}
			if mode != "handoff" {
				require.NoFileExists(t, filepath.Join(s.projectDir, "sibling"))
			}
		})
	}
}
func TestCapturedBatchPreparationFailureCancelsInventory(t *testing.T) {
	s := newHandoffServer(t, "")
	result, err := s.executionService().ExecuteBatchResponse(t.Context(), runstore.BatchInput{Concurrency: 1, Commands: []runstore.BatchCommand{{Command: "touch started; sleep 20"}, {Command: "touch sibling"}}}, 30*time.Second, 0, func(runstore.BatchResult) error {
		require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(s.projectDir, "started")); return err == nil }, 5*time.Second, time.Millisecond)
		return errors.New("synthetic serialization failure")
	})
	require.Error(t, err)
	require.NotEmpty(t, result.BatchID)
	require.NoFileExists(t, filepath.Join(s.projectDir, "sibling"))
	require.NotNil(t, waitBatch(t, s, result.BatchID).CompletedAt)
}

func TestCapturedBatchLargeOutputAndSharedCapacity(t *testing.T) {
	s := newHandoffServer(t, "")
	s.config.Runs.MaxActive = 1
	s.config.Runs.MaxResponseBytes = 2048
	content := strings.Repeat("ordinary diagnostic\n", 7000) + "middlefailure\n\x00\xfflast"
	require.NoError(t, os.WriteFile(filepath.Join(s.projectDir, "large.bin"), []byte(content), 0600))
	r := callCapturedBatch(t, s, map[string]any{"commands": []any{"touch started; while [ ! -e release ]; do sleep .01; done; cat large.bin", "printf sibling"}, "wait_ms": 0})
	require.False(t, r.IsError)
	b := batchResult(t, r)
	require.NotEmpty(t, b.BatchID)
	require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(s.projectDir, "started")); return err == nil }, 5*time.Second, time.Millisecond)
	blocked := callTool(t, s, map[string]any{"language": "shell", "code": "touch bypass"})
	require.True(t, blocked.IsError)
	require.Equal(t, "capacity_exhausted", capturedResult(t, blocked).Error.Code)
	require.NoFileExists(t, filepath.Join(s.projectDir, "bypass"))
	require.NoError(t, os.WriteFile(filepath.Join(s.projectDir, "release"), nil, 0600))
	final := waitBatch(t, s, b.BatchID)
	require.Zero(t, final.IncompleteCount)
	require.Zero(t, final.FailedCount)
	children := batchChildren(t, s, b.BatchID)
	require.Len(t, children, 2)
	cursor := ""
	var output []byte
	for {
		page, err := s.executionService().ReadRaw(t.Context(), children[0].RunID, "stdout", cursor, 65536)
		require.NoError(t, err)
		for _, f := range page.Fragments {
			output = append(output, f.Data...)
		}
		if page.EOF {
			break
		}
		require.NotNil(t, page.NextCursor)
		cursor = *page.NextCursor
	}
	require.Equal(t, []byte(content), output)
	for range 20 {
		page, err := s.executionService().Search(t.Context(), runstore.SearchOptions{BatchID: b.BatchID, Queries: []string{"middlefailure"}, Limit: 5})
		require.NoError(t, err)
		require.Nil(t, page.Error)
		if len(page.Results[0].Hits) > 0 {
			require.Equal(t, children[0].RunID, page.Results[0].Hits[0].RunID)
			return
		}
	}
	t.Fatal("lazy batch search did not reach late output")
}

func TestCapturedBatchSearchRetainsHitsBeyondCoverageBudget(t *testing.T) {
	s := newHandoffServer(t, "")
	commands := make([]any, 10)
	for i := range commands {
		commands[i] = "printf ordinary"
	}
	commands[9] = "printf distinctbatchmarker"
	r := callCapturedBatch(t, s, map[string]any{"commands": commands, "queries": []any{"distinctbatchmarker"}})
	require.False(t, r.IsError, resultText(r))
	assertRunBudget(t, r, 8192)
	b := batchResult(t, r)
	require.NotNil(t, b.Search)
	require.True(t, b.Search.CoverageOmitted)
	require.NotEmpty(t, b.Search.Results[0].Hits)
	entries := batchChildren(t, s, b.BatchID)
	require.Equal(t, entries[9].RunID, b.Search.Results[0].Hits[0].RunID)
}
