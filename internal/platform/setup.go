package platform

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/serpro69/capy/internal/config"
)

// capyWrapperRelPath is the project-relative path to the portable capy wrapper script.
const capyWrapperRelPath = ".claude/scripts/capy.sh"

// capyWrapperScript is the content of the portable wrapper script.
// It searches known installation paths for the capy binary and execs the first
// one found, making configs portable across machines and platforms.
// Paths are quoted to handle spaces (e.g. WSL home dirs like /c/Users/John Doe).
// Uses `command -v` instead of `which` for POSIX compliance.
const capyWrapperScript = `#!/usr/bin/env bash

# Wrapper that locates and runs the capy binary.
# Used as a Claude Code hook — must always exit 0 to avoid phantom hook errors.
# See: https://github.com/serpro69/claude-toolbox/issues/57

set -uo pipefail

for p in "$(command -v capy 2>/dev/null || true)" "$HOME/.local/bin/capy" "/opt/homebrew/bin/capy" "/usr/local/bin/capy" "$HOME/go/bin/capy" "capy"; do
  if [ -n "$p" ] && [ -x "$p" ]; then
    "$p" "$@" || true
    exit 0
  fi
done

# capy not found — deny tool use
jq -n --arg reason "capy binary not found" \
	'{hookSpecificOutput: {hookEventName: "PreToolUse", permissionDecision: "deny", permissionDecisionReason: $reason}}'
exit 0
`

// writeCapyWrapper creates the portable wrapper script at .claude/scripts/capy.sh.
func writeCapyWrapper(projectDir string) error {
	scriptsDir := filepath.Join(projectDir, ".claude", "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		return fmt.Errorf("creating scripts directory: %w", err)
	}
	wrapperPath := filepath.Join(projectDir, capyWrapperRelPath)
	return os.WriteFile(wrapperPath, []byte(capyWrapperScript), 0o755)
}

// SetupClaudeCode configures capy for a Claude Code project.
// It merges hook and MCP configurations idempotently, creates the .capy/
// directory, appends routing instructions to CLAUDE.md, and adds .capy/
// to .gitignore.
//
// Configs reference a portable wrapper script instead of a hardcoded binary
// path, making them work across machines and platforms (fixes #10).
func SetupClaudeCode(binaryPath, projectDir string, target SettingsTarget) error {
	// 1. Resolve binary path (validation only — configs use the portable wrapper)
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

	// 4. Write portable wrapper script (.claude/scripts/capy.sh)
	if err := writeCapyWrapper(projectDir); err != nil {
		return fmt.Errorf("writing wrapper script: %w", err)
	}

	// 5. Merge hooks into target settings file, migrating from the other if needed
	targetFile := target.SettingsFilename()
	otherFile := SettingsProject.SettingsFilename()
	if target == SettingsProject {
		otherFile = SettingsLocal.SettingsFilename()
	}

	otherPath := filepath.Join(claudeDir, otherFile)
	if removed, err := removeCapyHooks(otherPath); err != nil {
		return fmt.Errorf("removing hooks from %s: %w", otherFile, err)
	} else if removed {
		fmt.Fprintf(os.Stderr, "capy: migrated hooks from .claude/%s -> .claude/%s\n", otherFile, targetFile)
	}

	settingsPath := filepath.Join(claudeDir, targetFile)
	if err := mergeHooks(settingsPath); err != nil {
		return fmt.Errorf("merging hooks: %w", err)
	}

	// 6. Merge MCP server into .mcp.json
	mcpPath := filepath.Join(projectDir, ".mcp.json")
	if err := mergeMCPServer(mcpPath); err != nil {
		return fmt.Errorf("merging MCP config: %w", err)
	}

	// 7. Write routing instructions to .claude/capy/CLAUDE.md and import from root CLAUDE.md
	claudeMDPath := filepath.Join(projectDir, "CLAUDE.md")
	if err := writeRoutingInstructions(claudeDir, claudeMDPath); err != nil {
		return fmt.Errorf("updating routing instructions: %w", err)
	}

	// 8. Add .capy/** to .gitignore
	gitignorePath := filepath.Join(projectDir, ".gitignore")
	if err := ensureGitignoreEntry(gitignorePath, ".capy/**"); err != nil {
		return fmt.Errorf("updating .gitignore: %w", err)
	}

	// 9. Install git pre-commit hook for WAL checkpoint
	if err := installPreCommitHook(projectDir); err != nil {
		// Non-fatal: git hooks are a convenience, not a requirement
		fmt.Fprintf(os.Stderr, "capy: warning: could not install pre-commit hook: %v\n", err)
	}

	return nil
}

