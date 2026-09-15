package vault

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
)

// render.go is the `capy vault show` consumer over the transcript model: it
// decodes a session's archived bytes with the platform's Decoder (DecoderFor)
// and formats the resulting entries as a human-readable transcript. It owns
// only DISPLAY policy — the decoder (claude_decoder.go, …) owns the format work
// (line parsing, snapshot merging, call↔result correlation). The renderer aims
// for faithful display, so unlike the scanner it does NOT sanitize secrets
// (show reads the verbatim raw_jsonl of the user's own local archive), does not
// bound tool-result length (the pager handles size), and surfaces tool calls as
// indicator lines. A `--show-thinking` toggle and true inline subagent
// interleaving are TUI concerns; the CLI renders each subagent as its own
// appended section (see cmd/capy/vault.go).

// displayRole labels a rendered message. tool == tool_result output (from user
// entries); system == away_summary / pr-link.
const (
	displayUser      = "user"
	displayAssistant = "assistant"
	displayTool      = "tool"
	displaySystem    = "system"
)

// displayMsg is one composed message ready for formatting. queued marks a user
// message recovered from a queued_command attachment (A2) — submitted while the
// assistant was mid-turn — so its label is annotated "· queued".
type displayMsg struct {
	role   string
	body   string
	queued bool
}

// RenderText renders a session archived from platform p as a plain-text
// transcript suitable for a pager. An empty blob (or one with no displayable
// messages) yields "". A platform that cannot be decoded (see decodeForDisplay)
// also yields "" and is logged.
func RenderText(p Platform, raw []byte) string {
	return formatDisplay(displayMessages(decodeForDisplay(p, raw, "render")), p, false)
}

// RenderMarkdown renders a session archived from platform p as Markdown for
// clean export. Empty / undecodable input behaves as in RenderText.
func RenderMarkdown(p Platform, raw []byte) string {
	return formatDisplay(displayMessages(decodeForDisplay(p, raw, "render")), p, true)
}

// decodeForDisplay decodes raw with p's decoder for a display consumer (render
// or transcript), returning nil for an empty blob. A bytes.Reader never fails,
// so the only error Decode can return here is a platform-dispatch failure
// (ErrUnknownPlatform / ErrDecoderUnavailable — DecoderFor). The display
// readers return text, not an error, so that failure is logged at warn (the
// single-handling rule: logged, not returned) and nil is returned, which the
// consumers render as empty output — never as a silent Claude default.
func decodeForDisplay(p Platform, raw []byte, consumer string) *Transcript {
	if len(raw) == 0 {
		return nil
	}
	t, err := DecoderFor(p).Decode(bytes.NewReader(raw))
	if err != nil {
		slog.Warn("vault display: cannot decode session", "consumer", consumer, "platform", p, "error", err)
		return nil
	}
	return t
}

// displayMessages is the `show` consumer over the transcript model: it walks
// the decoded entries in order and emits one displayMsg per message. This is
// where every RENDER policy lives (the decoder is pre-policy — see
// transcript_model.go):
//
//   - A Human entry is a user message (Queued carried for the label).
//   - A ToolResult with an empty Body is skipped (the decoder emits those for
//     their Diff — D18 — which `show` does not render); an excludedResultTools
//     result (Read/NotebookRead) collapses to the one-line collapsedToolResult
//     marker; every other body is prefixed with its CallSummary, unbounded.
//   - An Assistant message's body is its text parts verbatim and every
//     ToolCall (launches included) as a "→ <Summary>" line, in part order.
//   - A System entry is shown unless SearchOnly (D7 — indexed by the scanner,
//     never displayed).
//
// Output for Claude sessions is byte-identical to the pre-model renderer
// (golden_test.go, parity_canary_test.go). A nil transcript (empty or
// undecodable input) yields nil. An entry of unknown Kind is skipped with a
// warning — it can only come from a decoder bug, never from valid input.
func displayMessages(t *Transcript) []displayMsg {
	if t == nil {
		return nil
	}
	var msgs []displayMsg
	for _, e := range t.Entries {
		switch e.Kind {
		case EntryHuman:
			msgs = append(msgs, displayMsg{role: displayUser, body: e.Text, queued: e.Queued})

		case EntryToolResult:
			if e.Body == "" {
				continue // emitted by the decoder for its Diff (D18); nothing to show
			}
			if excludedResultTools[e.CallName] {
				msgs = append(msgs, displayMsg{role: displayTool, body: collapsedToolResult(e.CallSummary, e.Body)})
			} else {
				msgs = append(msgs, displayMsg{role: displayTool, body: prefixToolResult(e.CallSummary, e.Body)})
			}

		case EntryAssistant:
			if b := assistantDisplayBody(e.Parts); b != "" {
				msgs = append(msgs, displayMsg{role: displayAssistant, body: b})
			}

		case EntrySystem:
			if e.SearchOnly {
				continue
			}
			msgs = append(msgs, displayMsg{role: displaySystem, body: e.Text})

		default:
			// Fail loud, not silent: an unknown Kind (including the EntryUnknown
			// zero value) is a decoder bug, and dropping it without a trace would
			// hide it. Output is unaffected for valid input.
			slog.Warn("vault render: skipping transcript entry of unknown kind",
				"platform", t.Meta.Platform, "line", e.LineIndex, "kind", e.Kind)
		}
	}
	return msgs
}

