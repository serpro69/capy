package vault

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/serpro69/capy/internal/sanitize"
)

// FTS roles. tool_result output is tagged "tool" (not "user") so that a
// `--role user` search returns human prompts, not tool output.
const (
	roleUser      = "user"
	roleAssistant = "assistant"
	roleTool      = "tool"
	roleSystem    = "system"
)

const (
	// maxScanLineBytes bounds a single JSONL line for FTS extraction. A line
	// larger than this (e.g. an inline base64 image) is skipped for extraction
	// only — raw_jsonl still preserves it verbatim, so restore/show are
	// unaffected. Matches internal/session/parse.go's 16MB buffer.
	maxScanLineBytes = 16 * 1024 * 1024
	// maxToolResultChars caps tool_result text per FTS row: 75% head + 25% tail
	// on a rune boundary (matching claude-history's truncate_for_search).
	maxToolResultChars = 16 * 1024
	// titleMaxChars bounds the first-user-message title fallback.
	titleMaxChars = 120
	// agentPromptMaxChars bounds the Agent/Task prompt summary.
	agentPromptMaxChars = 200
)

var (
	// sysReminderRe / noiseTagRe mirror internal/session/parse.go: strip
	// <system-reminder> blocks and slash-command/local-command noise tags from
	// human text before it enters the FTS index.
	sysReminderRe = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)
	noiseTagRe    = regexp.MustCompile(`(?s)<local-command-caveat>.*?</local-command-caveat>|<local-command-stdout>.*?</local-command-stdout>|<command-name>.*?</command-name>|<command-message>.*?</command-message>|<command-args>.*?</command-args>`)
)

// jsonNull is the literal JSON null, compared per line via bytes.Equal to avoid
// a per-line string allocation in the scan hot loop.
var jsonNull = []byte("null")

// ScanResult is one searchable message extracted from a session JSONL. One FTS
// row is inserted per ScanResult (its LineIndex populates vault_fts.line_index).
type ScanResult struct {
	TurnIndex    int       // increments per human-user turn; ordering only
	MessageIndex int       // sequential within a turn (0 = first); ordering only
	LineIndex    int       // 0-based line in the source JSONL; the view-jump anchor
	Role         string    // user | assistant | tool | system
	SubagentID   string    // "" for main session
	ContentText  string    // extracted, sanitized searchable text
	Timestamp    time.Time
}

// ScanOutput is the result of scanning a full session JSONL: the per-message FTS
// results plus the session-level metadata the import pipeline needs.
type ScanOutput struct {
	Results      []ScanResult
	Title        string    // last ai-title, else guarded first-user-message fallback
	CWD          string    // from the first user entry with a cwd field
	Branch       string    // from the first user entry with a gitBranch field
	StartTime    time.Time // first JSONL line with a timestamp
	EndTime      time.Time // last JSONL line with a timestamp
	MessageCount int       // human-text user turns + assistant turns (no tool_result-only users)
}

// ScanSession reads a Claude Code session JSONL from r and extracts searchable
// text for the FTS index. It accepts an io.Reader so it works for both
// import-from-disk (os.Open) and render-from-BLOB (bytes.NewReader). Malformed
// lines are logged and skipped; the scan never fails on bad content.
func ScanSession(r io.Reader) (*ScanOutput, error) {
	return scan(r)
}

// ScanSubagent scans a subagent JSONL the same way as ScanSession and stamps
// every result with subagentID — the anchor that lets the TUI open a subagent
// transcript at a matched line. A subagent file is just another JSONL.
func ScanSubagent(r io.Reader, subagentID string) ([]ScanResult, error) {
	out, err := scan(r)
	if err != nil {
		return nil, err
	}
	for i := range out.Results {
		out.Results[i].SubagentID = subagentID
	}
	return out.Results, nil
}

// scanEntry is an in-order slot collected during pass 1. Assistant snapshots
// sharing a message.id merge into a single slot (blocks deduplicated) so the
// canonical LineIndex is the first snapshot's line.
type scanEntry struct {
	kind      string // entryUser | entryAssistant | entrySystem
	lineIndex int
	timestamp time.Time
	content   json.RawMessage // user: message.content
	blocks    []contentBlock  // assistant: merged, deduplicated blocks
	text      string          // system: pre-composed text (away_summary / pr-link / attachment)
}

const (
	entryUser      = "user"
	entryAssistant = "assistant"
	entrySystem    = "system"
)

