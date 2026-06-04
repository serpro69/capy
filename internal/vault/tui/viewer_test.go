package tui

import (
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/serpro69/capy/internal/vault"
)

// key builds a tea.KeyMsg whose String() equals s — the form the models (and
// the wrapped bubbles components) switch on. Special keys use their proper Type;
// printable runes use KeyRunes.
func key(s string) tea.KeyMsg {
	switch s {
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "ctrl+d":
		return tea.KeyMsg{Type: tea.KeyCtrlD}
	case "ctrl+u":
		return tea.KeyMsg{Type: tea.KeyCtrlU}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func loadedViewer(t *testing.T) viewerModel {
	t.Helper()
	sess, files := sampleSession(t)
	v := newViewerModel(DefaultStyles(), 80, 24)
	v = v.loadSession(sess, files)
	return v
}

func TestViewer_LoadSession(t *testing.T) {
	v := loadedViewer(t)
	assert.True(t, v.ready)
	assert.False(t, v.inSub)
	assert.Equal(t, []string{"xyz"}, v.subIDs)
	assert.Len(t, v.active.markers, 1, "one openable subagent marker")
}

func TestViewer_JumpToSubagentAndReturn(t *testing.T) {
	v := loadedViewer(t)

	v = v.jumpTo("xyz", 1)
	require.True(t, v.inSub)
	assert.Equal(t, "xyz", v.subID)
	assert.Contains(t, v.active.content(), "subagent findings about the bug")

	// esc returns to the main session, not out of the viewer.
	v, _, action := v.Update(key("esc"))
	assert.Equal(t, viewerNone, action)
	assert.False(t, v.inSub)
	assert.Empty(t, v.subID)
}

func TestViewer_JumpToUnknownSubagentIsNoop(t *testing.T) {
	v := loadedViewer(t)
	v = v.jumpTo("does-not-exist", 0)
	assert.False(t, v.inSub, "an unarchived subagent id leaves the viewer on the main session")
}

func TestViewer_JumpToMainLineWhileInSubagentReturns(t *testing.T) {
	v := loadedViewer(t)
	v = v.jumpTo("xyz", 1)
	require.True(t, v.inSub)
	v = v.jumpTo("", 2) // a main-session hit
	assert.False(t, v.inSub, "a main-session jump exits the standalone subagent view")
}

func TestViewer_FocusAndOpenMarker(t *testing.T) {
	v := loadedViewer(t)

	v = v.focusMarker(1)
	assert.Equal(t, 0, v.focusedMarker)

	v = v.openFocusedMarker()
	assert.True(t, v.inSub)
	assert.Equal(t, "xyz", v.subID)
}

func TestViewer_FocusMarkerNoopWithoutMarkers(t *testing.T) {
	// A session with no subagents → no openable markers.
	sess := vault.Session{UUID: "u1234567", RawJSONL: jsonlLines(t, userLine("hi"))}
	v := newViewerModel(DefaultStyles(), 80, 24).loadSession(sess, nil)
	v = v.focusMarker(1)
	assert.Equal(t, -1, v.focusedMarker)
	v = v.openFocusedMarker()
	assert.False(t, v.inSub)
}

func TestViewer_BackActionAtMainLevel(t *testing.T) {
	v := loadedViewer(t)
	_, _, action := v.Update(key("q"))
	assert.Equal(t, viewerBack, action, "q at the main session asks the app to go back")
}

func TestViewer_ScrollsOnJumpToDeepLine(t *testing.T) {
	// A transcript taller than the viewport so a jump to a late line scrolls down.
	var lines []map[string]any
	for i := range 60 {
		lines = append(lines, assistantLine(fmt.Sprintf("m%d", i),
			[]map[string]any{textBlock(fmt.Sprintf("message body number %d", i))}))
	}
	sess := vault.Session{UUID: "deadbeef00", RawJSONL: jsonlLines(t, lines...)}
	v := newViewerModel(DefaultStyles(), 80, 10).loadSession(sess, nil)

	v = v.jumpTo("", 58)
	assert.Greater(t, v.vp.YOffset, 0, "jumping to a late line scrolls the viewport down")
}

func TestViewer_SubagentBytesLookup(t *testing.T) {
	v := loadedViewer(t)
	assert.NotNil(t, v.subagentBytes("xyz"))
	assert.Nil(t, v.subagentBytes("nope"))
}

// TestViewer_FocusHighlightsMarker verifies the focused marker is re-styled in
// the viewport content (not merely scrolled to) — the markers-only feedback fix.
func TestViewer_FocusHighlightsMarker(t *testing.T) {
	v := loadedViewer(t)
	require.Len(t, v.active.markers, 1)

	assert.Equal(t, v.active.content(), v.viewportContent(), "no focus → plain content")

	v = v.focusMarker(1)
	require.Equal(t, 0, v.focusedMarker)
	assert.NotEqual(t, v.active.content(), v.viewportContent(),
		"a focused marker re-styles its row, so content differs from the unfocused render")
}

// TestViewer_ScrollSurvivesResizeAcrossSubagent exercises the savedMainLine fix:
// scroll the main session, open a subagent, resize (re-wrap), then return — the
// main session must not snap back to the top.
func TestViewer_ScrollSurvivesResizeAcrossSubagent(t *testing.T) {
	// Tall main transcript with a subagent launch near the end.
	var lines []map[string]any
	for i := range 50 {
		lines = append(lines, assistantLine(fmt.Sprintf("m%d", i),
			[]map[string]any{textBlock(fmt.Sprintf("body line %d", i))}))
	}
	lines = append(lines, assistantLine("mlast", []map[string]any{taskBlock("late agent")}))
	sess := vault.Session{UUID: "cafe000000", RawJSONL: jsonlLines(t, lines...)}
	sub := jsonlLines(t, assistantLine("s1", []map[string]any{textBlock("sub body")}))
	files := []vault.File{{RelativePath: "subagents/agent-z.jsonl", RawContent: sub}}

	v := newViewerModel(DefaultStyles(), 80, 12).loadSession(sess, files)
	v = v.jumpTo("", 40) // scroll the main session down
	scrolled := v.vp.YOffset
	require.Greater(t, scrolled, 0)

	v = v.openSubagent("z", 0)
	require.True(t, v.inSub)
	v = v.setSize(60, 12) // resize/re-wrap while in the subagent
	v = v.returnToMain()

	assert.False(t, v.inSub)
	assert.Greater(t, v.vp.YOffset, 0, "main scroll position is preserved across the resize round-trip, not reset to top")
}

func TestViewer_ViewRendersHeaderAndContent(t *testing.T) {
	v := loadedViewer(t)
	out := v.View()
	assert.Contains(t, out, "Timeout investigation", "header shows the session title")
	assert.Contains(t, out, "answer one", "main transcript body is visible")

	// In a subagent the header notes which subagent is shown.
	v = v.jumpTo("xyz", 0)
	out = v.View()
	assert.Contains(t, out, "subagent")
	assert.Contains(t, out, "return to session", "subagent help line offers return")
}

func TestViewer_ViewBeforeLoad(t *testing.T) {
	v := newViewerModel(DefaultStyles(), 80, 24)
	assert.Equal(t, "no session loaded", v.View())
}

// TestViewer_NavKeysDoNotPanic exercises the scroll keybindings on a tall
// transcript and confirms downward keys advance the offset.
func TestViewer_NavKeysDoNotPanic(t *testing.T) {
	var lines []map[string]any
	for i := range 40 {
		lines = append(lines, assistantLine(fmt.Sprintf("m%d", i),
			[]map[string]any{textBlock(fmt.Sprintf("body %d", i))}))
	}
	sess := vault.Session{UUID: "feedface00", RawJSONL: jsonlLines(t, lines...)}
	v := newViewerModel(DefaultStyles(), 80, 10).loadSession(sess, nil)

	for _, k := range []string{"j", "j", "G", "k", "g", "ctrl+d", "ctrl+u", " ", "b"} {
		var action viewerAction
		v, _, action = v.Update(key(k))
		assert.Equal(t, viewerNone, action)
	}
	v, _, _ = v.Update(key("G"))
	assert.Greater(t, v.vp.YOffset, 0, "G scrolls to the bottom of a tall transcript")
}
