package tui

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func nestedTargetViewer(t *testing.T) viewerModel {
	t.Helper()
	sess, files := sampleSession(t)
	files[0].RawContent = jsonlLines(t,
		userLine("side needle needle"),
		assistantLine("side-tool", []map[string]any{bashUseBlock()}),
		toolResultLine(bigBody()+"\nbody needle needle needle"),
		assistantLine("tail", []map[string]any{textBlock(strings.Repeat("trailing context\n", 20))}),
	)
	return newViewerModel(DefaultStyles(), 100, 10).loadSession(sess, files)
}

func resizeTargetViewer(t *testing.T, v viewerModel, width int) viewerModel {
	t.Helper()
	v = v.setSize(width, v.height)
	v, cmd := v.nextFindCommand()
	return settleViewerFind(t, v, cmd)
}

func TestViewerTargetsClone(t *testing.T) {
	v := searchViewer(t, nestedTargetViewer(t), "answer")
	v = findKey(t, v, "n")
	saved := v.targetSnapshot().frame
	before := v.View()
	clone := saved.clone()
	clone.find.view.query = "child"
	clone.find.view.selected = 0
	clone.vp.GotoTop()
	clone.focusedMarker = 42
	assert.Equal(t, "answer", saved.find.view.query)
	assert.Equal(t, 1, saved.find.view.selected)
	assert.Equal(t, before, v.View())
	assert.Same(t, saved.find.view.corpus, clone.find.view.corpus, "immutable caches may be shared")
	assert.Same(t, saved.find.view.projection, clone.find.view.projection)
	assert.Nil(t, clone.find.cancel)
	assert.Nil(t, clone.find.snapshot)
	assert.Nil(t, clone.find.pending)
	assert.Equal(t, findWorkID{}, clone.find.running)

	v = findKey(t, v, "/")
	snapshot := v.find.snapshot
	v = findKey(t, v, "ctrl+u")
	v = findKey(t, v, "different")
	assert.Equal(t, "answer", snapshot.frame.find.view.query, "editor preview cannot mutate its transaction")
	assert.Equal(t, 1, snapshot.frame.find.view.selected)
	v = findKey(t, v, "esc")
	assert.Equal(t, before, v.View())
}

func TestViewerTargetsNested(t *testing.T) {
	for _, resize := range []bool{false, true} {
		t.Run(fmt.Sprintf("resize=%v", resize), func(t *testing.T) {
			v := nestedTargetViewer(t)
			v = findKey(t, v, "]")
			mainOffset, mainAnchor := v.vp.YOffset, v.findReadingAnchor()
			v = findKey(t, v, "enter")
			require.Equal(t, viewerTargetSidecar, v.target.kind)
			require.Len(t, v.parents, 1)
			v = findKey(t, v, "]")
			sideOffset, sideAnchor := v.vp.YOffset, v.findReadingAnchor()
			mi := v.active.markers[v.focusedMarker]
			original := v.active.messages[mi]
			scope := v.target.scope
			v = findKey(t, v, "enter")
			require.Equal(t, viewerTargetTool, v.target.kind)
			require.Len(t, v.parents, 2)
			assert.Equal(t, scope, v.target.ownerScope)
			assert.Equal(t, findPosition{message: mi, field: findBody}, v.target.origin)
			assert.Equal(t, "xyz", v.target.source.subagent)
			assert.Contains(t, v.header(), "subagent xyz")
			assert.Contains(t, v.helpLine(), "return to subagent")
			msg, ok := v.currentMessage()
			require.True(t, ok)
			assert.Equal(t, original.Body, msg.Body)
			assert.Equal(t, original.SourceLine, msg.SourceLine)
			if resize {
				v = resizeTargetViewer(t, v, 65)
			}
			v = findKey(t, v, "esc")
			assert.Equal(t, viewerTargetSidecar, v.target.kind)
			assert.Equal(t, 0, v.focusedMarker)
			assert.Equal(t, sideAnchor.message, v.findReadingAnchor().message)
			if !resize {
				assert.Equal(t, sideOffset, v.vp.YOffset)
			}
			v = findKey(t, v, "q")
			assert.Equal(t, viewerTargetMain, v.target.kind)
			assert.Empty(t, v.parents)
			assert.Equal(t, 0, v.focusedMarker)
			assert.Equal(t, mainAnchor.message, v.findReadingAnchor().message)
			if !resize {
				assert.Equal(t, mainOffset, v.vp.YOffset)
			}
		})
	}

	t.Run("global jump from nested detail", func(t *testing.T) {
		v := nestedTargetViewer(t).openSubagent("xyz", 0).focusMarker(1)
		v, _ = v.openFocusedMarker()
		v = v.jumpTo("xyz", 3)
		assert.Equal(t, viewerTargetSidecar, v.target.kind)
		require.Len(t, v.parents, 1)
		assert.Equal(t, viewerTargetMain, v.parents[0].target.kind)
		assert.Equal(t, min(v.active.rowForLine(3), max(0, v.vp.TotalLineCount()-v.vp.Height)), v.vp.YOffset)
		v = v.jumpTo("", 2)
		assert.Equal(t, viewerTargetMain, v.target.kind)
		assert.Empty(t, v.parents)
	})
}

