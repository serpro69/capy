package platform

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

const testCodexOwnerID = "0123456789abcdef0123456789abcdef"

// Codex persists this metadata beside inline hook event arrays in config.toml.
const codexHookStateFixture = `[hooks.state."/fixture/project/.codex/hooks.json:pre_tool_use:0:0"]
enabled = true
trusted_hash = "fixture-trusted-hash"

[hooks.state."capy-codex-launch-v99"]
enabled = false
trusted_hash = "opaque-host-metadata"
`

func writeCodexFixture(t *testing.T, dir, rel, text string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
}

func codexHookFixture(t *testing.T, id string) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{"hooks": map[string]any{"PreToolUse": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": codexHookCommand(id, "pretooluse")}}}}}})
	require.NoError(t, err)
	return string(data)
}

func TestCodexOwnerMarker(t *testing.T) {
	t.Run("concurrent creation and preservation", func(t *testing.T) {
		dir := t.TempDir()
		var wg sync.WaitGroup
		ids, errs := make([]string, 24), make([]error, 24)
		for i := range ids {
			wg.Add(1)
			go func() { defer wg.Done(); ids[i], errs[i] = ensureCodexOwner(dir) }()
		}
		wg.Wait()
		for i := range ids {
			require.NoError(t, errs[i])
			require.Equal(t, ids[0], ids[i])
		}
		again, err := ensureCodexOwner(dir)
		require.NoError(t, err)
		require.Equal(t, ids[0], again)
	})
	for _, marker := range []string{"", "bad", codexOwnerPrefix + strings.Repeat("A", 32) + "\n", codexOwnerContent(testCodexOwnerID) + "extra"} {
		t.Run("invalid "+marker, func(t *testing.T) {
			dir := t.TempDir()
			writeCodexFixture(t, dir, codexOwnerRelPath, marker)
			_, err := ensureCodexOwner(dir)
			require.ErrorContains(t, err, "restore")
			got, err := os.ReadFile(filepath.Join(dir, codexOwnerRelPath))
			require.NoError(t, err)
			require.Equal(t, marker, string(got))
		})
	}
	t.Run("missing live marker", func(t *testing.T) {
		dir := t.TempDir()
		writeCodexFixture(t, dir, ".codex/config.toml", codexMCPBlock(testCodexOwnerID))
		_, err := ensureCodexOwner(dir)
		require.ErrorContains(t, err, "restore the marker")
		require.NoFileExists(t, filepath.Join(dir, codexOwnerRelPath))
	})
	t.Run("obsolete launch rejected without conversion", func(t *testing.T) {
		dir := t.TempDir()
		content := strings.ReplaceAll(codexMCPBlock(testCodexOwnerID), codexLaunchVersion, "capy-codex-launch-v1")
		writeCodexFixture(t, dir, ".codex/config.toml", content)
		require.ErrorContains(t, setupCodex(dir), "unsupported")
		data, err := os.ReadFile(filepath.Join(dir, ".codex/config.toml"))
		require.NoError(t, err)
		require.Equal(t, content, string(data))
		require.NoFileExists(t, filepath.Join(dir, codexOwnerRelPath))
	})
}

func TestCodexOwnerBindings(t *testing.T) {
	command, err := json.Marshal(codexHookCommand(testCodexOwnerID, "pretooluse"))
	require.NoError(t, err)
	inlineHook := "[[hooks.PreToolUse]]\n[[hooks.PreToolUse.hooks]]\ntype='command'\ncommand=" + string(command) + "\n"
	for _, tc := range []struct{ name, file, content, want string }{
		{"MCP", "config.toml", codexMCPBlock(testCodexOwnerID), ""},
		{"MCP with hook trust state", "config.toml", codexHookStateFixture + codexMCPBlock(testCodexOwnerID), ""},
		{"inline hook with trust state", "config.toml", codexHookStateFixture + inlineHook, ""},
		{"invalid inline event with trust state", "config.toml", "[hooks]\nPreToolUse='invalid'\n" + codexHookStateFixture, "groups must be an array"},
		{"alias", "config.toml", strings.ReplaceAll(codexMCPBlock(testCodexOwnerID), "mcp_servers.capy", "mcp_servers.another"), ""},
		{"quoted table", "config.toml", strings.ReplaceAll(codexMCPBlock(testCodexOwnerID), "[mcp_servers.capy]", "['mcp_servers'.'capy']"), ""},
		{"hooks", "hooks.json", codexHookFixture(t, testCodexOwnerID), ""},
		{"escaped hook", "hooks.json", strings.ReplaceAll(codexHookFixture(t, testCodexOwnerID), "capy-codex-launch", `\u0063apy-codex-launch`), ""},
		{"invalid json", "hooks.json", "{", "invalid"},
		{"null", "hooks.json", "null", "invalid"},
		{"duplicate hooks key", "hooks.json", `{"hooks":{},"hooks":{}}`, "duplicate"},
		{"trailing JSON", "hooks.json", `{} {}`, "invalid"},
		{"wrong hooks type", "hooks.json", `{"hooks":[]}`, "hooks must"},
		{"wrong group type", "hooks.json", `{"hooks":{"PreToolUse":[false]}}`, "group must"},
		{"wrong handler type", "hooks.json", `{"hooks":{"PreToolUse":[{"hooks":[{"type":1}]}]}}`, "type must"},
		{"wrong MCP command", "config.toml", "[mcp_servers.other]\ncommand=123\n", "command must"},
		{"wrong MCP args", "config.toml", "[mcp_servers.other]\nargs='capy-codex-launch-v1'\n", "args must"},
		{"invalid toml", "config.toml", "[", "invalid"},
		{"oversize", "hooks.json", strings.Repeat(" ", codexConfigLimit+1), "exceeds"},
		{"unsupported version", "config.toml", strings.ReplaceAll(codexMCPBlock(testCodexOwnerID), codexLaunchVersion, "capy-codex-launch-v999"), "unsupported"},
		{"wrong ID", "config.toml", codexMCPBlock(strings.Repeat("a", 32)), "disagrees"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeCodexFixture(t, dir, codexOwnerRelPath, codexOwnerContent(testCodexOwnerID))
			writeCodexFixture(t, dir, ".codex/"+tc.file, tc.content)
			owners, err := InspectCodexOwners(dir)
			if tc.want != "" {
				require.ErrorContains(t, err, tc.want)
				return
			}
			require.NoError(t, err)
			require.Len(t, owners, 1)
			require.Len(t, owners[0].Bindings, 1)
			b := owners[0].Bindings[0]
			got, err := ResolveCodexOwner(dir, b.ID, b.Mode, b.Event)
			require.NoError(t, err)
			require.Equal(t, dir, got)
		})
	}
	t.Run("trust state does not hide an unbound inline hook", func(t *testing.T) {
		dir := t.TempDir()
		writeCodexFixture(t, dir, ".codex/config.toml", codexHookStateFixture+inlineHook)
		_, err := InspectCodexOwners(dir)
		require.ErrorContains(t, err, "no adjacent .codex/capy-owner")
	})
	t.Run("marker alone cannot authorize", func(t *testing.T) {
		dir := t.TempDir()
		writeCodexFixture(t, dir, codexOwnerRelPath, codexOwnerContent(testCodexOwnerID))
		_, err := ResolveCodexOwner(dir, testCodexOwnerID, "serve", "")
		require.ErrorContains(t, err, "no matching")
	})
	t.Run("unrelated configurations need no marker", func(t *testing.T) {
		dir := t.TempDir()
		writeCodexFixture(t, dir, ".codex/config.toml", "[mcp_servers.other]\ncommand='echo'\nargs=['capy is useful']\n")
		writeCodexFixture(t, dir, ".codex/hooks.json", `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo capy hook"}]}]}}`)
		owners, err := InspectCodexOwners(dir)
		require.NoError(t, err)
		require.Empty(t, owners)
	})
	t.Run("empty TOML is valid unrelated configuration", func(t *testing.T) {
		dir := t.TempDir()
		writeCodexFixture(t, dir, ".codex/config.toml", "# no settings yet\n")
		owners, err := InspectCodexOwners(dir)
		require.NoError(t, err)
		require.Empty(t, owners)
	})
	for _, rel := range []string{".codex", codexOwnerRelPath, ".codex/hooks.json", ".codex/config.toml"} {
		t.Run("symlink "+rel, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755))
			require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "missing"), filepath.Join(dir, rel)))
			_, err := InspectCodexOwners(dir)
			require.ErrorContains(t, err, "symlink")
		})
	}
	t.Run("unreadable file", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(dir, ".codex"), 0o755))
		_, err := inspectCodexDirectory(dir, func(path string, limit int64) ([]byte, bool, error) {
			return nil, true, &os.PathError{Op: "open", Path: path, Err: os.ErrPermission}
		})
		require.True(t, errors.Is(err, os.ErrPermission))
	})
	for _, rel := range []string{codexOwnerRelPath, ".codex/hooks.json", ".codex/config.toml"} {
		t.Run("nonregular "+rel, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(dir, rel), 0o755))
			_, err := InspectCodexOwners(dir)
			require.ErrorContains(t, err, "regular file")
		})
	}
}