// collapsedToolResult renders the one-line display placeholder for an excluded
// tool_result (Read/NotebookRead): the call summary plus an omitted-body marker
// with the line count, so the transcript shows the call and its target without the
// full file/cell dump. The verbatim body stays in raw_jsonl (`vault show
// --format json` / restore). Used by render.go (`vault show`) only; the TUI viewer
// collapses excluded results to an openable marker instead (A1, transcript.go).
func collapsedToolResult(summary, body string) string {
	if summary == "" {
		summary = "tool result"
	}
	return fmt.Sprintf("%s\n⋯ [output omitted from index — %d line(s)]", summary, strings.Count(body, "\n")+1)
}

// assistantDisplayBody composes an assistant message for `show`: text parts
// verbatim and every tool call as a "→ <Summary>" indicator line, in part order
// (order is load-bearing — transcript_model.go Entry.Parts). Launches
// (Task/Agent) render like any other call here; only the TUI turns them into
// markers (D19). A call with an empty Summary contributes nothing.
func assistantDisplayBody(parts []Part) string {
	var lines []string
	for _, p := range parts {
		switch {
		case p.Call != nil:
			if p.Call.Summary != "" {
				lines = append(lines, "→ "+p.Call.Summary)
			}
		case p.Text != "":
			lines = append(lines, p.Text)
		}
	}
	return strings.Join(lines, "\n")
}

// displayLabels maps a display role to its (plain, markdown) heading text for
// every role whose label is platform-independent. The assistant heading is
// platform-aware (Platform.DisplayName — "Claude" / "Codex") and is composed by
// displayLabel instead. displayLabel falls back to the raw role string for any
// missing key (defensive — the set is closed by construction).
var displayLabels = map[string][2]string{
	displayUser:   {"You", "👤 You"},
	displayTool:   {"Tool result", "⎿ Tool result"},
	displaySystem: {"System", "ℹ System"},
}

// displayLabel returns the heading text for a display role on platform p, in
// plain or markdown form.
func displayLabel(role string, p Platform, markdown bool) string {
	if role == displayAssistant {
		if markdown {
			return "🤖 " + p.DisplayName()
		}
		return p.DisplayName()
	}
	labels, ok := displayLabels[role]
	if !ok {
		return role
	}
	if markdown {
		return labels[1]
	}
	return labels[0]
}

// formatDisplay turns ordered messages into a transcript. Markdown uses `##`
// headings and fenced code blocks for tool results; plain text uses `[Role]`
// headers. The assistant heading names the platform (p.DisplayName). Returns
// "" for no messages.
func formatDisplay(msgs []displayMsg, p Platform, markdown bool) string {
	if len(msgs) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, m := range msgs {
		if i > 0 {
			sb.WriteString("\n")
		}
		label := displayLabel(m.role, p, markdown)
		if m.queued {
			label += " · queued"
		}
		if markdown {
			fmt.Fprintf(&sb, "## %s\n\n", label)
			if m.role == displayTool {
				// Widen the fence past any backtick run in the body so a tool
				// result containing ``` cannot prematurely close the code block.
				fence := mdFence(m.body)
				fmt.Fprintf(&sb, "%s\n%s\n%s\n", fence, m.body, fence)
			} else {
				sb.WriteString(m.body)
				sb.WriteString("\n")
			}
		} else {
			fmt.Fprintf(&sb, "[%s]\n%s\n", label, m.body)
		}
	}
	return sb.String()
}

// mdFence returns a backtick fence long enough to wrap body safely: one backtick
// longer than the longest backtick run inside body, with a minimum of three.
func mdFence(body string) string {
	longest, run := 0, 0
	for _, r := range body {
		if r == '`' {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", max(longest+1, 3))
}
