package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bashUse is a parametrized assistant Bash tool_use (distinct ids let one
// transcript carry several collapsible tool_results — A4 needs many markers).
func bashUse(id, cmd string) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": "Bash",
		"input": map[string]any{"command": cmd}}
}

// toolResultFor is a user tool_result for the given tool_use_id (parametrized
// twin of collapse_test.go's fixed-"t1" toolResultLine).
func toolResultFor(id, body string) map[string]any {
	return map[string]any{
		"type": "user", "timestamp": "2026-05-01T10:00:10Z", "cwd": "/p", "gitBranch": "main",
		"message": map[string]any{"role": "user", "content": []map[string]any{
			{"type": "tool_result", "tool_use_id": id, "content": body},
		}},
	}
}

// multiMarkerSession builds a transcript with three large Bash tool_results (each
// collapses to one openable marker), spaced apart by filler turns so the markers
// are more than a viewport height apart and can't all be visible at once.
func multiMarkerSession(t *testing.T) vault.Session {
	t.Helper()
	var lines []map[string]any
	lines = append(lines, userLine("start the run"))
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("t%d", i)
		lines = append(lines,
			assistantLine(fmt.Sprintf("a%d", i),
				[]map[string]any{textBlock(fmt.Sprintf("running step %d", i)), bashUse(id, fmt.Sprintf("cmd-%d", i))}),
			toolResultFor(id, bigBody()), // > threshold → collapses to a marker
		)
		for j := 0; j < 4; j++ { // filler spacing
			lines = append(lines, assistantLine(fmt.Sprintf("f%d-%d", i, j),
				[]map[string]any{textBlock(fmt.Sprintf("follow-up note %d-%d", i, j))}))
		}
	}
	return vault.Session{UUID: "multimark0000000", Title: "multi marker nav", RawJSONL: jsonlLines(t, lines...)}
}

// TestViewer_FocusMarkerResolvesFromViewport is the core A4 behavior: once the
// focused marker has scrolled out of view, ]/[ pick the next/prev marker relative
// to the current viewport — not by replaying from a stale focusedMarker±1.
func TestViewer_FocusMarkerResolvesFromViewport(t *testing.T) {
	v := newViewerModel(DefaultStyles(), 80, 8).loadSession(multiMarkerSession(t), nil)
	require.Len(t, v.active.markers, 3)
	r1, r2 := v.active.rowForMarker(1), v.active.rowForMarker(2)
	require.Less(t, v.active.rowForMarker(0), r1)
	require.Less(t, r1, r2)
	require.Greater(t, r2-r1, v.vp.Height,
		"markers must be spaced more than a viewport height apart so they aren't all visible")

	// Focus the first marker, then scroll down to a position between m1 and m2 —
	// the focused marker (m0) is now off-screen.
	v = v.focusMarker(1)
	require.Equal(t, 0, v.focusedMarker)
	v.vp.SetYOffset(r1 + 1)
	require.Greater(t, v.vp.YOffset, r1)
	require.Less(t, v.vp.YOffset, r2)
	require.False(t, v.focusedMarkerVisible(), "the focused marker scrolled out of view")

	// ] resolves from the viewport (the marker below the top), NOT sequentially
	// from focusedMarker — sequential would wrongly give m1.
	next := v.focusMarker(1)
	assert.Equal(t, 2, next.focusedMarker, "] from between m1 and m2 focuses m2, not the stale focusedMarker+1")

	// [ resolves to the marker above the viewport top (m1).
	prev := v.focusMarker(-1)
	assert.Equal(t, 1, prev.focusedMarker, "[ from between m1 and m2 focuses m1")
}

// TestViewer_FocusMarkerFromInitialStateUsesViewport covers the never-focused
// (focusedMarker == -1) path through the viewport branch — both from a fresh load
// and after the user scrolled without focusing anything. focusedMarkerVisible
// returns false for -1, so navigation resolves from the viewport top.
func TestViewer_FocusMarkerFromInitialStateUsesViewport(t *testing.T) {
	mk := func() viewerModel {
		return newViewerModel(DefaultStyles(), 80, 8).loadSession(multiMarkerSession(t), nil)
	}
	const last = 2 // multiMarkerSession has three markers

	// Fresh load (YOffset 0, nothing focused): ] selects the first marker, [ wraps
	// to the last.
	assert.Equal(t, 0, mk().focusMarker(1).focusedMarker, "] from a fresh load selects the first marker")
	assert.Equal(t, last, mk().focusMarker(-1).focusedMarker, "[ from a fresh load wraps to the last marker")

	// Scrolled between m0 and m1 without ever focusing: ] picks the marker below the
	// top (m1), [ the one above it (m0) — resolved from the viewport, not the -1 index.
	v := mk()
	r0, r1 := v.active.rowForMarker(0), v.active.rowForMarker(1)
	require.Less(t, r0+1, r1)
	v.vp.SetYOffset(r0 + 1)
	require.Equal(t, -1, v.focusedMarker, "no marker focused yet")
	assert.Equal(t, 1, v.focusMarker(1).focusedMarker, "] from between m0 and m1 selects m1")
	assert.Equal(t, 0, v.focusMarker(-1).focusedMarker, "[ from between m0 and m1 selects m0")
}

