package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVaultMerge_ProjectScopeAndReport(t *testing.T) {
	root, uuid := setupVaultEnv(t)
	srcPath := os.Getenv("CAPY_VAULT_PATH")
	destPath := filepath.Join(t.TempDir(), "dest.db")
	_, stderr, code := capy(t, "vault", "import", "--source", root)
	require.Equal(t, 0, code, "stderr: %s", stderr)
	// A home-looking label must remain literal in the merge report.
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	_, stderr, code = capy(t, "vault", "project", uuid, home)
	require.Equal(t, 0, code, "stderr: %s", stderr)
	t.Setenv("CAPY_VAULT_PATH", destPath)
	stdout, stderr, code := capy(t, "vault", "merge", "--from", srcPath, "--project", "/home/user/proj", "--dry-run")
	require.Equal(t, 0, code, "stderr: %s", stderr)
	assert.Contains(t, stdout, "no sessions matched", "a replaced imported path is not an alias")
	for _, dry := range []bool{true, false} {
		args := []string{"vault", "merge", "--from", srcPath, "--project", home}
		if dry {
			args = append(args, "--dry-run")
		}
		stdout, stderr, code = capy(t, args...)
		require.Equal(t, 0, code, "stderr: %s", stderr)
		assert.Contains(t, stdout, "imported 1")
		assert.Contains(t, stdout, truncate(home, 28), "custom label is not shortened to ~")
	}
	// The newer destination choice wins on the skipped-transcript branch,
	// while the source's independent title edit still reports one update.
	_, stderr, code = capy(t, "vault", "project", uuid, "destination label")
	require.Equal(t, 0, code, "stderr: %s", stderr)
	t.Setenv("CAPY_VAULT_PATH", srcPath)
	_, stderr, code = capy(t, "vault", "rename", uuid, "source title")
	require.Equal(t, 0, code, "stderr: %s", stderr)
	t.Setenv("CAPY_VAULT_PATH", destPath)
	stdout, stderr, code = capy(t, "vault", "merge", "--from", srcPath, "--project", home)
	require.Equal(t, 0, code, "stderr: %s", stderr)
	assert.Contains(t, stdout, "updated 1")
	assert.Contains(t, stdout, "destination label")
	assert.Contains(t, stdout, "source title")
}
