package vault

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// transcript_model.go defines the vault's single format seam: a Decoder turns a
// session's archived bytes into a Transcript — an ordered, platform-agnostic
// list of entries plus session metadata. Everything above the seam (the FTS
// scanner, the `show` renderer, the TUI transcript, chunking, retrieval) is
// platform-blind; everything below it (discovery, decoders) is per-platform. A
// third agent CLI is one Discoverer + one Decoder + one Platform constant.
//
// The contract is PRE-POLICY. A decoder does format work only: line parsing,
// noise-tag stripping, progressive-snapshot merging, call↔result correlation,
// diff extraction. It never sanitizes secrets, truncates, excludes tool bodies
// from search, or decides what collapses in a viewer — those are consumer
// policies, and they differ per consumer (the scanner strips secrets and bounds
// bodies; `show` and the TUI render the user's own archive verbatim). The one
// deliberate exception is the in-label sanitisation inside genericInputSummary,
// which is part of how a tool-call Summary is built and is shared by every
// consumer today (DIVERGENCES.md D22).
//
// See docs/feat/wip/codex-vault-sessions/design.md § The Transcript Model and
// internal/vault/testdata/golden/DIVERGENCES.md for the field-by-field mapping
// from today's three Claude readers.

// Transcript is a decoded session: session-level Meta plus entries in source
// order. Entries is nil for an empty blob.
type Transcript struct {
	Meta    Meta
	Entries []Entry
}

// Meta is the session-level metadata a decoder extracts. Text fields are
// UNTRUNCATED and UNSANITIZED — the scanner applies sanitize → truncate when it
// derives the stored title (ExplicitTitle first, else TitleFallback).
type Meta struct {
	// Platform is the decoder's platform constant.
	Platform Platform
	// PlatformID is the platform's own id for this session as recorded INSIDE
	// the file (Codex session_meta.id). Informational: the vault row key is
	// always the filename uuid, and import warns when the two differ. Empty for
	// Claude (not extracted today; keeps the refactor byte-identical).
	PlatformID string
	// ExplicitTitle is a platform-recorded title (Claude: the last ai-title).
	// Empty when the platform records none.
	ExplicitTitle string
	// TitleFallback is the decoder-chosen fallback title text. The rule is
	// platform-specific and load-bearing for byte-identical Claude output: the
	// first `user` entry whose content is a PLAIN JSON STRING, TrimSpace'd,
	// non-empty and not `<`-prefixed — taken raw (no cleanText), never from a
	// block-array text or a queued prompt (DIVERGENCES.md D1).
	TitleFallback string
	// CWD and Branch locate the project. Claude: the first user-typed line
	// carrying cwd / gitBranch, even a message-less one (D3).
	CWD    string
	Branch string
	// StartTime / EndTime are the first / last parseable timestamps across
	// EVERY valid-JSON line, including line types that produce no entry (D4).
	StartTime time.Time
	EndTime   time.Time
	// ParentUUID is the parent session of a child rollout (Codex sub-agents).
	// Empty for Claude, whose sub-agents are sidecar files, not sessions.
	ParentUUID string
	// Source is the platform's session origin as a string (Codex: cli | exec |
	// vscode | mcp | subagent | …). Empty for Claude. Not persisted in v1.
	Source string
}

// EntryKind discriminates Entry payloads. The zero value is invalid so a
// forgotten Kind cannot masquerade as a Human entry.
type EntryKind int

const (
	// EntryUnknown is the invalid zero value.
	EntryUnknown EntryKind = iota
	// EntryHuman is a human prompt (Text, Queued).
	EntryHuman
	// EntryAssistant is a model turn: ordered Parts of text and tool calls.
	EntryAssistant
	// EntryToolResult is one tool result, correlated to its call (CallID,
	// CallName, CallSummary, Body, Diff).
	EntryToolResult
	// EntrySystem is platform-injected text (Text, SearchOnly).
	EntrySystem
)