func TestViewerFindScopes(t *testing.T) {
	for _, resize := range []bool{false, true} {
		t.Run(fmt.Sprintf("resize=%v", resize), func(t *testing.T) {
			v := searchViewer(t, nestedTargetViewer(t), "answer")
			v = findKey(t, v, "n")
			require.Len(t, v.find.view.hits, 2)
			v = findKey(t, v, "]")
			mainView, mainHit := v.View(), v.find.view.hits[v.find.view.selected]
			mainScope, mainAnchor := v.target.scope, v.findReadingAnchor()
			v = findKey(t, v, "enter")
			assert.Empty(t, v.find.view.query)
			v = searchViewer(t, v, "needle")
			require.Len(t, v.find.view.hits, 2, "hidden bodies remain Task 3; main text is outside the sidecar scope")
			v = findKey(t, v, "n")
			v = findKey(t, v, "]")
			sideView, sideHit := v.View(), v.find.view.hits[v.find.view.selected]
			sideScope, sideAnchor := v.target.scope, v.findReadingAnchor()
			v = findKey(t, v, "enter")
			assert.Empty(t, v.find.view.query)
			v = searchViewer(t, v, "needle")
			require.Len(t, v.find.view.hits, 3, "manual tool scope contains the full Body")
			assert.NotEqual(t, sideScope, v.find.view.corpus.scope)
			v = searchViewer(t, v, "go test")
			require.Len(t, v.find.view.hits, 1, "full summary is a separate searchable field")
			assert.Contains(t, ansi.Strip(v.View()), "[go test]")
			if resize {
				v = resizeTargetViewer(t, v, 65)
			}
			v = findKey(t, v, "q")
			assert.Equal(t, "needle", v.find.view.query)
			assert.Equal(t, sideScope, v.find.view.corpus.scope)
			assert.Equal(t, sideHit, v.find.view.hits[v.find.view.selected])
			assert.Equal(t, sideAnchor, v.findReadingAnchor())
			if !resize {
				assert.Equal(t, sideView, v.View())
			}
			v = findKey(t, v, "q")
			assert.Equal(t, "answer", v.find.view.query)
			assert.Equal(t, mainScope, v.find.view.corpus.scope)
			assert.Equal(t, mainHit, v.find.view.hits[v.find.view.selected])
			assert.Equal(t, mainAnchor, v.findReadingAnchor())
			if !resize {
				assert.Equal(t, mainView, v.View())
			}
			assert.Empty(t, v.parents)
			v = findKey(t, v, "n")
			assert.Equal(t, 0, v.find.view.selected, "restored controller is immediately navigable")
		})
	}
}

