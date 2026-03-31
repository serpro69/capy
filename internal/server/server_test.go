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
	projectDir := t.TempDir()
	dbPath := filepath.Join(projectDir, "test.db")

	cfg := config.DefaultConfig()
	cfg.Store.Path = dbPath
	exec := executor.NewExecutor(projectDir, 0)
	srv := NewServer(cfg, nil, exec, projectDir)

	// Trigger store init + index content to generate WAL data.
	srv.registerToolsForTest()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"content": "# Shutdown Test\n\nData that must survive shutdown.",
		"source":  "shutdown-test",
	}
	result, err := srv.handleIndex(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)

	// Verify store was initialized and has data.
	require.NotNil(t, srv.store)

	// Call shutdown — this is what Serve() defers.
	srv.shutdown()

	// WAL and SHM must be gone or empty after shutdown.
	if info, err := os.Stat(dbPath + "-wal"); err == nil {
		assert.Equal(t, int64(0), info.Size(),
			"WAL should be empty after shutdown, got %d bytes", info.Size())
	}
	if info, err := os.Stat(dbPath + "-shm"); err == nil {
		assert.Equal(t, int64(0), info.Size(),
			"SHM should be empty after shutdown, got %d bytes", info.Size())
	}

	// Data must survive — reopen the DB directly and check.
	cfg2 := config.DefaultConfig()
	cfg2.Store.Path = dbPath
	srv2 := NewServer(cfg2, nil, nil, projectDir)
	st := srv2.getStore()
	defer st.Close()

	sources, err := st.ListSources()
	require.NoError(t, err)
	require.Len(t, sources, 1)
	assert.Equal(t, "shutdown-test", sources[0].Label)
}
