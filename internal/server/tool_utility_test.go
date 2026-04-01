package server

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── Stats tests ───────────────────────────────────────────────────────────────

func callStats(t *testing.T, srv *Server) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{}
	result, err := srv.handleStats(context.Background(), req)
	require.NoError(t, err)
	return result
}

func TestStats_Empty(t *testing.T) {
	srv := newTestServer(t, nil)
	r := callStats(t, srv)
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "Session Report")
	assert.Contains(t, text, "No capy tool calls yet")
}

func TestStats_WithCalls(t *testing.T) {
	srv := newTestServer(t, nil)

	// Make some tool calls to populate stats
	callTool(t, srv, map[string]any{
		"language": "shell",
		"code":     "echo hello",
	})
	callTool(t, srv, map[string]any{
		"language": "shell",
		"code":     "echo world",
	})

	r := callStats(t, srv)
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "Context Window Protection")
	assert.Contains(t, text, "capy_execute")
	assert.Contains(t, text, "Total data processed")
	assert.Contains(t, text, "Context savings")
}

func TestStats_WithKBStats(t *testing.T) {
	srv := newTestServer(t, nil)

	// Index something to initialize the store and populate KB
	callIndex(t, srv, map[string]any{
		"content": "# Test\n\nSome content for KB stats.",
		"source":  "test-stats",
	})

	r := callStats(t, srv)
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "Knowledge Base")
	assert.Contains(t, text, "Sources")
	assert.Contains(t, text, "Chunks")
}

func TestStats_SavingsCalculation(t *testing.T) {
	srv := newTestServer(t, nil)

	// Simulate: some bytes indexed, some returned
	srv.stats.AddBytesIndexed(10000)
	srv.stats.AddBytesSandboxed(5000)
	srv.stats.TrackResponse("capy_execute", 500)

	r := callStats(t, srv)
	text := resultText(r)
	// keptOut = 10000+5000 = 15000, totalProcessed = 15500, returned = 500
	// reduction = (1 - 500/15500)*100 = ~96.8%
	assert.Contains(t, text, "reduction")
	assert.Contains(t, text, "sandbox")
}

func TestStats_CacheSectionIncluded(t *testing.T) {
	srv := newTestServer(t, nil)

	// Simulate cache hits
	srv.stats.AddCacheHit(3200)
	srv.stats.AddCacheHit(4800)
	srv.stats.TrackResponse("capy_execute", 100)

	r := callStats(t, srv)
	text := resultText(r)
	assert.Contains(t, text, "TTL Cache")
	assert.Contains(t, text, "Cache hits")
	assert.Contains(t, text, "Data avoided by cache")
	assert.Contains(t, text, "Network requests saved")
	assert.Contains(t, text, "TTL remaining")
}

func TestStats_CacheSectionOmittedWhenNoCacheHits(t *testing.T) {
	srv := newTestServer(t, nil)
	srv.stats.TrackResponse("capy_execute", 100)

	r := callStats(t, srv)
	text := resultText(r)
	assert.NotContains(t, text, "TTL Cache")
}

func TestStats_CacheBytesSavedInSavingsCalc(t *testing.T) {
	srv := newTestServer(t, nil)

	// 10KB indexed, 500B returned, 5KB cache saved
	srv.stats.AddBytesIndexed(10000)
	srv.stats.AddCacheHit(5000)
	srv.stats.TrackResponse("capy_execute", 500)

	r := callStats(t, srv)
	text := resultText(r)
	// totalProcessed = 10000 + 0 + 500 + 5000 = 15500
	// reduction = (1 - 500/15500)*100 = ~96.8%
	assert.Contains(t, text, "reduction")
}

func TestStats_FormatBytes(t *testing.T) {
	assert.Equal(t, "0.0KB", formatBytes(0))
	assert.Equal(t, "1.0KB", formatBytes(1024))
	assert.Equal(t, "1.0MB", formatBytes(1024*1024))
	assert.Equal(t, "2.5MB", formatBytes(int64(2.5*1024*1024)))
}