func TestCodexHookStateConfiguration(t *testing.T) {
	for _, location := range []string{"ancestor", "project"} {
		t.Run(location, func(t *testing.T) {
			base := t.TempDir()
			project := filepath.Join(base, "project")
			cwd := filepath.Join(project, "subdir")
			require.NoError(t, os.MkdirAll(cwd, 0o755))
			stateDir := base
			if location == "project" {
				stateDir = project
			}
			writeCodexFixture(t, stateDir, ".codex/config.toml", codexHookStateFixture)
			owners, err := InspectCodexOwners(cwd)
			require.NoError(t, err)
			require.Empty(t, owners, "trust metadata alone is not an installation")

			require.NoError(t, setupCodex(project))
			owners, err = InspectCodexOwners(cwd)
			require.NoError(t, err)
			require.Len(t, owners, 1)
			for _, binding := range owners[0].Bindings {
				owner, err := ResolveCodexOwner(cwd, binding.ID, binding.Mode, binding.Event)
				require.NoError(t, err)
				require.Equal(t, project, owner)
			}
			checks := registrationChecks(t, cwd)
			require.Equal(t, Pass, checks["Codex hooks"].Status, checks)
			require.Equal(t, Pass, checks["Codex MCP"].Status, checks)
			require.Contains(t, checks["Codex activation"].Detail, "trust and execution are unverified")

			path := filepath.Join(stateDir, ".codex/config.toml")
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			if location == "ancestor" {
				require.Equal(t, codexHookStateFixture, string(before), "ancestor config must remain untouched")
			} else {
				require.True(t, strings.HasPrefix(string(before), codexHookStateFixture), "project trust metadata must remain byte-identical")
			}
			require.NoError(t, setupCodex(project))
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, before, after, "repeat setup must preserve trust metadata")
		})
	}
}

