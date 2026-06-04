package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/serpro69/capy/internal/vault"
)

// newTestApp builds an app over a stubStore holding the sample session.
func newTestApp(t *testing.T, opts Options) (Model, *stubStore) {
	t.Helper()
	sess, files := sampleSession(t)
	st := &stubStore{
		sessions: []vault.Session{sess},
		files:    map[string][]vault.File{sess.UUID: files},
	}
	m, err := newModel(st, opts)
	require.NoError(t, err)
	// Give the models a real size, as the first WindowSizeMsg would.
	tm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return tm.(Model), st
}

func TestApp_DefaultStartsInList(t *testing.T) {
	m, _ := newTestApp(t, Options{})
	assert.Equal(t, modeList, m.mode)
}

func TestApp_ListEnterOpensView(t *testing.T) {
	m, _ := newTestApp(t, Options{})
	next, _ := m.Update(key("enter"))
	m = next.(Model)
	assert.Equal(t, modeView, m.mode)
	assert.True(t, m.viewer.ready)
	assert.Equal(t, modeList, m.prevMode)
}

func TestApp_SlashOpensSearch(t *testing.T) {
	m, _ := newTestApp(t, Options{})
	next, _ := m.Update(key("/"))
	m = next.(Model)
	assert.Equal(t, modeSearch, m.mode)
}

func TestApp_ViewBackReturnsToPrevMode(t *testing.T) {
	m, _ := newTestApp(t, Options{})
	// list → view → back to list
	next, _ := m.Update(key("enter"))
	m = next.(Model)
	require.Equal(t, modeView, m.mode)
	next, _ = m.Update(key("q"))
	m = next.(Model)
	assert.Equal(t, modeList, m.mode)
}

func TestApp_SearchEnterOpensViewAndJumps(t *testing.T) {
	sess, files := sampleSession(t)
	st := &stubStore{
		sessions: []vault.Session{sess},
		files:    map[string][]vault.File{sess.UUID: files},
		results: []vault.SearchResult{
			{SessionUUID: sess.UUID, SubagentID: "xyz", LineIndex: 1, Role: "assistant", Snippet: "sub [hit]"},
		},
	}
	m, err := newModel(st, Options{Mode: "search", Query: "bug"})
	require.NoError(t, err)
	tm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = tm.(Model)
	require.Equal(t, modeSearch, m.mode)

	// Feed results as the debounced query would, then open the selected hit.
	m.search, _ = m.search.Update(searchResultsMsg{seq: m.search.seq, results: st.results})
	next, _ := m.Update(key("enter"))
	m = next.(Model)

	assert.Equal(t, modeView, m.mode)
	assert.True(t, m.viewer.inSub, "a subagent hit opens the subagent transcript standalone")
	assert.Equal(t, "xyz", m.viewer.subID)
	assert.Equal(t, modeSearch, m.prevMode, "view returns to search when opened from search")
}

func TestApp_SearchEscReturnsToList(t *testing.T) {
	m, _ := newTestApp(t, Options{})
	next, _ := m.Update(key("/"))
	m = next.(Model)
	require.Equal(t, modeSearch, m.mode)
	next, _ = m.Update(key("esc"))
	m = next.(Model)
	assert.Equal(t, modeList, m.mode)
}

func TestApp_ViewModeStartLoadsSession(t *testing.T) {
	sess, _ := sampleSession(t)
	m, _ := newTestApp(t, Options{Mode: "view", SessionID: sess.UUID})
	assert.Equal(t, modeView, m.mode)
	assert.True(t, m.viewer.ready)
}

func TestApp_CtrlCQuits(t *testing.T) {
	m, _ := newTestApp(t, Options{})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	require.NotNil(t, cmd)
	assert.Equal(t, tea.Quit(), cmd(), "ctrl+c issues the quit command")
}

func TestApp_QuitInListView(t *testing.T) {
	m, _ := newTestApp(t, Options{})
	m.quitting = false
	_, cmd := m.Update(key("q"))
	require.NotNil(t, cmd)
	assert.Equal(t, tea.Quit(), cmd())
}

func TestApp_ViewRendersEachMode(t *testing.T) {
	m, _ := newTestApp(t, Options{})

	// List mode.
	assert.Contains(t, m.View(), "Timeout investigation")

	// View mode.
	next, _ := m.Update(key("enter"))
	m = next.(Model)
	assert.Contains(t, m.View(), "answer one")

	// Back to list, then search mode.
	next, _ = m.Update(key("q"))
	m = next.(Model)
	next, _ = m.Update(key("/"))
	m = next.(Model)
	assert.Contains(t, m.View(), "search")

	// quitting renders empty.
	m.quitting = true
	assert.Empty(t, m.View())
}

func TestApp_StatusLineSurfacesError(t *testing.T) {
	m, _ := newTestApp(t, Options{})
	m.status = "something went wrong"
	assert.Contains(t, m.View(), "something went wrong")
}
