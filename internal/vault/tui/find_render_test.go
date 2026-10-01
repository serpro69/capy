package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func findTestProjection(t *testing.T, messages []vault.TranscriptMessage, width int) (*findCorpus, *findProjection) {
	t.Helper()
	c, err := buildFindCorpus(t.Context(), "test", messages)
	require.NoError(t, err)
	p, err := buildFindProjection(t.Context(), c, messages, vault.PlatformClaudeCode, DefaultStyles(), width)
	require.NoError(t, err)
	return c, p
}

func TestFindRenderCoverage(t *testing.T) {
	bodies := []string{"a  b   c ", "e\u0301界👨‍👩‍👧‍👦 xyz", "a\t\x00\r\x1b[31m\xffz", strings.Repeat("word", 40), "\nempty\n", "label\nsecond line"}
	for _, width := range []int{1, 2, 3, 5, 9, 80} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			var messages []vault.TranscriptMessage
			for _, body := range bodies {
				messages = append(messages, vault.TranscriptMessage{Role: vault.RoleAssistant, Body: body})
			}
			messages = append(messages, vault.TranscriptMessage{Role: vault.RoleSubagent, Body: "agent\nlabel", Openable: true})
			c, p := findTestProjection(t, messages, width)
			covered := make([]int, len(c.lines))
			for i, row := range p.rows {
				assert.NotContains(t, p.transcript.rows[i], "\n", "one element equals one row")
				if row.line < 0 {
					continue
				}
				line := c.lines[row.line]
				assert.Equal(t, covered[row.line]+line.position.offset, row.start)
				covered[row.line] += row.end - row.start
				for offset := row.start; offset < row.end; offset++ {
					assert.Equal(t, i, p.rowForHit(findHit{row.line, offset, offset + 1}))
				}
				display := p.highlightRow(c, i, nil, -1, DefaultStyles())
				assert.LessOrEqual(t, ansi.StringWidth(display), width)
				assert.NotContains(t, ansi.Strip(display), "\x1b")
			}
			for i, line := range c.lines {
				assert.Equal(t, len(line.text), covered[i], "line %d loses source bytes", i)
			}
			assert.Len(t, p.transcript.markers, 1)
		})
	}
}

func TestFindRenderBrackets(t *testing.T) {
	tests := []struct {
		name, body, query string
		width, selection  int
		want              []string
	}{
		{"same row second occurrence", "hit hit hit", "hit", 80, 1, []string{"hit [hit] hit"}},
		{"overlap", "banana", "ana", 80, 1, []string{"ban[ana]"}},
		{"wrapped", "abcdefghij", "defgh", 7, 0, []string{"abc[de]", "[fgh]ij"}},
		{"combining part", "e\u0301界", "\u0301", 80, 0, []string{"[e\u0301]界"}},
		{"escaped NUL", "a\x00b", "\x00", 80, 0, []string{"a[\\u0000]b"}},
		{"space at break", "abc def", " ", 6, 0, []string{"abc[ ]", "def"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, p := findTestProjection(t, []vault.TranscriptMessage{{Body: tt.body}}, tt.width)
			hits, err := findExact(t.Context(), c, tt.query)
			require.NoError(t, err)
			var got []string
			for i, row := range p.rows {
				if row.line >= 0 {
					got = append(got, ansi.Strip(p.highlightRow(c, i, hits, tt.selection, DefaultStyles())))
				}
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFindRenderSummaryAndLabels(t *testing.T) {
	c, p := findTestProjection(t, []vault.TranscriptMessage{
		{Role: vault.RoleTool, Collapsed: true, ToolSummary: "Read\ncomplete summary", Body: "hidden"},
		{Role: vault.RoleSubagent, Body: "first\nsecond launch line", Openable: true},
	}, 30)
	for _, query := range []string{"complete summary", "second launch"} {
		hits, err := findExact(t.Context(), c, query)
		require.NoError(t, err)
		require.Len(t, hits, 1)
		row := p.rowForHit(hits[0])
		assert.Contains(t, ansi.Strip(p.highlightRow(c, row, hits, 0, DefaultStyles())), "["+query+"]")
	}
	assert.Len(t, p.transcript.markers, 2)
	hits, err := findExact(t.Context(), c, "enter to expand")
	require.NoError(t, err)
	assert.Empty(t, hits, "structural hints are not searchable")
}

func TestFindRenderCancellation(t *testing.T) {
	c, err := buildFindCorpus(t.Context(), "test", []vault.TranscriptMessage{{Body: strings.Repeat("界abc ", 100_000)}})
	require.NoError(t, err)
	ctx := &findCheckpointContext{Context: t.Context(), remaining: 5}
	err = wrapFindLine(ctx, c.lines[0], 80, func(int, int, string) {})
	assert.ErrorIs(t, err, context.Canceled)
}

func TestFindRenderEmptyLineAnchor(t *testing.T) {
	c, p := findTestProjection(t, []vault.TranscriptMessage{{Body: "first\n\nlast"}}, 20)
	row := p.lineRows[1]
	anchor := p.positionForRow(c, row)
	assert.Equal(t, row, p.rowForPosition(c, anchor))
}
