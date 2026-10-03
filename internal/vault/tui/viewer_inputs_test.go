package tui

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func codeInputSession(t *testing.T) (vault.Session, string) {
	t.Helper()
	code := "// @exec: {\"yield_time_ms\": 1000}\ntext(\"**literal**\\nquoted\\\"\");\n" +
		strings.Repeat("// context\n", 25) + "// needle hidden alpha\n// needle hidden beta\n"
	lines := []map[string]any{
		codexAssistantLine("needle visible early\n" + strings.Repeat("assistant context\n", 30)),
		codexEnv("response_item", map[string]any{"type": "custom_tool_call", "name": "exec", "call_id": "call-code", "input": code}),
	}
	lines = append(lines, codexSpawnLines("spawn", codexChildID)...)
	lines = append(lines,
		codexEnv("response_item", map[string]any{"type": "custom_tool_call_output", "call_id": "call-code", "output": "wrapper output"}),
		codexAssistantLine("needle visible last\n"+strings.Repeat("tail context\n", 20)),
	)
	return codexSession(t, codexParentID, "", "Executable input", lines...), code
}

func codeInputViewer(t *testing.T) (viewerModel, string) {
	t.Helper()
	sess, code := codeInputSession(t)
	return newViewerModel(DefaultStyles(), 80, 10).loadSession(sess, nil), code
}

func TestViewerCodeInputSourceJump(t *testing.T) {
	v, _ := codeInputViewer(t)
	require.Len(t, v.active.markers, 2)
	assert.Equal(t, 2, strings.Count(ansi.Strip(v.active.content()), "▌ Codex"),
		"the owning entry has one header; the trailing separate entry has one")
	assert.True(t, v.active.messages[0].SourceAnchor)
	assert.Greater(t, v.active.msgRowStart[1], v.vp.Height)
	for _, line := range []int{1, 2, 3, 4} {
		v = v.jumpTo("", line)
		assert.Contains(t, ansi.Strip(v.View()), "▌ Codex")
		assert.Contains(t, ansi.Strip(v.View()), "needle visible early", "source line %d", line)
	}
	// Local marker return and rewrap use ordinals, so SourceAnchor cannot pull
	// a detail return back up to the assistant's header.
	v = v.focusMarker(1)
	before := v.vp.YOffset
	v = findKey(t, v, "enter")
	v = findKey(t, v, "esc")
	assert.Equal(t, before, v.vp.YOffset)
	v = findKey(t, v, "enter")
	v = resizeTargetViewer(t, v, 37)
	v = findKey(t, v, "esc")
	assert.Equal(t, 1, v.findReadingAnchor().message)
	assert.Equal(t, 0, v.focusedMarker)
}

func TestViewerCodeInputSourceTieFallback(t *testing.T) {
	messages := []vault.TranscriptMessage{
		{Role: vault.RoleAssistant, SourceLine: 2, Body: "old body"},
		{Role: vault.RoleSubagent, SourceLine: 2, Body: "old launch"},
		{Role: vault.RoleAssistant, SourceLine: 5, Body: "new owner", SourceAnchor: true},
		{Role: vault.RoleTool, SourceLine: 5, Body: "code", Collapsed: true},
		{Role: vault.RoleSubagent, SourceLine: 5, Body: "new launch"},
		{Role: vault.RoleAssistant, SourceLine: 9, Body: "later"},
		{Role: vault.RoleSubagent, SourceLine: 9, Body: "later launch"},
	}
	r := renderTranscript(vault.PlatformCodex, messages, DefaultStyles(), 80)
	for _, tc := range []struct{ line, message int }{{-1, 0}, {2, 1}, {4, 1}, {5, 2}, {8, 2}, {9, 6}, {99, 6}} {
		t.Run(fmt.Sprint(tc.line), func(t *testing.T) {
			assert.Equal(t, r.msgRowStart[tc.message], r.rowForLine(tc.line))
		})
	}
}

func TestViewerCodeInputDetail(t *testing.T) {
	v, code := codeInputViewer(t)
	assert.Contains(t, v.active.content(), "→ exec · input")
	assert.Contains(t, v.active.content(), "exec · output")
	assert.NotContains(t, v.active.content(), "yield_time_ms")
	v = findKey(t, v, "]")
	v = findKey(t, v, "enter")
	require.Equal(t, viewerTargetTool, v.target.kind)
	assert.Equal(t, "exec · input", v.target.label)
	assert.Contains(t, ansi.Strip(v.active.content()), "▌ Tool input")
	assert.Contains(t, ansi.Strip(v.active.content()), `**literal**\nquoted\"`, "Markdown is bypassed")
	msg, ok := v.currentMessage()
	require.True(t, ok)
	assert.Equal(t, code, msg.Body)
	for _, row := range v.active.rows {
		assert.NotContains(t, row, "\n")
	}
	v = searchViewer(t, v, "needle")
	assert.Len(t, v.find.view.hits, 2, "manual detail has exactly the complete input")
	assert.Equal(t, v.target.scope, v.find.view.corpus.scope)
	assert.Contains(t, ansi.Strip(v.active.content()), "▌ Tool input")
	v = findKey(t, v, "q")
	assert.Equal(t, viewerTargetMain, v.target.kind)
	assert.Empty(t, v.parents)
}

