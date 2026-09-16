package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// vault_codex_test.go drives the `capy vault` binary over a Codex home
// (codex-vault-sessions Slice 7.5): default discovery of every platform root,
// --platform, the 12-character Codex short id in the import table, `show`
// decoding with the stored platform, and `restore` writing the rollout back at
// its relative path — under --output and under the default Codex home.

const (
	codexCLIUUID = "019e22ba-4c15-7ae0-a903-255537a6a1b3"
	codexCLIRel  = "sessions/2026/05/01/rollout-2026-05-01T10-00-00-" + codexCLIUUID + ".jsonl"
)

// codexCLIRollout is a minimal legacy-mode rollout: session_meta, one human
// turn (a user_message event), one assistant reply, task_complete.
var codexCLIRollout = []byte(strings.Join([]string{
	`{"timestamp":"2026-05-01T10:00:00Z","type":"session_meta","payload":{"id":"` + codexCLIUUID + `","timestamp":"2026-05-01T09:58:30Z","cwd":"/home/user/proj","originator":"codex-tui","cli_version":"0.130.0","source":"cli","git":{"branch":"main"}}}`,
	`{"timestamp":"2026-05-01T10:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"Please fix the brontosaurus timeout"}}`,
	`{"timestamp":"2026-05-01T10:00:02Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Looking into the timeout now."}]}}`,
	`{"timestamp":"2026-05-01T10:00:03Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","last_agent_message":"Looking into the timeout now."}}`,
}, "\n") + "\n")

// setupCodexVaultEnv points the vault at a temp DB, gives Claude an empty config
// dir (no projects/ → its root is absent) and Codex a home holding one rollout.
func setupCodexVaultEnv(t *testing.T) (home string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CAPY_VAULT_KEY", testVaultKey)
	t.Setenv("CAPY_VAULT_PATH", filepath.Join(dir, "vault.db"))
	t.Setenv("CAPY_MACHINE_ID", "test-machine")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))

	home = filepath.Join(dir, "codex")
	t.Setenv("CODEX_HOME", home)
	target := filepath.Join(home, filepath.FromSlash(codexCLIRel))
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
	require.NoError(t, os.WriteFile(target, codexCLIRollout, 0o644))
	return home
}

func TestVaultCodex_ImportShowRestore(t *testing.T) {
	setupCodexVaultEnv(t)
	short := codexCLIUUID[:12]

	_, stderr, code := capy(t, "vault", "import", "--platform", "bogus")
	assert.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "invalid --platform")

	// Dry run: default discovery finds the Codex root without a --source, the
	// table shows the 12-character Codex short id, and nothing is written.
	stdout, stderr, code := capy(t, "vault", "import", "--dry-run", "--platform", "codex")
	require.Equal(t, 0, code, "stderr: %s", stderr)
	assert.Contains(t, stdout, "DRY RUN")
	assert.Contains(t, stdout, short+"  new")
	assert.Contains(t, stdout, "imported 1, updated 0, skipped 0, excluded 0, errors 0")
	stdout, _, code = capy(t, "vault", "list")
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, "no sessions archived")

	stdout, stderr, code = capy(t, "vault", "import")
	require.Equal(t, 0, code, "stderr: %s", stderr)
	assert.Contains(t, stdout, "imported 1")

	stdout, _, code = capy(t, "vault", "list", "--json")
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, `"platform": "codex"`)
	assert.Contains(t, stdout, `"project_path": "/home/user/proj"`)

	// show decodes with the stored platform: the assistant heading names Codex.
	stdout, stderr, code = capy(t, "vault", "show", short)
	require.Equal(t, 0, code, "stderr: %s", stderr)
	assert.Contains(t, stdout, "[Codex]\nLooking into the timeout now.")
	assert.Contains(t, stdout, "[You]\nPlease fix the brontosaurus timeout")
	assert.NotContains(t, stdout, "[Claude]")

	stdout, _, code = capy(t, "vault", "search", "brontosaurus", "--role", "user")
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, codexCLIUUID[:8])

	// restore --output: the rollout lands at its relative path, byte-identical.
	out := t.TempDir()
	stdout, stderr, code = capy(t, "vault", "restore", short, "--output", out)
	require.Equal(t, 0, code, "stderr: %s", stderr)
	assert.Contains(t, stdout, "restored")
	got, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(codexCLIRel)))
	require.NoError(t, err)
	assert.Equal(t, codexCLIRollout, got, "restored rollout must match the source verbatim")
	_, err = os.Stat(filepath.Join(out, codexCLIUUID+".jsonl"))
	assert.True(t, os.IsNotExist(err), "no Claude-style <uuid>.jsonl for a Codex row")

	// Re-import of the unchanged rollout is idempotent.
	stdout, _, code = capy(t, "vault", "import")
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, "skipped 1")
}

