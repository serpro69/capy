package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/require"
)

// Use the real prepare/apply boundary and independently inject list/search
// failures so tests can observe both durable deletion and the cached UI.
type deleteTestStore struct {
	*vault.VaultStore
	listErr, searchErr error
	prepares, applies  int
	options            vault.DeleteOptions
}

func (s *deleteTestStore) ListSessions(ctx context.Context, opts vault.ListOptions) ([]vault.Session, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.VaultStore.ListSessions(ctx, opts)
}
func (s *deleteTestStore) Search(ctx context.Context, opts vault.SearchOptions) ([]vault.SearchResult, error) {
	if s.searchErr != nil {
		return nil, s.searchErr
	}
	return s.VaultStore.Search(ctx, opts)
}
func (s *deleteTestStore) PrepareDelete(ctx context.Context, ids []string, opts vault.DeleteOptions) (*vault.DeletePlan, error) {
	s.prepares++
	s.options = opts
	return s.VaultStore.PrepareDelete(ctx, ids, opts)
}
func (s *deleteTestStore) ApplyDelete(ctx context.Context, p *vault.DeletePlan) error {
	s.applies++
	return s.VaultStore.ApplyDelete(ctx, p)
}

const deleteID = "abcd1234-1111-4444-8000-000000000001"
const otherID = "abcd1234-2222-4444-8000-000000000002"

func archiveForDelete(t *testing.T, st *vault.VaultStore, id string) {
	t.Helper()
	rec := vault.SessionRecord{
		Session: vault.Session{UUID: id, Title: "review\nline\u2028next", ProjectPath: "/repo\t\x1b[7m", RawJSONL: []byte("{}\n")},
		FTS:     []vault.FTSRow{{SessionUUID: id, Role: "user", ContentText: "needle"}},
	}
	require.NoError(t, st.InsertSession(t.Context(), &rec))
}

func newDeleteTestStore(t *testing.T) *deleteTestStore {
	t.Helper()
	t.Setenv("CAPY_VAULT_KEY", "synthetic-vault-test-key")
	t.Setenv("CAPY_MACHINE_ID", "test-machine")
	st := &deleteTestStore{VaultStore: vault.NewVaultStore(filepath.Join(t.TempDir(), "vault.db"))}
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	return st
}

func deletionApp(t *testing.T, remember bool, ids ...string) (Model, *deleteTestStore) {
	t.Helper()
	st := newDeleteTestStore(t)
	for _, id := range ids {
		archiveForDelete(t, st.VaultStore, id)
	}
	m, err := newModel(t.Context(), st, Options{ResolveRememberDeletions: func() (bool, error) { return remember, nil }})
	require.NoError(t, err)
	m, _ = deleteStep(m, tea.WindowSizeMsg{Width: 90, Height: 24})
	return m, st
}

func deleteStep(m Model, msg tea.Msg) (Model, tea.Cmd) {
	next, cmd := m.Update(msg)
	m = next.(Model)
	// These model tests drive one displayed frame per step. The renderer
	// integration test separately exercises input while a frame is buffered.
	if m.mode == modeDelete && m.deletion.phase == deleteReady {
		m.deleteOutput.didWrite([]byte(m.View()))
	}
	return m, cmd
}

func readyDeletion(t *testing.T, m Model) Model {
	t.Helper()
	m, prepare := deleteStep(m, keyMsg("x"))
	require.Equal(t, modeDelete, m.mode)
	require.NotNil(t, prepare)
	m, _ = deleteStep(m, prepare())
	require.Equal(t, deleteReady, m.deletion.phase, m.status)
	require.Equal(t, modeDelete, m.mode, m.status)
	return m
}

