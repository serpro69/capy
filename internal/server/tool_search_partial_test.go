package server

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/serpro69/capy/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Task 1 (compact-search-replies): an FTS layer failure is reported in-band
// beside the ranked hits that survived it, rather than only logged. Failures
// are injected by dropping layer tables through a second connection.

func dropSearchTables(t *testing.T, path, key string, tables ...string) {
	t.Helper()
	db, err := sql.Open("sqlite3", store.EncryptedDSN(path, key))
	require.NoError(t, err)
	defer db.Close()
	for _, table := range tables {
		_, err := db.ExecContext(t.Context(), "DROP TABLE "+table)
		require.NoError(t, err)
	}
}

// layerFailure matches a reported layer failure for a dropped table. SQLite
// reports a table dropped by another connection either when the statement is
// prepared or while stepping it, so both retrieval error phases are accepted.
func layerFailure(table string) string {
	return `search (query|row iteration): no such table: ` + table
}

func TestSearch_KnowledgeLayerFailure(t *testing.T) {
	srv := newTestServer(t, nil)
	seedDurableDBGuide(t, srv)
	// Durable-only keeps this a knowledge-corpus test, independent of the vault.
	search := func(queries ...any) string {
		srv.throttle.mu.Lock()
		srv.throttle.count = 0
		srv.throttle.mu.Unlock()
		res := callSearch(t, srv, map[string]any{"queries": queries, "include_kinds": []any{"durable"}})
		require.False(t, res.IsError, resultText(res))
		return resultText(res)
	}

	// Control: an intact store yields a clean hit and a clean miss.
	out := search("database", "unmatchedquasar")
	assert.Contains(t, out, "db-guide")
	assert.Contains(t, out, "## 2. unmatchedquasar\nNo results found.")
	assert.NotContains(t, out, "Error:")
	assert.NotContains(t, out, "partial results")

	dropSearchTables(t, srv.config.Store.Path, store.EncryptionKeyFromEnv(), "chunks_trigram")
	out = search("database", "unmatchedquasar")
	assert.Regexp(t, `## 1\. database\n\n⚠ partial results \(knowledge: `+layerFailure("chunks_trigram")+`\)`, out)
	assert.Contains(t, out, "--- [db-guide] ---", "the porter layer's hit survives")
	assert.Regexp(t, `## 2\. unmatchedquasar\nError: knowledge: `+layerFailure("chunks_trigram"), out,
		"a miss beside a failed layer is not a clean miss")
	assert.NotContains(t, out, "No results found.")

	dropSearchTables(t, srv.config.Store.Path, store.EncryptionKeyFromEnv(), "chunks")
	out = search("database")
	assert.Regexp(t, `## 1\. database\nError: knowledge: `+layerFailure("chunks(_trigram)?"), out)
	assert.NotContains(t, out, "--- [db-guide] ---")
	assert.NotContains(t, out, "Refine your query terms", "a failed batch makes no whole-batch miss claim")
}

func TestSearch_VaultLayerFailure(t *testing.T) {
	projectDir, uuid1, _ := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
	srv := newTestServerWithProjectDir(t, nil, projectDir)
	srv.vaultSweep(context.Background())
	seedDurableDBGuide(t, srv)
	search := func() string {
		srv.throttle.mu.Lock()
		srv.throttle.count = 0
		srv.throttle.mu.Unlock()
		res := callSearch(t, srv, map[string]any{"queries": []any{"configure the database"}})
		require.False(t, res.IsError, resultText(res))
		return resultText(res)
	}
	vaultPath := os.Getenv("CAPY_VAULT_PATH")

	out := search()
	require.Contains(t, out, "[session:"+uuid1+"]")
	assert.NotContains(t, out, "partial results")

	// One failed vault layer: the porter session hit survives beside knowledge.
	dropSearchTables(t, vaultPath, testVaultSweepKey, "vault_chunks_trigram")
	out = search()
	assert.Regexp(t, `⚠ partial results \(vault: `+layerFailure("vault_chunks_trigram")+`\)`, out)
	assert.Contains(t, out, "[session:"+uuid1+"]")
	assert.Contains(t, out, "--- [db-guide] ---")

	// A wholly failed vault beside healthy knowledge keeps the knowledge hit.
	dropSearchTables(t, vaultPath, testVaultSweepKey, "vault_chunks")
	out = search()
	assert.Regexp(t, `⚠ partial results \(vault: `+layerFailure("vault_chunks(_trigram)?")+`\)`, out)
	assert.NotContains(t, out, "[session:")
	assert.Contains(t, out, "--- [db-guide] ---")
	assert.Equal(t, 1, strings.Count(out, "partial results"), "one status per query")
	assert.NotContains(t, out, "vault availability check failed", "the scoped preflight does not read chunk tables")
}

func TestVaultSearch_LayerFailure(t *testing.T) {
	projectDir, uuid1, _ := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
	srv := newTestServerWithProjectDir(t, nil, projectDir)
	srv.vaultSweep(context.Background())
	search := func() string {
		res, err := srv.handleVaultSearch(context.Background(), vaultSearchReq(map[string]any{
			"queries": []any{"configure the database", "zzzzz-no-such-term-anywhere"},
		}))
		require.NoError(t, err)
		require.False(t, res.IsError, resultText(res))
		return resultText(res)
	}
	vaultPath := os.Getenv("CAPY_VAULT_PATH")

	out := search()
	assert.Contains(t, out, "[session:"+uuid1+"]")
	assert.Contains(t, out, "## zzzzz-no-such-term-anywhere\nNo results found.")
	assert.NotContains(t, out, "Error:")

	dropSearchTables(t, vaultPath, testVaultSweepKey, "vault_chunks_trigram")
	out = search()
	assert.Regexp(t, `## configure the database\n\n⚠ partial results \(`+layerFailure("vault_chunks_trigram")+`\)`, out)
	assert.Contains(t, out, "[session:"+uuid1+"]", "rows accompanied by a diagnostic reach the formatter")
	assert.Regexp(t, `## zzzzz-no-such-term-anywhere\nError: `+layerFailure("vault_chunks_trigram"), out)

	dropSearchTables(t, vaultPath, testVaultSweepKey, "vault_chunks")
	out = search()
	assert.Regexp(t, `## configure the database\nError: `+layerFailure("vault_chunks(_trigram)?"), out)
	assert.NotContains(t, out, "[session:")
}
