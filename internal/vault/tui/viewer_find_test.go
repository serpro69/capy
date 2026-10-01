package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/cursor"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func findTestViewer(t *testing.T, messages []vault.TranscriptMessage, width, height int) viewerModel {
	t.Helper()
	v := newViewerModel(DefaultStyles(), width, height)
	v.sess = vault.Session{UUID: "find-test", Title: "Find test"}
	v.ready = true
	v.main = renderTranscript(vault.PlatformClaudeCode, messages, v.styles, v.contentWidth())
	v = v.setActive(v.main, 0)
	v.find.ctx = t.Context()
	return v
}

func settleViewerFind(t *testing.T, v viewerModel, cmd tea.Cmd) viewerModel {
	t.Helper()
	for iterations := 0; cmd != nil; iterations++ {
		require.Less(t, iterations, 10, "command chain must retire")
		messages := runCmds(cmd)
		cmd = nil
		for _, msg := range messages {
			if _, ok := msg.(findResultMsg); !ok {
				continue // cursor cosmetics do not own search state
			}
			var next tea.Cmd
			v, next, _ = v.Update(msg)
			cmd = tea.Batch(cmd, next)
		}
	}
	return v
}

func findKey(t *testing.T, v viewerModel, key string) viewerModel {
	t.Helper()
	v, cmd, _ := v.Update(keyMsg(key))
	if v.find.editing {
		v.find.input.Cursor.SetMode(cursor.CursorStatic)
	}
	return settleViewerFind(t, v, cmd)
}

func searchViewer(t *testing.T, v viewerModel, query string) viewerModel {
	t.Helper()
	v = findKey(t, v, "/")
	v = findKey(t, v, "ctrl+u")
	v = findKey(t, v, query)
	return findKey(t, v, "enter")
}

func TestViewerFind(t *testing.T) {
	v := findTestViewer(t, []vault.TranscriptMessage{{Role: vault.RoleAssistant, Body: "hit hit banana"}}, 80, 8)
	v = searchViewer(t, v, "hit")
	assert.False(t, v.find.editing)
	assert.Contains(t, ansi.Strip(v.View()), "[hit] hit banana")
	assert.Contains(t, v.View(), "1/2")
	v = findKey(t, v, "n")
	assert.Contains(t, ansi.Strip(v.View()), "hit [hit] banana")
	assert.Contains(t, v.View(), "2/2")
	v = findKey(t, v, "n")
	assert.Contains(t, v.View(), "wrapped")
	v = findKey(t, v, "N")
	assert.Equal(t, 1, v.find.view.selected)
	assert.Contains(t, v.View(), "wrapped")
	v = searchViewer(t, v, "ana")
	assert.Len(t, v.find.view.hits, 2)
	v = findKey(t, v, "n")
	assert.Contains(t, ansi.Strip(v.View()), "ban[ana]")
	v = findKey(t, v, "esc")
	assert.False(t, v.find.view.plain)
	assert.Empty(t, v.find.view.query)
	_, _, action := v.Update(keyMsg("esc"))
	assert.Equal(t, viewerBack, action)
}

func TestViewerFindCancelAndEmpty(t *testing.T) {
	v := findTestViewer(t, []vault.TranscriptMessage{{Role: vault.RoleAssistant, Body: strings.Repeat("before\n", 20) + "hit hit\nafter"}}, 80, 7)
	v.vp.SetYOffset(12)
	before := v.View()
	v = findKey(t, v, "/")
	v = findKey(t, v, "hit")
	require.NotEqual(t, 12, v.vp.YOffset)
	v = findKey(t, v, "esc")
	assert.Equal(t, before, v.View(), "cancel restores the normal renderer and exact offset")
	v = searchViewer(t, v, "hit")
	v = findKey(t, v, "n")
	before = v.View()
	v = findKey(t, v, "/")
	v = findKey(t, v, "ctrl+u")
	v = findKey(t, v, "before")
	v = findKey(t, v, "esc")
	assert.Equal(t, before, v.View(), "cancel restores the committed query and occurrence")
	v = findKey(t, v, "/")
	v = findKey(t, v, "ctrl+u")
	assert.Empty(t, v.find.input.Value())
	v = findKey(t, v, "enter")
	assert.False(t, v.find.view.plain)
	assert.Empty(t, v.find.view.query)
}

