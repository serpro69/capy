package tui

import (
	"context"
	"errors"
	"strings"
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
	assert.True(t, m.list.listOptions.IncludeChildren)
	require.Len(t, m.list.list.Items(), 2)
	assert.Contains(t, m.list.list.Title, "all sessions")
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
	assert.False(t, m.list.listOptions.IncludeChildren, "a failed re-read leaves the setting as it was")
	assert.Len(t, m.list.list.Items(), 1, "and the current items in place")
	assert.True(t, m.statusErr)
	assert.Contains(t, m.status, "refreshing sessions failed: db gone")
}

func TestAppDirectChildModes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		all      bool
		firstKey string
		nextAll  bool
	}{
		{"child only to roots", false, "S", false},
		{"child only to all", false, "s", true},
		{"all to child only and back", true, "S", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &stubStore{}
			for _, row := range []struct{ id, parent, title string }{
				{"grand", "child", "keep grand"}, {"orphan", "absent", "keep orphan"}, {"child", "root", "keep child"},
				{"omitted", "", "skip by name"}, {"root", "", "keep parent"}, {"extra", "", "keep beyond cap"},
			} {
				st.sessions = append(st.sessions, vault.Session{UUID: row.id, ParentUUID: row.parent, Title: row.title, ProjectPath: "team", Platform: vault.PlatformCodex})
			}
			launch := vault.ListOptions{Name: "keep", Limit: 4, Platform: vault.PlatformCodex, Project: "team", IncludeChildren: tc.all, ChildrenOnly: !tc.all}
			m, err := newModel(t.Context(), st, Options{List: launch})
			require.NoError(t, err)
			resized, _ := m.Update(tea.WindowSizeMsg{Width: 125, Height: 30})
			m = resized.(Model)
			for _, label := range []string{`project "team"`, `name "keep"`, "codex", "limit 4"} {
				require.Contains(t, m.View(), label)
			}
			items := m.list.list.Items()
			require.Equal(t, "orphan", items[0].(sessionItem).sess.UUID)
			require.Contains(t, items[0].(sessionItem).Title(), "(parent absent)")
			if tc.all {
				require.Len(t, items, 4)
				require.Equal(t, "root", items[1].(sessionItem).sess.UUID)
				require.Equal(t, 2, items[3].(sessionItem).depth)
			} else {
				require.Len(t, items, 3)
				require.Equal(t, "child", items[1].(sessionItem).sess.UUID)
				require.Equal(t, 1, items[2].(sessionItem).depth)
			}
			m = press(t, m, tc.firstKey)
			want := launch
			if tc.all {
				want.ChildrenOnly, want.IncludeChildren = true, false
				require.Equal(t, want, st.lastListOpts)
				// A failed exit must keep the child-only list and remember all.
				st.listErr = errors.New("refresh failed")
				before := m.list.list.Items()
				m = press(t, m, "S")
				require.True(t, m.statusErr)
				require.Equal(t, want, m.list.listOptions)
				require.Equal(t, before, m.list.list.Items())
				st.listErr = nil
				m = press(t, m, "S")
			}
			want.ChildrenOnly, want.IncludeChildren = false, tc.nextAll
			require.Equal(t, want, st.lastListOpts)
			require.Equal(t, want, m.list.listOptions)
			if tc.nextAll {
				require.Len(t, m.list.list.Items(), 4)
			} else {
				require.Len(t, m.list.list.Items(), 2)
			}
			// Finder edits operate on the already-capped store result.
			if tc.nextAll {
				m = press(t, m, "f")
				m = typeRunes(t, m, "extra")
				require.Empty(t, m.list.list.Items())
			}
		})
	}
}

func TestAppListScopesRemainVisibleAfterResize(t *testing.T) {
	project := "/worktrees/platform/infrastructure/operator/controller-runtime"
	opts := vault.ListOptions{Project: project, Platform: vault.PlatformCodex, Name: "delivery manager", Limit: 12, ChildrenOnly: true}
	m, err := newModel(t.Context(), &stubStore{}, Options{List: opts})
	require.NoError(t, err)
	for _, width := range []int{160, 80, 40, 80} {
		next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		m = next.(Model)
		view := m.View()
		for _, part := range []string{"children only", "project \"/worktrees", "platform codex", `name "delivery manager"`, "limit 12"} {
			require.Contains(t, view, part, "width %d", width)
		}
		require.LessOrEqual(t, len(strings.Split(view, "\n")), 24)
		for _, row := range m.list.visibleScopeRows() {
			require.LessOrEqual(t, displayWidth(row), width)
		}
	}
}

