package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func appFindKey(t *testing.T, m Model, key string) Model {
	t.Helper()
	next, cmd := m.Update(keyMsg(key))
	return settleAppFind(t, next.(Model), cmd)
}

func appFindResize(t *testing.T, m Model, width, height int) Model {
	t.Helper()
	next, cmd := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return settleAppFind(t, next.(Model), cmd)
}

func selectedFindPosition(t *testing.T, v viewerModel) findPosition {
	t.Helper()
	require.GreaterOrEqual(t, v.find.view.selected, 0)
	require.Less(t, v.find.view.selected, len(v.find.view.hits))
	return v.find.view.corpus.position(v.find.view.hits[v.find.view.selected])
}

func findFamilyApp(t *testing.T) (Model, *stubStore) {
	t.Helper()
	parent := codexParentSession(t, codexParentID, codexChildID, 30)
	child := codexParentSession(t, codexChildID, codexGrandchildID, 25)
	child.ParentUUID = codexParentID
	grandchild := codexChildSession(t, codexGrandchildID, codexChildID)
	m, st := codexFamilyApp(t, parent, child, grandchild)
	m = appFindKey(t, m, "enter")
	m.viewer = searchViewer(t, m.viewer, "parent body line")
	return appFindKey(t, m, "n"), st
}

func TestAppFindChildScopes(t *testing.T) {
	t.Run("two levels restore independent search and reading positions", func(t *testing.T) {
		m, st := findFamilyApp(t)
		var saved []viewerModel
		for _, child := range []string{codexChildID, codexGrandchildID} {
			m = appFindKey(t, m, "]")
			saved = append(saved, m.viewer)
			m = appFindKey(t, m, "enter")
			require.Equal(t, child, m.viewer.sess.UUID)
			assert.Empty(t, m.viewer.find.view.query)
			m.viewer = searchViewer(t, m.viewer, "commit")
		}
		m = appFindResize(t, m, 63, 10)
		for i := len(saved) - 1; i >= 0; i-- {
			epoch := m.viewer.find.epoch
			m = appFindKey(t, m, "q") // q leaves the child's query in one action
			want := saved[i]
			assert.Equal(t, want.sess.UUID, m.viewer.sess.UUID)
			assert.Equal(t, want.find.view.query, m.viewer.find.view.query)
			assert.Equal(t, selectedFindPosition(t, want), selectedFindPosition(t, m.viewer))
			assert.Equal(t, want.findReadingAnchor(), m.viewer.findReadingAnchor())
			assert.Equal(t, want.focusedMarker, m.viewer.focusedMarker)
			assert.Greater(t, m.viewer.find.epoch, epoch)
			assert.Len(t, m.viewerStack, i)
		}
		assert.Zero(t, cap(m.viewerStack), "popped frames must not retain child corpora")
		assert.Zero(t, st.searchCalls)
		assert.Zero(t, st.renameCalls)
		assert.Zero(t, st.projectCalls)
	})

	t.Run("missing and failed children retain live work", func(t *testing.T) {
		for _, failure := range []string{"missing", "session", "files"} {
			t.Run(failure, func(t *testing.T) {
				m, st := findFamilyApp(t)
				m = appFindKey(t, m, "]")
				next, pending := m.Update(keyMsg("n"))
				m = next.(Model)
				epoch := m.viewer.find.epoch
				selected := m.viewer.find.view.selected
				switch failure {
				case "missing":
					st.sessions = st.sessions[:1]
				case "session":
					st.getErr = errors.New("session unavailable")
				case "files":
					st.filesErr = errors.New("files unavailable")
				}
				m = appFindKey(t, m, "enter")
				assert.Equal(t, codexParentID, m.viewer.sess.UUID)
				assert.Equal(t, epoch, m.viewer.find.epoch)
				assert.Empty(t, m.viewerStack)
				assert.NotEmpty(t, m.status)
				assert.Equal(t, failure != "missing", m.statusErr)
				m = settleAppFind(t, m, pending)
				assert.Equal(t, selected+1, m.viewer.find.view.selected)
				assert.Equal(t, findWorkID{}, m.viewer.find.running)
			})
		}
	})

	t.Run("late parent and child results cannot apply or block resume", func(t *testing.T) {
		for _, completeBefore := range []bool{false, true} {
			t.Run(fmt.Sprintf("already completed=%v", completeBefore), func(t *testing.T) {
				m, _ := findFamilyApp(t)
				m = appFindKey(t, m, "]")
				before := m.viewer
				next, parentCmd := m.Update(keyMsg("n"))
				m = next.(Model)
				var parentResult tea.Msg
				if completeBefore {
					parentResult = parentCmd()
				}
				m = appFindKey(t, m, "enter")
				require.Equal(t, codexChildID, m.viewer.sess.UUID)
				suspended := m.viewerStack[0].viewer.find
				assert.Nil(t, suspended.cancel)
				assert.Nil(t, suspended.pending)
				assert.Zero(t, suspended.running)
				if !completeBefore {
					parentResult = parentCmd()
					require.ErrorIs(t, parentResult.(findResultMsg).err, context.Canceled)
				}
				m.viewer = searchViewer(t, m.viewer, "body")
				next, childCmd := m.Update(keyMsg("n"))
				m = next.(Model)
				childResult := childCmd()
				next, _ = m.Update(parentResult)
				m = next.(Model)
				assert.Equal(t, "body", m.viewer.find.view.query)
				assert.NotZero(t, m.viewer.find.running)
				childEpoch := m.viewer.find.epoch
				m = appFindKey(t, m, "q")
				assert.Greater(t, m.viewer.find.epoch, childEpoch)
				assert.Equal(t, before.vp.YOffset, m.viewer.vp.YOffset)
				assert.Equal(t, before.find.view.selected, m.viewer.find.view.selected)
				next, current := m.Update(keyMsg("n"))
				m = next.(Model)
				for _, stale := range []tea.Msg{childResult, parentResult} {
					next, cmd := m.Update(stale)
					m = next.(Model)
					assert.Nil(t, cmd)
					assert.NotZero(t, m.viewer.find.running)
				}
				m = settleAppFind(t, m, current)
				assert.Equal(t, before.find.view.selected+1, m.viewer.find.view.selected)
				assert.Zero(t, m.viewer.find.running)
			})
		}
	})
}

