package vault

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// display_transcript_test.go pins the DISPLAY-side policies the two display
// consumers apply over a hand-built Transcript: displayMessages / formatDisplay
// (`vault show`, render.go) and transcriptMessages (the TUI viewer,
// transcript.go) — the consumer boundary Slice 4 moved out of the Claude reader
// loops. These tests are decoder-independent on purpose: the entries are
// constructed directly, so they hold for any platform's decoder (the Codex
// decoder in Slice 6 reuses both consumers unchanged). Claude end-to-end
// behaviour is pinned by golden_test.go, render_test.go and transcript_test.go.

// displayEntries is the shared fixture: every consumer policy has at least one
// entry that exercises it.
func displayEntries() []Entry {
	return []Entry{
		// line 0: human, queued
		{Kind: EntryHuman, LineIndex: 0, Text: "fix the timeout", Queued: true},
		// line 1: text → call → launch → text, in part order
		{Kind: EntryAssistant, LineIndex: 1, Parts: []Part{
			{Text: "Looking."},
			callPart("t1", "Read", "Read /proj/config.toml"),
			{Call: &ToolCall{ID: "t2", Name: "Task", Summary: "Agent investigate", Launch: &Launch{Label: "investigate"}}},
			{Text: "Then I will edit."},
			callPart("t3", "", ""), // nameless call with no summary contributes nothing
		}},
		// line 2: results
		{Kind: EntryToolResult, LineIndex: 2, CallID: "t1", CallName: "Read", CallSummary: "Read /proj/config.toml", Body: "a\nb\nc"},
		{Kind: EntryToolResult, LineIndex: 2, CallID: "t4", CallName: "Bash", CallSummary: "Bash go test", Body: "ok"},
		{Kind: EntryToolResult, LineIndex: 2, CallID: "zz", Body: "unmatched call output"},
		{Kind: EntryToolResult, LineIndex: 2, CallID: "t5", CallName: "Edit", CallSummary: "Edit /proj/main.go", Body: "", Diff: &Diff{Text: "@@ -1 +1 @@\n-a\n+b", Added: 1, Removed: 1}},
		{Kind: EntryToolResult, LineIndex: 2, CallID: "t6", CallName: "Bash", CallSummary: "Bash true", Body: ""},
		// line 3: assistant whose only call has an empty summary → no message
		{Kind: EntryAssistant, LineIndex: 3, Parts: []Part{callPart("t7", "X", "")}},
		// line 4: system entries — SearchOnly is never displayed
		{Kind: EntrySystem, LineIndex: 4, Text: "PR #7 o/r https://x/pull/7"},
		{Kind: EntrySystem, LineIndex: 4, Text: "attached notes.md", SearchOnly: true},
	}
}

func TestDisplayMessages_ConsumerPolicies(t *testing.T) {
	tr := &Transcript{Meta: Meta{Platform: PlatformClaudeCode}, Entries: displayEntries()}

	msgs := displayMessages(tr)

	type row struct{ role, body string }
	var got []row
	for _, m := range msgs {
		got = append(got, row{m.role, m.body})
	}
	assert.Equal(t, []row{
		{displayUser, "fix the timeout"},
		// Every call — the launch included — is an arrow line, in part order (D19).
		{displayAssistant, "Looking.\n→ Read /proj/config.toml\n→ Agent investigate\nThen I will edit."},
		// Excluded tool: one-line marker with the line count; body never shown (D13).
		{displayTool, "Read /proj/config.toml\n⋯ [output omitted from index — 3 line(s)]"},
		// Non-excluded: summary-prefixed, unbounded (D16/D17).
		{displayTool, "Bash go test\nok"},
		// Unmatched id: unprefixed.
		{displayTool, "unmatched call output"},
		// Empty bodies skipped (D18 — the Diff is a TUI concern, `show` keeps the body).
		// Empty-summary-only assistant skipped.
		{displaySystem, "PR #7 o/r https://x/pull/7"},
		// SearchOnly system skipped (D7).
	}, got)
	assert.True(t, msgs[0].queued, "Queued is carried for the label")
	assert.Nil(t, displayMessages(nil), "nil transcript (empty / undecodable input) renders nothing")
}

