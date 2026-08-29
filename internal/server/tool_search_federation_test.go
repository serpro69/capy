package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Task 8 (A1): capy_search federates the vault chunk corpus with the knowledge
// corpus and RRF-merges the two ranked lists. These tests exercise the seams the
// federation adds: default interleave, the ["durable"] opt-out, the corpus-aware
// empty-KB preflight, and the disabled/backlog messaging that replaces the stale
// knowledge.db session-exclusion copy. They reuse vault_sweep_test.go's fixtures.

// seedDurableDBGuide indexes a durable knowledge source that also matches a
// "configure the database" query, so a federated search can return BOTH a
// knowledge hit and a session hit for the same query.
func seedDurableDBGuide(t *testing.T, srv *Server) {
	t.Helper()
	r := callIndex(t, srv, map[string]any{
		"content": "# Database Setup Guide\n\nSet DATABASE_URL to configure the database connection pool for the service.",
		"source":  "db-guide",
	})
	require.False(t, r.IsError)
}

func TestSearch_FederatesVaultSessionByDefault(t *testing.T) {
	projectDir, uuid1, _ := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)

	srv := newTestServerWithProjectDir(t, nil, projectDir)
	srv.vaultSweep(context.Background()) // archive + chunk-index both fixtures
	seedDurableDBGuide(t, srv)           // a durable hit for the same query

	// Default scope (no include_kinds) must interleave the durable knowledge hit
	// and the archived-session hit for the same query.
	r := callSearch(t, srv, map[string]any{
		"queries": []any{"configure the database"},
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "db-guide", "the durable knowledge hit must appear")
	assert.Contains(t, text, "session:"+uuid1, "the archived session hit must federate in by default")
}

func TestSearch_DurableOnlyOmitsVault(t *testing.T) {
	projectDir, uuid1, _ := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)

	srv := newTestServerWithProjectDir(t, nil, projectDir)
	srv.vaultSweep(context.Background())
	seedDurableDBGuide(t, srv)

	// include_kinds:["durable"] drops session from scope — the vault pass must not
	// run, so no session hit interleaves.
	r := callSearch(t, srv, map[string]any{
		"queries":       []any{"configure the database"},
		"include_kinds": []any{"durable"},
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "db-guide", "durable hit must still appear")
	assert.NotContains(t, text, "session:"+uuid1, `["durable"] must exclude the vault session pass`)
}

func TestSearch_SessionOnlyReturnsVaultHits(t *testing.T) {
	projectDir, uuid1, _ := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)

	srv := newTestServerWithProjectDir(t, nil, projectDir)
	srv.vaultSweep(context.Background())
	seedDurableDBGuide(t, srv) // present, but session-only scope must exclude it

	r := callSearch(t, srv, map[string]any{
		"queries":       []any{"configure the database"},
		"include_kinds": []any{"session"},
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "session:"+uuid1, "session-only scope must return vault hits")
	assert.NotContains(t, text, "db-guide", "session-only scope must exclude the knowledge pass")
}

func TestSearch_VaultOnlyProjectSkipsEmptyKBPreflight(t *testing.T) {
	projectDir, uuid1, _ := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)

	srv := newTestServerWithProjectDir(t, nil, projectDir)
	srv.vaultSweep(context.Background()) // vault populated; knowledge.db left empty

	// With an empty knowledge base but archived sessions in scope, the
	// guide-to-indexing early return must NOT fire (review #4).
	r := callSearch(t, srv, map[string]any{
		"queries": []any{"configure the database"},
	})
	assert.False(t, r.IsError, "a vault-only project must not hit the empty-KB error return")
	text := resultText(r)
	assert.NotContains(t, text, "knowledge base is empty")
	assert.Contains(t, text, "session:"+uuid1, "the vault must serve the search when knowledge is empty")
}

func TestSearch_EmptyKBGuideFiresWhenVaultHasOnlyOtherProjects(t *testing.T) {
	// The vault is cross-project: a fresh project with an empty knowledge base
	// and no sessions of its OWN must still get the guide-to-indexing message,
	// even though the global vault holds other projects' sessions. Guards against
	// a global-count preflight wrongly suppressing the guide (review corroborated).
	_, _, _, _ = setupVaultSweepMultiProject(t) // lays out sessions for projects A + B
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
	t.Setenv("CAPY_VAULT_SWEEP_ALL", "1")

	emptyProject := t.TempDir() // a third project with neither knowledge nor sessions
	srv := newTestServerWithProjectDir(t, nil, emptyProject)
	srv.vaultSweep(context.Background()) // archives A + B; nothing for emptyProject

	r := callSearch(t, srv, map[string]any{
		"queries": []any{"configure the database"},
	})
	require.True(t, r.IsError, "an empty KB with no sessions for THIS project must return the empty-KB guide")
	text := resultText(r)
	assert.Contains(t, text, "knowledge base is empty")

	// Widening to all_projects flips it: the vault can now serve, so no guide.
	r2 := callSearch(t, srv, map[string]any{
		"queries":      []any{"configure the database"},
		"all_projects": true,
	})
	assert.False(t, r2.IsError, "all_projects reaches sibling-project sessions — the guide must not fire")
	assert.NotContains(t, resultText(r2), "knowledge base is empty")
}

func TestSearch_DisabledVaultNamesKey(t *testing.T) {
	// Set the (empty) key BEFORE constructing the server, matching the codebase
	// convention (e.g. TestVaultSweep_SkipsSilentlyWithoutKey) — getVault reads
	// the key lazily, but pinning it up front keeps the disabled state unambiguous.
	t.Setenv("CAPY_VAULT_KEY", "") // vault is opt-in — disabled
	srv := newTestServer(t, nil)
	indexTestContent(t, srv) // keep knowledge non-empty so the preflight passes

	// Session is in the default scope but the vault is disabled and the query
	// matches nothing — the zero-result messaging must name CAPY_VAULT_KEY.
	r := callSearch(t, srv, map[string]any{
		"queries": []any{"xyznonexistentterm123"},
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "No results found")
	assert.Contains(t, text, "CAPY_VAULT_KEY", "a disabled vault must name the enabling env var")
}

func TestSearch_BacklogHintFederated(t *testing.T) {
	projectDir, uuid1, uuid2 := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)

	srv := newTestServerWithProjectDir(t, nil, projectDir)
	srv.vaultSweep(context.Background())

	// Force a reindex backlog: stamp every archived session below the current
	// index version. UpdateSessionFTS also clears its chunk rows, so the vault
	// pass returns nothing and the reindex hint must fire.
	stampVaultIndexVersion(t, uuid1, 1)
	stampVaultIndexVersion(t, uuid2, 1)

	r := callSearch(t, srv, map[string]any{
		"queries": []any{"configure the database"},
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.NotContains(t, text, "knowledge base is empty", "archived sessions exist — must not claim the KB is empty")
	assert.Contains(t, text, "capy vault reindex", "a zero-hit search with a backlog must name the reindex command")
}