// String returns the kind's name for logs and test output.
func (k EntryKind) String() string {
	switch k {
	case EntryHuman:
		return "human"
	case EntryAssistant:
		return "assistant"
	case EntryToolResult:
		return "tool_result"
	case EntrySystem:
		return "system"
	}
	return fmt.Sprintf("EntryKind(%d)", int(k))
}

// Entry is one unit of a transcript. Kind selects which of the payload groups
// below is populated; the others are zero.
//
// A decoder never emits an entry with nothing in it — a Human with empty Text,
// an Assistant with no Parts or a System with empty Text is dropped at decode
// time — EXCEPT a ToolResult, which is emitted even with an empty Body because
// its Diff may still carry the change (D18: an Edit result whose success body is
// empty but whose structuredPatch is not). Consumers must still tolerate text
// that becomes empty under their own policy (e.g. after secret stripping).
type Entry struct {
	// Kind is the payload discriminator.
	Kind EntryKind
	// LineIndex is the 0-based PHYSICAL line of the originating record in the
	// source JSONL — the FTS line_index and the viewer's scroll anchor. For a
	// Claude assistant merged from progressive snapshots it is the FIRST
	// snapshot's line. Oversize and malformed lines still advance it.
	LineIndex int
	// Timestamp is the originating line's timestamp (a merged snapshot's first
	// line); zero when the line carries none.
	Timestamp time.Time

	// --- EntryHuman / EntrySystem ---

	// Text is the entry text with platform noise already stripped (Claude:
	// <system-reminder> and command tags removed via cleanText). For a System
	// entry it is the pre-composed text (pr-link, away_summary, attachment).
	Text string
	// Queued marks a Human entry recovered from a Claude queued_command
	// attachment — a prompt submitted while the assistant was mid-turn (A2).
	Queued bool

	// --- EntryAssistant ---

	// Parts holds the assistant message's text parts and tool calls IN BLOCK
	// ORDER. Order is load-bearing: every consumer interleaves text and call
	// summaries in this order (the scanner's assistant row text, `show`'s arrow
	// lines, the TUI body), so a {Text, []ToolCall} split would reorder any
	// message that places text after a call. Text parts are TrimSpace'd and
	// non-empty; thinking blocks are not parts.
	Parts []Part

	// --- EntryToolResult ---

	// CallID is the result's tool_use_id / call_id as written; CallName and
	// CallSummary are resolved by the decoder from the matching call anywhere
	// in the transcript (correlation is whole-transcript, not positional). An
	// unmatched id leaves both empty — consumers then render the body
	// unprefixed, as today.
	CallID      string
	CallName    string
	CallSummary string
	// Body is the verbatim, untruncated result text (a string, or the text
	// blocks of a block array joined by "\n", trimmed; image blocks skipped).
	Body string
	// Diff is the unified-diff view of the change a result applied, when the
	// platform records one (Claude: toolUseResult.structuredPatch on the FIRST
	// diff-tool result of a line — D15; Codex: the apply_patch input, on
	// success only). Nil when there is none.
	Diff *Diff

	// --- EntrySystem ---

	// SearchOnly marks a System entry the scanner indexes but `show` and the
	// TUI do not display. It preserves an asymmetry that exists today for
	// generic Claude `attachment` message content (D7); unifying the three
	// surfaces is a recorded follow-up (implementation.md § Deferred).
	SearchOnly bool
}

// Part is one element of an assistant message: either a text part (Call == nil)
// or a tool call (Call != nil). Exactly one of the two is set.
type Part struct {
	// Text is the part's text (TrimSpace'd, non-empty) when Call is nil.
	Text string
	// Call is the tool call when this part is one.
	Call *ToolCall
}

// IsCall reports whether the part is a tool call rather than text.
func (p Part) IsCall() bool { return p.Call != nil }

