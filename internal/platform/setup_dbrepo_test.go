package platform

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSetupDBRepo_WritesOnlyGitignoreAndHook asserts DB-repo setup writes exactly
// the .gitignore entries and the guard hook — no .claude/, .mcp.json, .capy/, or
// CLAUDE.md (all of which belong to project setup, not a DB repo).
func TestSetupDBRepo_WritesOnlyGitignoreAndHook(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git", "hooks"), 0o755))

	hookInstalled, err := SetupDBRepo(dir)
	require.NoError(t, err)
	assert.True(t, hookInstalled, "guard hook should report as installed")

	// .gitignore has both sidecar globs.
	gitignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(gitignore), "*.db-wal")
	assert.Contains(t, string(gitignore), "*.db-shm")

	// Guard hook installed with the DB-repo block.
	hook, err := os.ReadFile(filepath.Join(dir, ".git", "hooks", "pre-commit"))
	require.NoError(t, err)
	assert.Contains(t, string(hook), "#!/bin/sh")
	assert.Contains(t, string(hook), preCommitMarkerStart)
	assert.Contains(t, string(hook), `[ -e "$f-shm" ]`)

	// Negative assertions: none of the project-setup artifacts exist.
	for _, absent := range []string{
		".claude",
		".mcp.json",
		".capy",
		"CLAUDE.md",
		".codex",
	} {
		_, err := os.Stat(filepath.Join(dir, absent))
		assert.ErrorIs(t, err, os.ErrNotExist, "%s must not be created by SetupDBRepo", absent)
	}
}

// TestSetupDBRepo_Idempotent asserts a second run produces an identical tree.
func TestSetupDBRepo_Idempotent(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git", "hooks"), 0o755))

	_, err := SetupDBRepo(dir)
	require.NoError(t, err)
	first := readTree(t, dir)

	_, err = SetupDBRepo(dir)
	require.NoError(t, err)
	second := readTree(t, dir)

	assert.Equal(t, first, second, "SetupDBRepo should be idempotent")

	// Exactly one capy block in the hook after two runs.
	hook, err := os.ReadFile(filepath.Join(dir, ".git", "hooks", "pre-commit"))
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(hook), preCommitMarkerStart))
}

// TestSetupDBRepo_NoGitHooksWarnsAndSkips: without .git/hooks the guard hook is
// skipped (warn, non-fatal) but the .gitignore entries are still written.
func TestSetupDBRepo_NoGitHooksWarnsAndSkips(t *testing.T) {
	dir := t.TempDir()

	hookInstalled, err := SetupDBRepo(dir)
	require.NoError(t, err)
	assert.False(t, hookInstalled, "no hook without .git/hooks should report not installed")

	gitignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(gitignore), "*.db-wal")

	_, err = os.Stat(filepath.Join(dir, ".git", "hooks", "pre-commit"))
	assert.ErrorIs(t, err, os.ErrNotExist, "no hook should be written without .git/hooks")
}

// encryptedDBBytes returns deterministic non-SQLite bytes standing in for an
// encrypted knowledge DB (does not start with "SQLite format 3").
func encryptedDBBytes() []byte {
	b := make([]byte, 4096)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

// dbRepoSubdir is a distinctive per-project subdirectory name so shell-test
// assertions on the hook's `$(dirname "$f")` output are meaningful (a bare "p"
// would match almost any git output).
const dbRepoSubdir = "projAlpha"

// initTestDBRepo creates a temp git repo configured as a capy DB repo (via
// SetupDBRepo, so the real guard hook is installed) with an initial commit and a
// staged <dbRepoSubdir>/knowledge.db of the given content. Returns the dir, the
// DB path, and a git runner. Callers add sidecars, then attempt the commit.
func initTestDBRepo(t *testing.T, dbContent []byte) (dir, dbPath string, runGit func(args ...string)) {
	t.Helper()
	dir = t.TempDir()

	runGit = func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v failed: %s", args, out)
	}

	runGit("init")
	runGit("config", "user.email", "test@test.com")
	runGit("config", "user.name", "Test")
	runGit("config", "commit.gpgsign", "false")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "README"), []byte("init"), 0o644))
	runGit("add", "README")
	runGit("commit", "-m", "initial")

	hookInstalled, err := SetupDBRepo(dir)
	require.NoError(t, err)
	require.True(t, hookInstalled, "guard hook must be installed for the shell test")

	dbPath = filepath.Join(dir, dbRepoSubdir, "knowledge.db")
	require.NoError(t, os.MkdirAll(filepath.Dir(dbPath), 0o755))
	require.NoError(t, os.WriteFile(dbPath, dbContent, 0o644))
	runGit("add", dbRepoSubdir+"/knowledge.db")

	return dir, dbPath, runGit
}

// tryCommit attempts a commit in dir and returns its combined output and the
// error (nil on success).
func tryCommit(t *testing.T, dir, msg string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", "commit", "-m", msg)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestDBRepoHook_Shell exercises the installed DB-repo guard hook end-to-end
// through real git commits — the only way to prove the `|| exit 1` pipeline
// semantics and the sidecar checks.
func TestDBRepoHook_Shell(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	t.Run("clean encrypted DB commits", func(t *testing.T) {
		dir, _, _ := initTestDBRepo(t, encryptedDBBytes())
		out, err := tryCommit(t, dir, "clean db")
		assert.NoError(t, err, "commit should succeed with no sidecars: %s", out)
	})

	t.Run("open connection (-shm) blocks", func(t *testing.T) {
		dir, dbPath, _ := initTestDBRepo(t, encryptedDBBytes())
		require.NoError(t, os.WriteFile(dbPath+"-shm", []byte{}, 0o644)) // any size, even empty
		out, err := tryCommit(t, dir, "shm present")
		assert.Error(t, err, "commit should be blocked when -shm exists: %s", out)
		assert.Contains(t, out, dbRepoSubdir, "message should name the DB directory")
		assert.Contains(t, out, "capy checkpoint --project-dir", "message should name the remedy")
	})

	t.Run("non-empty WAL blocks", func(t *testing.T) {
		dir, dbPath, _ := initTestDBRepo(t, encryptedDBBytes())
		require.NoError(t, os.WriteFile(dbPath+"-wal", []byte("pending frames"), 0o644))
		out, err := tryCommit(t, dir, "wal present")
		assert.Error(t, err, "commit should be blocked when -wal is non-empty: %s", out)
		assert.Contains(t, out, "capy checkpoint --project-dir", "message should name the remedy")
	})

	t.Run("zero-byte WAL is tolerated", func(t *testing.T) {
		dir, dbPath, _ := initTestDBRepo(t, encryptedDBBytes())
		require.NoError(t, os.WriteFile(dbPath+"-wal", []byte{}, 0o644)) // TRUNCATE can leave one
		out, err := tryCommit(t, dir, "empty wal ok")
		assert.NoError(t, err, "commit should succeed with a zero-byte -wal and no -shm: %s", out)
	})

	t.Run("plaintext DB blocks", func(t *testing.T) {
		plaintext := make([]byte, 4096)
		copy(plaintext, append([]byte("SQLite format 3"), 0x00))
		dir, _, _ := initTestDBRepo(t, plaintext)
		out, err := tryCommit(t, dir, "plaintext")
		assert.Error(t, err, "commit should be blocked for an unencrypted DB: %s", out)
		assert.Contains(t, out, "capy encrypt", "message should name capy encrypt")
	})
}

// readTree returns a map of repo-relative path -> file contents for every file
// under dir, so two runs can be compared for idempotency.
func readTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		tree[rel] = string(data)
		return nil
	}))
	return tree
}
