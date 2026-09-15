package vault

import (
	"fmt"
	"strings"
)

// codex_patch.go converts Codex's apply_patch input — the `*** Begin Patch …
// *** End Patch` text a custom_tool_call carries — into the unified-diff text
// the transcript model's Diff field holds (the Codex analogue of diff.go, whose
// Claude counterpart renders toolUseResult.structuredPatch). The Codex format
// (openai/codex codex-rs/apply-patch) is:
//
//	*** Begin Patch
//	*** Add File: <path>          followed by `+` lines only
//	*** Delete File: <path>       no body
//	*** Update File: <path>       optionally `*** Move to: <path>`, then hunks:
//	@@ [context hint]             one or more, each with ' ' / '-' / '+' lines
//	*** End of File               optional hunk trailer
//	*** End Patch
//
// Update hunks carry no line numbers, so the converter cannot fabricate a
// `@@ -a,b +c,d @@` header for them: it keeps Codex's own `@@ [hint]` line
// verbatim (it starts with `@@`, which is all the TUI's renderDiffBody keys on)
// and keeps the `*** … File:` headers as plain section lines so a multi-file
// patch stays legible. Add File sections, whose numbers ARE known, get an exact
// `@@ -0,0 +1,N @@` header. Per-line prefixes are preserved verbatim, as in
// diff.go, so the TUI can colour by first byte.

const (
	codexPatchBegin      = "*** Begin Patch"
	codexPatchEnd        = "*** End Patch"
	codexPatchAddFile    = "*** Add File: "
	codexPatchDeleteFile = "*** Delete File: "
	codexPatchUpdateFile = "*** Update File: "
	codexPatchMoveTo     = "*** Move to: "
	codexPatchEOF        = "*** End of File"
)

// codexPatchFiles lists the file paths an apply_patch input names, in patch
// order, from its Add / Update / Delete File headers. It is a lenient line scan
// (no grammar check) so a summary can still be built for a malformed patch;
// a Move keeps the Update path (the summary names what was edited).
func codexPatchFiles(patch string) []string {
	var files []string
	for _, line := range strings.Split(patch, "\n") {
		line = strings.TrimRight(line, "\r")
		for _, prefix := range []string{codexPatchAddFile, codexPatchUpdateFile, codexPatchDeleteFile} {
			if strings.HasPrefix(line, prefix) {
				if p := strings.TrimSpace(strings.TrimPrefix(line, prefix)); p != "" {
					files = append(files, p)
				}
				break
			}
		}
	}
	return files
}

// codexPatchToDiff converts an apply_patch input into unified-diff text plus
// its add/remove counts. ok is false for a malformed patch: a missing Begin or
// End marker, content outside a file section, a non-`+` line under Add File, a
// body line under Update File before its first hunk, a hunk line with an
// unknown prefix, or an unknown `***` directive. A patch that parses but
// changes nothing (e.g. only a Delete File) still returns ok=true with empty
// counts — the change is real even though no line is added or removed.
func codexPatchToDiff(patch string) (text string, added, removed int, ok bool) {
	lines := strings.Split(strings.TrimRight(patch, "\n"), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}
	// Skip leading blank lines; the first real line must be the Begin marker.
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	if start >= len(lines) || lines[start] != codexPatchBegin {
		return "", 0, 0, false
	}

	type section int
	const (
		secNone section = iota
		secAdd
		secDelete
		secUpdate
	)
	var out []string
	cur := secNone
	inHunk := false
	// Pending Add File state: header emitted lazily once the line count is known.
	var addHeader string
	var addLines []string
	flushAdd := func() {
		if cur != secAdd {
			return
		}
		out = append(out, addHeader)
		if len(addLines) > 0 {
			out = append(out, fmt.Sprintf("@@ -0,0 +1,%d @@", len(addLines)))
			out = append(out, addLines...)
		}
		addLines = nil
	}

	ended := false
	for _, line := range lines[start+1:] {
		if ended {
			if strings.TrimSpace(line) != "" {
				return "", 0, 0, false // content after End Patch
			}
			continue
		}
		switch {
		case line == codexPatchEnd:
			flushAdd()
			ended = true

		case strings.HasPrefix(line, codexPatchAddFile):
			flushAdd()
			cur, inHunk = secAdd, false
			addHeader = line

		case strings.HasPrefix(line, codexPatchDeleteFile):
			flushAdd()
			cur, inHunk = secDelete, false
			out = append(out, line)

		case strings.HasPrefix(line, codexPatchUpdateFile):
			flushAdd()
			cur, inHunk = secUpdate, false
			out = append(out, line)

		case strings.HasPrefix(line, codexPatchMoveTo):
			if cur != secUpdate || inHunk {
				return "", 0, 0, false
			}
			out = append(out, line)

		case line == codexPatchEOF:
			if cur != secUpdate || !inHunk {
				return "", 0, 0, false
			}

		case strings.HasPrefix(line, "***"):
			return "", 0, 0, false // unknown directive

		case strings.HasPrefix(line, "@@"):
			if cur != secUpdate {
				return "", 0, 0, false
			}
			inHunk = true
			out = append(out, line)

		default:
			switch cur {
			case secAdd:
				if !strings.HasPrefix(line, "+") {
					return "", 0, 0, false
				}
				addLines = append(addLines, line)
				added++
			case secUpdate:
				if !inHunk {
					return "", 0, 0, false
				}
				switch {
				case strings.HasPrefix(line, "+"):
					added++
				case strings.HasPrefix(line, "-"):
					removed++
				case line == "" || strings.HasPrefix(line, " "):
					// context line (an empty line is a blank context line)
				default:
					return "", 0, 0, false
				}
				out = append(out, line)
			default:
				// A body line under Delete File, or before any file header.
				return "", 0, 0, false
			}
		}
	}
	if !ended {
		return "", 0, 0, false
	}
	return strings.Join(out, "\n"), added, removed, true
}