// ─── Doctor tests ──────────────────────────────────────────────────────────────

func callDoctor(t *testing.T, srv *Server) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{}
	result, err := srv.handleDoctor(context.Background(), req)
	require.NoError(t, err)
	return result
}

func TestDoctor_BasicOutput(t *testing.T) {
	srv := newTestServer(t, nil)
	r := callDoctor(t, srv)
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "capy doctor")
	assert.Contains(t, text, "Version:")
	assert.Contains(t, text, "Runtimes:")
	assert.Contains(t, text, "FTS5:")
	assert.Contains(t, text, "Config:")
	assert.Contains(t, text, "Project:")
}

func TestDoctor_RuntimesDetected(t *testing.T) {
	srv := newTestServer(t, nil)
	r := callDoctor(t, srv)
	text := resultText(r)
	// At minimum shell should be available
	assert.Contains(t, text, "shell")
	assert.Contains(t, text, "[x] Runtimes:")
}

func TestDoctor_FTS5Available(t *testing.T) {
	srv := newTestServer(t, nil)
	r := callDoctor(t, srv)
	text := resultText(r)
	assert.Contains(t, text, "[x] FTS5: available")
}

func TestDoctor_KBNotInitialized(t *testing.T) {
	srv := newTestServer(t, nil)
	r := callDoctor(t, srv)
	text := resultText(r)
	// Store hasn't been used yet — should show lazy init message
	// Note: doctor calls getStore() for FTS5 check, which initializes it.
	// So KB status will show as initialized with 0 sources.
	assert.Contains(t, text, "Knowledge base:")
}

func TestDoctor_ChecklistFormat(t *testing.T) {
	srv := newTestServer(t, nil)
	r := callDoctor(t, srv)
	text := resultText(r)
	// All lines should be checklist items (- [x], - [ ], - [-])
	for _, line := range strings.Split(text, "\n") {
		if line == "" || strings.HasPrefix(line, "##") {
			continue
		}
		assert.True(t,
			strings.HasPrefix(line, "- [x]") ||
				strings.HasPrefix(line, "- [ ]") ||
				strings.HasPrefix(line, "- [-]"),
			"expected checklist format, got: %s", line)
	}
}

// ─── Cleanup tests ─────────────────────────────────────────────────────────────

func callCleanup(t *testing.T, srv *Server, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	result, err := srv.handleCleanup(context.Background(), req)
	require.NoError(t, err)
	return result
}

func TestCleanup_DryRun_NoColdSources(t *testing.T) {
	srv := newTestServer(t, nil)

	// Index something fresh — won't be cold
	callIndex(t, srv, map[string]any{
		"content": "# Fresh content",
		"source":  "fresh-docs",
	})

	r := callCleanup(t, srv, map[string]any{})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "No cold sources")
}

func TestCleanup_DryRunDefault(t *testing.T) {
	srv := newTestServer(t, nil)

	// No args — dry_run should default to true
	r := callCleanup(t, srv, map[string]any{})
	assert.False(t, r.IsError)
	// Should not contain "removed" (actual deletion)
	assert.NotContains(t, resultText(r), "sources removed")
}

func TestCleanup_ExplicitDryRunFalse(t *testing.T) {
	srv := newTestServer(t, nil)

	// Nothing to clean but verify the parameter is accepted
	r := callCleanup(t, srv, map[string]any{
		"dry_run":      false,
		"max_age_days": float64(1),
	})
	assert.False(t, r.IsError)
}

func TestCleanup_CustomMaxAge(t *testing.T) {
	srv := newTestServer(t, nil)
	r := callCleanup(t, srv, map[string]any{
		"max_age_days": float64(7),
	})
	assert.False(t, r.IsError)
	assert.Contains(t, resultText(r), "7 days")
}

func TestCleanup_StatsTracking(t *testing.T) {
	srv := newTestServer(t, nil)
	callCleanup(t, srv, map[string]any{})
	snap := srv.stats.Snapshot()
	assert.Equal(t, 1, snap.Calls["capy_cleanup"])
}
