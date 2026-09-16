package tui

import (
	"context"
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Root-model tests for Codex child sessions (design § TUI, Task 10): the list's
// children toggle and the root-routed open-child / frame-stack flow. Everything
// runs over the stubStore, which mirrors the real store's default
// `parent_uuid IS NULL` predicate.

// codexFamilyApp builds a list-mode app over a Codex parent (tall enough that
// its spawn marker starts off-screen at height 12) and, unless omitted, its
// child. Returns the app sized so the viewer shows ~10 transcript rows.
func codexFamilyApp(t *testing.T, sessions ...vault.Session) (Model, *stubStore) {
	t.Helper()
	st := &stubStore{sessions: sessions, files: map[string][]vault.File{}}
	m, err := newModel(context.Background(), st, Options{})
	require.NoError(t, err)
	tm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 12})
	return tm.(Model), st
}

func press(t *testing.T, m Model, keys ...string) Model {
	t.Helper()
	for _, k := range keys {
		next, _ := m.Update(keyMsg(k))
		m = next.(Model)
	}
	return m
}

func TestApp_ChildrenToggleReloadsWithIncludeChildren(t *testing.T) {
	parent := codexParentSession(t, codexParentID, codexChildID, 0)
	child := codexChildSession(t, codexChildID, codexParentID)
	m, st := codexFamilyApp(t, parent, child)

	// Default: the child is hidden, exactly as the CLI's `vault list` hides it.
	require.Len(t, m.list.list.Items(), 1)
	assert.False(t, st.lastListOpts.IncludeChildren)
	assert.Contains(t, m.View(), "children", "the help line advertises the toggle")

	calls := st.listCalls
	m = press(t, m, listChildrenKey)
	assert.Equal(t, calls+1, st.listCalls, "the toggle re-reads the snapshot (children are a store predicate)")
	assert.True(t, st.lastListOpts.IncludeChildren)
	assert.True(t, m.list.includeChildren)
	require.Len(t, m.list.list.Items(), 2)
	assert.Contains(t, m.list.list.Title, "incl. children")
	var childItem sessionItem
	for _, it := range m.list.list.Items() {
		if si := it.(sessionItem); si.sess.UUID == codexChildID {
			childItem = si
		}
	}
	require.Equal(t, codexChildID, childItem.sess.UUID, "the child row is listed")
	assert.Contains(t, childItem.Title(), "↳ "+shortID(codexParentID), "indented under its parent's short id")

	m = press(t, m, listChildrenKey)
	assert.False(t, st.lastListOpts.IncludeChildren)
	assert.Len(t, m.list.list.Items(), 1)
	assert.NotContains(t, m.list.list.Title, "children")
}

func TestApp_ChildrenToggleReloadFailureReverts(t *testing.T) {
	parent := codexParentSession(t, codexParentID, codexChildID, 0)
	child := codexChildSession(t, codexChildID, codexParentID)
	m, st := codexFamilyApp(t, parent, child)
	st.listErr = errors.New("db gone")

	m = press(t, m, listChildrenKey)
	assert.False(t, m.list.includeChildren, "a failed re-read leaves the setting as it was")
	assert.Len(t, m.list.list.Items(), 1, "and the current items in place")
	assert.True(t, m.statusErr)
	assert.Contains(t, m.status, "refreshing sessions failed: db gone")
}

