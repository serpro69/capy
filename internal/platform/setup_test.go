package platform

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupClaudeCode(t *testing.T) {
	dir := t.TempDir()

	// Create a fake binary path (doesn't need to be real for setup)
	binaryPath := "/usr/local/bin/capy"

	err := SetupClaudeCode(binaryPath, dir, SettingsProject)
	require.NoError(t, err)

	// Verify .capy/ directory created
	info, err := os.Stat(filepath.Join(dir, ".capy"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	// Verify portable wrapper script created
	wrapperPath := filepath.Join(dir, capyWrapperRelPath)
	wrapperData, err := os.ReadFile(wrapperPath)
	require.NoError(t, err)
	assert.Contains(t, string(wrapperData), "#!/usr/bin/env bash")
	assert.Contains(t, string(wrapperData), "\"$p\" \"$@\" || true")
	wrapperInfo, err := os.Stat(wrapperPath)
	require.NoError(t, err)
	assert.NotZero(t, wrapperInfo.Mode()&0o111, "wrapper script should be executable")

	// Verify .claude/settings.json has hooks
	settingsData, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	require.NoError(t, err)

	var settings map[string]any
	require.NoError(t, json.Unmarshal(settingsData, &settings))

	hooks, ok := settings["hooks"].(map[string]any)
	require.True(t, ok)

	// Check PreToolUse hook uses portable wrapper
	preToolUse, ok := hooks["PreToolUse"].([]any)
	require.True(t, ok)
	require.Len(t, preToolUse, 1)

	entry := preToolUse[0].(map[string]any)
	assert.Equal(t, PreToolUseMatcherPattern, entry["matcher"])

	innerHooks := entry["hooks"].([]any)
	require.Len(t, innerHooks, 1)
	hook := innerHooks[0].(map[string]any)
	assert.Equal(t, "command", hook["type"])
	assert.Equal(t, "bash $CLAUDE_PROJECT_DIR/"+capyWrapperRelPath+" hook pretooluse", hook["command"])

	// Check all hook events registered
	for _, he := range hookEvents {
		_, ok := hooks[he.Event].([]any)
		assert.True(t, ok, "hook event %s should be registered", he.Event)
	}

	// Verify .mcp.json has capy server with portable wrapper
	mcpData, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	require.NoError(t, err)

	var mcp map[string]any
	require.NoError(t, json.Unmarshal(mcpData, &mcp))

	servers, ok := mcp["mcpServers"].(map[string]any)
	require.True(t, ok)

	capyServer, ok := servers["capy"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "bash", capyServer["command"])
	assert.Equal(t, []any{capyWrapperRelPath, "serve"}, capyServer["args"])

	// Verify .capy/AGENTS.md has routing instructions
	agentsMD, err := os.ReadFile(filepath.Join(dir, agentsRelPath))
	require.NoError(t, err)
	assert.Contains(t, string(agentsMD), "capy_batch_execute")
	assert.Contains(t, string(agentsMD), "capy_search")
	assert.Contains(t, string(agentsMD), "capy_execute")
	assert.Contains(t, string(agentsMD), "capy — MANDATORY routing rules")

	// Verify root CLAUDE.md has import, not inline routing
	claudeMD, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	require.NoError(t, err)
	assert.Contains(t, string(claudeMD), "@.capy/AGENTS.md")
	assert.NotContains(t, string(claudeMD), "capy_batch_execute",
		"root CLAUDE.md should not contain inline routing instructions")

	// Verify .gitignore has .capy/** and AGENTS.md exception
	gitignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(gitignore), ".capy/**")
	assert.Contains(t, string(gitignore), "!.capy/AGENTS.md")
}

func TestSetupIdempotent(t *testing.T) {
	dir := t.TempDir()
	binaryPath := "/usr/local/bin/capy"

	// Run setup twice
	require.NoError(t, SetupClaudeCode(binaryPath, dir, SettingsProject))
	require.NoError(t, SetupClaudeCode(binaryPath, dir, SettingsProject))

	// Settings should not have duplicate hook entries
	settingsData, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	require.NoError(t, err)

	var settings map[string]any
	require.NoError(t, json.Unmarshal(settingsData, &settings))

	hooks := settings["hooks"].(map[string]any)
	preToolUse := hooks["PreToolUse"].([]any)
	assert.Len(t, preToolUse, 1, "should not duplicate PreToolUse entry")

	// MCP should not have duplicate entries
	mcpData, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	require.NoError(t, err)

	var mcp map[string]any
	require.NoError(t, json.Unmarshal(mcpData, &mcp))
	servers := mcp["mcpServers"].(map[string]any)
	assert.Len(t, servers, 1, "should not duplicate capy MCP server")

	// Root CLAUDE.md should not have duplicate imports
	claudeMD, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(claudeMD), "@.capy/AGENTS.md"),
		"should not duplicate routing import")
	assert.NotContains(t, string(claudeMD), "capy_batch_execute",
		"root CLAUDE.md should not contain inline routing instructions")

	// .capy/AGENTS.md should have routing instructions
	agentsMD, err := os.ReadFile(filepath.Join(dir, agentsRelPath))
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(agentsMD), "capy — MANDATORY routing rules"),
		"should not duplicate routing instructions in AGENTS.md")

	// .gitignore should not have duplicate entries
	gitignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	lines := 0
	for _, line := range splitLines(string(gitignore)) {
		if line == ".capy/**" {
			lines++
		}
	}
	assert.Equal(t, 1, lines, "should not duplicate .gitignore entry")
}

