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

func TestApp_ProjectEditorEntry(t *testing.T) {
	for _, screen := range []string{"list", "finder", "search", "viewer", "detail"} {
		for _, state := range []string{"absent", "clear", "custom", "path-equal"} {
			t.Run(screen+"/"+state, func(t *testing.T) {
				m, st := newTestApp(t, Options{})
				switch screen {
				case "finder":
					m = press(t, m, "f")
				case "search":
					m = press(t, m, "/")
					m.search.input.SetValue("query")
					// Intentionally stale result: entry must read authoritative metadata.
					m.search.results = []vault.SearchResult{{SessionUUID: st.sessions[0].UUID, Project: "stale"}}
				case "viewer", "detail":
					m = press(t, m, "enter")
					if screen == "detail" {
						m = press(t, m, "]", "enter")
						require.True(t, m.viewer.inDetail())
					}
				}
				value := ""
				switch state {
				case "clear":
					st.sessions[0].ProjectOverride = &vault.SessionProject{}
				case "custom", "path-equal":
					value = "~/literal/project"
					if state == "path-equal" {
						value = st.sessions[0].ProjectPath
					}
					st.sessions[0].ProjectOverride = &vault.SessionProject{CustomProject: &value}
				}
				beforeMode := m.mode
				m = press(t, m, "ctrl+g")
				require.True(t, m.renaming)
				assert.Equal(t, editProject, m.editTarget)
				assert.Equal(t, st.sessions[0].UUID, m.renameUUID)
				assert.Equal(t, value, m.renameInput.Value())
				assert.Equal(t, st.sessions[0].ProjectPath, m.originalPath)
				assert.Contains(t, m.View(), "original path: "+st.sessions[0].ProjectPath)
				assert.Contains(t, m.View(), "project (empty clears)")
				assert.Equal(t, beforeMode, m.mode)
				assert.Equal(t, screen == "finder", m.list.filtering)
				m = press(t, m, "esc")
				assert.False(t, m.renaming)
				assert.Zero(t, st.projectCalls)
				assert.Equal(t, beforeMode, m.mode)
				if screen == "search" {
					assert.Equal(t, "query", m.search.input.Value())
				}
				if screen == "detail" {
					assert.True(t, m.viewer.inDetail())
				}
			})
		}
	}
}

func TestApp_ProjectEditorNoSelectionAndLookupError(t *testing.T) {
	for _, screen := range []string{"list", "search", "view", "raw"} {
		t.Run(screen, func(t *testing.T) {
			m, err := newModel(context.Background(), &stubStore{}, Options{})
			require.NoError(t, err)
			m.mode = map[string]mode{"list": modeList, "search": modeSearch, "view": modeView, "raw": modeRaw}[screen]
			m = press(t, m, "ctrl+g")
			assert.False(t, m.renaming)
			assert.Empty(t, m.status)
		})
	}
	m, st := twoProjectApp(t)
	st.getErr = errors.New("lookup unavailable")
	m = press(t, m, "ctrl+g")
	assert.False(t, m.renaming)
	assert.True(t, m.statusErr)
	assert.Contains(t, m.status, "opening project editor: lookup unavailable")
	assert.Zero(t, st.projectCalls)
}

func TestApp_ProjectEditorPreservesKeysAndPendingWrite(t *testing.T) {
	m, st := twoProjectApp(t)
	// ctrl+g must not replace an already open title editor.
	m = press(t, m, "e", "ctrl+g")
	require.Equal(t, editTitle, m.editTarget)
	assert.Equal(t, "alpha work", m.renameInput.Value())
	m = press(t, m, "esc", "ctrl+g")
	m = typeRunes(t, m, "egrRc/q")
	assert.Equal(t, "egrRc/q", m.renameInput.Value())
	m = press(t, m, "ctrl+e", "ctrl+g")
	assert.Equal(t, editProject, m.editTarget)
	assert.Equal(t, "egrRc/q", m.renameInput.Value())
	m, write := submitRename(t, m, "capy")
	require.True(t, m.renamePending)
	assert.Zero(t, st.projectCalls, "the write runs only when its command executes")
	for _, key := range []string{"enter", "esc", "ctrl+g", "e", "R"} {
		next, cmd := m.Update(keyMsg(key))
		m = next.(Model)
		assert.Nil(t, cmd)
		assert.True(t, m.renaming)
		assert.Equal(t, "capy", m.renameInput.Value())
	}
	next, quit := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	assert.True(t, next.(Model).quitting)
	require.NotNil(t, quit)
	assert.Equal(t, tea.Quit(), quit())
	result := renameResult(t, write)
	require.NoError(t, result.err)
	assert.Equal(t, editProject, result.target)
	assert.Equal(t, 1, st.projectCalls)
	assert.Zero(t, st.renameCalls)
	assert.Equal(t, vault.ProjectOptions{Name: "capy"}, st.lastProjectOpts)
}