// TestViewer_FocusMarkerSequentialWhenVisible verifies the classic cycle is
// preserved while the focused marker stays on screen: ]/[ step marker-by-marker
// with wraparound (the pre-A4 behavior, now the "visible" branch).
func TestViewer_FocusMarkerSequentialWhenVisible(t *testing.T) {
	v := newViewerModel(DefaultStyles(), 80, 8).loadSession(multiMarkerSession(t), nil)
	require.Len(t, v.active.markers, 3)

	v = v.focusMarker(1)
	require.Equal(t, 0, v.focusedMarker)
	require.True(t, v.focusedMarkerVisible(), "focusing scrolls the marker into view")

	v = v.focusMarker(1)
	assert.Equal(t, 1, v.focusedMarker, "] from a visible marker steps to the next index")
	v = v.focusMarker(1)
	assert.Equal(t, 2, v.focusedMarker)
	v = v.focusMarker(1)
	assert.Equal(t, 0, v.focusedMarker, "] past the last marker wraps to the first")

	v = v.focusMarker(-1)
	assert.Equal(t, 2, v.focusedMarker, "[ from the first marker wraps to the last")
}

// TestViewer_FocusMarkerLastMarkerNearBottomWraps is the clamped-offset
// regression: a marker in the last viewport-height rows can't sit at the top (the
// viewport clamps YOffset below its row), so a YOffset-only "next" would re-select
// it forever. The visible branch (the marker is still visible despite the clamp)
// must step past it — ] wraps to the first, [ goes to the previous.
func TestViewer_FocusMarkerLastMarkerNearBottomWraps(t *testing.T) {
	var lines []map[string]any
	lines = append(lines, userLine("go"))
	for i := 0; i < 2; i++ {
		id := fmt.Sprintf("t%d", i)
		lines = append(lines,
			assistantLine(fmt.Sprintf("a%d", i),
				[]map[string]any{textBlock(fmt.Sprintf("step %d", i)), bashUse(id, fmt.Sprintf("cmd-%d", i))}),
			toolResultFor(id, bigBody()),
		)
		if i == 0 { // filler only after the first marker → the second is the final row
			for j := 0; j < 4; j++ {
				lines = append(lines, assistantLine(fmt.Sprintf("f%d", j),
					[]map[string]any{textBlock(fmt.Sprintf("filler %d", j))}))
			}
		}
	}
	sess := vault.Session{UUID: "lastmark00000000", RawJSONL: jsonlLines(t, lines...)}
	v := newViewerModel(DefaultStyles(), 80, 8).loadSession(sess, nil)
	require.Len(t, v.active.markers, 2)

	last := len(v.active.markers) - 1
	lastRow := v.active.rowForMarker(last)
	v.focusedMarker = last
	v.vp.SetYOffset(lastRow)
	require.Less(t, v.vp.YOffset, lastRow, "the last marker's row clamps the offset below it")
	require.True(t, v.focusedMarkerVisible(), "the clamped marker is still visible in the viewport")

	wrapped := v.focusMarker(1)
	assert.Equal(t, 0, wrapped.focusedMarker, "] from the clamped last marker wraps to the first, not stuck")

	stepped := v.focusMarker(-1)
	assert.Equal(t, last-1, stepped.focusedMarker, "[ from the clamped last marker steps to the previous")
}