func TestViewerFindStableAnchor(t *testing.T) {
	v := findTestViewer(t, []vault.TranscriptMessage{{Body: "ab\n" + strings.Repeat("filler\n", 40) + "apple"}}, 30, 7)
	v = findKey(t, v, "/")
	v = findKey(t, v, "a")
	v = findKey(t, v, "p")
	require.Greater(t, v.vp.YOffset, 20)
	v = findKey(t, v, "backspace")
	assert.Equal(t, 0, v.find.view.selected, "preview movement must not move the transaction's origin")
	assert.Less(t, v.vp.YOffset, 5)
	v = findKey(t, v, "enter")
	v = findKey(t, v, "G")
	v = findKey(t, v, "n")
	assert.Equal(t, 1, v.find.view.selected, "manual scrolling does not replace selected occurrence")
}

func TestViewerFindNoMatchAndMarkerKeys(t *testing.T) {
	v := loadedViewer(t)
	v = searchViewer(t, v, "does not exist")
	offset := v.vp.YOffset
	assert.Contains(t, v.View(), "no matches")
	v = findKey(t, v, "n")
	v = findKey(t, v, "N")
	assert.Equal(t, offset, v.vp.YOffset)
	assert.Equal(t, -1, v.focusedMarker, "a no-match query still owns n/N")
	v = findKey(t, v, "]")
	assert.Equal(t, 0, v.focusedMarker)
	assert.Contains(t, ansi.Strip(v.View()), "▶")
	v = findKey(t, v, "esc")
	v = findKey(t, v, "n")
	assert.Equal(t, 0, v.focusedMarker, "n resumes marker navigation after clear")
}

func TestViewerFindResize(t *testing.T) {
	v := findTestViewer(t, []vault.TranscriptMessage{{Body: strings.Repeat("filler ", 80) + "needle needle"}}, 50, 8)
	v = searchViewer(t, v, "needle")
	v = findKey(t, v, "n")
	hit := v.find.view.hits[v.find.view.selected]
	projection := v.find.view.projection
	v = v.setSize(50, 6)
	assert.Same(t, projection, v.find.view.projection, "height alone does not rewrap")
	for _, size := range [][2]int{{19, 8}, {2, 1}, {1, 2}, {6, 3}, {60, 12}} {
		v = v.setSize(size[0], size[1])
		var cmd tea.Cmd
		v, cmd = v.nextFindCommand()
		v = settleViewerFind(t, v, cmd)
		assert.Equal(t, hit, v.find.view.hits[v.find.view.selected])
		rows := strings.Split(v.View(), "\n")
		assert.LessOrEqual(t, len(rows), size[1])
		for _, row := range rows {
			assert.LessOrEqual(t, ansi.StringWidth(row), size[0])
		}
	}
	assert.Contains(t, ansi.Strip(v.View()), "needle [needle]")
}

func TestViewerFindCancelAfterResize(t *testing.T) {
	for _, plain := range []bool{false, true} {
		t.Run(fmt.Sprint(plain), func(t *testing.T) {
			v := findTestViewer(t, []vault.TranscriptMessage{{Body: "intro"}, {Body: strings.Repeat("word ", 100) + "hit hit"}}, 45, 9)
			v.vp.SetYOffset(v.active.msgRowStart[1] + 5)
			if plain {
				v = searchViewer(t, v, "hit")
				v = findKey(t, v, "n")
			}
			selected := v.find.view.selected
			reading := v.findReadingAnchor()
			v = findKey(t, v, "/")
			v = findKey(t, v, "ctrl+u")
			v = findKey(t, v, "intro")
			v = v.setSize(24, 9)
			var cmd tea.Cmd
			v, cmd = v.nextFindCommand()
			v = settleViewerFind(t, v, cmd)
			v = findKey(t, v, "esc")
			assert.Equal(t, plain, v.find.view.plain)
			if plain {
				assert.Equal(t, selected, v.find.view.selected)
				assert.Contains(t, v.find.view.query, "hit")
				expected := v.find.view.projection.rowForPosition(v.find.view.corpus, reading)
				assert.Equal(t, min(expected, len(v.active.rows)-v.vp.Height), v.vp.YOffset,
					"cancel restores the original reading anchor even when the selected hit is now below the viewport")
			} else {
				assert.Equal(t, v.active.msgRowStart[1], v.vp.YOffset)
			}
		})
	}
}

