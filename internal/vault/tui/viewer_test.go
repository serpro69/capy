package tui

import (
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// key builds a tea.KeyMsg whose String() equals s — the form the models (and
// the wrapped bubbles components) switch on. Special keys use their proper Type;
// printable runes use KeyRunes.
func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case " ":
		// bubbletea reports a space press as KeySpace carrying the ' ' rune
		// (key.go detectOneMsg); textinput inserts msg.Runes, so the rune must
		// be present or typing a space into a filter/query/name drops it.
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "ctrl+d":
		return tea.KeyMsg{Type: tea.KeyCtrlD}
	case "ctrl+e":
		return tea.KeyMsg{Type: tea.KeyCtrlE}
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
	v, _, action := v.Update(keyMsg("esc"))
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

	v, _ = v.openFocusedMarker()
	assert.True(t, v.inSub)
	assert.Equal(t, "xyz", v.subID)
}

func TestViewer_FocusMarkerNoopWithoutMarkers(t *testing.T) {
	// A session with no subagents → no openable markers.
	sess := vault.Session{UUID: "u1234567", RawJSONL: jsonlLines(t, userLine("hi"))}
	v := newViewerModel(DefaultStyles(), 80, 24).loadSession(sess, nil)
	v = v.focusMarker(1)
	assert.Equal(t, -1, v.focusedMarker)
	v, _ = v.openFocusedMarker()
	assert.False(t, v.inSub)
}

// loadedCodexParent is a viewer over a Codex parent rollout whose one openable
// marker carries the child's uuid.
func loadedCodexParent(t *testing.T, height int) viewerModel {
	t.Helper()
	sess := codexParentSession(t, codexParentID, codexChildID, 0)
	return newViewerModel(DefaultStyles(), 80, height).loadSession(sess, nil)
}

// TestViewer_OpenChildMarkerReturnsActionWithoutMutation pins the root-routed
// contract: a focused marker with a ChildUUID makes openFocusedMarker return
// openChildAction(uuid) and leave the viewer exactly as it was — the viewer
// owns no store, so it must not try to load (or pretend to open) the child.
func TestViewer_OpenChildMarkerReturnsActionWithoutMutation(t *testing.T) {
	v := loadedCodexParent(t, 24)
	require.Len(t, v.active.markers, 1, "the resolved spawn is one openable marker")
	require.Equal(t, codexChildID, v.active.messages[v.active.markers[0]].ChildUUID)

	v = v.focusMarker(1)
	require.Equal(t, 0, v.focusedMarker)
	before := v

	after, action := v.openFocusedMarker()
	assert.Equal(t, openChildAction(codexChildID), action)
	assert.Equal(t, viewerActionOpenChild, action.kind)
	assert.Equal(t, before.sess.UUID, after.sess.UUID)
	assert.False(t, after.inDetail(), "no detail view is opened for a child session")
	assert.Equal(t, before.focusedMarker, after.focusedMarker)
	assert.Equal(t, before.vp.YOffset, after.vp.YOffset)
	assert.Equal(t, before.active.content(), after.active.content())

	// The keystroke path surfaces the same action.
	_, _, viaKey := v.Update(keyMsg("enter"))
	assert.Equal(t, openChildAction(codexChildID), viaKey)
}

// TestViewer_OpenMarkerActionsForOtherKinds pins that only a ChildUUID marker
// yields the open-child action: a Claude sidecar marker opens in place, an
// unfocused viewer and a non-openable marker are viewerNone.
func TestViewer_OpenMarkerActionsForOtherKinds(t *testing.T) {
	v := loadedViewer(t) // Claude sample: one sidecar-backed marker
	_, action := v.openFocusedMarker()
	assert.Equal(t, viewerNone, action, "nothing focused")

	v = v.focusMarker(1)
	opened, action := v.openFocusedMarker()
	assert.Equal(t, viewerNone, action, "a sidecar marker is viewer-local")
	assert.True(t, opened.inSub)

	// An unresolved Codex launch (no ChildUUID, no sidecar ids) is visible only.
	sess := codexSession(t, codexParentID, "", "unresolved",
		codexHumanLine("go"), codexAssistantLine("spawning"),
		codexEnv("response_item", map[string]any{"type": "function_call", "name": "spawn_agent", "call_id": "c1",
			"arguments": `{"task_name":"review"}`}))
	u := newViewerModel(DefaultStyles(), 80, 24).loadSession(sess, nil)
	assert.Empty(t, u.active.markers, "an unresolved launch is not openable")
	_, action = u.openFocusedMarker()
	assert.Equal(t, viewerNone, action)
}

// TestViewer_ResizeInDetailRendersUnderRightPlatform pins activePlatform on the
// re-wrap path: a sidecar detail re-renders under Claude whatever the session
// is, an inline tool-body detail under the session's platform; both stay open
// across the resize.
func TestViewer_ResizeInDetailRendersUnderRightPlatform(t *testing.T) {
	// Inline detail on a Codex session: a collapsed tool result opened standalone.
	sess := codexSession(t, codexParentID, "", "inline", codexHumanLine("go"), codexAssistantLine("ok"))
	v := newViewerModel(DefaultStyles(), 80, 24).loadSession(sess, nil)
	v = v.openInlineContent(vault.TranscriptMessage{Role: vault.RoleTool, Body: "tool body", ToolSummary: "exec_command ls"})
	require.True(t, v.inInline)
	assert.Equal(t, vault.PlatformCodex, v.activePlatform())
	v = v.setSize(60, 24)
	assert.True(t, v.inInline, "the inline detail survives the re-wrap")
	assert.Contains(t, v.active.content(), "tool body")

	// Sidecar detail: always Claude.
	c := loadedViewer(t).openSubagent("xyz", 0)
	require.True(t, c.inSub)
	assert.Equal(t, vault.PlatformClaudeCode, c.activePlatform())
	c = c.setSize(60, 24)
	assert.True(t, c.inSub)
	assert.Contains(t, c.active.content(), "Claude", "sidecar assistant header stays Claude")
}

// TestViewer_HeaderIsPlatformAware pins the location segment: the platform name
// for every session, and "child of <parent short id>" for a child.
func TestViewer_HeaderIsPlatformAware(t *testing.T) {
	claude := loadedViewer(t)
	assert.Contains(t, claude.header(), shortID("abcdef0123456789")+" · Claude · ")
	assert.NotContains(t, claude.header(), "child of")

	parent := loadedCodexParent(t, 24)
	assert.Contains(t, parent.header(), shortID(codexParentID)+" · Codex · ")
	assert.NotContains(t, parent.header(), "child of")

	child := newViewerModel(DefaultStyles(), 100, 24).loadSession(codexChildSession(t, codexChildID, codexParentID), nil)
	assert.Contains(t, child.header(), "Codex · child of "+shortID(codexParentID))
	assert.Contains(t, child.View(), "Codex", "the assistant header names the platform too")
}

func TestViewer_BackActionAtMainLevel(t *testing.T) {
	v := loadedViewer(t)
	_, _, action := v.Update(keyMsg("q"))
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
		v, _, action = v.Update(keyMsg(k))
		assert.Equal(t, viewerNone, action)
	}
	v, _, _ = v.Update(keyMsg("G"))
	assert.Greater(t, v.vp.YOffset, 0, "G scrolls to the bottom of a tall transcript")
}
