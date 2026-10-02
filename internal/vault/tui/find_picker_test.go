package tui

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/cursor"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindPicker(t *testing.T) {
	var lines []string
	for i := range 80 {
		lines = append(lines, fmt.Sprintf("row %02d 界é 👨‍👩‍👧", i))
	}
	v := findTestViewer(t, []vault.TranscriptMessage{{Role: vault.RoleAssistant, Body: strings.Join(lines, "\n")}}, 70, 10)
	v = findKey(t, v, "ctrl+f")
	require.True(t, v.find.picking)
	require.Len(t, v.find.picker.results.hits, 80)
	for i := range 80 {
		assert.Equal(t, i, v.find.picker.cursor)
		assert.Contains(t, ansi.Strip(v.View()), fmt.Sprintf("%d/80", i+1))
		assert.Contains(t, ansi.Strip(v.View()), "> assistant")
		assert.Contains(t, ansi.Strip(v.View()), fmt.Sprintf("row %02d", i))
		v = findKey(t, v, "ctrl+n")
	}
	v = findKey(t, v, "pgup")
	assert.Equal(t, 72, v.find.picker.cursor)
	v = findKey(t, v, "pgdown")
	assert.Equal(t, 79, v.find.picker.cursor)
	v = findKey(t, v, "ctrl+p")
	assert.Equal(t, 78, v.find.picker.cursor)
	for _, size := range [][2]int{{35, 5}, {2, 1}, {1, 2}, {6, 3}, {80, 12}} {
		v = v.setSize(size[0], size[1])
		var cmd tea.Cmd
		v, cmd = v.nextFindCommand()
		v = settleViewerFind(t, v, cmd)
		assert.Equal(t, 78, v.find.picker.cursor)
		rows := strings.Split(v.View(), "\n")
		assert.LessOrEqual(t, len(rows), size[1])
		for _, row := range rows {
			assert.LessOrEqual(t, ansi.StringWidth(row), size[0])
			assert.True(t, utf8.ValidString(row))
		}
	}
	v = findKey(t, v, "enter")
	assert.Contains(t, ansi.Strip(v.View()), "row 78")
	assert.True(t, v.find.view.fuzzy)
	assert.Empty(t, v.find.view.query)
	assert.Equal(t, len(strings.Join(lines[:78], "\n"))+1, selectedFindPosition(t, v).offset)
}

func TestFindPickerSnippet(t *testing.T) {
	text := strings.Repeat("left ", 300) + "界é👨‍👩‍👧 needle" + strings.Repeat(" after", 300) + "z"
	c, err := buildFindCorpus(t.Context(), "snippet", []vault.TranscriptMessage{{Body: text}})
	require.NoError(t, err)
	hits, err := findFuzzy(t.Context(), c, "界éz")
	require.NoError(t, err)
	assert.Empty(t, hits, "matching doesn't normalize the combining accent")
	hits, err = findFuzzy(t.Context(), c, "界ez")
	require.NoError(t, err)
	require.Len(t, hits, 1)
	for width := 1; width <= 70; width++ {
		snippet, err := findPickerSnippet(t.Context(), c.lines[0], hits[0], width, DefaultStyles())
		require.NoError(t, err)
		assert.LessOrEqual(t, ansi.StringWidth(snippet), width)
		assert.True(t, utf8.ValidString(snippet))
		if width > 24 {
			assert.Contains(t, ansi.Strip(snippet), "界é👨‍👩‍👧")
			assert.True(t, strings.HasPrefix(ansi.Strip(snippet), "…"))
			assert.True(t, strings.HasSuffix(ansi.Strip(snippet), "…"))
		}
	}
	text = "\x1b[31m a\x00b\xff"
	c, err = buildFindCorpus(t.Context(), "controls", []vault.TranscriptMessage{{Body: text}})
	require.NoError(t, err)
	hits, err = findFuzzy(t.Context(), c, "ab")
	require.NoError(t, err)
	require.Len(t, hits, 1)
	snippet, err := findPickerSnippet(t.Context(), c.lines[0], hits[0], 60, DefaultStyles())
	require.NoError(t, err)
	assert.Contains(t, ansi.Strip(snippet), `a\u0000b\xff`)
	assert.NotContains(t, snippet, "\x1b[31m", "transcript ANSI is escaped")
}