// ToolCall is a tool invocation inside an assistant message.
type ToolCall struct {
	// ID is the platform's call id (Claude tool_use.id, Codex call_id). May be
	// empty; only non-empty ids participate in result correlation.
	ID string
	// Name is the tool name as given (Read, Bash, exec_command, an MCP name…).
	Name string
	// Summary is the platform label consumers index and display: "Bash <cmd>",
	// "Read /path", "Agent <prompt≤200>", "exec_command <cmd>", or the bare name
	// (with a bounded key=value summary for generic inputs). May be empty for a
	// nameless call with no summarisable input; consumers skip empty summaries.
	Summary string
	// Input is the raw call input JSON, kept for future generic rendering
	// (ADR-025 § Deferred). Nil when the platform records none.
	Input json.RawMessage
	// Launch marks a sub-agent spawn (Claude Task/Agent, Codex spawn_agent).
	// Nil for every other call.
	Launch *Launch
}

// Launch describes a sub-agent spawn on a ToolCall.
type Launch struct {
	// Label is the short human label for the launch marker (Claude:
	// subagentLaunchLabel — description › subagent_type › prompt ≤ 100 runes,
	// else "subagent"). It is computed separately from ToolCall.Summary because
	// the two use different source keys and caps (D19).
	Label string
	// ChildUUID is the spawned child SESSION's uuid when the platform records
	// it (Codex). Empty for Claude, whose markers map onto sidecar ids by count
	// in the transcript consumer (D23). A marker with ChildUUID is openable.
	ChildUUID string
}

// Diff is unified-diff text plus its add/remove counts for a marker stat.
type Diff struct {
	// Text is unified-diff text. Claude (diff.go): one `@@ -a,b +c,d @@` header
	// per hunk followed by the hunk's prefixed lines. Codex (codex_patch.go): the
	// patch's own `*** Add/Update/Delete File:` section lines, an exact numbered
	// header for file adds, and Codex's line-number-less `@@ [hint]` header for
	// update hunks. Consumers must key only on the line prefixes — `@@` for a
	// hunk header, '+', '-', ' ' for hunk lines, anything else plain — which is
	// exactly what the TUI's renderDiffBody does; the prefixes are preserved
	// verbatim so it can colour by first byte.
	Text    string
	Added   int
	Removed int
}

// Decoder turns a session's archived bytes into a Transcript. Implementations
// are pure and pre-policy (see the file comment) and never fail on bad CONTENT:
// a malformed line is skipped (logged), an oversize line advances LineIndex
// without content (the scanLines contract), unknown record types are ignored
// (ADR-021). The only returned errors are genuine read errors from r.
type Decoder interface {
	Decode(r io.Reader) (*Transcript, error)
}

// ErrDecoderUnavailable is returned by the Decoder DecoderFor hands out for a
// known platform whose decoder is not implemented in this binary.
var ErrDecoderUnavailable = errors.New("no decoder available for platform")

// DecoderFor returns the Decoder for p. It never returns nil: an unknown value
// yields a decoder whose Decode fails with ErrUnknownPlatform, and a known
// platform without an implementation yields one failing with
// ErrDecoderUnavailable — so a caller that dispatches on a stored platform
// value gets a per-session error, never a nil dereference or a silent Claude
// default. Both known platforms have decoders today; the ErrDecoderUnavailable
// arm is the contract for a platform constant added ahead of its decoder.
func DecoderFor(p Platform) Decoder {
	switch p {
	case PlatformClaudeCode:
		return claudeDecoder{}
	case PlatformCodex:
		return codexDecoder{}
	}
	for _, k := range knownPlatforms {
		if p == k {
			return unavailableDecoder{err: fmt.Errorf("%w: %s", ErrDecoderUnavailable, p)}
		}
	}
	return unavailableDecoder{err: fmt.Errorf("%w: %q", ErrUnknownPlatform, string(p))}
}

// unavailableDecoder is the erroring Decoder DecoderFor returns for platforms
// it cannot decode (see DecoderFor).
type unavailableDecoder struct {
	err error
}

var _ Decoder = unavailableDecoder{}

// Decode always fails with the decoder's configured error.
func (d unavailableDecoder) Decode(io.Reader) (*Transcript, error) {
	return nil, d.err
}