func TestDeleteMarksAcrossPagesAndChildModes(t *testing.T) {
	st := &stubStore{}
	for i := range 24 {
		id := fmt.Sprintf("root-%032d", i)
		st.sessions = append(st.sessions, vault.Session{UUID: id, Title: "root"}, vault.Session{UUID: fmt.Sprintf("child-%032d", i), Title: "child", ParentUUID: id})
	}
	for _, tc := range []struct {
		key             string
		roots, children int
	}{{"S", 24, 0}, {"s", 24, 24}} {
		t.Run(tc.key, func(t *testing.T) {
			m, err := newModel(t.Context(), st, Options{List: vault.ListOptions{ChildrenOnly: true}})
			require.NoError(t, err)
			m, _ = deleteStep(m, tea.WindowSizeMsg{Width: 85, Height: 15})
			m = press(t, m, tc.key, "a")
			require.Greater(t, m.list.list.Paginator.TotalPages, 1)
			require.Len(t, m.list.marks, tc.roots+tc.children)
			for _, row := range st.sessions {
				require.Equal(t, row.ParentUUID == "" || tc.children > 0, m.list.marks[row.UUID])
			}
			m = press(t, m, "?")
			lines := strings.Split(ansi.Strip(m.View()), "\n")
			visible := strings.Join(lines[max(0, len(lines)-15):], "\n")
			require.Contains(t, visible, fmt.Sprintf("%d marked", tc.roots+tc.children))
			selected := m.list.list.SelectedItem().(sessionItem)
			require.True(t, selected.marked)
			require.Contains(t, visible, selected.Title())
			m = press(t, m, "?")
			m = press(t, m, "right")
			require.Positive(t, m.list.list.Paginator.Page)
			sel, ok := m.list.selected()
			require.True(t, ok)
			require.True(t, m.list.list.SelectedItem().(sessionItem).marked)
			m = press(t, m, "m")
			require.False(t, m.list.marks[sel.UUID])
			m = m.withError("test error")
			require.Contains(t, m.View(), fmt.Sprintf("%d marked", tc.roots+tc.children-1))
			require.Contains(t, m.View(), "test error")
			m = press(t, m, "A")
			require.Empty(t, m.list.marks)
		})
	}
}

func TestDeleteMarksFollowAcceptedFinder(t *testing.T) {
	m, _ := twoProjectApp(t)
	m = press(t, m, "a", "f")
	m = typeRunes(t, m, "alpha")
	require.Len(t, m.list.marks, 2)
	m = press(t, m, "x") // a draft character
	require.True(t, m.list.filtering)
	require.Equal(t, modeList, m.mode)
	m = press(t, m, "backspace", "enter")
	require.Equal(t, []string{"aaaa1111"}, m.list.deleteSelection().marked)
	require.Contains(t, m.status, "dropped 1 mark")
	m = press(t, m, "f", "esc")
	require.Len(t, m.list.list.Items(), 2)
	require.Equal(t, []string{"aaaa1111"}, m.list.deleteSelection().marked)
}

func TestDeleteMarksAfterMetadataRefresh(t *testing.T) {
	for _, field := range []string{"title", "project"} {
		t.Run(field, func(t *testing.T) {
			m, st := twoProjectApp(t)
			for i := range st.sessions {
				st.sessions[i].Title = "alpha"
				st.sessions[i].ProjectPath = "/keep"
			}
			m, _, _ = m.reloadSessions()
			accepted := "alpha"
			if field == "project" {
				accepted = "keep"
			}
			m = press(t, m, "f")
			m = typeRunes(t, m, accepted)
			m = press(t, m, "enter", "a")
			if field == "title" {
				m = press(t, m, "e")
			} else {
				// The draft matches only the highlighted row. Refresh against
				// the accepted project must retain the other hidden mark.
				m = press(t, m, "f")
				m.list.filter.SetValue("aaaa")
				m.list, _ = m.list.redraw()
				m = press(t, m, "ctrl+g")
			}
			m, save := submitRename(t, m, "elsewhere")
			m, _ = deleteStep(m, renameResult(t, save))
			require.Contains(t, m.status, "dropped 1 mark")
			require.Equal(t, map[string]bool{"bbbb2222": true}, m.list.marks)
			if field == "project" {
				require.True(t, m.list.filtering)
				require.Equal(t, "keep", m.list.applied)
				require.Len(t, m.list.list.Items(), 1)
				require.Equal(t, "aaaa1111", m.list.list.Items()[0].(sessionItem).sess.UUID)
				m = press(t, m, "esc")
			}
			require.Equal(t, []string{"bbbb2222"}, m.list.deleteSelection().marked)
		})
	}
}

