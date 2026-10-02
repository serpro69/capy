package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func collapsedFindMessages() []vault.TranscriptMessage {
	return []vault.TranscriptMessage{
		{Role: vault.RoleAssistant, SourceLine: 1, Body: "hit visible first"},
		{Role: vault.RoleTool, SourceLine: 2, Collapsed: true, ToolSummary: "Read /a",
			Body: strings.Repeat("earlier output\n", 100) + "hit body A\n" + strings.Repeat("later output\n", 20)},
		{Role: vault.RoleTool, SourceLine: 2, Collapsed: true, ToolSummary: "Bash hit command",
			Body: strings.Repeat("wrapped context ", 80) + "hit body B\n" + strings.Repeat("tail\n", 20)},
		{Role: vault.RoleAssistant, SourceLine: 3, Body: "hit visible last"},
	}
}

func requireFindLanding(t *testing.T, v viewerModel, position findPosition) {
	t.Helper()
	f := v.find.view
	require.GreaterOrEqual(t, f.selected, 0)
	require.Less(t, f.selected, len(f.hits))
	hit := f.hits[f.selected]
	require.Equal(t, position, f.corpus.position(hit))
	row := f.projection.rowForHit(hit)
	require.GreaterOrEqual(t, row, v.vp.YOffset)
	require.Less(t, row, v.vp.YOffset+v.vp.Height, "first matched character must be visible")
	mapped := f.projection.rows[row]
	assert.Equal(t, hit.line, mapped.line)
	assert.LessOrEqual(t, mapped.start, hit.start)
	assert.Greater(t, mapped.end, hit.start)
	assert.Contains(t, ansi.Strip(v.View()), "[", "selection remains visible without ANSI")
}

func TestViewerFindCollapsed(t *testing.T) {
	messages := collapsedFindMessages()
	want := []findPosition{
		{0, findBody, 0}, {1, findBody, len(strings.Repeat("earlier output\n", 100))},
		{2, findSummary, 5}, {2, findBody, len(strings.Repeat("wrapped context ", 80))}, {3, findBody, 0},
	}
	for _, platform := range []vault.Platform{vault.PlatformClaudeCode, vault.PlatformCodex} {
		t.Run(string(platform), func(t *testing.T) {
			v := findTestViewer(t, messages, 70, 8)
			v.target.source.platform = platform
			owner := v.target
			v = searchViewer(t, v, "hit")
			corpus := v.find.view.corpus
			for cycle := range 3 {
				for i, position := range want {
					require.Len(t, v.find.view.hits, len(want))
					requireFindLanding(t, v, position)
					assert.Same(t, corpus, v.find.view.corpus)
					assert.Equal(t, owner.scope, corpus.scope)
					assert.Contains(t, v.findCounter(), fmt.Sprintf("%d/5", i+1))
					if messages[position.message].Collapsed {
						require.Len(t, v.parents, 1, "one owner frame across all hidden hits")
						assert.True(t, v.target.searchSelected)
						assert.Equal(t, position, v.target.origin)
						assert.Equal(t, platform, v.activePlatform())
						msg, ok := v.currentMessage()
						require.True(t, ok)
						assert.Equal(t, messages[position.message].Body, msg.Body)
					} else {
						assert.Equal(t, owner, v.target)
						assert.Empty(t, v.parents)
						assert.Zero(t, cap(v.parents))
					}
					v = findKey(t, v, "n")
				}
				assert.True(t, v.find.wrapped, "cycle %d", cycle)
			}
			v = findKey(t, v, "N")
			requireFindLanding(t, v, want[4])
			v = findKey(t, v, "N")
			requireFindLanding(t, v, want[3])
			v = resizeTargetViewer(t, v, 24)
			requireFindLanding(t, v, want[3])
		})
	}

	t.Run("summary beyond truncated header", func(t *testing.T) {
		messages := []vault.TranscriptMessage{{Role: vault.RoleTool, Collapsed: true,
			ToolSummary: "Bash " + strings.Repeat("argument ", 120) + "summary-only", Body: "unchanged body"}}
		v := searchViewer(t, findTestViewer(t, messages, 40, 8), "summary-only")
		requireFindLanding(t, v, findPosition{0, findSummary, strings.Index(messages[0].ToolSummary, "summary-only")})
		assert.True(t, v.target.searchSelected)
		assert.Len(t, v.find.view.hits, 1)
		assert.Contains(t, ansi.Strip(v.View()), "[summary-only]")
	})

	t.Run("sidecar owner stays isolated", func(t *testing.T) {
		v := searchViewer(t, nestedTargetViewer(t), "answer")
		mainHit := v.find.view.hits[v.find.view.selected]
		v = v.openSubagent("xyz", 0)
		owner := v.target
		v = searchViewer(t, v, "body needle")
		require.True(t, v.target.searchSelected)
		assert.Equal(t, "xyz", v.target.source.subagent)
		assert.Equal(t, owner.scope, v.find.view.corpus.scope)
		require.Len(t, v.parents, 2)
		v = findKey(t, v, "q")
		assert.Equal(t, owner, v.target)
		assert.Empty(t, v.find.view.query)
		v = findKey(t, v, "q")
		assert.Equal(t, "answer", v.find.view.query)
		assert.Equal(t, mainHit, v.find.view.hits[v.find.view.selected])
		assert.Empty(t, v.parents)
	})
}

