package vault

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// render.go turns a raw session JSONL blob into a human-readable transcript for
// `capy vault show`. It is a SEPARATE parser from scanner.go (search extraction):
// the renderer aims for faithful display, so unlike the scanner it does NOT
// sanitize secrets (show reads the verbatim raw_jsonl of the user's own local
// archive), does not bound tool-result length (the pager handles size), and
// surfaces tool-use calls as indicators. Like the scanner it deduplicates
// progressive assistant snapshots and skips thinking blocks. A `--show-thinking`
// toggle and true inline subagent interleaving are TUI concerns (Task 6); the CLI
// renders each subagent as its own appended section (see cmd/capy/vault.go).

// displayRole labels a rendered message. tool == tool_result output (from user
// entries); system == away_summary / pr-link.
const (
	displayUser      = "user"
	displayAssistant = "assistant"
	displayTool      = "tool"
	displaySystem    = "system"
)

// renderMaxLineBytes caps a single JSONL line the renderer will parse. It is the
// same practical bound as the scanner's (a >16MB line is a pathological inline
// blob we skip for display too) but is named locally so the renderer's limit is
// explicit and independent of the scanner's FTS tuning.
const renderMaxLineBytes = 16 * 1024 * 1024

// displayMsg is one composed message ready for formatting. queued marks a user
// message recovered from a queued_command attachment (A2) — submitted while the
// assistant was mid-turn — so its label is annotated "· queued".
type displayMsg struct {
	role   string
	body   string
	queued bool
}

// RenderText renders a session JSONL blob as a plain-text transcript suitable
// for a pager. An empty blob (or one with no extractable messages) yields "".
func RenderText(raw []byte) string {
	return formatDisplay(collectDisplay(raw), false)
}

// RenderMarkdown renders a session JSONL blob as Markdown for clean export.
func RenderMarkdown(raw []byte) string {
	return formatDisplay(collectDisplay(raw), true)
}

// renderEntry is one in-order message collected during the single pass.
// Assistant snapshots sharing a message id merge into one entry (blocks
// deduplicated by the (Type, Text, Name, ID) tuple, as the scanner does).
type renderEntry struct {
	role    string
	content json.RawMessage // user: message.content
	blocks  []contentBlock  // assistant: merged, deduplicated blocks
	text    string          // system: pre-composed text
	queued  bool            // user: recovered from a queued_command attachment (A2)
}

// collectDisplay walks the JSONL once and returns ordered display messages.
func collectDisplay(raw []byte) []displayMsg {
	if len(raw) == 0 {
		return nil
	}

	var entries []renderEntry
	assistantPos := make(map[string]int) // message.id → index in entries

	_ = scanLines(bytes.NewReader(raw), renderMaxLineBytes, func(data []byte, oversize bool) {
		if oversize || len(data) == 0 {
			return
		}
		var line jsonlLine
		if err := json.Unmarshal(data, &line); err != nil {
			return
		}

		var msg jsonlMessage
		hasMsg := false
		if len(line.Message) > 0 && !bytes.Equal(line.Message, jsonNull) {
			if err := json.Unmarshal(line.Message, &msg); err == nil {
				hasMsg = true
				if line.Type == "" && msg.Role != "" {
					line.Type = msg.Role
				}
			}
		}

		switch line.Type {
		case "user":
			if hasMsg {
				entries = append(entries, renderEntry{role: displayUser, content: msg.Content})
			}
		case "assistant":
			if !hasMsg {
				return
			}
			id := msg.ID
			if id == "" {
				id = line.UUID
			}
			var blocks []contentBlock
			if len(msg.Content) > 0 {
				_ = json.Unmarshal(msg.Content, &blocks)
			}
			if pos, ok := assistantPos[id]; ok {
				entries[pos].blocks = dedupBlocks(entries[pos].blocks, blocks)
			} else {
				assistantPos[id] = len(entries)
				entries = append(entries, renderEntry{role: displayAssistant, blocks: blocks})
			}
		case "attachment":
			// A queued_command attachment is an in-flight user message (A2):
			// display it as a user turn, annotated "· queued".
			if prompt := queuedCommandPrompt(line.Attachment); prompt != "" {
				entries = append(entries, renderEntry{role: displayUser, content: userTextContent(prompt), queued: true})
			}
		case "pr-link":
			if t := prLinkText(line); t != "" {
				entries = append(entries, renderEntry{role: displaySystem, text: t})
			}
		case "system":
			if line.Subtype == "away_summary" {
				if t := strings.TrimSpace(line.Content); t != "" {
					entries = append(entries, renderEntry{role: displaySystem, text: t})
				}
			}
			// Other types (thinking-only assistant noise, agent-name, progress,
			// turn_duration, …) are not shown; raw_jsonl preserves them regardless.
		}
	})

	// Correlate tool_use_id → call so each tool_result renders with the call that
	// produced it (and excluded-tool results collapse to a marker).
	toolUses := make(map[string]toolCall)
	for _, e := range entries {
		if e.role == displayAssistant {
			collectToolUseSummaries(e.blocks, toolUses)
		}
	}

	var msgs []displayMsg
	for _, e := range entries {
		switch e.role {
		case displayUser:
			human, tools := renderUserContent(e.content, toolUses)
			if human != "" {
				msgs = append(msgs, displayMsg{role: displayUser, body: human, queued: e.queued})
			}
			for _, tr := range tools {
				msgs = append(msgs, displayMsg{role: displayTool, body: tr})
			}
		case displayAssistant:
			if b := renderAssistantBlocks(e.blocks); b != "" {
				msgs = append(msgs, displayMsg{role: displayAssistant, body: b})
			}
		case displaySystem:
			msgs = append(msgs, displayMsg{role: displaySystem, body: e.text})
		}
	}
	return msgs
}