func TestDeletePreviewConfirmationAndQuit(t *testing.T) {
	m, st := deletionApp(t, false, deleteID)
	m, cmd := deleteStep(m, keyMsg("x"))
	require.Nil(t, cmd)
	require.Equal(t, modeList, m.mode)
	m = press(t, m, "m")
	m, prepare := deleteStep(m, keyMsg("x"))
	first := prepare()
	for _, key := range []string{"x", "enter", "y"} {
		m, cmd = deleteStep(m, keyMsg(key))
		require.Nil(t, cmd)
	}
	m = press(t, m, "esc")
	require.Equal(t, modeList, m.mode)
	require.Len(t, m.list.marks, 1)
	m, prepare = deleteStep(m, keyMsg("x"))
	m, _ = deleteStep(m, first)
	require.Equal(t, deletePreparing, m.deletion.phase)
	m, _ = deleteStep(m, prepare())
	require.Equal(t, vault.DeleteOptions{ExactIDs: true}, st.options)
	require.Contains(t, m.View(), "One-time local deletion")
	require.Contains(t, m.View(), `review\nline\u2028next`)
	require.Contains(t, m.View(), `/repo\t\x1b[7m`)
	m, _ = deleteStep(m, tea.WindowSizeMsg{Width: 16, Height: 1})
	require.NotContains(t, m.View(), "\n")
	require.Contains(t, m.View(), "Review deletion")
	m, _ = deleteStep(m, tea.WindowSizeMsg{Width: 16, Height: 6})
	m = press(t, m, "down")
	require.Equal(t, 1, m.deletion.vp.YOffset)
	m = press(t, m, "pgdown")
	require.Equal(t, 6, m.deletion.vp.YOffset)
	m = press(t, m, "pgup", "up")
	require.Zero(t, m.deletion.vp.YOffset)
	var text strings.Builder
	for {
		rows := strings.Split(ansi.Strip(m.View()), "\n")
		for _, row := range rows[:len(rows)-1] {
			text.WriteString(strings.TrimRight(row, " "))
		}
		if m.deletion.vp.AtBottom() {
			break
		}
		m = press(t, m, "pgdown")
	}
	require.Contains(t, text.String(), deleteID)
	m = press(t, m, "x", "enter")
	require.Equal(t, deleteReady, m.deletion.phase)
	m, apply := deleteStep(m, keyMsg("y"))
	require.NotNil(t, apply)
	for _, key := range []string{"y", "esc", "ctrl+g", "ctrl+c"} {
		m, cmd = deleteStep(m, keyMsg(key))
		require.Nil(t, cmd)
		require.False(t, m.quitting)
		require.False(t, m.renaming)
		require.Equal(t, deleteApplying, m.deletion.phase)
	}
	require.IsType(t, tea.KeyMsg{}, waitForDelete(m, tea.QuitMsg{}))
	require.IsType(t, tea.KeyMsg{}, waitForDelete(m, tea.InterruptMsg{}))
	m, cmd = deleteStep(m, apply())
	require.True(t, m.quitting)
	require.IsType(t, tea.QuitMsg{}, cmd())
	require.Equal(t, 1, st.applies)
	require.Contains(t, m.status, "deleted 1 session")
	require.Empty(t, m.View(), "quitting skips the final result frame")
	var stderr strings.Builder
	require.NoError(t, m.deleteOutput.finishReports(&stderr))
	require.Equal(t, "deleted 1 session(s)\n", stderr.String())
	_, err := st.GetSession(t.Context(), deleteID)
	require.ErrorIs(t, err, vault.ErrSessionNotFound)
}

func TestDeletePreviewStaleSelection(t *testing.T) {
	for _, when := range []string{"before prepare", "before apply", "changed project", "reload fails"} {
		t.Run(when, func(t *testing.T) {
			m, st := deletionApp(t, true, deleteID, otherID)
			m, _ = deleteStep(m, tea.WindowSizeMsg{Width: 80, Height: 18})
			m = press(t, m, "a")
			m, prepare := deleteStep(m, keyMsg("x"))
			var cmd tea.Cmd
			if when != "before prepare" {
				m, _ = deleteStep(m, prepare())
				m, cmd = deleteStep(m, keyMsg("y"))
			} else {
				cmd = prepare
			}
			if when == "changed project" {
				_, err := st.SetSessionProject(t.Context(), otherID, vault.ProjectOptions{Name: "reassigned"})
				require.NoError(t, err)
			} else {
				ok, err := st.DeleteSession(t.Context(), otherID, false)
				require.NoError(t, err)
				require.True(t, ok)
			}
			if when == "reload fails" {
				st.listErr = errors.New("list unavailable")
			}
			m, _ = deleteStep(m, cmd())
			require.Equal(t, modeList, m.mode)
			require.True(t, m.statusErr)
			_, err := st.GetSession(t.Context(), deleteID)
			require.NoError(t, err)
			applies := st.applies
			if when == "reload fails" {
				require.True(t, m.deleteNeedsReload)
				require.Len(t, m.list.marks, 2)
				require.Contains(t, m.status, "list unavailable")
				require.Contains(t, m.View(), "stale preview · reload failed (x retries) · target missing")
				m, cmd = deleteStep(m, keyMsg("x"))
				require.Nil(t, cmd)
				st.listErr = nil
			} else if when != "changed project" {
				require.Contains(t, m.View(), "stale preview · dropped 1 mark(s) · target missing")
			} else {
				require.Contains(t, m.View(), "stale preview · dropped 0 mark(s) · project changed")
			}
			m = readyDeletion(t, m)
			require.Equal(t, applies, st.applies, "retry must require fresh confirmation")
			if when == "changed project" {
				require.Len(t, m.deletion.plan.Targets(), 2)
				require.Contains(t, m.deletion.content, "reassigned")
			} else {
				require.Len(t, m.deletion.plan.Targets(), 1)
				require.Equal(t, deleteID, m.deletion.plan.Targets()[0].UUID)
			}
		})
	}
}

