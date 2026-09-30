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
	dir := t.TempDir()
	stdout, _, code := capy(t, "--version", "--project-dir", dir)
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
	dir := t.TempDir()
	_, stderr, code := capy(t, "serve", "--project-dir", dir)
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
	dir := t.TempDir()
	// hook reads JSON from stdin; with empty stdin it passes through cleanly
	_, _, code := capy(t, "hook", "pretooluse", "--project-dir", dir)
	assert.Equal(t, 0, code)
}

func TestHookRequiresEventArg(t *testing.T) {
	dir := t.TempDir()
	_, _, code := capy(t, "hook", "--project-dir", dir)
	assert.NotEqual(t, 0, code)
}

func TestSetupSubcommand(t *testing.T) {
	dir := t.TempDir()
	stdout, _, code := capy(t, "setup", "--project-dir", dir, "--project")
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "setup")
}

func TestDoctorSubcommand(t *testing.T) {
	dir := t.TempDir()
	stdout, _, code := capy(t, "doctor", "--project-dir", dir)
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

func TestCleanupSubcommand_KindSession(t *testing.T) {
	t.Setenv("CAPY_DB_KEY", "test-passphrase-at-least-32-characters-long!!")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".capy.toml"),
		[]byte("[store]\npath = \"test.db\"\n"),
		0o644,
	))
	stdout, _, code := capy(t, "cleanup", "--kind", "session", "--project-dir", dir)
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "no evictable sources found")
}

func TestCleanupSubcommand_KindInvalid(t *testing.T) {
	t.Setenv("CAPY_DB_KEY", "test-passphrase-at-least-32-characters-long!!")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".capy.toml"),
		[]byte("[store]\npath = \"test.db\"\n"),
		0o644,
	))
	_, stderr, code := capy(t, "cleanup", "--kind", "bogus", "--project-dir", dir)
	assert.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "invalid --kind value")
}

func TestCheckpointSubcommand_NoDB(t *testing.T) {
	dir, _ := newCLIProject(t)
	t.Setenv("CAPY_DB_KEY", "")
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".capy.toml"),
		[]byte("[store]\npath = 'absent/test.db'\nkey_file = 'missing.key'\n"),
		0o644,
	))
	stdout, _, code := capy(t, "checkpoint", "--project-dir", dir)
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "no knowledge base")
	assert.NoDirExists(t, filepath.Join(dir, "absent"))
	assert.NoDirExists(t, filepath.Join(os.Getenv("XDG_DATA_HOME"), "capy"))
}

func TestCheckpointSubcommand_WithDB(t *testing.T) {
	const testKey = "test-passphrase-at-least-32-characters-long!!"
	t.Setenv("CAPY_DB_KEY", testKey)

	dir, dbPath := newCLIProject(t)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".capy.toml"),
		[]byte("[store]\npath = 'test.db'\nkey_file = 'db.key'\n"),
		0o644,
	))
	writeCLIKeyFixture(t, filepath.Join(dir, "db.key"), testKey)

	// Create an encrypted WAL-mode DB through the store API.
	st := store.NewContentStore(dbPath, dir, 0, 0)
	_, err := st.Index("# Test\n\nCheckpoint test content.", "cp-test", "", store.KindDurable)
	require.NoError(t, err)
	require.NoError(t, st.Close())

	t.Setenv("CAPY_DB_KEY", projectFileTestKey) // unrelated inherited credential
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
	st2 := store.NewContentStore(dbPath, dir, 0, 0, store.WithEncryptionKey(testKey, "synthetic fixture"))
	defer st2.Close()
	sources, err := st2.ListSources()
	require.NoError(t, err)
	require.Len(t, sources, 1)
	assert.Equal(t, "cp-test", sources[0].Label)
}

