package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	briefToolUUID    = "b1b1b1b1-aaaa-bbbb-cccc-000000000001"
	briefPendingUUID = "b1b1b1b1-dddd-bbbb-cccc-000000000002"
)

// setupBriefClaudeVault writes two Claude sessions under one project:
//   - briefToolUUID: one request answered by a tool-only assistant entry, plus a
//     sidecar whose lines 0 and 1 overlap the main transcript's coordinates.
//   - briefPendingUUID: an answered request, then a later unanswered one.
func setupBriefClaudeVault(t *testing.T) (root string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CAPY_VAULT_KEY", testVaultKey)
	t.Setenv("CAPY_VAULT_PATH", filepath.Join(dir, "vault.db"))
	t.Setenv("CAPY_MACHINE_ID", "test-machine")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	root = filepath.Join(dir, "project")
	require.NoError(t, os.MkdirAll(root, 0o755))
	write := func(name string, lines ...string) {
		t.Helper()
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(strings.Join(lines, "\n")+"\n"), 0o644))
	}
	write(briefToolUUID+".jsonl",
		`{"type":"user","uuid":"u1","timestamp":"2026-05-01T10:00:00Z","cwd":"/home/user/proj","message":{"role":"user","content":"Run the release checks"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-05-01T10:00:05Z","message":{"id":"m1","role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"make test"}}]}}`,
		`{"type":"ai-title","aiTitle":"Release checks","sessionId":"s1"}`,
	)
	write(filepath.Join(briefToolUUID, "subagents", "agent-7.jsonl"),
		`{"type":"user","message":{"role":"user","content":"sidecar request"}}`,
		`{"type":"assistant","message":{"id":"sm1","role":"assistant","content":[{"type":"text","text":"sidecar finding"}]}}`,
	)
	write(briefPendingUUID+".jsonl",
		`{"type":"user","uuid":"u1","timestamp":"2026-05-01T11:00:00Z","cwd":"/home/user/proj","message":{"role":"user","content":"Check the release."}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-05-01T11:00:05Z","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"Started."}]}}`,
		`{"type":"user","uuid":"u2","timestamp":"2026-05-01T11:01:00Z","message":{"role":"user","content":"Which checks are still running?"}}`,
	)
	return root
}

func TestVaultBrief_Claude(t *testing.T) {
	root := setupBriefClaudeVault(t)
	_, stderr, code := capy(t, "vault", "import", "--source", root)
	require.Equal(t, 0, code, "stderr: %s", stderr)

	stdout, stderr, code := capy(t, "vault", "brief", briefToolUUID[:13])
	require.Equal(t, 0, code, "stderr: %s", stderr)
	assert.Empty(t, stderr)
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	require.Len(t, lines, 4, stdout)
	assert.Equal(t, `Session "`+briefToolUUID+`" · claude-code`, lines[0])
	assert.Equal(t, `Project "/home/user/proj" · Title "Release checks"`, lines[1])
	assert.Equal(t, `Opening and latest user · message starts at line 0: "Run the release checks"`, lines[2])
	assert.True(t, strings.HasPrefix(lines[3], "Latest assistant · message starts at line 1: "), lines[3])
	assert.Contains(t, lines[3], "make test", "a tool-only assistant entry qualifies")
	assert.NotContains(t, stdout, "sidecar", "sidecar rows keep their own coordinate space")

	stdout, stderr, code = capy(t, "vault", "brief", briefPendingUUID)
	require.Equal(t, 0, code, "stderr: %s", stderr)
	assert.Equal(t, `Session "`+briefPendingUUID+`" · claude-code
Project "/home/user/proj" · Title "Check the release."
Opening user · message starts at line 0: "Check the release."
Latest user · message starts at line 2: "Which checks are still running?"
No indexed assistant entry in this turn.
`, stdout)
}

func TestVaultBrief_Codex(t *testing.T) {
	home := setupCodexVaultEnv(t)
	writeCodexChild(t, home)
	_, stderr, code := capy(t, "vault", "import")
	require.Equal(t, 0, code, "stderr: %s", stderr)

	stdout, stderr, code := capy(t, "vault", "brief", codexCLIUUID[:12])
	require.Equal(t, 0, code, "stderr: %s", stderr)
	assert.Equal(t, `Session "`+codexCLIUUID+`" · codex
Project "/home/user/proj" · Title "Please fix the brontosaurus timeout"
Opening and latest user · message starts at line 1: "Please fix the brontosaurus timeout"
Latest assistant · message starts at line 2: "Looking into the timeout now."
`, stdout)

	// An independently archived child has no indexed human request.
	stdout, stderr, code = capy(t, "vault", "brief", codexChildUUID)
	require.Equal(t, 0, code, "stderr: %s", stderr)
	assert.Contains(t, stdout, `Session "`+codexChildUUID+`" · codex`)
	assert.Contains(t, stdout, "No indexed user entry in this session.\n")
}

func TestVaultBrief_Errors(t *testing.T) {
	root := setupBriefClaudeVault(t)

	// Before import: the vault file is absent and the brief must not create it.
	_, stderr, code := capy(t, "vault", "brief", briefToolUUID[:13])
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "no vault database")
	_, err := os.Stat(os.Getenv("CAPY_VAULT_PATH"))
	assert.ErrorIs(t, err, os.ErrNotExist)

	_, stderr, code = capy(t, "vault", "import", "--source", root)
	require.Equal(t, 0, code, "stderr: %s", stderr)

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "scoped miss", args: []string{"ffffffff"}, want: `no session matches "ffffffff"`},
		{name: "ambiguous prefix", args: []string{"b1b1b1b1"}, want: "matches more than one session — use a longer ID"},
		{name: "short prefix", args: []string{"b1b1"}, want: "at least 8"},
		{name: "literal wildcard", args: []string{"b1b1b1b%"}, want: "no session matches"},
		{name: "tui rejected", args: []string{briefToolUUID[:13], "--tui"}, want: "not supported"},
		{name: "cutoff rejected", args: []string{briefToolUUID[:13], "--through-line", "3"}, want: "--through-line is not supported"},
		{name: "one positional", args: []string{}, want: "accepts 1 arg"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, code := capy(t, append([]string{"vault", "brief"}, tt.args...)...)
			assert.Equal(t, 1, code)
			assert.Empty(t, stdout, "no brief is written on failure")
			assert.Contains(t, stderr, tt.want)
		})
	}
}