func TestAppFindPicker(t *testing.T) {
	t.Run("cleared search detail gets its own projection", func(t *testing.T) {
		v := searchViewer(t, findTestViewer(t, collapsedFindMessages(), 65, 8), "hit")
		v = findKey(t, v, "n")
		v = findKey(t, v, "esc")
		require.False(t, v.target.searchSelected)
		require.Equal(t, viewerTargetTool, v.target.kind)
		v = findKey(t, v, "ctrl+f")
		v = findKey(t, v, "hba")
		v = findKey(t, v, "enter")
		assert.Contains(t, ansi.Strip(v.View()), "[hit body A]")
		assert.False(t, v.target.searchSelected)
		assert.Equal(t, v.target.scope, v.find.view.corpus.scope)
		assert.Len(t, v.parents, 1)
		assert.Len(t, v.active.messages, 1)
	})
	for _, platform := range []vault.Platform{vault.PlatformClaudeCode, vault.PlatformCodex} {
		for _, tc := range []struct {
			name, query, bracket string
			message              int
			field                findField
		}{
			{"ordinary", "vif", "[visible f", 0, findBody},
			{"hidden", "hba", "[hit body A]", 1, findBody},
			{"summary", "bhc", "[Bash hit c", 2, findSummary},
		} {
			t.Run(string(platform)+"/"+tc.name, func(t *testing.T) {
				m, st := newTestApp(t, Options{Mode: "view", SessionID: "abcdef01"})
				m.viewer = findTestViewer(t, collapsedFindMessages(), 80, 9)
				m.viewer.target.source.platform = platform
				m.viewer = searchViewer(t, m.viewer, "hit")
				m = appFindKey(t, m, "n") // picker must retain original owner scope
				require.True(t, m.viewer.target.searchSelected)
				before := m.viewer.View()
				m = appFindKey(t, m, "ctrl+f")
				m = appFindKey(t, m, tc.query)
				require.NotEmpty(t, m.viewer.find.picker.results.hits)
				m = appFindKey(t, m, "esc")
				assert.Equal(t, before, m.viewer.View())
				m = appFindKey(t, m, "ctrl+f")
				m = appFindKey(t, m, tc.query)
				m = appFindKey(t, m, "enter")
				require.True(t, m.viewer.find.view.fuzzy)
				assert.Empty(t, m.viewer.find.view.query)
				position := selectedFindPosition(t, m.viewer)
				assert.Equal(t, tc.message, position.message)
				assert.Equal(t, tc.field, position.field)
				requireFindLanding(t, m.viewer, position)
				assert.Contains(t, ansi.Strip(m.viewer.View()), tc.bracket)
				m = appFindResize(t, m, 24, 9)
				requireFindLanding(t, m.viewer, position)
				if m.viewer.target.searchSelected {
					assert.Empty(t, m.viewer.parents[len(m.viewer.parents)-1].find.view.query)
					m = appFindKey(t, m, "esc")
					assert.Equal(t, viewerTargetTool, m.viewer.target.kind)
					m = appFindKey(t, m, "esc")
					assert.Equal(t, viewerTargetMain, m.viewer.target.kind)
					assert.Empty(t, m.viewer.find.view.query)
				}
				assert.Zero(t, st.searchCalls)
				assert.Zero(t, st.renameCalls)
				assert.Zero(t, st.projectCalls)
			})
		}
	}
	t.Run("nested sidecar keeps separately suspended main query", func(t *testing.T) {
		m, _ := newTestApp(t, Options{Mode: "view", SessionID: "abcdef01"})
		m.viewer = searchViewer(t, nestedTargetViewer(t), "answer")
		m = appFindKey(t, m, "]")
		m = appFindKey(t, m, "enter")
		m = appFindKey(t, m, "ctrl+f")
		m = appFindKey(t, m, "bnnn")
		m = appFindKey(t, m, "enter")
		assert.True(t, m.viewer.target.searchSelected)
		assert.Equal(t, "xyz", m.viewer.target.source.subagent)
		assert.Len(t, m.viewer.parents, 2)
		m = appFindKey(t, m, "q")
		assert.Equal(t, viewerTargetSidecar, m.viewer.target.kind)
		assert.Empty(t, m.viewer.find.view.query)
		m = appFindKey(t, m, "q")
		assert.Equal(t, "answer", m.viewer.find.view.query)
	})
	t.Run("accepted span brackets and marker navigation", func(t *testing.T) {
		v := findTestViewer(t, []vault.TranscriptMessage{
			{Body: "first α---界---Z last"},
			{Role: vault.RoleTool, Collapsed: true, ToolSummary: "Read file", Body: "data"},
		}, 70, 9)
		v = findKey(t, v, "ctrl+f")
		v = findKey(t, v, "Α界z")
		v = findKey(t, v, "enter")
		assert.Contains(t, ansi.Strip(v.View()), "first [α---界---Z] last")
		v = findKey(t, v, "n")
		assert.Equal(t, 0, v.focusedMarker)
		v = findKey(t, v, "N")
		assert.Equal(t, 0, v.focusedMarker)
		v = resizeTargetViewer(t, v, 13)
		assert.Contains(t, ansi.Strip(v.View()), "[")
		v = findKey(t, v, "esc")
		assert.False(t, v.find.view.plain)
	})
	t.Run("wrapped accepted span brackets without ANSI", func(t *testing.T) {
		v := findTestViewer(t, []vault.TranscriptMessage{{Body: "a-b-c"}}, 6, 8)
		v = findKey(t, v, "ctrl+f")
		v = findKey(t, v, "abc")
		v = findKey(t, v, "enter")
		plain := ansi.Strip(v.View())
		assert.Contains(t, plain, "[a-b]\n[-c]")
	})
}

