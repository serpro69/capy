package vault

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

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

	// The four caps below bound genericInputSummary — the fall-through summary for
	// arbitrary/MCP tools (design.md § Constants). Together they keep FTS rows small
	// and BM25 ranking intact; no single long key, value, or field count can blow
	// past genericSummaryMaxChars.

	// genericMaxFields caps how many fields render before the omitted-field marker.
	genericMaxFields = 6
	// genericKeyMaxChars caps each rendered key (tool/attacker-controlled, otherwise
	// unbounded).
	genericKeyMaxChars = 40
	// genericTokenMaxChars caps each key=value token, applied AFTER sanitization so a
	// secret is never split below the sanitizer's length floor (design.md § Secret
	// handling).
	genericTokenMaxChars = 80
	// genericSummaryMaxChars is the final hard cap on the whole generic portion
	// (reuses the agentPromptMaxChars ceiling).
	genericSummaryMaxChars = agentPromptMaxChars
)

// genericPriorityKeys is a fixed, tool-agnostic salient-key priority list (NOT a
// per-tool registry — see design.md § Not Doing). Present priority keys are emitted
// in THIS order, ahead of the alphabetical fill, so the meaningful fields lead and
// survive the length cap. Matched case-insensitively.
var genericPriorityKeys = []string{
	"query", "queries", "command", "content", "source",
	"url", "path", "pattern", "prompt", "code", "name",
}

var (
	// sysReminderRe / noiseTagRe mirror internal/session/parse.go: strip
	// <system-reminder> blocks and slash-command/local-command noise tags from
	// human text before it enters the FTS index.
	sysReminderRe = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)
	noiseTagRe    = regexp.MustCompile(`(?s)<local-command-caveat>.*?</local-command-caveat>|<local-command-stdout>.*?</local-command-stdout>|<command-name>.*?</command-name>|<command-message>.*?</command-message>|<command-args>.*?</command-args>`)
)

// jsonNull is the literal JSON null, compared per line via bytes.Equal to avoid
// a per-line string allocation in the decode hot loop.
var jsonNull = []byte("null")

// scanLineCap is the per-line byte cap the Claude decoder passes to scanLines
// when its own lineCap is zero (claudeDecoder.Decode). It is a variable
// (initialised to maxScanLineBytes) purely so the golden harness
// (golden_test.go) can lower it and exercise the oversize-line path with a
// small fixture instead of a 16 MB line; production code never reassigns it.
var scanLineCap = maxScanLineBytes

// ScanResult is one searchable message extracted from a session JSONL. One FTS
// row is inserted per ScanResult (its LineIndex populates vault_fts.line_index).
type ScanResult struct {
	TurnIndex    int    // increments per human-user turn; ordering only
	MessageIndex int    // sequential within a turn (0 = first); ordering only
	LineIndex    int    // 0-based line in the source JSONL; the view-jump anchor
	Role         string // user | assistant | tool | system
	SubagentID   string // "" for main session
	ContentText  string // extracted, sanitized searchable text
	Timestamp    time.Time
	// ToolNames lists the tool_use names on an assistant row, in block order
	// (nil for other roles). Consumed by the vault chunker's title builder
	// (chunker.go); it is NOT persisted to vault_fts.
	ToolNames []string
}

// ScanOutput is the result of scanning a full session: the per-message FTS
// results plus the session-level metadata the import pipeline needs. The
// Platform, PlatformID, ParentUUID and Source fields are copied verbatim from
// the decoder's Meta so import can persist the platform and parent link from
// the single decode and check PlatformID against the filename uuid; Source is
// not persisted in v1 (design § Consumer contracts).
type ScanOutput struct {
	Results      []ScanResult
	Title        string    // sanitized ExplicitTitle, else sanitized+truncated TitleFallback
	CWD          string    // Meta.CWD
	Branch       string    // Meta.Branch
	StartTime    time.Time // Meta.StartTime
	EndTime      time.Time // Meta.EndTime
	MessageCount int       // user + assistant rows (tool_result-only user lines do not count)
	Platform     Platform  // Meta.Platform — the decoder that produced this output
	PlatformID   string    // Meta.PlatformID — the platform's own session id; "" for Claude
	ParentUUID   string    // Meta.ParentUUID — parent session of a child rollout; "" for Claude
	Source       string    // Meta.Source — platform session origin; "" for Claude
}