// A run that touches both platforms prints a per-platform summary line under
// the total, --project matches the Codex cwd hint, and a skipped revert variant
// is counted after the summary.
func TestVaultCodex_MixedPlatformSummary(t *testing.T) {
	home := setupCodexVaultEnv(t)

	// A Claude session under the (otherwise empty) CLAUDE_CONFIG_DIR.
	claudeUUID := "abcd1234-aaaa-bbbb-cccc-1234567890ab"
	projDir := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "projects", "-home-user-other")
	require.NoError(t, os.MkdirAll(projDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projDir, claudeUUID+".jsonl"), []byte(strings.Join([]string{
		`{"type":"user","uuid":"u1","timestamp":"2026-05-01T10:00:00Z","cwd":"/home/user/other","message":{"role":"user","content":"Claude side"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-05-01T10:00:05Z","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"On it."}]}}`,
	}, "\n")+"\n"), 0o644))
	// A Codex revert variant beside the base rollout: recognised, skipped, counted.
	variant := filepath.Join(home, "sessions", "2026", "05", "01", "rollout-2026-05-01T10-00-00-"+codexCLIUUID+"_a1b2c3.jsonl")
	require.NoError(t, os.WriteFile(variant, codexCLIRollout, 0o644))

	stdout, stderr, code := capy(t, "vault", "import", "--dry-run")
	require.Equal(t, 0, code, "stderr: %s", stderr)
	assert.Contains(t, stdout, "\nimported 2, updated 0, skipped 0, excluded 0, errors 0\n")
	assert.Contains(t, stdout, "  claude-code: imported 1, updated 0, skipped 0, excluded 0, errors 0\n")
	assert.Contains(t, stdout, "  codex: imported 1, updated 0, skipped 0, excluded 0, errors 0\n")
	assert.Contains(t, stdout, "skipped 1 codex revert rollout variant(s)")
	assert.Contains(t, stdout, claudeUUID[:8]+"      new", "Claude rows keep the 8-char id (padded to the 12-wide column)")
	assert.Contains(t, stdout, codexCLIUUID[:12]+"  new", "Codex rows show 12")
	assert.Contains(t, stderr, "skipping codex revert rollout variant", "discovery warns with the path")

	// --project matches the Codex cwd hint (no scan needed) and excludes Claude.
	stdout, stderr, code = capy(t, "vault", "import", "--dry-run", "--project", "/home/user/proj")
	require.Equal(t, 0, code, "stderr: %s", stderr)
	assert.Contains(t, stdout, "imported 1,")
	assert.Contains(t, stdout, codexCLIUUID[:12])
	assert.NotContains(t, stdout, claudeUUID[:8])
	assert.NotContains(t, stdout, "  codex:", "a single-platform result prints no per-platform lines")
}

// Restore with no --output writes into the Codex home (CODEX_HOME), not the
// Claude projects dir, and leaves a compressed twin untouched with a note.
func TestVaultCodex_RestoreDefaultRootIsCodexHome(t *testing.T) {
	setupCodexVaultEnv(t)
	_, stderr, code := capy(t, "vault", "import")
	require.Equal(t, 0, code, "stderr: %s", stderr)

	// A fresh home: the restore must recreate the sessions/YYYY/MM/DD/ path.
	home2 := t.TempDir()
	t.Setenv("CODEX_HOME", home2)
	twin := filepath.Join(home2, filepath.FromSlash(codexCLIRel)+".zst")
	require.NoError(t, os.MkdirAll(filepath.Dir(twin), 0o755))
	require.NoError(t, os.WriteFile(twin, []byte("compressed twin"), 0o644))

	stdout, stderr, code := capy(t, "vault", "restore", codexCLIUUID[:12])
	require.Equal(t, 0, code, "stderr: %s", stderr)
	assert.Contains(t, stdout, "restored 1 file(s) to")
	assert.Contains(t, stderr, "note: left a compressed copy of this session untouched at "+twin)

	got, err := os.ReadFile(filepath.Join(home2, filepath.FromSlash(codexCLIRel)))
	require.NoError(t, err)
	assert.Equal(t, codexCLIRollout, got)
	twinBytes, err := os.ReadFile(twin)
	require.NoError(t, err)
	assert.Equal(t, []byte("compressed twin"), twinBytes, "the .zst twin is never touched")
}
