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
	assert.Contains(t, content, "capy: checkpoint WAL")
	assert.Contains(t, content, `"/usr/local/bin/capy" checkpoint`)
	assert.Contains(t, content, "knowledge.db")

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
	assert.Contains(t, content, "capy: checkpoint WAL")
	assert.Contains(t, content, `"/usr/local/bin/capy" checkpoint`)
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
	count := strings.Count(content, "capy: checkpoint WAL")
	assert.Equal(t, 1, count, "should not duplicate checkpoint hook")
}

func TestInstallPreCommitHook_NoGitDir(t *testing.T) {
	dir := t.TempDir()
	// No .git/hooks directory — should silently skip

	err := installPreCommitHook("/usr/local/bin/capy", dir)
	assert.NoError(t, err)
}

func TestPreCommitHookScript_ContainsBinaryPath(t *testing.T) {
	script := preCommitHookScript("/opt/custom/capy")
	assert.Contains(t, script, `"/opt/custom/capy" checkpoint`)
	assert.Contains(t, script, "knowledge.db")
}

func TestSetupClaudeCode_InstallsPreCommitHook(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))

	binaryPath := "/usr/local/bin/capy"
	require.NoError(t, SetupClaudeCode(binaryPath, dir))

	data, err := os.ReadFile(filepath.Join(hooksDir, "pre-commit"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "capy: checkpoint WAL")
}