func TestCheckpointSubcommand_BadConfig(t *testing.T) {
	dir, _ := newCLIProject(t)
	t.Setenv("CAPY_DB_KEY", "")
	// Write invalid TOML
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".capy.toml"),
		[]byte("this is not [valid toml\n"),
		0o644,
	))
	stdout, stderr, code := capy(t, "checkpoint", "--project-dir", dir)
	assert.NotZero(t, code)
	assert.Empty(t, stdout)
	assert.Contains(t, stderr, "loading configuration")
	assert.NotContains(t, stderr, "resolving knowledge credential")
	assert.NoDirExists(t, filepath.Join(os.Getenv("XDG_DATA_HOME"), "capy"))
	assert.NoFileExists(t, filepath.Join(dir, ".project"))
}

func TestCheckpointSubcommand_StatError(t *testing.T) {
	dir, parent := newCLIProject(t)
	writeCLIKeyFixture(t, parent, "not a directory")
	writeCLIKeyFixture(t, filepath.Join(dir, ".capy.toml"), "[store]\npath = 'test.db/knowledge.db'\nkey_file = 'missing.key'\n")
	_, statErr := os.Stat(filepath.Join(parent, "knowledge.db"))
	require.Error(t, statErr)
	require.False(t, os.IsNotExist(statErr))

	stdout, stderr, code := capy(t, "checkpoint", "--project-dir", dir)
	assert.NotZero(t, code)
	assert.Empty(t, stdout)
	assert.Contains(t, stderr, "accessing knowledge database")
	assert.Contains(t, stderr, statErr.Error())
	assert.NotContains(t, stderr, "resolving knowledge credential")
	assert.NoFileExists(t, filepath.Join(dir, ".project"))
}

func TestCheckpointSubcommand_BusyWithProjectKey(t *testing.T) {
	dir, dbPath := newCLIProject(t)
	writeCLIKeyFixture(t, filepath.Join(dir, ".env"), "CAPY_DB_KEY="+projectFileTestKey+"\n")
	st := seedKeyResolutionDB(t, dbPath, dir)

	// Hold a read snapshot, then append a new WAL frame. TRUNCATE must fail
	// until this reader releases its snapshot; no sleeps or timing races.
	db, err := sql.Open("sqlite3", store.EncryptedDSN(dbPath, projectFileTestKey)+"&_journal_mode=WAL")
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	tx, err := db.Begin()
	require.NoError(t, err)
	defer tx.Rollback()
	var count int
	require.NoError(t, tx.QueryRow("SELECT count(*) FROM sources").Scan(&count))
	require.Equal(t, 1, count)
	_, err = st.Index("# Pending\n\nContent waiting in WAL.", "pending-marker", "", store.KindDurable)
	require.NoError(t, err)

	stdout, stderr, code := capy(t, "checkpoint", "--project-dir", dir)
	assert.NotZero(t, code)
	assert.Contains(t, stderr, "checkpoint incomplete")
	assert.Contains(t, stderr, "pages busy")
	assert.NotContains(t, stdout, "safe to commit")
	assert.NotContains(t, stdout+stderr, projectFileTestKey)
	assert.NotContains(t, stdout+stderr, cliTestKey)
	assert.NotContains(t, stdout+stderr, "cipher=")
	info, err := os.Stat(dbPath + "-wal")
	require.NoError(t, err)
	assert.Positive(t, info.Size(), "failed checkpoint must retain pending WAL data")

	require.NoError(t, tx.Rollback())
	require.NoError(t, db.Close())
	require.NoError(t, st.Close())
	stdout, stderr, code = capy(t, "checkpoint", "--project-dir", dir)
	require.Zero(t, code, stderr)
	assert.Contains(t, stdout, "safe to commit")
	sources, err := st.ListSources()
	require.NoError(t, err)
	require.Len(t, sources, 2)
	assert.ElementsMatch(t, []string{"project-B-marker", "pending-marker"}, []string{sources[0].Label, sources[1].Label})
}

func TestDefaultCommandIsServe(t *testing.T) {
	t.Setenv("CAPY_DB_KEY", "test-passphrase-at-least-32-characters-long!!")
	dir := t.TempDir()
	// default command is serve; with empty stdin it exits cleanly
	_, _, code := capy(t, "--project-dir", dir)
	assert.Equal(t, 0, code)
}

func TestUnknownSubcommand(t *testing.T) {
	dir := t.TempDir()
	_, _, code := capy(t, "nonexistent", "--project-dir", dir)
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
