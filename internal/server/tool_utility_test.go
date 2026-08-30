package server

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/store"
	"github.com/serpro69/capy/internal/vault"
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

func TestStats_WithSessionSources(t *testing.T) {
	srv := newTestServer(t, nil)
	st := srv.getStore()

	_, err := st.Index("session transcript content", "session:2026-05-01T10:00:00Z:abc-123", "session", store.KindSession)
	require.NoError(t, err)

	r := callStats(t, srv)
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "session: 1")
	assert.Contains(t, text, "Session TTL buckets")
	assert.Contains(t, text, "fresh")
	// The knowledge.db session sweep is gone (vault-session-search D8); these
	// rows are legacy leftovers, so the tier is marked draining and names the
	// reclaim command.
	assert.Contains(t, text, "legacy")
	assert.Contains(t, text, "capy_cleanup purge_session")
}

func TestStats_SessionSectionOmittedWhenNoSessions(t *testing.T) {
	srv := newTestServer(t, nil)

	callIndex(t, srv, map[string]any{
		"content": "# Durable only content for this test.",
		"source":  "durable-only",
	})

	r := callStats(t, srv)
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.NotContains(t, text, "Session TTL buckets")
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

func TestDoctor_VaultDisabledWithoutKey(t *testing.T) {
	t.Setenv("CAPY_VAULT_KEY", "")
	srv := newTestServer(t, nil)
	r := callDoctor(t, srv)
	text := resultText(r)
	assert.Contains(t, text, "Vault:")
	assert.Contains(t, text, "disabled (CAPY_VAULT_KEY not set)")
}

func TestDoctor_LegacySessionReclaimHint(t *testing.T) {
	// Legacy knowledge.db session rows (pre-D8 sweep leftovers) must surface a
	// "Legacy sessions" check naming the reclaim command — the doctor reports
	// both this and the vault reindex backlog (design D4).
	// CAPY_VAULT_KEY intentionally left as-is: this exercises the knowledge-DB
	// path (doctor reads kbStats, not the vault), so the vault state is irrelevant.
	srv := newTestServer(t, nil)
	st := srv.getStore()
	_, err := st.Index("legacy session transcript content", "session:2026-05-01T10:00:00Z:legacy-1", "session", store.KindSession)
	require.NoError(t, err)

	text := resultText(callDoctor(t, srv))
	assert.Contains(t, text, "Legacy sessions:")
	assert.Contains(t, text, "capy_cleanup purge_session")
}

func TestDoctor_NoLegacySessionHintWhenClean(t *testing.T) {
	srv := newTestServer(t, nil)
	callIndex(t, srv, map[string]any{
		"content": "# Durable only content, no session rows.",
		"source":  "durable-only",
	})
	text := resultText(callDoctor(t, srv))
	assert.NotContains(t, text, "Legacy sessions:")
}

func TestDoctor_VaultReindexBacklogHint(t *testing.T) {
	// An archived session below currentIndexVersion must surface the backlog
	// and name the command that clears it (design D4: backfill is not silent).
	srv, uuid := newTestServerWithArchivedSession(t)

	stampVaultIndexVersion(t, uuid, 1)

	r := callDoctor(t, srv)
	text := resultText(r)
	assert.Contains(t, text, "Vault:")
	assert.Contains(t, text, "capy vault reindex")

	// Once nothing is below current, the hint disappears.
	stampVaultIndexVersion(t, uuid, 999)
	text = resultText(callDoctor(t, srv))
	assert.Contains(t, text, "1 sessions archived")
	assert.NotContains(t, text, "capy vault reindex")
}

func TestStats_VaultReindexBacklog(t *testing.T) {
	srv, uuid := newTestServerWithArchivedSession(t)
	stampVaultIndexVersion(t, uuid, 1)

	text := resultText(callStats(t, srv))
	assert.Contains(t, text, "### Vault")
	assert.Contains(t, text, "Archived sessions")
	assert.Contains(t, text, "Reindex backlog")
	assert.Contains(t, text, "capy vault reindex")
}

// newTestServerWithArchivedSession builds a server whose (temp) vault holds one
// archived session, returning the session UUID. The record carries no FTS/chunk
// rows on purpose — these tests validate only the index_version backlog
// reporting; full chunk production through import is covered in the vault
// package (chunker_test.go).
func newTestServerWithArchivedSession(t *testing.T) (*Server, string) {
	t.Helper()
	t.Setenv("CAPY_VAULT_KEY", "test-vault-doctor-key-at-least-32-chars!!")
	srv := newTestServer(t, nil)

	st := vault.NewVaultStore(vault.VaultDBPath())
	defer func() { require.NoError(t, st.Close()) }()
	uuid := "doctor00-1111-2222-3333-444444444444"
	rec := &vault.SessionRecord{Session: vault.Session{
		UUID: uuid, ContentHash: "h", MachineID: "m", ClaudeProjectDir: "-p",
		ProjectPath: "/p", MessageCount: 1, IndexVersion: 1,
		RawJSONL: []byte(`{"type":"user"}` + "\n"),
	}}
	require.NoError(t, st.InsertSession(context.Background(), rec))
	return srv, uuid
}

// stampVaultIndexVersion sets a session's index_version (simulating an older or
// newer indexer) through a short-lived store handle. Note: UpdateSessionFTS with
// nil rows destructively replaces the session's FTS/chunk rows wholesale — fine
// here, since the fixture session never had any.
func stampVaultIndexVersion(t *testing.T, uuid string, version int) {
	t.Helper()
	st := vault.NewVaultStore(vault.VaultDBPath())
	defer func() { require.NoError(t, st.Close()) }()
	require.NoError(t, st.UpdateSessionFTS(context.Background(), uuid, version, nil, nil))
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
	assert.Contains(t, text, "No evictable sources")
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
		"dry_run": false,
	})
	assert.False(t, r.IsError)
}