func TestApp_ProjectEditorValidationAndClear(t *testing.T) {
	secret := "ghp_" + strings.Repeat("a", 36)
	for _, tc := range []struct {
		name, input string
		wantErr     bool
	}{
		{"label", "  capy  ", false},
		{"literal path", "~/capy", false},
		{"unicode boundary", strings.Repeat("界", 120), false},
		{"unicode over limit", strings.Repeat("界", 121), true},
		{"long input", strings.Repeat("x", 300), true},
		{"trim before limit", strings.Repeat(" ", 300) + "capy", false},
		{"redact before limit", strings.Repeat("a", 100) + secret, false},
		{"empty clear", "", false},
		{"whitespace clear", "  \u2003  ", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, st := twoProjectApp(t)
			title := "independent title"
			st.sessions[0].Name = &vault.SessionName{CustomTitle: &title}
			beforePath := st.sessions[0].ProjectPath
			m = press(t, m, "ctrl+g")
			// A multi-rune input event exercises textinput's real insertion limit.
			next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tc.input)})
			m = next.(Model)
			require.Equal(t, tc.input, m.renameInput.Value(), "no silent truncation")
			next, cmd := m.Update(keyMsg("enter"))
			m = next.(Model)
			result := renameResult(t, cmd)
			next, _ = m.Update(result)
			m = next.(Model)
			if tc.wantErr {
				require.Error(t, result.err)
				assert.True(t, m.renaming)
				assert.Equal(t, tc.input, m.renameInput.Value())
				assert.Contains(t, m.status, "project error:")
				assert.Contains(t, m.status, "must not exceed 120 characters")
				assert.Nil(t, st.sessions[0].ProjectOverride)
			} else {
				require.NoError(t, result.err)
				assert.False(t, m.renaming)
				require.NotNil(t, st.sessions[0].ProjectOverride)
				if strings.TrimSpace(tc.input) == "" {
					assert.Nil(t, st.sessions[0].ProjectOverride.CustomProject)
					assert.Equal(t, vault.ProjectOptions{Clear: true}, st.lastProjectOpts)
					assert.Contains(t, m.status, "cleared custom project")
					assert.Equal(t, beforePath, result.sess.EffectiveProject())
				} else {
					expected, err := vault.NormalizeSessionProject(tc.input)
					require.NoError(t, err)
					assert.Equal(t, expected, result.sess.EffectiveProject())
					selected, ok := m.list.selected()
					require.True(t, ok)
					assert.Equal(t, expected, selected.EffectiveProject())
				}
			}
			assert.Equal(t, title, st.sessions[0].EffectiveTitle())
			assert.Equal(t, beforePath, st.sessions[0].ProjectPath)
			assert.Nil(t, st.sessions[1].ProjectOverride)
			assert.Zero(t, st.renameCalls)
		})
	}
}

func TestApp_ProjectEditorWriteErrorAndCancellation(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%v", cancel), func(t *testing.T) {
			m, st := twoProjectApp(t)
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			m.ctx = ctx
			m = press(t, m, "ctrl+g")
			m, cmd := submitRename(t, m, "keep this text")
			if cancel {
				stop()
			} else {
				st.projectErr = errors.New("database unavailable")
			}
			result := renameResult(t, cmd)
			require.Error(t, result.err)
			if cancel {
				require.ErrorIs(t, result.err, context.Canceled)
			}
			next, _ := m.Update(result)
			m = next.(Model)
			assert.True(t, m.renaming)
			assert.False(t, m.renamePending)
			assert.Equal(t, "keep this text", m.renameInput.Value())
			assert.Contains(t, m.status, "project error:")
			m = press(t, m, "esc")
			assert.False(t, m.renaming)
		})
	}
}

