package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func codexChangesViewer(t *testing.T) viewerModel {
	t.Helper()
	changes := map[string]any{}
	for i, counts := range [][2]int{{5, 2}, {2, 2}, {10, 8}, {6, 2}} {
		text := fmt.Sprintf("@@ -1,%d +1,%d @@\n", counts[1], counts[0]) +
			strings.Repeat("-old example\n", counts[1]) + strings.Repeat("+new example\n", counts[0])
		changes[fmt.Sprintf("/archive/project/file-%d", i+1)] = map[string]any{
			"type": "update", "move_path": nil, "unified_diff": text}
	}
	raw := jsonlLines(t,
		map[string]any{"type": "session_meta", "payload": map[string]any{"cwd": "/archive/project"}},
		map[string]any{"type": "event_msg", "payload": map[string]any{"type": "item_completed", "item": map[string]any{
			"type": "FileChange", "id": "nested-edit", "status": "completed", "changes": changes}}},
	)
	return newViewerModel(DefaultStyles(), 100, 24).loadSession(vault.Session{
		UUID: "grouped-diff", Platform: vault.PlatformCodex, RawJSONL: raw}, nil)
}

func TestViewerCodexFileChanges(t *testing.T) {
	v := codexChangesViewer(t)
	require.Len(t, v.active.markers, 1)
	assert.Contains(t, v.active.content(), "4 files changed (+23 −14)")
	assert.NotContains(t, v.active.content(), "line(s)")
	assert.NotContains(t, v.active.content(), "+new example")
	v = v.focusMarker(1)
	before, position := v.active.content(), v.vp.YOffset
	v, _ = v.openFocusedMarker()
	assert.Equal(t, viewerTargetTool, v.target.kind)
	assert.Equal(t, "4 files changed (+23 −14)", v.target.label)
	assert.Contains(t, ansi.Strip(v.active.content()), "▌ File changes · completed")
	assert.NotContains(t, ansi.Strip(v.active.content()), "▌ Tool result")
	assert.Contains(t, v.active.content(), "*** Update File: file-4 (+6 −2)")
	msg, ok := v.currentMessage()
	require.True(t, ok)
	assert.Equal(t, v.parents[0].active.messages[0].Body, msg.Body, "copy uses the complete grouped body")
	v = v.returnToParent()
	assert.Equal(t, before, v.active.content())
	assert.Equal(t, position, v.vp.YOffset)
	assert.Equal(t, 0, v.focusedMarker)

	v = searchViewer(t, v, "new example")
	require.True(t, v.target.searchSelected)
	require.Len(t, v.find.view.hits, 23)
	assert.Contains(t, ansi.Strip(v.active.content()), "▌ File changes · completed")
	assert.True(t, v.active.messages[0].Diff)
	corpus := v.find.view.corpus
	for range 24 {
		v = findKey(t, v, "n")
		assert.Same(t, corpus, v.find.view.corpus)
		assert.Len(t, v.parents, 1)
	}
	v = resizeTargetViewer(t, v, 60)
	assert.Contains(t, ansi.Strip(v.active.content()), "▌ File changes · completed")
	assert.Contains(t, ansi.Strip(v.View()), "[new example]")
}

func TestTranscriptHeadingOverride(t *testing.T) {
	for _, tc := range []struct{ name, heading, want string }{
		{"default", "", "▌ Tool result"},
		{"file changes", "File changes · completed", "▌ File changes · completed"},
		{"multiline", "custom\nheading", "▌ custom heading"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := []vault.TranscriptMessage{{Role: vault.RoleTool, Heading: tc.heading, Body: "**literal**\n+diff"}}
			st := DefaultStyles()
			rendered := renderTranscript(vault.PlatformCodex, messages, st, 80)
			assert.Equal(t, tc.want, ansi.Strip(rendered.rows[0]))
			assert.Contains(t, ansi.Strip(rendered.content()), "**literal**", "tool bodies bypass Markdown in both builds")
			v := findTestViewer(t, messages, 80, 24)
			v = searchViewer(t, v, "literal")
			assert.Equal(t, tc.want, ansi.Strip(v.active.rows[0]))
			for _, row := range v.active.rows {
				assert.NotContains(t, row, "\n")
			}
		})
	}
	encoded, err := json.Marshal(vault.TranscriptMessage{Role: vault.RoleTool})
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "Heading", "existing Claude goldens omit the optional override")
}
