package hook

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/serpro69/capy/internal/adapter"
	"github.com/serpro69/capy/internal/security"
)

func handlePreToolUse(input []byte, a adapter.HookAdapter, policies []security.SecurityPolicy, projectDir string) ([]byte, error) {
	event, err := a.ParsePreToolUse(input)
	if err != nil {
		return nil, nil // pass through on parse error — don't block the tool
	}
	ctx := hookContext{projectDir: projectDir, workingDir: projectDir}
	if cwd := validatedPayloadCwd(event); cwd != "" {
		ctx.workingDir = cwd
	}
	return routePreToolUse(event, a, policies, ctx)
}

func routePreToolUse(event *adapter.PreToolUseEvent, a adapter.HookAdapter, policies []security.SecurityPolicy, ctx hookContext) ([]byte, error) {
	toolName := event.ToolName
	toolInput := event.ToolInput
	sessionID := event.SessionID

	canonical := canonicalToolName(toolName)

	// ─── Capy MCP tools: security checks only ───
	if isCapyTool(toolName) {
		return routeCapyTool(toolName, toolInput, policies, ctx, a)
	}

	// ─── Bash: security check + routing ───
	if canonical == "Bash" {
		return routeBash(event, a, policies, ctx)
	}

	// ─── WebFetch: deny → redirect with comprehension-aware guidance ───
	if canonical == "WebFetch" {
		rawURL, _ := toolInput["url"].(string)
		if event.AgentID != "" {
			parsedURL, err := url.Parse(rawURL)
			eligible := err == nil && parsedURL.Hostname() != "" &&
				(parsedURL.Scheme == "https" || parsedURL.Scheme == "http") && gitPlatformBlockMessage(rawURL) == ""
			if eligible {
				if tool := consumeChildObservation(event, ctx, true); tool != "" {
					return a.FormatBlock("capy: Use " + tool + " for this web extraction. If that call fails or the tool is unavailable, retry the native tool.")
				}
			}
			return a.FormatAllow(childToolGuidance)
		}
		return a.FormatBlock(webFetchBlockMessage(rawURL))
	}

	// ─── Read: guidance once ───
	if canonical == "Read" {
		return guidanceOnce("read", READ_GUIDANCE, a, ctx.projectDir, sessionID)
	}

	// ─── Grep: guidance once ───
	if canonical == "Grep" {
		return guidanceOnce("grep", GREP_GUIDANCE, a, ctx.projectDir, sessionID)
	}

	// ─── Agent/Task: inject routing block into subagent prompt ───
	if canonical == "Agent" || canonical == "Task" {
		return routeAgent(toolInput, a)
	}

	// Unknown tool — pass through
	return nil, nil
}

// routeBash handles Bash tool routing: security check, curl/wget, HTTP, build tools, guidance.
func routeBash(event *adapter.PreToolUseEvent, a adapter.HookAdapter, policies []security.SecurityPolicy, ctx hookContext) ([]byte, error) {
	command, _ := event.ToolInput["command"].(string)
	// Stage 1: Security check (full evaluateCommand with ask support)
	if result, err := checkCommandSecurity(command, policies, a); result != nil || err != nil {
		return result, err
	}

	// Stage 2: Context-mode routing

	// curl/wget detection (strip quoted content to avoid false positives)
	// Return routing guidance as a denial reason, without approving a replacement
	// command. FormatModify is reserved for actual Agent/Task input edits.
	// Smart check: allow curl/wget that writes to a file silently (#166).
	stripped := stripQuotedContent(command)
	if isCurlOrWget(stripped) {
		segments := splitChainedCommands(stripped)
		allSafe := true
		for _, seg := range segments {
			if isCurlOrWget(seg) && !isCurlWgetSafe(seg) {
				allSafe = false
				break
			}
		}
		if !allSafe {
			if event.AgentID != "" {
				if tool := consumeChildObservation(event, ctx, false); tool != "" {
					return a.FormatBlock("capy: Use " + tool + "(language, code) for this HTTP call. If that call fails or the tool is unavailable, retry the native tool.")
				}
				return a.FormatAllow(childToolGuidance)
			}
			return a.FormatBlock("capy: curl/wget blocked (stdout flood risk). " +
				"Use capy_fetch_and_index(url, source) to fetch URLs, or capy_execute(language, code) to run HTTP calls in sandbox. " +
				"Silent file downloads with -o/--output (curl) or -O/--output-document (wget) are allowed.")
		}
		// All curl/wget segments write to file silently — allow through
	}

	// Inline HTTP detection (strip only heredocs — code in -e/-c flags should be visible)
	noHeredoc := stripHeredocs(command)
	if hasInlineHTTP(noHeredoc) {
		if event.AgentID != "" {
			if tool := consumeChildObservation(event, ctx, false); tool != "" {
				return a.FormatBlock("capy: Use " + tool + "(language, code) for this HTTP call. If that call fails or the tool is unavailable, retry the native tool.")
			}
			return a.FormatAllow(childToolGuidance)
		}
		return a.FormatBlock("capy: Inline HTTP blocked. " +
			"Use capy_execute(language, code) to run HTTP calls in sandbox, or capy_fetch_and_index(url, source) for web pages. " +
			"Do NOT retry with Bash.")
	}

	// Allow, but inject routing nudge (once per session)
	return guidanceOnce("bash", BASH_GUIDANCE, a, ctx.projectDir, event.SessionID)
}

