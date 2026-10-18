package security

import (
	"fmt"
	"strings"
)

// shellWord preserves whether quote removal produced a literal argument. It
// never expands variables, globs, substitutions, ANSI-C strings or aliases.
type shellWord struct {
	value      string
	literal    bool
	assignment bool
	start      int
}

// literalShellWords consumes one element already checked by shellScanner. Reuse
// its expansion grammar to skip dynamic words without splitting their contents
// into argv. Executable substitutions were emitted separately by that scanner.
func literalShellWords(element string) ([]shellWord, error) {
	s := shellScanner{text: element, budget: 8*len(element) + 4096}
	var words []shellWord
	for i := 0; i < len(element); {
		if strings.ContainsRune(" \t\r\n", rune(element[i])) {
			i++
			continue
		}
		if element[i] == '#' {
			break
		}
		// Redirections are not arguments. Their operands may contain spaces,
		// quotes and substitutions, so consume them with the same word reader.
		redirect := false
		j := i
		for j < len(element) && element[j] >= '0' && element[j] <= '9' {
			j++
		}
		if j < len(element) && strings.ContainsRune("<>", rune(element[j])) || strings.HasPrefix(element[i:], "&>") {
			redirect = true
			i = j
			operator := i
			for i < len(element) && strings.ContainsRune("<>&", rune(element[i])) {
				i++
			}
			if element[operator:i] == "<<" && i < len(element) && element[i] == '-' {
				i++ // <<- strips heredoc tabs; >&- instead has '-' as its operand.
			}
			for i < len(element) && strings.ContainsRune(" \t", rune(element[i])) {
				i++
			}
		}
		if i == len(element) {
			break
		}
		word := shellWord{start: i, literal: true}
		var value strings.Builder
		var quote byte
		for i < len(element) {
			ch := element[i]
			if quote == 0 && strings.ContainsRune(" \t\r\n<>", rune(ch)) {
				break
			}
			if ch == '\\' && quote != '\'' {
				if i+1 == len(element) {
					return nil, fmt.Errorf("unfinished shell escape")
				}
				next := element[i+1]
				if quote == 0 || strings.ContainsRune("$`\"\\\n", rune(next)) {
					if next != '\n' {
						value.WriteByte(next)
					}
					i += 2
					continue
				}
			}
			if ch == quote {
				quote = 0
				i++
				continue
			}
			if quote == 0 && (ch == '\'' || ch == '"') {
				quote = ch
				i++
				continue
			}
			if quote != '\'' {
				if quote == 0 && ch == '(' {
					array, err := s.arrayAssignment(i)
					if err != nil {
						return nil, err
					}
					if array {
						next, err := s.arrayData(i+1, len(element), 1)
						if err != nil {
							return nil, err
						}
						word.literal, i = false, next
						continue
					}
				}
				if quote == 0 && ch == '$' && i+1 < len(element) && (element[i+1] == '\'' || element[i+1] == '"') {
					next, err := s.quote(i+1, len(element), 1, element[i+1] == '\'')
					if err != nil {
						return nil, err
					}
					word.literal, i = false, next
					continue
				}
				if next, ok, err := s.expansion(i, len(element), 0, quote == '"'); ok {
					if err != nil {
						return nil, err
					}
					word.literal, i = false, next
					continue
				}
				if ch == '$' || quote == 0 && strings.ContainsRune("*?[]{}~", rune(ch)) {
					word.literal = false
				}
			}
			value.WriteByte(ch)
			i++
		}
		if quote != 0 {
			return nil, fmt.Errorf("unterminated shell quote")
		}
		word.value = value.String()
		word.assignment = shellAssignment(element[word.start:i])
		if !redirect {
			if len(words) == MaxShellElements {
				return nil, &ShellScanLimitError{Limit: "words", Maximum: MaxShellElements}
			}
			words = append(words, word)
		}
	}
	return words, nil
}
