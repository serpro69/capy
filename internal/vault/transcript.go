package vault

import (
	"fmt"
	"log/slog"
	"strings"
)

// transcript.go is the TUI viewer's consumer over the transcript model. Like
// render.go (`vault show`) it decodes a session with the platform's Decoder and
// renders faithfully and unsanitized (the viewer shows the user's own local
// archive verbatim); unlike render.go it records each message's source-line
// anchor, makes the collapse decision explicit (A1), carries reconstructed
// diffs (A3) and surfaces subagent launch points as separate marker messages so
// the TUI can scroll to a search hit (line_index) and open a subagent — or, for
// a platform that records the child's uuid, a child session — standalone.
//
// It lives in package vault, not internal/vault/tui, because the transcript
// model and the display policy helpers (excludedResultTools, prefixToolResult,
// …) are here and unexported; the tui package stays pure presentation
// (lipgloss + scrolling) over the TranscriptMessage slice this returns.

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

// collapseToolResultLines / collapseToolResultBytes bound an inline tool_result in
// the TUI viewer (design.md § Addenda A1): a RoleTool body exceeding either is
// collapsed to a focusable, openable marker that expands on demand. Excluded-tool
// results (excludedResultTools — Read/NotebookRead) collapse regardless of size.
// Plain `vault show` is unaffected: it renders via render.go's displayMessages,
// not ParseTranscript.
//
// The values are rough "would a reader rather page past this?" heuristics — ~20
// lines is about a viewport, ~2000 bytes catches a verbose single-line blob.
// Constants for now; validate against real sessions before exposing as config.
const (
	collapseToolResultLines = 20
	collapseToolResultBytes = 2000
)

