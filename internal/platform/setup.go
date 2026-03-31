package platform

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SetupClaudeCode configures capy for a Claude Code project.
// It merges hook and MCP configurations idempotently, creates the .capy/
// directory, appends routing instructions to CLAUDE.md, and adds .capy/
// to .gitignore.
func SetupClaudeCode(binaryPath, projectDir string) error {
	// 1. Resolve binary path
	if binaryPath == "" {
		var err error
		binaryPath, err = exec.LookPath("capy")
		if err != nil {
			// Fallback: use the current executable (handles `go run` and installed binary)
			binaryPath, err = os.Executable()
			if err != nil {
				return fmt.Errorf("capy binary not found in PATH; use --binary to specify location")
			}
		}
	}

	// 2. Create .capy/ directory
	capyDir := filepath.Join(projectDir, ".capy")
	if err := os.MkdirAll(capyDir, 0o755); err != nil {
		return fmt.Errorf("creating .capy directory: %w", err)
	}

	// 3. Create .claude/ directory (needed for settings.json)
	claudeDir := filepath.Join(projectDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		return fmt.Errorf("creating .claude directory: %w", err)
	}

	// 4. Merge hooks into .claude/settings.json
	settingsPath := filepath.Join(claudeDir, "settings.json")
	if err := mergeHooks(settingsPath, binaryPath); err != nil {
		return fmt.Errorf("merging hooks: %w", err)
	}

	// 5. Merge MCP server into .mcp.json
	mcpPath := filepath.Join(projectDir, ".mcp.json")
	if err := mergeMCPServer(mcpPath, binaryPath); err != nil {
		return fmt.Errorf("merging MCP config: %w", err)
	}

	// 6. Append routing instructions to CLAUDE.md
	claudeMDPath := filepath.Join(projectDir, "CLAUDE.md")
	if err := appendRoutingInstructions(claudeMDPath); err != nil {
		return fmt.Errorf("updating CLAUDE.md: %w", err)
	}

	// 7. Add .capy/ to .gitignore
	gitignorePath := filepath.Join(projectDir, ".gitignore")
	if err := ensureGitignoreEntry(gitignorePath, ".capy/"); err != nil {
		return fmt.Errorf("updating .gitignore: %w", err)
	}

	return nil
}

// PreToolUseMatcherPattern is the pipe-separated matcher for PreToolUse hooks.
const PreToolUseMatcherPattern = "Bash|WebFetch|Read|Grep|Agent|Task|mcp__*capy*"

// hookEvents maps hook event names to their matcher patterns and CLI event arguments.
var hookEvents = []struct {
	Event   string // Claude Code hook event name
	CLIArg  string // capy hook <arg>
	Matcher string // empty = match all
}{
	{"PreToolUse", "pretooluse", PreToolUseMatcherPattern},
	{"PostToolUse", "posttooluse", ""},
	{"PreCompact", "precompact", ""},
	{"SessionStart", "sessionstart", ""},
	{"SessionEnd", "sessionend", ""},
	{"UserPromptSubmit", "userpromptsubmit", ""},
}

// mergeHooks reads .claude/settings.json, upserts capy hook entries, and writes back.
func mergeHooks(settingsPath, binaryPath string) error {
	settings, err := readJSONFile(settingsPath)
	if err != nil {
		return err
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = make(map[string]any)
	}

	for _, he := range hookEvents {
		// NOTE: binary paths with spaces are not supported — same as TS reference.
		// Claude Code splits the command string on spaces when spawning.
		hookCommand := binaryPath + " hook " + he.CLIArg
		entry := map[string]any{
			"matcher": he.Matcher,
			"hooks": []any{
				map[string]any{
					"type":    "command",
					"command": hookCommand,
				},
			},
		}

		existing, _ := hooks[he.Event].([]any)
		idx := findHookEntry(existing, "capy hook")
		if idx >= 0 {
			// Update existing capy entry
			existing[idx] = entry
		} else {
			existing = append(existing, entry)
		}
		hooks[he.Event] = existing
	}

	settings["hooks"] = hooks
	return writeJSONFile(settingsPath, settings)
}

// findHookEntry finds the index of a hook entry whose command contains the given substring.
// Returns -1 if not found.
func findHookEntry(entries []any, commandSubstr string) int {
	for i, e := range entries {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		innerHooks, _ := m["hooks"].([]any)
		for _, h := range innerHooks {
			hm, ok := h.(map[string]any)
			if !ok {
				continue
			}
			cmd, _ := hm["command"].(string)
			if strings.Contains(cmd, commandSubstr) {
				return i
			}
		}
	}
	return -1
}

// mergeMCPServer reads .mcp.json, upserts the capy server entry, and writes back.
func mergeMCPServer(mcpPath, binaryPath string) error {
	root, err := readJSONFile(mcpPath)
	if err != nil {
		return err
	}

	servers, _ := root["mcpServers"].(map[string]any)
	if servers == nil {
		servers = make(map[string]any)
	}

	servers["capy"] = map[string]any{
		"command": binaryPath,
		"args":    []any{"serve"},
	}

	root["mcpServers"] = servers
	return writeJSONFile(mcpPath, root)
}

// appendRoutingInstructions appends the routing block to CLAUDE.md if not already present.
func appendRoutingInstructions(claudeMDPath string) error {
	content, err := os.ReadFile(claudeMDPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	if strings.Contains(string(content), "capy — MANDATORY routing rules") {
		return nil // already has routing instructions
	}

	instructions := GenerateRoutingInstructions()

	f, err := os.OpenFile(claudeMDPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	// Add separator if file already has content
	if len(content) > 0 && !strings.HasSuffix(string(content), "\n\n") {
		if strings.HasSuffix(string(content), "\n") {
			fmt.Fprint(f, "\n")
		} else {
			fmt.Fprint(f, "\n\n")
		}
	}

	_, err = fmt.Fprint(f, instructions)
	return err
}

// ensureGitignoreEntry adds an entry to .gitignore if not already present.
func ensureGitignoreEntry(gitignorePath, entry string) error {
	content, err := os.ReadFile(gitignorePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	// Check if entry already exists
	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == entry {
			return nil // already present
		}
	}

	f, err := os.OpenFile(gitignorePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	// Add newline before entry if file doesn't end with one
	if len(content) > 0 && !strings.HasSuffix(string(content), "\n") {
		fmt.Fprint(f, "\n")
	}

	_, err = fmt.Fprintln(f, entry)
	return err
}

// readJSONFile reads a JSON file into a map. Returns an empty map if the file doesn't exist.
func readJSONFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return make(map[string]any), nil
	}
	if err != nil {
		return nil, err
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return result, nil
}

// writeJSONFile writes a map to a JSON file with 2-space indentation.
func writeJSONFile(path string, data map[string]any) error {
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return os.WriteFile(path, out, 0o644)
}