func TestViewerFindAsyncLatestPending(t *testing.T) {
	v := findTestViewer(t, []vault.TranscriptMessage{{Body: "ab abc abcd"}}, 80, 10)
	v, first, _ := v.Update(keyMsg("/"))
	v.find.input.Cursor.SetMode(cursor.CursorStatic)
	firstID := v.find.running
	for _, text := range []string{"a", "b", "c"} {
		var cmd tea.Cmd
		v, cmd, _ = v.Update(keyMsg(text))
		assert.Nil(t, cmd, "only the latest pending slot changes while running")
		assert.Equal(t, firstID, v.find.running)
	}
	v, _, _ = v.Update(keyMsg("enter"))
	require.True(t, v.find.editing, "pending submit must not commit an old preview")
	stale, ok := first().(findResultMsg)
	require.True(t, ok)
	require.ErrorIs(t, stale.err, context.Canceled)
	v, latest, _ := v.Update(stale)
	require.NotNil(t, latest)
	assert.Equal(t, "abc", v.find.input.Value())
	assert.Nil(t, v.find.pending)
	newID := v.find.running
	v, cmd, _ := v.Update(stale) // duplicate / out-of-order completion
	assert.Nil(t, cmd)
	assert.Equal(t, newID, v.find.running)
	v = settleViewerFind(t, v, latest)
	assert.False(t, v.find.editing)
	assert.Equal(t, "abc", v.find.view.query)
	assert.Len(t, v.find.view.hits, 2)
	assert.Equal(t, findWorkID{}, v.find.running)
}

func TestViewerFindAsyncCancelAndStaleLayout(t *testing.T) {
	v := findTestViewer(t, []vault.TranscriptMessage{{Body: "needle"}}, 80, 10)
	v, first, _ := v.Update(keyMsg("/"))
	ready := first() // completes, but is not yet delivered
	v = v.setSize(20, 10)
	v, next, _ := v.Update(ready)
	require.NotNil(t, next)
	assert.False(t, v.find.view.plain, "old-width result cannot apply")
	v = settleViewerFind(t, v, next)
	assert.Equal(t, 19, v.find.view.projection.width)
	v.find.input.Cursor.SetMode(cursor.CursorStatic)
	v, pending, _ := v.Update(keyMsg("needle"))
	before := *v.find.snapshot
	v, _, _ = v.Update(keyMsg("esc"))
	v = settleViewerFind(t, v, pending)
	// Cancellation may queue a normal-render restore at the new width.
	if v.find.pending != nil {
		v, next = v.nextFindCommand()
		v = settleViewerFind(t, v, next)
	}
	assert.Equal(t, before.view.query, v.find.view.query)
	assert.False(t, v.find.editing)
	assert.Equal(t, findWorkID{}, v.find.running)
	v = searchViewer(t, v, "needle")
	assert.Len(t, v.find.view.hits, 1, "cancelled work must not leave the controller busy")
}

func TestViewerFindAsyncErrorAndRetry(t *testing.T) {
	v := findTestViewer(t, []vault.TranscriptMessage{{Body: "needle"}}, 80, 10)
	v = findKey(t, v, "/")
	v, cmd, _ := v.Update(keyMsg("needle"))
	require.NotNil(t, cmd)
	id := v.find.running
	v = v.applyFindResult(findResultMsg{id: id, err: errors.New("projection failed")}, true)
	assert.Contains(t, v.View(), "find error")
	assert.Equal(t, "needle", v.find.input.Value())
	v = findKey(t, v, "enter")
	assert.Equal(t, "needle", v.find.view.query)
	assert.False(t, v.find.editing)
}

func settleAppFind(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	for i := 0; cmd != nil; i++ {
		require.Less(t, i, 10)
		messages := runCmds(cmd)
		cmd = nil
		for _, msg := range messages {
			if _, ok := msg.(findResultMsg); !ok {
				continue
			}
			next, follow := m.Update(msg)
			m, cmd = next.(Model), tea.Batch(cmd, follow)
		}
	}
	return m
}

