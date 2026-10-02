package tui

import (
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/require"
)

// These fixtures install the same already-decoded reference messages in each
// scope. Archive reads, parsing and initial normal rendering are setup work;
// measured returns include Update, scheduling, re-projection, application and View.
func findLifecycleBase(t *testing.T, platform vault.Platform) Model {
	t.Helper()
	m, err := newModel(t.Context(), &stubStore{}, Options{})
	require.NoError(t, err)
	m.width, m.height, m.mode = 100, 30, modeView
	m.viewer = findTestViewer(t, findReferenceMessages(), 100, 30)
	m.viewer.sess.Platform = platform
	m.viewer.target.source.platform = platform
	m.viewer = m.viewer.setActive(renderTranscript(platform, m.viewer.active.messages, m.styles, m.viewer.contentWidth()), 0)
	return m.assignFindEpoch().layoutSubmodels()
}

func findLatencyQuery(t *testing.T, m Model, query string) Model {
	t.Helper()
	for _, key := range []string{"/", "ctrl+u", query, "enter"} {
		m = stepFindLatency(t, m, keyMsg(key))
	}
	return m
}

func requireFindRestored(t *testing.T, got, want Model) {
	t.Helper()
	require.Equal(t, modeView, got.mode)
	require.Equal(t, want.viewer.target, got.viewer.target)
	require.Equal(t, want.viewer.find.view.query, got.viewer.find.view.query)
	require.Equal(t, want.viewer.find.view.fuzzy, got.viewer.find.view.fuzzy)
	require.Equal(t, selectedFindPosition(t, want.viewer), selectedFindPosition(t, got.viewer))
	require.Len(t, got.viewer.parents, len(want.viewer.parents))
	require.Len(t, got.viewerStack, len(want.viewerStack))
	require.Zero(t, got.viewer.find.running)
	require.Nil(t, got.viewer.find.pending)
}