func consumeChildObservation(event *adapter.PreToolUseEvent, ctx hookContext, allowFetch bool) string {
	// D5: this is one-use evidence, not authoritative discovery of a child tool
	// pool. A child's first native call has no evidence and remains advisory.
	if !event.SessionIDStable || event.SessionID == "" || event.AgentID == "" {
		return ""
	}
	tool, err := newObservationStore(ctx.projectDir).consume(event.SessionID, event.AgentID, allowFetch)
	if err != nil {
		slog.Warn("could not consume child tool observation", "error", err)
		return ""
	}
	return tool
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

// routeCapyTool runs security checks and routing guidance on capy MCP tools.
func routeCapyTool(toolName string, toolInput map[string]any, policies []security.SecurityPolicy, ctx hookContext, a adapter.HookAdapter) ([]byte, error) {
	if strings.HasSuffix(toolName, "execute_file") || strings.HasSuffix(toolName, "_index") {
		if filePath, _ := toolInput["path"].(string); filePath != "" {
			policy, err := security.LoadReadPolicy(security.FilePolicyContext{
				ProjectDir: ctx.projectDir,
				WorkingDir: ctx.workingDir,
			})
			if err == nil {
				for _, warning := range policy.Warnings() {
					slog.Warn(warning)
				}
				// Capy file parameters stay project-relative; only the rule's cwd
				// anchor follows the hook payload. Preserve symlink-before-.. input.
				if !filepath.IsAbs(filePath) {
					filePath = ctx.projectDir + string(filepath.Separator) + filePath
				}
				err = policy.Check(filePath)
			}
			if err != nil {
				return a.FormatBlock(fmt.Sprintf("Blocked by security policy: %v", err))
			}
		}
	}
	// Scanner limits apply even without configured policy patterns.
	switch {
	case strings.HasSuffix(toolName, "execute") && !strings.HasSuffix(toolName, "batch_execute"):
		lang, _ := toolInput["language"].(string)
		code, _ := toolInput["code"].(string)
		return checkCodeSecurity(code, lang, policies, a)

	case strings.HasSuffix(toolName, "execute_file"):
		lang, _ := toolInput["language"].(string)
		code, _ := toolInput["code"].(string)
		return checkCodeSecurity(code, lang, policies, a)

	case strings.HasSuffix(toolName, "batch_execute"):
		raw := toolInput["commands"]
		if encoded, ok := raw.(string); ok {
			if err := json.Unmarshal([]byte(encoded), &raw); err != nil {
				return a.FormatBlock("Command blocked: invalid batch commands JSON")
			}
		}
		entries, ok := raw.([]any)
		if !ok {
			return a.FormatBlock("Command blocked: expected batch commands array")
		}
		commands := make([]string, 0, len(entries))
		for _, entry := range entries {
			if m, ok := entry.(map[string]any); ok {
				entry = m["command"]
			}
			command, ok := entry.(string)
			if !ok {
				return a.FormatBlock("Command blocked: invalid command format (expected string)")
			}
			commands = append(commands, command)
		}
		return checkCommandsSecurity(commands, policies, a)
	}

	// Git platform URL enforcement for fetch_and_index (fires every call)
	if strings.HasSuffix(toolName, "fetch_and_index") {
		url, _ := toolInput["url"].(string)
		if msg := gitPlatformBlockMessage(url); msg != "" {
			return a.FormatBlock(msg)
		}
		if guidance := gistGuidance(url); guidance != "" {
			return a.FormatAllow(guidance)
		}
	}

	return nil, nil // allow
}

// checkCommandSecurity evaluates a command against deny policies.
func checkCommandSecurity(command string, policies []security.SecurityPolicy, a adapter.HookAdapter) ([]byte, error) {
	return checkCommandsSecurity([]string{command}, policies, a)
}

func checkCommandsSecurity(commands []string, policies []security.SecurityPolicy, a adapter.HookAdapter) ([]byte, error) {
	ask := false
	for _, command := range commands {
		result, err := security.EvaluateCommand(command, policies)
		if err != nil {
			return a.FormatBlock(fmt.Sprintf("Command blocked: %v", err))
		}
		if result.Decision == "deny" {
			return a.FormatBlock(fmt.Sprintf("Blocked by security policy: matches deny pattern %s", result.MatchedPattern))
		}
		ask = ask || result.Decision == "ask" && result.MatchedPattern != ""
	}
	// A matched ask must not hide a later deny or scanner failure in the request.
	if ask {
		return a.FormatAsk()
	}
	return nil, nil
}

func checkCodeSecurity(code, language string, policies []security.SecurityPolicy, a adapter.HookAdapter) ([]byte, error) {
	if language == "shell" {
		return checkCommandSecurity(code, policies, a)
	}
	return checkCommandsSecurity(security.ExtractShellCommands(code, language), policies, a)
}
