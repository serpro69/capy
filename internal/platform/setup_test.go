package platform

import (
	"encoding/json"
	"os"
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

	err := SetupClaudeCode(binaryPath, dir)
	require.NoError(t, err)

	// Verify .capy/ directory created
	info, err := os.Stat(filepath.Join(dir, ".capy"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	// Verify .claude/settings.json has hooks
	settingsData, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	require.NoError(t, err)

	var settings map[string]any
	require.NoError(t, json.Unmarshal(settingsData, &settings))

	hooks, ok := settings["hooks"].(map[string]any)
	require.True(t, ok)

	// Check PreToolUse hook
	preToolUse, ok := hooks["PreToolUse"].([]any)
	require.True(t, ok)
	require.Len(t, preToolUse, 1)

	entry := preToolUse[0].(map[string]any)
	assert.Equal(t, PreToolUseMatcherPattern, entry["matcher"])

	innerHooks := entry["hooks"].([]any)
	require.Len(t, innerHooks, 1)
	hook := innerHooks[0].(map[string]any)
	assert.Equal(t, "command", hook["type"])
	assert.Equal(t, binaryPath+" hook pretooluse", hook["command"])

	// Check all 5 hook events registered
	for _, he := range hookEvents {
		_, ok := hooks[he.Event].([]any)
		assert.True(t, ok, "hook event %s should be registered", he.Event)
	}

	// Verify .mcp.json has capy server
	mcpData, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	require.NoError(t, err)

	var mcp map[string]any
	require.NoError(t, json.Unmarshal(mcpData, &mcp))

	servers, ok := mcp["mcpServers"].(map[string]any)
	require.True(t, ok)

	capyServer, ok := servers["capy"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, binaryPath, capyServer["command"])
	assert.Equal(t, []any{"serve"}, capyServer["args"])

	// Verify CLAUDE.md has routing instructions
	claudeMD, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	require.NoError(t, err)
	assert.Contains(t, string(claudeMD), "capy_batch_execute")
	assert.Contains(t, string(claudeMD), "capy_search")
	assert.Contains(t, string(claudeMD), "capy_execute")
	assert.Contains(t, string(claudeMD), "capy — MANDATORY routing rules")

	// Verify .gitignore has .capy/
	gitignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(gitignore), ".capy/")
}

func TestSetupIdempotent(t *testing.T) {
	dir := t.TempDir()
	binaryPath := "/usr/local/bin/capy"

	// Run setup twice
	require.NoError(t, SetupClaudeCode(binaryPath, dir))
	require.NoError(t, SetupClaudeCode(binaryPath, dir))

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

	// CLAUDE.md should not have duplicate routing instructions
	claudeMD, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(claudeMD), "capy — MANDATORY routing rules"),
		"should not duplicate routing instructions")

	// .gitignore should not have duplicate entries
	gitignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	lines := 0
	for _, line := range splitLines(string(gitignore)) {
		if line == ".capy/" {
			lines++
		}
	}
	assert.Equal(t, 1, lines, "should not duplicate .gitignore entry")
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
	require.NoError(t, SetupClaudeCode(binaryPath, dir))

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
	require.NoError(t, SetupClaudeCode(binaryPath, dir))

	claudeMD, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	require.NoError(t, err)

	content := string(claudeMD)
	assert.Contains(t, content, "# My Project")
	assert.Contains(t, content, "Some custom instructions.")
	assert.Contains(t, content, "capy_batch_execute")
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

	// When binary path is empty, setup falls back to os.Executable()
	// This should succeed (we're running as a Go test binary)
	err := SetupClaudeCode("", dir)
	require.NoError(t, err)

	// Verify the MCP config uses the fallback executable path
	mcpData, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	require.NoError(t, err)

	var mcp map[string]any
	require.NoError(t, json.Unmarshal(mcpData, &mcp))

	servers := mcp["mcpServers"].(map[string]any)
	capyServer := servers["capy"].(map[string]any)
	command := capyServer["command"].(string)
	assert.NotEmpty(t, command, "should resolve to executable path")
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
