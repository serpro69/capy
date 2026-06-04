package tui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/serpro69/capy/internal/vault"
)

func TestRenderTranscript_RowMapAndMarkers(t *testing.T) {
	sess, files := sampleSession(t)
	ids := sortedSubagentIDs(files)
	msgs := vault.ParseTranscript(sess.RawJSONL, ids)
	rt := renderTranscript(msgs, DefaultStyles(), 80)

	require.Equal(t, len(msgs), len(rt.msgRowStart))
	// Row starts are monotonically non-decreasing and the content has as many
	// rows as the last start implies.
	for i := 1; i < len(rt.msgRowStart); i++ {
		assert.GreaterOrEqual(t, rt.msgRowStart[i], rt.msgRowStart[i-1])
	}
	assert.Len(t, rt.markers, 1, "one openable subagent marker (counts align)")

	// The openable marker row is the start row of a RoleSubagent message.
	row := rt.rowForMarker(0)
	require.GreaterOrEqual(t, row, 0)
	assert.Contains(t, rt.rows[row], "subagent")
	assert.Equal(t, -1, rt.rowForMarker(1), "out-of-range marker")
}

func TestRenderTranscript_RowForLine(t *testing.T) {
	sess, _ := sampleSession(t)
	msgs := vault.ParseTranscript(sess.RawJSONL, nil)
	rt := renderTranscript(msgs, DefaultStyles(), 80)

	// Source line 3 is the final assistant message ("final answer"); its row must
	// contain that body once we scroll there.
	row := rt.rowForLine(3)
	joined := strings.Join(rt.rows[row:], "\n")
	assert.Contains(t, joined, "final answer")

	// A line before the first message resolves to row 0.
	assert.Equal(t, 0, rt.rowForLine(-1))
	// A line past the end resolves to the last message's start (clamped forward).
	assert.GreaterOrEqual(t, rt.rowForLine(9999), rt.msgRowStart[len(msgs)-1])
}

func TestRenderTranscript_ContentJoinsRows(t *testing.T) {
	rt := renderedTranscript{rows: []string{"a", "b", "c"}}
	assert.Equal(t, "a\nb\nc", rt.content())
}

func TestRenderTranscript_LineForRowInvertsRowForLine(t *testing.T) {
	sess, _ := sampleSession(t)
	msgs := vault.ParseTranscript(sess.RawJSONL, nil)
	rt := renderTranscript(msgs, DefaultStyles(), 80)

	// For each message, the row it starts at maps back to its source line.
	for i, m := range msgs {
		assert.Equal(t, m.SourceLine, rt.lineForRow(rt.msgRowStart[i]),
			"row %d (message %d) should map back to source line %d", rt.msgRowStart[i], i, m.SourceLine)
	}
	// A row before the first message resolves to line 0.
	assert.Equal(t, 0, rt.lineForRow(-1))
}