// dedupBlocks appends add to dst, skipping blocks already present by the
// (Type, Text, Name, ID) tuple — the progressive-snapshot dedup the scanner uses.
func dedupBlocks(dst, add []contentBlock) []contentBlock {
	for _, b := range add {
		dup := false
		for _, eb := range dst {
			if eb.Type == b.Type && eb.Text == b.Text && eb.Name == b.Name && eb.ID == b.ID {
				dup = true
				break
			}
		}
		if !dup {
			dst = append(dst, b)
		}
	}
	return dst
}

// renderUserContent splits a user message into human text and a list of
// (unbounded) tool_result texts. Content is either a plain string (human input)
// or an array of text / tool_result blocks. Each tool_result is prefixed with its
// originating tool-call summary (from toolUses, keyed by tool_use_id) so display
// shows which call produced the output; an unknown id leaves the text unprefixed.
// A tool_result whose call is in excludedResultTools (Read/NotebookRead) is
// collapsed to a one-line marker instead of the full file/cell dump (the verbatim
// body stays in raw_jsonl). Used by render.go (`vault show`) only; the TUI viewer
// uses splitUserContentForViewer (transcript.go), which keeps the full body so a
// collapsed result can expand on demand (A1).
func renderUserContent(raw json.RawMessage, toolUses map[string]toolCall) (human string, tools []string) {
	if len(raw) == 0 {
		return "", nil
	}
	if s, ok := asJSONString(raw); ok {
		return cleanText(s), nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", nil
	}
	var texts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if c := cleanText(b.Text); c != "" {
				texts = append(texts, c)
			}
		case "tool_result":
			if t := toolResultText(b.Content); t != "" {
				call := toolUses[b.ToolUseID]
				if excludedResultTools[call.name] {
					tools = append(tools, collapsedToolResult(call, t))
				} else {
					tools = append(tools, prefixToolResult(call.summary, t))
				}
			}
		}
	}
	return strings.Join(texts, "\n"), tools
}

// collapsedToolResult renders the one-line display placeholder for an excluded
// tool_result (Read/NotebookRead): the call summary plus an omitted-body marker
// with the line count, so the transcript shows the call and its target without the
// full file/cell dump. The verbatim body stays in raw_jsonl (`vault show
// --format json` / restore). Used by render.go (`vault show`) only; the TUI viewer
// collapses excluded results to an openable marker instead (A1, transcript.go).
func collapsedToolResult(call toolCall, body string) string {
	label := call.summary
	if label == "" {
		label = "tool result"
	}
	return fmt.Sprintf("%s\n⋯ [output omitted from index — %d line(s)]", label, strings.Count(body, "\n")+1)
}

// renderAssistantBlocks keeps text and renders tool_use blocks as indicator
// lines; thinking blocks are skipped (display default, no toggle in v1).
func renderAssistantBlocks(blocks []contentBlock) string {
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if t := strings.TrimSpace(b.Text); t != "" {
				parts = append(parts, t)
			}
		case "tool_use":
			if s := toolUseSummary(b.Name, b.Input); s != "" {
				parts = append(parts, "→ "+s)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// displayLabels maps a display role to its (plain, markdown) heading text. It
// must cover every displayXxx role constant; formatDisplay falls back to the raw
// role string for any missing key (defensive — the set is closed by construction).
var displayLabels = map[string][2]string{
	displayUser:      {"You", "👤 You"},
	displayAssistant: {"Claude", "🤖 Claude"},
	displayTool:      {"Tool result", "⎿ Tool result"},
	displaySystem:    {"System", "ℹ System"},
}

// formatDisplay turns ordered messages into a transcript. Markdown uses `##`
// headings and fenced code blocks for tool results; plain text uses `[Role]`
// headers. Returns "" for no messages.
func formatDisplay(msgs []displayMsg, markdown bool) string {
	if len(msgs) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, m := range msgs {
		if i > 0 {
			sb.WriteString("\n")
		}
		labels := displayLabels[m.role]
		if markdown {
			label := labels[1]
			if label == "" {
				label = m.role
			}
			if m.queued {
				label += " · queued"
			}
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
			label := labels[0]
			if label == "" {
				label = m.role
			}
			if m.queued {
				label += " · queued"
			}
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