func TestAppFindRouting(t *testing.T) {
	m, st := newTestApp(t, Options{Mode: "view", SessionID: "abcdef01"})
	var clipboard bytes.Buffer
	m.clipOut = &clipboard
	original := bytes.Clone(m.viewer.sess.RawJSONL)
	listCalls := st.listCalls
	next, cmd := m.Update(keyMsg("/"))
	m = next.(Model)
	m.viewer.find.input.Cursor.SetMode(cursor.CursorStatic)
	m = settleAppFind(t, m, cmd)
	for _, key := range []string{"r", "R", "e", "c", "v", "q", "n", "N", "j", "k", "g", "G", "b", " ", "/", "[", "]"} {
		next, cmd = m.Update(keyMsg(key))
		m = settleAppFind(t, next.(Model), cmd)
		assert.Equal(t, modeView, m.mode)
		assert.False(t, m.renaming)
		assert.Equal(t, ActionNone, m.action.Kind)
		assert.False(t, m.quitting)
	}
	assert.Equal(t, "rRecvqnNjkgGb /[]", m.viewer.find.input.Value())
	next, cmd = m.Update(keyMsg("ctrl+g"))
	m = settleAppFind(t, next.(Model), cmd)
	assert.False(t, m.renaming, "root project editor cannot interrupt local input")
	assert.Equal(t, 0, st.renameCalls)
	assert.Equal(t, 0, st.projectCalls)
	assert.Equal(t, 0, st.searchCalls)
	assert.Equal(t, listCalls, st.listCalls)
	assert.Empty(t, clipboard.String())
	assert.Equal(t, original, st.sessions[0].RawJSONL)
	next, cmd = m.Update(keyMsg("ctrl+c"))
	assert.True(t, next.(Model).quitting)
	require.NotNil(t, cmd)
	assert.IsType(t, tea.QuitMsg{}, cmd())
}

func TestAppFindAsyncSameSessionReopen(t *testing.T) {
	m, _ := newTestApp(t, Options{Mode: "view", SessionID: "abcdef01"})
	next, first := m.Update(keyMsg("/"))
	m = next.(Model)
	old := first()
	oldEpoch := m.viewer.find.epoch
	var err error
	m, err = m.openSession("abcdef01", modeList, "", 0)
	require.NoError(t, err)
	require.Greater(t, m.viewer.find.epoch, oldEpoch)
	next, current := m.Update(keyMsg("/"))
	m = next.(Model)
	next, _ = m.Update(old)
	m = next.(Model)
	assert.False(t, m.viewer.find.view.plain)
	assert.NotEqual(t, findWorkID{}, m.viewer.find.running)
	m = settleAppFind(t, m, current)
	assert.True(t, m.viewer.find.view.plain)
}

func TestAppFindPlatforms(t *testing.T) {
	claude, _ := sampleSession(t)
	codex := codexSession(t, codexParentID, "", "Find", codexHumanLine("literal needle needle"))
	for _, sess := range []vault.Session{claude, codex} {
		t.Run(string(sess.Platform.OrClaude()), func(t *testing.T) {
			v := newViewerModel(DefaultStyles(), 100, 20).loadSession(sess, nil)
			query := "question"
			if sess.Platform == vault.PlatformCodex {
				query = "needle"
			}
			v = searchViewer(t, v, query)
			assert.Len(t, v.find.view.hits, 2)
			assert.Contains(t, ansi.Strip(v.View()), "["+query+"]")
		})
	}
}

func TestViewerFindCancelRestoresReadingAnchorAfterResize(t *testing.T) {
	v := findTestViewer(t, []vault.TranscriptMessage{{Body: "hit near the start\n" + strings.Repeat("reading further down the transcript\n", 90)}}, 55, 9)
	v = searchViewer(t, v, "hit")
	v.vp.SetYOffset(65)
	reading := v.findReadingAnchor()
	v = findKey(t, v, "/")
	v = findKey(t, v, "ctrl+u")
	v = findKey(t, v, "start")
	v = v.setSize(30, 9)
	v, cmd := v.nextFindCommand()
	v = settleViewerFind(t, v, cmd)
	v = findKey(t, v, "esc")
	assert.Equal(t, 0, v.find.view.selected, "selected occurrence is independent of reading position")
	assert.Equal(t, v.find.view.projection.rowForPosition(v.find.view.corpus, reading), v.vp.YOffset)
	assert.Greater(t, v.vp.YOffset, 60, "cancel must not jump back to the selected hit")
}

