package vault

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTranscriptCodeInput_BoundariesAndBytes(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		collapsed  bool
	}{
		{"empty", "", false},
		{"short escapes", "  text(\"quote\\\" and newline\\n\");\r\n", false},
		{"2000 bytes", strings.Repeat("x", 2000), false},
		{"2001 bytes", strings.Repeat("x", 2001), true},
		{"20 lines", strings.Repeat("x\n", 19) + "x", false},
		{"21 lines", strings.Repeat("x\n", 20) + "x", true},
		{"multibyte bytes", strings.Repeat("界", 667), true},
		{"pragma and escapes", "// @exec: {\"yield_time_ms\": 1000}\r\ntext(\"a\\nb\\\"c\");\r\n" + strings.Repeat("// padding\n", 21), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := codexRollout(t, codexPaginated, codexCustomToolCallLine(at(0), "code", "exec", tc.code))
			tr := decodeCodexRaw(t, raw)
			require.Len(t, tr.Entries, 1)
			call := tr.Entries[0].Parts[0].Call
			require.NotNil(t, call)
			assert.Equal(t, tc.code, call.CodeText)
			var input string
			require.NoError(t, json.Unmarshal(call.Input, &input))
			assert.Equal(t, tc.code, input)
			assert.Equal(t, "exec", call.Name)
			first, _, _ := strings.Cut(strings.TrimSpace(tc.code), "\n")
			wantSummary := "exec"
			if strings.TrimSpace(first) != "" {
				wantSummary += " " + strings.TrimSpace(first)
			}
			assert.Equal(t, wantSummary, call.Summary)
			msgs := transcriptMessages(tr, nil)
			if !tc.collapsed {
				require.Len(t, msgs, 1)
				assert.Equal(t, "→ "+wantSummary, msgs[0].Body)
				assert.False(t, msgs[0].SourceAnchor)
				return
			}
			require.Len(t, msgs, 2)
			assert.Equal(t, "→ exec · input", msgs[0].Body)
			assert.True(t, msgs[0].SourceAnchor)
			assert.Equal(t, TranscriptMessage{Role: RoleTool, Body: tc.code, SourceLine: 0,
				Collapsed: true, ToolSummary: "exec · input", Heading: "Tool input"}, msgs[1])
		})
	}
	for _, name := range []string{"apply_patch", "future_tool"} {
		t.Run(name+" has no executable metadata", func(t *testing.T) {
			tr := decodeCodexRaw(t, codexRollout(t, codexLegacy,
				codexCustomToolCallLine(at(0), "other", name, strings.Repeat("x", 2100))))
			assert.Empty(t, tr.Entries[0].Parts[0].Call.CodeText)
			assert.Len(t, transcriptMessages(tr, nil), 1)
		})
	}
	encoded, err := json.Marshal(ToolCall{})
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "CodeText")
	encoded, err = json.Marshal(TranscriptMessage{})
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "SourceAnchor")
}

func TestTranscriptCodeInput_OrderedDetails(t *testing.T) {
	codeA, codeB := strings.Repeat("a", 2001), strings.Repeat("b\n", 21)
	tr := &Transcript{Entries: []Entry{{Kind: EntryAssistant, LineIndex: 7, Parts: []Part{
		{Text: "before"},
		{Call: &ToolCall{Summary: "exec large A", CodeText: codeA}},
		{Text: "between"},
		{Call: &ToolCall{Launch: &Launch{Label: "sidecar"}}},
		{Call: &ToolCall{Summary: "exec short"}},
		{Call: &ToolCall{Summary: "exec large B", CodeText: codeB}},
		{Call: &ToolCall{Launch: &Launch{Label: "child", ChildUUID: "child-id"}}},
		{Text: "after"},
	}}}}
	msgs := transcriptMessages(tr, []string{"sidecar-id"})
	require.Len(t, msgs, 5)
	assert.Equal(t, "before\n→ exec · input\nbetween\n→ exec short\n→ exec · input\nafter", msgs[0].Body)
	assert.True(t, msgs[0].SourceAnchor)
	assert.Equal(t, codeA, msgs[1].Body)
	assert.Equal(t, RoleSubagent, msgs[2].Role)
	assert.Equal(t, "sidecar-id", msgs[2].AgentID, "input markers do not count as sidecars")
	assert.True(t, msgs[2].Openable)
	assert.Equal(t, codeB, msgs[3].Body)
	assert.Equal(t, "child-id", msgs[4].ChildUUID)
	assert.True(t, msgs[4].Openable)
	for i, msg := range msgs {
		assert.Equal(t, 7, msg.SourceLine)
		assert.Equal(t, i == 0, msg.SourceAnchor)
	}
}

func TestTranscriptCodeInput_OutputAliases(t *testing.T) {
	for _, tc := range []struct {
		name, id, body     string
		collapsed, aliased bool
	}{
		{"inline", "call-code", "complete output", false, true},
		{"collapsed", "call-code", strings.Repeat("output\n", 21), true, true},
		{"nested ID independent", "exec-patch", "nested result", false, false},
		{"missing ID", "", "unidentified", false, false},
		{"unknown ID", "unknown", "unmatched", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const summary = "exec original shared summary"
			// Result deliberately precedes the call: aliases are whole-transcript.
			tr := &Transcript{Entries: []Entry{
				{Kind: EntryToolResult, CallID: tc.id, CallName: "exec", CallSummary: summary, Body: tc.body},
				{Kind: EntryAssistant, Parts: []Part{{Call: &ToolCall{ID: "call-code", Summary: summary, CodeText: strings.Repeat("x", 2001)}}}},
			}}
			msg := transcriptMessages(tr, nil)[0]
			want := summary
			if tc.aliased {
				want = "exec · output"
			}
			assert.Equal(t, tc.collapsed, msg.Collapsed)
			if tc.collapsed {
				assert.Equal(t, want, msg.ToolSummary)
				assert.Equal(t, tc.body, msg.Body)
			} else {
				assert.Equal(t, want+"\n"+tc.body, msg.Body)
			}
			assert.Equal(t, summary, tr.Entries[0].CallSummary, "viewer never mutates shared summaries")
		})
	}
}

func TestTranscriptCodeInput_WrappedEdit(t *testing.T) {
	tr := decodeCodexRaw(t, codexEditCompatibilityCases(t)[0].raw)
	msgs := transcriptMessages(tr, nil)
	var inputs, edits, outputs int
	for _, m := range msgs {
		switch {
		case m.Heading == "Tool input":
			inputs++
			assert.Equal(t, tr.Entries[1].Parts[1].Call.CodeText, m.Body)
		case m.Heading == "File changes · completed":
			edits++
			assert.Equal(t, "4 files changed (+23 −14)", m.ToolSummary)
		case strings.HasPrefix(m.Body, "exec · output\n"):
			outputs++
		default:
			assert.NotContains(t, m.Body, "const edit = await")
		}
	}
	assert.Equal(t, 1, inputs)
	assert.Equal(t, 1, edits)
	assert.Equal(t, 1, outputs)
}

func BenchmarkExecutableInputThreshold(b *testing.B) {
	for _, tc := range []struct{ name, body string }{
		{"short", "text('example');"},
		{"21-lines", strings.Repeat("// context\n", 20)},
		{"4-KiB", strings.Repeat("x", 4<<10)},
		{"4-MiB", strings.Repeat("x", 4<<20)},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				overCollapseThreshold(tc.body)
			}
		})
	}
}
