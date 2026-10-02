package tui

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/serpro69/capy/internal/vault"
)

const findFixtureSeed int64 = 20260920

var findBenchmarkQueries = []string{"a", "needle08", strings.Repeat("ab", 16), strings.Repeat("Q", 256), strings.Repeat("界", 256), "NO_SUCH_PASSAGE"}

// Exactly 100 groups of 79 ordinary lines + 1 summary + 20 hidden lines.
// All measurements start with these parsed messages in an already-open viewer.
func findReferenceMessages() []vault.TranscriptMessage {
	rng := rand.New(rand.NewSource(findFixtureSeed))
	messages := make([]vault.TranscriptMessage, 0, 200)
	ordinal := 0
	line := func(diff bool) string {
		ordinal++
		text := fmt.Sprintf("line %05d · a passage about **Markdown**, cancellation, and Unicode 界 é. ", ordinal)
		if diff {
			text = "+ " + text
		}
		text += fmt.Sprintf("value=%08x ", rng.Uint32())
		if ordinal%11 == 0 {
			text += "needle08 needle08 "
		}
		if ordinal%31 == 0 {
			text += findBenchmarkQueries[2]
		}
		if ordinal%257 == 0 {
			text += findBenchmarkQueries[3]
		}
		if ordinal%509 == 0 {
			text += findBenchmarkQueries[4]
		}
		if ordinal%997 == 0 {
			text += strings.Repeat("long-token-", 400)
		}
		return text
	}
	for group := range 100 {
		body := make([]string, 79)
		for i := range body {
			body[i] = line(false)
		}
		messages = append(messages, vault.TranscriptMessage{Role: vault.RoleAssistant, SourceLine: group * 2, Body: strings.Join(body, "\n")})
		hidden := make([]string, 20)
		for i := range hidden {
			hidden[i] = line(group%3 == 0)
		}
		messages = append(messages, vault.TranscriptMessage{Role: vault.RoleTool, SourceLine: group*2 + 1,
			Collapsed: true, Diff: group%3 == 0, ToolSummary: fmt.Sprintf("Read /fixture/%03d needle08", group), Body: strings.Join(hidden, "\n")})
	}
	return messages
}

func TestFindReferenceFixture(t *testing.T) {
	messages := findReferenceMessages()
	c, err := buildFindCorpus(t.Context(), "fixture", messages)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.lines) != 10_000 {
		t.Fatalf("reference lines = %d, want 10000", len(c.lines))
	}
	for _, query := range findBenchmarkQueries[:5] {
		hits, err := findExact(t.Context(), c, query)
		if err != nil || len(hits) == 0 {
			t.Fatalf("query length %d: hits=%d err=%v", utf8.RuneCountInString(query), len(hits), err)
		}
		visible, hidden := 0, 0
		for _, hit := range hits {
			if c.lines[hit.line].hidden {
				hidden++
			} else {
				visible++
			}
		}
		if visible == 0 || hidden == 0 {
			t.Fatalf("query length %d must exercise both fields: visible=%d hidden=%d", utf8.RuneCountInString(query), visible, hidden)
		}
	}
}