func TestSetupIdempotent_DifferentBinaryArg(t *testing.T) {
	dir := t.TempDir()

	// Setup with different --binary args should produce identical configs
	// since configs use the portable wrapper, not the resolved binary path.
	require.NoError(t, SetupClaudeCode("/usr/local/bin/my-custom-cli", dir, SettingsProject))
	require.NoError(t, SetupClaudeCode("/opt/tools/my-custom-cli", dir, SettingsProject))

	settingsData, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	require.NoError(t, err)

	var settings map[string]any
	require.NoError(t, json.Unmarshal(settingsData, &settings))

	hooks := settings["hooks"].(map[string]any)

	for _, he := range hookEvents {
		entries := hooks[he.Event].([]any)
		assert.Len(t, entries, 1,
			"hook event %s should have 1 entry after re-setup with different binary, got %d",
			he.Event, len(entries))

		// Verify the command uses the portable wrapper, not any hardcoded path
		entry := entries[0].(map[string]any)
		innerHooks := entry["hooks"].([]any)
		hook := innerHooks[0].(map[string]any)
		cmd := hook["command"].(string)
		assert.Contains(t, cmd, capyWrapperRelPath,
			"hook %s should use the portable wrapper script", he.Event)
		assert.NotContains(t, cmd, "/usr/local/bin/",
			"hook %s should not contain a hardcoded binary path", he.Event)
		assert.NotContains(t, cmd, "/opt/tools/",
			"hook %s should not contain a hardcoded binary path", he.Event)
	}
}

func TestMergePreservesExistingSettings(t *testing.T) {
	dir := t.TempDir()
	binaryPath := "/usr/local/bin/capy"

	// Create existing settings with other hooks and permissions
	claudeDir := filepath.Join(dir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o755))

	existing := map[string]any{
		"permissions": map[string]any{
			"allow": []any{"Bash(git:*)"},
			"deny":  []any{"Bash(rm -rf /*)"},
		},
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": "Bash",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": "/usr/local/bin/my-custom-hook",
						},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0o644))

	// Create existing .mcp.json with another server
	mcpExisting := map[string]any{
		"mcpServers": map[string]any{
			"other-server": map[string]any{
				"command": "/usr/bin/other",
				"args":    []any{"start"},
			},
		},
	}
	mcpData, _ := json.MarshalIndent(mcpExisting, "", "  ")
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".mcp.json"), mcpData, 0o644))

	// Run setup
	require.NoError(t, SetupClaudeCode(binaryPath, dir, SettingsProject))

	// Verify existing permissions preserved
	settingsData, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	require.NoError(t, err)

	var settings map[string]any
	require.NoError(t, json.Unmarshal(settingsData, &settings))

	perms := settings["permissions"].(map[string]any)
	assert.Contains(t, perms["allow"], "Bash(git:*)")
	assert.Contains(t, perms["deny"], "Bash(rm -rf /*)")

	// Verify existing custom hook preserved (capy added as separate entry)
	hooks := settings["hooks"].(map[string]any)
	preToolUse := hooks["PreToolUse"].([]any)
	assert.Len(t, preToolUse, 2, "should have both custom hook and capy hook")

	// Verify existing MCP server preserved
	mcpReadData, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	require.NoError(t, err)

	var mcp map[string]any
	require.NoError(t, json.Unmarshal(mcpReadData, &mcp))

	servers := mcp["mcpServers"].(map[string]any)
	assert.Contains(t, servers, "other-server")
	assert.Contains(t, servers, "capy")
}

func TestMergePreservesExistingCLAUDEMD(t *testing.T) {
	dir := t.TempDir()

	// Create existing CLAUDE.md with custom content
	existingContent := "# My Project\n\nSome custom instructions.\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(existingContent), 0o644))

	binaryPath := "/usr/local/bin/capy"
	require.NoError(t, SetupClaudeCode(binaryPath, dir, SettingsProject))

	claudeMD, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	require.NoError(t, err)

	content := string(claudeMD)
	assert.Contains(t, content, "# My Project")
	assert.Contains(t, content, "Some custom instructions.")
	assert.Contains(t, content, "@.capy/AGENTS.md")
	assert.NotContains(t, content, "capy_batch_execute",
		"root CLAUDE.md should not contain inline routing instructions")

	// Verify routing instructions written to separate file
	agentsMD, err := os.ReadFile(filepath.Join(dir, agentsRelPath))
	require.NoError(t, err)
	assert.Contains(t, string(agentsMD), "capy_batch_execute")
}

