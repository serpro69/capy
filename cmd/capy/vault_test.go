package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// setupVaultWithSidecar writes a fixture session that also has a subagent
// sidecar so restore exercises vault_files. Returns the project root (for
// --path), the uuid, and the raw main + sidecar bytes for verbatim diffing.
func setupVaultWithSidecar(t *testing.T) (root, uuid string, mainBytes, sidecarBytes []byte) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CAPY_VAULT_KEY", testVaultKey)
	t.Setenv("CAPY_VAULT_PATH", filepath.Join(dir, "vault.db"))
	t.Setenv("CAPY_MACHINE_ID", "test-machine")

	root = filepath.Join(dir, "project")
	require.NoError(t, os.MkdirAll(root, 0o755))
	uuid = "abcd1234-aaaa-bbbb-cccc-1234567890ab"
	mainBytes = []byte(strings.Join([]string{
		`{"type":"user","uuid":"u1","timestamp":"2026-05-01T10:00:00Z","cwd":"/home/user/proj","gitBranch":"main","message":{"role":"user","content":"Fix the brontosaurus timeout"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-05-01T10:00:05Z","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"On it."},{"type":"tool_use","id":"t1","name":"Task","input":{"description":"investigate","prompt":"look"}}]}}`,
		`{"type":"ai-title","aiTitle":"Fix the brontosaurus timeout","sessionId":"s1"}`,
	}, "\n") + "\n")
	require.NoError(t, os.WriteFile(filepath.Join(root, uuid+".jsonl"), mainBytes, 0o644))

	sidecarBytes = []byte(`{"type":"assistant","message":{"id":"sm1","role":"assistant","content":[{"type":"text","text":"stegosaurus subagent finding"}]}}` + "\n")
	subDir := filepath.Join(root, uuid, "subagents")
	require.NoError(t, os.MkdirAll(subDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(subDir, "agent-7.jsonl"), sidecarBytes, 0o644))
	return root, uuid, mainBytes, sidecarBytes
}

func TestVaultRestoreToOutputDir(t *testing.T) {
	root, uuid, mainBytes, sidecarBytes := setupVaultWithSidecar(t)
	short := uuid[:8]

	_, stderr, code := capy(t, "vault", "import", "--path", root)
	require.Equal(t, 0, code, "stderr: %s", stderr)

	out := t.TempDir()
	stdout, stderr, code := capy(t, "vault", "restore", short, "--output", out)
	require.Equal(t, 0, code, "stderr: %s", stderr)
	assert.Contains(t, stdout, "restored")

	gotMain, err := os.ReadFile(filepath.Join(out, uuid+".jsonl"))
	require.NoError(t, err)
	assert.Equal(t, mainBytes, gotMain, "restored main JSONL must match the original verbatim")

	gotSide, err := os.ReadFile(filepath.Join(out, uuid, "subagents", "agent-7.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, sidecarBytes, gotSide, "restored sidecar must match the original verbatim")
}

// Restore with no --output writes into the session's Claude projects dir,
// honoring CLAUDE_CONFIG_DIR. claude_project_dir is the basename of the imported
// project directory ("project").
func TestVaultRestoreDefaultLocationHonorsConfigDir(t *testing.T) {
	root, uuid, mainBytes, _ := setupVaultWithSidecar(t)
	short := uuid[:8]

	_, stderr, code := capy(t, "vault", "import", "--path", root)
	require.Equal(t, 0, code, "stderr: %s", stderr)

	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	_, stderr, code = capy(t, "vault", "restore", short)
	require.Equal(t, 0, code, "stderr: %s", stderr)

	got, err := os.ReadFile(filepath.Join(cfg, "projects", "project", uuid+".jsonl"))
	require.NoError(t, err)
	assert.Equal(t, mainBytes, got)
}

func TestVaultDeleteWithYes(t *testing.T) {
	root, uuid := setupVaultEnv(t)
	short := uuid[:8]

	_, stderr, code := capy(t, "vault", "import", "--path", root)
	require.Equal(t, 0, code, "stderr: %s", stderr)

	stdout, stderr, code := capy(t, "vault", "delete", short, "--yes")
	require.Equal(t, 0, code, "stderr: %s", stderr)
	assert.Contains(t, stdout, "deleted "+short)

	stdout, _, code = capy(t, "vault", "list")
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, "no sessions archived")

	// FTS rows are gone too — the deleted content is no longer searchable.
	stdout, _, code = capy(t, "vault", "search", "brontosaurus")
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, "no matches")
}

// Without --yes and with no interactive stdin, delete must abort (the safe
// default) and leave the session intact.
func TestVaultDeleteAbortsWithoutConfirmation(t *testing.T) {
	root, uuid := setupVaultEnv(t)
	short := uuid[:8]

	_, stderr, code := capy(t, "vault", "import", "--path", root)
	require.Equal(t, 0, code, "stderr: %s", stderr)

	stdout, _, code := capy(t, "vault", "delete", short)
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, "aborted")

	stdout, _, code = capy(t, "vault", "list")
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, short, "session must survive an aborted delete")
}

// writeClaudeStub installs a fake `claude` first on PATH that records its args
// and physical cwd to outFile and exits with exitCode.
func writeClaudeStub(t *testing.T, outFile string, exitCode int) {
	t.Helper()
	binDir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\necho \"ARGS: $@\" > %q\necho \"PWD: $(pwd -P)\" >> %q\nexit %d\n",
		outFile, outFile, exitCode)
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestVaultResumeLaunchesClaude(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh stub")
	}
	root, uuid := setupVaultEnv(t)
	short := uuid[:8]
	_, stderr, code := capy(t, "vault", "import", "--path", root)
	require.Equal(t, 0, code, "stderr: %s", stderr)

	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir()) // restore lands here, not real ~/.claude
	rec := filepath.Join(t.TempDir(), "claude-invocation.txt")
	writeClaudeStub(t, rec, 0)

	launchDir := t.TempDir()
	_, stderr, code = capy(t, "vault", "resume", short, "--dir", launchDir)
	require.Equal(t, 0, code, "stderr: %s", stderr)

	got, err := os.ReadFile(rec)
	require.NoError(t, err, "the stub claude should have been launched")
	assert.Contains(t, string(got), "--resume "+uuid)
	resolved, err := filepath.EvalSymlinks(launchDir)
	require.NoError(t, err)
	assert.Contains(t, string(got), "PWD: "+resolved, "claude must launch in the resolved --dir")
}

func TestVaultResumePropagatesExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh stub")
	}
	root, uuid := setupVaultEnv(t)
	short := uuid[:8]

	// `go run` always exits 1 on a non-zero child (it only prints "exit status
	// N" to stderr), masking the propagated code — so build the binary and exec
	// it directly to observe capy's real exit status.
	bin := filepath.Join(t.TempDir(), "capy")
	build := exec.Command("go", "build", "-tags", "fts5", "-o", bin, ".")
	build.Env = os.Environ()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build capy: %v\n%s", err, out)
	}

	imp := exec.Command(bin, "vault", "import", "--path", root)
	imp.Env = os.Environ()
	require.NoError(t, imp.Run(), "import via built binary")

	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	writeClaudeStub(t, filepath.Join(t.TempDir(), "rec.txt"), 3)

	resume := exec.Command(bin, "vault", "resume", short, "--dir", t.TempDir())
	resume.Env = os.Environ()
	err := resume.Run()

	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee, "resume should exit non-zero when claude does")
	assert.Equal(t, 3, ee.ExitCode(), "capy must propagate claude's own exit code")
}