func TestAppFindRoutingPicker(t *testing.T) {
	m, st := newTestApp(t, Options{Mode: "view", SessionID: "abcdef01"})
	var clipboard bytes.Buffer
	m.clipOut = &clipboard
	original := bytes.Clone(m.viewer.sess.RawJSONL)
	m = appFindKey(t, m, "ctrl+f")
	for _, key := range []string{"r", "R", "e", "c", "v", "q", "n", "N", "j", "k", "g", "G", "b", " ", "/", "[", "]", "ctrl+g"} {
		m = appFindKey(t, m, key)
		assert.Equal(t, modeView, m.mode)
		assert.False(t, m.renaming)
		assert.False(t, m.quitting)
		assert.Equal(t, ActionNone, m.action.Kind)
	}
	assert.Equal(t, "rRecvqnNjkgGb /[]", m.viewer.find.input.Value())
	assert.Zero(t, st.searchCalls)
	assert.Zero(t, st.renameCalls)
	assert.Zero(t, st.projectCalls)
	assert.Empty(t, clipboard.String())
	assert.Equal(t, original, st.sessions[0].RawJSONL)
	next, cmd := m.Update(keyMsg("ctrl+c"))
	assert.True(t, next.(Model).quitting)
	require.NotNil(t, cmd)
}