func TestSetupMigratesInlineRouting(t *testing.T) {
	dir := t.TempDir()
	binaryPath := "/usr/local/bin/capy"

	// Create CLAUDE.md with old inline routing block and content after it
	before := "# My Project\n\nSome custom instructions.\n\n"
	after := "\n# Extra Section\n\nMore content after capy block.\n"
	oldContent := before + GenerateRoutingInstructions() + after
	require.NoError(t, os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(oldContent), 0o644))

	require.NoError(t, SetupClaudeCode(binaryPath, dir, SettingsProject))

	// Root CLAUDE.md: inline block replaced with import, surrounding content preserved
	claudeMD, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	require.NoError(t, err)
	content := string(claudeMD)
	assert.Contains(t, content, "# My Project")
	assert.Contains(t, content, "Some custom instructions.")
	assert.Contains(t, content, "@.capy/AGENTS.md")
	assert.Contains(t, content, "# Extra Section")
	assert.Contains(t, content, "More content after capy block.")
	assert.NotContains(t, content, "capy_batch_execute",
		"inline routing should be replaced with import")

	// Routing instructions written to separate file
	agentsMD, err := os.ReadFile(filepath.Join(dir, agentsRelPath))
	require.NoError(t, err)
	assert.Contains(t, string(agentsMD), "capy_batch_execute")
	assert.Contains(t, string(agentsMD), "capy — MANDATORY routing rules")
}

func TestSetupMigratesStaleInlineRouting(t *testing.T) {
	dir := t.TempDir()
	binaryPath := "/usr/local/bin/capy"

	// Simulate an older version's inline routing block that doesn't match
	// the current GenerateRoutingInstructions() output (e.g. a tool was
	// added or wording changed between capy versions).
	staleBlock := "# capy — MANDATORY routing rules\n\nOld routing content from v0.1.\n\n## Old Section\n\nStale instructions here.\n"
	before := "# My Project\n\nCustom instructions.\n\n"
	after := "# Other Section\n\nUser content after capy block.\n"
	oldContent := before + staleBlock + after
	require.NoError(t, os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(oldContent), 0o644))

	require.NoError(t, SetupClaudeCode(binaryPath, dir, SettingsProject))

	claudeMD, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	require.NoError(t, err)
	content := string(claudeMD)

	// Stale inline block replaced with import
	assert.Contains(t, content, "@.capy/AGENTS.md")
	assert.NotContains(t, content, "Old routing content from v0.1",
		"stale inline routing should be removed")
	assert.NotContains(t, content, "Stale instructions here",
		"stale sub-sections should be removed")

	// Surrounding content preserved
	assert.Contains(t, content, "# My Project")
	assert.Contains(t, content, "Custom instructions.")
	assert.Contains(t, content, "# Other Section")
	assert.Contains(t, content, "User content after capy block.")

	// Current routing written to separate file
	agentsMD, err := os.ReadFile(filepath.Join(dir, agentsRelPath))
	require.NoError(t, err)
	assert.Contains(t, string(agentsMD), "capy_batch_execute")
}

func TestSetupMigratesOldImportPath(t *testing.T) {
	dir := t.TempDir()
	binaryPath := "/usr/local/bin/capy"

	// Create CLAUDE.md with old import path (@.claude/capy/CLAUDE.md)
	oldContent := "# My Project\n\n# capy — MANDATORY routing rules\n\n@.claude/capy/CLAUDE.md\n\n# Footer\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(oldContent), 0o644))

	// Create old .claude/capy/CLAUDE.md (should be cleaned up)
	oldCapyDir := filepath.Join(dir, ".claude", "capy")
	require.NoError(t, os.MkdirAll(oldCapyDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(oldCapyDir, "CLAUDE.md"), []byte("old"), 0o644))

	require.NoError(t, SetupClaudeCode(binaryPath, dir, SettingsProject))

	claudeMD, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	require.NoError(t, err)
	content := string(claudeMD)
	assert.Contains(t, content, "@.capy/AGENTS.md")
	assert.NotContains(t, content, "@.claude/capy/CLAUDE.md")
	assert.Contains(t, content, "# Footer")

	// Old file should be cleaned up
	_, err = os.Stat(filepath.Join(oldCapyDir, "CLAUDE.md"))
	assert.True(t, os.IsNotExist(err), "old .claude/capy/CLAUDE.md should be removed")

	// New file should exist
	agentsMD, err := os.ReadFile(filepath.Join(dir, agentsRelPath))
	require.NoError(t, err)
	assert.Contains(t, string(agentsMD), "capy_batch_execute")
}