func scan(r io.Reader) (*ScanOutput, error) {
	out := &ScanOutput{}
	var entries []scanEntry
	assistantIdx := make(map[string]int) // message.id → index in entries
	var lastAITitle, titleFallback string
	lineIndex := -1

	// Pass 1: read line-by-line, capturing session metadata and building the
	// ordered entry list. Assistant progressive snapshots merge by message.id.
	err := scanLines(r, maxScanLineBytes, func(data []byte, oversize bool) {
		lineIndex++
		if oversize || len(data) == 0 {
			return
		}

		var line jsonlLine
		if err := json.Unmarshal(data, &line); err != nil {
			slog.Warn("vault scanner: skipping malformed JSONL line", "line", lineIndex, "error", err)
			return
		}

		// Infer type from message.role when the top-level type is absent.
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

		// Track first/last timestamps from lines that carry one (ai-title lines
		// have none).
		ts := parseJSONLTime(line.Timestamp)
		if !ts.IsZero() {
			if out.StartTime.IsZero() {
				out.StartTime = ts
			}
			out.EndTime = ts
		}

		switch line.Type {
		case "user":
			if out.CWD == "" && line.CWD != "" {
				out.CWD = line.CWD
			}
			if out.Branch == "" && line.GitBranch != "" {
				out.Branch = line.GitBranch
			}
			if !hasMsg {
				return
			}
			// Title fallback: the first user entry whose content is a plain
			// string (not a tool_result array) and isn't a <…>-prefixed tag.
			if titleFallback == "" {
				if s, ok := asJSONString(msg.Content); ok {
					if t := strings.TrimSpace(s); t != "" && !strings.HasPrefix(t, "<") {
						titleFallback = t
					}
				}
			}
			entries = append(entries, scanEntry{
				kind: entryUser, lineIndex: lineIndex, timestamp: ts, content: msg.Content,
			})

		case "assistant":
			if !hasMsg {
				return
			}
			msgID := msg.ID
			if msgID == "" {
				msgID = line.UUID
			}
			var blocks []contentBlock
			if len(msg.Content) > 0 {
				if err := json.Unmarshal(msg.Content, &blocks); err != nil {
					slog.Warn("vault scanner: skipping malformed assistant content", "line", lineIndex, "error", err)
				}
			}
			if idx, ok := assistantIdx[msgID]; ok {
				mergeBlocks(&entries[idx], blocks)
			} else {
				assistantIdx[msgID] = len(entries)
				entries = append(entries, scanEntry{
					kind: entryAssistant, lineIndex: lineIndex, timestamp: ts, blocks: blocks,
				})
			}

		case "ai-title":
			if line.AITitle != "" {
				lastAITitle = line.AITitle // last wins
			}

		case "pr-link":
			if text := prLinkText(line); text != "" {
				entries = append(entries, scanEntry{
					kind: entrySystem, lineIndex: lineIndex, timestamp: ts, text: text,
				})
			}

		case "attachment":
			if hasMsg {
				if text := attachmentText(msg.Content); text != "" {
					entries = append(entries, scanEntry{
						kind: entrySystem, lineIndex: lineIndex, timestamp: ts, text: text,
					})
				}
			}

		case "system":
			if line.Subtype == "away_summary" {
				if text := strings.TrimSpace(line.Content); text != "" {
					entries = append(entries, scanEntry{
						kind: entrySystem, lineIndex: lineIndex, timestamp: ts, text: text,
					})
				}
			}

			// All other types (custom-title, agent-name, progress,
			// permission-mode, file-history-snapshot, system:turn_duration, …,
			// and anything unknown) are skipped by default — raw_jsonl preserves
			// them regardless.
		}
	})
	if err != nil {
		return nil, fmt.Errorf("reading session: %w", err)
	}

	// Pass 2: walk entries in order, emit one sanitized ScanResult per message.
	turnIndex, messageIndex, emitted := 0, 0, 0
	emit := func(role, text string, lineIdx int, ts time.Time) {
		text = sanitize.StripSecrets(strings.TrimSpace(text))
		if text == "" {
			return
		}
		out.Results = append(out.Results, ScanResult{
			TurnIndex: turnIndex, MessageIndex: messageIndex, LineIndex: lineIdx,
			Role: role, ContentText: text, Timestamp: ts,
		})
		messageIndex++
		emitted++
	}

	for _, e := range entries {
		switch e.kind {
		case entryUser:
			humanText, toolResults := extractUserBlocks(e.content)
			// Only human text starts a new turn; a tool_result-only user entry
			// continues the calling assistant's turn.
			if humanText != "" {
				if emitted > 0 {
					turnIndex++
					messageIndex = 0
				}
				emit(roleUser, humanText, e.lineIndex, e.timestamp)
			}
			for _, tr := range toolResults {
				emit(roleTool, tr, e.lineIndex, e.timestamp)
			}
		case entryAssistant:
			emit(roleAssistant, extractAssistantText(e.blocks), e.lineIndex, e.timestamp)
		case entrySystem:
			emit(roleSystem, e.text, e.lineIndex, e.timestamp)
		}
	}

	for _, r := range out.Results {
		if r.Role == roleUser || r.Role == roleAssistant {
			out.MessageCount++
		}
	}

	// Sanitize the title like FTS content — it is surfaced by `capy vault list`,
	// so a secret in an ai-title or the first user message must not leak there.
	// Sanitize the fallback BEFORE truncation: truncating first could split a
	// secret so StripSecrets' length-floored regex no longer matches the fragment.
	if lastAITitle != "" {
		out.Title = sanitize.StripSecrets(lastAITitle)
	} else if titleFallback != "" {
		out.Title = truncateRunes(sanitize.StripSecrets(titleFallback), titleMaxChars)
	}

	return out, nil
}