func TestAppFindRawReturn(t *testing.T) {
	for _, scope := range []string{"main", "sidecar", "manual tool", "selected tool", "child"} {
		t.Run(scope, func(t *testing.T) {
			m, st := newTestApp(t, Options{Mode: "view", SessionID: "abcdef01"})
			query := "answer"
			if scope == "child" {
				m, st = findFamilyApp(t)
				m = appFindKey(t, appFindKey(t, m, "]"), "enter")
				query = "body"
			} else {
				m.viewer = nestedTargetViewer(t).setSize(m.width, m.bodyHeight())
				if scope != "main" {
					m.viewer = m.viewer.openSubagent("xyz", 0)
					query = "needle"
				}
				if scope == "manual tool" {
					m.viewer = m.viewer.focusMarker(1)
					m.viewer, _ = m.viewer.openFocusedMarker()
				}
				if scope == "selected tool" {
					query = "body needle"
				}
			}
			m.viewer = searchViewer(t, m.viewer, query)
			m = appFindKey(t, m, "n")
			before := m.viewer
			position := selectedFindPosition(t, before)
			original := bytes.Clone(m.viewer.sess.RawJSONL)
			depth := len(m.viewerStack)
			for _, resize := range []string{"none", "height", "width"} {
				m = openRaw(t, m)
				assert.Zero(t, m.viewer.find.running)
				assert.Nil(t, m.viewer.find.cancel)
				if before.target.source.subagent != "" {
					assert.Contains(t, m.raw.title, "xyz")
					m.raw.vp.Width, m.raw.vp.Height = 2000, m.raw.vp.TotalLineCount()
					assert.Contains(t, m.raw.vp.View(), "body needle needle needle")
					assert.NotContains(t, m.raw.vp.View(), "first question")
				} else {
					assert.Contains(t, m.raw.title, before.sess.UUID)
				}
				if resize == "height" {
					m = appFindResize(t, m, m.width, 8)
				}
				if resize == "width" {
					m = appFindResize(t, m, 57, 11)
				}
				m = appFindKey(t, m, "esc")
				assert.Equal(t, modeView, m.mode)
				assert.Equal(t, before.target, m.viewer.target)
				assert.Equal(t, query, m.viewer.find.view.query)
				assert.Equal(t, position, selectedFindPosition(t, m.viewer))
				assert.Greater(t, m.viewer.find.epoch, before.find.epoch)
				assert.Len(t, m.viewer.parents, len(before.parents))
				assert.Len(t, m.viewerStack, depth)
				assert.Zero(t, m.viewer.find.running)
				assert.NotContains(t, m.viewer.findCounter(), "finding")
				if resize == "none" {
					assert.Equal(t, before.vp.YOffset, m.viewer.vp.YOffset)
				}
				requireFindLanding(t, m.viewer, position)
			}
			assert.Equal(t, original, m.viewer.sess.RawJSONL)
			assert.Zero(t, st.searchCalls)
			assert.Zero(t, st.renameCalls)
		})
	}
}