func TestSetupMigratesInlineRouting_Idempotent(t *testing.T) {
	dir := t.TempDir()
	binaryPath := "/usr/local/bin/capy"

	// Create CLAUDE.md with old inline routing block
	oldContent := "# My Project\n\n" + GenerateRoutingInstructions() + "\n# Footer\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(oldContent), 0o644))

	// Run setup twice — first migrates, second should be a no-op
	require.NoError(t, SetupClaudeCode(binaryPath, dir, SettingsProject))
	require.NoError(t, SetupClaudeCode(binaryPath, dir, SettingsProject))

	claudeMD, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	require.NoError(t, err)
	content := string(claudeMD)
	assert.Equal(t, 1, strings.Count(content, "@.capy/AGENTS.md"),
		"should have exactly one import after migration + re-run")
	assert.Contains(t, content, "# Footer")
}

func TestSetupCodex(t *testing.T) {
	dir := t.TempDir()
	binaryPath := "/usr/local/bin/capy"

	err := SetupCodex(binaryPath, dir)
	require.NoError(t, err)

	// Verify .capy/ directory created
	info, err := os.Stat(filepath.Join(dir, ".capy"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	// Verify wrapper script created at .codex/scripts/capy.sh
	wrapperPath := filepath.Join(dir, codexWrapperRelPath)
	wrapperData, err := os.ReadFile(wrapperPath)
	require.NoError(t, err)
	assert.Contains(t, string(wrapperData), "#!/usr/bin/env bash")
	assert.Contains(t, string(wrapperData), "\"$p\" \"$@\" || true")

	wrapperInfo, err := os.Stat(wrapperPath)
	require.NoError(t, err)
	assert.NotZero(t, wrapperInfo.Mode()&0o111, "wrapper script should be executable")

	// Verify .capy/AGENTS.md has routing instructions
	agentsMD, err := os.ReadFile(filepath.Join(dir, agentsRelPath))
	require.NoError(t, err)
	assert.Contains(t, string(agentsMD), "capy — MANDATORY routing rules")
	assert.Contains(t, string(agentsMD), "capy_batch_execute")

	// Verify .codex/config.toml has MCP server
	configToml, err := os.ReadFile(filepath.Join(dir, ".codex", "config.toml"))
	require.NoError(t, err)
	assert.Contains(t, string(configToml), "[mcp_servers.capy]")
	assert.Contains(t, string(configToml), codexWrapperRelPath)

	// Verify .gitignore has .capy/** and AGENTS.md exception
	gitignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(gitignore), ".capy/**")
	assert.Contains(t, string(gitignore), "!.capy/AGENTS.md")
}

func TestSetupCodex_CreatesCodexDir(t *testing.T) {
	dir := t.TempDir()
	binaryPath := "/usr/local/bin/capy"

	// .codex doesn't exist yet — setup should create it
	err := SetupCodex(binaryPath, dir)
	require.NoError(t, err)

	info, err := os.Stat(filepath.Join(dir, ".codex"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestSetupCodex_Idempotent(t *testing.T) {
	dir := t.TempDir()
	binaryPath := "/usr/local/bin/capy"

	require.NoError(t, SetupCodex(binaryPath, dir))
	require.NoError(t, SetupCodex(binaryPath, dir))

	// Wrapper should still be valid
	wrapperData, err := os.ReadFile(filepath.Join(dir, codexWrapperRelPath))
	require.NoError(t, err)
	assert.Contains(t, string(wrapperData), "#!/usr/bin/env bash")

	// .gitignore should not have duplicate entries
	gitignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	lines := 0
	for _, line := range splitLines(string(gitignore)) {
		if line == ".capy/**" {
			lines++
		}
	}
	assert.Equal(t, 1, lines, "should not duplicate .gitignore entry")

	// MCP config should not be duplicated
	configToml, err := os.ReadFile(filepath.Join(dir, ".codex", "config.toml"))
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(configToml), "[mcp_servers.capy]"),
		"should not duplicate MCP server entry")
}

func TestMergeCodexMCPServer_NewFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")

	require.NoError(t, mergeCodexMCPServer(configPath))

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "[mcp_servers.capy]")
	assert.Contains(t, string(data), codexWrapperRelPath)
}

func TestMergeCodexMCPServer_PreservesExisting(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")

	existing := "#:schema https://developers.openai.com/codex/config-schema.json\nmodel = \"gpt-5.5\"\n\n[features]\ncodex_hooks = true\n"
	require.NoError(t, os.WriteFile(configPath, []byte(existing), 0o644))

	require.NoError(t, mergeCodexMCPServer(configPath))

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "#:schema")
	assert.Contains(t, content, `model = "gpt-5.5"`)
	assert.Contains(t, content, "[features]")
	assert.Contains(t, content, "[mcp_servers.capy]")
}

func TestMergeCodexMCPServer_Idempotent(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")

	existing := "model = \"gpt-5.5\"\n\n[mcp_servers.capy]\ncommand = \"bash\"\nargs = [\".codex/scripts/capy.sh\", \"serve\"]\n"
	require.NoError(t, os.WriteFile(configPath, []byte(existing), 0o644))

	require.NoError(t, mergeCodexMCPServer(configPath))

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Equal(t, existing, string(data), "should not modify file when MCP server already exists")
}

