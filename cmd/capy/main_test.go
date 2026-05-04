package main

import (
	"bytes"
	"database/sql"
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
	cmd.Env = os.Environ()
	if v := os.Getenv("CAPY_DB_KEY"); v != "" {
		cmd.Env = append(cmd.Env, "CAPY_DB_KEY="+v)
	}
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
	t.Setenv("CAPY_DB_KEY", "test-passphrase-at-least-32-characters-long!!")
	dir := t.TempDir()
	// serve starts MCP JSON-RPC on stdio; with empty stdin it exits cleanly
	_, _, code := capy(t, "serve", "--project-dir", dir)
	assert.Equal(t, 0, code)
}

func TestServeSubcommand_NoKey(t *testing.T) {
	t.Setenv("CAPY_DB_KEY", "")
	_, stderr, code := capy(t, "serve")
	assert.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "CAPY_DB_KEY")
}

func TestServeSubcommand_UnencryptedDB(t *testing.T) {
	t.Setenv("CAPY_DB_KEY", "test-passphrase-at-least-32-characters-long!!")
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".capy.toml"),
		[]byte("[store]\npath = \"test.db\"\n"),
		0o644,
	))

	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE t (id INTEGER)")
	require.NoError(t, err)
	db.Close()

	_, stderr, code := capy(t, "serve", "--project-dir", dir)
	assert.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "not encrypted")
	assert.Contains(t, stderr, "capy encrypt")
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

	// Data must survive the checkpoint.
	st2 := store.NewContentStore(dbPath, dir, 0)
	defer st2.Close()
	sources, err := st2.ListSources()
	require.NoError(t, err)
	require.Len(t, sources, 1)
	assert.Equal(t, "cp-test", sources[0].Label)
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
	t.Setenv("CAPY_DB_KEY", "test-passphrase-at-least-32-characters-long!!")
	dir := t.TempDir()
	// default command is serve; with empty stdin it exits cleanly
	_, _, code := capy(t, "--project-dir", dir)
	assert.Equal(t, 0, code)
}

func TestUnknownSubcommand(t *testing.T) {
	_, _, code := capy(t, "nonexistent")
	require.NotEqual(t, 0, code)
}

func TestEncryptPlain_WALMode(t *testing.T) {
	const passphrase = "test-encrypt-plain-at-least-32-characters!!"

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	require.NoError(t, err)
	_, err = db.Exec("CREATE VIRTUAL TABLE fts USING fts5(content)")
	require.NoError(t, err)
	_, err = db.Exec("INSERT INTO fts (content) VALUES (?)", "encrypt plain wal test")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	_, err = db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	require.NoError(t, err)
	db.Close()

	require.NoError(t, encryptPlain(dbPath, passphrase))

	raw, err := os.ReadFile(dbPath)
	require.NoError(t, err)
	require.True(t, len(raw) >= 15)
	assert.NotEqual(t, "SQLite format 3", string(raw[:15]), "file should be encrypted")

	bakPath := dbPath + ".bak"
	_, err = os.Stat(bakPath)
	assert.NoError(t, err, "backup file should exist")

	verifyDB, err := sql.Open("sqlite3", store.EncryptedDSN(dbPath, passphrase))
	require.NoError(t, err)
	defer verifyDB.Close()

	var content string
	require.NoError(t, verifyDB.QueryRow("SELECT content FROM fts WHERE fts MATCH ?", "encrypt").Scan(&content))
	assert.Equal(t, "encrypt plain wal test", content)
}