// Run the same scenario checks once in ordinary tests and 100 times per class in
// the opt-in latency gate. Timed operations use the root Update path throughout.
func measureFindLifecycle(t *testing.T, base Model, timed bool) {
	t.Helper()
	run := func(name string, before Model, msg tea.Msg, check func(*testing.T, Model)) {
		t.Helper()
		operation := func(t *testing.T) time.Duration {
			check(t, stepFindLatency(t, before, msg))
			return 0
		}
		if timed {
			measureFindLatency(t, name, func() time.Duration { return operation(t) })
		} else {
			t.Run(name, func(t *testing.T) { operation(t) })
		}
	}
	exact := findLatencyQuery(t, base, "needle08")
	// Find adjacent occurrences crossing each presentation boundary. Derive
	// expected positions independently from the source corpus, not viewport rows.
	transitions := map[string]bool{}
	current := exact
	var hidden Model
	for i := 0; i+1 < len(exact.viewer.find.view.hits) && len(transitions) < 3; i++ {
		f := current.viewer.find.view
		a, b := f.corpus.lines[f.hits[i].line], f.corpus.lines[f.hits[i+1].line]
		name := ""
		switch {
		case !a.hidden && a.position.field == findBody && b.position.field == findSummary:
			name = "exact/visible-to-summary"
		case a.position.field == findSummary && b.hidden:
			name = "exact/summary-to-body"
		case a.hidden && !b.hidden && b.position.field == findBody:
			name = "exact/body-to-visible"
		}
		if name != "" && !transitions[name] {
			want := f.corpus.position(f.hits[i+1])
			run(name, current, keyMsg("n"), func(t *testing.T, m Model) {
				requireFindLanding(t, m.viewer, want)
				require.Equal(t, i+1, m.viewer.find.view.selected)
				require.Equal(t, b.hidden || b.position.field == findSummary, m.viewer.target.searchSelected)
				require.LessOrEqual(t, len(m.viewer.parents), 1)
			})
			transitions[name] = true
		}
		current = stepFindLatency(t, current, keyMsg("n"))
		if current.viewer.target.searchSelected {
			hidden = current
		}
	}
	require.Len(t, transitions, 3)
	summaries := findLatencyQuery(t, base, "/fixture/")
	run("exact/detail-to-detail", summaries, keyMsg("n"), func(t *testing.T, m Model) {
		requireFindLanding(t, m.viewer, summaries.viewer.find.view.corpus.position(summaries.viewer.find.view.hits[1]))
		require.NotEqual(t, summaries.viewer.target.origin.message, m.viewer.target.origin.message)
		require.Len(t, m.viewer.parents, 1)
	})
	run("exact/hidden-resize", hidden, tea.WindowSizeMsg{Width: 80, Height: 30}, func(t *testing.T, m Model) {
		requireFindRestored(t, m, hidden)
		requireFindLanding(t, m.viewer, selectedFindPosition(t, hidden.viewer))
	})
	draft := stepFindLatency(t, hidden, keyMsg("/"))
	draft = stepFindLatency(t, draft, keyMsg("ctrl+u"))
	draft = stepFindLatency(t, draft, keyMsg("Markdown"))
	run("exact/cancel-hidden", draft, keyMsg("esc"), func(t *testing.T, m Model) {
		requireFindRestored(t, m, hidden)
		require.Equal(t, hidden.viewer.vp.YOffset, m.viewer.vp.YOffset)
	})
	run("exact/clear-hidden", hidden, keyMsg("esc"), func(t *testing.T, m Model) {
		require.Equal(t, viewerTargetTool, m.viewer.target.kind)
		require.False(t, m.viewer.find.view.plain)
		require.Empty(t, m.viewer.find.view.query)
	})
	cleared := stepFindLatency(t, hidden, keyMsg("esc"))
	for _, tc := range []struct {
		name string
		m    Model
	}{{"exact/back-cleared", cleared}, {"exact/back-hidden", hidden}} {
		run(tc.name, tc.m, keyMsg("q"), func(t *testing.T, m Model) {
			require.Equal(t, viewerTargetMain, m.viewer.target.kind)
			require.Empty(t, m.viewer.find.view.query)
			require.Empty(t, m.viewer.parents)
		})
	}

	// Manual sidecar/tool scopes suspend their parent's own committed query.
	side := exact
	side.viewer = side.viewer.pushTarget(viewerTarget{
		kind: viewerTargetSidecar, scope: "find-test/side",
		source: viewerTranscriptSource{session: side.viewer.sess.UUID, subagent: "side", platform: vault.PlatformClaudeCode},
	}, base.viewer.active.messages, 0)
	side = findLatencyQuery(t, side, "Markdown")
	side = stepFindLatency(t, side, keyMsg("]"))
	tool := stepFindLatency(t, side, keyMsg("enter"))
	require.Equal(t, viewerTargetTool, tool.viewer.target.kind)
	tool = findLatencyQuery(t, tool, "needle08")
	for _, tc := range []struct {
		name        string
		start, want Model
	}{{"local/tool-return", tool, side}, {"local/sidecar-return", side, exact}} {
		for _, resize := range []bool{false, true} {
			start := tc.start
			if resize {
				start = stepFindLatency(t, start, tea.WindowSizeMsg{Width: 80, Height: 30})
			}
			run(fmt.Sprintf("%s/resize=%v", tc.name, resize), start, keyMsg("q"), func(t *testing.T, m Model) {
				requireFindRestored(t, m, tc.want)
				f := m.viewer.find.view
				row := f.projection.rowForPosition(f.corpus, tc.want.viewer.findReadingAnchor())
				require.Equal(t, min(row, max(0, m.viewer.vp.TotalLineCount()-m.viewer.vp.Height)), m.viewer.vp.YOffset)
			})
		}
	}

	// Real root child push/pop, with decoded reference fields installed after
	// opening. Store reads and decoding happen before the timed return.
	parent := exact
	parent.store = &stubStore{sessions: []vault.Session{
		codexChildSession(t, codexChildID, parent.viewer.sess.UUID),
		codexChildSession(t, codexGrandchildID, codexChildID),
	}}
	child := parent.openChild(codexChildID)
	child.viewer = child.viewer.setActive(renderTranscript(vault.PlatformCodex, base.viewer.active.messages, child.styles, child.viewer.contentWidth()), 0)
	child = findLatencyQuery(t, child, "needle08")
	grandchild := child.openChild(codexGrandchildID)
	grandchild.viewer = grandchild.viewer.setActive(renderTranscript(vault.PlatformCodex, base.viewer.active.messages, grandchild.styles, grandchild.viewer.contentWidth()), 0)
	grandchild = findLatencyQuery(t, grandchild, "Markdown")
	for _, tc := range []struct {
		name        string
		start, want Model
	}{{"root/child-return", child, parent}, {"root/grandchild-return", grandchild, child}} {
		for _, resize := range []bool{false, true} {
			start := tc.start
			if resize {
				start = stepFindLatency(t, start, tea.WindowSizeMsg{Width: 80, Height: 30})
			}
			run(fmt.Sprintf("%s/resize=%v", tc.name, resize), start, keyMsg("q"), func(t *testing.T, m Model) {
				requireFindRestored(t, m, tc.want)
				require.Greater(t, m.viewer.find.epoch, tc.start.viewer.find.epoch)
				requireFindLanding(t, m.viewer, selectedFindPosition(t, tc.want.viewer))
			})
		}
	}

	for _, scope := range []struct {
		name string
		m    Model
	}{{"main", exact}, {"sidecar", side}, {"manual-tool", tool}, {"selected-tool", hidden}, {"child", child}} {
		for _, fuzzy := range []bool{false, true} {
			want := scope.m
			if fuzzy {
				want = stepFindLatency(t, want, keyMsg("ctrl+f"))
				want = stepFindLatency(t, want, keyMsg("needle08"))
				want = stepFindLatency(t, want, keyMsg("enter"))
			}
			// Finish raw formatting before the timed search-resume operation.
			next, cmd := want.Update(keyMsg("v"))
			raw := next.(Model)
			require.Equal(t, modeRaw, raw.mode)
			next, follow := raw.Update(cmd())
			raw = next.(Model)
			require.Nil(t, follow)
			require.False(t, raw.raw.loading)
			for _, dimensions := range []struct{ width, height int }{{100, 30}, {100, 10}, {80, 30}} {
				resized := stepFindLatency(t, raw, tea.WindowSizeMsg{Width: dimensions.width, Height: dimensions.height})
				run(fmt.Sprintf("raw/%s/fuzzy=%v/%dx%d", scope.name, fuzzy, dimensions.width, dimensions.height),
					resized, keyMsg("q"), func(t *testing.T, m Model) {
						requireFindRestored(t, m, want)
						require.Greater(t, m.viewer.find.epoch, want.viewer.find.epoch)
						if dimensions.width == 100 && dimensions.height == 30 {
							require.Equal(t, want.viewer.vp.YOffset, m.viewer.vp.YOffset)
						}
						require.Empty(t, m.raw.title)
					})
			}
		}
	}
	for _, st := range []*stubStore{base.store.(*stubStore), parent.store.(*stubStore)} {
		require.Zero(t, st.searchCalls)
		require.Zero(t, st.renameCalls)
		require.Zero(t, st.projectCalls)
	}
}

