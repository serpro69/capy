package hook

import (
	"regexp"
	"strings"
)

// heredocStartRe matches the start of a heredoc: <<EOF, <<"EOF", <<'EOF', <<-EOF
var heredocStartRe = regexp.MustCompile(`<<-?\s*["']?(\w+)["']?`)

// stripHeredocs removes heredoc content from a shell command.
// Go's RE2 doesn't support backreferences, so we find each heredoc start,
// extract the delimiter, then scan for the matching end marker.
func stripHeredocs(cmd string) string {
	result := cmd
	for {
		loc := heredocStartRe.FindStringSubmatchIndex(result)
		if loc == nil {
			break
		}
		// loc[2]:loc[3] is the captured delimiter name
		delimiter := result[loc[2]:loc[3]]
		// Find the closing delimiter on its own line
		endPattern := "\n" + delimiter
		endIdx := strings.Index(result[loc[1]:], endPattern)
		if endIdx == -1 {
			break // unclosed heredoc — stop
		}
		// Remove from heredoc start through end delimiter
		cutEnd := loc[1] + endIdx + len(endPattern)
		result = result[:loc[0]] + result[cutEnd:]
	}
	return result
}

// stripQuotedContent removes heredocs, single-quoted strings, and double-quoted
// strings from a shell command. Prevents false positives like:
//
//	gh issue edit --body "text with curl in it"
// Pre-compiled regexes for quote stripping (avoid compiling per call).
var (
	singleQuoteRe = regexp.MustCompile(`'[^']*'`)
	doubleQuoteRe = regexp.MustCompile(`"[^"]*"`)
)

func stripQuotedContent(cmd string) string {
	s := stripHeredocs(cmd)
	s = singleQuoteRe.ReplaceAllString(s, "''")
	s = doubleQuoteRe.ReplaceAllString(s, `""`)
	return s
}

// curlWgetRe matches curl or wget as a command (not inside quoted strings).
var curlWgetRe = regexp.MustCompile(`(?i)(^|\s|&&|\||\;)(curl|wget)\s`)

// isCurlOrWget returns true if the (stripped) command contains curl or wget.
func isCurlOrWget(stripped string) bool {
	return curlWgetRe.MatchString(stripped)
}

// httpPatterns matches inline HTTP calls in code.
var httpPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)fetch\s*\(\s*['"](https?://|http)`),
	regexp.MustCompile(`(?i)requests\.(get|post|put)\s*\(`),
	regexp.MustCompile(`(?i)http\.(get|request)\s*\(`),
}

// hasInlineHTTP returns true if the command contains inline HTTP calls.
func hasInlineHTTP(cmd string) bool {
	for _, re := range httpPatterns {
		if re.MatchString(cmd) {
			return true
		}
	}
	return false
}

// buildToolRe matches build tools: gradle, gradlew, mvn, mvnw.
var buildToolRe = regexp.MustCompile(`(?i)(^|\s|&&|\||\;)(\.\/gradlew|gradlew|gradle|\.\/mvnw|mvnw|mvn)\s`)

// isBuildTool returns true if the (stripped) command invokes a build tool.
func isBuildTool(stripped string) bool {
	return buildToolRe.MatchString(stripped)
}

// isCapyTool returns true if the tool name is a capy MCP tool.
func isCapyTool(toolName string) bool {
	return strings.HasPrefix(toolName, "capy_") ||
		strings.Contains(toolName, "/capy_") ||
		strings.Contains(toolName, "__capy_")
}

// toolAliases maps platform-specific tool names to canonical Claude Code names.
var toolAliases = map[string]string{
	// Gemini CLI
	"run_shell_command":  "Bash",
	"read_file":          "Read",
	"read_many_files":    "Read",
	"grep_search":        "Grep",
	"search_file_content": "Grep",
	"web_fetch":          "WebFetch",
	// OpenCode
	"bash":  "Bash",
	"view":  "Read",
	"grep":  "Grep",
	"fetch": "WebFetch",
	"agent": "Agent",
	// Codex CLI
	"shell":         "Bash",
	"shell_command":  "Bash",
	"exec_command":   "Bash",
	"container.exec": "Bash",
	"local_shell":    "Bash",
	"grep_files":     "Grep",
	// Cursor
	"mcp_web_fetch":  "WebFetch",
	"mcp_fetch_tool": "WebFetch",
	"Shell":          "Bash",
	// VS Code Copilot
	"run_in_terminal": "Bash",
	// Kiro CLI
	"fs_read":       "Read",
	"fs_write":      "Write",
	"execute_bash":  "Bash",
}

// canonicalToolName normalizes platform-specific tool names to canonical names.
func canonicalToolName(name string) string {
	if alias, ok := toolAliases[name]; ok {
		return alias
	}
	return name
}