// preCommitMarkerStart is the start marker for the capy block in pre-commit hooks.
const preCommitMarkerStart = "# capy: checkpoint WAL before committing knowledge DB"

// preCommitMarkerEnd is the end marker for the capy block in pre-commit hooks.
const preCommitMarkerEnd = "# capy: end checkpoint"

// shellEscapePath escapes a path for safe use in single-quoted shell strings.
func shellEscapePath(path string) string {
	return strings.ReplaceAll(path, "'", `'\''`)
}

// preCommitHookScript returns the content of the git pre-commit hook.
// It checkpoints the WAL only when the knowledge DB is staged for commit.
// dbPattern is a grep pattern matching the DB path relative to the repo root.
func preCommitHookScript(dbPattern string) string {
	safePattern := shellEscapePath(dbPattern)
	return fmt.Sprintf(`#!/bin/sh
%s
# Installed by capy setup — safe to remove if not needed.

if git diff --cached --name-only | grep -q '%s'; then
  bash "%s" checkpoint
  git diff --cached --name-only | grep '%s' | while read -r f; do git add "$f"; done
fi
%s
`, preCommitMarkerStart, safePattern, capyWrapperRelPath, safePattern, preCommitMarkerEnd)
}

// installPreCommitHook installs or updates the git pre-commit hook.
// If a pre-commit hook already exists with a capy block, replaces it.
// Otherwise appends the checkpoint logic.
func installPreCommitHook(projectDir string) error {
	hookDir := filepath.Join(projectDir, ".git", "hooks")
	if _, err := os.Stat(hookDir); os.IsNotExist(err) {
		return nil // not a git repo, skip
	}

	// Resolve the actual DB path from config so the hook matches custom paths.
	dbPattern := resolveDBPattern(projectDir)
	script := preCommitHookScript(dbPattern)
	hookPath := filepath.Join(hookDir, "pre-commit")

	existing, err := os.ReadFile(hookPath)
	if os.IsNotExist(err) {
		// No existing hook — create new
		return os.WriteFile(hookPath, []byte(script), 0o755)
	}
	if err != nil {
		return err
	}

	content := string(existing)

	// If capy block exists, replace it (handles binary path / DB path changes)
	if startIdx := strings.Index(content, preCommitMarkerStart); startIdx >= 0 {
		endIdx := strings.Index(content, preCommitMarkerEnd)
		if endIdx >= 0 {
			endIdx += len(preCommitMarkerEnd)
			// Consume trailing newline if present
			if endIdx < len(content) && content[endIdx] == '\n' {
				endIdx++
			}
			content = content[:startIdx] + script + content[endIdx:]
			return os.WriteFile(hookPath, []byte(content), 0o755)
		}
	}

	// No existing capy block — append
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += "\n" + script

	return os.WriteFile(hookPath, []byte(content), 0o755)
}

// resolveDBPattern returns a grep pattern for the knowledge DB path relative
// to the project root. Falls back to the default `.capy/knowledge.db` if
// config can't be loaded.
func resolveDBPattern(projectDir string) string {
	cfg, err := config.Load(projectDir)
	if err != nil || cfg == nil {
		cfg = config.DefaultConfig()
	}
	dbPath := cfg.ResolveDBPath(projectDir)

	// Make relative to project dir for the git diff pattern
	rel, err := filepath.Rel(projectDir, dbPath)
	if err != nil {
		rel = ".capy/knowledge.db"
	}

	// Escape dots for grep regex
	return strings.ReplaceAll(rel, ".", `\.`) + "$"
}

// SettingsTarget indicates which Claude Code settings file to write hooks to.
type SettingsTarget int

const (
	// SettingsProject targets .claude/settings.json (shared, committed to git).
	SettingsProject SettingsTarget = iota
	// SettingsLocal targets .claude/settings.local.json (personal, not committed).
	SettingsLocal
)

