package security

import (
	"fmt"
	"strings"
)

const (
	// MaxShellInputBytes bounds each command submitted for evaluation.
	MaxShellInputBytes = 1 << 20
	// MaxShellFrames bounds simultaneously active scanner contexts.
	MaxShellFrames = 64
	// MaxShellElements bounds executable elements emitted by one scan.
	MaxShellElements = 4096
)

// ShellScanLimitError means policy evaluation could not inspect the entire
// command. Callers must block execution, even when no policies are configured.
type ShellScanLimitError struct {
	Limit   string
	Maximum int
}

func (e *ShellScanLimitError) Error() string {
	return fmt.Sprintf("shell scanner %s limit exceeded (maximum %d)", e.Limit, e.Maximum)
}

// SplitChainedCommands returns executable elements, including substitutions.
// Quoted literals and heredoc data are not commands. This is a bounded static
// scanner, not a shell interpreter; it does not resolve aliases or dynamic code.
// On failure it returns no partial list.
func SplitChainedCommands(command string) ([]string, error) {
	if len(command) > MaxShellInputBytes {
		return nil, &ShellScanLimitError{Limit: "input bytes", Maximum: MaxShellInputBytes}
	}
	s := shellScanner{text: command, budget: 8*len(command) + 4096}
	if _, err := s.commands(0, len(command), 0, 0); err != nil {
		return nil, err
	}
	return s.elements, nil
}

type shellScanner struct {
	text     string
	elements []string
	budget   int
	visits   int
}

func (s *shellScanner) visit(n int) error {
	s.visits += n
	if s.visits > s.budget {
		return &ShellScanLimitError{Limit: "visits", Maximum: s.budget}
	}
	return nil
}

func (s *shellScanner) frame(depth int) error {
	if depth > MaxShellFrames {
		return &ShellScanLimitError{Limit: "frames", Maximum: MaxShellFrames}
	}
	return nil
}

func (s *shellScanner) emit(start, end int) error {
	// Charge overlapping parent spans too: nested substitutions must not cause
	// unbounded aggregate trimming/matching work despite a small input.
	if err := s.visit(end - start); err != nil {
		return err
	}
	element := strings.TrimSpace(s.text[start:end])
	if element == "" {
		return nil
	}
	if len(s.elements) == MaxShellElements {
		return &ShellScanLimitError{Limit: "elements", Maximum: MaxShellElements}
	}
	s.elements = append(s.elements, element)
	return nil
}

type shellHeredoc struct {
	delimiter string
	quoted    bool
	stripTabs bool
}

// commands walks one command context. A recursive call consumes its closing
// delimiter; the outer span still retains the original substitution spelling.
func (s *shellScanner) commands(start, end int, stop byte, depth int) (int, error) {
	if err := s.frame(depth); err != nil {
		return start, err
	}
	segment := start
	atWordStart := true
	var pending []shellHeredoc
	for i := start; i < end; {
		if err := s.visit(1); err != nil {
			return i, err
		}
		ch := s.text[i]
		wasWordStart := atWordStart
		atWordStart = ch == ' ' || ch == '\t'
		if stop != 0 && ch == stop {
			return i + 1, s.emit(segment, i)
		}
		if ch == '\\' {
			if i+1 < end && s.text[i+1] == '\n' {
				atWordStart = wasWordStart
			}
			i += min(2, end-i) // consuming pairs handles escape parity
			continue
		}
		ansiQuote := ch == '$' && i+1 < end && s.text[i+1] == '\''
		if ch == '\'' || ch == '"' || ansiQuote {
			quoteStart := i
			if ansiQuote {
				quoteStart++
			}
			next, err := s.quote(quoteStart, end, depth+len(pending)+1, ansiQuote)
			if err != nil {
				return i, err
			}
			i = next
			continue
		}
		if next, ok, err := s.expansion(i, end, depth+len(pending), false); ok {
			if err != nil {
				return i, err
			}
			i = next
			continue
		}
		// Comments start at a token boundary, not inside a word.
		if ch == '#' && wasWordStart {
			if err := s.emit(segment, i); err != nil {
				return i, err
			}
			for i < end && s.text[i] != '\n' && s.text[i] != '\r' {
				if err := s.visit(1); err != nil {
					return i, err
				}
				i++
			}
			segment = i
			continue
		}
		if ch == '<' && i+1 < end && s.text[i+1] == '<' {
			if i+2 < end && s.text[i+2] == '<' { // here-string; its quotes/expansions are normal
				i += 3
				continue
			}
			if err := s.frame(depth + len(pending) + 1); err != nil {
				return i, err
			}
			h, next, err := s.heredocWord(i+2, end)
			if err != nil {
				return i, err
			}
			pending = append(pending, h)
			i = next
			continue
		}
		// Descriptor duplication and &> redirection are not command boundaries.
		if (ch == '<' || ch == '>') && i+1 < end && s.text[i+1] == '&' {
			i += 2
			continue
		}
		if ch == '&' && i+1 < end && s.text[i+1] == '>' {
			i += 2
			continue
		}
		if ch == '(' {
			if err := s.emit(segment, i); err != nil {
				return i, err
			}
			var next int
			var err error
			if i+1 < end && s.text[i+1] == '(' {
				next, err = s.arithmetic(i+2, end, depth+len(pending)+1)
			} else {
				next, err = s.commands(i+1, end, ')', depth+len(pending)+1)
			}
			if err != nil {
				return i, err
			}
			i, segment = next, next
			atWordStart = true
			continue
		}
		if strings.ContainsRune(";|&\n\r", rune(ch)) {
			if err := s.emit(segment, i); err != nil {
				return i, err
			}
			i++
			if i < end && ((ch == '&' || ch == '|') && s.text[i] == ch || ch == '|' && s.text[i] == '&' || ch == '\r' && s.text[i] == '\n') {
				i++
			}
			if ch == '\n' || ch == '\r' {
				for _, h := range pending {
					next, err := s.heredocBody(i, end, h, depth+len(pending))
					if err != nil {
						return i, err
					}
					i = next
				}
				pending = nil
			}
			segment = i
			atWordStart = true
			continue
		}
		i++
	}
	if stop != 0 {
		return end, fmt.Errorf("unterminated shell substitution or group")
	}
	return end, s.emit(segment, end)
}

