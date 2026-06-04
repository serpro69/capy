package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testVaultSweepKey = "test-vault-sweep-key-at-least-32-characters!!"

// setupVaultSweepProject lays out a fake HOME with Claude Code sessions for one
// project (the layout vaultSweep resolves via vault.ProjectSessionDir →
// config.ClaudeProjectsDir) and points the vault at a temp DB. It returns the
// real project dir to hand the server, and the two session UUIDs written.
func setupVaultSweepProject(t *testing.T) (projectDir, uuid1, uuid2 string) {
	t.Helper()
	tmpHome := t.TempDir()
	projectDir = t.TempDir()

	t.Setenv("HOME", tmpHome)
	t.Setenv("CAPY_VAULT_PATH", filepath.Join(t.TempDir(), "vault.db"))
	t.Setenv("CAPY_MACHINE_ID", "test-machine") // avoid touching ~/.config/capy

	// Create the sessions where ProjectSessionDir (the resolver the sweep uses)
	// says they live, so setup and the code under test cannot drift. The exact
	// mangling is pinned independently by TestProjectSessionDir_* in the vault pkg.
	sessionDir, err := vault.ProjectSessionDir(projectDir)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	uuid1 = "sweep-sess-aaaa"
	uuid2 = "sweep-sess-bbbb"
	writeSessionJSONL(t, sessionDir, uuid1, "How do I configure the database?",
		"Set DATABASE_URL in your environment to configure the database.")
	writeSessionJSONL(t, sessionDir, uuid2, "Explain goroutine scheduling",
		"Go uses an M:N scheduler with work stealing across OS threads.")
	return projectDir, uuid1, uuid2
}

func TestVaultSweep_ImportsCurrentProjectSessions(t *testing.T) {
	projectDir, uuid1, uuid2 := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)

	srv := newTestServerWithProjectDir(t, nil, projectDir)
	srv.vaultSweep(context.Background())

	// Open the vault independently and verify both sessions were archived.
	st := vault.NewVaultStore(vault.VaultDBPath())
	t.Cleanup(func() { _ = st.Close() })

	sessions, err := st.ListSessions(vault.ListOptions{})
	require.NoError(t, err)
	require.Len(t, sessions, 2)

	got := map[string]bool{}
	for _, s := range sessions {
		got[s.UUID] = true
	}
	assert.True(t, got[uuid1], "session %s should be archived", uuid1)
	assert.True(t, got[uuid2], "session %s should be archived", uuid2)
}

func TestVaultSweep_SkipsSilentlyWithoutKey(t *testing.T) {
	projectDir, _, _ := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", "") // vault is opt-in

	srv := newTestServerWithProjectDir(t, nil, projectDir)

	// Must not panic, must not create the vault DB.
	require.NotPanics(t, func() { srv.vaultSweep(context.Background()) })

	_, err := os.Stat(vault.VaultDBPath())
	assert.True(t, os.IsNotExist(err), "vault DB must not be created when CAPY_VAULT_KEY is unset")
}

// A project whose Claude session directory does not exist yet (the common case
// at first startup) is a no-op, not a failure — and it must not create a vault.
func TestVaultSweep_NoSessionsIsNoOp(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
	t.Setenv("CAPY_VAULT_PATH", filepath.Join(t.TempDir(), "vault.db"))
	t.Setenv("CAPY_MACHINE_ID", "test-machine")

	srv := newTestServerWithProjectDir(t, nil, t.TempDir())
	require.NotPanics(t, func() { srv.vaultSweep(context.Background()) })

	_, err := os.Stat(vault.VaultDBPath())
	assert.True(t, os.IsNotExist(err), "vault DB must not be created when there are no sessions to import")
}
