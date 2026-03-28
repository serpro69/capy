package hook

import (
	"fmt"
	"maps"
	"strings"

	"github.com/serpro69/capy/internal/adapter"
	"github.com/serpro69/capy/internal/security"
)

func handlePreToolUse(input []byte, a adapter.HookAdapter, policies []security.SecurityPolicy, projectDir string) ([]byte, error) {
	event, err := a.ParsePreToolUse(input)
	if err != nil {
		return nil, nil // pass through on parse error — don't block the tool
	}

	toolName := event.ToolName
	toolInput := event.ToolInput

	// Use projectDir from event if available, otherwise fall back to the one from CLI
	if event.ProjectDir != "" {
		projectDir = event.ProjectDir
	}

	canonical := canonicalToolName(toolName)

	// ─── Capy MCP tools: security checks only ───
	if isCapyTool(toolName) {
		return routeCapyTool(toolName, toolInput, policies, projectDir, a)
	}

	// ─── Bash: security check + routing ───
	if canonical == "Bash" {
		command, _ := toolInput["command"].(string)
		return routeBash(command, policies, a)
	}

	// ─── WebFetch: deny → redirect to sandbox ───
	if canonical == "WebFetch" {
		url, _ := toolInput["url"].(string)
		reason := fmt.Sprintf("capy: WebFetch blocked. Use capy_fetch_and_index(url: %q) to fetch this URL in sandbox. Then use capy_search(queries: [...]) to query results.", url)
		return a.FormatBlock(reason)
	}

	// ─── Read: guidance once ───
	if canonical == "Read" {
		return guidanceOnce("read", READ_GUIDANCE, a)
	}

	// ─── Grep: guidance once ───
	if canonical == "Grep" {
		return guidanceOnce("grep", GREP_GUIDANCE, a)
	}

	// ─── Agent/Task: inject routing block into subagent prompt ───
	if canonical == "Agent" || canonical == "Task" {
		return routeAgent(toolInput, a)
	}

	// Unknown tool — pass through
	return nil, nil
}

// routeBash handles Bash tool routing: security check, curl/wget, HTTP, build tools, guidance.
func routeBash(command string, policies []security.SecurityPolicy, a adapter.HookAdapter) ([]byte, error) {
	// Stage 1: Security check (full evaluateCommand with ask support)
	if len(policies) > 0 {
		result := security.EvaluateCommand(command, policies)
		if result.Decision == "deny" {
			return a.FormatBlock(fmt.Sprintf("Blocked by security policy: matches deny pattern %s", result.MatchedPattern))
		}
		if result.Decision == "ask" && result.MatchedPattern != "" {
			return a.FormatAsk()
		}
	}

	// Stage 2: Context-mode routing

	// curl/wget detection (strip quoted content to avoid false positives)
	// Replace command with echo message (FormatModify) instead of hard deny,
	// matching the TS reference — the LLM sees the guidance in stdout.
	stripped := stripQuotedContent(command)
	if isCurlOrWget(stripped) {
		return a.FormatModify(map[string]any{
			"command": `echo "capy: curl/wget blocked. Use capy_fetch_and_index(url, source) to fetch URLs, or capy_execute(language, code) to run HTTP calls in sandbox. Do NOT retry with curl/wget."`,
		})
	}

	// Inline HTTP detection (strip only heredocs — code in -e/-c flags should be visible)
	noHeredoc := stripHeredocs(command)
	if hasInlineHTTP(noHeredoc) {
		return a.FormatModify(map[string]any{
			"command": `echo "capy: Inline HTTP blocked. Use capy_execute(language, code) to run HTTP calls in sandbox, or capy_fetch_and_index(url, source) for web pages. Do NOT retry with Bash."`,
		})
	}

	// Build tools (gradle, maven) → redirect to sandbox
	if isBuildTool(stripped) {
		return a.FormatModify(map[string]any{
			"command": `echo "capy: Build tool redirected to sandbox. Use capy_execute(language: \"shell\", code: \"...\") to run this command. Do NOT retry with Bash."`,
		})
	}

	// Allow, but inject routing nudge (once per session)
	return guidanceOnce("bash", BASH_GUIDANCE, a)
}

// routeAgent injects the routing block into Agent/Task subagent prompts.
func routeAgent(toolInput map[string]any, a adapter.HookAdapter) ([]byte, error) {
	// Find the prompt field
	promptFields := []string{"prompt", "request", "objective", "question", "query", "task"}
	fieldName := "prompt"
	for _, f := range promptFields {
		if _, ok := toolInput[f]; ok {
			fieldName = f
			break
		}
	}

	prompt, _ := toolInput[fieldName].(string)

	// Build updated input
	updated := make(map[string]any, len(toolInput))
	maps.Copy(updated, toolInput)
	updated[fieldName] = prompt + RoutingBlock()

	// Upgrade Bash subagent to general-purpose
	if subType, _ := toolInput["subagent_type"].(string); subType == "Bash" {
		updated["subagent_type"] = "general-purpose"
	}

	return a.FormatModify(updated)
}

// routeCapyTool runs security checks on capy MCP tools.
func routeCapyTool(toolName string, toolInput map[string]any, policies []security.SecurityPolicy, projectDir string, a adapter.HookAdapter) ([]byte, error) {
	if len(policies) == 0 {
		return nil, nil
	}

	// Determine which tool variant we're dealing with
	switch {
	case strings.HasSuffix(toolName, "execute") && !strings.HasSuffix(toolName, "batch_execute"):
		// capy_execute: check shell code against deny policies
		lang, _ := toolInput["language"].(string)
		if lang == "shell" {
			code, _ := toolInput["code"].(string)
			return checkCommandSecurity(code, policies, a)
		}

	case strings.HasSuffix(toolName, "execute_file"):
		// capy_execute_file: check file path + shell code
		filePath, _ := toolInput["path"].(string)
		if filePath != "" {
			denyGlobs := security.ReadToolDenyPatterns("Read", projectDir, "")
			denied, pattern := security.EvaluateFilePath(filePath, denyGlobs)
			if denied {
				return a.FormatBlock(fmt.Sprintf("Blocked by security policy: file path matches Read deny pattern %s", pattern))
			}
		}
		lang, _ := toolInput["language"].(string)
		if lang == "shell" {
			code, _ := toolInput["code"].(string)
			return checkCommandSecurity(code, policies, a)
		}

	case strings.HasSuffix(toolName, "batch_execute"):
		// capy_batch_execute: check each command
		commands, _ := toolInput["commands"].([]any)
		for _, entry := range commands {
			if m, ok := entry.(map[string]any); ok {
				cmd, _ := m["command"].(string)
				if result, err := checkCommandSecurity(cmd, policies, a); result != nil || err != nil {
					return result, err
				}
			}
		}
	}

	return nil, nil // allow
}

// checkCommandSecurity evaluates a command against deny policies.
func checkCommandSecurity(command string, policies []security.SecurityPolicy, a adapter.HookAdapter) ([]byte, error) {
	result := security.EvaluateCommand(command, policies)
	if result.Decision == "deny" {
		return a.FormatBlock(fmt.Sprintf("Blocked by security policy: matches deny pattern %s", result.MatchedPattern))
	}
	if result.Decision == "ask" && result.MatchedPattern != "" {
		return a.FormatAsk()
	}
	return nil, nil
}
