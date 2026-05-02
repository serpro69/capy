package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/serpro69/capy/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func capy(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command("go", append([]string{"run", "-tags", "fts5", "."}, args...)...)
	cmd.Env = append(os.Environ(), "CAPY_DB_KEY="+os.Getenv("CAPY_DB_KEY"))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		}
	}
	return stdout.String(), stderr.String(), exitCode
}

func TestVersionFlag(t *testing.T) {
	stdout, _, code := capy(t, "--version")
	assert.Equal(t, 0, code)
	assert.NotEmpty(t, stdout)
}

func TestServeSubcommand(t *testing.T) {
	// serve starts MCP JSON-RPC on stdio; with empty stdin it exits cleanly
	_, _, code := capy(t, "serve")
	assert.Equal(t, 0, code)
}

func TestHookSubcommand(t *testing.T) {
	// hook reads JSON from stdin; with empty stdin it passes through cleanly
	_, _, code := capy(t, "hook", "pretooluse")
	assert.Equal(t, 0, code)
}

func TestHookRequiresEventArg(t *testing.T) {
	_, _, code := capy(t, "hook")
	assert.NotEqual(t, 0, code)
}

func TestSetupSubcommand(t *testing.T) {
	dir := t.TempDir()
	stdout, _, code := capy(t, "setup", "--project-dir", dir, "--project")
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "setup")
}

func TestDoctorSubcommand(t *testing.T) {
	stdout, _, code := capy(t, "doctor")
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "doctor")
}

func TestCleanupSubcommand(t *testing.T) {
	t.Setenv("CAPY_DB_KEY", "test-passphrase-at-least-32-characters-long!!")
	dir := t.TempDir()
	// Write a config that keeps the DB inside the temp dir (avoids leaking to ~/.local/share/capy/)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".capy.toml"),
		[]byte("[store]\npath = \"test.db\"\n"),
		0o644,
	))
	stdout, _, code := capy(t, "cleanup", "--project-dir", dir)
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "cleanup")
}

func TestCheckpointSubcommand_NoDB(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".capy.toml"),
		[]byte("[store]\npath = \"test.db\"\n"),
		0o644,
	))
	stdout, _, code := capy(t, "checkpoint", "--project-dir", dir)
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "no knowledge base")
}

func TestCheckpointSubcommand_WithDB(t *testing.T) {
	const testKey = "test-passphrase-at-least-32-characters-long!!"
	t.Setenv("CAPY_DB_KEY", testKey)

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".capy.toml"),
		[]byte("[store]\npath = \"test.db\"\n"),
		0o644,
	))

	// Create an encrypted WAL-mode DB through the store API.
	st := store.NewContentStore(dbPath, dir, 0)
	_, err := st.Index("# Test\n\nCheckpoint test content.", "cp-test", "", store.KindDurable)
	require.NoError(t, err)
	require.NoError(t, st.Close())

	stdout, _, code := capy(t, "checkpoint", "--project-dir", dir)
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "WAL flushed")

	walPath := dbPath + "-wal"
	shmPath := dbPath + "-shm"
	if info, err := os.Stat(walPath); err == nil {
		assert.Equal(t, int64(0), info.Size(), "WAL file should be empty after checkpoint, got %d bytes", info.Size())
	}
	if info, err := os.Stat(shmPath); err == nil {
		assert.Equal(t, int64(0), info.Size(), "SHM file should be empty after checkpoint, got %d bytes", info.Size())
	}
}

func TestCheckpointSubcommand_BadConfig(t *testing.T) {
	dir := t.TempDir()
	// Write invalid TOML
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".capy.toml"),
		[]byte("this is not [valid toml\n"),
		0o644,
	))
	// Should still succeed (falls back to defaults) but warn on stderr
	_, stderr, code := capy(t, "checkpoint", "--project-dir", dir)
	assert.Equal(t, 0, code)
	assert.Contains(t, stderr, "config load failed", "should warn about bad config on stderr")
}

func TestDefaultCommandIsServe(t *testing.T) {
	// default command is serve; with empty stdin it exits cleanly
	_, _, code := capy(t)
	assert.Equal(t, 0, code)
}

func TestUnknownSubcommand(t *testing.T) {
	_, _, code := capy(t, "nonexistent")
	require.NotEqual(t, 0, code)
}
