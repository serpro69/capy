package security

// SplitChainedCommands splits a shell command on chain operators (&&, ||, ;, |)
// while respecting single/double quotes and backticks.
//
// "echo hello && sudo rm -rf /" → ["echo hello", "sudo rm -rf /"]
//
// This prevents bypassing deny patterns by prepending innocent commands.
func SplitChainedCommands(command string) []string {
	var parts []string
	var current []byte
	inSingle := false
	inDouble := false
	inBacktick := false

	for i := 0; i < len(command); i++ {
		ch := command[i]
		prev := byte(0)
		if i > 0 {
			prev = command[i-1]
		}

		switch {
		case ch == '\'' && !inDouble && !inBacktick && prev != '\\':
			inSingle = !inSingle
			current = append(current, ch)
		case ch == '"' && !inSingle && !inBacktick && prev != '\\':
			inDouble = !inDouble
			current = append(current, ch)
		case ch == '`' && !inSingle && !inDouble && prev != '\\':
			inBacktick = !inBacktick
			current = append(current, ch)
		case !inSingle && !inDouble && !inBacktick:
			switch {
			case ch == ';':
				if s := trimBytes(current); len(s) > 0 {
					parts = append(parts, s)
				}
				current = current[:0]
			case ch == '|' && i+1 < len(command) && command[i+1] == '|':
				if s := trimBytes(current); len(s) > 0 {
					parts = append(parts, s)
				}
				current = current[:0]
				i++ // skip second |
			case ch == '&' && i+1 < len(command) && command[i+1] == '&':
				if s := trimBytes(current); len(s) > 0 {
					parts = append(parts, s)
				}
				current = current[:0]
				i++ // skip second &
			case ch == '|':
				// Single pipe — left side is a command too
				if s := trimBytes(current); len(s) > 0 {
					parts = append(parts, s)
				}
				current = current[:0]
			default:
				current = append(current, ch)
			}
		default:
			current = append(current, ch)
		}
	}

	if s := trimBytes(current); len(s) > 0 {
		parts = append(parts, s)
	}
	return parts
}

// trimBytes trims spaces and tabs (not newlines) from a byte slice.
// Intentionally narrower than strings.TrimSpace for shell command context.
func trimBytes(b []byte) string {
	start := 0
	end := len(b)
	for start < end && (b[start] == ' ' || b[start] == '\t') {
		start++
	}
	for end > start && (b[end-1] == ' ' || b[end-1] == '\t') {
		end--
	}
	if start >= end {
		return ""
	}
	return string(b[start:end])
}
