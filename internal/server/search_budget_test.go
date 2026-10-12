package server

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearchBudgetBoundaries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, tt := range []struct {
		name   string
		policy config.SearchConfig
	}{
		{"defaults", config.DefaultConfig().Search},
		{"overridden", config.SearchConfig{WindowMS: 1500, TaperAfter: 1, BlockAfter: 2}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(t, nil)
			srv.throttle.policy = tt.policy
			now := time.Unix(100, 0)
			srv.throttle.windowStart = now
			srv.throttle.now = func() time.Time { return now }
			for _, label := range []string{"alpha", "beta", "gamma"} {
				r := callIndex(t, srv, map[string]any{"content": "budgetmarker " + label, "source": label})
				require.False(t, r.IsError)
			}
			for call := 1; call <= tt.policy.BlockAfter+2; call++ {
				r := callSearch(t, srv, map[string]any{"query": "budgetmarker", "include_kinds": []string{"durable"}})
				out := resultText(r)
				limit := 2
				if call > tt.policy.TaperAfter {
					limit = 1
				}
				if call > tt.policy.BlockAfter {
					limit = 0
				}
				assert.Equal(t, limit == 0, r.IsError, out)
				assert.Equal(t, limit, strings.Count(out, "--- ["), "actual hit count: %s", out)
				assert.Contains(t, out, fmt.Sprintf("search call #%d/%d", call, tt.policy.BlockAfter))
				assert.Contains(t, out, fmt.Sprintf("Results limited to %d/query", limit))
				assert.Contains(t, out, fmt.Sprintf("Untapered calls left: %d; calls before block: %d", max(0, tt.policy.TaperAfter-call), max(0, tt.policy.BlockAfter-call)))
				assert.Contains(t, out, fmt.Sprintf("resets in %d ms (window %d ms; taper after %d; block after %d)", tt.policy.WindowMS, tt.policy.WindowMS, tt.policy.TaperAfter, tt.policy.BlockAfter))
				assert.Contains(t, out, "Shared MCP server")
			}
			now = now.Add(time.Duration(tt.policy.WindowMS)*time.Millisecond - time.Nanosecond)
			r := callSearch(t, srv, map[string]any{"query": "budgetmarker"})
			assert.True(t, r.IsError)
			assert.Contains(t, resultText(r), "resets in 1 ms")
			now = now.Add(time.Nanosecond)
			r = callSearch(t, srv, map[string]any{"query": "budgetmarker", "limit": 1})
			assert.False(t, r.IsError)
			assert.Contains(t, resultText(r), "search call #1/")
			assert.Contains(t, resultText(r), "Results limited to 1/query")
		})
	}
}

