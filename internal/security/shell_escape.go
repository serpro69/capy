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
func extractPythonSubprocessListArgs(code string) []string {
	var commands []string
	matches := pythonSubprocessListPattern.FindAllStringSubmatch(code, -1)
	for _, m := range matches {
		listContent := m[1]
		argMatches := pythonListArgPattern.FindAllStringSubmatch(listContent, -1)
		if len(argMatches) == 0 {
			continue
		}
		var args []string
		for _, am := range argMatches {
			args = append(args, am[1])
		}
		commands = append(commands, joinArgs(args))
	}
	return commands
}

// joinArgs joins string arguments with spaces.
func joinArgs(args []string) string {
	return strings.Join(args, " ")
}

// ExtractShellCommands scans non-shell code for shell-escape calls and
// extracts the embedded command strings.
//
// Returns an array of command strings found in the code. For unknown
// languages or code without shell-escape calls, returns an empty array.
func ExtractShellCommands(code, language string) []string {
	patterns := shellEscapePatterns[language]
	if patterns == nil && language != "python" {
		return nil
	}

	var commands []string

	for _, pattern := range patterns {
		matches := pattern.FindAllStringSubmatch(code, -1)
		for _, m := range matches {
			// Each pattern has exactly one capture group for the command.
			command := m[1]
			if command != "" {
				commands = append(commands, command)
			}
		}
	}

	// Python: also extract subprocess list-form args
	if language == "python" {
		commands = append(commands, extractPythonSubprocessListArgs(code)...)
	}

	return commands
}