func TestCollapsedToolResult_UnknownCallFallsBackToGenericLabel(t *testing.T) {
	// An excluded result whose call could not be correlated has no summary; the
	// marker still names it so the omitted body is not an unexplained hole.
	assert.Equal(t, "tool result\n⋯ [output omitted from index — 1 line(s)]", collapsedToolResult("", "x"))
	assert.Equal(t, "Read /p\n⋯ [output omitted from index — 2 line(s)]", collapsedToolResult("Read /p", "a\nb"))
}

func TestFormatDisplay_AssistantHeadingIsPlatformAware(t *testing.T) {
	msgs := []displayMsg{
		{role: displayUser, body: "hi", queued: true},
		{role: displayAssistant, body: "hello"},
		{role: displayTool, body: "out"},
		{role: displaySystem, body: "sys"},
	}

	t.Run("claude", func(t *testing.T) {
		text := formatDisplay(msgs, PlatformClaudeCode, false)
		assert.Equal(t, "[You · queued]\nhi\n\n[Claude]\nhello\n\n[Tool result]\nout\n\n[System]\nsys\n", text)
		md := formatDisplay(msgs, PlatformClaudeCode, true)
		assert.Contains(t, md, "## 👤 You · queued\n\nhi\n")
		assert.Contains(t, md, "## 🤖 Claude\n\nhello\n")
		assert.Contains(t, md, "## ⎿ Tool result\n\n```\nout\n```\n")
		assert.Contains(t, md, "## ℹ System\n\nsys\n")
	})

	t.Run("codex", func(t *testing.T) {
		assert.Contains(t, formatDisplay(msgs, PlatformCodex, false), "[Codex]\nhello\n")
		assert.Contains(t, formatDisplay(msgs, PlatformCodex, true), "## 🤖 Codex\n\nhello\n")
	})

	t.Run("unknown role falls back to the raw role string", func(t *testing.T) {
		out := formatDisplay([]displayMsg{{role: "future", body: "x"}}, PlatformClaudeCode, false)
		assert.Equal(t, "[future]\nx\n", out)
	})

	assert.Equal(t, "", formatDisplay(nil, PlatformClaudeCode, false))
}

func TestTranscriptMessages_ConsumerPolicies(t *testing.T) {
	entries := displayEntries()
	longBody := strings.Repeat("y", collapseToolResultBytes+1)
	entries = append(entries,
		// line 5: over the byte threshold → collapsed by size, not by tool name
		Entry{Kind: EntryToolResult, LineIndex: 5, CallID: "t8", CallName: "Bash", CallSummary: "Bash cat", Body: longBody},
	)
	tr := &Transcript{Meta: Meta{Platform: PlatformClaudeCode}, Entries: entries}

	msgs := transcriptMessages(tr, nil)

	type row struct {
		role string
		line int
	}
	var got []row
	for _, m := range msgs {
		got = append(got, row{m.Role, m.SourceLine})
	}
	assert.Equal(t, []row{
		{RoleUser, 0},
		{RoleAssistant, 1},
		{RoleSubagent, 1}, // the launch marker follows the body (D19)
		{RoleTool, 2},     // Read: collapsed (excluded)
		{RoleTool, 2},     // Bash: inline
		{RoleTool, 2},     // unmatched: inline, unprefixed
		{RoleTool, 2},     // Edit: Diff marker despite the empty body (D18)
		// empty-bodied Bash skipped; empty-summary assistant skipped
		{RoleSystem, 4}, // SearchOnly skipped (D7)
		{RoleTool, 5},   // collapsed by threshold
	}, got)

	assert.Equal(t, "fix the timeout", msgs[0].Body)
	assert.True(t, msgs[0].Queued)

	// The launch is NOT an arrow line in the body; it is the marker after it.
	assert.Equal(t, "Looking.\n→ Read /proj/config.toml\nThen I will edit.", msgs[1].Body)
	assert.Equal(t, "investigate", msgs[2].Body)
	assert.False(t, msgs[2].Openable, "no sidecar ids → the Claude-style marker is not openable")
	assert.Empty(t, msgs[2].AgentID)
	assert.Empty(t, msgs[2].ChildUUID)

	// Excluded tool: collapsed marker with the FULL body and the bare summary.
	assert.Equal(t, TranscriptMessage{Role: RoleTool, Body: "a\nb\nc", SourceLine: 2, Collapsed: true, ToolSummary: "Read /proj/config.toml"}, msgs[3])
	// Inline: summary-prefixed body, not collapsed.
	assert.Equal(t, TranscriptMessage{Role: RoleTool, Body: "Bash go test\nok", SourceLine: 2}, msgs[4])
	assert.Equal(t, TranscriptMessage{Role: RoleTool, Body: "unmatched call output", SourceLine: 2}, msgs[5])
	// Diff marker: stat baked into the summary, unified diff as the body.
	assert.Equal(t, TranscriptMessage{Role: RoleTool, Body: "@@ -1 +1 @@\n-a\n+b", SourceLine: 2, Collapsed: true, ToolSummary: "Edit /proj/main.go (+1 −1)", Diff: true}, msgs[6])
	assert.Equal(t, "PR #7 o/r https://x/pull/7", msgs[7].Body)
	// Threshold collapse keeps the full body for the open target.
	assert.True(t, msgs[8].Collapsed)
	assert.Equal(t, "Bash cat", msgs[8].ToolSummary)
	assert.Equal(t, longBody, msgs[8].Body)

	assert.Nil(t, transcriptMessages(nil, nil), "nil transcript (empty / undecodable input) yields nil")
}