func TestSetupCodex_WrapperMatchesClaudeCode(t *testing.T) {
	dir := t.TempDir()
	binaryPath := "/usr/local/bin/capy"

	// Setup both platforms
	require.NoError(t, SetupCodex(binaryPath, dir))
	require.NoError(t, SetupClaudeCode(binaryPath, dir, SettingsProject))

	// Both wrapper scripts should have identical content
	codexWrapper, err := os.ReadFile(filepath.Join(dir, codexWrapperRelPath))
	require.NoError(t, err)
	claudeWrapper, err := os.ReadFile(filepath.Join(dir, capyWrapperRelPath))
	require.NoError(t, err)

	assert.Equal(t, string(claudeWrapper), string(codexWrapper),
		"Codex and Claude Code wrapper scripts should be identical")
}

func TestWriteCapyWrapper(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".claude"), 0o755))

	require.NoError(t, writeCapyWrapper(dir))

	wrapperPath := filepath.Join(dir, capyWrapperRelPath)
	data, err := os.ReadFile(wrapperPath)
	require.NoError(t, err)

	content := string(data)
	assert.Contains(t, content, "#!/usr/bin/env bash")
	assert.Contains(t, content, `"$p" "$@"`)
	assert.Contains(t, content, `"$HOME/.local/bin/capy"`)
	assert.Contains(t, content, `"/opt/homebrew/bin/capy"`)
	assert.Contains(t, content, `"/usr/local/bin/capy"`)
	assert.Contains(t, content, `"$HOME/go/bin/capy"`)
	assert.Contains(t, content, "command -v capy", "should use POSIX command -v, not which")
	assert.Contains(t, content, "capy not found")
	assert.Contains(t, content, "jq -n --arg reason \"capy binary not found\"")
	assert.Contains(t, content, "{hookSpecificOutput: {hookEventName: \"PreToolUse\", permissionDecision: \"deny\", permissionDecisionReason: $reason}}")

	info, err := os.Stat(wrapperPath)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&0o111, "wrapper should be executable")
}

func TestWriteCapyWrapper_Idempotent(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".claude"), 0o755))

	require.NoError(t, writeCapyWrapper(dir))
	first, err := os.ReadFile(filepath.Join(dir, capyWrapperRelPath))
	require.NoError(t, err)

	require.NoError(t, writeCapyWrapper(dir))
	second, err := os.ReadFile(filepath.Join(dir, capyWrapperRelPath))
	require.NoError(t, err)

	assert.Equal(t, string(first), string(second), "wrapper content should be identical after re-run")
}

func TestSetupMigratesOldHardcodedHooks(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o755))

	// Simulate old-format hooks with hardcoded binary path
	oldHooks := make(map[string]any)
	for _, he := range hookEvents {
		oldHooks[he.Event] = []any{
			map[string]any{
				"matcher": he.Matcher,
				"hooks": []any{
					map[string]any{
						"type":    "command",
						"command": "/opt/homebrew/bin/capy hook " + he.CLIArg,
					},
				},
			},
		}
	}
	oldSettings := map[string]any{"hooks": oldHooks}
	data, _ := json.MarshalIndent(oldSettings, "", "  ")
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0o644))

	// Simulate old-format MCP config
	oldMCP := map[string]any{
		"mcpServers": map[string]any{
			"capy": map[string]any{
				"command": "/opt/homebrew/bin/capy",
				"args":    []any{"serve"},
			},
		},
	}
	mcpData, _ := json.MarshalIndent(oldMCP, "", "  ")
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".mcp.json"), mcpData, 0o644))

	// Run setup — should migrate to wrapper format
	require.NoError(t, SetupClaudeCode("/usr/local/bin/capy", dir, SettingsProject))

	// Verify hooks migrated to wrapper format
	settingsData, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	require.NoError(t, err)
	var settings map[string]any
	require.NoError(t, json.Unmarshal(settingsData, &settings))

	hooks := settings["hooks"].(map[string]any)
	for _, he := range hookEvents {
		entries := hooks[he.Event].([]any)
		assert.Len(t, entries, 1,
			"hook event %s should have exactly 1 entry after migration", he.Event)

		entry := entries[0].(map[string]any)
		innerHooks := entry["hooks"].([]any)
		hook := innerHooks[0].(map[string]any)
		cmd := hook["command"].(string)
		assert.Contains(t, cmd, capyWrapperRelPath,
			"hook %s should use wrapper script after migration", he.Event)
		assert.NotContains(t, cmd, "/opt/homebrew/bin/capy",
			"hook %s should not contain old hardcoded path", he.Event)
	}

	// Verify MCP migrated to wrapper format
	mcpResult, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	require.NoError(t, err)
	var mcp map[string]any
	require.NoError(t, json.Unmarshal(mcpResult, &mcp))

	servers := mcp["mcpServers"].(map[string]any)
	capyServer := servers["capy"].(map[string]any)
	assert.Equal(t, "bash", capyServer["command"],
		"MCP command should be 'bash' after migration")
	assert.Equal(t, []any{capyWrapperRelPath, "serve"}, capyServer["args"],
		"MCP args should use wrapper after migration")
}

func TestEnsureGitignoreCreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")

	require.NoError(t, ensureGitignoreEntry(path, ".capy/"))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, ".capy/\n", string(data))
}

func TestEnsureGitignoreAppendsToExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")

	require.NoError(t, os.WriteFile(path, []byte("node_modules/\n"), 0o644))
	require.NoError(t, ensureGitignoreEntry(path, ".capy/"))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "node_modules/\n.capy/\n", string(data))
}

func TestEnsureGitignoreHandlesMissingNewline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")

	require.NoError(t, os.WriteFile(path, []byte("node_modules/"), 0o644))
	require.NoError(t, ensureGitignoreEntry(path, ".capy/"))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "node_modules/\n.capy/\n", string(data))
}

func TestSetupUsesExecutableFallback(t *testing.T) {
	dir := t.TempDir()

	// When binary path is empty, setup falls back to os.Executable() for
	// validation but configs always use the portable wrapper.
	err := SetupClaudeCode("", dir, SettingsProject)
	require.NoError(t, err)

	// Verify the MCP config uses bash + wrapper (not the fallback executable)
	mcpData, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	require.NoError(t, err)

	var mcp map[string]any
	require.NoError(t, json.Unmarshal(mcpData, &mcp))

	servers := mcp["mcpServers"].(map[string]any)
	capyServer := servers["capy"].(map[string]any)
	assert.Equal(t, "bash", capyServer["command"])
	assert.Equal(t, []any{capyWrapperRelPath, "serve"}, capyServer["args"])
}

func TestSetupLocalTarget(t *testing.T) {
	dir := t.TempDir()
	binaryPath := "/usr/local/bin/capy"

	err := SetupClaudeCode(binaryPath, dir, SettingsLocal)
	require.NoError(t, err)

	// Hooks should be in settings.local.json
	localData, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.local.json"))
	require.NoError(t, err)

	var localSettings map[string]any
	require.NoError(t, json.Unmarshal(localData, &localSettings))

	hooks, ok := localSettings["hooks"].(map[string]any)
	require.True(t, ok)
	for _, he := range hookEvents {
		_, ok := hooks[he.Event].([]any)
		assert.True(t, ok, "hook event %s should be registered in settings.local.json", he.Event)
	}

	// settings.json should NOT exist (no hooks written there)
	_, err = os.Stat(filepath.Join(dir, ".claude", "settings.json"))
	assert.True(t, os.IsNotExist(err), "settings.json should not be created when targeting local")
}

func TestSetupLocalIdempotent(t *testing.T) {
	dir := t.TempDir()
	binaryPath := "/usr/local/bin/capy"

	require.NoError(t, SetupClaudeCode(binaryPath, dir, SettingsLocal))
	require.NoError(t, SetupClaudeCode(binaryPath, dir, SettingsLocal))

	localData, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.local.json"))
	require.NoError(t, err)

	var localSettings map[string]any
	require.NoError(t, json.Unmarshal(localData, &localSettings))

	hooks := localSettings["hooks"].(map[string]any)
	preToolUse := hooks["PreToolUse"].([]any)
	assert.Len(t, preToolUse, 1, "should not duplicate PreToolUse entry in local settings")
}

func TestSetupMigratesProjectToLocal(t *testing.T) {
	dir := t.TempDir()
	binaryPath := "/usr/local/bin/capy"

	// First setup writes to project (settings.json)
	require.NoError(t, SetupClaudeCode(binaryPath, dir, SettingsProject))

	// Verify hooks in settings.json
	settingsData, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	require.NoError(t, err)
	var settings map[string]any
	require.NoError(t, json.Unmarshal(settingsData, &settings))
	require.NotNil(t, settings["hooks"])

	// Now setup targeting local — should migrate
	require.NoError(t, SetupClaudeCode(binaryPath, dir, SettingsLocal))

	// settings.json should have no capy hooks
	settingsData, err = os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	require.NoError(t, err)
	var settingsAfter map[string]any
	require.NoError(t, json.Unmarshal(settingsData, &settingsAfter))
	_, hasHooks := settingsAfter["hooks"]
	assert.False(t, hasHooks, "settings.json should have no hooks after migration to local")

	// settings.local.json should have the hooks
	localData, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.local.json"))
	require.NoError(t, err)
	var localSettings map[string]any
	require.NoError(t, json.Unmarshal(localData, &localSettings))
	hooks := localSettings["hooks"].(map[string]any)
	for _, he := range hookEvents {
		_, ok := hooks[he.Event].([]any)
		assert.True(t, ok, "hook event %s should be in settings.local.json after migration", he.Event)
	}
}