// TestApp_OpenChildAndReturnToParentThenList drives the whole chain: open the
// parent from the list, scroll to its spawn marker, open the child (root-routed
// through the store), return to the parent at the same offset with the same
// focus, then leave the viewer for the list.
func TestApp_OpenChildAndReturnToParentThenList(t *testing.T) {
	parent := codexParentSession(t, codexParentID, codexChildID, 40)
	child := codexChildSession(t, codexChildID, codexParentID)
	m, _ := codexFamilyApp(t, parent, child)

	m = press(t, m, "enter")
	require.Equal(t, modeView, m.mode)
	require.Equal(t, codexParentID, m.viewer.sess.UUID)
	require.Len(t, m.viewer.active.markers, 1)

	// ] focuses the (off-screen) marker and scrolls to it.
	m = press(t, m, "]")
	require.Equal(t, 0, m.viewer.focusedMarker)
	savedOffset := m.viewer.vp.YOffset
	require.Greater(t, savedOffset, 0, "the marker sits below the first viewport page")

	// enter opens the child SESSION through the store; the parent is suspended.
	m = press(t, m, "enter")
	assert.Equal(t, modeView, m.mode)
	assert.Equal(t, codexChildID, m.viewer.sess.UUID)
	assert.Equal(t, codexParentID, m.viewer.sess.ParentUUID)
	assert.Contains(t, m.View(), "child findings about the commit")
	assert.Contains(t, m.View(), "Codex · child of "+shortID(codexParentID))
	require.Len(t, m.viewerStack, 1)
	assert.Equal(t, codexParentID, m.viewerStack[0].viewer.sess.UUID)
	assert.Equal(t, modeList, m.prevMode, "prevMode is untouched by a child open")
	assert.Equal(t, "", m.status, "a successful open sets no status")

	// esc returns to the parent exactly where it was.
	m = press(t, m, "esc")
	assert.Equal(t, modeView, m.mode)
	assert.Equal(t, codexParentID, m.viewer.sess.UUID)
	assert.Empty(t, m.viewerStack)
	assert.Equal(t, savedOffset, m.viewer.vp.YOffset, "scroll offset preserved across the child")
	assert.Equal(t, 0, m.viewer.focusedMarker, "marker focus preserved across the child")

	// q with an empty stack leaves the viewer for the originating mode.
	m = press(t, m, "q")
	assert.Equal(t, modeList, m.mode)
}

// TestApp_OpenChildSurvivesResizeWhileInChild pins the pop re-size: a terminal
// resize while the child is open re-wraps the restored parent at the new width
// and keeps its top source line, so the parent does not snap back to the top.
// (Without a resize the frame is restored verbatim — the exact-offset case is
// TestApp_OpenChildAndReturnToParentThenList.)
func TestApp_OpenChildSurvivesResizeWhileInChild(t *testing.T) {
	parent := codexParentSession(t, codexParentID, codexChildID, 40)
	child := codexChildSession(t, codexChildID, codexParentID)
	m, _ := codexFamilyApp(t, parent, child)

	m = press(t, m, "enter", "]")
	topLine := m.viewer.main.lineForRow(m.viewer.vp.YOffset)
	require.Greater(t, topLine, 0)
	m = press(t, m, "enter")
	require.Equal(t, codexChildID, m.viewer.sess.UUID)

	tm, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 12})
	m = tm.(Model)
	m = press(t, m, "q")
	assert.Equal(t, codexParentID, m.viewer.sess.UUID)
	assert.Equal(t, 60, m.viewer.width, "the restored parent is re-sized to the current terminal")
	assert.Equal(t, topLine, m.viewer.main.lineForRow(m.viewer.vp.YOffset), "top source line preserved")
}

// TestApp_TwoLevelChildChainPopsInOrder: parent → child → grandchild, then two
// pops land on the child and the parent in turn, and a third leaves the viewer.
func TestApp_TwoLevelChildChainPopsInOrder(t *testing.T) {
	parent := codexParentSession(t, codexParentID, codexChildID, 0)
	child := codexParentSession(t, codexChildID, codexGrandchildID, 0)
	child.ParentUUID = codexParentID
	child.Title = "Child reviewer"
	grandchild := codexChildSession(t, codexGrandchildID, codexChildID)
	m, _ := codexFamilyApp(t, parent, child, grandchild)

	m = press(t, m, "enter", "]", "enter")
	require.Equal(t, codexChildID, m.viewer.sess.UUID)
	require.Len(t, m.viewerStack, 1)

	m = press(t, m, "]", "enter")
	require.Equal(t, codexGrandchildID, m.viewer.sess.UUID)
	require.Len(t, m.viewerStack, 2)
	assert.Equal(t, codexParentID, m.viewerStack[0].viewer.sess.UUID)
	assert.Equal(t, codexChildID, m.viewerStack[1].viewer.sess.UUID)
	assert.Contains(t, m.View(), "child of "+shortID(codexChildID))

	m = press(t, m, "q")
	assert.Equal(t, codexChildID, m.viewer.sess.UUID)
	assert.Len(t, m.viewerStack, 1)
	assert.Equal(t, modeView, m.mode)

	m = press(t, m, "q")
	assert.Equal(t, codexParentID, m.viewer.sess.UUID)
	assert.Empty(t, m.viewerStack)
	assert.Equal(t, modeView, m.mode)

	m = press(t, m, "q")
	assert.Equal(t, modeList, m.mode)
}