func TestViewerCodeInputFind(t *testing.T) {
	v, _ := codeInputViewer(t)
	v = searchViewer(t, v, "needle")
	require.Len(t, v.find.view.hits, 4)
	corpus := v.find.view.corpus
	for range 3 {
		for i, ordinal := range []int{0, 1, 1, 5} {
			position := selectedFindPosition(t, v)
			assert.Equal(t, ordinal, position.message)
			requireFindLanding(t, v, position)
			assert.Same(t, corpus, v.find.view.corpus)
			if i == 1 || i == 2 {
				assert.Len(t, v.parents, 1)
				assert.Equal(t, viewerTargetTool, v.target.kind)
				assert.Contains(t, ansi.Strip(v.active.content()), "▌ Tool input")
			} else {
				assert.Empty(t, v.parents)
			}
			v = findKey(t, v, "n")
		}
	}
	v = findKey(t, v, "ctrl+f")
	v = findKey(t, v, "ndlha")
	require.NotEmpty(t, v.find.picker.results.hits)
	v = findKey(t, v, "enter")
	assert.True(t, v.find.view.fuzzy)
	assert.Equal(t, 1, selectedFindPosition(t, v).message)
	assert.Same(t, corpus, v.find.view.corpus)
	assert.Len(t, v.parents, 1)
	requireFindLanding(t, v, selectedFindPosition(t, v))
}

func TestViewerCodeInputPending(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%v", cancel), func(t *testing.T) {
			v, _ := codeInputViewer(t)
			v = searchViewer(t, v, "needle")
			v = findKey(t, v, "n")
			before := selectedFindPosition(t, v)
			v, pending, _ := v.Update(keyMsg("n"))
			require.NotNil(t, pending)
			if cancel {
				v, _, _ = v.Update(keyMsg("/"))
			}
			v = v.setSize(37, 10)
			if cancel {
				v, _, _ = v.Update(keyMsg("esc"))
			}
			v = settleViewerFind(t, v, pending)
			assert.Len(t, v.parents, 1)
			if cancel {
				assert.Equal(t, before, selectedFindPosition(t, v))
			} else {
				assert.Greater(t, selectedFindPosition(t, v).offset, before.offset)
			}
			requireFindLanding(t, v, selectedFindPosition(t, v))
			assert.Equal(t, findWorkID{}, v.find.running)
		})
	}
}

func TestViewerCodeInputCopyRawChildReturn(t *testing.T) {
	parent, code := codeInputSession(t)
	child := codexChildSession(t, codexChildID, codexParentID)
	m, st := codexFamilyApp(t, parent, child)
	m = appFindKey(t, m, "enter")
	m = appFindKey(t, m, "]")
	m = appFindKey(t, m, "enter")
	require.Equal(t, viewerTargetTool, m.viewer.target.kind)
	var clipboard, want bytes.Buffer
	m.clipOut = &clipboard
	next, cmd := m.Update(keyMsg("c"))
	m = next.(Model)
	runCmds(cmd)
	runCmds(copyToClipboard(&want, code))
	assert.Equal(t, want.String(), clipboard.String())
	before := m.viewer.target
	m = openRaw(t, m)
	m.raw.vp.Width, m.raw.vp.Height = 8000, m.raw.vp.TotalLineCount()
	assert.Contains(t, m.raw.vp.View(), "custom_tool_call")
	assert.Contains(t, m.raw.vp.View(), "yield_time_ms")
	m = appFindKey(t, m, "esc")
	assert.Equal(t, before, m.viewer.target)
	msg, ok := m.viewer.currentMessage()
	require.True(t, ok)
	assert.Equal(t, code, msg.Body)
	m = appFindKey(t, m, "esc")
	m = appFindKey(t, m, "]")
	parentOffset := m.viewer.vp.YOffset
	m = appFindKey(t, m, "enter")
	require.Equal(t, codexChildID, m.viewer.sess.UUID)
	m = appFindKey(t, m, "esc")
	assert.Equal(t, codexParentID, m.viewer.sess.UUID)
	assert.Equal(t, parentOffset, m.viewer.vp.YOffset)
	assert.Equal(t, 1, m.viewer.focusedMarker)
	assert.Empty(t, m.viewerStack)
	assert.Zero(t, st.searchCalls)
}