func TestSetupMigratesLocalToProject(t *testing.T) {
	dir := t.TempDir()
	binaryPath := "/usr/local/bin/capy"

	// First setup writes to local
	require.NoError(t, SetupClaudeCode(binaryPath, dir, SettingsLocal))

	// Now setup targeting project — should migrate
	require.NoError(t, SetupClaudeCode(binaryPath, dir, SettingsProject))

	// settings.local.json should have no capy hooks
	localData, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.local.json"))
	require.NoError(t, err)
	var localSettings map[string]any
	require.NoError(t, json.Unmarshal(localData, &localSettings))
	_, hasHooks := localSettings["hooks"]
	assert.False(t, hasHooks, "settings.local.json should have no hooks after migration to project")

	// settings.json should have the hooks
	settingsData, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	require.NoError(t, err)
	var settings map[string]any
	require.NoError(t, json.Unmarshal(settingsData, &settings))
	hooks := settings["hooks"].(map[string]any)
	for _, he := range hookEvents {
		_, ok := hooks[he.Event].([]any)
		assert.True(t, ok, "hook event %s should be in settings.json after migration", he.Event)
	}
}

func TestSetupMigrationPreservesOtherSettings(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o755))

	// Create settings.json with capy hooks AND custom hooks + permissions
	binaryPath := "/usr/local/bin/capy"
	require.NoError(t, SetupClaudeCode(binaryPath, dir, SettingsProject))

	// Add custom content to settings.json
	settingsPath := filepath.Join(claudeDir, "settings.json")
	settingsData, err := os.ReadFile(settingsPath)
	require.NoError(t, err)
	var settings map[string]any
	require.NoError(t, json.Unmarshal(settingsData, &settings))
	settings["permissions"] = map[string]any{
		"allow": []any{"Bash(git:*)"},
	}
	// Add a custom hook alongside capy's
	hooks := settings["hooks"].(map[string]any)
	preToolUse := hooks["PreToolUse"].([]any)
	preToolUse = append(preToolUse, map[string]any{
		"matcher": "Bash",
		"hooks": []any{
			map[string]any{"type": "command", "command": "/usr/local/bin/my-hook"},
		},
	})
	hooks["PreToolUse"] = preToolUse
	data, _ := json.MarshalIndent(settings, "", "  ")
	require.NoError(t, os.WriteFile(settingsPath, data, 0o644))

	// Migrate to local
	require.NoError(t, SetupClaudeCode(binaryPath, dir, SettingsLocal))

	// settings.json should still have permissions and custom hook
	settingsData, err = os.ReadFile(settingsPath)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(settingsData, &settings))

	perms := settings["permissions"].(map[string]any)
	assert.Contains(t, perms["allow"], "Bash(git:*)")

	hooks = settings["hooks"].(map[string]any)
	preToolUse = hooks["PreToolUse"].([]any)
	assert.Len(t, preToolUse, 1, "should have only the custom hook remaining")
	entry := preToolUse[0].(map[string]any)
	innerHooks := entry["hooks"].([]any)
	hook := innerHooks[0].(map[string]any)
	assert.Equal(t, "/usr/local/bin/my-hook", hook["command"])
}

func TestRemoveCapyHooks(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	// Create a settings file with capy hooks
	settings := map[string]any{
		"hooks": map[string]any{},
	}
	hooks := settings["hooks"].(map[string]any)
	for _, he := range hookEvents {
		hooks[he.Event] = []any{
			map[string]any{
				"matcher": he.Matcher,
				"hooks": []any{
					map[string]any{
						"type":    "command",
						"command": "bash $CLAUDE_PROJECT_DIR/" + capyWrapperRelPath + " hook " + he.CLIArg,
					},
				},
			},
		}
	}
	data, _ := json.MarshalIndent(settings, "", "  ")
	require.NoError(t, os.WriteFile(settingsPath, data, 0o644))

	removed, err := removeCapyHooks(settingsPath)
	require.NoError(t, err)
	assert.True(t, removed)

	// Verify all hooks removed
	readData, err := os.ReadFile(settingsPath)
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, json.Unmarshal(readData, &result))
	_, hasHooks := result["hooks"]
	assert.False(t, hasHooks, "hooks key should be removed when empty")
}

func TestRemoveCapyHooks_NoHooks(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	// File with no hooks
	data, _ := json.MarshalIndent(map[string]any{"permissions": map[string]any{}}, "", "  ")
	require.NoError(t, os.WriteFile(settingsPath, data, 0o644))

	removed, err := removeCapyHooks(settingsPath)
	require.NoError(t, err)
	assert.False(t, removed)
}

func TestRemoveCapyHooks_FileNotExist(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "nonexistent.json")

	removed, err := removeCapyHooks(settingsPath)
	require.NoError(t, err)
	assert.False(t, removed)
}