func TestViewerFindFuzzyCompletion(t *testing.T) {
	t.Run("pending clear picker acceptance stays snapshot safe", func(t *testing.T) {
		v := searchViewer(t, findTestViewer(t, collapsedFindMessages(), 65, 8), "hit")
		v = findKey(t, v, "n")
		v, clearing, _ := v.Update(keyMsg("esc"))
		v, _, _ = v.Update(keyMsg("ctrl+f"))
		v = settleViewerFind(t, v, clearing)
		v = findKey(t, v, "hba")
		v, accepting, _ := v.Update(keyMsg("enter"))
		v, _, _ = v.Update(keyMsg("/"))
		v = settleViewerFind(t, v, accepting)
		assert.True(t, v.find.editing)
		assert.NotPanics(t, func() { _ = v.View() })
	})
	t.Run("escape clears pending acceptance before back", func(t *testing.T) {
		for _, detail := range []bool{false, true} {
			t.Run(fmt.Sprint(detail), func(t *testing.T) {
				v := findTestViewer(t, collapsedFindMessages(), 65, 8)
				if detail {
					v = v.openInlineContent(1)
				}
				original := v.target
				v = findKey(t, v, "ctrl+f")
				v = findKey(t, v, "hba")
				v, held, _ := v.Update(keyMsg("enter"))
				v, _, action := v.Update(keyMsg("esc"))
				assert.Equal(t, viewerNone, action)
				v = settleViewerFind(t, v, held)
				assert.Equal(t, original, v.target)
				assert.False(t, v.find.view.plain)
				assert.Empty(t, v.find.view.query)
			})
		}
	})
	t.Run("marker key during pending acceptance", func(t *testing.T) {
		v := searchViewer(t, findTestViewer(t, collapsedFindMessages(), 65, 8), "hit")
		v = findKey(t, v, "ctrl+f")
		v = findKey(t, v, "hba")
		v, held, _ := v.Update(keyMsg("enter"))
		v, _, _ = v.Update(keyMsg("n"))
		v = settleViewerFind(t, v, held)
		assert.Equal(t, 0, v.find.view.selected, "rune spans are not navigable occurrences")
		assert.Empty(t, v.find.view.query)
		assert.Contains(t, ansi.Strip(v.View()), "[hit body A]")
	})
	t.Run("resize pending acceptance", func(t *testing.T) {
		for _, exact := range []bool{false, true} {
			t.Run(fmt.Sprint(exact), func(t *testing.T) {
				v := findTestViewer(t, collapsedFindMessages(), 65, 8)
				if exact {
					v = searchViewer(t, v, "hit")
				}
				v = findKey(t, v, "ctrl+f")
				v = findKey(t, v, "hba")
				v, held, _ := v.Update(keyMsg("enter"))
				v = v.setSize(30, 8)
				v = settleViewerFind(t, v, held)
				require.True(t, v.find.view.fuzzy)
				require.True(t, v.target.searchSelected)
				assert.Empty(t, v.find.view.query)
				assert.Contains(t, ansi.Strip(v.View()), "[hit body A]")
			})
		}
	})
	for _, query := range []string{"ab", strings.Repeat("a", 256), strings.Repeat("界", 256)} {
		t.Run(fmt.Sprintf("%d/%c", utf8.RuneCountInString(query), []rune(query)[0]), func(t *testing.T) {
			v := findTestViewer(t, []vault.TranscriptMessage{{Body: "\x00" + query + "\x00\nnext"}}, 40, 8)
			v = findKey(t, v, "ctrl+f")
			v, stale, _ := v.Update(keyMsg("wrong"))
			v, _, _ = v.Update(keyMsg("ctrl+u"))
			v, _, _ = v.Update(keyMsg(query))
			v, _, _ = v.Update(keyMsg("enter"))
			assert.True(t, v.find.picking, "pending Enter never accepts an older row")
			assert.NotContains(t, ansi.Strip(v.View()), "> system")
			v = settleViewerFind(t, v, stale)
			assert.Equal(t, findWorkID{}, v.find.running)
			assert.Nil(t, v.find.pending)
			require.Len(t, v.find.picker.results.hits, 1)
			assert.Len(t, v.find.picker.results.hits[0].spans, utf8.RuneCountInString(query))
			v = findKey(t, v, "ctrl+u")
			v = findKey(t, v, "next")
			v = findKey(t, v, "enter")
			assert.Contains(t, ansi.Strip(v.View()), "[next]")
			assert.Equal(t, findWorkID{}, v.find.running)
		})
	}
	t.Run("cancel stale completion after resize", func(t *testing.T) {
		v := searchViewer(t, findTestViewer(t, collapsedFindMessages(), 65, 8), "hit")
		v = findKey(t, v, "n")
		position := selectedFindPosition(t, v)
		v = findKey(t, v, "ctrl+f")
		v, stale, _ := v.Update(keyMsg("body B"))
		v = v.setSize(30, 8)
		v, _, _ = v.Update(keyMsg("esc"))
		v = settleViewerFind(t, v, stale)
		requireFindLanding(t, v, position)
		assert.Equal(t, "hit", v.find.view.query)
		assert.False(t, v.find.picking)
		assert.Equal(t, findWorkID{}, v.find.running)
	})
	t.Run("query limit and empty set", func(t *testing.T) {
		v := findKey(t, loadedViewer(t), "ctrl+f")
		v.find.input.Cursor.SetMode(cursor.CursorStatic)
		v = findKey(t, v, strings.Repeat("界", 300))
		assert.Equal(t, 256, utf8.RuneCountInString(v.find.input.Value()))
		v = findKey(t, v, "enter")
		assert.True(t, v.find.picking)
		assert.Contains(t, ansi.Strip(v.View()), "no matches")
	})
}