// SettingsFilename returns the filename for the given settings target.
func (t SettingsTarget) SettingsFilename() string {
	if t == SettingsLocal {
		return "settings.local.json"
	}
	return "settings.json"
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

// mergeHooks reads the given settings file, upserts capy hook entries, and writes back.
// Hook commands reference the portable wrapper script via $CLAUDE_PROJECT_DIR.
func mergeHooks(settingsPath string) error {
	settings, err := readJSONFile(settingsPath)
	if err != nil {
		return err
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = make(map[string]any)
	}

	for _, he := range hookEvents {
		hookCommand := "bash $CLAUDE_PROJECT_DIR/" + capyWrapperRelPath + " hook " + he.CLIArg
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
		idx := findHookEntry(existing, "hook "+he.CLIArg)
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

// removeCapyHooks removes all capy hook entries from a settings file.
// Returns true if any hooks were removed. Preserves non-capy entries.
func removeCapyHooks(settingsPath string) (bool, error) {
	settings, err := readJSONFile(settingsPath)
	if err != nil {
		return false, err
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return false, nil
	}

	removed := false
	for _, he := range hookEvents {
		existing, _ := hooks[he.Event].([]any)
		idx := findHookEntry(existing, "hook "+he.CLIArg)
		if idx >= 0 {
			existing = append(existing[:idx], existing[idx+1:]...)
			removed = true
			if len(existing) == 0 {
				delete(hooks, he.Event)
			} else {
				hooks[he.Event] = existing
			}
		}
	}

	if !removed {
		return false, nil
	}

	if len(hooks) == 0 {
		delete(settings, "hooks")
	}
	return true, writeJSONFile(settingsPath, settings)
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
// Uses the portable wrapper script instead of a hardcoded binary path.
func mergeMCPServer(mcpPath string) error {
	root, err := readJSONFile(mcpPath)
	if err != nil {
		return err
	}

	servers, _ := root["mcpServers"].(map[string]any)
	if servers == nil {
		servers = make(map[string]any)
	}

	servers["capy"] = map[string]any{
		"command": "bash",
		"args":    []any{capyWrapperRelPath, "serve"},
	}

	root["mcpServers"] = servers
	return writeJSONFile(mcpPath, root)
}

// routingImportRef is the Claude Code import reference for the capy routing instructions file.
const routingImportRef = "@.claude/capy/CLAUDE.md"

// routingImportBlock is the full block appended to root CLAUDE.md to import capy routing.
const routingImportBlock = "# capy — MANDATORY routing rules\n\n" + routingImportRef + "\n"

// writeRoutingInstructions writes routing instructions to .claude/capy/CLAUDE.md
// and ensures root CLAUDE.md imports them. If root CLAUDE.md has the old inline
// routing block, it is replaced with the import reference.
func writeRoutingInstructions(claudeDir, claudeMDPath string) error {
	// Step A: Write .claude/capy/CLAUDE.md (always overwrite — generated content)
	capyDir := filepath.Join(claudeDir, "capy")
	if err := os.MkdirAll(capyDir, 0o755); err != nil {
		return fmt.Errorf("creating .claude/capy directory: %w", err)
	}

	capyCLAUDEMD := filepath.Join(capyDir, "CLAUDE.md")
	if err := os.WriteFile(capyCLAUDEMD, []byte(GenerateRoutingInstructions()), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", capyCLAUDEMD, err)
	}

	// Step B: Ensure root CLAUDE.md imports .claude/capy/CLAUDE.md
	content, err := os.ReadFile(claudeMDPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	text := string(content)

	// Already has the import → nothing to do
	if strings.Contains(text, routingImportRef) {
		return nil
	}

	// Old inline routing block present → replace with import.
	// Uses marker-based detection so migration works even when
	// GenerateRoutingInstructions() output changed between versions.
	const startMarker = "# capy — MANDATORY routing rules"
	if startIdx := strings.Index(text, startMarker); startIdx >= 0 {
		rest := text[startIdx+len(startMarker):]
		if endIdx := strings.Index(rest, "\n# "); endIdx >= 0 {
			// Content follows after the capy block — preserve it
			text = text[:startIdx] + routingImportBlock + rest[endIdx+1:]
		} else {
			// Capy block extends to EOF
			text = text[:startIdx] + routingImportBlock
		}
		return os.WriteFile(claudeMDPath, []byte(text), 0o644)
	}

	// Neither import nor inline → append the import block
	f, err := os.OpenFile(claudeMDPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	// Add separator if file already has content
	if len(content) > 0 && !strings.HasSuffix(text, "\n\n") {
		if strings.HasSuffix(text, "\n") {
			fmt.Fprint(f, "\n")
		} else {
			fmt.Fprint(f, "\n\n")
		}
	}

	_, err = fmt.Fprint(f, routingImportBlock)
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