func BenchmarkFindCorpus(b *testing.B) {
	messages := findReferenceMessages()
	for b.Loop() {
		if _, err := buildFindCorpus(b.Context(), "fixture", messages); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFindExact(b *testing.B) {
	c, err := buildFindCorpus(b.Context(), "fixture", findReferenceMessages())
	if err != nil {
		b.Fatal(err)
	}
	for i, query := range findBenchmarkQueries {
		b.Run(fmt.Sprintf("query%d", i), func(b *testing.B) {
			for b.Loop() {
				if _, err := findExact(b.Context(), c, query); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkFindProjection(b *testing.B) {
	messages := findReferenceMessages()
	c, err := buildFindCorpus(b.Context(), "fixture", messages)
	if err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		if _, err := buildFindProjection(b.Context(), c, messages, vault.PlatformClaudeCode, DefaultStyles(), 99); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFindHighlight(b *testing.B) {
	messages := findReferenceMessages()
	c, err := buildFindCorpus(b.Context(), "fixture", messages)
	if err != nil {
		b.Fatal(err)
	}
	p, err := buildFindProjection(b.Context(), c, messages, vault.PlatformClaudeCode, DefaultStyles(), 99)
	if err != nil {
		b.Fatal(err)
	}
	hits, err := findExact(b.Context(), c, "needle08")
	if err != nil {
		b.Fatal(err)
	}
	row := p.rowForHit(hits[0])
	for b.Loop() {
		for i := row; i < min(len(p.rows), row+28); i++ {
			p.highlightRow(c, i, hits, 0, DefaultStyles())
		}
	}
}

// Execute commands as Bubble Tea does: Batch children run independently, so a
// cosmetic blink timer cannot delay search delivery. Only find messages are
// delivered here; cursor ticks are outside the input-to-search-result measure.
func dispatchFindLatency(cmd tea.Cmd, output chan<- findResultMsg) {
	if cmd == nil {
		return
	}
	go func() {
		switch result := cmd().(type) {
		case tea.BatchMsg:
			for _, child := range result {
				dispatchFindLatency(child, output)
			}
		case findResultMsg:
			output <- result
		}
	}()
}

func finishFindLatency(t *testing.T, m Model, output <-chan findResultMsg, submit func(tea.Cmd)) Model {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for m.viewer.find.running != (findWorkID{}) || m.viewer.find.pending != nil {
		select {
		case result := <-output:
			next, cmd := m.Update(result)
			m = next.(Model)
			submit(cmd)
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-deadline.C:
			t.Fatal("search controller did not retire")
		}
	}
	if m.viewer.find.err != "" {
		t.Fatal(m.viewer.find.err)
	}
	_ = m.View()
	return m
}

func stepFindLatency(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	output := make(chan findResultMsg, 8)
	next, cmd := m.Update(msg)
	dispatchFindLatency(cmd, output)
	return finishFindLatency(t, next.(Model), output, func(cmd tea.Cmd) { dispatchFindLatency(cmd, output) })
}

func measureFindLatency(t *testing.T, name string, operation func() time.Duration) {
	t.Helper()
	timings := make([]time.Duration, 100)
	var allocated uint64
	for i := range timings {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		start := time.Now()
		override := operation()
		timings[i] = time.Since(start)
		if override > 0 {
			timings[i] = override
		}
		runtime.ReadMemStats(&after)
		allocated += after.TotalAlloc - before.TotalAlloc
	}
	sort.Slice(timings, func(i, j int) bool { return timings[i] < timings[j] })
	t.Logf("%-22s n=100 median=%8.3fms p95=%8.3fms max=%8.3fms bytes/op=%d", name,
		float64(timings[49])/float64(time.Millisecond), float64(timings[94])/float64(time.Millisecond),
		float64(timings[99])/float64(time.Millisecond), allocated/100)
	if timings[99] > 100*time.Millisecond {
		t.Errorf("%s exceeded the 100 ms gate: %v", name, timings[99])
	}
}

func TestFindLatency(t *testing.T) {
	if os.Getenv("CAPY_FIND_BENCH") != "1" {
		t.Skip("opt-in reference workload: CAPY_FIND_BENCH=1")
	}
	messages := findReferenceMessages()
	c, err := buildFindCorpus(t.Context(), "fixture", messages)
	if err != nil {
		t.Fatal(err)
	}
	bytes, collapsed := 0, 0
	var canonical strings.Builder
	for _, msg := range messages {
		bytes += len(msg.Body)
		canonical.WriteString(msg.Body)
		if msg.Collapsed {
			collapsed++
			bytes += len(msg.ToolSummary)
			canonical.WriteString(msg.ToolSummary)
		}
	}
	t.Logf("seed=%d digest=%x lines=%d bytes=%d messages=%d collapsed=%d terminal=100x30 Go=%s %s/%s GOMAXPROCS=%d",
		findFixtureSeed, sha256.Sum256([]byte(canonical.String())), len(c.lines), bytes, len(messages), collapsed,
		runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.GOMAXPROCS(0))
	st := &stubStore{}
	base, err := newModel(t.Context(), st, Options{})
	if err != nil {
		t.Fatal(err)
	}
	base.width, base.height, base.mode = 100, 30, modeView
	base.viewer = findTestViewer(t, messages, 100, 30)
	base = base.assignFindEpoch().layoutSubmodels()
	_ = base.View() // warm the normal renderer; decode/archive load is excluded
	measureFindLatency(t, "open/cold-projection", func() time.Duration {
		stepFindLatency(t, base, keyMsg("/"))
		return 0
	})
	opened := stepFindLatency(t, base, keyMsg("/"))
	for i, query := range findBenchmarkQueries {
		measureFindLatency(t, fmt.Sprintf("edit/%d/%d-runes", i, utf8.RuneCountInString(query)), func() time.Duration {
			m := stepFindLatency(t, opened, keyMsg(query))
			if m.viewer.find.view.query != query {
				t.Fatal("wrong query applied")
			}
			for _, hit := range m.viewer.find.view.hits {
				line := m.viewer.find.view.corpus.lines[hit.line]
				if line.text[hit.start-line.position.offset:hit.end-line.position.offset] != query {
					t.Fatal("match lost original source coordinates")
				}
			}
			return 0
		})
	}
	preview := stepFindLatency(t, opened, keyMsg("needle08"))
	measureFindLatency(t, "enter", func() time.Duration {
		stepFindLatency(t, preview, keyMsg("enter"))
		return 0
	})
	committed := stepFindLatency(t, preview, keyMsg("enter"))
	for _, key := range []string{"n", "N"} {
		measureFindLatency(t, "navigate/"+key, func() time.Duration {
			m := stepFindLatency(t, committed, keyMsg(key))
			if key == "N" && !m.viewer.target.searchSelected {
				t.Fatal("previous-hit wrap must open the final collapsed tool")
			}
			return 0
		})
	}
	measureFindLatency(t, "selected/resize", func() time.Duration {
		m := stepFindLatency(t, committed, tea.WindowSizeMsg{Width: 80, Height: 30})
		if m.viewer.find.view.hits[m.viewer.find.view.selected] != committed.viewer.find.view.hits[committed.viewer.find.view.selected] {
			t.Fatal("resize changed selected occurrence")
		}
		return 0
	})
	measureFindLatency(t, "rapid/final-keystroke", func() time.Duration {
		m := opened
		output := make(chan findResultMsg, 8)
		for _, key := range "needle0" {
			next, cmd := m.Update(keyMsg(string(key)))
			m = next.(Model)
			dispatchFindLatency(cmd, output)
		}
		start := time.Now()
		next, cmd := m.Update(keyMsg("8"))
		dispatchFindLatency(cmd, output)
		m = finishFindLatency(t, next.(Model), output, func(cmd tea.Cmd) { dispatchFindLatency(cmd, output) })
		if m.viewer.find.view.query != "needle08" {
			t.Fatal("obsolete rapid-typing query applied")
		}
		return time.Since(start)
	})
	for i, query := range findBenchmarkQueries {
		measureFindLatency(t, fmt.Sprintf("full-corpus/build+scan%d", i), func() time.Duration {
			full, err := buildFindCorpus(t.Context(), "fixture", messages)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := findExact(t.Context(), full, query); err != nil {
				t.Fatal(err)
			}
			return 0
		})
	}
	measureFindFuzzyLatency(t, base)
	t.Log("Exact and integrated fuzzy gates measured; complete suspension protocol remains Task 6")
}

// Task 1 stress evidence covers current construction/matching/projection paths;
// Task 6 adds repeated nested open/back measurements using the local frames.
func TestFindStress(t *testing.T) {
	if os.Getenv("CAPY_FIND_BENCH") != "1" {
		t.Skip("opt-in stress measurements")
	}
	for _, fixture := range []struct{ name, body string }{
		{"100000-lines", strings.Repeat("line needle\n", 99_999) + "last needle"},
		{"single-MiB-line", strings.Repeat("a", 1<<20-6) + "needle"},
		{"long-graphemes", strings.Repeat("e"+strings.Repeat("\u0301", 8192)+" needle\n", 64)},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			messages := []vault.TranscriptMessage{{Body: fixture.body}}
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			start := time.Now()
			c, err := buildFindCorpus(t.Context(), fixture.name, messages)
			if err != nil {
				t.Fatal(err)
			}
			hits, err := findExact(t.Context(), c, "needle")
			if err != nil {
				t.Fatal(err)
			}
			p, err := buildFindProjection(t.Context(), c, messages, vault.PlatformClaudeCode, DefaultStyles(), 99)
			if err != nil {
				t.Fatal(err)
			}
			elapsed := time.Since(start)
			runtime.GC()
			runtime.ReadMemStats(&after)
			t.Logf("bytes=%d lines=%d hits=%d rows=%d build+scan+projection=%v retained-heap-delta=%d",
				len(fixture.body), len(c.lines), len(hits), len(p.rows), elapsed,
				int64(after.HeapAlloc)-int64(before.HeapAlloc))
			runtime.KeepAlive(p)
			runtime.KeepAlive(hits)
			ctx := &findCheckpointContext{Context: context.Background(), remaining: 4}
			start = time.Now()
			_, err = findExact(ctx, c, "absent")
			if err != context.Canceled {
				t.Fatalf("mid-scan cancellation failed: %v", err)
			}
			t.Logf("cancel after four scan checkpoints: %v", time.Since(start))
			ctx = &findCheckpointContext{Context: context.Background(), remaining: 4}
			start = time.Now()
			_, err = findFuzzy(ctx, c, "absent")
			if err != context.Canceled {
				t.Fatalf("fuzzy mid-scan cancellation failed: %v", err)
			}
			t.Logf("fuzzy cancel after four scan checkpoints: %v", time.Since(start))
		})
	}
}

func BenchmarkFindFuzzy(b *testing.B) {
	c, err := buildFindCorpus(b.Context(), "fixture", findReferenceMessages())
	if err != nil {
		b.Fatal(err)
	}
	for i, query := range findBenchmarkQueries {
		b.Run(fmt.Sprintf("query%d", i), func(b *testing.B) {
			for b.Loop() {
				if _, err := findFuzzy(b.Context(), c, query); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func measureFindFuzzyLatency(t *testing.T, base Model) {
	measureFindLatency(t, "fuzzy/open-cold", func() time.Duration {
		m := stepFindLatency(t, base, keyMsg("ctrl+f"))
		if len(m.viewer.find.picker.results.hits) != 10_000 {
			t.Fatal("empty picker must retain all nonempty reference lines")
		}
		return 0
	})
	opened := stepFindLatency(t, base, keyMsg("ctrl+f"))
	for i, query := range findBenchmarkQueries {
		measureFindLatency(t, fmt.Sprintf("fuzzy/edit%d/%d", i, utf8.RuneCountInString(query)), func() time.Duration {
			m := stepFindLatency(t, opened, keyMsg(query))
			if m.viewer.find.picker.results.query != query {
				t.Fatal("obsolete picker query applied")
			}
			if i < 5 && len(m.viewer.find.picker.results.hits) == 0 {
				t.Fatal("positive fuzzy fixture query lost all results")
			}
			return 0
		})
	}
	for _, key := range []string{"down", "pgdown", "pgup"} {
		start := opened
		if key == "pgup" {
			start = stepFindLatency(t, start, keyMsg("pgdown"))
		}
		measureFindLatency(t, "fuzzy/"+key, func() time.Duration {
			stepFindLatency(t, start, keyMsg(key))
			return 0
		})
	}
	measureFindLatency(t, "fuzzy/picker-resize", func() time.Duration {
		stepFindLatency(t, opened, tea.WindowSizeMsg{Width: 80, Height: 30})
		return 0
	})
	for _, target := range []string{"visible", "summary", "body", "browse"} {
		preview := opened
		if target != "browse" {
			preview = stepFindLatency(t, opened, keyMsg("needle08"))
			found := false
			for _, hit := range preview.viewer.find.picker.results.hits {
				line := preview.viewer.find.picker.results.corpus.lines[hit.line]
				if target == "visible" && line.position.field == findBody && !line.hidden ||
					target == "summary" && line.position.field == findSummary || target == "body" && line.hidden {
					found = true
					break
				}
				preview = stepFindLatency(t, preview, keyMsg("down"))
			}
			if !found {
				t.Fatal("missing fixture target: " + target)
			}
		}
		measureFindLatency(t, "fuzzy/accept-"+target, func() time.Duration {
			m := stepFindLatency(t, preview, keyMsg("enter"))
			if !m.viewer.find.view.fuzzy || m.viewer.find.view.query != "" {
				t.Fatal("picker acceptance did not replace exact search")
			}
			if (target == "summary" || target == "body") && !m.viewer.target.searchSelected {
				t.Fatal("hidden fuzzy selection did not open detail")
			}
			return 0
		})
		accepted := stepFindLatency(t, preview, keyMsg("enter"))
		measureFindLatency(t, "fuzzy/resize-"+target, func() time.Duration {
			m := stepFindLatency(t, accepted, tea.WindowSizeMsg{Width: 80, Height: 30})
			if m.viewer.find.view.hits[0] != accepted.viewer.find.view.hits[0] {
				t.Fatal("resize moved fuzzy selection")
			}
			return 0
		})
	}
	exact := stepFindLatency(t, base, keyMsg("/"))
	exact = stepFindLatency(t, exact, keyMsg("needle08"))
	exact = stepFindLatency(t, exact, keyMsg("enter"))
	exact = stepFindLatency(t, exact, keyMsg("N"))
	picker := stepFindLatency(t, exact, keyMsg("ctrl+f"))
	picker = stepFindLatency(t, picker, keyMsg("Markdown"))
	measureFindLatency(t, "fuzzy/cancel-exact", func() time.Duration {
		m := stepFindLatency(t, picker, keyMsg("esc"))
		if m.viewer.target != exact.viewer.target || m.viewer.find.view.query != "needle08" {
			t.Fatal("cancel did not restore exact detail")
		}
		return 0
	})
	measureFindLatency(t, "fuzzy/rapid-final", func() time.Duration {
		m := opened
		output := make(chan findResultMsg, 8)
		for _, key := range "needle0" {
			next, cmd := m.Update(keyMsg(string(key)))
			m = next.(Model)
			dispatchFindLatency(cmd, output)
		}
		start := time.Now()
		next, cmd := m.Update(keyMsg("8"))
		dispatchFindLatency(cmd, output)
		m = finishFindLatency(t, next.(Model), output, func(cmd tea.Cmd) { dispatchFindLatency(cmd, output) })
		if m.viewer.find.picker.results.query != "needle08" {
			t.Fatal("obsolete fuzzy result survived replacement")
		}
		return time.Since(start)
	})
}