// mergeBlocks appends nb's blocks to the entry, skipping duplicates by the
// (Type, Text, Name, ID) tuple — the progressive-snapshot dedup used by
// internal/session/parse.go.
func mergeBlocks(entry *scanEntry, nb []contentBlock) {
	for _, b := range nb {
		dup := false
		for _, eb := range entry.blocks {
			if eb.Type == b.Type && eb.Text == b.Text && eb.Name == b.Name && eb.ID == b.ID {
				dup = true
				break
			}
		}
		if !dup {
			entry.blocks = append(entry.blocks, b)
		}
	}
}

// extractUserBlocks splits a user message into human text (Role=user) and a
// bounded list of tool_result texts (Role=tool). Content is either a plain
// string (human input) or an array of blocks (text and/or tool_result).
func extractUserBlocks(raw json.RawMessage) (humanText string, toolResults []string) {
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
				toolResults = append(toolResults, truncateHeadTail(t, maxToolResultChars))
			}
		}
	}
	return strings.Join(texts, "\n"), toolResults
}

// extractAssistantText keeps text blocks and tool_use summaries; thinking blocks
// are skipped (not a search signal). Assistant entries never carry tool_result.
func extractAssistantText(blocks []contentBlock) string {
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if t := strings.TrimSpace(b.Text); t != "" {
				parts = append(parts, t)
			}
		case "tool_use":
			if s := toolUseSummary(b.Name, b.Input); s != "" {
				parts = append(parts, s)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// toolUseSummary renders a searchable summary for a tool_use block: the file
// path for Read/Edit/Write, the command for Bash, a bounded prompt for
// Agent/Task, and the bare tool name for everything else.
func toolUseSummary(name string, input json.RawMessage) string {
	switch name {
	case "Read", "Edit", "Write":
		if p := jsonStringField(input, "file_path"); p != "" {
			return name + " " + p
		}
	case "Bash":
		if c := jsonStringField(input, "command"); c != "" {
			return "Bash " + c
		}
	case "Agent", "Task":
		if p := jsonStringField(input, "prompt"); p != "" {
			return "Agent " + truncateRunes(p, agentPromptMaxChars)
		}
	}
	return name
}

// toolResultText extracts text from a tool_result's nested content, which is
// either a plain string or an array of blocks. Image/binary blocks are skipped.
func toolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if s, ok := asJSONString(raw); ok {
		return strings.TrimSpace(s)
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			if t := strings.TrimSpace(b.Text); t != "" {
				parts = append(parts, t)
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// prLinkText composes a searchable string from a pr-link entry's fields, e.g.
// "PR #42 owner/repo https://github.com/owner/repo/pull/42".
func prLinkText(line jsonlLine) string {
	var parts []string
	if n := strings.TrimSpace(line.PRNumber.String()); n != "" && n != "0" {
		parts = append(parts, "PR #"+n)
	}
	if line.PRRepository != "" {
		parts = append(parts, line.PRRepository)
	}
	if line.PRURL != "" {
		parts = append(parts, line.PRURL)
	}
	return strings.Join(parts, " ")
}

// attachmentKeys lists content-block fields an attachment line might carry a
// searchable filename/text under.
//
// FIXME(vault, Task 2): the `attachment` line schema is UNVERIFIED — it was
// absent from the documented fields of the 223-session sample, so the keys below
// are a best-effort guess. Extraction is deliberately broad and skip-on-miss (it
// never produces bad data, only possibly nothing). Next step: capture a real
// `attachment` line from live session data and tighten this to the actual
// field(s). design.md JSONL Line Types lists attachment as Extract →
// "Attachment filename for search".
var attachmentKeys = []string{"text", "filename", "fileName", "name", "title", "source", "path"}

// attachmentText pulls a best-effort searchable string from an attachment's
// message content (a block array, or a plain string).
func attachmentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		if s, ok := asJSONString(raw); ok {
			return strings.TrimSpace(s)
		}
		return ""
	}
	var parts []string
	for _, b := range blocks {
		seen := map[string]struct{}{} // dedup values within a block (e.g. filename == name)
		for _, k := range attachmentKeys {
			v, ok := b[k]
			if !ok {
				continue
			}
			s, ok := asJSONString(v)
			if !ok {
				continue
			}
			t := strings.TrimSpace(s)
			if t == "" {
				continue
			}
			if _, dup := seen[t]; dup {
				continue
			}
			seen[t] = struct{}{}
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, " ")
}

// cleanText strips system-reminder and slash-command noise tags, then trims.
func cleanText(text string) string {
	text = sysReminderRe.ReplaceAllString(text, "")
	text = noiseTagRe.ReplaceAllString(text, "")
	return strings.TrimSpace(text)
}

// asJSONString reports whether raw is a JSON string and returns its value.
func asJSONString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// jsonStringField unmarshals raw as an object and returns the string value at
// key, or "" if absent / not a string.
func jsonStringField(raw json.RawMessage, key string) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, _ := asJSONString(v)
	return s
}

// truncateRunes head-truncates s to max runes (rune boundary), appending "…".
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max { // byte length >= rune count, so this short-circuits without allocating
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// truncateHeadTail bounds s to max runes by keeping a 75% head and 25% tail on
// rune boundaries, joined by an ellipsis — keeps both the start (error class,
// command) and the end (final status) of long tool output searchable.
func truncateHeadTail(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	head := max * 3 / 4
	tail := max - head
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}

// parseJSONLTime parses an RFC3339(Nano) timestamp, returning the zero time on a
// missing or unparseable value.
func parseJSONLTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// scanLines invokes fn once per newline-delimited line in r, in order. A line
// longer than maxLineBytes is reported with oversize=true and data=nil (then
// drained to the next newline) so a single giant line does not abort the scan —
// every physical line still advances fn exactly once, which keeps the caller's
// 0-based line index aligned with the source JSONL (the viewer's scroll anchor).
// Returns only genuine read errors (never io.EOF).
//
// The data slice handed to fn aliases an internal read buffer and is valid ONLY
// for the duration of the call — fn MUST copy any bytes it needs to retain. The
// scan callback unmarshals immediately and json.RawMessage copies, so it holds
// nothing past the call; a future caller that defers on data must copy first.
func scanLines(r io.Reader, maxLineBytes int, fn func(data []byte, oversize bool)) error {
	br := bufio.NewReaderSize(r, 64*1024)
	var acc []byte
	dropping := false

	// complete emits the line ending with tail (tail may include the trailing
	// '\n'), then resets accumulation state. A line over the cap — flagged during
	// accumulation (dropping) or detected here for a line that fit the internal
	// buffer but still exceeds maxLineBytes — is reported oversize with nil data.
	// The warn fires exactly once per oversize line.
	complete := func(tail []byte) {
		switch {
		case dropping:
			fn(nil, true) // warn already emitted when dropping was set
		case len(acc)+len(tail) > maxLineBytes:
			slog.Warn("vault scanner: skipping oversize JSONL line for FTS extraction", "max_bytes", maxLineBytes)
			fn(nil, true)
		case len(acc) == 0:
			fn(trimEOL(tail), false)
		default:
			acc = append(acc, tail...)
			fn(trimEOL(acc), false)
		}
		acc, dropping = nil, false
	}

	for {
		chunk, err := br.ReadSlice('\n')
		switch err {
		case nil:
			complete(chunk)

		case bufio.ErrBufferFull:
			// No newline within the internal buffer — accumulate up to the cap,
			// then drop the rest of the line.
			if !dropping {
				if len(acc)+len(chunk) > maxLineBytes {
					dropping = true
					acc = nil
					slog.Warn("vault scanner: skipping oversize JSONL line for FTS extraction", "max_bytes", maxLineBytes)
				} else {
					acc = append(acc, chunk...)
				}
			}

		case io.EOF:
			// Flush a final line that lacked a trailing newline.
			if len(chunk) > 0 || len(acc) > 0 || dropping {
				complete(chunk)
			}
			return nil

		default:
			return err
		}
	}
}

// trimEOL strips a trailing CRLF/LF from a raw line.
func trimEOL(b []byte) []byte {
	b = bytes.TrimSuffix(b, []byte{'\n'})
	b = bytes.TrimSuffix(b, []byte{'\r'})
	return b
}
