package vault

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
)

// claude_decoder.go is the Claude Code Decoder: the "pass 1" that scanner.go,
// render.go and transcript.go each duplicated today, extracted once behind the
// transcript model. It reproduces the UNION of what the three readers extract
// (testdata/golden/DIVERGENCES.md); each reader keeps only its own policy.
//
// codex-vault-sessions Slice 2 added this file WITHOUT touching the three reader
// loops; Slice 3 replaced the scanner's loop with ScanTranscript (scanner.go).
// The render.go / transcript.go loops (and the helpers only they still use —
// collectToolUseSummaries, renderUserContent, splitUserContentForViewer,
// assistantBodyAndLaunches, userTextContent, renderLineCap …) are replaced by
// consumers over Transcript in Slice 4 and deleted there.

// claudeDecoder decodes Claude Code session JSONL (main sessions and subagent
// sidecars alike — a sidecar is just another JSONL). The zero value is ready to
// use and is what DecoderFor(PlatformClaudeCode) returns.
type claudeDecoder struct {
	// lineCap bounds one physical JSONL line; a longer line is skipped for
	// extraction but still advances LineIndex (scanLines). Zero selects the
	// package cap scanLineCap, which the golden harness lowers for the oversize
	// fixture. render.go/transcript.go carry an identically-valued renderLineCap
	// today (D11); Slice 4 collapses the two onto this decoder.
	lineCap int
}

var _ Decoder = claudeDecoder{}

// claudeSlot is one in-order record collected during pass 1, before call↔result
// correlation is possible. Assistant snapshots sharing a message id merge into
// one slot (blocks deduplicated) so the canonical LineIndex/Timestamp are the
// first snapshot's.
type claudeSlot struct {
	kind      EntryKind
	lineIndex int
	timestamp time.Time

	// EntryHuman: cleaned human text (may be empty — no entry then), the line's
	// tool_result blocks in block order, the line's top-level toolUseResult
	// (Edit/Write structuredPatch — A3) and the queued_command flag (A2).
	human      string
	results    []contentBlock
	toolResult json.RawMessage
	queued     bool

	// EntryAssistant: merged, deduplicated content blocks.
	blocks []contentBlock

	// EntrySystem: pre-composed text and the scanner-only flag (D7).
	text       string
	searchOnly bool
}