func TestAppFindSuspensionPending(t *testing.T) {
	for _, destination := range []string{"raw", "child"} {
		for _, operation := range []string{"navigation", "resize", "clear"} {
			t.Run(destination+"/"+operation, func(t *testing.T) {
				m, _ := findFamilyApp(t)
				m = appFindKey(t, m, "]")
				before := m.viewer
				var pending tea.Cmd
				var next tea.Model
				switch operation {
				case "navigation":
					next, pending = m.Update(keyMsg("n"))
				case "resize":
					next, pending = m.Update(tea.WindowSizeMsg{Width: 60, Height: 12})
				case "clear":
					m = appFindResize(t, m, 60, 12)
					next, pending = m.Update(keyMsg("esc"))
				}
				m = next.(Model)
				require.NotNil(t, pending)
				stale := pending() // successful completion held across suspension
				if destination == "raw" {
					m = openRaw(t, m)
				} else {
					m = appFindKey(t, m, "enter")
					require.Equal(t, codexChildID, m.viewer.sess.UUID)
				}
				next, _ = m.Update(stale)
				m = next.(Model)
				m = appFindResize(t, m, 53, 10)
				m = appFindKey(t, m, "q")
				next, cmd := m.Update(stale)
				m = settleAppFind(t, next.(Model), cmd)
				assert.Zero(t, m.viewer.find.running)
				assert.Nil(t, m.viewer.find.pending)
				if operation == "clear" {
					assert.Empty(t, m.viewer.find.view.query)
					assert.False(t, m.viewer.find.view.plain)
				} else {
					assert.Equal(t, before.find.view.query, m.viewer.find.view.query)
					assert.Equal(t, selectedFindPosition(t, before), selectedFindPosition(t, m.viewer))
					assert.Equal(t, 52, m.viewer.find.view.projection.width)
				}
				m.viewer = searchViewer(t, m.viewer, "commit")
				assert.Len(t, m.viewer.find.view.hits, 1, "controller remains usable after stale completion")
			})
		}
	}
}