func TestApp_ProjectEditorScopeRefreshAndStaleResults(t *testing.T) {
	for _, scope := range []string{"unrestricted", "project", "finder"} {
		t.Run(scope, func(t *testing.T) {
			m, st := twoProjectApp(t)
			label := "original-group"
			st.sessions[0].ProjectOverride = &vault.SessionProject{CustomProject: &label}
			st.sessions[1].ProjectOverride = &vault.SessionProject{CustomProject: &label}
			for i := range st.sessions {
				st.sessions[i].Platform = vault.PlatformCodex
			}
			m.list.platform, m.search.platform = vault.PlatformCodex, vault.PlatformCodex
			m.list.includeChildren = true
			if scope == "project" {
				m.list.project, m.search.project = label, label
			}
			var err error
			m, _, err = m.reloadSessions()
			require.NoError(t, err)
			if scope == "finder" {
				m = press(t, m, "f")
				m = typeRunes(t, m, label)
			}
			m.search.input.SetValue("needle")
			old := []vault.SearchResult{
				{SessionUUID: st.sessions[1].UUID, Project: label, Platform: vault.PlatformCodex, LineIndex: 1},
				{SessionUUID: st.sessions[0].UUID, Project: label, Platform: vault.PlatformCodex, LineIndex: 2},
			}
			m.search.results = old
			m.search.cursor = 1
			staleSeq := m.search.seq
			m = press(t, m, "ctrl+g")
			m, cmd := submitRename(t, m, "new-group")
			next, refresh := m.Update(renameResult(t, cmd))
			m = next.(Model)
			require.Greater(t, m.search.seq, staleSeq)
			assert.Equal(t, vault.PlatformCodex, st.lastListOpts.Platform)
			assert.True(t, st.lastListOpts.IncludeChildren)
			if scope != "unrestricted" {
				require.Len(t, m.list.list.Items(), 1, "reassigned session disappears from active scope")
				assert.Equal(t, st.sessions[1].UUID, m.list.list.Items()[0].(sessionItem).sess.UUID)
			}
			if scope == "project" {
				assert.Equal(t, label, st.lastListOpts.Project)
			}
			if scope == "finder" {
				assert.True(t, m.list.filtering)
				assert.Equal(t, label, m.list.applied)
				assert.Equal(t, label, m.list.filterValue())
			}
			st.results = append([]vault.SearchResult(nil), old...)
			st.results[1].Project = "new-group"
			for _, msg := range runCmds(refresh) {
				next, _ = m.Update(msg)
				m = next.(Model)
			}
			assert.Equal(t, "needle", st.lastSearchOpts.Query)
			assert.Equal(t, vault.PlatformCodex, st.lastSearchOpts.Platform)
			if scope == "project" {
				assert.Equal(t, label, st.lastSearchOpts.Project)
				require.Len(t, m.search.results, 1)
			} else {
				require.Len(t, m.search.results, 2)
				assert.Equal(t, 1, m.search.cursor, "retain the selected hit when still present")
				assert.Equal(t, "new-group", m.search.results[1].Project)
			}
			expected := append([]vault.SearchResult(nil), m.search.results...)
			next, staleCmd := m.Update(searchResultsMsg{seq: staleSeq, results: old})
			m = next.(Model)
			assert.Nil(t, staleCmd)
			assert.Equal(t, expected, m.search.results)
			_, staleCmd = m.Update(debounceMsg{seq: staleSeq})
			assert.Nil(t, staleCmd)
		})
	}
}