// Decode reads Claude Code session JSONL from r. It never fails on content —
// malformed lines are logged and skipped, oversize lines are skipped, unknown
// types are ignored — and returns only a genuine read error, wrapped exactly as
// ScanSession reports it today ("reading session: …", D10).
func (d claudeDecoder) Decode(r io.Reader) (*Transcript, error) {
	lineCap := d.lineCap
	if lineCap <= 0 {
		lineCap = scanLineCap
	}
	t := &Transcript{Meta: Meta{Platform: PlatformClaudeCode}}
	var slots []claudeSlot
	assistantIdx := make(map[string]int) // message.id (else line uuid) → index in slots
	lineIndex := -1

	// Pass 1: one callback per physical line. Capture Meta from every valid line
	// and build the ordered slot list; assistant progressive snapshots merge by id.
	err := scanLines(r, lineCap, func(data []byte, oversize bool) {
		lineIndex++
		if oversize || len(data) == 0 {
			return
		}

		var line jsonlLine
		if err := json.Unmarshal(data, &line); err != nil {
			slog.Warn("vault claude decoder: skipping malformed JSONL line", "line", lineIndex, "error", err)
			return
		}

		// message is parsed only when present and not the literal null; a
		// message that is not a JSON object (e.g. a string) leaves the line
		// message-less. Infer the type from message.role when type is absent.
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

		// D4: timestamps come from every valid-JSON line, entry or not.
		ts := parseJSONLTime(line.Timestamp)
		if !ts.IsZero() {
			if t.Meta.StartTime.IsZero() {
				t.Meta.StartTime = ts
			}
			t.Meta.EndTime = ts
		}

		switch line.Type {
		case "user":
			// D3: location from user-typed lines only, message-less ones included.
			if t.Meta.CWD == "" && line.CWD != "" {
				t.Meta.CWD = line.CWD
			}
			if t.Meta.Branch == "" && line.GitBranch != "" {
				t.Meta.Branch = line.GitBranch
			}
			if !hasMsg {
				return
			}
			// D1: the title fallback is the first PLAIN-STRING user content,
			// TrimSpace'd, non-empty, not `<`-prefixed — taken raw (no cleanText).
			// Block-array text is never eligible; scanning continues past
			// ineligible lines.
			if t.Meta.TitleFallback == "" {
				if s, ok := asJSONString(msg.Content); ok {
					if tt := strings.TrimSpace(s); tt != "" && !strings.HasPrefix(tt, "<") {
						t.Meta.TitleFallback = tt
					}
				}
			}
			human, results := splitUserContent(msg.Content)
			slots = append(slots, claudeSlot{
				kind: EntryHuman, lineIndex: lineIndex, timestamp: ts,
				human: human, results: results, toolResult: line.ToolUseResult,
			})

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
				if err := json.Unmarshal(msg.Content, &blocks); err != nil {
					// The entry is still created/merged, with no blocks (D9 log-only).
					slog.Warn("vault claude decoder: skipping malformed assistant content", "line", lineIndex, "error", err)
				}
			}
			if idx, ok := assistantIdx[id]; ok {
				slots[idx].blocks = dedupBlocks(slots[idx].blocks, blocks)
			} else {
				assistantIdx[id] = len(slots)
				slots = append(slots, claudeSlot{
					kind: EntryAssistant, lineIndex: lineIndex, timestamp: ts, blocks: blocks,
				})
			}

		case "ai-title":
			if line.AITitle != "" {
				t.Meta.ExplicitTitle = line.AITitle // last wins (D2)
			}

		case "pr-link":
			if text := prLinkText(line); text != "" {
				slots = append(slots, claudeSlot{kind: EntrySystem, lineIndex: lineIndex, timestamp: ts, text: text})
			}

		case "attachment":
			// A queued_command attachment is an in-flight user message (A2): a
			// Human entry flagged Queued. It flows through cleanText like any
			// plain-string prompt. When the line ALSO carries a message, the
			// queued prompt wins and the message is ignored (D7).
			if prompt := queuedCommandPrompt(line.Attachment); prompt != "" {
				slots = append(slots, claudeSlot{
					kind: EntryHuman, lineIndex: lineIndex, timestamp: ts, human: cleanText(prompt), queued: true,
				})
				return
			}
			// Generic attachment message content is indexed by the scanner but
			// not displayed (D7) — a SearchOnly System entry.
			if hasMsg {
				if text := attachmentText(msg.Content); text != "" {
					slots = append(slots, claudeSlot{
						kind: EntrySystem, lineIndex: lineIndex, timestamp: ts, text: text, searchOnly: true,
					})
				}
			}

		case "system":
			if line.Subtype == "away_summary" {
				if text := strings.TrimSpace(line.Content); text != "" {
					slots = append(slots, claudeSlot{kind: EntrySystem, lineIndex: lineIndex, timestamp: ts, text: text})
				}
			}

			// Every other type (custom-title, agent-name, progress,
			// permission-mode, file-history-snapshot, queue-operation,
			// system:turn_duration, …, and anything unknown) is skipped —
			// raw_jsonl preserves it regardless (ADR-021).
		}
	})
	if err != nil {
		return nil, fmt.Errorf("reading session: %w", err)
	}

	// Pass 2a: assistant blocks → ordered Parts, and the whole-transcript
	// call-id → call map results are correlated against. Built over ALL
	// assistant slots before any result is emitted, so correlation is
	// whole-transcript, not positional (a tool_use always precedes its result,
	// but the full map is simplest and matches today's readers).
	parts := make([][]Part, len(slots))
	calls := make(map[string]toolCall)
	for i, s := range slots {
		if s.kind != EntryAssistant {
			continue
		}
		parts[i] = assistantParts(s.blocks)
		for _, p := range parts[i] {
			if p.Call != nil && p.Call.ID != "" {
				calls[p.Call.ID] = toolCall{name: p.Call.Name, summary: p.Call.Summary}
			}
		}
	}

	// Pass 2b: slots → entries, in order. Empty Human/Assistant/System slots
	// produce nothing; every tool_result block produces a ToolResult (D18).
	for i, s := range slots {
		switch s.kind {
		case EntryHuman:
			if s.human != "" {
				t.Entries = append(t.Entries, Entry{
					Kind: EntryHuman, LineIndex: s.lineIndex, Timestamp: s.timestamp, Text: s.human, Queued: s.queued,
				})
			}
			t.Entries = appendToolResults(t.Entries, s, calls)
		case EntryAssistant:
			if len(parts[i]) > 0 {
				t.Entries = append(t.Entries, Entry{
					Kind: EntryAssistant, LineIndex: s.lineIndex, Timestamp: s.timestamp, Parts: parts[i],
				})
			}
		case EntrySystem:
			t.Entries = append(t.Entries, Entry{
				Kind: EntrySystem, LineIndex: s.lineIndex, Timestamp: s.timestamp, Text: s.text, SearchOnly: s.searchOnly,
			})
		}
	}
	return t, nil
}