// TranscriptMessage is one renderable unit for the viewer. Body is the composed,
// unsanitized display text (may be multi-line). SourceLine is the 0-based line of
// the originating entry in this transcript's source JSONL — the canonical/first
// line for a deduplicated assistant snapshot, matching the FTS scanner's
// line_index, so a search hit scrolls to the same place. AgentID/ChildUUID/
// Openable are set only on RoleSubagent markers; Collapsed/ToolSummary only on
// RoleTool messages.
type TranscriptMessage struct {
	Role       string
	Body       string
	SourceLine int
	AgentID    string // RoleSubagent only: the mapped Claude subagent sidecar id ("" when unmatched)
	Openable   bool   // RoleSubagent only: AgentID resolves to an archived sidecar, or ChildUUID is set

	// ChildUUID is the spawned child SESSION's uuid when the platform records it
	// on the launch (Codex spawn_agent → Launch.ChildUUID). A marker with a
	// ChildUUID is Openable as a session, not a sidecar; the tui root model
	// resolves it through the store (design § TUI). Empty for Claude, whose
	// markers map onto sidecar ids by count (AgentID). Omitted from JSON when
	// empty so the Claude transcript goldens (golden_test.go) are unchanged.
	ChildUUID string `json:",omitempty"`

	// Collapsed marks a RoleTool message the viewer renders as a focusable,
	// openable marker (expand-on-demand) rather than inline — an excluded-tool body
	// or one over the collapseToolResult* thresholds (A1). Body still carries the
	// full result text for the open target; ToolSummary is the compact call label
	// ("Read /path", "Bash <cmd>") shown on the marker row.
	Collapsed   bool
	ToolSummary string

	// Diff marks a RoleTool message whose Body is reconstructed unified-diff text
	// from an Edit/Write structuredPatch (A3) rather than a raw tool_result body.
	// The viewer colors it by line prefix on expand (tui render.go renderDiffBody)
	// and the marker shows a "(+a −b)" stat (baked into ToolSummary) instead of a
	// line count. Set only alongside Collapsed on a diff-tool result.
	Diff bool

	// Queued marks a RoleUser message recovered from a queued_command attachment
	// (A2) — a prompt the user submitted while the assistant was mid-turn. The
	// viewer annotates its header "· queued"; it is otherwise a normal user turn.
	Queued bool
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

// ParseTranscript decodes a session (or Claude subagent sidecar) archived from
// platform p into ordered display messages for the TUI viewer. Task/Agent
// launches become RoleSubagent marker messages; when the number of markers
// WITHOUT a ChildUUID equals len(subagentIDs) those markers are mapped to the ids
// in order and become Openable (a best-effort launch-point→file mapping — Claude
// JSONL carries no verified tool_use↔agent-id link, so on any count mismatch
// markers stay visible but non-openable and search-jump, which is exact, remains
// the reliable path). A marker whose launch records the child session's uuid
// (Codex) is Openable through ChildUUID regardless of the count mapping. Pass
// nil subagentIDs for a subagent transcript (no nested markers expected).
//
// Malformed lines are skipped by the decoder (ADR-021), so the returned slice
// may be incomplete for a corrupt blob — the viewer shows what parsed rather than
// failing the whole session. An empty blob yields nil; so does a platform that
// cannot be decoded (logged — see decodeForDisplay).
func ParseTranscript(p Platform, raw []byte, subagentIDs []string) []TranscriptMessage {
	return transcriptMessages(decodeForDisplay(p, raw, "transcript"), subagentIDs)
}

// transcriptMessages is the TUI consumer over the transcript model: it walks the
// decoded entries in order and emits one TranscriptMessage per message, plus a
// RoleSubagent marker per launch. This is where every VIEWER policy lives (the
// decoder is pre-policy — see transcript_model.go):
//
//   - A Human entry is a RoleUser message anchored to its LineIndex.
//   - A ToolResult with a Diff is a collapsed Diff marker (ToolSummary carries
//     the "+a −b" stat, Body the unified diff) even when its Body is empty
//     (D18); otherwise an empty Body is skipped, an excludedResultTools result or
//     one over the collapse thresholds is a collapsed marker with the full Body,
//     and everything else is inline with the CallSummary prefix (viewerToolMessage).
//   - An Assistant message's body is its text parts and non-launch calls as
//     "→ <Summary>" lines in part order; each launch becomes a RoleSubagent
//     marker AFTER the body, labelled by Launch.Label (D19).
//   - A System entry is shown unless SearchOnly (D7).
//
// Output for Claude sessions is byte-identical to the pre-model parser
// (golden_test.go, parity_canary_test.go). A nil transcript yields nil. An entry
// of unknown Kind is skipped with a warning — a decoder bug, never valid input.
func transcriptMessages(t *Transcript, subagentIDs []string) []TranscriptMessage {
	if t == nil {
		return nil
	}
	var msgs []TranscriptMessage
	var markerIdx []int // indices in msgs of RoleSubagent markers without a ChildUUID (count-based mapping)
	for _, e := range t.Entries {
		switch e.Kind {
		case EntryHuman:
			msgs = append(msgs, TranscriptMessage{Role: RoleUser, Body: e.Text, SourceLine: e.LineIndex, Queued: e.Queued})

		case EntryToolResult:
			if m, ok := viewerToolMessage(e); ok {
				msgs = append(msgs, m)
			}

		case EntryAssistant:
			body, launches := assistantBodyAndLaunches(e.Parts)
			if body != "" {
				msgs = append(msgs, TranscriptMessage{Role: RoleAssistant, Body: body, SourceLine: e.LineIndex})
			}
			for _, l := range launches {
				if l.ChildUUID == "" {
					markerIdx = append(markerIdx, len(msgs))
				}
				msgs = append(msgs, TranscriptMessage{
					Role: RoleSubagent, Body: l.Label, SourceLine: e.LineIndex,
					ChildUUID: l.ChildUUID, Openable: l.ChildUUID != "",
				})
			}

		case EntrySystem:
			if e.SearchOnly {
				continue
			}
			msgs = append(msgs, TranscriptMessage{Role: RoleSystem, Body: e.Text, SourceLine: e.LineIndex})

		default:
			// Fail loud, not silent: an unknown Kind (including the EntryUnknown
			// zero value) is a decoder bug, and dropping it without a trace would
			// hide it. Output is unaffected for valid input.
			slog.Warn("vault transcript: skipping transcript entry of unknown kind",
				"platform", t.Meta.Platform, "line", e.LineIndex, "kind", e.Kind)
		}
	}

	// Best-effort launch-point→sidecar mapping (Claude, D23): only when every
	// count-mapped marker pairs with an archived subagent (counts align) do we
	// make them openable. Otherwise the mapping is ambiguous, so they stay
	// visible-only and search-jump (exact) is the way in.
	if len(markerIdx) > 0 && len(markerIdx) == len(subagentIDs) {
		for k, mi := range markerIdx {
			msgs[mi].AgentID = subagentIDs[k]
			msgs[mi].Openable = true
		}
	}
	return msgs
}

// viewerToolMessage applies the viewer's tool-result policy to one ToolResult
// entry (ok=false when nothing is shown). The decision order is load-bearing for
// byte-identical Claude output: a Diff wins even over an empty Body (D18 — an
// Edit whose success body is empty but whose patch is not), then an empty Body
// is skipped, then the collapse decision — excluded tool, or over the size/line
// threshold — is made explicitly rather than baked into a display string, so a
// collapsed marker can expand to the full Body on demand (A1). An inline
// (non-collapsed) result keeps the summary-prefixed body, as `show` renders it.
func viewerToolMessage(e Entry) (TranscriptMessage, bool) {
	if e.Diff != nil {
		return TranscriptMessage{
			Role: RoleTool, Body: e.Diff.Text, SourceLine: e.LineIndex,
			Collapsed:   true,
			ToolSummary: fmt.Sprintf("%s (+%d −%d)", e.CallSummary, e.Diff.Added, e.Diff.Removed),
			Diff:        true,
		}, true
	}
	if e.Body == "" {
		return TranscriptMessage{}, false
	}
	if excludedResultTools[e.CallName] || overCollapseThreshold(e.Body) {
		// Carry the full body for the open target; the marker shows the compact
		// summary + line count (see tui toolMarkerRow).
		return TranscriptMessage{
			Role: RoleTool, Body: e.Body, SourceLine: e.LineIndex,
			Collapsed: true, ToolSummary: e.CallSummary,
		}, true
	}
	return TranscriptMessage{Role: RoleTool, Body: prefixToolResult(e.CallSummary, e.Body), SourceLine: e.LineIndex}, true
}

// assistantBodyAndLaunches splits an assistant entry's ordered parts into a
// display body and its launches. Text parts are kept verbatim; a non-launch call
// renders as a "→ <Summary>" body line (as render.go does); a call carrying a
// Launch (Task/Agent, spawn_agent) is returned for a marker instead of a body
// line. A call with an empty Summary contributes nothing.
func assistantBodyAndLaunches(parts []Part) (body string, launches []*Launch) {
	var lines []string
	for _, p := range parts {
		switch {
		case p.Call != nil && p.Call.Launch != nil:
			launches = append(launches, p.Call.Launch)
		case p.Call != nil:
			if p.Call.Summary != "" {
				lines = append(lines, "→ "+p.Call.Summary)
			}
		case p.Text != "":
			lines = append(lines, p.Text)
		}
	}
	return strings.Join(lines, "\n"), launches
}

// overCollapseThreshold reports whether a tool_result body is large enough to
// collapse to a marker in the viewer (A1) — by line count or byte size, so both a
// many-line log and a single huge line collapse.
func overCollapseThreshold(body string) bool {
	return strings.Count(body, "\n")+1 > collapseToolResultLines || len(body) > collapseToolResultBytes
}