func TestApp_ProjectEditorRefreshFailures(t *testing.T) {
	for _, failure := range []string{"list", "search", "both"} {
		t.Run(failure, func(t *testing.T) {
			m, st := newTestApp(t, Options{})
			m = press(t, m, "enter", "ctrl+g")
			m.search.input.SetValue("needle")
			staleSeq := m.search.seq
			m, cmd := submitRename(t, m, "saved-project")
			result := renameResult(t, cmd)
			require.NoError(t, result.err)
			if failure != "search" {
				st.listErr = errors.New("list unavailable")
			}
			if failure != "list" {
				st.searchErr = errors.New("search unavailable")
			}
			next, refresh := m.Update(result)
			m = next.(Model)
			assert.False(t, m.renaming)
			assert.Equal(t, "saved-project", m.viewer.sess.EffectiveProject())
			require.Greater(t, m.search.seq, staleSeq)
			for _, msg := range runCmds(refresh) {
				next, _ = m.Update(msg)
				m = next.(Model)
			}
			assert.Equal(t, 1, st.searchCalls, "a list failure cannot prevent search refresh")
			assert.True(t, m.statusErr)
			assert.Contains(t, m.status, "saved, refresh failed:")
			assert.Equal(t, 1, st.projectCalls)
		})
	}
}

func TestApp_ProjectEditorChildAndDetailPreserveViewer(t *testing.T) {
	parent := codexParentSession(t, codexParentID, codexChildID, 40)
	child := codexChildSession(t, codexChildID, codexParentID)
	m, st := codexFamilyApp(t, parent, child)
	m = press(t, m, "enter", "]")
	savedOffset, savedMarker := m.viewer.vp.YOffset, m.viewer.focusedMarker
	m = press(t, m, "enter")
	raw, content := string(m.viewer.sess.RawJSONL), m.viewer.active.content()
	m = press(t, m, "ctrl+g")
	m, cmd := submitRename(t, m, "child-project")
	next, _ := m.Update(renameResult(t, cmd))
	m = next.(Model)
	assert.Equal(t, codexChildID, st.lastProjectID)
	assert.Nil(t, st.sessions[0].ProjectOverride, "parent is not edited")
	assert.Equal(t, "child-project", m.viewer.sess.EffectiveProject())
	assert.Equal(t, raw, string(m.viewer.sess.RawJSONL))
	assert.Equal(t, content, m.viewer.active.content())
	require.Len(t, m.viewerStack, 1)
	assert.Nil(t, m.viewerStack[0].viewer.sess.ProjectOverride)
	m = press(t, m, "esc")
	assert.Equal(t, codexParentID, m.viewer.sess.UUID)
	assert.Equal(t, savedOffset, m.viewer.vp.YOffset)
	assert.Equal(t, savedMarker, m.viewer.focusedMarker)

	m, _ = newTestApp(t, Options{})
	m = press(t, m, "enter", "]", "enter")
	require.True(t, m.viewer.inDetail())
	content = m.viewer.active.content()
	m = press(t, m, "ctrl+g")
	m, cmd = submitRename(t, m, "detail-project")
	next, _ = m.Update(renameResult(t, cmd))
	m = next.(Model)
	assert.True(t, m.viewer.inDetail())
	assert.Equal(t, content, m.viewer.active.content())
	assert.Equal(t, "detail-project", m.viewer.sess.EffectiveProject())
}

func TestApp_ProjectEditorLayoutAndHelp(t *testing.T) {
	m, st := newTestApp(t, Options{})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 240, Height: 24})
	m = next.(Model)
	assert.Contains(t, m.View(), "ctrl+g")
	m = press(t, m, "f")
	assert.Contains(t, m.View(), "ctrl+g project")
	m = press(t, m, "esc", "/")
	m.search.results = []vault.SearchResult{{SessionUUID: st.sessions[0].UUID}}
	assert.Contains(t, m.View(), "ctrl+g project")
	m = press(t, m, "enter")
	assert.Contains(t, m.View(), "ctrl+g project")
	m = press(t, m, "]", "enter")
	assert.Contains(t, m.View(), "ctrl+g project")
	m = press(t, m, "ctrl+g")
	m.renameInput.SetValue(strings.Repeat("界", 300))
	st.projectErr = errors.New("cannot save")
	m, cmd := submitRename(t, m, m.renameInput.Value())
	next, _ = m.Update(renameResult(t, cmd))
	m = next.(Model)
	for _, width := range []int{100, 60, 30, 10} {
		next, _ = m.Update(tea.WindowSizeMsg{Width: width, Height: 20})
		m = next.(Model)
		assert.Equal(t, strings.Repeat("界", 300), m.renameInput.Value())
		assert.LessOrEqual(t, displayWidth(m.renameLine()), width)
		rows := strings.Split(m.View(), "\n")
		assert.LessOrEqual(t, len(rows), 20, "editor, original path and error reserve rows")
		for _, row := range rows {
			assert.LessOrEqual(t, displayWidth(row), width)
		}
	}
}