// ScanSession decodes a session archived from platform p and extracts
// searchable text for the FTS index: DecoderFor(p).Decode then ScanTranscript.
// It accepts an io.Reader so it works for both import-from-disk (os.Open) and
// render-from-BLOB (bytes.NewReader). The decoder never fails on bad content
// (malformed lines are logged and skipped — ADR-021); the only errors are a
// genuine read error, wrapped by the decoder, or an unusable platform value
// (ErrUnknownPlatform / ErrDecoderUnavailable, see DecoderFor).
func ScanSession(p Platform, r io.Reader) (*ScanOutput, error) {
	t, err := DecoderFor(p).Decode(r)
	if err != nil {
		return nil, err
	}
	return ScanTranscript(t), nil
}

// ScanSubagent scans a Claude Code subagent sidecar the same way as ScanSession
// and stamps every result with subagentID — the anchor that lets the TUI open a
// subagent transcript at a matched line. Sidecars are a Claude concept (a
// sidecar is just another Claude JSONL), so this always uses the Claude decoder.
func ScanSubagent(r io.Reader, subagentID string) ([]ScanResult, error) {
	t, err := claudeDecoder{}.Decode(r)
	if err != nil {
		return nil, err
	}
	out := ScanTranscript(t)
	for i := range out.Results {
		out.Results[i].SubagentID = subagentID
	}
	return out.Results, nil
}

