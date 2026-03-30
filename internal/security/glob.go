package security

import (
	"regexp"
	"strings"
	"sync"
)

// .+ is greedy: for "Bash(echo (foo))" it captures "echo (foo)"
// because $ forces the final \) to match only the last paren.
var parseBashRe = regexp.MustCompile(`^Bash\((.+)\)$`)
var parseToolRe = regexp.MustCompile(`^(\w+)\((.+)\)$`)

// parseBashPattern extracts the glob from a Bash permission pattern.
// "Bash(sudo *)" returns "sudo *"; "Read(.env)" returns "".
func parseBashPattern(pattern string) string {
	m := parseBashRe.FindStringSubmatch(pattern)
	if m == nil {
		return ""
	}
	return m[1]
}

// parseToolPattern parses any tool permission pattern like "ToolName(glob)".
// Returns tool name and glob, or empty strings if not a valid pattern.
func parseToolPattern(pattern string) (tool, glob string) {
	m := parseToolRe.FindStringSubmatch(pattern)
	if m == nil {
		return "", ""
	}
	return m[1], m[2]
}

// escapeRegex escapes all regex special characters.
func escapeRegex(s string) string {
	special := `.*+?^${}()|[]\/-`
	var b strings.Builder
	for _, ch := range s {
		if strings.ContainsRune(special, ch) {
			b.WriteByte('\\')
		}
		b.WriteRune(ch)
	}
	return b.String()
}

// convertGlobPart escapes regex specials except *, then converts * to .*
func convertGlobPart(glob string) string {
	special := `.+?^${}()|[]\/-`
	var b strings.Builder
	for _, ch := range glob {
		if ch == '*' {
			b.WriteString(".*")
		} else {
			if strings.ContainsRune(special, ch) {
				b.WriteByte('\\')
			}
			b.WriteRune(ch)
		}
	}
	return b.String()
}

// regexCache caches compiled glob regexes to avoid recompilation per call.
var regexCache sync.Map

// globToRegex converts a Bash permission glob to a regex.
//
// Two formats:
//   - Colon: "tree:*" becomes /^tree(\s.*)?$/ (command with optional args)
//   - Space: "sudo *" becomes /^sudo .*$/ (literal glob match)
//
// Results are cached to avoid recompilation in hot paths.
func globToRegex(glob string, caseInsensitive bool) *regexp.Regexp {
	key := glob
	if caseInsensitive {
		key += "|i"
	}
	if cached, ok := regexCache.Load(key); ok {
		return cached.(*regexp.Regexp)
	}

	var regexStr string

	if command, argsGlob, ok := strings.Cut(glob, ":"); ok {
		escapedCmd := escapeRegex(command)
		argsRegex := convertGlobPart(argsGlob)
		regexStr = `^` + escapedCmd + `(\s` + argsRegex + `)?$`
	} else {
		regexStr = `^` + convertGlobPart(glob) + `$`
	}

	if caseInsensitive {
		regexStr = "(?i)" + regexStr
	}
	re := regexp.MustCompile(regexStr)
	regexCache.Store(key, re)
	return re
}

// fileRegexCache caches compiled file glob regexes.
var fileRegexCache sync.Map

// fileGlobToRegex converts a file path glob to a regex.
//
// Unlike globToRegex (which handles command patterns), this handles file path
// globs where:
//   - ** matches any number of path segments (including zero)
//   - * matches anything except path separators
//   - ? matches a single non-separator character
//
// Results are cached to avoid recompilation in hot paths.
func fileGlobToRegex(glob string, caseInsensitive bool) *regexp.Regexp {
	key := glob
	if caseInsensitive {
		key += "|i"
	}
	if cached, ok := fileRegexCache.Load(key); ok {
		return cached.(*regexp.Regexp)
	}

	var b strings.Builder
	i := 0

	for i < len(glob) {
		if glob[i] == '*' && i+1 < len(glob) && glob[i+1] == '*' {
			// **/ at the start or after a slash means "zero or more directories"
			if i+2 < len(glob) && glob[i+2] == '/' {
				b.WriteString("(.*/)?")
				i += 3
			} else {
				// Trailing ** matches everything
				b.WriteString(".*")
				i += 2
			}
		} else if glob[i] == '*' {
			b.WriteString("[^/]*")
			i++
		} else if glob[i] == '?' {
			b.WriteString("[^/]")
			i++
		} else {
			// Escape regex-special characters
			ch := string(glob[i])
			if strings.ContainsAny(ch, `.+^${}()|[]\/-`) {
				b.WriteByte('\\')
			}
			b.WriteByte(glob[i])
			i++
		}
	}

	regexStr := `^` + b.String() + `$`
	if caseInsensitive {
		regexStr = "(?i)" + regexStr
	}
	re := regexp.MustCompile(regexStr)
	fileRegexCache.Store(key, re)
	return re
}

// matchesAnyBashPattern checks if a command matches any Bash pattern in the list.
// Returns the matching pattern string, or empty string if no match.
func matchesAnyBashPattern(command string, patterns []string, caseInsensitive bool) string {
	for _, pattern := range patterns {
		glob := parseBashPattern(pattern)
		if glob == "" {
			continue
		}
		if globToRegex(glob, caseInsensitive).MatchString(command) {
			return pattern
		}
	}
	return ""
}