func TestAppFindRawReturnReadingAndCancellation(t *testing.T) {
	m, _ := findFamilyApp(t)
	m = appFindKey(t, m, "]") // read the launch marker, away from selection
	before := m.viewer
	next, pending := m.Update(keyMsg("n"))
	m = next.(Model)
	next, rawCmd := m.Update(keyMsg("v"))
	m = next.(Model)
	require.Equal(t, modeRaw, m.mode)
	cancelled := pending()
	require.ErrorIs(t, cancelled.(findResultMsg).err, context.Canceled)
	next, cmd := m.Update(cancelled)
	m = next.(Model)
	assert.Nil(t, cmd, "a suspended controller cannot restart work")
	m = appFindResize(t, m, 67, 9)
	m = appFindKey(t, m, "q") // leave before raw formatting completes
	assert.Equal(t, before.findReadingAnchor(), m.viewer.findReadingAnchor())
	assert.Equal(t, selectedFindPosition(t, before), selectedFindPosition(t, m.viewer))
	assert.NotContains(t, m.viewer.findCounter(), "finding")
	lateRaw := rawCmd()
	require.ErrorIs(t, lateRaw.(rawLoadedMsg).err, context.Canceled)
	next, cmd = m.Update(lateRaw)
	m = settleAppFind(t, next.(Model), cmd)
	assert.Equal(t, modeView, m.mode)
	assert.Empty(t, m.raw.title)
	m = appFindKey(t, m, "n")
	requireFindLanding(t, m.viewer, selectedFindPosition(t, m.viewer))
}

func TestAppFindRawReturnPendingResizeKeepsVisibleSelection(t *testing.T) {
	m, _ := newTestApp(t, Options{Mode: "view", SessionID: "abcdef01"})
	m = appFindResize(t, m, 100, 10)
	m.viewer = findTestViewer(t, []vault.TranscriptMessage{{
		Role: vault.RoleAssistant, Body: strings.Repeat("context ", 30) + "needle",
	}}, 100, 10)
	m.viewer = searchViewer(t, m.viewer, "needle")
	position := selectedFindPosition(t, m.viewer)
	requireFindLanding(t, m.viewer, position)
	next, pending := m.Update(tea.WindowSizeMsg{Width: 25, Height: 10})
	m = next.(Model)
	require.NotNil(t, pending)
	stale := pending()
	m = openRaw(t, m)
	m = appFindKey(t, m, "q") // root dimensions already match the pending layout
	requireFindLanding(t, m.viewer, position)
	next, cmd := m.Update(stale)
	m = settleAppFind(t, next.(Model), cmd)
	requireFindLanding(t, m.viewer, position)
}

