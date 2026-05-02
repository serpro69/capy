package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewServer(t *testing.T) {
	cfg := config.DefaultConfig()
	policies := []security.SecurityPolicy{
		{Deny: []string{"Bash(sudo *)"}},
	}
	exec := executor.NewExecutor(t.TempDir(), 0)

	srv := NewServer(cfg, policies, exec, "/tmp/test")
	require.NotNil(t, srv)
	assert.NotNil(t, srv.stats)
	assert.NotNil(t, srv.throttle)
	assert.Equal(t, "/tmp/test", srv.projectDir)
	assert.Nil(t, srv.store, "store should be nil until lazy init")
}

func TestGetStore_LazyInit(t *testing.T) {
	t.Setenv("CAPY_DB_KEY", "test-passphrase-at-least-32-characters-long!!")
	cfg := config.DefaultConfig()
	// Override DB path to temp dir so it doesn't pollute real XDG
	cfg.Store.Path = t.TempDir() + "/test.db"

	srv := NewServer(cfg, nil, nil, t.TempDir())

	assert.Nil(t, srv.store)
	st := srv.getStore()
	assert.NotNil(t, st)

	// Second call returns same instance
	st2 := srv.getStore()
	assert.Same(t, st, st2)

	_ = st.Close()
}

func TestToolRegistration(t *testing.T) {
	cfg := config.DefaultConfig()
	exec := executor.NewExecutor(t.TempDir(), 0)
	srv := NewServer(cfg, nil, exec, t.TempDir())

	// Registering tools should not panic
	require.NotPanics(t, func() {
		srv.registerToolsForTest()
	})
}

func TestShutdownCheckpointsWAL(t *testing.T) {
	t.Setenv("CAPY_DB_KEY", "test-passphrase-at-least-32-characters-long!!")
	projectDir := t.TempDir()
	dbPath := filepath.Join(projectDir, "test.db")

	// ── Session 1: index content, shutdown ──

	cfg := config.DefaultConfig()
	cfg.Store.Path = dbPath
	exec := executor.NewExecutor(projectDir, 0)
	srv := NewServer(cfg, nil, exec, projectDir)
	srv.registerToolsForTest()

	indexReq := mcp.CallToolRequest{}
	indexReq.Params.Arguments = map[string]any{
		"content": "# Authentication Guide\n\nUse JWT tokens for API authentication.\n\n## Token Format\n\nTokens are signed with RS256.",
		"source":  "auth-docs",
	}
	result, err := srv.handleIndex(context.Background(), indexReq)
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.NotNil(t, srv.store)

	// Shutdown — this is what defer s.shutdown() does in Serve().
	srv.shutdown()

	// WAL and SHM must be gone or empty.
	if info, err := os.Stat(dbPath + "-wal"); err == nil {
		assert.Equal(t, int64(0), info.Size(),
			"WAL should be empty after shutdown, got %d bytes", info.Size())
	}
	if info, err := os.Stat(dbPath + "-shm"); err == nil {
		assert.Equal(t, int64(0), info.Size(),
			"SHM should be empty after shutdown, got %d bytes", info.Size())
	}

	// ── Session 2: new server, same DB, search for session 1's content ──

	cfg2 := config.DefaultConfig()
	cfg2.Store.Path = dbPath
	exec2 := executor.NewExecutor(projectDir, 0)
	srv2 := NewServer(cfg2, nil, exec2, projectDir)
	srv2.registerToolsForTest()
	defer srv2.shutdown()

	// Search for content indexed in session 1.
	searchReq := mcp.CallToolRequest{}
	searchReq.Params.Arguments = map[string]any{
		"queries": []any{"JWT authentication tokens"},
	}
	searchResult, err := srv2.handleSearch(context.Background(), searchReq)
	require.NoError(t, err)
	require.False(t, searchResult.IsError)

	searchText := ""
	for _, c := range searchResult.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			searchText += tc.Text
		}
	}
	assert.Contains(t, searchText, "auth-docs", "search should find content from session 1")
	assert.Contains(t, searchText, "JWT", "search should return relevant content")
	assert.NotContains(t, searchText, "No results found", "search must return results")
}
