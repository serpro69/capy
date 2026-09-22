package server

import (
	"context"
	"strings"
	"testing"

	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearch_ProjectOverrideScopes(t *testing.T) {
	for _, knowledge := range []string{"empty knowledge", "populated knowledge"} {
		t.Run(knowledge, func(t *testing.T) {
			projectA, uuidA, projectB, uuidB := setupVaultSweepMultiProject(t)
			t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
			t.Setenv("CAPY_VAULT_SWEEP_ALL", "1")
			srv := newTestServerWithProjectDir(t, nil, projectA)
			srv.vaultSweep(t.Context())
			if knowledge == "populated knowledge" {
				seedDurableDBGuide(t, srv)
			}
			const label = `Named CAFÉ %_\'"`
			_, err := srv.getVault().SetSessionProject(t.Context(), uuidA, vault.ProjectOptions{Name: label})
			require.NoError(t, err)
			// A path-looking label on another checkout must not create an implicit association.
			_, err = srv.getVault().SetSessionProject(t.Context(), uuidB, vault.ProjectOptions{Name: projectA})
			require.NoError(t, err)

			for _, tt := range []struct {
				name, query, uuid string
				args              map[string]any
				want, emptyScope  bool
			}{
				{"omitted retains physical session", "database", uuidA, nil, true, false},
				{"empty retains physical session", "database", uuidA, map[string]any{"project": ""}, true, false},
				{"explicit label ASCII folding", "database", uuidA, map[string]any{"project": "NAMED"}, true, false},
				{"literal metacharacters", "database", uuidA, map[string]any{"project": `%_\'"`}, true, false},
				{"no Unicode folding", "database", uuidA, map[string]any{"project": "café"}, false, true},
				{"explicit replaced path", "database", uuidA, map[string]any{"project": projectA}, false, false},
				{"unrelated selector", "database", uuidA, map[string]any{"project": "unrelated"}, false, true},
				{"no label association", "scheduler", uuidB, nil, false, false},
				{"empty has no label association", "scheduler", uuidB, map[string]any{"project": ""}, false, false},
				{"explicit path-looking label", "scheduler", uuidB, map[string]any{"project": projectA}, true, false},
				{"replaced worktree path", "scheduler", uuidB, map[string]any{"project": projectB}, false, true},
				{"star widens", "scheduler", uuidB, map[string]any{"project": "*"}, true, false},
				{"all projects", "scheduler", uuidB, map[string]any{"all_projects": true}, true, false},
				{"widening wins", "scheduler", uuidB, map[string]any{"all_projects": true, "project": "missing"}, true, false},
				{"star overrides false widening", "scheduler", uuidB, map[string]any{"all_projects": false, "project": "*"}, true, false},
				{"false widening keeps default", "scheduler", uuidB, map[string]any{"all_projects": false}, false, false},
				{"metadata is not transcript text", "Named", uuidA, map[string]any{"project": label}, false, false},
			} {
				t.Run(tt.name, func(t *testing.T) {
					// Each row represents a fresh search window; throttling is tested separately.
					srv.throttle.mu.Lock()
					srv.throttle.count = 0
					srv.throttle.mu.Unlock()
					args := map[string]any{"queries": []any{tt.query}}
					for k, v := range tt.args {
						args[k] = v
					}
					res := callSearch(t, srv, args)
					out := resultText(res)
					wantGuide := knowledge == "empty knowledge" && tt.emptyScope
					assert.Equal(t, wantGuide, res.IsError, out)
					assert.Equal(t, wantGuide, strings.Contains(out, "knowledge base is empty"), out)
					assert.Equal(t, tt.want, strings.Contains(out, "[session:"+tt.uuid+"]"), out)
					if tt.want {
						project := label
						if tt.uuid == uuidB {
							project = projectA
						}
						assert.Contains(t, out, "\n"+project+" · ")
					}
					if knowledge == "populated knowledge" && tt.query == "database" {
						assert.Contains(t, out, "db-guide", "project selectors never scope knowledge results")
					}
					assert.Equal(t, projectA, srv.projectDir)
				})
			}
			_, err = srv.getVault().SetSessionProject(t.Context(), uuidA, vault.ProjectOptions{Clear: true})
			require.NoError(t, err)
			res := callSearch(t, srv, map[string]any{"query": "database", "project": projectA})
			assert.False(t, res.IsError)
			assert.Contains(t, resultText(res), "[session:"+uuidA+"]", "clear restores explicit imported-path membership")
		})
	}
}

func TestSearch_VaultAvailabilityFailure(t *testing.T) {
	for _, knowledge := range []string{"empty knowledge", "populated knowledge"} {
		t.Run(knowledge, func(t *testing.T) {
			t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
			srv := newTestServer(t, nil)
			if knowledge == "populated knowledge" {
				seedDurableDBGuide(t, srv)
			}
			// Cache an enabled, unopened vault, then make opening it fail. This
			// fails availability and search independently without damaging a DB.
			require.NotNil(t, srv.getVault())
			t.Setenv("CAPY_VAULT_KEY", "")
			res := callSearch(t, srv, map[string]any{"queries": []any{"database", "unmatchedquasar"}, "project": "named"})
			out := resultText(res)
			assert.NotContains(t, out, "knowledge base is empty")
			assert.Equal(t, 1, strings.Count(out, "vault availability check failed"), out)
			assert.Contains(t, out, "CAPY_VAULT_KEY")
			assert.Contains(t, out, "## unmatchedquasar\nError: vault:", "the actual vault pass must still run")
			if knowledge == "populated knowledge" {
				assert.False(t, res.IsError, "successful knowledge results stay usable")
				assert.Contains(t, out, "db-guide")
				assert.Contains(t, out, "partial results (vault:")
			} else {
				assert.Contains(t, out, "## database\nError: vault:")
			}

			// Source and kind opt-outs must bypass availability as well as retrieval.
			for _, args := range []map[string]any{
				{"query": "database", "source": "db-guide", "project": "named"},
				{"query": "database", "include_kinds": []any{"durable"}, "project": "named"},
			} {
				out := resultText(callSearch(t, srv, args))
				assert.NotContains(t, out, "vault availability")
				assert.NotContains(t, out, "Error: vault:")
				if knowledge == "populated knowledge" {
					assert.Contains(t, out, "db-guide")
				}
			}
		})
	}
}

func TestSearch_ProjectHelp(t *testing.T) {
	tool := toolSearch()
	require.Len(t, tool.InputSchema.Properties, 6, "no new MCP argument")
	prop, ok := tool.InputSchema.Properties["project"].(map[string]any)
	require.True(t, ok)
	help, ok := prop["description"].(string)
	require.True(t, ok)
	for _, text := range []string{"literal substring", "effective project", "custom label", "Omitted or empty", "imported path", `Exact "*" is reserved`, "all_projects: true overrides", "No effect on the knowledge pass"} {
		assert.Contains(t, help, text)
	}
}

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

// Task 11.1: the federated path renders vault hits through the same
// formatVaultHit, so a Claude session hit in capy_search carries its platform
// token and the session:<uuid> tag is unchanged.
func TestSearch_FederatedVaultHitCarriesPlatform(t *testing.T) {
	projectDir, uuid1, _ := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)

	srv := newTestServerWithProjectDir(t, nil, projectDir)
	srv.vaultSweep(context.Background())
	seedDurableDBGuide(t, srv)

	r := callSearch(t, srv, map[string]any{
		"queries": []any{"configure the database"},
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "[session:"+uuid1+"]")
	assert.Contains(t, text, " · claude-code\n", "a Claude vault hit is labeled with its stored platform token")
	assert.NotContains(t, text, "child of", "a top-level Claude session has no parent marker")
}
