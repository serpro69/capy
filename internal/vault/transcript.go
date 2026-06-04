package vault

import (
	"bytes"
	"encoding/json"
	"strings"
)

// transcript.go is the display parser the TUI viewer consumes. It is a third
// JSONL reader alongside scanner.go (FTS extraction) and render.go (the `show`
// pager renderer): like render.go it is faithful and unsanitized (the viewer
// shows the user's own local archive verbatim) and deduplicates progressive
// assistant snapshots; unlike render.go it records each message's source-line
// anchor and surfaces subagent launch points as separate marker messages so the
// TUI can scroll to a search hit (line_index) and open a subagent standalone.
//
// It lives in package vault, not internal/vault/tui, because the JSONL wire
// types and extraction helpers (scanLines, dedupBlocks, toolUseSummary, …) are
// here and unexported; the tui package stays pure presentation (lipgloss +
// scrolling) over the TranscriptMessage slice this returns.

// Display roles for TUI styling. The message roles mirror render.go's display*
// constants (kept identical so a future merge is trivial); RoleSubagent is a
// launch-point marker, not a transcript message.
const (
	RoleUser      = displayUser      // "user"
	RoleAssistant = displayAssistant // "assistant"
	RoleTool      = displayTool      // "tool"
	RoleSystem    = displaySystem    // "system"
	RoleSubagent  = "subagent"       // launch-point marker
)

// subagentLabelMaxChars bounds the marker label (description/prompt summary).
const subagentLabelMaxChars = 100

// TranscriptMessage is one renderable unit for the viewer. Body is the composed,
// unsanitized display text (may be multi-line). SourceLine is the 0-based line of
// the originating entry in this transcript's source JSONL — the canonical/first
// line for a deduplicated assistant snapshot, matching the FTS scanner's
// line_index, so a search hit scrolls to the same place. AgentID/Openable are set
// only on RoleSubagent markers.
type TranscriptMessage struct {
	Role       string
	Body       string
	SourceLine int
	AgentID    string // RoleSubagent only: the mapped subagent id ("" when unmatched)
	Openable   bool   // RoleSubagent only: AgentID resolves to an archived transcript
}

// SubagentRelPath returns the vault_files relative path that stores a subagent
// transcript, the inverse of import.go's subagentID(). The viewer uses it to
// fetch a subagent's bytes from the session's File set when opening it standalone.
func SubagentRelPath(id string) string {
	return "subagents/agent-" + id + ".jsonl"
}

// SubagentIDFromPath returns the agent id for a subagents/agent-<id>.jsonl path
// (ok=false for any other sidecar), so the tui package can enumerate a session's
// subagents without re-implementing the path convention.
func SubagentIDFromPath(rel string) (string, bool) {
	id := subagentID(rel)
	return id, id != ""
}

// transcriptEntry is an in-order slot from pass 1. Assistant snapshots sharing a
// message id merge into one slot (blocks deduplicated) so its SourceLine is the
// first snapshot's line — exactly the scanner's canonical line_index.
type transcriptEntry struct {
	role    string // displayUser | displayAssistant | displaySystem
	line    int
	content json.RawMessage // user: message.content
	blocks  []contentBlock  // assistant: merged, deduplicated blocks
	text    string          // system: pre-composed text (pr-link / away_summary)
}

