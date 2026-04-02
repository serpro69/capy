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

	err := installPreCommitHook(dir)
	require.NoError(t, err)

	hookPath := filepath.Join(hooksDir, "pre-commit")
	data, err := os.ReadFile(hookPath)
	require.NoError(t, err)

	content := string(data)
	assert.Contains(t, content, "#!/bin/sh")
	assert.Contains(t, content, preCommitMarkerStart)
	assert.Contains(t, content, preCommitMarkerEnd)
	assert.Contains(t, content, `bash "`+capyWrapperRelPath+`" checkpoint`)
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

	err := installPreCommitHook(dir)
	require.NoError(t, err)

	data, err := os.ReadFile(hookPath)
	require.NoError(t, err)

	content := string(data)
	// Existing content preserved
	assert.Contains(t, content, "existing hook")
	// Capy checkpoint appended
	assert.Contains(t, content, preCommitMarkerStart)
	assert.Contains(t, content, `bash "`+capyWrapperRelPath+`" checkpoint`)
}

func TestInstallPreCommitHook_Idempotent(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))

	// Install twice
	require.NoError(t, installPreCommitHook(dir))
	require.NoError(t, installPreCommitHook(dir))

	data, err := os.ReadFile(filepath.Join(hooksDir, "pre-commit"))
	require.NoError(t, err)

	content := string(data)
	count := strings.Count(content, preCommitMarkerStart)
	assert.Equal(t, 1, count, "should not duplicate checkpoint hook")
}

func TestInstallPreCommitHook_PreservesExistingOnReinstall(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))

	// Create existing hook, then install capy twice
	hookPath := filepath.Join(hooksDir, "pre-commit")
	existing := "#!/bin/sh\necho 'my custom hook'\n"
	require.NoError(t, os.WriteFile(hookPath, []byte(existing), 0o755))

	require.NoError(t, installPreCommitHook(dir))
	require.NoError(t, installPreCommitHook(dir))

	data, err := os.ReadFile(hookPath)
	require.NoError(t, err)

	content := string(data)
	assert.Contains(t, content, "my custom hook", "existing hook content preserved")
	assert.Contains(t, content, `bash "`+capyWrapperRelPath+`" checkpoint`)
	assert.Equal(t, 1, strings.Count(content, preCommitMarkerStart), "should have exactly one capy block")
}

func TestInstallPreCommitHook_MigratesOldHardcodedPath(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))

	// Simulate old-format pre-commit hook with hardcoded binary path
	hookPath := filepath.Join(hooksDir, "pre-commit")
	oldScript := "#!/bin/sh\n" +
		preCommitMarkerStart + "\n" +
		"# Installed by capy setup — safe to remove if not needed.\n\n" +
		"if git diff --cached --name-only | grep -q '\\.capy/knowledge\\.db$'; then\n" +
		"  '/opt/homebrew/bin/capy' checkpoint\n" +
		"  git diff --cached --name-only | grep '\\.capy/knowledge\\.db$' | while read -r f; do git add \"$f\"; done\n" +
		"fi\n" +
		preCommitMarkerEnd + "\n"
	require.NoError(t, os.WriteFile(hookPath, []byte(oldScript), 0o755))

	// Re-install — should replace old block with wrapper-based one
	require.NoError(t, installPreCommitHook(dir))

	data, err := os.ReadFile(hookPath)
	require.NoError(t, err)

	content := string(data)
	assert.Contains(t, content, `bash "`+capyWrapperRelPath+`" checkpoint`,
		"should use wrapper script after migration")
	assert.NotContains(t, content, "/opt/homebrew/bin/capy",
		"should not contain old hardcoded path")
	assert.Equal(t, 1, strings.Count(content, preCommitMarkerStart),
		"should have exactly one capy block")
}

func TestInstallPreCommitHook_NoGitDir(t *testing.T) {
	dir := t.TempDir()
	// No .git/hooks directory — should silently skip

	err := installPreCommitHook(dir)
	assert.NoError(t, err)
}

func TestPreCommitHookScript_UsesWrapper(t *testing.T) {
	script := preCommitHookScript(`\.capy/knowledge\.db$`)
	assert.Contains(t, script, `bash "`+capyWrapperRelPath+`" checkpoint`)
	assert.Contains(t, script, `knowledge\.db`)
	assert.NotContains(t, script, "/usr/local/bin/capy", "should not contain hardcoded binary path")
}

func TestPreCommitHookScript_EscapesDBPattern(t *testing.T) {
	script := preCommitHookScript(`path/with'quote/db\.db$`)
	assert.Contains(t, script, `path/with'\''quote/db\.db$`)
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

	require.NoError(t, installPreCommitHook(dir))

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