func TestViewerFindDiff(t *testing.T) {
	raw := jsonlLines(t,
		assistantLine("edit", []map[string]any{{"type": "tool_use", "id": "t1", "name": "Edit", "input": map[string]any{"file_path": "/p/file"}}}),
		editDiffLine("/p/file", []map[string]any{{"oldStart": 10, "oldLines": 1, "newStart": 10, "newLines": 1,
			"lines": []string{"-old needle", "+new needle"}}}),
	)
	v := newViewerModel(DefaultStyles(), 45, 8).loadSession(vault.Session{UUID: "diff", RawJSONL: raw}, nil)
	v = searchViewer(t, v, "needle")
	require.True(t, v.target.searchSelected)
	require.Len(t, v.find.view.hits, 2)
	assert.True(t, v.active.messages[0].Diff)
	assert.Contains(t, ansi.Strip(v.View()), "-old [needle]")
	f := v.find.view
	li := f.hits[0].line
	row := f.projection.lineRows[li]
	assert.Equal(t, v.styles.DiffDel.Render("-old needle"), f.projection.highlightRow(f.corpus, row, nil, -1, v.styles))
	v = findKey(t, v, "n")
	assert.Contains(t, ansi.Strip(v.View()), "+new [needle]")
	f = v.find.view
	li = f.hits[1].line
	row = f.projection.lineRows[li]
	assert.Equal(t, v.styles.DiffAdd.Render("+new needle"), f.projection.highlightRow(f.corpus, row, nil, -1, v.styles))
}

func TestViewerFindCollapsedCancelClear(t *testing.T) {
	for _, committed := range []bool{false, true} {
		for _, resize := range []bool{false, true} {
			t.Run(fmt.Sprintf("cancel/committed=%v/resize=%v", committed, resize), func(t *testing.T) {
				v := findTestViewer(t, collapsedFindMessages(), 70, 8)
				if committed {
					v = searchViewer(t, v, "hit")
					v = findKey(t, v, "n")
					v = findKey(t, v, "j") // reading position is independent of selection
				}
				target, reading, before := v.target, v.findReadingAnchor(), v.View()
				selection, depth := v.find.view.selected, len(v.parents)
				v = findKey(t, v, "/")
				v = findKey(t, v, "ctrl+u")
				assert.Equal(t, target, v.target, "empty draft previews the pre-edit target")
				v = findKey(t, v, "body B")
				require.True(t, v.target.searchSelected)
				if resize {
					v = resizeTargetViewer(t, v, 33)
				}
				v = findKey(t, v, "esc")
				assert.Equal(t, target, v.target)
				assert.Len(t, v.parents, depth)
				assert.Equal(t, committed, v.find.view.plain)
				if committed {
					assert.Equal(t, selection, v.find.view.selected)
				} else {
					assert.Empty(t, v.find.view.query)
					assert.Empty(t, v.find.view.hits)
				}
				if !resize {
					assert.Equal(t, before, v.View())
				} else if committed {
					assert.Equal(t, v.find.view.projection.rowForPosition(v.find.view.corpus, reading), v.vp.YOffset)
				}
			})
		}
	}
	for _, key := range []string{"esc", "q", "empty-enter"} {
		t.Run("clear/"+key, func(t *testing.T) {
			v := searchViewer(t, findTestViewer(t, collapsedFindMessages(), 70, 8), "hit")
			v = findKey(t, v, "n")
			v = resizeTargetViewer(t, v, 32)
			if key == "empty-enter" {
				v = findKey(t, v, "/")
				v = findKey(t, v, "ctrl+u")
				v = findKey(t, v, "enter")
			} else {
				v = findKey(t, v, key)
			}
			assert.Empty(t, v.find.view.query)
			assert.False(t, v.find.view.plain)
			assert.False(t, v.target.searchSelected)
			if key != "q" {
				require.Equal(t, viewerTargetTool, v.target.kind)
				assert.Empty(t, v.parents[0].find.view.query)
				v = searchViewer(t, v, "hit")
				assert.Len(t, v.find.view.hits, 1, "after clear, slash is local to this tool")
				assert.Equal(t, v.target.scope, v.find.view.corpus.scope)
				v = findKey(t, v, "q")
			}
			assert.Equal(t, viewerTargetMain, v.target.kind)
			assert.Empty(t, v.find.view.query, "back must never resurrect cleared owner search")
			assert.False(t, v.find.view.plain)
			assert.Empty(t, v.parents)
			assert.Equal(t, min(v.active.msgRowStart[1], max(0, len(v.active.rows)-v.vp.Height)), v.vp.YOffset,
				"return to the selected containing message, subject to last-page clamping")
			v = searchViewer(t, v, "hit")
			assert.Len(t, v.find.view.hits, 5, "owner scope remains reusable")
		})
	}
}