// TestApp_OpenUnarchivedChildIsStatusNotError: a marker whose child rollout was
// never imported yields a neutral status line; the viewer, the stack and the
// mode are untouched, and the next key clears the status like any transient.
func TestApp_OpenUnarchivedChildIsStatusNotError(t *testing.T) {
	parent := codexParentSession(t, codexParentID, codexChildID, 0)
	m, _ := codexFamilyApp(t, parent) // no child row in the store

	m = press(t, m, "enter", "]")
	offset := m.viewer.vp.YOffset
	m = press(t, m, "enter")
	assert.Equal(t, modeView, m.mode)
	assert.Equal(t, codexParentID, m.viewer.sess.UUID, "the viewer stays on the parent")
	assert.Empty(t, m.viewerStack, "nothing was pushed")
	assert.Equal(t, offset, m.viewer.vp.YOffset)
	assert.Equal(t, "child session "+shortID(codexChildID)+" not archived", m.status)
	assert.False(t, m.statusErr, "a missing child is information, not an error")
	assert.Contains(t, m.View(), "not archived")

	m = press(t, m, "j")
	assert.Equal(t, "", m.status, "transient: cleared by the next keystroke")
}

// TestApp_OpenChildStoreErrorIsErrorStatus: a store failure other than
// not-found is surfaced in the error style, likewise without mutating the viewer.
func TestApp_OpenChildStoreErrorIsErrorStatus(t *testing.T) {
	parent := codexParentSession(t, codexParentID, codexChildID, 0)
	child := codexChildSession(t, codexChildID, codexParentID)
	m, st := codexFamilyApp(t, parent, child)
	m = press(t, m, "enter", "]")
	st.getErr = errors.New("db gone")

	m = press(t, m, "enter")
	assert.Equal(t, codexParentID, m.viewer.sess.UUID)
	assert.Empty(t, m.viewerStack)
	assert.True(t, m.statusErr)
	assert.Contains(t, m.status, "opening child session "+shortID(codexChildID)+": db gone")

	// A files read failure after a successful session read is the same shape:
	// error status, nothing pushed, viewer untouched.
	st.getErr = nil
	st.filesErr = errors.New("files gone")
	m = press(t, m, "]", "enter")
	assert.Equal(t, codexParentID, m.viewer.sess.UUID)
	assert.Empty(t, m.viewerStack)
	assert.True(t, m.statusErr)
	assert.Contains(t, m.status, "loading child session files: files gone")
}

// TestApp_FreshOpenFromListDropsSuspendedParents: a new open from the list (or
// search) starts a new chain — a parent suspended by an earlier drill-down must
// not resurface behind the freshly opened session.
func TestApp_FreshOpenFromListDropsSuspendedParents(t *testing.T) {
	parent := codexParentSession(t, codexParentID, codexChildID, 0)
	child := codexChildSession(t, codexChildID, codexParentID)
	m, _ := codexFamilyApp(t, parent, child)

	m = press(t, m, "enter", "]", "enter")
	require.Len(t, m.viewerStack, 1)
	// Rename-free way to leave the viewer with a non-empty stack does not exist
	// (q pops first), so simulate the list re-open path directly.
	m = press(t, m, "q", "q")
	require.Equal(t, modeList, m.mode)
	m.viewerStack = []viewerFrame{{viewer: m.viewer}} // a stale frame, as if left behind
	m = press(t, m, "enter")
	assert.Equal(t, modeView, m.mode)
	assert.Empty(t, m.viewerStack, "openSession starts a new chain")
}

// TestApp_ViewKeysActOnOpenChild: r/R/e in the viewer target the session being
// shown — the child while a child is open — not the suspended parent.
func TestApp_ViewKeysActOnOpenChild(t *testing.T) {
	parent := codexParentSession(t, codexParentID, codexChildID, 0)
	child := codexChildSession(t, codexChildID, codexParentID)
	m, _ := codexFamilyApp(t, parent, child)
	m = press(t, m, "enter", "]", "enter")
	require.Equal(t, codexChildID, m.viewer.sess.UUID)

	next, _ := m.Update(keyMsg("r"))
	got := next.(Model)
	assert.Equal(t, ActionRestore, got.action.Kind)
	assert.Equal(t, codexChildID, got.action.SessionUUID)

	m = press(t, m, "e")
	assert.True(t, m.renaming)
	assert.Equal(t, codexChildID, m.renameUUID)
}