// ScanTranscript is the FTS consumer over the transcript model: it walks the
// decoded entries in order and emits one sanitized ScanResult per message. This
// is where every SCANNER policy lives (the decoder is pre-policy — see
// transcript_model.go):
//
//   - A Human entry starts a new turn; a ToolResult continues the calling
//     assistant's turn (the shared turn heuristic — explicit platform turn ids
//     are not used).
//   - A ToolResult whose CallName is in ftsExcludedResult is dropped (the call
//     stays searchable on the assistant row), as is one with an empty Body (the
//     decoder emits those for their Diff — D18 — which the scanner does not
//     index). Every other body is prefixed with its CallSummary and THEN
//     head/tail-bounded to maxToolResultChars, so the label survives in the head.
//   - An Assistant row's text is its text parts and its ToolCall.Summary values
//     joined by "\n" in part order, so a tool-only assistant entry is still an
//     assistant row and counts in MessageCount. ToolNames carries the call names
//     in order for the chunker's title.
//   - System entries are indexed, SearchOnly ones included (D7 — the scanner is
//     the one consumer that sees them).
//   - Every emitted text is TrimSpace'd then sanitize.StripSecrets'ed; the title
//     is sanitized (and the fallback truncated AFTER sanitizing) the same way.
//
// Output for Claude sessions is byte-identical to the pre-model scanner
// (golden_test.go, parity_canary_test.go).
//
// t must be non-nil: every Decoder returns a non-nil *Transcript on a nil error
// (transcript_model.go), and ScanSession / ScanSubagent only call this after
// that check. An entry of unknown Kind is skipped with a warning — it can only
// come from a decoder bug, never from valid input.
func ScanTranscript(t *Transcript) *ScanOutput {
	out := &ScanOutput{
		CWD:        t.Meta.CWD,
		Branch:     t.Meta.Branch,
		StartTime:  t.Meta.StartTime,
		EndTime:    t.Meta.EndTime,
		Platform:   t.Meta.Platform,
		PlatformID: t.Meta.PlatformID,
		ParentUUID: t.Meta.ParentUUID,
		Source:     t.Meta.Source,
	}

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

	for _, e := range t.Entries {
		switch e.Kind {
		case EntryHuman:
			// Only human text starts a new turn; a tool_result-only user line
			// (no Human entry) continues the calling assistant's turn.
			if emitted > 0 {
				turnIndex++
				messageIndex = 0
			}
			emit(roleUser, e.Text, e.LineIndex, e.Timestamp)

		case EntryToolResult:
			if ftsExcludedResult(e.CallName) {
				continue // dump / boilerplate body — excluded from FTS (call stays searchable on the assistant row)
			}
			if e.Body == "" {
				continue // emitted by the decoder for its Diff (D18); nothing to index
			}
			// Prefix the call summary BEFORE truncation so it survives in the
			// 75%-head of a long result.
			//
			// TODO(codex-vault-sessions, after Slice 4): this truncates BEFORE emit's
			// StripSecrets — the opposite of the title path below (sanitize, then
			// truncate). A secret straddling the head/tail cut of a >16 KiB body is
			// split into fragments the length-floored redaction regex may miss. It
			// is pre-existing behaviour (the deleted extractUserBlocks did the same)
			// and is kept for the byte-identical gate; fixing it changes FTS bytes
			// and must land as its own golden-regenerating change. See
			// implementation.md § Deferred work #7.
			body := prefixToolResult(e.CallSummary, e.Body)
			emit(roleTool, truncateHeadTail(body, maxToolResultChars), e.LineIndex, e.Timestamp)

		case EntryAssistant:
			before := len(out.Results)
			emit(roleAssistant, assistantRowText(e.Parts), e.LineIndex, e.Timestamp)
			// Attach tool names to the row just emitted (emit skips empty text,
			// so guard on growth). Title-building metadata only — see ScanResult.
			if len(out.Results) > before {
				out.Results[len(out.Results)-1].ToolNames = partToolNames(e.Parts)
			}

		case EntrySystem:
			emit(roleSystem, e.Text, e.LineIndex, e.Timestamp)

		default:
			// Fail loud, not silent: a Kind this consumer does not know (including
			// the EntryUnknown zero value) is a decoder bug, and dropping the entry
			// without a trace would hide it. Output is unaffected for valid input.
			slog.Warn("vault scanner: skipping transcript entry of unknown kind",
				"platform", t.Meta.Platform, "line", e.LineIndex, "kind", int(e.Kind))
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
	if t.Meta.ExplicitTitle != "" {
		out.Title = sanitize.StripSecrets(t.Meta.ExplicitTitle)
	} else if t.Meta.TitleFallback != "" {
		out.Title = truncateRunes(sanitize.StripSecrets(t.Meta.TitleFallback), titleMaxChars)
	}

	return out
}

// assistantRowText composes an assistant FTS row from its ordered parts: text
// parts verbatim (already TrimSpace'd by the decoder) and each call's Summary,
// joined by "\n" in part order. A call with an empty Summary contributes
// nothing. Launches (Task/Agent) contribute their "Agent <prompt>" summary like
// any other call — the scanner has no marker concept (D19).
func assistantRowText(parts []Part) string {
	var texts []string
	for _, p := range parts {
		switch {
		case p.Call != nil:
			if p.Call.Summary != "" {
				texts = append(texts, p.Call.Summary)
			}
		case p.Text != "":
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// partToolNames returns the tool-call names of an assistant entry in part order
// (nil when it has none). Feeds the vault chunker's BM25 title
// (buildVaultChunkTitle); duplicates are kept — the title builder dedups. A
// nameless call is skipped.
func partToolNames(parts []Part) []string {
	var names []string
	for _, p := range parts {
		if p.Call != nil && p.Call.Name != "" {
			names = append(names, p.Call.Name)
		}
	}
	return names
}

// toolCall is the correlated info for a tool_use, keyed by its id and matched to
// the later tool_result that references it. name drives the result-exclusion
// policy (excludedResultTools); summary is the searchable/display label
// ("Read /path", "Bash <cmd>").
type toolCall struct {
	name    string
	summary string
}

// excludedResultTools names the tools whose tool_result BODY is dropped from the
// FTS index (scanner) and collapsed in the rendered transcript (render/transcript).
// Their output is a file/cell content dump — Read returns file contents, the
// legacy NotebookRead returns notebook cells+outputs — that is high-volume,
// low-signal for conversation search and already lives on disk/git. The tool CALL
// summary ("Read /path") is still indexed on the assistant tool_use row, so
// finding a session by what it read still works; raw_jsonl keeps the body verbatim
// for `vault show --format json` and restore.
//
// To exclude another tool, add its name here (within an unreleased version, no
// index_version bump is needed — see currentIndexVersion). Bash, Grep, etc. are
// deliberately NOT excluded — their output (errors, command results) is unique,
// non-reproducible signal worth searching.
var excludedResultTools = map[string]bool{
	"Read":         true,
	"NotebookRead": true,
}

// diffResultTools names the tools whose tool_result BODY is a one-line success
// string ("The file X has been updated successfully") while the meaningful change —
// a unified-diff structuredPatch — lives in the sibling toolUseResult field
// (design.md § Addenda A3). Their result bodies are FTS-excluded for the same reason
// as excludedResultTools (the success string is identical boilerplate per edit, pure
// search noise; the "Edit /path" call stays searchable on the assistant row), but
// they are NOT in excludedResultTools because their DISPLAY differs: `vault show`
// keeps the success body verbatim, and the TUI viewer collapses them to a marker
// that expands to the rendered diff (not the raw body). To add another (e.g.
// MultiEdit), put it here. Edit/Write are kept distinct from Read so the display
// split stays explicit.
var diffResultTools = map[string]bool{
	"Edit":  true,
	"Write": true,
}

// ftsExcludedResult reports whether a tool's tool_result BODY is dropped from the
// FTS index: dump tools (Read/NotebookRead) and diff tools (Edit/Write). In both
// cases the body is low-signal (a file/cell dump, or boilerplate success text) and
// the call summary remains indexed on the assistant tool_use row.
func ftsExcludedResult(name string) bool {
	return excludedResultTools[name] || diffResultTools[name]
}

// collectToolUseSummaries records, for each tool_use block in blocks, the call's
// name + summary (tool name + key inputs, via toolUseSummary) keyed by the block's
// id — the tool_use_id a later tool_result block references. Used by the render
// and transcript readers' own pass 1 to associate a result with its call and to
// apply the excludedResultTools policy; the scanner now receives the correlation
// from the decoder (ToolResult.CallName / CallSummary). Deleted with those loops
// in codex-vault-sessions Slice 4.
func collectToolUseSummaries(blocks []contentBlock, into map[string]toolCall) {
	for _, b := range blocks {
		if b.Type == "tool_use" && b.ID != "" {
			into[b.ID] = toolCall{name: b.Name, summary: toolUseSummary(b.Name, b.Input)}
		}
	}
}

// prefixToolResult prepends a tool-call label to a tool_result body, separated by
// a newline, so the result carries the context of the call that produced it. An
// empty label (unknown/missing tool_use_id) leaves the body unchanged.
func prefixToolResult(label, body string) string {
	if label == "" {
		return body
	}
	return label + "\n" + body
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
	// All other tools (MCP, WebFetch, ToolSearch, custom, …) fall through to a
	// bounded, sanitized key=value summary of the input object (design.md § Chosen
	// approach; the switch has no default: clause). genericInputSummary returns "" for
	// a non-object/empty/malformed input, in which case we emit the bare name — the
	// unchanged legacy behaviour.
	if s := genericInputSummary(input); s != "" {
		return name + " " + s
	}
	return name
}

// genericInputSummary renders a bounded, sanitized `key=value` summary of an
// arbitrary tool_use input object — the fall-through for tools toolUseSummary does
// not special-case (MCP, WebFetch, ToolSearch, custom). It returns "" for an empty,
// non-object, or malformed input so the caller emits the bare tool name (graceful
// degradation, never an error — matches the "unknown id → omit prefix" ethos).
//
// Bounding is load-bearing: arbitrary tool inputs are free-form JSON, and indexing
// them verbatim would bloat FTS rows and degrade BM25 ranking. Four caps
// (genericMaxFields, genericKeyMaxChars, genericTokenMaxChars, genericSummaryMaxChars)
// keep the output small. Each token is sanitized BEFORE truncation so a secret is
// never split below sanitize.StripSecrets' length floor / away from its closing
// </private> tag (design.md § Secret handling); nested objects render as {…} (no
// descent) so nested credential shapes never surface.
func genericInputSummary(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(input, &m); err != nil || len(m) == 0 {
		return "" // non-object, empty, or malformed → bare name
	}

	selected := selectGenericKeys(m)
	omitted := len(m) - len(selected)

	tokens := make([]string, 0, len(selected)+1)
	for _, k := range selected {
		tokens = append(tokens, renderGenericToken(k, m[k]))
	}
	if omitted > 0 {
		// Trailing +N marker so the output never looks complete when it isn't.
		tokens = append(tokens, fmt.Sprintf("+%d", omitted))
	}
	// Sanitize the JOINED summary once more before the final cap. Per-token
	// StripSecrets (renderGenericToken) cannot catch a secret that straddles two
	// fields — notably a <private>…</private> span whose opening tag is in one
	// field's token and closing tag in another's (privateTagRe needs both tags in
	// one string). The FTS path is backstopped by the emit-boundary StripSecrets,
	// but the display path (vault show / TUI) is not, so this in-function pass is
	// what keeps generic inputs redacted on display too (design.md § Secret
	// handling). Sanitize-before-truncate, as everywhere else — a cross-field span
	// is redacted while whole, and the final cap only ever trims clean text.
	return truncateRunes(sanitize.StripSecrets(strings.Join(tokens, " ")), genericSummaryMaxChars)
}

// selectGenericKeys picks the fields genericInputSummary renders, in order: present
// priority keys (case-insensitive, in genericPriorityKeys order), then the remaining
// keys alphabetically, capped at genericMaxFields. Priority ordering ensures the
// salient fields lead so they survive the length cap; a salient field absent from the
// priority list still gets in via the alphabetical fill — it is only ordered later or
// (past the cap) counted in the omitted marker, never dropped silently.
func selectGenericKeys(m map[string]json.RawMessage) []string {
	// Lower-cased key → actual key, for case-insensitive priority matching. On a
	// case collision (e.g. "Query" and "query") keep the lexicographically smaller
	// actual key so the pick is deterministic — map iteration order is randomized, and
	// a non-deterministic pick would make the same input yield different FTS rows.
	lower := make(map[string]string, len(m))
	for k := range m {
		lk := strings.ToLower(k)
		if cur, ok := lower[lk]; !ok || k < cur {
			lower[lk] = k
		}
	}
	selected := make([]string, 0, genericMaxFields)
	used := make(map[string]bool, len(m))
	for _, pk := range genericPriorityKeys {
		if len(selected) >= genericMaxFields {
			break
		}
		if actual, ok := lower[pk]; ok && !used[actual] {
			selected = append(selected, actual)
			used[actual] = true
		}
	}
	rest := make([]string, 0, len(m))
	for k := range m {
		if !used[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		if len(selected) >= genericMaxFields {
			break
		}
		selected = append(selected, k)
	}
	return selected
}

// renderGenericToken builds one `key=value` token: key sanitized-then-truncated,
// value type-directed (see renderGenericValue), then the FULL token sanitized, THEN
// truncated (order is load-bearing — design.md § Secret handling), then control chars
// normalized to spaces so the summary stays a single stable line.
//
// The key is sanitized BEFORE its own genericKeyMaxChars cap for the same reason the
// value is: keys are attacker/tool-controlled, so a >40-char key carrying a
// <private>…</private> span would otherwise lose its closing tag to the cap before
// StripSecrets ran, leaking the opening fragment on the (unsanitized) display path.
// The second StripSecrets over the whole token is a harmless backstop (placeholders
// don't re-match) that also catches a secret straddling the key=value boundary.
func renderGenericToken(key string, raw json.RawMessage) string {
	token := truncateRunes(sanitize.StripSecrets(key), genericKeyMaxChars) + "=" + renderGenericValue(raw)
	token = sanitize.StripSecrets(token)
	token = truncateRunes(token, genericTokenMaxChars)
	return normalizeControlChars(token)
}

// renderGenericValue renders a tool-input value by type: string → unquoted raw;
// nested object → the placeholder "{…}" (no descent); array → compact JSON with every
// non-scalar element replaced by a {…}/[…] placeholder first (all-scalar arrays stay
// ordinary compact JSON); number/bool/null → literal via json.Compact. json.Compact on
// every non-string value prevents source-JSONL whitespace from widening a token.
func renderGenericValue(raw json.RawMessage) string {
	if s, ok := asJSONString(raw); ok {
		return s
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ""
	}
	switch trimmed[0] {
	case '{':
		return "{…}" // nested object: no descent
	case '[':
		return renderGenericArray(trimmed)
	default:
		return compactJSON(trimmed) // number / bool / null
	}
}

// renderGenericArray compacts a JSON array, replacing every non-scalar element
// (object or nested array) with a {…}/[…] placeholder so nested contents never
// surface. An all-scalar array therefore renders as ordinary compact JSON.
func renderGenericArray(raw json.RawMessage) string {
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		return "[…]"
	}
	parts := make([]string, len(elems))
	for i, el := range elems {
		t := bytes.TrimSpace(el)
		switch {
		case len(t) == 0:
			parts[i] = "null"
		case t[0] == '{':
			parts[i] = "{…}"
		case t[0] == '[':
			parts[i] = "[…]"
		default:
			parts[i] = compactJSON(t) // scalar element
		}
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// compactJSON returns raw with insignificant whitespace removed, falling back to the
// raw bytes if it is not valid JSON (should not happen for values unmarshalled above).
func compactJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}

// normalizeControlChars replaces every control rune (newline, tab, etc.) with a
// single space so an unquoted value cannot inject a newline into the one-line summary.
func normalizeControlChars(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
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

// queuedCommandPrompt returns the user prompt carried by a `queued_command`
// attachment (design.md § Addenda A2), or "" for any other attachment. When a
// user submits a message while the assistant is mid-turn, Claude Code records the
// text in a top-level `attachment` object — NOT under `message` — so it is
// invisible to the message.content parsers and would be lost from both search and
// display. Surfacing it here recovers the in-flight prompt as a normal user turn.
//
// The bracketing `queue-operation` enqueue/remove lines carry the same text but
// are operational metadata (no parser handles their type), so the queued_command
// attachment — positioned in conversation order at the point the message was
// actually processed — is the single, non-duplicated representation. A message
// that is enqueued but never dequeued (session ended while queued) has no
// queued_command line; it was never sent to the model, so it is intentionally not
// surfaced.
func queuedCommandPrompt(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var a struct {
		Type   string `json:"type"`
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return ""
	}
	if a.Type != "queued_command" {
		return ""
	}
	return strings.TrimSpace(a.Prompt)
}

// userTextContent wraps plain text as the JSON message.content (a quoted string)
// the user-entry parsers expect, normalizing a queued_command prompt (A2) into
// its equivalent user message so the render/transcript readers handle it through
// their existing user path (the decoder sets Entry.Queued directly; this helper
// goes with the reader loops in Slice 4). json.Marshal of a
// concrete string type is specified never to return an error, so the discard is
// safe (matching the json.Marshal convention in internal/store/chunk.go).
func userTextContent(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// attachmentKeys lists content-block fields an attachment line might carry a
// searchable filename/text under.
//
// NOTE(vault, A2): real `attachment` lines carry a top-level `attachment` object
// (NOT a `message`). Known content-bearing subtypes (per a 223-session taxonomy):
// `queued_command` (the in-flight user prompt — recovered as a user turn via
// queuedCommandPrompt above), plus `task_reminder`, `file`, `directory`, and
// `hook_additional_context`. Only `queued_command` is handled today (it is the
// A2 target: a lost USER message); the others are out of A2 scope — a `directory`
// or `file` attachment is context the model received, not a human turn, so
// recovering them is a separate, deliberate follow-up (decide role + dedup vs the
// tool call that triggered them before indexing). The message.content path below
// is a best-effort fallback for any message-bearing variant (broad, skip-on-miss:
// it never produces bad data, only possibly nothing).
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
