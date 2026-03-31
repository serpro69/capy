package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func capy(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command("go", append([]string{"run", "-tags", "fts5", "."}, args...)...)
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
	stdout, _, code := capy(t, "setup", "--project-dir", dir)
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "setup")
}

func TestDoctorSubcommand(t *testing.T) {
	stdout, _, code := capy(t, "doctor")
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "doctor")
}

func TestCleanupSubcommand(t *testing.T) {
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
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".capy.toml"),
		[]byte("[store]\npath = \"test.db\"\n"),
		0o644,
	))

	// Create a WAL-mode DB with data that forces WAL file creation
	cmd := exec.Command("sqlite3", dbPath, "PRAGMA journal_mode=WAL; CREATE TABLE t(id INTEGER); INSERT INTO t VALUES(1);")
	if err := cmd.Run(); err != nil {
		t.Skip("sqlite3 CLI not available")
	}

	// Write more data to ensure WAL has content
	cmd = exec.Command("sqlite3", dbPath, "INSERT INTO t VALUES(2); INSERT INTO t VALUES(3);")
	require.NoError(t, cmd.Run())

	stdout, _, code := capy(t, "checkpoint", "--project-dir", dir)
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "WAL flushed")

	// The actual assertion: WAL and SHM files must be gone or empty
	walPath := dbPath + "-wal"
	shmPath := dbPath + "-shm"
	if info, err := os.Stat(walPath); err == nil {
		assert.Equal(t, int64(0), info.Size(), "WAL file should be empty after checkpoint, got %d bytes", info.Size())
	}
	if info, err := os.Stat(shmPath); err == nil {
		assert.Equal(t, int64(0), info.Size(), "SHM file should be empty after checkpoint, got %d bytes", info.Size())
	}

	// Data must survive the checkpoint
	cmd = exec.Command("sqlite3", dbPath, "SELECT COUNT(*) FROM t;")
	out, err := cmd.Output()
	require.NoError(t, err)
	assert.Contains(t, string(out), "3")
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
