package config

import (
	"fmt"
	"strings"
	"unicode"
)

// parseDotenvKey is a literal, single-line compatibility reader, not a shell.
// Files without a target declaration are irrelevant, even if their application
// syntax is unsupported. Once a candidate exists, validate the entire file so
// an embedded declaration or malformed trailing input cannot yield a key.
func parseDotenvKey(data []byte, path string) (string, bool, error) {
	lines := strings.Split(string(data), "\n")
	candidate := false
	for _, line := range lines {
		name, _ := dotenvName(line)
		if name == "CAPY_DB_KEY" {
			candidate = true
			break
		}
	}
	if !candidate {
		return "", false, nil
	}

	var key string
	found := false
	for i, line := range lines {
		if i < len(lines)-1 {
			line = strings.TrimSuffix(line, "\r") // CRLF, not a bare CR
		}
		line = strings.TrimLeft(line, " \t")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, rest := dotenvName(line)
		rest = strings.TrimLeft(rest, " \t")
		if name == "" || !strings.HasPrefix(rest, "=") {
			return "", false, dotenvError(path, i+1, "expected a single-line variable assignment")
		}
		target := name == "CAPY_DB_KEY"
		if target && found {
			return "", false, dotenvError(path, i+1, "duplicate CAPY_DB_KEY declaration")
		}
		value, reason := dotenvValue(rest[1:], target)
		if reason != "" {
			return "", false, dotenvError(path, i+1, reason)
		}
		if target {
			if value == "" {
				return "", false, dotenvError(path, i+1, "CAPY_DB_KEY passphrase is empty")
			}
			key, found = value, true
		}
	}
	return key, found, nil
}

func dotenvError(path string, line int, reason string) error {
	// Only fixed reasons reach this formatter; never include an input line/value.
	return fmt.Errorf("dotenv %q line %d: %s; configure store.key_file with a literal passphrase file", path, line, reason)
}

// dotenvName also recognizes malformed target assignments (missing '=', '+='),
// while leaving prefixed/misspelled variable names and comments unrelated.
func dotenvName(line string) (string, string) {
	line = strings.TrimLeft(line, " \t")
	if strings.HasPrefix(line, "export ") || strings.HasPrefix(line, "export\t") {
		line = strings.TrimLeft(line[len("export"):], " \t")
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return line[:i], line[i:]
	}
	return line, ""
}

func dotenvValue(input string, target bool) (string, string) {
	value := strings.TrimLeft(input, " \t")
	if value == "" || len(value) < len(input) && strings.HasPrefix(value, "#") {
		return "", ""
	}
	if value[0] == '\'' || value[0] == '"' {
		return dotenvQuotedValue(value, target)
	}

	end := strings.IndexAny(value, " \t")
	if end >= 0 {
		if !dotenvCommentOrEnd(value[end:]) {
			return "", "unquoted values cannot contain whitespace or trailing statements"
		}
		value = value[:end]
	}
	for _, c := range value {
		if unicode.IsSpace(c) || strings.ContainsRune("\x00'\"\\;&|<>()`", c) {
			return "", "unsupported unquoted value syntax"
		}
		if target && c == '$' {
			return "", "CAPY_DB_KEY expansion syntax is not supported"
		}
	}
	return value, ""
}

func dotenvQuotedValue(input string, target bool) (string, string) {
	quote := input[0]
	var value strings.Builder
	for i := 1; i < len(input); i++ {
		c := input[i]
		if c == quote {
			if !dotenvCommentOrEnd(input[i+1:]) {
				return "", "unexpected input after quoted value"
			}
			return value.String(), ""
		}
		if c == 0 || c == '\r' || c == '\n' {
			return "", "values cannot contain NUL or line endings"
		}
		if quote == '"' {
			if target && (c == '$' || c == '`') {
				return "", "CAPY_DB_KEY expansion syntax is not supported"
			}
			if c == '\\' {
				i++
				if i == len(input) {
					return "", "unterminated quoted value"
				}
				c = input[i]
				// Non-target escapes are consumed only to validate quote structure;
				// those values are discarded, never expanded or exported.
				if target && c != '\\' && c != '"' {
					return "", "unsupported CAPY_DB_KEY escape"
				}
				if c == 0 || c == '\r' || c == '\n' {
					return "", "values cannot contain NUL or line endings"
				}
			}
		}
		value.WriteByte(c)
	}
	return "", "unterminated quoted value"
}

func dotenvCommentOrEnd(input string) bool {
	input = strings.TrimLeft(input, " \t")
	return input == "" || strings.HasPrefix(input, "#")
}
