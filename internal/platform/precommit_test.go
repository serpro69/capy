package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstallPreCommitHook_NewHook(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))

	err := installPreCommitHook("/usr/local/bin/capy", dir)
	require.NoError(t, err)

	hookPath := filepath.Join(hooksDir, "pre-commit")
	data, err := os.ReadFile(hookPath)
	require.NoError(t, err)

	content := string(data)
	assert.Contains(t, content, "#!/bin/sh")
	assert.Contains(t, content, preCommitMarkerStart)
	assert.Contains(t, content, preCommitMarkerEnd)
	assert.Contains(t, content, "'/usr/local/bin/capy' checkpoint")
	assert.Contains(t, content, "knowledge")

	// Verify executable permission
	info, err := os.Stat(hookPath)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&0o111, "hook should be executable")
}

func TestInstallPreCommitHook_AppendsToExisting(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))

	// Create an existing pre-commit hook
	hookPath := filepath.Join(hooksDir, "pre-commit")
	existing := "#!/bin/sh\necho 'existing hook'\n"
	require.NoError(t, os.WriteFile(hookPath, []byte(existing), 0o755))

	err := installPreCommitHook("/usr/local/bin/capy", dir)
	require.NoError(t, err)

	data, err := os.ReadFile(hookPath)
	require.NoError(t, err)

	content := string(data)
	// Existing content preserved
	assert.Contains(t, content, "existing hook")
	// Capy checkpoint appended
	assert.Contains(t, content, preCommitMarkerStart)
	assert.Contains(t, content, "'/usr/local/bin/capy' checkpoint")
}

func TestInstallPreCommitHook_Idempotent(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))

	// Install twice
	require.NoError(t, installPreCommitHook("/usr/local/bin/capy", dir))
	require.NoError(t, installPreCommitHook("/usr/local/bin/capy", dir))

	data, err := os.ReadFile(filepath.Join(hooksDir, "pre-commit"))
	require.NoError(t, err)

	content := string(data)
	count := strings.Count(content, preCommitMarkerStart)
	assert.Equal(t, 1, count, "should not duplicate checkpoint hook")
}

func TestInstallPreCommitHook_UpdatesBinaryPath(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))

	// Install with old path
	require.NoError(t, installPreCommitHook("/old/path/capy", dir))

	data, err := os.ReadFile(filepath.Join(hooksDir, "pre-commit"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "'/old/path/capy' checkpoint")

	// Re-install with new path — should update, not duplicate
	require.NoError(t, installPreCommitHook("/new/path/capy", dir))

	data, err = os.ReadFile(filepath.Join(hooksDir, "pre-commit"))
	require.NoError(t, err)

	content := string(data)
	assert.Contains(t, content, "'/new/path/capy' checkpoint")
	assert.NotContains(t, content, "/old/path/capy")
	assert.Equal(t, 1, strings.Count(content, preCommitMarkerStart), "should have exactly one capy block")
}

func TestInstallPreCommitHook_PreservesExistingOnUpdate(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))

	// Create existing hook, then install capy, then update
	hookPath := filepath.Join(hooksDir, "pre-commit")
	existing := "#!/bin/sh\necho 'my custom hook'\n"
	require.NoError(t, os.WriteFile(hookPath, []byte(existing), 0o755))

	require.NoError(t, installPreCommitHook("/old/capy", dir))
	require.NoError(t, installPreCommitHook("/new/capy", dir))

	data, err := os.ReadFile(hookPath)
	require.NoError(t, err)

	content := string(data)
	assert.Contains(t, content, "my custom hook", "existing hook content preserved")
	assert.Contains(t, content, "'/new/capy' checkpoint", "updated to new path")
	assert.NotContains(t, content, "/old/capy", "old path removed")
}

func TestInstallPreCommitHook_NoGitDir(t *testing.T) {
	dir := t.TempDir()
	// No .git/hooks directory — should silently skip

	err := installPreCommitHook("/usr/local/bin/capy", dir)
	assert.NoError(t, err)
}

func TestPreCommitHookScript_ContainsBinaryPath(t *testing.T) {
	script := preCommitHookScript("/opt/custom/capy", `\.capy/knowledge\.db$`)
	assert.Contains(t, script, "'/opt/custom/capy' checkpoint")
	assert.Contains(t, script, `knowledge\.db`)
}

func TestPreCommitHookScript_EscapesSingleQuotes(t *testing.T) {
	script := preCommitHookScript("/path/with'quote/capy", `\.capy/knowledge\.db$`)
	assert.Contains(t, script, `'/path/with'\''quote/capy' checkpoint`)
}

func TestInstallPreCommitHook_CustomDBPath(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))

	// Write config with custom DB path
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".capy.toml"),
		[]byte("[store]\npath = \"shared/my-kb.db\"\n"),
		0o644,
	))

	require.NoError(t, installPreCommitHook("/usr/local/bin/capy", dir))

	data, err := os.ReadFile(filepath.Join(hooksDir, "pre-commit"))
	require.NoError(t, err)

	content := string(data)
	assert.Contains(t, content, `shared/my-kb\.db$`, "should use custom DB path pattern")
	assert.NotContains(t, content, ".capy/knowledge", "should not use default path")
}

func TestResolveDBPattern_Default(t *testing.T) {
	dir := t.TempDir()
	// No config file — should fall back to default XDG path which won't be
	// relative to dir, so it falls back to .capy/knowledge.db
	pattern := resolveDBPattern(dir)
	assert.Contains(t, pattern, "knowledge")
	assert.True(t, strings.HasSuffix(pattern, "$"))
}

func TestResolveDBPattern_Custom(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".capy.toml"),
		[]byte("[store]\npath = \"data/store.db\"\n"),
		0o644,
	))

	pattern := resolveDBPattern(dir)
	assert.Equal(t, `data/store\.db$`, pattern)
}

func TestSetupClaudeCode_InstallsPreCommitHook(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))

	binaryPath := "/usr/local/bin/capy"
	require.NoError(t, SetupClaudeCode(binaryPath, dir))

	data, err := os.ReadFile(filepath.Join(hooksDir, "pre-commit"))
	require.NoError(t, err)
	assert.Contains(t, string(data), preCommitMarkerStart)
}

func TestShellEscapePath(t *testing.T) {
	assert.Equal(t, "/normal/path", shellEscapePath("/normal/path"))
	assert.Equal(t, `/path/with'\''quote`, shellEscapePath("/path/with'quote"))
	assert.Equal(t, "no-change", shellEscapePath("no-change"))
}