func TestCodexOverlappingInstallations(t *testing.T) {
	for _, id := range []string{testCodexOwnerID, strings.Repeat("b", 32)} {
		t.Run(id, func(t *testing.T) {
			root := t.TempDir()
			nested := filepath.Join(root, "nested")
			writeCodexFixture(t, root, codexOwnerRelPath, codexOwnerContent(testCodexOwnerID))
			writeCodexFixture(t, root, ".codex/config.toml", codexMCPBlock(testCodexOwnerID))
			require.NoError(t, os.Mkdir(nested, 0o755))
			_, err := ensureCodexOwner(nested)
			require.ErrorContains(t, err, "overlapping")
			writeCodexFixture(t, nested, codexOwnerRelPath, codexOwnerContent(id))
			writeCodexFixture(t, nested, ".codex/config.toml", codexMCPBlock(id))
			_, err = ResolveCodexOwner(nested, id, "serve", "")
			require.ErrorContains(t, err, "overlapping")
			require.NoError(t, os.Remove(filepath.Join(root, codexOwnerRelPath)))
			_, err = ResolveCodexOwner(nested, id, "serve", "")
			require.ErrorContains(t, err, filepath.Join(root, ".codex/config.toml"))
			_, err = ensureCodexOwner(nested)
			require.ErrorContains(t, err, "restore the marker")
		})
	}
}
