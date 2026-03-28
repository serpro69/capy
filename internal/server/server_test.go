package server

import (
	"testing"

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
