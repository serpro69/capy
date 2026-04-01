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

// splitChainedCommands splits a shell command on &&, ||, ;, and | operators,
// respecting single-quoted, double-quoted, and backtick-quoted strings.
func splitChainedCommands(cmd string) []string {
	var parts []string
	var current strings.Builder
	inSingle, inDouble, inBacktick := false, false, false

	for i := 0; i < len(cmd); i++ {
		ch := cmd[i]
		prev := byte(0)
		if i > 0 {
			prev = cmd[i-1]
		}

		switch {
		case ch == '\'' && !inDouble && !inBacktick && prev != '\\':
			inSingle = !inSingle
			current.WriteByte(ch)
		case ch == '"' && !inSingle && !inBacktick && prev != '\\':
			inDouble = !inDouble
			current.WriteByte(ch)
		case ch == '`' && !inSingle && !inDouble && prev != '\\':
			inBacktick = !inBacktick
			current.WriteByte(ch)
		case !inSingle && !inDouble && !inBacktick:
			if ch == ';' {
				parts = append(parts, strings.TrimSpace(current.String()))
				current.Reset()
			} else if ch == '|' && i+1 < len(cmd) && cmd[i+1] == '|' {
				parts = append(parts, strings.TrimSpace(current.String()))
				current.Reset()
				i++ // skip second |
			} else if ch == '&' && i+1 < len(cmd) && cmd[i+1] == '&' {
				parts = append(parts, strings.TrimSpace(current.String()))
				current.Reset()
				i++ // skip second &
			} else if ch == '|' {
				// Single pipe — left side is a command too
				parts = append(parts, strings.TrimSpace(current.String()))
				current.Reset()
			} else {
				current.WriteByte(ch)
			}
		default:
			current.WriteByte(ch)
		}
	}
	if s := strings.TrimSpace(current.String()); s != "" {
		parts = append(parts, s)
	}
	// Filter empty parts
	result := parts[:0]
	for _, p := range parts {
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// curlOutputRe matches curl file-output flags: -o, -sSLo (combined), or --output
var curlOutputRe = regexp.MustCompile(`\s-[a-zA-Z]*o\s|--output\s`)

// wgetOutputRe matches wget file-output flags: -O, -qO (combined), or --output-document
var wgetOutputRe = regexp.MustCompile(`\s-[a-zA-Z]*O\s|--output-document\s`)

// redirectRe matches shell output redirects (> or >>) to a file path.
// Excludes fd duplications like 2>&1 by requiring a non-& non-> non-whitespace char after.
var redirectRe = regexp.MustCompile(`>>?\s*[^&>\s]`)

// curlStdoutAliasRe matches curl outputting to stdout: -o -, -sSLo -, or --output /dev/stdout
var curlStdoutAliasRe = regexp.MustCompile(`\s-[a-zA-Z]*o\s+(-|/dev/stdout)(\s|$)|--output\s+(-|/dev/stdout)(\s|$)`)

// wgetStdoutAliasRe matches wget outputting to stdout: -O -, -qO -, or --output-document /dev/stdout
var wgetStdoutAliasRe = regexp.MustCompile(`\s-[a-zA-Z]*O\s+(-|/dev/stdout)(\s|$)|--output-document\s+(-|/dev/stdout)(\s|$)`)

// verboseRe matches verbose/trace flags that flood stderr
var verboseRe = regexp.MustCompile(`\s(-v|--verbose|--trace|-D\s+-)\b`)

// curlSilentRe matches curl silent flags: -s, --silent, or combined like -sS, -fsSL
var curlSilentRe = regexp.MustCompile(`\s-[a-zA-Z]*s|--silent`)

// wgetQuietRe matches wget quiet flags: -q, --quiet, or combined like -qO
var wgetQuietRe = regexp.MustCompile(`\s-[a-zA-Z]*q|--quiet`)

// isCurlWgetSafe checks if a curl/wget command segment writes to a file silently
// (not stdout). A segment is safe only if it has:
//   - file output (-o/--output, -O/--output-document, or >/>>)
//   - no stdout aliases (-o -, -O -, -o /dev/stdout)
//   - no verbose/trace flags
//   - silent mode enabled (-s/--silent for curl, -q/--quiet for wget)
func isCurlWgetSafe(segment string) bool {
	isCurl := strings.Contains(strings.ToLower(segment), "curl")
	isWget := strings.Contains(strings.ToLower(segment), "wget")

	if !isCurl && !isWget {
		return true // not a curl/wget command
	}

	// Check for file output flags
	hasFileOutput := false
	if isCurl {
		hasFileOutput = curlOutputRe.MatchString(segment) || redirectRe.MatchString(segment)
	} else {
		hasFileOutput = wgetOutputRe.MatchString(segment) || redirectRe.MatchString(segment)
	}
	if !hasFileOutput {
		return false // no file output → stdout flood risk
	}

	// Stdout aliases: -o - or -o /dev/stdout
	if isCurl && curlStdoutAliasRe.MatchString(segment) {
		return false
	}
	if isWget && wgetStdoutAliasRe.MatchString(segment) {
		return false
	}

	// Verbose/trace flags flood stderr → context
	if verboseRe.MatchString(segment) {
		return false
	}

	// Must be silent to prevent progress bar stderr flood
	if isCurl {
		return curlSilentRe.MatchString(segment)
	}
	return wgetQuietRe.MatchString(segment)
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