func TestAppListScopesYieldToFinderInShortTerminal(t *testing.T) {
	project := "/projects/long/directory/for/a/small/terminal"
	sess := vault.Session{UUID: "child001", ParentUUID: "parent01", Platform: vault.PlatformCodex, ProjectPath: project, Title: "keep"}
	st := &stubStore{sessions: []vault.Session{sess}}
	m, err := newModel(t.Context(), st, Options{List: vault.ListOptions{ChildrenOnly: true, Project: project, Platform: vault.PlatformCodex, Name: "keep", Limit: 12}})
	require.NoError(t, err)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 9})
	m = press(t, next.(Model), "f")
	rows := strings.Split(m.View(), "\n")
	// Bubble Tea takes the last terminal-height rows when a view is too tall.
	visible := strings.Join(rows[max(0, len(rows)-9):], "\n")
	require.Contains(t, visible, "filter:")
	require.Contains(t, visible, "children only")
	require.Contains(t, visible, "keep")
	require.Empty(t, m.list.visibleScopeRows())
}

func TestApp_ChildrenGroupedInVisibleList(t *testing.T) {
	// The store returns newest end time first. The newest child belongs to
	// the oldest parent, and both families are interleaved in that order.
	longTitle := "Agency" + strings.Repeat(" long title", 12)
	m, st := codexFamilyApp(t,
		vault.Session{UUID: "new-child-a", ParentUUID: "parent-a", Title: longTitle},
		vault.Session{UUID: "grandchild", ParentUUID: "new-child-a", Title: "Agency review"},
		vault.Session{UUID: "parent-b", Title: "Parent B"},
		vault.Session{UUID: "orphan", ParentUUID: "missing", Title: "Orphan"},
		vault.Session{UUID: "child-b", ParentUUID: "parent-b", Title: "Child B"},
		vault.Session{UUID: "old-child-a", ParentUUID: "parent-a", Title: "Older child"},
		vault.Session{UUID: "parent-a", Title: "Parent A"},
	)
	ids := func(m Model) []string {
		var ids []string
		for _, item := range m.list.list.Items() {
			ids = append(ids, item.(sessionItem).sess.UUID)
		}
		return ids
	}
	m = press(t, m, "down", listChildrenKey)
	require.Equal(t, []string{"parent-b", "child-b", "orphan", "parent-a", "new-child-a", "grandchild", "old-child-a"}, ids(m))
	selected, ok := m.list.selected()
	require.True(t, ok)
	assert.Equal(t, "parent-a", selected.UUID)
	items := m.list.list.Items()
	assert.Equal(t, "(parent missing)  Orphan", items[2].(sessionItem).Title())
	assert.Contains(t, items[5].(sessionItem).Title(), "    ↳ ")

	// A grandchild disappears on collapse; its family's root remains selected.
	m = press(t, m, "down", "down", listChildrenKey)
	selected, ok = m.list.selected()
	require.True(t, ok)
	require.Equal(t, "parent-a", selected.UUID)
	m = press(t, m, listChildrenKey)

	m = press(t, m, "f")
	calls := st.listCalls
	m = press(t, m, "A", "g", "e", "n", "c", "y")
	require.Equal(t, []string{"new-child-a", "grandchild"}, ids(m))
	m = press(t, m, "up")
	assert.Contains(t, m.View(), "(parent parent-a)", "the parent stays visible when the child's title is truncated")
	assert.Contains(t, m.list.list.Items()[1].(sessionItem).Title(), "  ↳ ")
	assert.Equal(t, calls, st.listCalls)
	m = press(t, m, "esc", listChildrenKey)
	assert.Equal(t, []string{"parent-b", "parent-a"}, ids(m))
}

func TestApp_HidingOrphanKeepsASelectableRow(t *testing.T) {
	m, _ := codexFamilyApp(t, vault.Session{UUID: "root"}, vault.Session{UUID: "orphan", ParentUUID: "missing"})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = press(t, next.(Model), listChildrenKey, "down", listChildrenKey)
	selected, ok := m.list.selected()
	require.True(t, ok)
	require.Equal(t, "root", selected.UUID)
}

func TestSessionListItems_CyclicParentIDs(t *testing.T) {
	sessions := []vault.Session{
		{UUID: "a", ParentUUID: "b"}, {UUID: "b", ParentUUID: "a"},
		{UUID: "self", ParentUUID: "self"},
	}
	items := sessionListItems(sessions, true)
	var ids []string
	for _, item := range items {
		ids = append(ids, item.(sessionItem).sess.UUID)
	}
	require.ElementsMatch(t, []string{"a", "b", "self"}, ids)
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
	topLine := m.viewer.active.lineForRow(m.viewer.vp.YOffset)
	require.Greater(t, topLine, 0)
	m = press(t, m, "enter")
	require.Equal(t, codexChildID, m.viewer.sess.UUID)

	tm, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 12})
	m = tm.(Model)
	m = press(t, m, "q")
	assert.Equal(t, codexParentID, m.viewer.sess.UUID)
	assert.Equal(t, 60, m.viewer.width, "the restored parent is re-sized to the current terminal")
	assert.Equal(t, topLine, m.viewer.active.lineForRow(m.viewer.vp.YOffset), "top source line preserved")
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
