package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/serpro69/capy/internal/vault"
)

// renderedTranscript is a styled, viewport-ready transcript: the display rows
// plus, for each source message, the row where its rendering starts. msgRowStart
// is what turns a search hit's line_index into a scroll offset (see rowForLine).
//
// IMPLEMENTATION NOTE — eager render (deliberate v1 deviation from design.md's
// "lazy windowed" viewer). design.md §Read-path performance specifies a lazy
// \n-offset index that unmarshals only visible lines. We instead parse + style
// the whole transcript once here. Rationale: (1) the design's lazy+no-dedup
// combination would re-show every progressive assistant snapshot (3× growing
// copies of one message) — poor UX; deduping requires cross-line state that
// breaks per-line laziness anyway; (2) the eager cost (a transient parse, then a
// retained display string ≈ blob size) equals what `capy vault show` already
// pays, and typical sessions are ≤1MB (design DB projection: 536KB avg main
// JSONL); (3) the search→scroll *contract* (line_index/subagent_id anchors,
// never turn_index) is fully preserved via msgRowStart. Lazy windowing is a
// deferred performance optimization — see the tasks.md follow-up. Revisit if
// profiling shows lag on multi-MB sessions.
type renderedTranscript struct {
	messages    []vault.TranscriptMessage
	rows        []string // display rows (no trailing newline); join with "\n" for the viewport
	msgRowStart []int     // msgRowStart[i] = first row index of messages[i]
	markers     []int     // indices into messages that are openable RoleSubagent markers
}

// renderTranscript styles parsed messages into wrapped display rows for a
// viewport of the given content width. width <= 0 disables wrapping.
func renderTranscript(messages []vault.TranscriptMessage, st Styles, width int) renderedTranscript {
	out := renderedTranscript{messages: messages}
	out.msgRowStart = make([]int, len(messages))

	for i, m := range messages {
		out.msgRowStart[i] = len(out.rows)
		if m.Role == vault.RoleSubagent {
			if m.Openable {
				out.markers = append(out.markers, i)
			}
			out.rows = append(out.rows, st.markerRow(m, false))
			continue
		}
		out.rows = append(out.rows, st.messageHeader(m.Role))
		for _, line := range wrapBody(m.Body, st.Body, width) {
			out.rows = append(out.rows, line)
		}
		out.rows = append(out.rows, "") // blank separator between messages
	}
	return out
}

// content joins the rows into a single string for viewport.SetContent.
func (r renderedTranscript) content() string {
	return strings.Join(r.rows, "\n")
}

// rowForLine maps a source JSONL line index to the row to scroll to: the start
// row of the last message at or before that line (deduped assistant snapshots
// resolve to their canonical first line, so an exact hit lands on the message).
// Returns 0 when there is no match (empty transcript or a line before the first
// message).
func (r renderedTranscript) rowForLine(line int) int {
	row := 0
	for i, m := range r.messages {
		if m.SourceLine <= line {
			row = r.msgRowStart[i]
		} else {
			break
		}
	}
	return row
}

// lineForRow is the inverse of rowForLine: the source JSONL line of the message
// rendered at display row. Used to keep the scroll position stable across a
// re-wrap (terminal resize) — capture the top line before re-rendering, then
// restore the offset via rowForLine afterward. Returns 0 when row precedes the
// first message.
func (r renderedTranscript) lineForRow(row int) int {
	line := 0
	for i, start := range r.msgRowStart {
		if start <= row {
			line = r.messages[i].SourceLine
		} else {
			break
		}
	}
	return line
}

// rowForMarker returns the start row of an openable marker by its position in
// the markers slice, or -1 if out of range.
func (r renderedTranscript) rowForMarker(markerPos int) int {
	if markerPos < 0 || markerPos >= len(r.markers) {
		return -1
	}
	return r.msgRowStart[r.markers[markerPos]]
}

// messageHeader renders a role label line, e.g. "▌ You" / "▌ Claude".
func (s Styles) messageHeader(role string) string {
	label := roleLabel(role)
	return s.roleStyle(role).Render("▌ " + label)
}

// markerRow renders a subagent launch marker. The leading glyph differs by state
// (focused "▶" vs unfocused "▸") so focus is visible even without color (NO_COLOR
// / dumb terminals, where lipgloss strips the highlight style). Focused markers
// are highlighted; openable markers are accented; visible-only markers are dimmed.
func (s Styles) markerRow(m vault.TranscriptMessage, focused bool) string {
	label := "subagent: " + m.Body
	if m.Openable && m.AgentID != "" {
		label = "subagent " + shortID(m.AgentID) + ": " + m.Body
	}
	switch {
	case focused:
		return s.MarkerFocused.Render("▶ " + label + "  (enter to open)")
	case m.Openable:
		return s.MarkerOpenable.Render("▸ " + label + "  (enter to open)")
	default:
		return s.Marker.Render("▸ " + label)
	}
}

// roleLabel is the human label for a display/search role.
func roleLabel(role string) string {
	switch role {
	case "user":
		return "You"
	case "assistant":
		return "Claude"
	case "tool":
		return "Tool result"
	case "subagent":
		return "Subagent"
	default:
		return "System"
	}
}

// wrapBody wraps body text to width using lipgloss (which counts display width
// correctly for wide/!ASCII runes), then splits into individual rows so the
// caller can index them. width <= 0 leaves the text unwrapped.
func wrapBody(body string, style lipgloss.Style, width int) []string {
	if width > 0 {
		body = style.Width(width).Render(body)
	} else {
		body = style.Render(body)
	}
	return strings.Split(body, "\n")
}

// shortID trims a long agent id for compact display.
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