func TestApp_ProjectEditorPreservesOffsetWithinMessage(t *testing.T) {
	sess := vault.Session{
		UUID: "tall00000000", Title: "tall", ProjectPath: "/original",
		RawJSONL: jsonlLines(t, assistantLine("m1", []map[string]any{
			textBlock(strings.Repeat("a line within one message\n", 80)),
		})),
	}
	st := &stubStore{sessions: []vault.Session{sess}}
	m, err := newModel(context.Background(), st, Options{Mode: "view", SessionID: sess.UUID})
	require.NoError(t, err)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m = next.(Model)
	m.viewer.vp.SetYOffset(10)
	require.Equal(t, 10, m.viewer.vp.YOffset)
	m = press(t, m, "ctrl+g")
	assert.Equal(t, 10, m.viewer.vp.YOffset, "opening editor preserves position within a message")
	m, cmd := submitRename(t, m, "assigned")
	next, _ = m.Update(renameResult(t, cmd))
	m = next.(Model)
	assert.Equal(t, 10, m.viewer.vp.YOffset, "save and status layout preserve scroll position")
	assert.Equal(t, "assigned", m.viewer.sess.EffectiveProject())
	m = press(t, m, "ctrl+g")
	assert.Equal(t, "assigned", m.renameInput.Value())
	m, cmd = submitRename(t, m, "  ")
	next, _ = m.Update(renameResult(t, cmd))
	m = next.(Model)
	assert.Equal(t, "/original", m.viewer.sess.EffectiveProject())
	assert.Equal(t, 10, m.viewer.vp.YOffset)
	m = press(t, m, "ctrl+g")
	assert.Empty(t, m.renameInput.Value(), "reopening a cleared assignment starts blank")
	m = press(t, m, "esc", "e")
	assert.Equal(t, editTitle, m.editTarget)
	assert.Equal(t, "tall", m.renameInput.Value())
	assert.Equal(t, renamePrompt, m.renameInput.Prompt)
}

func TestApp_ProjectEditorEmptyQueryInvalidatesSearch(t *testing.T) {
	m, st := twoProjectApp(t)
	m.search.seq = 7
	old := []vault.SearchResult{{SessionUUID: st.sessions[0].UUID, Project: "old"}}
	m.search.results = old
	m = press(t, m, "ctrl+g")
	m, cmd := submitRename(t, m, "new")
	next, refresh := m.Update(renameResult(t, cmd))
	m = next.(Model)
	runCmds(refresh)
	assert.Zero(t, st.searchCalls)
	assert.Greater(t, m.search.seq, 7)
	assert.Empty(t, m.search.results)
	next, _ = m.Update(searchResultsMsg{seq: 7, results: old})
	assert.Empty(t, next.(Model).search.results)
}

func TestApp_ProjectEditorUnicodeSaveStatusFitsWidth(t *testing.T) {
	m, _ := newTestApp(t, Options{})
	m = press(t, m, "enter", "ctrl+g")
	m, cmd := submitRename(t, m, strings.Repeat("界", 120))
	result := renameResult(t, cmd)
	require.NoError(t, result.err)
	next, _ := m.Update(result)
	m = next.(Model)
	for _, width := range []int{80, 60, 30} {
		next, _ = m.Update(tea.WindowSizeMsg{Width: width, Height: 20})
		m = next.(Model)
		rows := strings.Split(m.View(), "\n")
		assert.LessOrEqual(t, len(rows), 20)
		for _, row := range rows {
			assert.LessOrEqual(t, displayWidth(row), width)
		}
	}
}