func TestSearchBudgetInvalidRequests(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := newTestServer(t, nil)
	for _, tt := range []struct {
		name string
		args map[string]any
	}{
		{"missing query", nil},
		{"blank query", map[string]any{"query": "  "}},
		{"null query", map[string]any{"query": nil}},
		{"null queries with fallback", map[string]any{"queries": nil, "query": "marker"}},
		{"mixed queries", map[string]any{"queries": []any{"marker", 1}}},
		{"serialized mixed queries", map[string]any{"queries": `["marker",null]`}},
		{"empty member", map[string]any{"queries": []any{"marker", ""}}},
		{"bad source", map[string]any{"query": "marker", "source": 1}},
		{"bad project", map[string]any{"query": "marker", "project": false, "all_projects": true}},
		{"bad all projects", map[string]any{"query": "marker", "all_projects": 1}},
		{"bad kinds shape", map[string]any{"query": "marker", "include_kinds": 1}},
		{"null kinds", map[string]any{"query": "marker", "include_kinds": nil}},
		{"mixed kinds", map[string]any{"query": "marker", "include_kinds": []any{"durable", 1}}},
		{"unknown kind", map[string]any{"query": "marker", "include_kinds": []string{"bogus"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := callSearch(t, srv, tt.args)
			assert.True(t, r.IsError)
			assert.NotContains(t, resultText(r), "search budget")
		})
	}
	for _, limit := range []any{nil, 0, -1, 1.5, math.NaN(), math.Inf(1), "NaN", "Inf", "1e999", "bad", true, []any{1}} {
		t.Run(fmt.Sprintf("limit %v", limit), func(t *testing.T) {
			r := callSearch(t, srv, map[string]any{"query": "marker", "limit": limit})
			assert.True(t, r.IsError)
			assert.Contains(t, resultText(r), `invalid parameter "limit"`)
		})
	}
	assert.Nil(t, srv.store)
	assert.Nil(t, srv.vault)
	assert.Empty(t, srv.stats.Snapshot().Calls)
	r := callSearch(t, srv, map[string]any{"queries": `["marker"]`, "limit": "1", "include_kinds": `["durable"]`})
	assert.Contains(t, resultText(r), "search call #1/8")
	assert.Contains(t, resultText(r), "Results limited to 1/query")
}

func TestSearchBudgetResponsePaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, path := range []string{"empty", "no results", "hits", "partial", "failed"} {
		t.Run(path, func(t *testing.T) {
			srv := newTestServer(t, nil)
			if path == "no results" || path == "hits" || path == "partial" {
				seedDurableDBGuide(t, srv)
			}
			if path == "partial" || path == "failed" {
				t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
				require.NotNil(t, srv.getVault())
				t.Setenv("CAPY_VAULT_KEY", "")
			}
			query := "database"
			if path == "no results" {
				query = "unmatchedquasar"
			}
			r := callSearch(t, srv, map[string]any{"query": query})
			out := resultText(r)
			assert.Equal(t, 1, strings.Count(out, "Shared MCP server search budget:"), out)
			assert.Contains(t, out, "search call #1/8")
			assert.Contains(t, out, "Results limited to 2/query")
			switch path {
			case "empty":
				assert.Contains(t, out, "knowledge base is empty")
			case "no results":
				assert.Contains(t, out, "No results found")
			case "hits":
				assert.Contains(t, out, "db-guide")
			case "partial":
				assert.Contains(t, out, "partial results (vault:")
			case "failed":
				assert.Contains(t, out, "Error: vault:")
			}
			assert.Equal(t, int64(len(out)), srv.stats.Snapshot().BytesReturned["capy_search"])
		})
	}
}

func TestSearchBudgetConcurrentCalls(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := newTestServer(t, nil)
	now := time.Unix(100, 0)
	srv.throttle.windowStart = now.Add(-time.Minute)
	srv.throttle.now = func() time.Time { return now }
	seedDurableDBGuide(t, srv)
	const calls = 32
	results := make([]*mcp.CallToolResult, calls)
	errs := make([]error, calls)
	var wg sync.WaitGroup
	for i := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := mcp.CallToolRequest{}
			req.Params.Arguments = map[string]any{"query": "database", "include_kinds": []string{"durable"}}
			results[i], errs[i] = srv.handleSearch(t.Context(), req)
		}()
	}
	wg.Wait()
	seen := make(map[int]bool)
	for i, r := range results {
		require.NoError(t, errs[i])
		out := resultText(r)
		var count, threshold, limit int
		_, err := fmt.Sscanf(strings.Split(out, "search call #")[1], "%d/%d in this window. Results limited to %d/query.", &count, &threshold, &limit)
		require.NoError(t, err)
		assert.False(t, seen[count], "duplicate count %d", count)
		seen[count] = true
		assert.Equal(t, 8, threshold)
		assert.Equal(t, count > 8, r.IsError)
		switch {
		case count > 8:
			assert.Equal(t, 0, limit)
		case count > 3:
			assert.Equal(t, 1, limit)
		default:
			assert.Equal(t, 2, limit)
		}
		assert.Contains(t, out, "resets in 60000 ms")
	}
	assert.Len(t, seen, calls)
}

func TestSearchBudgetCapturedPolicy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Search = config.SearchConfig{WindowMS: 2000, TaperAfter: 1, BlockAfter: 2}
	srv := NewServer(cfg, nil, nil, t.TempDir())
	cfg.Search = config.DefaultConfig().Search
	first := srv.throttle.advance(2)
	second := srv.throttle.advance(2)
	third := srv.throttle.advance(2)
	assert.Equal(t, 2, first.limit)
	assert.Equal(t, 1, second.limit)
	assert.Zero(t, third.limit)
	assert.Contains(t, third.notice(), "window 2000 ms; taper after 1; block after 2")
}
