package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSetupDBRepoFlag runs `capy setup --db-repo` against a scratch repo and
// asserts it writes exactly the DB-repo artifacts (gitignore entries + guard
// hook) and nothing project-related.
func TestSetupDBRepoFlag(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git", "hooks"), 0o755))

	stdout, stderr, code := capy(t, "setup", "--db-repo", "--project-dir", dir)
	require.Equal(t, 0, code, "stderr: %s", stderr)
	assert.Contains(t, stdout, "knowledge-DB repo setup complete")

	gitignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(gitignore), "*.db-wal")
	assert.Contains(t, string(gitignore), "*.db-shm")

	_, err = os.Stat(filepath.Join(dir, ".git", "hooks", "pre-commit"))
	assert.NoError(t, err, "guard hook should be installed")

	// No project artifacts.
	_, err = os.Stat(filepath.Join(dir, ".mcp.json"))
	assert.True(t, os.IsNotExist(err), ".mcp.json must not be written in db-repo mode")
	_, err = os.Stat(filepath.Join(dir, ".capy"))
	assert.True(t, os.IsNotExist(err), ".capy/ must not be written in db-repo mode")
}

// TestSetupDBRepoFlag_MutuallyExclusive asserts --db-repo cannot combine with the
// platform/target flags.
func TestSetupDBRepoFlag_MutuallyExclusive(t *testing.T) {
	dir := t.TempDir()
	_, stderr, code := capy(t, "setup", "--db-repo", "--platform", "codex", "--project-dir", dir)
	assert.NotEqual(t, 0, code, "combining --db-repo with --platform should error")
	assert.Contains(t, stderr, "db-repo")
}
