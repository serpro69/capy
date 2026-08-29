package server

import (
	"context"
	"os"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Task 7: capy_vault_search runs the chunk corpus through the shared retrieval
// engine and degrades loudly when the vault is disabled or holds a reindex
// backlog. These tests mirror vault_sweep_test.go's fixture layout.

// vaultSearchReq builds a capy_vault_search CallToolRequest from raw args.
func vaultSearchReq(args map[string]any) mcp.CallToolRequest {
	var req mcp.CallToolRequest
	req.Params.Name = "capy_vault_search"
	req.Params.Arguments = args
	return req
}

func TestVaultSearch_ArchivedSessionsReturnHits(t *testing.T) {
	projectDir, uuid1, _ := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)

	srv := newTestServerWithProjectDir(t, nil, projectDir)
	srv.vaultSweep(context.Background()) // archive + chunk-index both fixtures

	// Default scope = current project. The database-configuration fixture (uuid1)
	// must rank for a query about configuring the database.
	res, err := srv.handleVaultSearch(context.Background(), vaultSearchReq(map[string]any{
		"queries": []any{"configure the database"},
	}))
	require.NoError(t, err)
	require.False(t, res.IsError, "a populated vault must not return an error result")

	out := resultText(res)
	assert.Contains(t, out, "session:"+uuid1, "the database session should be a ranked hit")
	assert.NotContains(t, out, "No results found.")
}

func TestVaultSearch_AllProjectsWidensScope(t *testing.T) {
	projectA, _, _, uuidB := setupVaultSweepMultiProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
	t.Setenv("CAPY_VAULT_SWEEP_ALL", "1")

	srv := newTestServerWithProjectDir(t, nil, projectA)
	srv.vaultSweep(context.Background())

	// The goroutine-scheduling fixture lives in project B. A default (current
	// project = A) search must miss it; all_projects must find it.
	scoped, err := srv.handleVaultSearch(context.Background(), vaultSearchReq(map[string]any{
		"queries": []any{"goroutine scheduler work stealing"},
	}))
	require.NoError(t, err)
	assert.NotContains(t, resultText(scoped), "session:"+uuidB,
		"default scope is the current project — project B must not appear")

	widened, err := srv.handleVaultSearch(context.Background(), vaultSearchReq(map[string]any{
		"queries":      []any{"goroutine scheduler work stealing"},
		"all_projects": true,
	}))
	require.NoError(t, err)
	assert.Contains(t, resultText(widened), "session:"+uuidB,
		"all_projects must reach sibling-project sessions")
}

func TestVaultSearch_ProjectStarWidensScope(t *testing.T) {
	projectA, _, _, uuidB := setupVaultSweepMultiProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
	t.Setenv("CAPY_VAULT_SWEEP_ALL", "1")

	srv := newTestServerWithProjectDir(t, nil, projectA)
	srv.vaultSweep(context.Background())

	// project:"*" is the string equivalent of all_projects:true.
	res, err := srv.handleVaultSearch(context.Background(), vaultSearchReq(map[string]any{
		"queries": []any{"goroutine scheduler work stealing"},
		"project": "*",
	}))
	require.NoError(t, err)
	assert.Contains(t, resultText(res), "session:"+uuidB,
		`project:"*" must reach sibling-project sessions`)
}

func TestVaultSearch_NoQuery(t *testing.T) {
	projectDir, _, _ := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
	srv := newTestServerWithProjectDir(t, nil, projectDir)

	res, err := srv.handleVaultSearch(context.Background(), vaultSearchReq(map[string]any{}))
	require.NoError(t, err)
	require.True(t, res.IsError)
	assert.Contains(t, resultText(res), "provide query or queries")
}

func TestVaultSearch_InvalidDate(t *testing.T) {
	projectDir, _, _ := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
	srv := newTestServerWithProjectDir(t, nil, projectDir)
	srv.vaultSweep(context.Background())

	res, err := srv.handleVaultSearch(context.Background(), vaultSearchReq(map[string]any{
		"queries": []any{"database"},
		"after":   "not-a-date",
	}))
	require.NoError(t, err)
	require.True(t, res.IsError)
	assert.Contains(t, resultText(res), "invalid date")
}

func TestVaultSearch_DisabledWithoutKey(t *testing.T) {
	projectDir, _, _ := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", "") // vault is opt-in

	srv := newTestServerWithProjectDir(t, nil, projectDir)

	res, err := srv.handleVaultSearch(context.Background(), vaultSearchReq(map[string]any{
		"queries": []any{"anything"},
	}))
	require.NoError(t, err)
	require.True(t, res.IsError, "disabled vault must return an error result")
	assert.Contains(t, resultText(res), "CAPY_VAULT_KEY")

	// Degrade-loudly must not create the vault file as a side effect.
	_, statErr := os.Stat(vault.VaultDBPath())
	assert.True(t, os.IsNotExist(statErr), "a disabled search must not create the vault DB")
}

func TestVaultSearch_BacklogHintOnZeroHits(t *testing.T) {
	projectDir, uuid1, uuid2 := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)

	srv := newTestServerWithProjectDir(t, nil, projectDir)
	srv.vaultSweep(context.Background())

	// Force a reindex backlog: stamp every archived session below the current
	// index version so it counts as OutdatedSessions. UpdateSessionFTS also
	// clears its chunk rows, which only reinforces the zero-hit path.
	stampVaultIndexVersion(t, uuid1, 1)
	stampVaultIndexVersion(t, uuid2, 1)

	// A query that matches nothing must still surface the reindex hint.
	res, err := srv.handleVaultSearch(context.Background(), vaultSearchReq(map[string]any{
		"queries":      []any{"zzzzz-no-such-term-anywhere"},
		"all_projects": true, // widen so scope can't be blamed for the miss
	}))
	require.NoError(t, err)
	require.False(t, res.IsError, "a zero-hit search with a backlog is informational, not an error result")
	out := resultText(res)
	assert.Contains(t, out, "No results found.")
	assert.Contains(t, out, "capy vault reindex", "a zero-hit search with a backlog must name the reindex command")
}