// TestViewer_FocusMarkerKeepsViewportWhenVisible is the no-jump refinement: ]/[ to
// a marker that is already on screen must NOT scroll, so walking through a
// screenful of markers leaves the reading position put. (Off-screen targets still
// scroll — covered by the resolve-from-viewport and wraparound tests.)
func TestViewer_FocusMarkerKeepsViewportWhenVisible(t *testing.T) {
	var lines []map[string]any
	lines = append(lines, userLine("go"))
	// Top filler so there's scrollable content above the markers (maxYOffset > 0) —
	// otherwise the old unconditional SetYOffset would clamp to 0 and hide the bug.
	for j := 0; j < 5; j++ {
		lines = append(lines, assistantLine(fmt.Sprintf("top%d", j),
			[]map[string]any{textBlock(fmt.Sprintf("top filler %d", j))}))
	}
	for i := 0; i < 2; i++ { // two markers one turn apart → both fit one screen
		id := fmt.Sprintf("t%d", i)
		lines = append(lines,
			assistantLine(fmt.Sprintf("a%d", i),
				[]map[string]any{textBlock(fmt.Sprintf("step %d", i)), bashUse(id, fmt.Sprintf("cmd-%d", i))}),
			toolResultFor(id, bigBody()),
		)
	}
	// Bottom filler so the markers aren't in the clamped last-screen region.
	for j := 0; j < 6; j++ {
		lines = append(lines, assistantLine(fmt.Sprintf("bot%d", j),
			[]map[string]any{textBlock(fmt.Sprintf("bottom filler %d", j))}))
	}
	sess := vault.Session{UUID: "closemarks000000", RawJSONL: jsonlLines(t, lines...)}
	v := newViewerModel(DefaultStyles(), 80, 12).loadSession(sess, nil)
	require.Len(t, v.active.markers, 2)

	r0, r1 := v.active.rowForMarker(0), v.active.rowForMarker(1)
	// Scroll so BOTH markers are on screen but the first is NOT at the very top: the
	// old unconditional SetYOffset(rowForMarker) would yank it up to the top here.
	top := r0 - 2
	require.GreaterOrEqual(t, top, 1, "first marker must sit below the viewport top")
	v.vp.SetYOffset(top)
	require.Equal(t, top, v.vp.YOffset, "viewport must not clamp this offset")
	require.Less(t, r1, top+v.vp.Height, "both markers fit in the viewport window")

	// ] resolves to the on-screen first marker — viewport must stay put.
	v = v.focusMarker(1)
	require.Equal(t, 0, v.focusedMarker)
	assert.Equal(t, top, v.vp.YOffset, "focusing an on-screen marker does not scroll")
	require.True(t, v.focusedMarkerVisible())

	// ] again steps to the on-screen second marker — still no scroll.
	v = v.focusMarker(1)
	require.Equal(t, 1, v.focusedMarker)
	assert.Equal(t, top, v.vp.YOffset, "stepping to an on-screen marker does not scroll")
	require.True(t, v.focusedMarkerVisible())
}

// TestRenderTranscript_MarkerRowsAreSingleLine is the row↔line invariant guard: a
// subagent marker label that carries newlines (the launch label falls back to a
// multi-line prompt; truncateRunes keeps the newlines) must still render as exactly
// one viewport line. Otherwise msgRowStart (a row-slice index) drifts above the
// marker's true viewport line, and SetYOffset(rowForMarker) scrolls ]/[ to an
// offset that leaves the focused marker off-screen below the viewport.
func TestRenderTranscript_MarkerRowsAreSingleLine(t *testing.T) {
	msgs := []vault.TranscriptMessage{
		{Role: vault.RoleSubagent, Body: "explore the parser\nthen summarize\nthe findings",
			AgentID: "agent-a1234567", Openable: true, SourceLine: 0},
		{Role: vault.RoleAssistant, Body: "some follow-up", SourceLine: 1},
		// A non-openable subagent never enters rt.markers, but its row still lands in
		// msgRowStart. A multi-line Body here would desync every later marker's row, so
		// singleLine must flatten this path too — and it sits BEFORE the second openable
		// marker, so a regression would mis-place that marker's rowForMarker.
		{Role: vault.RoleSubagent, Body: "visible only\nsecond line", Openable: false, SourceLine: 2},
		{Role: vault.RoleSubagent, Body: "second launch", AgentID: "agent-b1234567", Openable: true, SourceLine: 3},
	}
	rt := renderTranscript(msgs, DefaultStyles(), 80)

	// No rendered row may contain an embedded newline — the viewport splits content
	// on "\n", so a multi-line row would desync the row index from the line index.
	for i, row := range rt.rows {
		assert.NotContains(t, row, "\n", "row %d must be a single line", i)
	}
	// Row count and viewport line count must agree (the SetYOffset invariant).
	assert.Equal(t, len(rt.rows), strings.Count(rt.content(), "\n")+1,
		"out.rows count must equal viewport line count")

	// Each openable marker's rowForMarker must point at the line actually holding
	// its rendered glyph — the second marker exercises the post-multi-line-label drift.
	require.Len(t, rt.markers, 2)
	lines := strings.Split(rt.content(), "\n")
	for pos := 0; pos < len(rt.markers); pos++ {
		row := rt.rowForMarker(pos)
		require.GreaterOrEqual(t, row, 0)
		require.Less(t, row, len(lines))
		assert.Contains(t, lines[row], "subagent", "marker %d row points at its own line", pos)
	}
}
