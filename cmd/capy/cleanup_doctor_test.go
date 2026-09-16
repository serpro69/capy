package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/serpro69/capy/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const cliTestKey = "test-passphrase-at-least-32-characters-long!!"

// newCLIProject creates a project dir whose config keeps the knowledge DB inside
// the temp dir (never leaking to ~/.local/share/capy/) and returns the dir and
// the resolved DB path.
func newCLIProject(t *testing.T) (dir, dbPath string) {
	t.Helper()
	t.Setenv("CAPY_DB_KEY", cliTestKey)
	dir = t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".capy.toml"),
		[]byte("[store]\npath = \"test.db\"\n"),
		0o644,
	))
	return dir, filepath.Join(dir, "test.db")
}

// ─── cleanup --optimize / --vacuum (issue #82) ─────────────────────────────────

func TestCleanupSubcommand_OptimizeStandalone(t *testing.T) {
	dir, dbPath := newCLIProject(t)
	st := store.NewContentStore(dbPath, dir, 0, 0)
	_, err := st.Index("# Auth\n\nJWT validation middleware.", "auth-doc", "", store.KindDurable)
	require.NoError(t, err)
	require.NoError(t, st.Close())

	// --optimize alone ignores the dry-run default (ADR-029 §4).
	stdout, stderr, code := capy(t, "cleanup", "--optimize", "--project-dir", dir)
	assert.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "optimize complete")
	assert.NotContains(t, stdout, "skipped")

	st2 := store.NewContentStore(dbPath, dir, 0, 0)
	defer st2.Close()
	sources, err := st2.ListSources()
	require.NoError(t, err)
	require.Len(t, sources, 1)
	assert.Equal(t, "auth-doc", sources[0].Label)
}

func TestCleanupSubcommand_OptimizeOnFreshProject(t *testing.T) {
	// Regression: --optimize as the very first operation on a project with no
	// knowledge DB yet must create the schema rather than fail with
	// "no such table: chunks".
	dir, _ := newCLIProject(t)
	stdout, stderr, code := capy(t, "cleanup", "--optimize", "--project-dir", dir)
	assert.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "optimize complete")
}

func TestCleanupSubcommand_VacuumStandalone(t *testing.T) {
	dir, _ := newCLIProject(t)
	stdout, stderr, code := capy(t, "cleanup", "--vacuum", "--project-dir", dir)
	assert.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "vacuum complete")
}

func TestCleanupSubcommand_OptimizeSkippedLoudlyOnDryRun(t *testing.T) {
	dir, _ := newCLIProject(t)

	// An eviction selector without --force is a dry run: reclamation must not
	// run, and the output must say so instead of silently no-op'ing.
	stdout, stderr, code := capy(t, "cleanup", "--kind", "ephemeral", "--optimize", "--project-dir", dir)
	assert.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "--optimize skipped (dry run)")
	assert.NotContains(t, stdout, "optimize complete")
}

func TestCleanupSubcommand_OptimizeAfterForce(t *testing.T) {
	dir, _ := newCLIProject(t)
	stdout, stderr, code := capy(t, "cleanup", "--force", "--optimize", "--project-dir", dir)
	assert.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "no evictable sources found")
	assert.Contains(t, stdout, "optimize complete")
}

func TestCleanupSubcommand_OptimizeAfterExplicitDryRunFalse(t *testing.T) {
	// Review finding (#82): an explicit --dry-run=false must count as an
	// eviction request just like --force, so --optimize runs AFTER the
	// eviction pass instead of taking the standalone reclaim-only path. This
	// matches the MCP tool, where `dry_run: false` plays the same role.
	dir, _ := newCLIProject(t)
	stdout, stderr, code := capy(t, "cleanup", "--dry-run=false", "--optimize", "--project-dir", dir)
	assert.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "no evictable sources found")
	assert.Contains(t, stdout, "optimize complete")
}

func TestCleanupSubcommand_OptimizeAfterSourceEviction(t *testing.T) {
	dir, dbPath := newCLIProject(t)
	st := store.NewContentStore(dbPath, dir, 0, 0)
	_, err := st.Index("# Doomed\n\nEvicted by label.", "doomed", "", store.KindDurable)
	require.NoError(t, err)
	_, err = st.Index("# Kept\n\nStays put.", "kept", "", store.KindDurable)
	require.NoError(t, err)
	require.NoError(t, st.Close())

	stdout, stderr, code := capy(t, "cleanup", "--source", "doomed", "--force", "--optimize", "--project-dir", dir)
	assert.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, `removed source "doomed"`)
	assert.Contains(t, stdout, "optimize complete")

	st2 := store.NewContentStore(dbPath, dir, 0, 0)
	defer st2.Close()
	sources, err := st2.ListSources()
	require.NoError(t, err)
	require.Len(t, sources, 1)
	assert.Equal(t, "kept", sources[0].Label)
}

// ─── doctor parity with capy_doctor (issue #82) ────────────────────────────────