func TestFindLifecycle(t *testing.T) {
	for _, platform := range []vault.Platform{vault.PlatformClaudeCode, vault.PlatformCodex} {
		t.Run(string(platform), func(t *testing.T) {
			measureFindLifecycle(t, findLifecycleBase(t, platform), false)
		})
	}
}

func findRetainedHeap(m Model) uint64 {
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	runtime.KeepAlive(m)
	return stats.HeapAlloc
}

func TestFindLifetimeStress(t *testing.T) {
	if os.Getenv("CAPY_FIND_BENCH") != "1" {
		t.Skip("opt-in retained-memory measurements")
	}
	m := findLifecycleBase(t, vault.PlatformCodex)
	m.store = &stubStore{sessions: []vault.Session{codexChildSession(t, codexChildID, m.viewer.sess.UUID)}}
	normal := findRetainedHeap(m)
	t.Run("prepare exact", func(t *testing.T) { m = findLatencyQuery(t, m, "needle08") })
	exact := findRetainedHeap(m)
	t.Run("prepare picker", func(t *testing.T) {
		m = stepFindLatency(t, m, keyMsg("ctrl+f"))
		m = stepFindLatency(t, m, keyMsg("needle08"))
	})
	picker := findRetainedHeap(m)
	t.Run("cancel picker", func(t *testing.T) { m = stepFindLatency(t, m, keyMsg("esc")) })
	t.Logf("reference retained heap: exact-minus-normal=%d picker-minus-exact=%d",
		int64(exact)-int64(normal), int64(picker)-int64(exact))
	baseline := findRetainedHeap(m)
	for cycle := 1; cycle <= 20; cycle++ {
		t.Run(fmt.Sprintf("open %d", cycle), func(t *testing.T) {
			m = m.openChild(codexChildID)
			m.viewer = m.viewer.setActive(renderTranscript(vault.PlatformCodex, findReferenceMessages(), m.styles, m.viewer.contentWidth()), 0)
			m = findLatencyQuery(t, m, "needle08")
			m = stepFindLatency(t, m, keyMsg("]"))
			m = stepFindLatency(t, m, keyMsg("enter"))
			m = stepFindLatency(t, m, keyMsg("ctrl+f"))
			m = stepFindLatency(t, m, keyMsg("needle08"))
			m = stepFindLatency(t, m, keyMsg("enter"))
			m = openRaw(t, m)
			m = stepFindLatency(t, m, keyMsg("q"))
		})
		if cycle == 1 {
			t.Logf("nested child/tool/fuzzy/raw-return retained delta=%d", int64(findRetainedHeap(m))-int64(baseline))
		}
		t.Run(fmt.Sprintf("return %d", cycle), func(t *testing.T) {
			m = stepFindLatency(t, m, keyMsg("q"))
			m = stepFindLatency(t, m, keyMsg("q"))
		})
		require.Equal(t, "find-test", m.viewer.sess.UUID)
		require.Equal(t, "needle08", m.viewer.find.view.query)
		require.Zero(t, cap(m.viewerStack))
		require.Zero(t, cap(m.viewer.parents))
		if cycle == 1 || cycle == 10 || cycle == 20 {
			t.Logf("after %d nested open/back cycles retained delta=%d", cycle, int64(findRetainedHeap(m))-int64(baseline))
		}
	}
	t.Run("leave viewer", func(t *testing.T) { m = stepFindLatency(t, m, keyMsg("q")) })
	require.False(t, m.viewer.ready)
	require.Nil(t, m.viewer.find.view.corpus)
	require.Nil(t, m.viewer.find.view.projection)
	require.Zero(t, cap(m.viewerStack))
	require.Zero(t, cap(m.viewer.parents))
	t.Logf("after leaving viewer retained delta=%d", int64(findRetainedHeap(m))-int64(baseline))
}
