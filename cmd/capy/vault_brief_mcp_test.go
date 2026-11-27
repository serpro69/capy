package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVaultBrief_MCPMatchesCLI drives the built binary over MCP stdio and
// compares each decoded capy_vault_brief text item with `capy vault brief`
// stdout for the same archive (vault-session-brief Task 2.1).
func TestVaultBrief_MCPMatchesCLI(t *testing.T) {
	bin := buildStdioCapy(t)
	const key = "synthetic-stdio-brief-passphrase-123456"
	const vaultKey = "synthetic-stdio-brief-vault-passphrase-12345"

	claudeRoot := setupBriefClaudeVault(t) // transcripts only; the child env below selects the vault
	codexHome := t.TempDir()
	for rel, body := range map[string][]byte{codexCLIRel: codexCLIRollout, codexChildRel: codexChildRollout} {
		target := filepath.Join(codexHome, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
		require.NoError(t, os.WriteFile(target, body, 0o644))
	}
	project := stdioProject(t)
	env := append(stdioEnv(t, key), "CAPY_VAULT_KEY="+vaultKey, "CAPY_MACHINE_ID=brief-test", "CODEX_HOME="+codexHome)

	run := func(t *testing.T, args ...string) (stdout string, code int) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), bin, args...)
		cmd.Env, cmd.Dir = env, project
		out, err := cmd.Output()
		if exitErr, ok := err.(*exec.ExitError); ok {
			return string(out), exitErr.ExitCode()
		}
		require.NoError(t, err)
		return string(out), 0
	}
	for _, args := range [][]string{{"vault", "import", "--source", claudeRoot}, {"vault", "import"}} {
		_, code := run(t, args...)
		require.Zero(t, code, "%v", args)
	}

	p := startStdioProcess(t, stdioCommand{
		executable: bin, args: []string{"serve", "--project-dir", project},
		dir: project, env: env, secrets: []string{key, vaultKey},
	})
	p.initialize(t)

	var listed struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	require.NoError(t, p.request("tools/list", map[string]any{}, &listed))
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	assert.Contains(t, names, "capy_vault_brief")

	// The fixtures were imported from outside the server's project, so the MCP
	// calls widen to every project; the CLI resolves across the whole vault.
	for _, session := range []string{briefToolUUID, briefPendingUUID[:13], codexCLIUUID, codexChildUUID} {
		t.Run(session, func(t *testing.T) {
			stdout, code := run(t, "vault", "brief", session)
			require.Zero(t, code)
			require.NotEmpty(t, stdout)
			isError, text := stdioBooleanCall(t, p, "capy_vault_brief", map[string]any{"session": session, "all_projects": true})
			require.False(t, isError, text)
			assert.Equal(t, stdout, text, "the decoded MCP text equals CLI stdout")
		})
	}

	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"ambiguous prefix", map[string]any{"session": "b1b1b1b1", "all_projects": true}, "use a longer ID"},
		{"default scope miss", map[string]any{"session": briefToolUUID}, "no session in the requested scope matches"},
		{"historical cutoff", map[string]any{"session": briefToolUUID, "through_line": 3}, "through_line is not supported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isError, text := stdioBooleanCall(t, p, "capy_vault_brief", tc.args)
			assert.True(t, isError, text)
			assert.Contains(t, text, tc.want)
		})
	}
	p.close(t)
}
