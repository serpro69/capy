package security

import (
	"regexp"
	"strings"
)

// quotedPattern creates two regexp patterns (single-quote and double-quote variants)
// from a prefix and suffix. This replaces backreference patterns (\1) that Go's
// RE2 engine doesn't support — each quote type gets its own compiled pattern.
func quotedPattern(prefix, suffix string) []*regexp.Regexp {
	return []*regexp.Regexp{
		regexp.MustCompile(prefix + `'([^']*?)'` + suffix),
		regexp.MustCompile(prefix + `"([^"]*?)"` + suffix),
	}
}

// quotedPatternWithBacktick creates three regexp patterns (single, double, backtick).
func quotedPatternWithBacktick(prefix, suffix string) []*regexp.Regexp {
	return []*regexp.Regexp{
		regexp.MustCompile(prefix + `'([^']*?)'` + suffix),
		regexp.MustCompile(prefix + `"([^"]*?)"` + suffix),
		regexp.MustCompile(prefix + "`([^`]*?)`" + suffix),
	}
}

// shellEscapePatterns detects shell-escape calls in non-shell languages.
// Each pattern has exactly one capture group containing the command string.
var shellEscapePatterns map[string][]*regexp.Regexp

func init() {
	shellEscapePatterns = map[string][]*regexp.Regexp{
		"python": flatten(
			quotedPattern(`os\.system\(\s*`, `\s*\)`),
			quotedPattern(`subprocess\.(?:run|call|Popen|check_output|check_call)\(\s*`, ``),
		),
		"javascript": flatten(
			quotedPatternWithBacktick(`exec(?:Sync|File|FileSync)?\(\s*`, ``),
			quotedPatternWithBacktick(`spawn(?:Sync)?\(\s*`, ``),
		),
		"typescript": flatten(
			quotedPatternWithBacktick(`exec(?:Sync|File|FileSync)?\(\s*`, ``),
			quotedPatternWithBacktick(`spawn(?:Sync)?\(\s*`, ``),
		),
		"ruby": flatten(
			quotedPattern(`system\(\s*`, ``),
			[]*regexp.Regexp{regexp.MustCompile("`([^`]*?)`")},
		),
		"go": flatten(
			quotedPatternWithBacktick(`exec\.Command\(\s*`, ``),
		),
		"php": flatten(
			quotedPatternWithBacktick(`shell_exec\(\s*`, ``),
			quotedPatternWithBacktick(`(?:^|[^.])exec\(\s*`, ``),
			quotedPatternWithBacktick(`(?:^|[^.])system\(\s*`, ``),
			quotedPatternWithBacktick(`passthru\(\s*`, ``),
			quotedPatternWithBacktick(`proc_open\(\s*`, ``),
		),
		"rust": flatten(
			quotedPatternWithBacktick(`Command::new\(\s*`, ``),
		),
	}
}

// flatten concatenates multiple regexp slices.
func flatten(slices ...[]*regexp.Regexp) []*regexp.Regexp {
	var result []*regexp.Regexp
	for _, s := range slices {
		result = append(result, s...)
	}
	return result
}

// pythonSubprocessListPattern matches subprocess calls with list arguments:
// subprocess.run(["rm", "-rf", "/"]) → extracts the list content.
var pythonSubprocessListPattern = regexp.MustCompile(
	`subprocess\.(?:run|call|Popen|check_output|check_call)\(\s*\[([^\]]+)\]`,
)

// pythonListArgPattern matches individual string elements in a list.
var pythonListArgPattern = regexp.MustCompile(`['"]([^'"]*?)['"]`)

// extractPythonSubprocessListArgs extracts commands from Python subprocess
// list-form calls: subprocess.run(["rm", "-rf", "/"]) → "rm -rf /"
func extractPythonSubprocessListArgs(code string) []EmbeddedCommand {
	var commands []EmbeddedCommand
	matches := pythonSubprocessListPattern.FindAllStringSubmatchIndex(code, -1)
	for _, m := range matches {
		listContent := code[m[2]:m[3]]
		argMatches := pythonListArgPattern.FindAllStringSubmatch(listContent, -1)
		if len(argMatches) == 0 {
			continue
		}
		var args []string
		for _, am := range argMatches {
			args = append(args, am[1])
		}
		commands = append(commands, EmbeddedCommand{text: strings.Join(args, " "), argv: args})
		if pythonListUsesShell(code[m[1]:]) {
			// POSIX subprocess with shell=True executes only the first list
			// element as shell source; the rest supply shell positional args.
			commands = append(commands, EmbeddedCommand{text: args[0]})
		}
		// An explicit shell still interprets its -c argument as source.
		// Quoting argv data must not hide those executable substitutions.
		if script, ok := shellArgvScript(args); ok {
			commands = append(commands, EmbeddedCommand{text: script})
		}
	}
	return commands
}