func TestViewerFindCollapsedPending(t *testing.T) {
	v := searchViewer(t, findTestViewer(t, collapsedFindMessages(), 70, 8), "hit")
	v = findKey(t, v, "n")
	before := v.View()
	v = findKey(t, v, "/")
	v = findKey(t, v, "ctrl+u")
	v, pending, _ := v.Update(keyMsg("body B"))
	ready := pending() // delivered after cancellation
	v, _, _ = v.Update(keyMsg("esc"))
	v, cmd, _ := v.Update(ready)
	v = settleViewerFind(t, v, cmd)
	assert.Equal(t, before, v.View())
	assert.Equal(t, findWorkID{}, v.find.running)
	assert.Len(t, v.parents, 1)
	v = searchViewer(t, v, "body B")
	assert.Contains(t, ansi.Strip(v.View()), "[body B]")
	assert.Equal(t, 2, v.target.origin.message, "slash in a selected detail still searches the owner")
	position, offset := v.target, v.vp.YOffset
	v = searchViewer(t, v, "no such text")
	assert.Equal(t, position, v.target)
	assert.Equal(t, offset, v.vp.YOffset)
	assert.Contains(t, v.View(), "no matches")
}

func TestViewerFindCollapsedPendingNavigationCancel(t *testing.T) {
	for _, resize := range []bool{false, true} {
		t.Run(fmt.Sprintf("resize=%v", resize), func(t *testing.T) {
			v := searchViewer(t, findTestViewer(t, collapsedFindMessages(), 70, 8), "hit")
			v = findKey(t, v, "n") // tool A
			before, target := v.View(), v.target
			selected, reading := v.find.view.selected, v.findReadingAnchor()
			v, navigation, _ := v.Update(keyMsg("n")) // hold navigation to tool B
			require.NotNil(t, navigation)
			v, _, _ = v.Update(keyMsg("/"))
			if resize {
				v = v.setSize(32, 8)
			}
			v, _, _ = v.Update(keyMsg("esc"))
			v = settleViewerFind(t, v, navigation)
			assert.Equal(t, selected, v.find.view.selected)
			assert.Equal(t, target, v.target)
			if !resize {
				assert.Equal(t, before, v.View())
			} else {
				assert.Equal(t, v.find.view.projection.rowForPosition(v.find.view.corpus, reading), v.vp.YOffset)
			}
			assert.Contains(t, ansi.Strip(v.View()), "[hit]")
			assert.Contains(t, v.findCounter(), "2/5")
			assert.Equal(t, findWorkID{}, v.find.running)
		})
	}
}

func TestViewerFindCollapsedPendingNavigationResize(t *testing.T) {
	for _, repeats := range []int{1, 2, 4} {
		t.Run(fmt.Sprintf("moves=%d", repeats), func(t *testing.T) {
			v := searchViewer(t, findTestViewer(t, collapsedFindMessages(), 70, 8), "hit")
			v = findKey(t, v, "n") // tool A
			v, navigation, _ := v.Update(keyMsg("n"))
			for i := 1; i < repeats; i++ {
				v, _, _ = v.Update(keyMsg("n"))
			}
			v = v.setSize(32, 8)
			v = settleViewerFind(t, v, navigation)
			assert.Equal(t, (1+repeats)%5, v.find.view.selected)
			assert.Equal(t, repeats == 4, v.find.wrapped)
			assert.Equal(t, v.contentWidth(), v.find.view.projection.width)
			assert.Contains(t, ansi.Strip(v.View()), "[hit]")
			assert.Equal(t, findWorkID{}, v.find.running)
		})
	}
}