func TestDoctorSubcommand_KnowledgeBaseStatsAndLegacySessions(t *testing.T) {
	dir, dbPath := newCLIProject(t)
	t.Setenv("CAPY_VAULT_KEY", "")
	st := store.NewContentStore(dbPath, dir, 0, 0)
	_, err := st.Index("session transcript content", "session:2026-05-01T00:00:00Z:test-uuid", "session", store.KindSession)
	require.NoError(t, err)
	_, err = st.Index("# Durable\n\nReference content.", "durable-doc", "", store.KindDurable)
	require.NoError(t, err)
	require.NoError(t, st.Close())

	stdout, stderr, code := capy(t, "doctor", "--project-dir", dir)
	assert.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "[x] Knowledge base: 2 sources")
	assert.Contains(t, stdout, "[-] Legacy sessions: 1 legacy knowledge.db session row(s)")
	assert.Contains(t, stdout, "`capy cleanup --kind session --force`")
	assert.Contains(t, stdout, "[-] Vault: disabled (CAPY_VAULT_KEY not set)")
}

func TestDoctorSubcommand_KnowledgeBaseNotInitialized(t *testing.T) {
	dir, dbPath := newCLIProject(t)
	stdout, stderr, code := capy(t, "doctor", "--project-dir", dir)
	assert.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "Knowledge base: not initialized")
	// A diagnostic must not create the DB as a side effect.
	_, err := os.Stat(dbPath)
	assert.True(t, os.IsNotExist(err), "doctor must not create the knowledge DB")
}

func TestDoctorSubcommand_VaultEnabledButNotCreated(t *testing.T) {
	dir, _ := newCLIProject(t)
	vaultPath := filepath.Join(t.TempDir(), "vault.db")
	t.Setenv("CAPY_VAULT_KEY", "test-key")
	t.Setenv("CAPY_VAULT_PATH", vaultPath)

	stdout, stderr, code := capy(t, "doctor", "--project-dir", dir)
	assert.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "[x] Vault: enabled — no sessions archived yet")
	_, err := os.Stat(vaultPath)
	assert.True(t, os.IsNotExist(err), "doctor must not create the vault DB")
}

// ─── doctor: vault platform roots (codex-vault-sessions Task 12) ───────────────

// pinGoEnvForEmptyHome fixes the Go cache/module paths in the environment so a
// test may blank $HOME (to make os.UserHomeDir fail inside the binary) without
// also breaking the `go run` that capy() uses to build it — `go` derives
// GOCACHE, GOMODCACHE and GOPATH from $HOME when they are unset.
func pinGoEnvForEmptyHome(t *testing.T) {
	t.Helper()
	out, err := exec.Command("go", "env", "GOCACHE", "GOMODCACHE", "GOPATH").Output()
	require.NoError(t, err)
	vals := strings.Split(strings.TrimSpace(string(out)), "\n")
	require.Len(t, vals, 3)
	for i, k := range []string{"GOCACHE", "GOMODCACHE", "GOPATH"} {
		t.Setenv(k, strings.TrimSpace(vals[i])) // `go env` prints \r\n on Windows
	}
}

func TestDoctorSubcommand_VaultPlatformsWithoutDB(t *testing.T) {
	dir, _ := newCLIProject(t)
	vaultPath := filepath.Join(t.TempDir(), "vault.db")
	t.Setenv("CAPY_VAULT_KEY", "test-key")
	t.Setenv("CAPY_VAULT_PATH", vaultPath)
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	claude := filepath.Join(cfg, "projects")
	require.NoError(t, os.MkdirAll(claude, 0o755))
	codex := filepath.Join(t.TempDir(), ".codex")
	t.Setenv("CODEX_HOME", codex)

	// No vault DB yet: the roots are still reported (0 archived) so the user
	// can see what the first sweep will walk — and the diagnostic still must
	// not create the DB.
	stdout, stderr, code := capy(t, "doctor", "--project-dir", dir)
	assert.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "[x] Vault: enabled — no sessions archived yet")
	assert.Contains(t, stdout, "[x] Vault platforms: claude-code: 0 archived ("+claude+"); codex: 0 archived ("+codex+" absent — not swept)")
	_, err := os.Stat(vaultPath)
	assert.True(t, os.IsNotExist(err), "doctor must not create the vault DB")

	// Disabled vault: no platforms line at all (parity with capy_doctor).
	t.Setenv("CAPY_VAULT_KEY", "")
	stdout, stderr, code = capy(t, "doctor", "--project-dir", dir)
	assert.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "[-] Vault: disabled (CAPY_VAULT_KEY not set)")
	assert.NotContains(t, stdout, "Vault platforms")
}

func TestDoctorSubcommand_VaultPlatformsResolutionError(t *testing.T) {
	dir, _ := newCLIProject(t)
	t.Setenv("CAPY_VAULT_KEY", "test-key")
	t.Setenv("CAPY_VAULT_PATH", filepath.Join(t.TempDir(), "vault.db"))
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CODEX_HOME", "")
	pinGoEnvForEmptyHome(t)
	t.Setenv("HOME", "")        // CodexHome falls back to $HOME, which is now unresolvable
	t.Setenv("USERPROFILE", "") // os.UserHomeDir reads this on Windows

	stdout, stderr, code := capy(t, "doctor", "--project-dir", dir)
	assert.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "[-] Vault platforms: error resolving platform roots (resolving codex home:")
}