func TestDeletePolicyErrorAndReadyCancellation(t *testing.T) {
	m, st := deletionApp(t, true, deleteID)
	m = press(t, m, "a")
	m.resolveRememberDeletions = func() (bool, error) { return false, errors.New("invalid invocation configuration") }
	m, cmd := deleteStep(m, keyMsg("x"))
	require.Nil(t, cmd)
	require.Contains(t, m.status, "invalid invocation configuration")
	require.Equal(t, modeList, m.mode)
	require.Zero(t, st.prepares)
	remember := true
	m.resolveRememberDeletions = func() (bool, error) { return remember, nil }
	m = readyDeletion(t, m)
	require.Contains(t, m.deletion.content, "remember each UUID")
	m = press(t, m, "esc")
	require.Zero(t, st.applies)
	require.Equal(t, []string{deleteID}, m.list.deleteSelection().marked)
	_, err := st.GetSession(t.Context(), deleteID)
	require.NoError(t, err)
	remember = false
	m = readyDeletion(t, m)
	require.False(t, m.deletion.plan.RemembersDeletions())
	remember = true
	m, cmd = deleteStep(m, keyMsg("y"))
	m, _ = deleteStep(m, cmd())
	require.Contains(t, m.status, "deleted 1 session")
	archiveForDelete(t, st.VaultStore, deleteID)
	_, err = st.GetSession(t.Context(), deleteID)
	require.NoError(t, err, "the reviewed one-time policy is frozen at confirmation")
}

func TestDeleteCommitInvalidatesSearchDespiteListFailure(t *testing.T) {
	for _, result := range []string{"gone", "reimported", "search error"} {
		t.Run(result, func(t *testing.T) {
			m, st := deletionApp(t, false, deleteID, otherID)
			m = press(t, m, "/")
			m.search, _ = m.search.setQuery("needle")
			old := m.search.runSearch(m.search.seq)().(searchResultsMsg)
			require.Len(t, old.results, 2)
			m, _ = deleteStep(m, old)
			m = press(t, m, "esc", "m")
			m = readyDeletion(t, m)
			m, apply := deleteStep(m, keyMsg("y"))
			st.listErr = errors.New("list unavailable")
			m, refresh := deleteStep(m, apply())
			require.Contains(t, m.status, "deleted 1 session")
			require.Contains(t, m.status, "list unavailable")
			require.Empty(t, m.list.marks)
			require.Len(t, m.list.all, 1)
			require.Equal(t, otherID, m.list.all[0].UUID)
			require.Empty(t, m.search.results)
			require.Zero(t, m.search.cursor)
			require.Greater(t, m.search.seq, old.seq)
			require.NotNil(t, refresh)
			m = press(t, m, "/")
			m, _ = deleteStep(m, old)
			m, oldCmd := deleteStep(m, debounceMsg{old.seq})
			require.Nil(t, oldCmd)
			require.Empty(t, m.search.results)
			if result == "reimported" {
				archiveForDelete(t, st.VaultStore, deleteID)
			}
			if result == "search error" {
				st.searchErr = errors.New("search unavailable")
			}
			m, _ = deleteStep(m, refresh())
			switch result {
			case "gone":
				require.Len(t, m.search.results, 1)
				require.Equal(t, otherID, m.search.results[0].SessionUUID)
			case "reimported":
				require.Len(t, m.search.results, 2)
			case "search error":
				for _, part := range []string{"deleted 1 session", "list unavailable", "search unavailable"} {
					require.Contains(t, m.status, part)
				}
			}
			require.Equal(t, 1, st.applies)
		})
	}
}