func TestAppFindActions(t *testing.T) {
	t.Run("copy and metadata preserve original body and search", func(t *testing.T) {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("rename failure=%v", fail), func(t *testing.T) {
				m, st := newTestApp(t, Options{Mode: "view", SessionID: "abcdef01"})
				m.viewer = nestedTargetViewer(t).setSize(m.width, m.bodyHeight()).openSubagent("xyz", 0)
				m.viewer = searchViewer(t, m.viewer, "body needle")
				before := m.viewer
				position := selectedFindPosition(t, before)
				msg, ok := before.currentMessage()
				require.True(t, ok)
				var clipboard bytes.Buffer
				m.clipOut = &clipboard
				m = appFindKey(t, m, "c")
				assert.Equal(t, osc52Sequence(msg.Body), clipboard.String())
				assert.Equal(t, before.vp.YOffset, m.viewer.vp.YOffset)
				assert.Same(t, before.find.view.projection, m.viewer.find.view.projection)
				m = appFindKey(t, m, "e")
				assert.True(t, m.renaming)
				assert.Equal(t, before.sess.UUID, m.renameUUID)
				if fail {
					st.renameErr = errors.New("write unavailable")
				}
				m, cmd := submitRename(t, m, "updated title")
				result := renameResult(t, cmd)
				if result.sess != nil {
					result.sess.RawJSONL = nil // metadata-only store response
				}
				next, cmd := m.Update(result)
				m = settleAppFind(t, next.(Model), cmd)
				assert.Equal(t, fail, m.renaming)
				if fail {
					assert.Contains(t, m.status, "write unavailable")
					m = appFindKey(t, m, "esc")
				} else {
					assert.Equal(t, "updated title", m.viewer.sess.EffectiveTitle())
				}
				assert.Same(t, before.find.view.corpus, m.viewer.find.view.corpus)
				assert.Same(t, before.find.view.projection, m.viewer.find.view.projection)
				assert.Equal(t, before.find.epoch, m.viewer.find.epoch)
				assert.Equal(t, before.target, m.viewer.target)
				assert.Equal(t, before.sess.RawJSONL, m.viewer.sess.RawJSONL)
				requireFindLanding(t, m.viewer, position)
				m = appFindResize(t, m, 55, 9)
				requireFindLanding(t, m.viewer, position)
				assert.Zero(t, st.searchCalls)
			})
		}
	})

	t.Run("clear and immediate back never revive the owner query", func(t *testing.T) {
		for _, key := range []string{"esc", "q"} {
			t.Run(key, func(t *testing.T) {
				m, _ := newTestApp(t, Options{Mode: "view", SessionID: "abcdef01"})
				m.viewer = nestedTargetViewer(t).setSize(m.width, m.bodyHeight())
				m.viewer = searchViewer(t, m.viewer, "answer")
				m = appFindKey(t, appFindKey(t, m, "]"), "enter")
				m.viewer = searchViewer(t, m.viewer, "body needle")
				require.True(t, m.viewer.target.searchSelected)
				m = appFindKey(t, m, key)
				assert.Empty(t, m.viewer.find.view.query)
				if key == "esc" {
					assert.Equal(t, viewerTargetTool, m.viewer.target.kind)
					m = appFindKey(t, m, "esc")
				}
				assert.Equal(t, viewerTargetSidecar, m.viewer.target.kind)
				assert.Empty(t, m.viewer.find.view.query)
				m = appFindKey(t, m, "q")
				assert.Equal(t, "answer", m.viewer.find.view.query)
				m = appFindKey(t, m, "q")
				assert.Equal(t, modeList, m.mode)
				assert.False(t, m.viewer.ready)
				assert.Nil(t, m.viewer.find.view.corpus)
				assert.Empty(t, m.viewer.parents)
			})
		}
	})

	t.Run("restore and resume target child and cancel outstanding work", func(t *testing.T) {
		for _, key := range []string{"r", "R", "ctrl+c"} {
			t.Run(key, func(t *testing.T) {
				m, _ := findFamilyApp(t)
				m = appFindKey(t, appFindKey(t, m, "]"), "enter")
				m.viewer = searchViewer(t, m.viewer, "body")
				next, pending := m.Update(keyMsg("n"))
				m = next.(Model)
				next, quit := m.Update(keyMsg(key))
				m = next.(Model)
				assert.True(t, m.quitting)
				require.NotNil(t, quit)
				assert.IsType(t, tea.QuitMsg{}, quit())
				if key != "ctrl+c" {
					assert.Equal(t, actionFor(key), m.action.Kind)
					assert.Equal(t, codexChildID, m.action.SessionUUID)
				}
				require.ErrorIs(t, pending().(findResultMsg).err, context.Canceled)
			})
		}
	})
}

func TestAppFindFrameLifetime(t *testing.T) {
	m, st := findFamilyApp(t)
	for range 20 {
		m = appFindKey(t, appFindKey(t, m, "]"), "enter")
		m.viewer = searchViewer(t, m.viewer, "commit")
		m = openRaw(t, m)
		m = appFindKey(t, m, "q")
		assert.Contains(t, ansi.Strip(m.View()), "[commit]")
		m = appFindKey(t, m, "q")
		assert.Equal(t, "parent body line", m.viewer.find.view.query)
		assert.Empty(t, m.viewerStack)
		assert.Zero(t, cap(m.viewerStack))
		assert.Empty(t, m.viewer.parents)
	}
	// Entering from global search clears all local state and still returns there.
	var err error
	m, err = m.openSession(codexChildID, modeSearch, "", 1)
	require.NoError(t, err)
	assert.Empty(t, m.viewer.find.view.query)
	assert.Nil(t, m.viewer.find.view.corpus)
	m = appFindKey(t, m, "q")
	assert.Equal(t, modeSearch, m.mode)
	assert.Zero(t, st.searchCalls)
	assert.Zero(t, st.renameCalls)
}