func (s *shellScanner) quote(start, end, depth int, ansi bool) (int, error) {
	if err := s.frame(depth); err != nil {
		return start, err
	}
	quote := s.text[start]
	for i := start + 1; i < end; {
		if err := s.visit(1); err != nil {
			return i, err
		}
		if s.text[i] == quote {
			return i + 1, nil
		}
		if ansi && s.text[i] == '\\' {
			i += min(2, end-i)
			continue
		}
		if quote == '"' {
			if s.text[i] == '\\' && i+1 < end && strings.ContainsRune("$`\"\\\n", rune(s.text[i+1])) {
				i += 2
				continue
			}
			if next, ok, err := s.expansion(i, end, depth, true); ok {
				if err != nil {
					return i, err
				}
				i = next
				continue
			}
		}
		i++
	}
	return end, fmt.Errorf("unterminated shell quote")
}

func (s *shellScanner) expansion(i, end, depth int, doubleQuoted bool) (int, bool, error) {
	if s.text[i] == '$' && i+1 < end && s.text[i+1] == '{' {
		next, err := s.parameter(i+2, end, depth+1, doubleQuoted)
		return next, true, err
	}
	if s.text[i] == '`' {
		next, err := s.backtick(i+1, end, depth+1)
		return next, true, err
	}
	if s.text[i] == '$' && i+1 < end && s.text[i+1] == '(' {
		if i+2 < end && s.text[i+2] == '(' {
			next, err := s.arithmetic(i+3, end, depth+1)
			return next, true, err
		}
		next, err := s.commands(i+2, end, ')', depth+1)
		return next, true, err
	}
	return i, false, nil
}

// Parameter expansion words can contain parentheses and separators as data,
// but their nested command substitutions still execute. Keep their delimiters
// out of the enclosing command/quote context.
func (s *shellScanner) parameter(start, end, depth int, doubleQuoted bool) (int, error) {
	if err := s.frame(depth); err != nil {
		return start, err
	}
	for i := start; i < end; {
		if err := s.visit(1); err != nil {
			return i, err
		}
		if s.text[i] == '}' {
			return i + 1, nil
		}
		if s.text[i] == '\\' {
			i += min(2, end-i)
			continue
		}
		ansi := !doubleQuoted && s.text[i] == '$' && i+1 < end && s.text[i+1] == '\''
		if (s.text[i] == '\'' && !doubleQuoted) || s.text[i] == '"' || ansi {
			quoteStart := i
			if ansi {
				quoteStart++
			}
			next, err := s.quote(quoteStart, end, depth+1, ansi)
			if err != nil {
				return i, err
			}
			i = next
			continue
		}
		if next, ok, err := s.expansion(i, end, depth, doubleQuoted); ok {
			if err != nil {
				return i, err
			}
			i = next
			continue
		}
		i++
	}
	return end, fmt.Errorf("unterminated shell parameter expansion")
}

// Legacy backticks remove one level of escaping before parsing their command.
// In particular, \` introduces a nested substitution in that decoded command.
func (s *shellScanner) backtick(start, end, depth int) (int, error) {
	if err := s.frame(depth); err != nil {
		return start, err
	}
	var decoded strings.Builder
	for i := start; i < end; i++ {
		if err := s.visit(1); err != nil {
			return i, err
		}
		ch := s.text[i]
		if ch == '`' {
			original := s.text
			s.text = decoded.String()
			_, err := s.commands(0, len(s.text), 0, depth)
			s.text = original
			return i + 1, err
		}
		if ch == '\\' && i+1 < end && strings.ContainsRune("$`\\", rune(s.text[i+1])) {
			i++
			ch = s.text[i]
		}
		decoded.WriteByte(ch)
	}
	return end, fmt.Errorf("unterminated shell backtick substitution")
}

