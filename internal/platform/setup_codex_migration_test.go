package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const legacyCodexMCPFixture = "[mcp_servers.capy]\ncommand = 'bash'\nargs = ['.codex/scripts/capy.sh', 'serve']\n"

func TestSetupCodexMigratesLegacyMCP(t *testing.T) {
	for _, tc := range []struct {
		name, block string
		marker      bool
	}{
		{name: "original wrapper", block: legacyCodexMCPFixture},
		{name: "knowledge key", block: legacyCodexMCPFixture + "env_vars = ['CAPY_DB_KEY']\n"},
		{name: "both keys and quoted table", block: strings.ReplaceAll(legacyCodexMCPFixture, "[mcp_servers.capy]", `[mcp_servers."capy"]`) + "env_vars = ['CAPY_DB_KEY', 'CAPY_VAULT_KEY']\n"},
		{name: "existing owner", block: legacyCodexMCPFixture, marker: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			prefix := "# keep this comment\nmodel = 'fixture'\n[tui]\nanimations = false\n\n"
			suffix := "\n[mcp_servers.other]\ncommand = 'other-tool'\n" + codexHookStateFixture
			writeCodexFixture(t, dir, ".codex/config.toml", prefix+tc.block+suffix)
			writeCodexFixture(t, dir, codexHooksRelPath, `{"description":"external metadata","hooks":{"SessionStart":[{"matcher":"startup|resume","hooks":[{"type":"command","command":"echo external"}]}]}}`)
			if tc.marker {
				writeCodexFixture(t, dir, codexOwnerRelPath, codexOwnerContent(testCodexOwnerID))
			}
			// Runtime and doctor remain strict until setup replaces the old entry.
			_, err := InspectCodexOwners(dir)
			require.ErrorContains(t, err, "unsupported")
			require.NoError(t, setupCodex(dir))
			owners, err := InspectCodexOwners(dir)
			require.NoError(t, err)
			require.Len(t, owners, 1)
			if tc.marker {
				require.Equal(t, testCodexOwnerID, owners[0].ID)
			}
			data, err := os.ReadFile(filepath.Join(dir, ".codex/config.toml"))
			require.NoError(t, err)
			require.Equal(t, prefix+codexMCPBlock(owners[0].ID)+suffix, string(data))
			hooks := mustReadJSONMap(t, filepath.Join(dir, codexHooksRelPath))
			require.Equal(t, "external metadata", hooks["description"])
			groups := hooks["hooks"].(map[string]any)["SessionStart"].([]any)
			require.Len(t, groups, 2)
			require.Equal(t, "echo external", groups[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"])
			checks := registrationChecks(t, dir)
			require.Equal(t, Pass, checks["Codex hooks"].Status, checks)
			require.Equal(t, Pass, checks["Codex MCP"].Status, checks)
			require.NoError(t, setupCodex(dir))
			after, err := os.ReadFile(filepath.Join(dir, ".codex/config.toml"))
			require.NoError(t, err)
			require.Equal(t, data, after)
		})
	}
}

func TestSetupCodexLegacyMigrationRejectsBrokenBindings(t *testing.T) {
	for _, tc := range []struct {
		name, config, hooks, marker, want string
	}{
		{name: "aliased legacy entry", config: strings.ReplaceAll(legacyCodexMCPFixture, "mcp_servers.capy", "mcp_servers.copy"), want: "unsupported"},
		{name: "extra argument", config: strings.ReplaceAll(legacyCodexMCPFixture, "'serve']", "'serve', '--project-dir', '/other']"), want: "unsupported"},
		{name: "different wrapper path", config: strings.ReplaceAll(legacyCodexMCPFixture, "'.codex/", "'/other/.codex/"), want: "unsupported"},
		{name: "invalid owner", config: legacyCodexMCPFixture, marker: "broken\n", want: "invalid owner"},
		{name: "missing live hook owner", config: legacyCodexMCPFixture, hooks: codexHookFixture(t, testCodexOwnerID), want: "restore the marker"},
		{name: "missing live MCP owner", config: legacyCodexMCPFixture + strings.ReplaceAll(codexMCPBlock(testCodexOwnerID), "mcp_servers.capy", "mcp_servers.copy"), want: "restore the marker"},
		{name: "mismatched live hook owner", config: legacyCodexMCPFixture, hooks: codexHookFixture(t, strings.Repeat("b", 32)), marker: codexOwnerContent(testCodexOwnerID), want: "disagrees"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeCodexFixture(t, dir, ".codex/config.toml", tc.config)
			if tc.hooks != "" {
				writeCodexFixture(t, dir, codexHooksRelPath, tc.hooks)
			}
			if tc.marker != "" {
				writeCodexFixture(t, dir, codexOwnerRelPath, tc.marker)
			}
			require.ErrorContains(t, setupCodex(dir), tc.want)
			data, err := os.ReadFile(filepath.Join(dir, ".codex/config.toml"))
			require.NoError(t, err)
			require.Equal(t, tc.config, string(data))
			if tc.marker == "" {
				require.NoFileExists(t, filepath.Join(dir, codexOwnerRelPath))
			}
			require.NoDirExists(t, filepath.Join(dir, ".capy"))
		})
	}
}

func TestSetupCodexLegacyMigrationIsProjectLocal(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "child")
	writeCodexFixture(t, root, ".codex/config.toml", legacyCodexMCPFixture)
	writeCodexFixture(t, nested, ".codex/config.toml", legacyCodexMCPFixture)
	// A child setup must neither ignore nor rewrite a legacy ancestor binding.
	err := setupCodex(nested)
	require.ErrorContains(t, err, filepath.Join(root, ".codex/config.toml"))
	require.NoFileExists(t, filepath.Join(nested, codexOwnerRelPath))
	require.NoError(t, setupCodex(root))
	require.NoError(t, setupCodex(nested))
	owners, err := InspectCodexOwners(nested)
	require.NoError(t, err)
	require.Len(t, owners, 2)
	require.NotEqual(t, owners[0].ID, owners[1].ID)
}
