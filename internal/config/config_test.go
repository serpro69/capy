package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	assert.Equal(t, 30, cfg.Executor.Timeout)
	assert.Equal(t, 102400, cfg.Executor.MaxOutputBytes)
	assert.Equal(t, 2.0, cfg.Store.TitleWeight)
	assert.Equal(t, 30, cfg.Store.Cleanup.ColdThresholdDays)
	assert.False(t, cfg.Store.Cleanup.AutoPrune)
	assert.Equal(t, 24, cfg.Store.Cache.FetchTTLHours)
	assert.Equal(t, "info", cfg.Server.LogLevel)
	assert.Empty(t, cfg.Store.Path)
}

func TestLoadSingleFile(t *testing.T) {
	dir := t.TempDir()
	content := `
[executor]
timeout = 60

[server]
log_level = "debug"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".capy.toml"), []byte(content), 0o644))

	cfg, err := Load(dir)
	require.NoError(t, err)
	assert.Equal(t, 60, cfg.Executor.Timeout)
	assert.Equal(t, "debug", cfg.Server.LogLevel)
	// Defaults preserved for unset fields.
	assert.Equal(t, 102400, cfg.Executor.MaxOutputBytes)
	assert.Equal(t, 30, cfg.Store.Cleanup.ColdThresholdDays)
}

func TestLoadThreeLevelPrecedence(t *testing.T) {
	dir := t.TempDir()

	// Simulate XDG config (lowest priority).
	xdgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgDir)
	require.NoError(t, os.MkdirAll(filepath.Join(xdgDir, "capy"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(xdgDir, "capy", "config.toml"),
		[]byte(`
[executor]
timeout = 10
max_output_bytes = 1000

[server]
log_level = "warn"
`), 0o644))

	// Project .capy/config.toml (medium priority).
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".capy"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".capy", "config.toml"),
		[]byte(`
[executor]
timeout = 20
`), 0o644))

	// Project .capy.toml (highest priority).
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".capy.toml"),
		[]byte(`
[server]
log_level = "error"
`), 0o644))

	cfg, err := Load(dir)
	require.NoError(t, err)

	// timeout: XDG=10 → .capy/config.toml=20 (wins)
	assert.Equal(t, 20, cfg.Executor.Timeout)
	// max_output_bytes: XDG=1000, no override
	assert.Equal(t, 1000, cfg.Executor.MaxOutputBytes)
	// log_level: XDG=warn → .capy.toml=error (wins)
	assert.Equal(t, "error", cfg.Server.LogLevel)
}

func TestTitleWeightFromTOML(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	content := `
[store]
title_weight = 5.0
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".capy.toml"), []byte(content), 0o644))

	cfg, err := Load(dir)
	require.NoError(t, err)
	assert.Equal(t, 5.0, cfg.Store.TitleWeight)
}

func TestTitleWeightDefaultWhenOmitted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// TOML that doesn't mention title_weight — default should survive.
	content := `
[executor]
timeout = 45
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".capy.toml"), []byte(content), 0o644))

	cfg, err := Load(dir)
	require.NoError(t, err)
	assert.Equal(t, 2.0, cfg.Store.TitleWeight)
}

func TestFetchTTLHoursFromTOML(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	content := `
[store.cache]
fetch_ttl_hours = 48
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".capy.toml"), []byte(content), 0o644))

	cfg, err := Load(dir)
	require.NoError(t, err)
	assert.Equal(t, 48, cfg.Store.Cache.FetchTTLHours)
}

func TestFetchTTLHoursDefaultWhenOmitted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	content := `
[executor]
timeout = 45
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".capy.toml"), []byte(content), 0o644))

	cfg, err := Load(dir)
	require.NoError(t, err)
	assert.Equal(t, 24, cfg.Store.Cache.FetchTTLHours)
}

func TestLoadMissingFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg, err := Load(dir)
	require.NoError(t, err)
	// All defaults.
	assert.Equal(t, 30, cfg.Executor.Timeout)
	assert.Equal(t, "info", cfg.Server.LogLevel)
}

func TestLoadMalformedTOML(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".capy.toml"), []byte(`[broken`), 0o644))

	_, err := Load(dir)
	assert.Error(t, err)
}

func TestDetectProjectRoot(t *testing.T) {
	// With CLAUDE_PROJECT_DIR set, it takes priority.
	t.Setenv("CLAUDE_PROJECT_DIR", "/some/project")
	assert.Equal(t, "/some/project", DetectProjectRoot())
}

func TestProjectHashDeterminism(t *testing.T) {
	h1 := ProjectHash("/some/path")
	h2 := ProjectHash("/some/path")
	assert.Equal(t, h1, h2)
	assert.Len(t, h1, 16) // 8 bytes = 16 hex chars

	h3 := ProjectHash("/different/path")
	assert.NotEqual(t, h1, h3)
}

func TestResolveDBPathDefault(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)

	cfg := DefaultConfig()
	path := cfg.ResolveDBPath("/my/project")

	hash := ProjectHash("/my/project")
	expected := filepath.Join(xdg, "capy", hash, "knowledge.db")
	assert.Equal(t, expected, path)
}

func TestResolveDBPathRelative(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Store.Path = ".capy/data.db"

	path := cfg.ResolveDBPath("/my/project")
	assert.Equal(t, "/my/project/.capy/data.db", path)
}

func TestResolveDBPathAbsolute(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Store.Path = "/custom/path/knowledge.db"

	path := cfg.ResolveDBPath("/my/project")
	assert.Equal(t, "/custom/path/knowledge.db", path)
}