func TestRemoveCapyHooks_PreservesOtherHooks(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	settings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{
				// Custom hook
				map[string]any{
					"matcher": "Bash",
					"hooks": []any{
						map[string]any{"type": "command", "command": "/usr/local/bin/my-hook"},
					},
				},
				// Capy hook
				map[string]any{
					"matcher": PreToolUseMatcherPattern,
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": "bash $CLAUDE_PROJECT_DIR/" + capyWrapperRelPath + " hook pretooluse",
						},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(settings, "", "  ")
	require.NoError(t, os.WriteFile(settingsPath, data, 0o644))

	removed, err := removeCapyHooks(settingsPath)
	require.NoError(t, err)
	assert.True(t, removed)

	readData, err := os.ReadFile(settingsPath)
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, json.Unmarshal(readData, &result))

	hooks := result["hooks"].(map[string]any)
	preToolUse := hooks["PreToolUse"].([]any)
	assert.Len(t, preToolUse, 1, "should have only the custom hook")
	entry := preToolUse[0].(map[string]any)
	innerHooks := entry["hooks"].([]any)
	hook := innerHooks[0].(map[string]any)
	assert.Equal(t, "/usr/local/bin/my-hook", hook["command"])
}

func TestSettingsTargetFilename(t *testing.T) {
	assert.Equal(t, "settings.json", SettingsProject.SettingsFilename())
	assert.Equal(t, "settings.local.json", SettingsLocal.SettingsFilename())
}

func TestPreCommitHookScriptContainsEncryptionCheck(t *testing.T) {
	script := preCommitHookScript(`\.capy/knowledge\.db$`)

	assert.Contains(t, script, `head -c 15 "$f"`,
		"should check first 15 bytes of staged DB files")
	assert.Contains(t, script, `SQLite format 3`,
		"should detect unencrypted SQLite magic string")
	assert.Contains(t, script, `refusing to commit unencrypted`,
		"should print rejection message")
	assert.Contains(t, script, `capy encrypt`,
		"should point user to capy encrypt")
	assert.Contains(t, script, `if [ $? -ne 0 ]; then exit 1; fi`,
		"should propagate subshell exit code")
}

// initTestGitRepo creates a temp git repo with an initial commit and a
// pre-commit hook installed with a known DB pattern. Uses preCommitHookScript
// directly instead of installPreCommitHook, which resolves the DB path via
// config and would produce a pattern pointing outside the temp repo.
func initTestGitRepo(t *testing.T) (string, func(args ...string)) {
	t.Helper()
	dir := t.TempDir()

	runGit := func(args ...string) {
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

	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".capy"), 0o755))

	hookDir := filepath.Join(dir, ".git", "hooks")
	script := preCommitHookScript(`\.capy/knowledge\.db$`)
	require.NoError(t, os.WriteFile(filepath.Join(hookDir, "pre-commit"), []byte(script), 0o755))

	return dir, runGit
}

func TestPreCommitHookRejectsUnencryptedDB(t *testing.T) {
	dir, runGit := initTestGitRepo(t)

	// Create an unencrypted SQLite DB (starts with "SQLite format 3\000")
	sqliteMagic := append([]byte("SQLite format 3"), 0x00)
	dbContent := make([]byte, 4096)
	copy(dbContent, sqliteMagic)
	dbPath := filepath.Join(dir, ".capy", "knowledge.db")
	require.NoError(t, os.WriteFile(dbPath, dbContent, 0o644))

	runGit("add", "-f", dbPath)

	cmd := exec.Command("git", "commit", "-m", "should fail")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	assert.Error(t, err, "commit should fail for unencrypted DB")
	assert.Contains(t, string(output), "refusing to commit unencrypted",
		"hook should print rejection message")
}

func TestPreCommitHookAllowsEncryptedDB(t *testing.T) {
	dir, runGit := initTestGitRepo(t)

	// Create an "encrypted" DB (deterministic non-SQLite bytes)
	dbContent := make([]byte, 4096)
	for i := range dbContent {
		dbContent[i] = byte(i % 251)
	}
	dbPath := filepath.Join(dir, ".capy", "knowledge.db")
	require.NoError(t, os.WriteFile(dbPath, dbContent, 0o644))

	runGit("add", "-f", dbPath)

	cmd := exec.Command("git", "commit", "-m", "encrypted db ok")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	assert.NoError(t, err, "commit should succeed for encrypted DB: %s", output)
}

func TestPreCommitHookAllowsNoDBStaged(t *testing.T) {
	dir, runGit := initTestGitRepo(t)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "other.txt"), []byte("hello"), 0o644))
	runGit("add", "other.txt")

	cmd := exec.Command("git", "commit", "-m", "no db staged")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	assert.NoError(t, err, "commit should succeed when no DB is staged: %s", output)
}

// splitLines splits a string into lines, similar to strings.Split but handles edge cases.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := make([]string, 0)
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