func TestCleanup_PurgeBothMutuallyExclusive(t *testing.T) {
	srv := newTestServer(t, nil)

	r := callCleanup(t, srv, map[string]any{"purge_ephemeral": true, "purge_session": true})
	assert.True(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "mutually exclusive")
}

func TestCleanup_PurgeSessionAccepted(t *testing.T) {
	srv := newTestServer(t, nil)

	// purge_session parameter is accepted; no stale sessions exist so nothing evicted.
	r := callCleanup(t, srv, map[string]any{"purge_session": true})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "No evictable sources")
}

func TestCleanup_PurgeSessionPreservesFreshSessions(t *testing.T) {
	srv := newTestServer(t, nil)
	st := srv.getStore()

	// Index a fresh session source — should NOT be evicted (within TTL).
	_, err := st.Index("session transcript content", "session:2026-05-01T00:00:00Z:test-uuid", "session", store.KindSession)
	require.NoError(t, err)

	r := callCleanup(t, srv, map[string]any{"purge_session": true, "dry_run": false})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "No evictable sources")
}

func TestCleanup_PurgeSessionDoesNotTouchDurable(t *testing.T) {
	srv := newTestServer(t, nil)
	st := srv.getStore()

	// Index a very old durable source (would be evictable by retention).
	_, err := st.Index("old durable content", "old-durable-source", "plaintext", store.KindDurable)
	require.NoError(t, err)

	// purge_session should not touch durable sources at all.
	r := callCleanup(t, srv, map[string]any{"purge_session": true, "dry_run": false})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.NotContains(t, text, "old-durable-source")
}

func TestCleanup_StatsTracking(t *testing.T) {
	srv := newTestServer(t, nil)
	callCleanup(t, srv, map[string]any{})
	snap := srv.stats.Snapshot()
	assert.Equal(t, 1, snap.Calls["capy_cleanup"])
}

// ─── purge_all (Task 9 full project-scope reset) ─────────────────────────

func TestCleanup_PurgeAllMutuallyExclusive(t *testing.T) {
	srv := newTestServer(t, nil)

	cases := []map[string]any{
		{"purge_all": true, "source": "some-label"},
		{"purge_all": true, "purge_ephemeral": true},
		{"purge_all": true, "purge_session": true},
	}
	for _, args := range cases {
		r := callCleanup(t, srv, args)
		assert.True(t, r.IsError, "expected error for %v", args)
		assert.Contains(t, resultText(r), "purge_all cannot be combined")
	}
}

func TestCleanup_PurgeAllDryRun(t *testing.T) {
	srv := newTestServer(t, nil)
	callIndex(t, srv, map[string]any{
		"content": "# Reset target\n\nauthentication middleware validates tokens.",
		"source":  "purge-dry-src",
	})

	// dry_run defaults to true — should report counts but delete nothing.
	r := callCleanup(t, srv, map[string]any{"purge_all": true})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "purge all) preview (dry run)")
	assert.Contains(t, text, "Would purge 1 sources")

	// Source must still be searchable after a dry run.
	sr := callSearch(t, srv, map[string]any{"queries": []any{"authentication middleware"}})
	assert.NotContains(t, resultText(sr), "No results found")
}

func TestCleanup_PurgeAllResetsKnowledgeBaseAndStats(t *testing.T) {
	srv := newTestServer(t, nil)

	callIndex(t, srv, map[string]any{
		"content": "# Reset target\n\nauthentication middleware validates tokens.",
		"source":  "purge-src",
	})
	callSearch(t, srv, map[string]any{"queries": []any{"authentication"}})

	// Sanity: stats reflect the index + search before the purge.
	pre := srv.stats.Snapshot()
	require.Equal(t, 1, pre.Calls["capy_index"])
	require.Equal(t, 1, pre.Calls["capy_search"])
	require.Greater(t, pre.BytesIndexed, int64(0))

	r := callCleanup(t, srv, map[string]any{"purge_all": true, "dry_run": false})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "Knowledge base reset")
	assert.Contains(t, text, "Purged 1 sources")

	// Knowledge base is fully empty — search returns the onboarding guidance
	// rather than the indexed content.
	sr := callSearch(t, srv, map[string]any{"queries": []any{"authentication"}})
	srText := resultText(sr)
	assert.Contains(t, srText, "knowledge base is empty")
	assert.NotContains(t, srText, "validates tokens")

	// Stats are reset: prior index/search counters are gone. SessionStart is
	// preserved, and the cleanup call's own tracking (recorded after Reset)
	// plus this post-purge search are the only surviving counts.
	post := srv.stats.Snapshot()
	assert.Equal(t, 0, post.Calls["capy_index"])
	assert.Equal(t, int64(0), post.BytesIndexed)
	assert.Equal(t, 1, post.Calls["capy_cleanup"], "cleanup tracks itself after Reset")
	assert.False(t, post.SessionStart.IsZero(), "SessionStart must survive a reset")
}
