package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindText(t *testing.T) {
	tests := []struct {
		name, body, query string
		starts            []int
	}{
		{"overlaps", "banana", "ana", []int{1, 3}},
		{"case", "Needle needle NEEDLE", "needle", []int{7}},
		{"punctuation", "[a.*] [a.*]", "[a.*]", []int{0, 6}},
		{"spaces", " a  a ", " a ", []int{0, 3}},
		{"space only", "x  y", " ", []int{1, 2}},
		{"empty", "anything", "", nil},
		{"no cross LF", "one\ntwo", "e\nt", nil},
		{"unicode overlap", "界界界", "界界", []int{0, 3}},
		{"no normalization", "é e\u0301", "é", []int{0}},
		{"NUL", "a\x00a", "\x00", []int{1}},
		{"invalid bytes retained", "a\xffa", "a", []int{0, 2}},
		{"repeated lines", "hit\nhit\n", "hit", []int{0, 4}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := buildFindCorpus(t.Context(), "scope", []vault.TranscriptMessage{{Body: tt.body}})
			require.NoError(t, err)
			hits, err := findExact(t.Context(), c, tt.query)
			require.NoError(t, err)
			var starts []int
			for _, hit := range hits {
				starts = append(starts, hit.start)
				assert.Equal(t, tt.query, tt.body[hit.start:hit.end])
			}
			assert.Equal(t, tt.starts, starts)
		})
	}
}

func TestFindTextFields(t *testing.T) {
	messages := []vault.TranscriptMessage{
		{SourceLine: 3, Role: vault.RoleAssistant, Body: "needle\n\nneedle\n"},
		{SourceLine: 3, Role: vault.RoleTool, Collapsed: true, ToolSummary: "Read needle", Body: strings.Repeat("x", 100_000) + "needle"},
		{SourceLine: 3, Role: vault.RoleTool, ToolSummary: "needle", Body: "needle: ordinary body"},
		{SourceLine: 3, Role: vault.RoleSubagent, Body: "launch\nneedle"},
	}
	c, err := buildFindCorpus(t.Context(), "session/main", messages)
	require.NoError(t, err)
	assert.Equal(t, "session/main", c.scope)
	assert.Len(t, c.lines, 9)
	assert.Equal(t, "", c.lines[1].text)
	assert.Equal(t, "", c.lines[3].text)
	hits, err := findExact(t.Context(), c, "needle")
	require.NoError(t, err)
	require.Len(t, hits, 6, "ordinary summary must not be indexed twice")
	assert.Equal(t, findPosition{1, findSummary, 5}, c.position(hits[2]))
	assert.Equal(t, findPosition{1, findBody, 100_000}, c.position(hits[3]))
	assert.Equal(t, findPosition{2, findBody, 0}, c.position(hits[4]))
	assert.Equal(t, 1, c.lines[hits[5].line].ordinal)
}

func TestFindTextCollapsed(t *testing.T) {
	large := strings.Repeat("before\n", 12_000) + "middle-needle\n" + strings.Repeat("after\n", 12_000)
	for _, tool := range []string{"Read", "Bash", "exec_command"} {
		t.Run(tool, func(t *testing.T) {
			platform := vault.PlatformClaudeCode
			body := large
			if tool == "Read" {
				body = "middle-needle" // excluded from FTS regardless of length
			}
			var raw []byte
			if tool == "exec_command" {
				platform = vault.PlatformCodex
				raw = jsonlLines(t,
					codexHumanLine("run it"),
					codexEnv("response_item", map[string]any{"type": "function_call", "name": tool, "call_id": "call",
						"arguments": `{"cmd":"summary-only"}`}),
					codexEnv("response_item", map[string]any{"type": "function_call_output", "call_id": "call", "output": body}),
				)
			} else {
				raw = jsonlLines(t,
					userLine("run it"),
					assistantLine("call", []map[string]any{{"type": "tool_use", "id": "t1", "name": tool,
						"input": map[string]any{"command": "summary-only", "file_path": "/p/summary-only"}}}),
					toolResultLine(body),
				)
			}
			messages := vault.ParseTranscript(platform, raw, nil)
			require.Equal(t, 3, len(messages), "prompt, displayed tool call, and result")
			require.True(t, messages[2].Collapsed)
			c, err := buildFindCorpus(t.Context(), tool, messages)
			require.NoError(t, err)
			hits, err := findExact(t.Context(), c, "middle-needle")
			require.NoError(t, err)
			require.Len(t, hits, 1)
			assert.Equal(t, findPosition{2, findBody, strings.Index(body, "middle-needle")}, c.position(hits[0]))
			hits, err = findExact(t.Context(), c, "summary-only")
			require.NoError(t, err)
			require.Len(t, hits, 2, "one displayed call Body plus one result ToolSummary")
			assert.Equal(t, findBody, c.position(hits[0]).field)
			assert.Equal(t, findSummary, c.position(hits[1]).field)
			v := newViewerModel(DefaultStyles(), 75, 9).loadSession(vault.Session{UUID: tool, Platform: platform, RawJSONL: raw}, nil)
			v = searchViewer(t, v, "middle-needle")
			requireFindLanding(t, v, findPosition{2, findBody, strings.Index(body, "middle-needle")})
			assert.Contains(t, ansi.Strip(v.View()), "[middle-needle]")
		})
	}
}

// A deterministic context that expires after checkpoints, allowing cancellation
// inside one long line to be tested without scheduler timing or sleeps.
type findCheckpointContext struct {
	context.Context
	checks, remaining int
}

func (c *findCheckpointContext) Err() error {
	c.checks++
	if c.checks > c.remaining {
		return context.Canceled
	}
	return nil
}

func TestFindTextCancellation(t *testing.T) {
	text := strings.Repeat("界", 100_000)
	messages := []vault.TranscriptMessage{{Body: text}}
	ctx := &findCheckpointContext{Context: t.Context(), remaining: 3}
	_, err := buildFindCorpus(ctx, "scope", messages)
	require.ErrorIs(t, err, context.Canceled)
	c, err := buildFindCorpus(t.Context(), "scope", messages)
	require.NoError(t, err)
	ctx = &findCheckpointContext{Context: t.Context(), remaining: 5}
	_, err = findExact(ctx, c, "missing")
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 6, ctx.checks)
}
