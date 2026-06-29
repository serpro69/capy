package tui

import (
	"fmt"
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
