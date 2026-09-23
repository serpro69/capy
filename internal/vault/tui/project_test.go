package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApp_ProjectScopePersistence(t *testing.T) {
	for _, startMode := range []string{"list", "search"} {
		t.Run(startMode, func(t *testing.T) {
			label := "capy%_\\'"
			parent := codexSession(t, codexParentID, "", "Parent", codexHumanLine("needle"))
			parent.ProjectOverride = &vault.SessionProject{CustomProject: &label}
			child := codexChildSession(t, codexChildID, parent.UUID)
			child.ProjectOverride = &vault.SessionProject{CustomProject: &label}
			other := codexSession(t, codexGrandchildID, "", "Other", codexHumanLine("needle"))
			// A replaced path is not an alias for a project's new label.
			other.ProjectPath = "/tmp/" + label
			otherLabel := "unrelated"
			other.ProjectOverride = &vault.SessionProject{CustomProject: &otherLabel}
			claude, files := sampleSession(t)
			claude.ProjectOverride = &vault.SessionProject{CustomProject: &label}
			st := &stubStore{
				sessions: []vault.Session{parent, child, other, claude},
				files:    map[string][]vault.File{claude.UUID: files},
			}
			for _, sess := range st.sessions {
				st.results = append(st.results, vault.SearchResult{
					SessionUUID: sess.UUID, Platform: sess.Platform, ParentUUID: sess.ParentUUID,
					Project: sess.EffectiveProject(), ProjectPath: sess.ProjectPath, Title: sess.Title,
				})
			}
			m, err := newModel(context.Background(), st, Options{
				Mode: startMode, Query: "needle", Project: label, Platform: vault.PlatformCodex,
			})
			require.NoError(t, err)
			next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
			m = next.(Model)
			checkList := func(children bool) {
				t.Helper()
				assert.Equal(t, vault.ListOptions{Project: label, Platform: vault.PlatformCodex, IncludeChildren: children}, st.lastListOpts)
				want := 1
				if children {
					want++
				}
				require.Len(t, m.list.list.Items(), want)
				assert.Equal(t, parent.UUID, m.list.list.Items()[0].(sessionItem).sess.UUID)
			}
			checkSearch := func() {
				t.Helper()
				next, cmd := m.Update(debounceMsg{seq: m.search.seq})
				m = next.(Model)
				require.NotNil(t, cmd)
				next, _ = m.Update(cmd())
				m = next.(Model)
				assert.Equal(t, label, st.lastSearchOpts.Project)
				assert.Empty(t, st.lastSearchOpts.ProjectPath)
				assert.Equal(t, vault.PlatformCodex, st.lastSearchOpts.Platform)
				require.Len(t, m.search.results, 2, "search includes the matching child")
				assert.Equal(t, parent.UUID, m.search.results[0].SessionUUID)
				assert.Equal(t, child.UUID, m.search.results[1].SessionUUID)
			}
			press := func(key string) {
				t.Helper()
				next, _ := m.Update(keyMsg(key))
				m = next.(Model)
			}
			checkList(false)
			if startMode == "search" {
				require.NotNil(t, m.Init())
				checkSearch()
				press("esc")
			}
			press("f") // filter entry re-reads the scoped snapshot
			checkList(false)
			m = typeRunes(t, m, "Parent")
			press("enter")
			m, _, err = m.reloadSessions()
			require.NoError(t, err)
			checkList(false)
			assert.Equal(t, "Parent", m.list.applied)
			press("f")
			press("esc") // clearing the local finder must retain SQL scope
			press(listChildrenKey)
			checkList(true)
			press("/")
			require.Equal(t, modeSearch, m.mode)
			m = typeRunes(t, m, " another")
			checkSearch()
			press("enter")
			require.Equal(t, modeView, m.mode)
			assert.Contains(t, m.viewer.header(), label)
			press("esc")
			require.Equal(t, modeSearch, m.mode)
			m = typeRunes(t, m, " query")
			checkSearch()
			press("esc")
			require.Equal(t, modeList, m.mode)
			press(listChildrenKey)
			checkList(false)
		})
	}
}