// Arithmetic operators and grouping are data; only substitutions execute.
func (s *shellScanner) arithmetic(start, end, depth int) (int, error) {
	if err := s.frame(depth); err != nil {
		return start, err
	}
	parens := 0
	for i := start; i < end; {
		if err := s.visit(1); err != nil {
			return i, err
		}
		if s.text[i] == '\\' {
			i += min(2, end-i)
			continue
		}
		if next, ok, err := s.expansion(i, end, depth+parens, false); ok {
			if err != nil {
				return i, err
			}
			i = next
			continue
		}
		if s.text[i] == '(' {
			parens++
			if err := s.frame(depth + parens); err != nil {
				return i, err
			}
		}
		if s.text[i] == ')' {
			if parens == 0 && i+1 < end && s.text[i+1] == ')' {
				return i + 2, nil
			}
			parens--
		}
		i++
	}
	return end, fmt.Errorf("unterminated shell arithmetic")
}

func (s *shellScanner) heredocWord(start, end int) (shellHeredoc, int, error) {
	h := shellHeredoc{}
	i := start
	if i < end && s.text[i] == '-' {
		h.stripTabs = true
		i++
	}
	for i < end && (s.text[i] == ' ' || s.text[i] == '\t') {
		i++
	}
	var word strings.Builder
	var quote byte
	for i < end {
		ch := s.text[i]
		if quote == 0 && ch == '$' && i+1 < end && (s.text[i+1] == '\'' || s.text[i+1] == '"') {
			// ANSI-C/localized quote removal can change the delimiter bytes.
			// Until that grammar is supported, fail closed rather than consume
			// subsequent executable lines as a misidentified literal body.
			return h, i, fmt.Errorf("unsupported shell heredoc delimiter quoting: use ordinary single or double quotes")
		}
		if ch == '\\' && quote != '\'' && i+1 < end && s.text[i+1] == '\n' {
			// Line continuation is removed before quote removal; it does not
			// make an otherwise unquoted delimiter literal.
			i += 2
			continue
		}
		if quote == 0 && strings.ContainsRune(" \t\r\n;|&<>()", rune(ch)) {
			break
		}
		if ch == quote {
			quote = 0
		} else if quote == 0 && (ch == '\'' || ch == '"') {
			h.quoted, quote = true, ch
		} else if ch == '\\' && quote != '\'' && i+1 < end && (quote == 0 || strings.ContainsRune("$`\"\\\n", rune(s.text[i+1]))) {
			h.quoted = true
			i++
			word.WriteByte(s.text[i])
		} else {
			word.WriteByte(ch)
		}
		i++
	}
	if err := s.visit(i - start); err != nil {
		return h, i, err
	}
	if quote != 0 || (word.Len() == 0 && !h.quoted) {
		return h, i, fmt.Errorf("invalid shell heredoc delimiter")
	}
	h.delimiter = word.String()
	return h, i, nil
}

func (s *shellScanner) heredocBody(start, end int, h shellHeredoc, depth int) (int, error) {
	next := end
	var body strings.Builder
	for i := start; i < end; {
		lineStart := i
		var logical strings.Builder
		for i < end && s.text[i] != '\n' {
			// Unquoted heredocs join backslash-newline before testing their
			// delimiter and performing expansions. Consume pairs for parity.
			if !h.quoted && s.text[i] == '\\' && i+1 < end {
				if s.text[i+1] != '\n' {
					logical.WriteString(s.text[i : i+2])
				}
				i += 2
				continue
			}
			logical.WriteByte(s.text[i])
			i++
		}
		if err := s.visit(i - lineStart + 1); err != nil {
			return i, err
		}
		line := logical.String()
		if h.stripTabs {
			line = strings.TrimLeft(line, "\t")
		}
		i = min(i+1, end)
		if line == h.delimiter {
			next = i
			break
		}
		if !h.quoted {
			body.WriteString(line)
			body.WriteByte('\n')
		}
	}
	if h.quoted {
		return next, nil
	}
	// Reuse the same accounting and elements while scanning the joined body.
	original := s.text
	s.text = body.String()
	defer func() { s.text = original }()
	start, bodyEnd := 0, len(s.text)
	for i := start; i < bodyEnd; {
		if err := s.visit(1); err != nil {
			return i, err
		}
		if s.text[i] == '\\' && i+1 < bodyEnd && strings.ContainsRune("$`\\\n", rune(s.text[i+1])) {
			i += 2
			continue
		}
		if n, ok, err := s.expansion(i, bodyEnd, depth, true); ok {
			if err != nil {
				return i, err
			}
			i = n
			continue
		}
		i++
	}
	return next, nil
}
