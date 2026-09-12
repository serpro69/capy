package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runCmds executes cmd, recursively flattening tea.Batch, and returns the
// non-nil messages produced — so tests can drive async command chains (rename
// write, post-rename search rerun) synchronously.
func runCmds(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, runCmds(c)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

// submitRename replaces the editor's text and submits it, returning the model
// with the write pending plus the rename command.
func submitRename(t *testing.T, m Model, value string) (Model, tea.Cmd) {
	t.Helper()
	require.True(t, m.renaming, "the rename editor must be open before submitting")
	m.renameInput.SetValue(value)
	next, cmd := m.Update(keyMsg("enter"))
	return next.(Model), cmd
}

// renameResult executes the rename command and returns its result message.
func renameResult(t *testing.T, cmd tea.Cmd) renameResultMsg {
	t.Helper()
	msgs := runCmds(cmd)
	require.Len(t, msgs, 1)
	res, ok := msgs[0].(renameResultMsg)
	require.True(t, ok, "the rename command must produce a renameResultMsg")
	return res
}

func TestApp_RenameFromListPrefillsEffectiveTitle(t *testing.T) {
	m, _ := twoProjectApp(t)
	next, _ := m.Update(keyMsg("e"))
	m = next.(Model)

	require.True(t, m.renaming)
	assert.Equal(t, "aaaa1111", m.renameUUID, "the selected session is the target")
	assert.Equal(t, "alpha work", m.renameInput.Value(), "prefilled with the effective title")
	assert.Contains(t, m.View(), "name (empty clears)", "the labeled editor is visible")
}

func TestApp_RenamePrefillsCustomName(t *testing.T) {
	m, st := twoProjectApp(t)
	custom := "My Custom Name"
	st.sessions[0].Name = &vault.SessionName{CustomTitle: &custom, RenamedAtNS: 1, MachineID: "stub"}
	// Refresh the list items so they carry the name state.
	next, _ := m.applySessionFilter("")
	m = next.(Model)

	next, _ = m.Update(keyMsg("e"))
	m = next.(Model)
	require.True(t, m.renaming)
	assert.Equal(t, "My Custom Name", m.renameInput.Value(),
		"an active custom name, not the imported title, is prefilled")
}

func TestApp_RenameNoopWhenListEmpty(t *testing.T) {
	st := &stubStore{sessions: nil, files: map[string][]vault.File{}}
	m, err := newModel(context.Background(), st, Options{})
	require.NoError(t, err)
	tm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = tm.(Model)

	next, _ := m.Update(keyMsg("e"))
	m = next.(Model)
	assert.False(t, m.renaming, "no selection → no editor")
}

func TestApp_RenameFromViewer(t *testing.T) {
	m, _ := newTestApp(t, Options{})
	next, _ := m.Update(keyMsg("enter")) // open the viewer
	m = next.(Model)
	require.Equal(t, modeView, m.mode)

	next, _ = m.Update(keyMsg("e"))
	m = next.(Model)
	require.True(t, m.renaming)
	assert.Equal(t, "abcdef0123456789", m.renameUUID)
	assert.Equal(t, "Timeout investigation", m.renameInput.Value())
	assert.Equal(t, modeView, m.mode, "the viewer stays underneath the editor")
}

func TestApp_RenameFromSearchUsesCtrlE(t *testing.T) {
	sess, files := sampleSession(t)
	st := &stubStore{
		sessions: []vault.Session{sess},
		files:    map[string][]vault.File{sess.UUID: files},
		results: []vault.SearchResult{
			{SessionUUID: sess.UUID, Role: "user", Snippet: "hit", Title: "Timeout investigation"},
		},
	}
	m, err := newModel(context.Background(), st, Options{Mode: "search", Query: "timeouts"})
	require.NoError(t, err)
	tm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = tm.(Model)
	m.search, _ = m.search.Update(searchResultsMsg{seq: m.search.seq, results: st.results})

	next, _ := m.Update(keyMsg("ctrl+e"))
	m = next.(Model)
	require.True(t, m.renaming)
	assert.Equal(t, sess.UUID, m.renameUUID)
	assert.Equal(t, "Timeout investigation", m.renameInput.Value(),
		"prefilled with the result's effective title")
}

func TestApp_SearchCtrlENoopWithoutResults(t *testing.T) {
	m, _ := newTestApp(t, Options{Mode: "search"})
	next, _ := m.Update(keyMsg("ctrl+e"))
	m = next.(Model)
	assert.False(t, m.renaming, "no selected result → no editor")
}

func TestApp_SearchTypingEEditsQuery(t *testing.T) {
	m, _ := newTestApp(t, Options{Mode: "search"})
	next, _ := m.Update(keyMsg("e"))
	m = next.(Model)
	assert.False(t, m.renaming, "bare e must stay typeable in the search query")
	assert.Equal(t, "e", m.search.input.Value())
}

func TestApp_ListFilterTypingEEditsFilter(t *testing.T) {
	m, _ := twoProjectApp(t)
	next, _ := m.Update(keyMsg("f"))
	m = next.(Model)
	require.True(t, m.list.filtering)

	next, _ = m.Update(keyMsg("e"))
	m = next.(Model)
	assert.False(t, m.renaming, "while the filter input is active, e edits the filter")
	assert.Equal(t, "e", m.list.filterValue())
}

func TestApp_RenameSubmitSuccessRefreshesList(t *testing.T) {
	m, st := twoProjectApp(t)
	next, _ := m.Update(keyMsg("e"))
	m = next.(Model)

	m, cmd := submitRename(t, m, "Alpha Renamed")
	require.True(t, m.renamePending)
	res := renameResult(t, cmd)
	require.NoError(t, res.err)

	next, _ = m.Update(res)
	m = next.(Model)
	assert.False(t, m.renaming, "success closes the editor")
	assert.False(t, m.renamePending)
	assert.False(t, m.statusErr)
	assert.Contains(t, m.status, `renamed aaaa1111 to "Alpha Renamed"`)

	assert.Equal(t, 1, st.renameCalls)
	assert.Equal(t, "aaaa1111", st.lastRenameID)
	assert.Equal(t, vault.RenameOptions{Name: "Alpha Renamed"}, st.lastRenameOpts)
	assert.Equal(t, 0, st.searchCalls, "no active search query → the refresh does not run a search")

	sel, ok := m.list.selected()
	require.True(t, ok)
	assert.Equal(t, "aaaa1111", sel.UUID, "selection is preserved across the refresh")
	assert.Equal(t, "Alpha Renamed", sel.EffectiveTitle(), "the list shows the authoritative new title")
	assert.Contains(t, m.View(), "Alpha Renamed")
}

func TestApp_RenameEmptySubmitClears(t *testing.T) {
	m, st := twoProjectApp(t)
	custom := "Old Custom"
	st.sessions[0].Name = &vault.SessionName{CustomTitle: &custom, RenamedAtNS: 1, MachineID: "stub"}

	next, _ := m.Update(keyMsg("e"))
	m = next.(Model)
	m, cmd := submitRename(t, m, "   ")
	res := renameResult(t, cmd)
	require.NoError(t, res.err)
	assert.True(t, res.cleared)
	assert.Equal(t, vault.RenameOptions{Clear: true}, st.lastRenameOpts,
		"an emptied input submits a clear, not a rename")

	next, _ = m.Update(res)
	m = next.(Model)
	assert.False(t, m.renaming)
	assert.Contains(t, m.status, `cleared custom name for aaaa1111 — title is now "alpha work"`,
		"clearing falls back to the imported title")
}

func TestApp_RenameEscCancels(t *testing.T) {
	m, st := twoProjectApp(t)
	next, _ := m.Update(keyMsg("e"))
	m = next.(Model)
	require.True(t, m.renaming)

	next, _ = m.Update(keyMsg("esc"))
	m = next.(Model)
	assert.False(t, m.renaming)
	assert.Equal(t, "", m.renameUUID)
	assert.Equal(t, 0, st.renameCalls, "cancel writes nothing")
	assert.Equal(t, modeList, m.mode)
}

func TestApp_RenameErrorKeepsEditorOpen(t *testing.T) {
	m, st := twoProjectApp(t)
	st.renameErr = errors.New("boom")

	next, _ := m.Update(keyMsg("e"))
	m = next.(Model)
	m, cmd := submitRename(t, m, "won't land")
	res := renameResult(t, cmd)
	require.Error(t, res.err)

	next, _ = m.Update(res)
	m = next.(Model)
	assert.True(t, m.renaming, "a failure leaves the editor open")
	assert.False(t, m.renamePending, "the failed write is no longer pending")
	assert.Equal(t, "won't land", m.renameInput.Value(), "the text is retained for correction")
	assert.True(t, m.statusErr)
	assert.Contains(t, m.status, "rename error: boom")
}

func TestApp_RenamePendingSuppressesInput(t *testing.T) {
	m, st := twoProjectApp(t)
	next, _ := m.Update(keyMsg("e"))
	m = next.(Model)
	m, firstCmd := submitRename(t, m, "pending name")
	require.True(t, m.renamePending)
	require.NotNil(t, firstCmd)

	// A duplicate submit is ignored while the write is in flight.
	next, cmd := m.Update(keyMsg("enter"))
	m = next.(Model)
	assert.Nil(t, cmd, "duplicate submissions are ignored while pending")

	// Esc cannot close the editor mid-flight, and typing does not edit the text.
	next, _ = m.Update(keyMsg("esc"))
	m = next.(Model)
	assert.True(t, m.renaming)
	next, _ = m.Update(keyMsg("z"))
	m = next.(Model)
	assert.Equal(t, "pending name", m.renameInput.Value())

	// Only the single original write reaches the store.
	renameResult(t, firstCmd)
	assert.Equal(t, 1, st.renameCalls)
}

func TestApp_RenameSuccessRerunsSearchQuery(t *testing.T) {
	sess, files := sampleSession(t)
	st := &stubStore{
		sessions: []vault.Session{sess},
		files:    map[string][]vault.File{sess.UUID: files},
		results: []vault.SearchResult{
			{SessionUUID: sess.UUID, Role: "user", Snippet: "hit", Title: "Timeout investigation"},
		},
	}
	m, err := newModel(context.Background(), st, Options{Mode: "search", Query: "timeouts"})
	require.NoError(t, err)
	tm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = tm.(Model)
	m.search, _ = m.search.Update(searchResultsMsg{seq: m.search.seq, results: st.results})

	next, _ := m.Update(keyMsg("ctrl+e"))
	m = next.(Model)
	m, cmd := submitRename(t, m, "Named From Search")
	res := renameResult(t, cmd)
	require.NoError(t, res.err)

	next, refreshCmd := m.Update(res)
	m = next.(Model)
	require.Equal(t, modeSearch, m.mode)

	// The store serves the renamed title on the rerun the refresh command fires.
	st.results = []vault.SearchResult{
		{SessionUUID: sess.UUID, Role: "user", Snippet: "hit", Title: "Named From Search"},
	}
	callsBefore := st.searchCalls
	msgs := runCmds(refreshCmd)
	assert.Equal(t, callsBefore+1, st.searchCalls, "the active query reruns after a rename")
	for _, msg := range msgs {
		tm, _ := m.Update(msg)
		m = tm.(Model)
	}
	require.Len(t, m.search.results, 1)
	assert.Equal(t, "Named From Search", m.search.results[0].Title)
	assert.Contains(t, m.View(), "Named From Search", "search rows render the refreshed effective title")
}

func TestApp_RenameFilteredItemDisappears(t *testing.T) {
	st := &stubStore{
		sessions: []vault.Session{
			{UUID: "aaaa1111", Title: "projectless", ProjectPath: "/p/one"},
			{UUID: "bbbb2222", Title: "other", ProjectPath: "/p/two"},
		},
		files: map[string][]vault.File{},
	}
	m, err := newModel(context.Background(), st, Options{})
	require.NoError(t, err)
	tm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = tm.(Model)

	// Apply a filter matching only the first session's title, then apply it.
	next, _ := m.Update(keyMsg("f"))
	m = next.(Model)
	m = typeRunes(t, m, "projectless")
	require.Len(t, m.list.list.Items(), 1)
	next, _ = m.Update(keyMsg("enter"))
	m = next.(Model)

	// Rename it so it no longer matches the still-active filter.
	next, _ = m.Update(keyMsg("e"))
	m = next.(Model)
	m, cmd := submitRename(t, m, "gone")
	res := renameResult(t, cmd)
	require.NoError(t, res.err)
	next, _ = m.Update(res)
	m = next.(Model)

	assert.Equal(t, "projectless", m.list.applied, "the active filter is preserved")
	assert.Empty(t, m.list.list.Items(), "the renamed item legitimately disappears from a non-matching filter")
	_, ok := m.list.selected()
	assert.False(t, ok)
}

func TestApp_RenameFromViewerRefreshesHeader(t *testing.T) {
	m, _ := newTestApp(t, Options{})
	next, _ := m.Update(keyMsg("enter"))
	m = next.(Model)
	require.Equal(t, modeView, m.mode)

	next, _ = m.Update(keyMsg("e"))
	m = next.(Model)
	m, cmd := submitRename(t, m, "Investigated!")
	res := renameResult(t, cmd)
	require.NoError(t, res.err)

	next, _ = m.Update(res)
	m = next.(Model)
	assert.Equal(t, "Investigated!", m.viewer.sess.EffectiveTitle(),
		"the viewer's session metadata is refreshed")
	out := m.View()
	assert.Contains(t, out, "Investigated!", "the header shows the new effective title")
	assert.Contains(t, out, "first question", "the parsed transcript is untouched by the metadata refresh")

	// The list underneath is refreshed too, so returning to it is not stale.
	sel, ok := m.list.selected()
	require.True(t, ok)
	assert.Equal(t, "Investigated!", sel.EffectiveTitle())
}

func TestApp_RenameHelpAdvertised(t *testing.T) {
	// List help (wide window so bubbles' short help does not truncate the keys).
	m, st := twoProjectApp(t)
	tm, _ := m.Update(tea.WindowSizeMsg{Width: 220, Height: 40})
	m = tm.(Model)
	assert.Contains(t, m.View(), "rename", "list help advertises e rename")

	// Viewer help.
	next, _ := m.Update(keyMsg("enter"))
	m = next.(Model)
	require.Equal(t, modeView, m.mode)
	assert.Contains(t, m.View(), "e rename")

	// Search help (needs results for the results-footer help line).
	next, _ = m.Update(keyMsg("q"))
	m = next.(Model)
	next, _ = m.Update(keyMsg("/"))
	m = next.(Model)
	st.results = sampleResults()
	m.search, _ = m.search.Update(searchResultsMsg{seq: m.search.seq, results: st.results})
	assert.Contains(t, m.View(), "ctrl+e rename")
}

func TestApp_RenameValidationErrorKeepsEditorOpen(t *testing.T) {
	m, st := twoProjectApp(t)
	next, _ := m.Update(keyMsg("e"))
	m = next.(Model)

	// The stub applies the shared NormalizeSessionName, so an over-long name is
	// rejected exactly as the real store rejects it — and the TUI surfaces that
	// validation error the same way as a DB failure.
	tooLong := strings.Repeat("x", 121)
	m, cmd := submitRename(t, m, tooLong)
	res := renameResult(t, cmd)
	require.Error(t, res.err)

	next, _ = m.Update(res)
	m = next.(Model)
	assert.True(t, m.renaming, "a validation failure leaves the editor open")
	assert.Equal(t, tooLong, m.renameInput.Value(), "the rejected text is retained for correction")
	assert.True(t, m.statusErr)
	assert.Contains(t, m.status, "must not exceed 120 characters")
	assert.Equal(t, 1, st.renameCalls)
}

func TestApp_RenameRefreshFailureSurfacesError(t *testing.T) {
	m, st := twoProjectApp(t)
	next, _ := m.Update(keyMsg("e"))
	m = next.(Model)
	m, cmd := submitRename(t, m, "Landed")
	res := renameResult(t, cmd)
	require.NoError(t, res.err)

	// The write landed, but the authoritative re-read fails: the editor closes
	// (there is nothing left to retry) and the status says exactly what happened.
	st.listErr = errors.New("db gone")
	next, _ = m.Update(res)
	m = next.(Model)
	assert.False(t, m.renaming, "the write succeeded, so the editor closes")
	assert.False(t, m.renamePending)
	assert.True(t, m.statusErr)
	assert.Contains(t, m.status, "rename succeeded, but refreshing sessions failed: db gone")
	assert.Equal(t, 1, st.renameCalls)
}

func TestApp_RenameRefreshFailureStillUpdatesViewer(t *testing.T) {
	m, st := newTestApp(t, Options{})
	next, _ := m.Update(keyMsg("enter"))
	m = next.(Model)
	require.Equal(t, modeView, m.mode)

	next, _ = m.Update(keyMsg("e"))
	m = next.(Model)
	m, cmd := submitRename(t, m, "Committed")
	res := renameResult(t, cmd)
	require.NoError(t, res.err)

	// The viewer needs only the returned session, so the committed rename must
	// show in its header even though the list re-read fails.
	st.listErr = errors.New("db gone")
	next, _ = m.Update(res)
	m = next.(Model)
	assert.False(t, m.renaming)
	assert.Equal(t, "Committed", m.viewer.sess.EffectiveTitle())
	assert.Contains(t, m.View(), "Committed", "the header reflects the committed write")
	assert.True(t, m.statusErr)
	assert.Contains(t, m.status, "refreshing sessions failed")
}

func TestApp_RenameNilSessionResultIsAnError(t *testing.T) {
	m, _ := twoProjectApp(t)
	next, _ := m.Update(keyMsg("e"))
	m = next.(Model)
	m, _ = submitRename(t, m, "whatever")
	require.True(t, m.renamePending)

	// A store returning (nil, nil) violates its contract; the model must fail
	// loud in the status line instead of panicking inside Update.
	next, cmd := m.Update(renameResultMsg{sess: nil, err: nil})
	m = next.(Model)
	assert.Nil(t, cmd)
	assert.True(t, m.renaming, "the editor stays open so the user can retry or cancel")
	assert.False(t, m.renamePending)
	assert.True(t, m.statusErr)
	assert.Contains(t, m.status, "store returned no session")
}

func TestApp_RenameTypingCEditsNameAndClearsStatus(t *testing.T) {
	m, _ := twoProjectApp(t)
	next, _ := m.Update(keyMsg("e"))
	m = next.(Model)
	m = m.withStatus("stale status")

	// In normal routing "c" is the copy key and exempt from status clearing;
	// while renaming it is an ordinary character.
	next, _ = m.Update(keyMsg("c"))
	m = next.(Model)
	assert.True(t, m.renaming)
	assert.Equal(t, "alpha workc", m.renameInput.Value(), "c types into the editor")
	assert.Equal(t, "", m.status, "the stale status clears like on any other keystroke")
}

func TestApp_RenameCtrlCQuits(t *testing.T) {
	m, st := twoProjectApp(t)
	next, _ := m.Update(keyMsg("e"))
	m = next.(Model)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	require.NotNil(t, cmd)
	assert.Equal(t, tea.Quit(), cmd(), "ctrl+c quits while editing a name")

	// While a write is pending every other key is swallowed; ctrl+c is not.
	m, writeCmd := submitRename(t, m, "pending")
	require.True(t, m.renamePending)
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	require.NotNil(t, cmd)
	assert.Equal(t, tea.Quit(), cmd(), "ctrl+c quits while a rename is pending")
	renameResult(t, writeCmd)
	assert.Equal(t, 1, st.renameCalls)
}

func TestApp_RenameFromViewerOpenedFromSearchRefreshesSearch(t *testing.T) {
	sess, files := sampleSession(t)
	st := &stubStore{
		sessions: []vault.Session{sess},
		files:    map[string][]vault.File{sess.UUID: files},
		results: []vault.SearchResult{
			{SessionUUID: sess.UUID, Role: "user", Snippet: "hit", Title: "Timeout investigation"},
		},
	}
	m, err := newModel(context.Background(), st, Options{Mode: "search", Query: "timeouts"})
	require.NoError(t, err)
	tm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = tm.(Model)
	m.search, _ = m.search.Update(searchResultsMsg{seq: m.search.seq, results: st.results})

	// Open the hit, then rename from the viewer.
	next, _ := m.Update(keyMsg("enter"))
	m = next.(Model)
	require.Equal(t, modeView, m.mode)
	require.Equal(t, modeSearch, m.prevMode)

	next, _ = m.Update(keyMsg("e"))
	m = next.(Model)
	m, cmd := submitRename(t, m, "Renamed In Viewer")
	res := renameResult(t, cmd)
	require.NoError(t, res.err)

	st.results = []vault.SearchResult{
		{SessionUUID: sess.UUID, Role: "user", Snippet: "hit", Title: "Renamed In Viewer"},
	}
	next, refreshCmd := m.Update(res)
	m = next.(Model)
	require.Equal(t, modeView, m.mode, "the viewer stays open after the rename")

	// The rerun's results arrive while the viewer is still the active mode; they
	// must reach the search model rather than be dropped by mode routing.
	for _, msg := range runCmds(refreshCmd) {
		tm, _ := m.Update(msg)
		m = tm.(Model)
	}
	require.Len(t, m.search.results, 1)
	assert.Equal(t, "Renamed In Viewer", m.search.results[0].Title)

	// Returning to search shows the refreshed title without another query.
	calls := st.searchCalls
	next, _ = m.Update(keyMsg("q"))
	m = next.(Model)
	require.Equal(t, modeSearch, m.mode)
	assert.Equal(t, calls, st.searchCalls)
	assert.Contains(t, m.View(), "Renamed In Viewer")
}

func TestApp_RenameFromSubagentDetailTargetsOwningSession(t *testing.T) {
	m, _ := newTestApp(t, Options{})
	next, _ := m.Update(keyMsg("enter"))
	m = next.(Model)
	require.Equal(t, modeView, m.mode)

	// Focus the subagent marker and open its detail transcript.
	next, _ = m.Update(keyMsg("]"))
	m = next.(Model)
	next, _ = m.Update(keyMsg("enter"))
	m = next.(Model)
	require.True(t, m.viewer.inDetail(), "the subagent detail is open")

	next, _ = m.Update(keyMsg("e"))
	m = next.(Model)
	require.True(t, m.renaming)
	assert.Equal(t, "abcdef0123456789", m.renameUUID, "a detail still belongs to the archived session")
	assert.Equal(t, "Timeout investigation", m.renameInput.Value())

	m, cmd := submitRename(t, m, "From Detail")
	res := renameResult(t, cmd)
	require.NoError(t, res.err)
	next, _ = m.Update(res)
	m = next.(Model)
	assert.True(t, m.viewer.inDetail(), "the metadata refresh does not leave the detail view")
	assert.Equal(t, "From Detail", m.viewer.sess.EffectiveTitle())
}

func TestApp_RenameLineFitsTerminalWidth(t *testing.T) {
	m, st := twoProjectApp(t)
	long := strings.Repeat("x", 120) // the store's maximum name length
	st.sessions[0].Title = long
	next, _ := m.applySessionFilter("")
	m = next.(Model)
	tm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	m = tm.(Model)

	next, _ = m.Update(keyMsg("e"))
	m = next.(Model)
	require.True(t, m.renaming)
	require.Equal(t, long, m.renameInput.Value(), "the full title is prefilled")
	assert.LessOrEqual(t, displayWidth(m.renameLine()), 80,
		"a name wider than the terminal scrolls under the cursor instead of running past the edge")

	// A resize while the editor is open re-bounds the value window too.
	tm, _ = m.Update(tea.WindowSizeMsg{Width: 60, Height: 40})
	m = tm.(Model)
	assert.LessOrEqual(t, displayWidth(m.renameLine()), 60)

	// Same with the cursor parked mid-value (inside the previous, wider window):
	// textinput would keep that stale window unless the layout re-bounds it.
	tm, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = tm.(Model)
	m.renameInput.SetCursor(70)
	tm, _ = m.Update(tea.WindowSizeMsg{Width: 60, Height: 40})
	m = tm.(Model)
	assert.Equal(t, 70, m.renameInput.Position(), "the cursor position survives the resize")
	assert.LessOrEqual(t, displayWidth(m.renameLine()), 60,
		"a mid-value cursor's window is re-bounded to the new width")

	// Absurdly narrow terminals clamp to a one-cell value area, never negative.
	tm, _ = m.Update(tea.WindowSizeMsg{Width: 10, Height: 40})
	m = tm.(Model)
	assert.Equal(t, 1, m.renameInput.Width)
}

func TestApp_RenameEditorKeepsViewWithinHeight(t *testing.T) {
	// A transcript taller than the viewport so the body alone fills the height.
	var lines []map[string]any
	for i := range 80 {
		lines = append(lines, assistantLine(fmt.Sprintf("m%d", i),
			[]map[string]any{textBlock(fmt.Sprintf("body line %d", i))}))
	}
	sess := vault.Session{UUID: "tall00000000", Title: "tall", RawJSONL: jsonlLines(t, lines...)}
	st := &stubStore{
		sessions:  []vault.Session{sess},
		files:     map[string][]vault.File{},
		renameErr: errors.New("nope"),
	}
	m, err := newModel(context.Background(), st, Options{Mode: "view", SessionID: "tall00000000"})
	require.NoError(t, err)
	tm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m = tm.(Model)
	require.Equal(t, modeView, m.mode)

	height := func() int { return len(strings.Split(m.View(), "\n")) }
	require.LessOrEqual(t, height(), 20, "body alone fits the height")

	// The editor row is reserved by shrinking the body (bodyHeight), not appended
	// on top — otherwise the alt-screen scrolls and flickers every frame.
	next, _ := m.Update(keyMsg("e"))
	m = next.(Model)
	require.True(t, m.renaming)
	assert.LessOrEqual(t, height(), 20, "the open editor must not push the View past the terminal height")

	// A failed write adds the error status row while the editor stays open.
	m, cmd := submitRename(t, m, "x")
	next, _ = m.Update(renameResult(t, cmd))
	m = next.(Model)
	require.True(t, m.renaming)
	require.NotEmpty(t, m.status)
	assert.LessOrEqual(t, height(), 20, "editor and status rows together must fit the terminal height")
}

func TestSearch_RowRendersEffectiveTitle(t *testing.T) {
	st := &stubStore{}
	m := newSearchModel(context.Background(), st, DefaultStyles(), 120, 24)
	m, _ = m.Update(searchResultsMsg{seq: m.seq, results: []vault.SearchResult{
		{SessionUUID: "aaaa1111", Role: "user", Snippet: "[match] here", Title: "Renamed Session"},
	}})
	assert.Contains(t, m.View(), "Renamed Session", "search rows render the width-bounded effective title")
}
