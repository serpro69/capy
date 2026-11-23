package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/require"
)

const siblingID = "abcd1234-3333-4444-8000-000000000003"
const grandchildID = "abcd1234-4444-4444-8000-000000000004"

func archiveFamilyMember(t *testing.T, st *deleteTestStore, id, parent, title, project string) {
	t.Helper()
	rec := &vault.SessionRecord{Session: vault.Session{UUID: id, ParentUUID: parent, Title: title, ProjectPath: project, Platform: vault.PlatformCodex, RawJSONL: []byte("{}\n")}}
	require.NoError(t, st.InsertSession(t.Context(), rec))
}

func descendantReviewApp(t *testing.T, opts vault.ListOptions) (Model, *deleteTestStore) {
	t.Helper()
	st := newDeleteTestStore(t)
	archiveFamilyMember(t, st, deleteID, "", "keep parent", "team")
	archiveFamilyMember(t, st, otherID, deleteID, "keep child", "team")
	archiveFamilyMember(t, st, siblingID, deleteID, "hidden sibling", "team")
	m, err := newModel(t.Context(), st, Options{List: opts, ResolveRememberDeletions: func() (bool, error) { return false, nil }})
	require.NoError(t, err)
	m, _ = deleteStep(m, tea.WindowSizeMsg{Width: 100, Height: 10})
	return m, st
}

func toggleDescendants(t *testing.T, m Model) Model {
	t.Helper()
	m, prepare := deleteStep(m, keyMsg("d"))
	require.Equal(t, deletePreparing, m.deletion.phase)
	require.Nil(t, m.deletion.plan)
	require.NotNil(t, prepare)
	m, _ = deleteStep(m, prepare())
	require.Equal(t, modeDelete, m.mode, m.status)
	require.Equal(t, deleteReady, m.deletion.phase)
	return m
}

func TestDeleteDescendantsOpeningList(t *testing.T) {
	m, st := descendantReviewApp(t, vault.ListOptions{IncludeChildren: true, Name: "keep"})
	require.Len(t, m.list.list.Items(), 2)
	require.Equal(t, 1, m.list.list.Paginator.PerPage)
	require.Equal(t, otherID, m.list.list.Items()[1].(sessionItem).sess.UUID)
	m = readyDeletion(t, press(t, m, "m"))
	require.Contains(t, m.deletion.content, "1 explicit, 0 added")
	require.Contains(t, m.deletion.content, "Include descendants: off")
	require.Contains(t, m.deletion.content, "2 direct child session(s) will stay archived")
	require.Contains(t, m.View(), "d descendants")
	m.resolveRememberDeletions = func() (bool, error) { return false, fmt.Errorf("policy was already resolved for this review") }

	checkExpanded := func(m Model) {
		t.Helper()
		text := m.deletion.content
		require.Contains(t, text, "1 explicit, 2 added")
		require.Contains(t, text, "Include descendants: on")
		require.Contains(t, text, "Explicit selection\nUUID: "+deleteID)
		require.Contains(t, text, "Added descendant\nParent: "+deleteID+"\nUUID: "+otherID)
		require.Contains(t, text, "Added descendant · outside current list\nParent: "+deleteID+"\nUUID: "+siblingID)
		require.Equal(t, 1, strings.Count(text, "outside current list"))
		require.False(t, m.deletion.plan.RemembersDeletions())
	}
	m = toggleDescendants(t, m)
	checkExpanded(m)
	// Refresh the list after the parent leaves the filter and the children
	// swap places. Review still owns its original marks and list membership.
	_, err := st.RenameSession(t.Context(), deleteID, vault.RenameOptions{Name: "renamed parent"})
	require.NoError(t, err)
	_, err = st.RenameSession(t.Context(), otherID, vault.RenameOptions{Name: "now hidden"})
	require.NoError(t, err)
	_, err = st.RenameSession(t.Context(), siblingID, vault.RenameOptions{Name: "keep sibling now"})
	require.NoError(t, err)
	m, _, err = m.reloadSessions()
	require.NoError(t, err)
	require.Len(t, m.list.list.Items(), 1)
	require.Equal(t, siblingID, m.list.list.Items()[0].(sessionItem).sess.UUID)
	require.Empty(t, m.list.marks)
	m = toggleDescendants(t, m)
	require.Contains(t, m.deletion.content, "1 explicit, 0 added")
	require.NotContains(t, m.deletion.content, "outside current list")
	m = toggleDescendants(t, m)
	checkExpanded(m)
	require.Contains(t, m.deletion.content, "Title: keep sibling now")

	archiveFamilyMember(t, st, grandchildID, otherID, "later arrival", "another project")
	m, apply := deleteStep(m, keyMsg("y"))
	m, ignored := deleteStep(m, keyMsg("d"))
	require.Nil(t, ignored)
	m, _ = deleteStep(m, apply())
	require.Contains(t, m.status, "deleted 3 session(s)")
	rows, err := st.ListSessions(t.Context(), vault.ListOptions{IncludeChildren: true})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, grandchildID, rows[0].UUID)
}