// ParseTranscript parses a raw session (or subagent) JSONL blob into ordered
// display messages. Progressive assistant snapshots are deduplicated. Task/Agent
// tool_use blocks become RoleSubagent marker messages; when the number of markers
// equals len(subagentIDs) the markers are mapped to those ids in order and become
// Openable (a best-effort launch-point→file mapping — the JSONL carries no
// verified tool_use↔agent-id link, so on any count mismatch markers stay visible
// but non-openable and search-jump, which is exact, remains the reliable path).
// Pass nil subagentIDs for a subagent transcript (no nested markers expected).
//
// Malformed JSONL lines are skipped (consistent with render.go's scanLines
// contract), so the returned slice may be incomplete for a corrupt blob — the
// viewer shows what parsed rather than failing the whole session.
func ParseTranscript(raw []byte, subagentIDs []string) []TranscriptMessage {
	if len(raw) == 0 {
		return nil
	}

	var entries []transcriptEntry
	assistantPos := make(map[string]int) // message.id → index in entries
	lineIndex := -1

	_ = scanLines(bytes.NewReader(raw), renderMaxLineBytes, func(data []byte, oversize bool) {
		lineIndex++
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
				entries = append(entries, transcriptEntry{role: displayUser, line: lineIndex, content: msg.Content})
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
				entries = append(entries, transcriptEntry{role: displayAssistant, line: lineIndex, blocks: blocks})
			}
		case "pr-link":
			if t := prLinkText(line); t != "" {
				entries = append(entries, transcriptEntry{role: displaySystem, line: lineIndex, text: t})
			}
		case "system":
			if line.Subtype == "away_summary" {
				if t := strings.TrimSpace(line.Content); t != "" {
					entries = append(entries, transcriptEntry{role: displaySystem, line: lineIndex, text: t})
				}
			}
			// Other types (agent-name, ai-title, progress, file-history-snapshot,
			// turn_duration, …) are not rendered; raw_jsonl preserves them.
		}
	})

	var msgs []TranscriptMessage
	var markerIdx []int // indices in msgs that are RoleSubagent (for ordered mapping)
	for _, e := range entries {
		switch e.role {
		case displayUser:
			human, tools := renderUserContent(e.content)
			if human != "" {
				msgs = append(msgs, TranscriptMessage{Role: RoleUser, Body: human, SourceLine: e.line})
			}
			for _, tr := range tools {
				msgs = append(msgs, TranscriptMessage{Role: RoleTool, Body: tr, SourceLine: e.line})
			}
		case displayAssistant:
			body, launches := assistantBodyAndLaunches(e.blocks)
			if body != "" {
				msgs = append(msgs, TranscriptMessage{Role: RoleAssistant, Body: body, SourceLine: e.line})
			}
			for _, label := range launches {
				markerIdx = append(markerIdx, len(msgs))
				msgs = append(msgs, TranscriptMessage{Role: RoleSubagent, Body: label, SourceLine: e.line})
			}
		case displaySystem:
			msgs = append(msgs, TranscriptMessage{Role: RoleSystem, Body: e.text, SourceLine: e.line})
		}
	}

	// Best-effort launch-point→file mapping: only when every marker pairs with an
	// archived subagent (counts align) do we make markers openable. Otherwise the
	// mapping is ambiguous, so markers stay visible-only and search-jump (exact)
	// is the way in.
	if len(markerIdx) > 0 && len(markerIdx) == len(subagentIDs) {
		for k, mi := range markerIdx {
			msgs[mi].AgentID = subagentIDs[k]
			msgs[mi].Openable = true
		}
	}
	return msgs
}

// assistantBodyAndLaunches splits an assistant message's blocks into a display
// body and the labels of any Task/Agent launch points. Text blocks are kept;
// non-subagent tool_use blocks render as "→ <summary>" body lines (as render.go
// does); Task/Agent tool_use blocks become launch markers instead of body lines;
// thinking blocks are skipped.
func assistantBodyAndLaunches(blocks []contentBlock) (body string, launches []string) {
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if t := strings.TrimSpace(b.Text); t != "" {
				parts = append(parts, t)
			}
		case "tool_use":
			if b.Name == "Task" || b.Name == "Agent" {
				launches = append(launches, subagentLaunchLabel(b.Input))
				continue
			}
			if s := toolUseSummary(b.Name, b.Input); s != "" {
				parts = append(parts, "→ "+s)
			}
		}
	}
	return strings.Join(parts, "\n"), launches
}

// subagentLaunchLabel builds a short human label for a Task/Agent launch from its
// input: the description if present, else the prompt, truncated. Returns
// "subagent" when neither is available.
func subagentLaunchLabel(input json.RawMessage) string {
	for _, key := range []string{"description", "subagent_type", "prompt"} {
		if v := strings.TrimSpace(jsonStringField(input, key)); v != "" {
			return truncateRunes(v, subagentLabelMaxChars)
		}
	}
	return "subagent"
}