func TestViewerTargetsConsumers(t *testing.T) {
	m, st := newTestApp(t, Options{})
	m = press(t, m, "enter")
	m.viewer = nestedTargetViewer(t).setSize(160, 20).openSubagent("xyz", 0).focusMarker(1)
	m.width, m.height = 160, 20
	m.viewer, _ = m.viewer.openFocusedMarker()
	owner := m.viewer.sess.UUID
	assert.Equal(t, owner, m.viewer.target.source.session)
	var clipboard bytes.Buffer
	m.clipOut = &clipboard
	m, cmd := func() (Model, tea.Cmd) {
		tm, cmd := m.Update(keyMsg("c"))
		return tm.(Model), cmd
	}()
	msg, ok := m.viewer.currentMessage()
	require.True(t, ok)
	runCmds(cmd)
	var wantClipboard bytes.Buffer
	runCmds(copyToClipboard(&wantClipboard, msg.Body))
	assert.Equal(t, wantClipboard.String(), clipboard.String())
	assert.NotEmpty(t, clipboard.String())
	before := append([]byte(nil), m.viewer.subagentBytes("xyz")...)
	m = openRaw(t, m)
	assert.Contains(t, m.raw.title, "xyz")
	m.raw.vp.Height = m.raw.vp.TotalLineCount()
	m.raw.vp.Width = 2000 // raw view pans long physical records instead of wrapping
	assert.Contains(t, m.raw.vp.View(), "body needle needle needle")
	assert.NotContains(t, m.raw.vp.View(), "first question about timeouts")
	m = press(t, m, "esc")
	assert.Equal(t, viewerTargetTool, m.viewer.target.kind)
	assert.Equal(t, before, m.viewer.subagentBytes("xyz"))
	m = press(t, m, "e", "new title", "enter")
	// press leaves asynchronous rename delivery to the dedicated rename tests;
	// the modal target must still be the session that owns the sidecar/tool.
	assert.Equal(t, owner, m.renameUUID)
	assert.Zero(t, st.searchCalls)

	// A sidecar-owned tool stays Claude even under a Codex owning session.
	v := nestedTargetViewer(t)
	sess := codexSession(t, "codex-owner", "", "owner", codexHumanLine("go"))
	v = v.loadSession(sess, v.files).openSubagent("xyz", 0).focusMarker(1)
	v, _ = v.openFocusedMarker()
	v = resizeTargetViewer(t, v, 55)
	assert.Equal(t, vault.PlatformClaudeCode, v.activePlatform())
	assert.Equal(t, sess.UUID, v.target.source.session)
}

func TestViewerFindScopesRetireStaleWork(t *testing.T) {
	v := searchViewer(t, nestedTargetViewer(t), "answer")
	v = v.setSize(60, 10)
	v, oldCmd := v.nextFindCommand()
	require.NotNil(t, oldCmd)
	oldResult := oldCmd()
	v = v.openSubagent("xyz", 0)
	v = searchViewer(t, v, "needle")
	childEpoch := v.find.epoch
	v = findKey(t, v, "q")
	require.Greater(t, v.find.epoch, childEpoch)
	v, cmd, _ := v.Update(oldResult)
	v = settleViewerFind(t, v, cmd)
	assert.Equal(t, "answer", v.find.view.query)
	assert.Equal(t, 59, v.find.view.projection.width)
	assert.Equal(t, findWorkID{}, v.find.running)
	v = searchViewer(t, v, "question")
	assert.Len(t, v.find.view.hits, 2)

	for range 20 {
		v = findKey(t, v.openSubagent("xyz", 0), "q")
		assert.Empty(t, v.parents)
		assert.Zero(t, cap(v.parents), "discarded frame backing arrays must be released")
	}
}

func TestViewerFindScopesPendingClear(t *testing.T) {
	v := searchViewer(t, nestedTargetViewer(t), "answer")
	v = resizeTargetViewer(t, v, 60)
	v, restore, _ := v.Update(keyMsg("esc"))
	require.NotNil(t, restore, "normal rendering needs the new width")
	require.Empty(t, v.find.view.query)
	v = v.openSubagent("xyz", 0)
	v = searchViewer(t, v, "needle")
	v = findKey(t, v, "q")
	assert.False(t, v.find.view.plain, "return must finish the parent's clear")
	assert.Empty(t, v.find.view.query)
	v = settleViewerFind(t, v, restore)
	assert.False(t, v.find.view.plain, "late work must not revive cleared state")
	v = searchViewer(t, v, "question")
	assert.Len(t, v.find.view.hits, 2)
}