func TestViewerFindResizeDuringNormalRestore(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			v := findTestViewer(t, []vault.TranscriptMessage{{Role: vault.RoleAssistant, Body: strings.Repeat("markdown **hit** paragraph\n", 40)}}, 60, 10)
			v = findKey(t, v, "/")
			v = findKey(t, v, "hit")
			if committed {
				v = findKey(t, v, "enter")
			}
			v = v.setSize(40, 10)
			v, cmd := v.nextFindCommand()
			v = settleViewerFind(t, v, cmd)
			v, restore, _ := v.Update(keyMsg("esc"))
			require.NotNil(t, restore)
			v = v.setSize(25, 10) // supersede the still-undelivered normal render
			v = settleViewerFind(t, v, restore)
			assert.False(t, v.find.view.plain)
			assert.False(t, v.find.editing)
			assert.Empty(t, v.find.view.query)
			assert.Equal(t, v.contentWidth(), v.find.view.normalWidth)
			assert.Equal(t, findWorkID{}, v.find.running)
			assert.Nil(t, v.find.pending)
		})
	}
}

func TestViewerFindClearMarkerFocus(t *testing.T) {
	v := findTestViewer(t, []vault.TranscriptMessage{
		{Role: vault.RoleTool, Collapsed: true, ToolSummary: "first", Body: "first body"},
		{Role: vault.RoleTool, Collapsed: true, ToolSummary: "second", Body: "second body"},
	}, 80, 20)
	v = v.focusMarker(1)
	require.Equal(t, 0, v.focusedMarker)
	v = searchViewer(t, v, "missing")
	v = findKey(t, v, "]")
	require.Equal(t, 1, v.focusedMarker)
	v = findKey(t, v, "esc")
	assert.Contains(t, ansi.Strip(v.vp.View()), "▶ second")
	assert.NotContains(t, ansi.Strip(v.vp.View()), "▶ first")
	v, _, _ = v.Update(keyMsg("enter"))
	assert.Contains(t, v.View(), "second body")
}

func TestViewerFindMarkerChangeDuringRestore(t *testing.T) {
	v := findTestViewer(t, []vault.TranscriptMessage{
		{Role: vault.RoleTool, Collapsed: true, ToolSummary: "first", Body: "first body"},
		{Role: vault.RoleTool, Collapsed: true, ToolSummary: "second", Body: "second body"},
	}, 80, 20)
	v = v.focusMarker(1)
	v = searchViewer(t, v, "missing")
	v = findKey(t, v, "]")
	v, restore, _ := v.Update(keyMsg("esc"))
	require.NotNil(t, restore)
	v, _, _ = v.Update(keyMsg("]"))
	require.Equal(t, 0, v.focusedMarker)
	v = settleViewerFind(t, v, restore)
	assert.Contains(t, ansi.Strip(v.vp.View()), "▶ first")
	assert.NotContains(t, ansi.Strip(v.vp.View()), "▶ second")
	v, _, _ = v.Update(keyMsg("enter"))
	assert.Contains(t, v.View(), "first body")
}

func TestViewerFindReopenDuringRestore(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			v := findTestViewer(t, []vault.TranscriptMessage{{Role: vault.RoleAssistant, Body: strings.Repeat("markdown **hit** paragraph\n", 40)}}, 60, 10)
			v = findKey(t, v, "/")
			v = findKey(t, v, "hit")
			if committed {
				v = findKey(t, v, "enter")
			}
			v = v.setSize(40, 10)
			v, cmd := v.nextFindCommand()
			v = settleViewerFind(t, v, cmd)
			v, restore, _ := v.Update(keyMsg("esc"))
			require.NotNil(t, restore)
			v, _, _ = v.Update(keyMsg("/"))
			require.True(t, v.find.editing)
			v, _, _ = v.Update(keyMsg("esc"))
			v = settleViewerFind(t, v, restore)
			assert.False(t, v.find.view.plain)
			assert.Empty(t, v.find.view.query)
			expected := renderTranscript(v.activePlatform(), v.active.messages, v.styles, v.contentWidth())
			assert.Equal(t, expected.rows, v.active.rows, "restore must prepare the actual current width")
		})
	}
}

func TestViewerFindInputLimitAndPaste(t *testing.T) {
	v := findTestViewer(t, []vault.TranscriptMessage{{Body: strings.Repeat("界", 300)}}, 80, 12)
	v = findKey(t, v, "/")
	v, cmd, _ := v.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	assert.Nil(t, cmd, "Ctrl+V must not start an external clipboard process")
	assert.Empty(t, v.find.input.Value())
	v, cmd, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("界", 257)), Paste: true})
	v = settleViewerFind(t, v, cmd)
	assert.Equal(t, 256, utf8.RuneCountInString(v.find.input.Value()))
	assert.Len(t, v.find.view.hits, 45, "terminal paste retains literal overlapping Unicode matches")
}
