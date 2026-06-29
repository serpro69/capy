package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bashUseBlock is an assistant Bash tool_use whose later tool_result (tool_use_id
// "t1") the collapse tests reference.
func bashUseBlock() map[string]any {
	return map[string]any{"type": "tool_use", "id": "t1", "name": "Bash",
		"input": map[string]any{"command": "go test ./..."}}
}

// toolResultLine is a user entry carrying one tool_result for tool_use_id "t1".
func toolResultLine(body string) map[string]any {
	return map[string]any{
		"type": "user", "timestamp": "2026-05-01T10:00:10Z", "cwd": "/p", "gitBranch": "main",
		"message": map[string]any{"role": "user", "content": []map[string]any{
			{"type": "tool_result", "tool_use_id": "t1", "content": body},
		}},
	}
}

// collapseSession builds a viewer over a transcript whose single Bash tool_result
// has the given body — collapsing turns purely on the size/line threshold.
func collapseSession(t *testing.T, body string) viewerModel {
	t.Helper()
	main := jsonlLines(t,
		userLine("run the tests"),
		assistantLine("m1", []map[string]any{textBlock("Running."), bashUseBlock()}),
		toolResultLine(body),
	)
	sess := vault.Session{UUID: "abc1234567", Title: "collapse test", RawJSONL: main}
	return newViewerModel(DefaultStyles(), 80, 24).loadSession(sess, nil)
}

// bigBody returns a body with more than collapseToolResultLines lines. 30 is
// comfortably over the current threshold (vault.collapseToolResultLines = 20,
// unexported across the package boundary) — bump if that constant grows past 30.
func bigBody() string {
	var b strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&b, "log line %d\n", i)
	}
	return strings.TrimRight(b.String(), "\n")
}

func TestViewer_LargeToolResultIsOpenableMarker(t *testing.T) {
	v := collapseSession(t, bigBody())
	require.Len(t, v.active.markers, 1, "the large Bash result is the one openable marker")

	// The marker row carries the call summary and the expand affordance.
	row := v.active.rowForMarker(0)
	require.GreaterOrEqual(t, row, 0)
	assert.Contains(t, v.active.rows[row], "Bash go test")
	assert.Contains(t, v.active.rows[row], "expand")
	// The full body is NOT dumped inline — it lives behind the marker.
	assert.NotContains(t, v.active.content(), "log line 17")
}

func TestViewer_OpenAndReturnInlineToolResult(t *testing.T) {
	v := collapseSession(t, bigBody())

	v = v.focusMarker(1)
	require.Equal(t, 0, v.focusedMarker)

	v = v.openFocusedMarker()
	require.True(t, v.inInline, "enter on a collapsed tool marker opens the inline body")
	assert.False(t, v.inSub, "inline detail is not a subagent view")
	assert.Contains(t, v.active.content(), "log line 17", "the full body is shown on open")

	out := v.View()
	assert.Contains(t, out, "Bash go test", "the header labels the inline detail with the call summary")
	assert.Contains(t, out, "return to session", "the inline detail offers a return")

	// esc returns to the main session (not out of the viewer).
	v, _, action := v.Update(keyMsg("esc"))
	assert.Equal(t, viewerNone, action)
	assert.False(t, v.inInline, "esc returns to the main session from the inline detail")
}

func TestViewer_SmallToolResultStaysInline(t *testing.T) {
	v := collapseSession(t, "one short line")
	assert.Empty(t, v.active.markers, "a small non-excluded result is not collapsed to a marker")
	assert.Contains(t, v.active.content(), "one short line", "the small result renders inline")
}

func TestViewer_ExcludedToolResultCollapsesRegardlessOfSize(t *testing.T) {
	// A tiny Read result still collapses (excludedResultTools), unlike the small
	// Bash result above.
	main := jsonlLines(t,
		userLine("read it"),
		assistantLine("m1", []map[string]any{
			{"type": "tool_use", "id": "t1", "name": "Read",
				"input": map[string]any{"file_path": "/p/main.go"}},
		}),
		toolResultLine("package main"),
	)
	sess := vault.Session{UUID: "def1234567", RawJSONL: main}
	v := newViewerModel(DefaultStyles(), 80, 24).loadSession(sess, nil)

	require.Len(t, v.active.markers, 1, "an excluded Read result collapses even when tiny")
	row := v.active.rowForMarker(0)
	assert.Contains(t, v.active.rows[row], "Read /p/main.go")
}
