package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommandsRejectUnsafeKeyFilePermissions(t *testing.T) {
	for _, command := range []string{"serve", "dbsize", "cleanup", "checkpoint", "doctor"} {
		t.Run(command, func(t *testing.T) {
			dir, _ := newCLIProject(t)
			writeCLIKeyFixture(t, filepath.Join(dir, ".capy.toml"), "[store]\npath = 'absent/knowledge.db'\nkey_file = 'db.key'\n")
			path := filepath.Join(dir, "db.key")
			writeCLIKeyFixture(t, path, projectFileTestKey)
			require.NoError(t, os.Chmod(path, 0o644))
			var before []byte
			if command == "checkpoint" {
				// Missing DB checkpoints are intentionally keyless; exercise an existing DB.
				seedKeyResolutionDB(t, filepath.Join(dir, "absent", "knowledge.db"), dir)
				var err error
				before, err = os.ReadFile(filepath.Join(dir, "absent", "knowledge.db"))
				require.NoError(t, err)
			}
			stdout, stderr, code := capy(t, command, "--project-dir", dir)
			if command == "doctor" {
				require.Zero(t, code, stderr) // retain doctor's diagnostic exit convention
				assert.Contains(t, stdout, "[ ] Knowledge credential:")
				assert.Contains(t, stdout, "Knowledge base: not checked (credential selection failed)")
				assert.Contains(t, stdout, "[x] FTS5: available")
			} else {
				require.NotZero(t, code)
			}
			assert.Contains(t, stdout+stderr, "unsafe key file permissions")
			assert.Contains(t, stdout+stderr, "0644")
			assert.Contains(t, stdout+stderr, path)
			assert.NotContains(t, stdout+stderr, projectFileTestKey)
			assert.NotContains(t, stdout+stderr, cliTestKey)
			if command == "checkpoint" {
				after, err := os.ReadFile(filepath.Join(dir, "absent", "knowledge.db"))
				require.NoError(t, err)
				assert.Equal(t, before, after)
			} else {
				assert.NoDirExists(t, filepath.Join(dir, "absent"))
			}
			info, err := os.Stat(path)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
			assert.NoDirExists(t, filepath.Join(os.Getenv("XDG_DATA_HOME"), "capy"))
		})
	}
}

func TestSetupRepairsKeyFilePermissions(t *testing.T) {
	dir, _ := newCLIProject(t)
	writeCLIKeyFixture(t, filepath.Join(dir, ".capy.toml"), "[store]\npath = 'knowledge.db'\nkey_file = 'db.key'\n")
	path := filepath.Join(dir, "db.key")
	writeCLIKeyFixture(t, path, projectFileTestKey)
	require.NoError(t, os.Chmod(path, 0o644))
	stdout, stderr, code := capy(t, "setup", "--project", "--project-dir", dir)
	require.Zero(t, code, stderr)
	assert.Contains(t, stdout, "Claude Code setup complete")
	assert.Contains(t, stdout, "Codex setup complete")
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	stdout, stderr, code = capy(t, "doctor", "--project-dir", dir)
	require.Zero(t, code, stderr)
	assert.Contains(t, stdout, "[x] Knowledge credential:")
	assert.NotContains(t, stdout+stderr, projectFileTestKey)
	assert.NoFileExists(t, filepath.Join(dir, "knowledge.db"))
}