// splitUserContent splits a user message.content into cleaned human text and
// the line's tool_result blocks in block order. Content is a plain JSON string
// (human input → cleanText) or a block array: each text block is cleanText'ed and
// the non-empty ones joined by "\n"; tool_result blocks are returned as-is for
// pass 2. Empty content, or a block array that fails to unmarshal (D27), yields
// no text and no results — exactly as all three readers behave today.
func splitUserContent(raw json.RawMessage) (human string, results []contentBlock) {
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
			results = append(results, b)
		}
	}
	return strings.Join(texts, "\n"), results
}

// appendToolResults emits one ToolResult per tool_result block of a Human slot,
// correlated to its call by tool_use_id (an unknown id leaves CallName /
// CallSummary empty). The body is toolResultText — verbatim, untruncated; an
// EMPTY body is still emitted (D18). Diff: the line carries at most one
// top-level toolUseResult, so the FIRST diff-tool result (diffResultTools —
// Edit/Write, whose structuredPatch lives there, A3) with a renderable patch
// claims it; later diff-tool results on the same line get their plain body, and a
// result whose patch does not render (no hunks, no lines) gets no Diff (D15).
func appendToolResults(entries []Entry, s claudeSlot, calls map[string]toolCall) []Entry {
	diffConsumed := false
	for _, b := range s.results {
		call := calls[b.ToolUseID]
		e := Entry{
			Kind: EntryToolResult, LineIndex: s.lineIndex, Timestamp: s.timestamp,
			CallID: b.ToolUseID, CallName: call.name, CallSummary: call.summary,
			Body: toolResultText(b.Content),
		}
		// TODO(codex-vault-sessions, after Slice 4): gating on the consumer policy
		// map diffResultTools is a deliberate coupling kept for the byte-identical
		// gate — it is exactly what splitUserContentForViewer does today. The
		// format-pure rule is "a Diff whenever the line's structuredPatch renders",
		// regardless of tool name; adopting it changes TUI output for any
		// non-Edit/Write result that carries a patch (none observed) and must land
		// as its own golden-regenerating change. See implementation.md § Deferred.
		if diffResultTools[call.name] && !diffConsumed {
			if text, added, removed, ok := diffBodyFromToolResult(s.toolResult); ok {
				diffConsumed = true
				e.Diff = &Diff{Text: text, Added: added, Removed: removed}
			}
		}
		entries = append(entries, e)
	}
	return entries
}

// assistantParts converts merged assistant blocks into ordered Parts: a text
// block becomes a text part when its TrimSpace'd text is non-empty; a tool_use
// block becomes a ToolCall with the shared toolUseSummary label, and Task/Agent
// calls additionally carry a Launch labelled by subagentLaunchLabel (the two
// labels differ in source keys and caps — D19). Thinking blocks are not parts.
func assistantParts(blocks []contentBlock) []Part {
	var parts []Part
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if t := strings.TrimSpace(b.Text); t != "" {
				parts = append(parts, Part{Text: t})
			}
		case "tool_use":
			call := &ToolCall{ID: b.ID, Name: b.Name, Summary: toolUseSummary(b.Name, b.Input), Input: b.Input}
			if b.Name == "Task" || b.Name == "Agent" {
				call.Launch = &Launch{Label: subagentLaunchLabel(b.Input)}
			}
			parts = append(parts, Part{Call: call})
		}
	}
	return parts
}