func TestTranscriptMessages_LaunchMarkers(t *testing.T) {
	// Two Claude-style launches (no ChildUUID) and one platform-resolved child
	// launch in the same assistant entry.
	tr := &Transcript{Entries: []Entry{
		{Kind: EntryAssistant, LineIndex: 0, Parts: []Part{
			{Call: &ToolCall{ID: "t1", Name: "Task", Summary: "Agent a", Launch: &Launch{Label: "first"}}},
			{Call: &ToolCall{ID: "t2", Name: "spawn_agent", Summary: "spawn_agent worker", Launch: &Launch{Label: "worker", ChildUUID: "0199c0ff-ee00-7000-8000-000000000001"}}},
			{Call: &ToolCall{ID: "t3", Name: "Agent", Summary: "Agent b", Launch: &Launch{Label: "second"}}},
		}},
	}}

	t.Run("child uuid is openable on its own and excluded from the count mapping", func(t *testing.T) {
		msgs := transcriptMessages(tr, []string{"aaa", "bbb"})
		require.Len(t, msgs, 3, "a launch-only assistant has no body message, only markers")
		assert.Equal(t, TranscriptMessage{Role: RoleSubagent, Body: "first", SourceLine: 0, AgentID: "aaa", Openable: true}, msgs[0])
		assert.Equal(t, TranscriptMessage{Role: RoleSubagent, Body: "worker", SourceLine: 0, ChildUUID: "0199c0ff-ee00-7000-8000-000000000001", Openable: true}, msgs[1],
			"a ChildUUID marker is openable as a session and never receives a sidecar id")
		assert.Equal(t, TranscriptMessage{Role: RoleSubagent, Body: "second", SourceLine: 0, AgentID: "bbb", Openable: true}, msgs[2])
	})

	t.Run("count mismatch leaves Claude markers non-openable, child marker unaffected", func(t *testing.T) {
		msgs := transcriptMessages(tr, []string{"only-one"})
		require.Len(t, msgs, 3)
		assert.False(t, msgs[0].Openable)
		assert.Empty(t, msgs[0].AgentID)
		assert.True(t, msgs[1].Openable)
		assert.Equal(t, "0199c0ff-ee00-7000-8000-000000000001", msgs[1].ChildUUID)
		assert.False(t, msgs[2].Openable)
	})

	t.Run("no sidecars", func(t *testing.T) {
		msgs := transcriptMessages(tr, nil)
		require.Len(t, msgs, 3)
		assert.False(t, msgs[0].Openable)
		assert.True(t, msgs[1].Openable)
		assert.False(t, msgs[2].Openable)
	})
}

