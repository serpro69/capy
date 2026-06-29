package vault

import (
	"encoding/json"
	"fmt"
	"strings"
)

// diff.go reconstructs an Edit/Write diff for the TUI viewer (design.md § Addenda
// A3). Unlike Read — whose tool_result.content is a large file dump — an Edit/Write
// tool_result body is a one-line success string ("The file X has been updated
// successfully"); the actual change lives in the sibling top-level `toolUseResult`
// field as a pre-computed unified-diff `structuredPatch`, which the scanner's
// message-only parser never sees. These helpers turn that patch into displayable
// unified-diff text + an add/remove stat. The tui package colors it by line prefix
// (render.go renderDiffBody) — keeping diff TEXT in package vault and diff COLOR in
// the presentation layer, mirroring how transcript text vs. lipgloss styling split.

// diffHunk is one hunk of a Claude Code structuredPatch. Lines carry their unified
// -diff prefix as the first byte ('+' added, '-' removed, ' ' context); a removed
// markdown list item "- foo" therefore appears as "-- foo" (diff '-' + content).
type diffHunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
}

// toolResultPatch is the subset of an Edit/Write `toolUseResult` we render: just the
// structuredPatch. Both tools emit it; every other field (filePath, oldString/
// newString/originalFile for Edit, content/type for Write) is ignored — the marker
// labels from the tool_use call summary, not from here.
type toolResultPatch struct {
	StructuredPatch []diffHunk `json:"structuredPatch"`
}

// diffBodyFromToolResult renders a line's `toolUseResult.structuredPatch` into
// unified-diff text (one `@@ -a,b +c,d @@` header per hunk, then the hunk's prefixed
// lines) and counts added/removed lines for the marker stat. ok is false when the
// raw is empty, not a structuredPatch object, or carries no hunks — the caller then
// falls back to the plain success body. The per-line prefix is preserved verbatim so
// the tui can color by first byte.
func diffBodyFromToolResult(raw json.RawMessage) (body string, added, removed int, ok bool) {
	if len(raw) == 0 {
		return "", 0, 0, false
	}
	var p toolResultPatch
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", 0, 0, false
	}
	if len(p.StructuredPatch) == 0 {
		return "", 0, 0, false
	}
	var b strings.Builder
	emitted := 0 // hunks actually written; a hunk with no lines is skipped (malformed)
	for _, h := range p.StructuredPatch {
		if len(h.Lines) == 0 {
			continue // a header-only hunk would render as a lone "@@" line — drop it
		}
		if emitted > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
		for _, ln := range h.Lines {
			b.WriteByte('\n')
			b.WriteString(ln)
			switch {
			case strings.HasPrefix(ln, "+"):
				added++
			case strings.HasPrefix(ln, "-"):
				removed++
			}
		}
		emitted++
	}
	if emitted == 0 {
		return "", 0, 0, false // every hunk was empty — fall back to the success body
	}
	return b.String(), added, removed, true
}