func TestFilterSessions_EffectiveProject(t *testing.T) {
	label := "Équipe%_\\'"
	sessions := []vault.Session{{
		UUID: "abcdef012345", Title: "Independent title", ProjectPath: "/tmp/replaced-checkout",
		ProjectOverride: &vault.SessionProject{CustomProject: &label},
	}}
	for _, tc := range []struct {
		name, needle string
		want         int
	}{
		{"custom Unicode folded", "éQUIPE", 1},
		{"literal punctuation", "%_\\'", 1},
		{"replaced path", "replaced-checkout", 0},
		{"title", "independent", 1},
		{"UUID", "ABCDEF", 1},
		{"empty", "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Len(t, filterSessions(sessions, tc.needle), tc.want)
		})
	}
	item := sessionItem{sess: sessions[0]}
	assert.Contains(t, item.FilterValue(), label)
	assert.NotContains(t, item.FilterValue(), "replaced-checkout")
	// The raw path can still match an independent field, such as the title.
	sessions[0].Title = "Discuss replaced-checkout"
	assert.Len(t, filterSessions(sessions, "replaced-checkout"), 1)
	sessions[0].Title = "Independent title"
	sessions[0].ProjectOverride = &vault.SessionProject{}
	assert.Len(t, filterSessions(sessions, "replaced-checkout"), 1, "clear restores the path operand")
}

func TestProjectPresentation(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	custom := "/home/u/label"
	path := "/home/u/proj"
	for _, tc := range []struct {
		name     string
		state    *vault.SessionProject
		want     string
		original bool
	}{
		{"unset", nil, "~/proj", false},
		{"clear", &vault.SessionProject{}, "~/proj", false},
		{"custom path", &vault.SessionProject{CustomProject: &custom}, custom, true},
		{"custom equals path", &vault.SessionProject{CustomProject: &path}, path, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess, files := sampleSession(t)
			sess.ProjectOverride = tc.state
			assert.Contains(t, (sessionItem{sess: sess}).Description(), " · "+tc.want)
			r := vault.SearchResult{Project: sess.EffectiveProject(), ProjectPath: sess.ProjectPath, Title: sess.Title, Snippet: "needle"}
			if tc.state != nil {
				r.CustomProject = tc.state.CustomProject
			}
			search := newSearchModel(context.Background(), &stubStore{}, DefaultStyles(), 160, 24)
			for _, selected := range []bool{false, true} {
				assert.Contains(t, search.resultRow(r, selected), tc.want)
			}
			v := newViewerModel(DefaultStyles(), 160, 24).loadSession(sess, files)
			assert.Contains(t, v.header(), " · "+tc.want)
			if tc.original {
				assert.Contains(t, v.View(), "Original path: ~/proj")
				assert.NotContains(t, v.header(), "~/proj")
			} else {
				assert.NotContains(t, v.View(), "Original path:")
			}
			assert.LessOrEqual(t, len(strings.Split(v.View(), "\n")), 24)
		})
	}
}

func TestProjectPresentation_NarrowTerminal(t *testing.T) {
	for _, project := range []string{strings.Repeat("long-label-", 12), strings.Repeat("项目", 60)} {
		for _, override := range []bool{false, true} {
			for _, width := range []int{20, 40, 80} {
				t.Run(fmt.Sprintf("%s/custom=%v/width=%d", project[:6], override, width), func(t *testing.T) {
					sess, files := sampleSession(t)
					sess.ProjectPath = "/tmp/" + project
					if override {
						sess.ProjectOverride = &vault.SessionProject{CustomProject: &project}
					}
					v := newViewerModel(DefaultStyles(), 160, 12).loadSession(sess, files).setSize(width, 12)
					assert.LessOrEqual(t, displayWidth(v.header()), width)
					assert.LessOrEqual(t, displayWidth(v.View()), width)
					assert.LessOrEqual(t, len(strings.Split(v.View(), "\n")), 12)
					r := vault.SearchResult{Project: sess.EffectiveProject(), ProjectPath: sess.ProjectPath, Title: "title", Snippet: "needle"}
					if override {
						r.CustomProject = &project
					}
					search := newSearchModel(context.Background(), &stubStore{}, DefaultStyles(), width, 12)
					for _, selected := range []bool{false, true} {
						assert.LessOrEqual(t, displayWidth(search.resultRow(r, selected)), width)
					}
					// Metadata refresh changes header height without reparsing or losing
					// the open subagent's navigation state or archived bytes.
					v = v.openSubagent("xyz", 1)
					active := v.active.content()
					sess.ProjectOverride = &vault.SessionProject{}
					sess.RawJSONL = nil
					v = v.setSessionMeta(sess)
					assert.True(t, v.inSub)
					assert.Equal(t, active, v.active.content())
					assert.NotEmpty(t, v.sess.RawJSONL)
					assert.NotContains(t, v.View(), "Original path:")
					assert.LessOrEqual(t, len(strings.Split(v.View(), "\n")), 12)
				})
			}
		}
	}
}