// Inspect only this call's outer keyword arguments. Nested calls and quoted
// ')' bytes must not hide a later shell=True or borrow it from another call.
// Dynamic supplied shell values conservatively retain shell-source inspection.
func pythonListUsesShell(tail string) bool {
	depth, argStart := 0, false
	end := min(len(tail), MaxShellInputBytes)
	tail = tail[:end]
	for n := 0; n < end; n++ {
		ch := tail[n]
		if next := pythonSpaceEnd(tail, n); next != n {
			n = next - 1
			continue
		}
		if depth == 0 && argStart && ch != ')' {
			name, next := pythonIdentifier(tail, n)
			next = pythonSpaceEnd(tail, next)
			if name == "" || next == end || tail[next] != '=' {
				return true // positional arguments or **kwargs may select shell.
			}
			if name == "shell" {
				value, after := pythonIdentifier(tail, pythonSpaceEnd(tail, next+1))
				after = pythonSpaceEnd(tail, after)
				return value != "False" || after == end || tail[after] != ',' && tail[after] != ')'
			}
			argStart = false
			n = next // Resume with this non-shell keyword's value.
			continue
		}
		if ch == '\'' || ch == '"' {
			quote := ch
			triple := n+2 < end && tail[n+1] == quote && tail[n+2] == quote
			if triple {
				n += 2
			}
			for n++; n < end; n++ {
				if tail[n] == '\\' {
					n++
					continue
				}
				if tail[n] == quote {
					if !triple {
						break
					}
					if n+2 < end && tail[n+1] == quote && tail[n+2] == quote {
						n += 2
						break
					}
				}
			}
			argStart = false
			continue
		}
		if strings.ContainsRune("([{", rune(ch)) {
			depth++
			if depth > MaxShellFrames {
				return true
			}
			continue
		}
		if strings.ContainsRune(")]}", rune(ch)) {
			if depth == 0 {
				return false
			}
			depth--
			continue
		}
		if depth != 0 {
			continue
		}
		if ch == ',' {
			argStart = true
			continue
		}
		argStart = false
	}
	// Truncated/ambiguous call tails cannot establish direct-argv execution.
	return true
}

func pythonSpaceEnd(text string, start int) int {
	for start < len(text) {
		if strings.ContainsRune(" \t\r\n", rune(text[start])) {
			start++
			continue
		}
		if text[start] == '#' {
			for start < len(text) && text[start] != '\n' {
				start++
			}
			continue
		}
		if text[start] == '\\' && start+1 < len(text) {
			if text[start+1] == '\n' {
				start += 2
				continue
			}
			if text[start+1] == '\r' && start+2 < len(text) && text[start+2] == '\n' {
				start += 3
				continue
			}
		}
		break
	}
	return start
}

func pythonIdentifier(text string, start int) (string, int) {
	var name strings.Builder
	for start < len(text) {
		ch := text[start]
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch == '_' || name.Len() > 0 && ch >= '0' && ch <= '9' {
			name.WriteByte(ch)
			start++
			continue
		}
		if ch == '\\' {
			if next := pythonSpaceEnd(text, start); next != start {
				start = next
				continue
			}
		}
		break
	}
	return name.String(), start
}

// joinArgs preserves literal argv data when passing it to the shell scanner.
// Keep ordinary words unquoted so existing command-prefix policies retain their
// spelling. Shell metacharacters in direct arguments are data, not new commands.
func joinArgs(args []string) string {
	quoted := make([]string, len(args))
	for n, arg := range args {
		if arg == "" || strings.ContainsAny(arg, " \t\r\n'\"`$;&|<>(){}\\") {
			quoted[n] = "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
		} else {
			quoted[n] = arg
		}
	}
	return strings.Join(quoted, " ")
}

func shellArgvScript(args []string) (string, bool) {
	if len(args) < 3 {
		return "", false
	}
	name := args[0][strings.LastIndex(args[0], "/")+1:]
	if name == "env" {
		// Common direct env invocation: optional assignments before argv.
		args = args[1:]
		for len(args) > 0 && strings.Contains(args[0], "=") {
			args = args[1:]
		}
		return shellArgvScript(args)
	}
	switch name {
	case "sh", "bash", "dash", "zsh", "ksh":
		for n := 1; n+1 < len(args); n++ {
			arg := args[n]
			if arg == "--" || !strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "+") {
				break
			}
			if arg == "-o" || arg == "+o" || arg == "-O" || arg == "+O" || arg == "--rcfile" || arg == "--init-file" {
				n++ // These switches take a separate value before -c.
				continue
			}
			if strings.HasPrefix(arg, "--") || strings.HasPrefix(arg, "+") {
				continue
			}
			if strings.ContainsRune(arg, 'c') {
				return args[n+1], true
			}
		}
	}
	return "", false
}

// ExtractShellCommands scans non-shell code for shell-escape calls and
// extracts the embedded command strings.
//
// Returns an array of command strings found in the code. For unknown
// languages or code without shell-escape calls, returns an empty array.
func ExtractShellCommands(code, language string) []string {
	var commands []string
	for _, command := range ExtractEmbeddedCommands(code, language) {
		commands = append(commands, command.text)
	}
	return commands
}

// EmbeddedCommand retains whether extracted text is shell source or literal
// direct argv. Flattening argv before scanning would execute data as syntax.
type EmbeddedCommand struct {
	text string
	argv []string
}

// ExtractEmbeddedCommands supplies policy evaluators with argument provenance.
// Like the legacy string extractor, this is best-effort static detection, not
// a complete interpreter for the submitted language.
func ExtractEmbeddedCommands(code, language string) []EmbeddedCommand {
	patterns := shellEscapePatterns[language]
	if patterns == nil && language != "python" {
		return nil
	}

	var commands []EmbeddedCommand

	for _, pattern := range patterns {
		matches := pattern.FindAllStringSubmatch(code, -1)
		for _, m := range matches {
			// Each pattern has exactly one capture group for the command.
			command := m[1]
			if command != "" {
				commands = append(commands, EmbeddedCommand{text: command})
			}
		}
	}

	// Python: also extract subprocess list-form args
	if language == "python" {
		commands = append(commands, extractPythonSubprocessListArgs(code)...)
	}

	return commands
}