func TestDeleteDescendantsRootsAndOverlappingMarks(t *testing.T) {
	for _, rootsOnly := range []bool{true, false} {
		t.Run(fmt.Sprintf("rootsOnly=%t", rootsOnly), func(t *testing.T) {
			m, st := descendantReviewApp(t, vault.ListOptions{IncludeChildren: !rootsOnly, Name: "keep"})
			m = readyDeletion(t, press(t, m, "a"))
			archiveFamilyMember(t, st, grandchildID, otherID, "new grandchild", "other\tteam")
			m = toggleDescendants(t, m)
			text := m.deletion.content
			if rootsOnly {
				require.Contains(t, text, "1 explicit, 3 added")
				require.Equal(t, 3, strings.Count(text, "outside current list"))
			} else {
				require.Contains(t, text, "2 explicit, 2 added")
				require.Contains(t, text, "Explicit selection\nUUID: "+otherID)
				require.Equal(t, 1, strings.Count(text, "UUID: "+otherID))
				require.Equal(t, 2, strings.Count(text, "outside current list"))
			}
			require.Contains(t, text, "Added descendant · outside current list\nParent: "+otherID+"\nUUID: "+grandchildID+"\nTitle: new grandchild\nPlatform: codex\nProject: other\\tteam")
			m = press(t, m, "esc")
			require.Zero(t, st.applies)
			m = readyDeletion(t, m)
			require.Contains(t, m.deletion.content, "Include descendants: off")
			require.Len(t, m.deletion.plan.Targets(), m.deletion.plan.ExplicitCount())
		})
	}
}

func TestDeleteDescendantsSupersededReplies(t *testing.T) {
	m, st := descendantReviewApp(t, vault.ListOptions{})
	m = press(t, m, "m")
	m, opening := deleteStep(m, keyMsg("x"))
	m, expand := deleteStep(m, keyMsg("d"))
	canceled := opening().(deletePreparedMsg)
	require.ErrorIs(t, canceled.err, context.Canceled)
	expanded := expand().(deletePreparedMsg)
	require.NoError(t, expanded.err)
	m, collapse := deleteStep(m, keyMsg("d"))
	m, _ = deleteStep(m, canceled)
	m, _ = deleteStep(m, expanded)
	m, apply := deleteStep(m, keyMsg("y"))
	require.Nil(t, apply)
	require.Equal(t, deletePreparing, m.deletion.phase)
	m, _ = deleteStep(m, collapse())
	m, _ = deleteStep(m, expanded)
	require.Contains(t, m.deletion.content, "1 explicit, 0 added")

	// Escape also invalidates a completed toggle whose reply is still queued.
	m, again := deleteStep(m, keyMsg("d"))
	late := again()
	m = press(t, m, "esc")
	require.Equal(t, []string{deleteID}, m.list.deleteSelection().marked)
	m = readyDeletion(t, m)
	m, _ = deleteStep(m, late)
	m, apply = deleteStep(m, keyMsg("y"))
	m, _ = deleteStep(m, apply())
	require.Equal(t, 1, st.applies)
	require.Contains(t, m.status, "deleted 1 session(s)")
	rows, err := st.ListSessions(t.Context(), vault.ListOptions{IncludeChildren: true})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.ElementsMatch(t, []string{otherID, siblingID}, []string{rows[0].UUID, rows[1].UUID})
}
