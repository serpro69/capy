package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testVaultKey = "test-vault-cli-key-at-least-32-characters!!"

// setupVaultEnv points the vault at a temp DB and writes one fixture session
// under a single project directory. It returns the project root (for --path) and
// the session UUID. Env vars flow to the `go run` subprocess via os.Environ().
func setupVaultEnv(t *testing.T) (root, uuid string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CAPY_VAULT_KEY", testVaultKey)
	t.Setenv("CAPY_VAULT_PATH", filepath.Join(dir, "vault.db"))
	t.Setenv("CAPY_MACHINE_ID", "test-machine") // avoid touching ~/.config/capy

	root = filepath.Join(dir, "project")
	require.NoError(t, os.MkdirAll(root, 0o755))
	uuid = "abcd1234-aaaa-bbbb-cccc-1234567890ab"
	lines := []string{
		`{"type":"user","uuid":"u1","timestamp":"2026-05-01T10:00:00Z","cwd":"/home/user/proj","gitBranch":"main","message":{"role":"user","content":"Please fix the brontosaurus timeout"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-05-01T10:00:05Z","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"Looking into the timeout now."},{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/proj/config.toml"}}]}}`,
		`{"type":"ai-title","aiTitle":"Fix the brontosaurus timeout","sessionId":"s1"}`,
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, uuid+".jsonl"),
		[]byte(strings.Join(lines, "\n")+"\n"), 0o644))
	return root, uuid
}

func TestVaultCommands_EndToEnd(t *testing.T) {
	root, uuid := setupVaultEnv(t)
	short := uuid[:8]

	stdout, stderr, code := capy(t, "vault", "import", "--path", root)
	require.Equal(t, 0, code, "stderr: %s", stderr)
	assert.Contains(t, stdout, "imported 1")

	stdout, _, code = capy(t, "vault", "list")
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, short)
	assert.Contains(t, stdout, "Fix the brontosaurus timeout")

	stdout, _, code = capy(t, "vault", "search", "brontosaurus")
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, short)

	// "brontosaurus" is in the human user message, so it must NOT match --role tool.
	stdout, _, code = capy(t, "vault", "search", "brontosaurus", "--role", "tool")
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, "no matches")

	stdout, _, code = capy(t, "vault", "show", short, "--format", "markdown")
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, "# Fix the brontosaurus timeout")
	assert.Contains(t, stdout, "Looking into the timeout now.")

	stdout, _, code = capy(t, "vault", "show", short, "--format", "json")
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, `"aiTitle":"Fix the brontosaurus timeout"`, "json format is the verbatim JSONL")

	stdout, _, code = capy(t, "vault", "stats", "--json")
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, `"sessions": 1`)

	stdout, _, code = capy(t, "vault", "checkpoint")
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, "WAL flushed")

	// Re-import of an unchanged session is idempotent.
	stdout, _, code = capy(t, "vault", "import", "--path", root)
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, "skipped 1")
}

func TestVaultRequiresKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CAPY_VAULT_KEY", "")
	t.Setenv("CAPY_VAULT_PATH", filepath.Join(dir, "vault.db"))
	_, stderr, code := capy(t, "vault", "list")
	assert.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "CAPY_VAULT_KEY")
}

func TestVaultShowTUINotImplemented(t *testing.T) {
	root, uuid := setupVaultEnv(t)
	_, stderr, code := capy(t, "vault", "import", "--path", root)
	require.Equal(t, 0, code, "stderr: %s", stderr)

	_, stderr, code = capy(t, "vault", "show", uuid[:8], "--tui")
	assert.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "not yet implemented")
}
