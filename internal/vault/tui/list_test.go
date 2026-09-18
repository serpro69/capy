package tui

import (
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionItem_Display(t *testing.T) {
	it := sessionItem{sess: vault.Session{
		UUID: "abcdef0123", Title: "My session", ProjectPath: "/home/u/proj",
		MessageCount: 12, SizeBytes: 2048, EndTime: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
	}}
	assert.Equal(t, "My session", it.Title())
	desc := it.Description()
	assert.Contains(t, desc, "abcdef01")
	assert.Contains(t, desc, "2026-05-01")
	assert.Contains(t, desc, "12msg")
	assert.Contains(t, desc, "2.0KB")
	fv := it.FilterValue()
	assert.Contains(t, fv, "My session")
	assert.Contains(t, fv, "/home/u/proj")
}

func TestSessionItem_UntitledFallback(t *testing.T) {
	it := sessionItem{sess: vault.Session{UUID: "deadbeef00"}}
	assert.Equal(t, "(untitled)", it.Title())
}

func TestSessionItem_EffectiveTitle(t *testing.T) {
	custom := "Custom Name"
	named := vault.Session{
		UUID: "abcdef0123", Title: "Imported title", ProjectPath: "/home/u/proj",
		Name: &vault.SessionName{CustomTitle: &custom, RenamedAtNS: 1, MachineID: "m"},
	}
	it := sessionItem{sess: named}
	assert.Equal(t, "Custom Name", it.Title(), "an active custom name overrides the imported title")
	assert.Contains(t, it.FilterValue(), "Custom Name", "filtering sees the effective title")

	// A clear tombstone falls back to the imported title.
	named.Name = &vault.SessionName{CustomTitle: nil, RenamedAtNS: 2, MachineID: "m"}
	it = sessionItem{sess: named}
	assert.Equal(t, "Imported title", it.Title())
}

// TestSessionItem_PlatformInDescription pins the platform token on the
// secondary line: the stored value ("codex"), and "claude-code" for a Session
// built without the field (the in-memory zero value is Claude).
func TestSessionItem_PlatformInDescription(t *testing.T) {
	codex := sessionItem{sess: vault.Session{UUID: codexParentID, Platform: vault.PlatformCodex}}
	assert.Contains(t, codex.Description(), " · codex · ")
	claude := sessionItem{sess: vault.Session{UUID: "abcdef0123"}}
	assert.Contains(t, claude.Description(), " · claude-code · ")
}

// TestSessionItem_ChildRowIndentedUnderParent pins the child-row shape the CLI's
// sessionTitleCell also uses: indented, "↳", the parent's short id, then the
// title. A top-level row is untouched.
func TestSessionItem_ChildRowIndentedUnderParent(t *testing.T) {
	child := sessionItem{sess: vault.Session{
		UUID: codexChildID, ParentUUID: codexParentID, Platform: vault.PlatformCodex, Title: "Boole · code-reviewer",
	}}
	assert.Equal(t, "  ↳ "+shortID(codexParentID)+"  Boole · code-reviewer", child.Title())
	assert.Equal(t, "  ↳ 019dc606-f55  Boole · code-reviewer", child.Title(), "12-character parent id")

	parent := sessionItem{sess: vault.Session{UUID: codexParentID, Platform: vault.PlatformCodex, Title: "Parent review"}}
	assert.Equal(t, "Parent review", parent.Title())

	untitledChild := sessionItem{sess: vault.Session{UUID: codexChildID, ParentUUID: codexParentID}}
	assert.Equal(t, "  ↳ "+shortID(codexParentID)+"  (untitled)", untitledChild.Title(),
		"the untitled fallback still carries the parent prefix")
}

// TestListKeys_ChildrenToggleIsUnbound pins that listChildrenKey collides with
// neither the app's own list-mode bindings nor any default bubbles/list
// navigation binding — the wrapped list would otherwise consume or double-act
// on the key. Update both sets here when adding a list-mode key.
func TestListKeys_ChildrenToggleIsUnbound(t *testing.T) {
	appKeys := []string{"q", "/", "f", "e", "r", "R", "enter", "esc", "ctrl+c"}
	assert.NotContains(t, appKeys, listChildrenKey)

	km := list.DefaultKeyMap()
	keyPress := keyMsg(listChildrenKey)
	for name, b := range map[string]key.Binding{
		"CursorUp": km.CursorUp, "CursorDown": km.CursorDown, "PrevPage": km.PrevPage, "NextPage": km.NextPage,
		"GoToStart": km.GoToStart, "GoToEnd": km.GoToEnd, "Filter": km.Filter, "ClearFilter": km.ClearFilter,
		"CancelWhileFiltering": km.CancelWhileFiltering, "AcceptWhileFiltering": km.AcceptWhileFiltering,
		"ShowFullHelp": km.ShowFullHelp, "CloseFullHelp": km.CloseFullHelp, "Quit": km.Quit, "ForceQuit": km.ForceQuit,
	} {
		assert.False(t, key.Matches(keyPress, b), "%q must not be bound to bubbles/list %s", listChildrenKey, name)
	}
}

// TestListModel_IncludeChildrenOptionsAndTitle pins the store query the toggle
// drives and the title suffix that tells the user children are shown.
func TestListModel_IncludeChildrenOptionsAndTitle(t *testing.T) {
	m := newListModel(nil, DefaultStyles(), 80, 24)
	assert.Equal(t, vault.ListOptions{}, m.listOptions(), "children hidden by default — the unfiltered read")
	assert.NotContains(t, m.list.Title, "children")

	m.platform = vault.PlatformCodex
	assert.Equal(t, vault.ListOptions{Platform: vault.PlatformCodex}, m.listOptions(),
		"the launch platform is retained in later store reads")

	m = m.setIncludeChildren(true)
	assert.Equal(t, vault.ListOptions{IncludeChildren: true, Platform: vault.PlatformCodex}, m.listOptions())
	m, _ = m.setSessions(nil, "")
	assert.Contains(t, m.list.Title, "incl. children")

	m = m.setIncludeChildren(false)
	m, _ = m.setSessions(nil, "")
	assert.NotContains(t, m.list.Title, "children")
}

func TestListModel_Selected(t *testing.T) {
	sessions := []vault.Session{
		{UUID: "1111aaaa", Title: "one"},
		{UUID: "2222bbbb", Title: "two"},
	}
	m := newListModel(sessions, DefaultStyles(), 80, 24)
	sel, ok := m.selected()
	require.True(t, ok)
	assert.Equal(t, "1111aaaa", sel.UUID)
}

func TestListModel_SelectedEmpty(t *testing.T) {
	m := newListModel(nil, DefaultStyles(), 80, 24)
	_, ok := m.selected()
	assert.False(t, ok)
}
