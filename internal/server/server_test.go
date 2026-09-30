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

func TestServer_KnowledgeCredentialSnapshot(t *testing.T) {
	const selectedKey = "synthetic-server-selected-passphrase-12345"
	const changedKey = "synthetic-server-changed-passphrase-67890"
	for _, mode := range []string{"environment", "explicit", "resolved file"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CAPY_DB_KEY", selectedKey)
			t.Setenv("CAPY_VAULT_KEY", "")
			project := t.TempDir()
			cfg := config.DefaultConfig()
			cfg.Store.Path = filepath.Join(project, "knowledge.db")
			var opts []Option
			if mode == "explicit" {
				t.Setenv("CAPY_DB_KEY", changedKey)
				opts = append(opts, WithKnowledgeCredentials(selectedKey, config.KeySource{Kind: config.KeySourceFile, Path: "fixture.key"}))
			}
			keyPath := filepath.Join(project, "db.key")
			if mode == "resolved file" {
				t.Setenv("CAPY_DB_KEY", changedKey)
				cfg.Store.KeyFile = keyPath
				require.NoError(t, os.WriteFile(keyPath, []byte(selectedKey), 0o600))
				key, source, err := cfg.ResolveStoreKey(project)
				require.NoError(t, err)
				opts = append(opts, WithKnowledgeCredentials(key, source))
			}
			srv := NewServer(cfg, nil, nil, project, opts...)
			t.Cleanup(srv.shutdown)

			// Change inputs before even constructing the lazy store. The first
			// connection and shutdown checkpoint must still use the snapshot.
			t.Setenv("CAPY_DB_KEY", changedKey)
			if mode == "resolved file" {
				require.NoError(t, os.WriteFile(keyPath, []byte(changedKey), 0o600))
			}
			result := callIndex(t, srv, map[string]any{
				"source": "snapshot-fixture", "content": "# Orchard\n\nRetained copperbadger marker.",
			})
			require.False(t, result.IsError, resultText(result))
			srv.shutdown()
			if info, err := os.Stat(cfg.Store.Path + "-wal"); err == nil {
				assert.Zero(t, info.Size(), "shutdown must checkpoint with the captured key")
			} else {
				require.True(t, os.IsNotExist(err), "unexpected WAL stat error: %v", err)
			}

			reopened := NewServer(cfg, nil, nil, project,
				WithKnowledgeCredentials(selectedKey, config.KeySource{Kind: config.KeySourceEnvironment}))
			t.Cleanup(reopened.shutdown)
			sources, err := reopened.getStore().ListSources()
			require.NoError(t, err)
			require.Len(t, sources, 1)
			assert.Equal(t, "snapshot-fixture", sources[0].Label)
			reopened.shutdown()

			// A new default server captures the changed environment, proving
			// the database was encrypted with the earlier selected credential.
			changed := NewServer(cfg, nil, nil, project)
			t.Cleanup(changed.shutdown)
			_, err = changed.getStore().ListSources()
			require.ErrorContains(t, err, "wrong passphrase")
			assert.NotContains(t, err.Error(), selectedKey)
			assert.NotContains(t, err.Error(), changedKey)
		})
	}
}

func TestServer_EmptyKnowledgeCredentialDoctor(t *testing.T) {
	const inheritedKey = "synthetic-server-inherited-passphrase-12345"
	t.Setenv("CAPY_DB_KEY", inheritedKey)
	t.Setenv("CAPY_VAULT_KEY", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	project := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Store.Path = filepath.Join(project, "absent", "knowledge.db")
	source := config.KeySource{Kind: config.KeySourceFile, Path: filepath.Join(project, "db.key")}
	srv := NewServer(cfg, nil, executor.NewExecutor(project, 0), project, WithKnowledgeCredentials("", source))
	t.Cleanup(srv.shutdown)
	text := resultText(callDoctor(t, srv))
	assert.Contains(t, text, "[x] FTS5: available")
	assert.Contains(t, text, "[ ] Knowledge credential: cannot select from "+source.String())
	assert.Contains(t, text, "captured credential is empty")
	assert.Contains(t, text, "[-] Knowledge base: not checked (credential selection failed)")
	assert.Contains(t, text, "[-] Vault: disabled")
	assert.NotContains(t, text, inheritedKey)
	assert.NotContains(t, text, "_key=")
	srv.shutdown()
	for _, path := range []string{
		filepath.Dir(cfg.Store.Path), filepath.Join(filepath.Dir(cfg.Store.Path), ".project"),
		cfg.Store.Path, cfg.Store.Path + "-wal", cfg.Store.Path + "-shm",
	} {
		_, err := os.Stat(path)
		assert.True(t, os.IsNotExist(err), "empty credential created %s", path)
	}
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