func TestDisplayConsumers_UnknownKindWarnsAndSkips(t *testing.T) {
	// Fail loud: a Kind the consumers do not know (a decoder bug, incl. the
	// EntryUnknown zero value) is skipped WITH a warning, never silently, and the
	// surrounding messages are unaffected.
	h := &recordingHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })

	tr := &Transcript{Meta: Meta{Platform: PlatformClaudeCode}, Entries: []Entry{
		{Kind: EntryHuman, LineIndex: 0, Text: "hi"},
		{LineIndex: 1, Text: "forgotten kind"}, // zero Kind
		{Kind: EntryKind(99), LineIndex: 2, Text: "future kind"},
		{Kind: EntryAssistant, LineIndex: 3, Parts: []Part{{Text: "hello"}}},
	}}

	shown := displayMessages(tr)
	require.Len(t, shown, 2)
	assert.Equal(t, displayUser, shown[0].role)
	assert.Equal(t, displayAssistant, shown[1].role)
	assert.Len(t, h.messagesWithPrefix("vault render: skipping transcript entry of unknown kind"), 2)

	viewed := transcriptMessages(tr, nil)
	require.Len(t, viewed, 2)
	assert.Equal(t, RoleUser, viewed[0].Role)
	assert.Equal(t, RoleAssistant, viewed[1].Role)
	assert.Len(t, h.messagesWithPrefix("vault transcript: skipping transcript entry of unknown kind"), 2)
}

func TestDisplayReaders_DispatchFailureIsLoggedNotSilent(t *testing.T) {
	// The display signatures return text, not an error, so a platform-dispatch
	// failure (the only Decode error a bytes.Reader can surface) must be logged
	// and yield EMPTY output — never a silent Claude default over the bytes.
	h := &recordingHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })

	body := []byte(`{"type":"user","uuid":"u1","timestamp":"2026-05-01T10:00:00Z","message":{"role":"user","content":"hello"}}` + "\n")

	// Only an unknown platform value is a dispatch failure now: since Slice 6
	// PlatformCodex resolves to a real decoder (until then it was the
	// ErrDecoderUnavailable stub this test originally exercised).
	t.Run("bogus", func(t *testing.T) {
		p := Platform("bogus")
		before := len(h.messagesWithPrefix("vault display: cannot decode session"))
		assert.Equal(t, "", RenderText(p, body))
		assert.Equal(t, "", RenderMarkdown(p, body))
		assert.Nil(t, ParseTranscript(p, body, nil))
		assert.Equal(t, before+3, len(h.messagesWithPrefix("vault display: cannot decode session")),
			"one warning per failed decode")
	})

	t.Run("codex over claude bytes decodes to nothing, no dispatch warning", func(t *testing.T) {
		// The Codex decoder skips every record type it does not know (ADR-021),
		// so Claude bytes yield an empty transcript — empty output, but NOT the
		// dispatch failure path.
		before := len(h.messagesWithPrefix("vault display: cannot decode session"))
		assert.Equal(t, "", RenderText(PlatformCodex, body))
		assert.Equal(t, "", RenderMarkdown(PlatformCodex, body))
		assert.Nil(t, ParseTranscript(PlatformCodex, body, nil))
		assert.Equal(t, before, len(h.messagesWithPrefix("vault display: cannot decode session")))
	})

	t.Run("claude decodes and does not warn", func(t *testing.T) {
		before := len(h.messagesWithPrefix("vault display: cannot decode session"))
		assert.Contains(t, RenderText(PlatformClaudeCode, body), "hello")
		require.Len(t, ParseTranscript(PlatformClaudeCode, body, nil), 1)
		assert.Equal(t, before, len(h.messagesWithPrefix("vault display: cannot decode session")))
	})

	t.Run("empty input is not a failure", func(t *testing.T) {
		before := len(h.messagesWithPrefix("vault display: cannot decode session"))
		assert.Equal(t, "", RenderText(PlatformCodex, nil))
		assert.Nil(t, ParseTranscript(Platform("bogus"), []byte{}, nil))
		assert.Equal(t, before, len(h.messagesWithPrefix("vault display: cannot decode session")), "nothing to decode, nothing to warn about")
	})
}
