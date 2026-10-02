package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/serpro69/capy/internal/vault"
)

// Prepared rows and hits are immutable; only cursor/window belong to Update.
type findPickerResults struct {
	query  string
	width  int
	corpus *findCorpus
	hits   []findFuzzyHit
	rows   []string
}

type findPicker struct {
	results *findPickerResults
	cursor  int
	top     int
}

func (p findPicker) window(height int) findPicker {
	height = max(1, height)
	if p.results == nil || len(p.results.hits) == 0 {
		p.cursor, p.top = 0, 0
		return p
	}
	p.cursor = max(0, min(p.cursor, len(p.results.hits)-1))
	p.top = min(p.top, p.cursor)
	p.top = max(p.top, p.cursor-height+1)
	p.top = max(0, min(p.top, len(p.results.hits)-height))
	return p
}

// Scan the original line, retaining only a cell-bounded prefix before the first
// hit. No line-size work runs in View, and generated escapes are never matched.
func findPickerSnippet(ctx context.Context, line findLine, hit findFuzzyHit, width int, st Styles) (string, error) {
	if width <= 0 {
		return "", ctx.Err()
	}
	first := 0
	if len(hit.spans) > 0 {
		first = hit.spans[0].start - line.position.offset
	}
	type glyph struct {
		text       string
		start, end int
		cells      int
	}
	// The first reference run allocated ~97 MB per empty-picker update from
	// per-line glyph buffers. Reuse one small stack buffer for prefix + excerpt;
	// exceptionally wide terminals can still grow it without clipping results.
	var buffer [128]glyph
	prefix := buffer[:0]
	head, cells, offset, nextCheck := 0, 0, 0, 0
	check := func() error {
		if offset >= nextCheck {
			nextCheck = offset + findCheckBytes
			return ctx.Err()
		}
		return nil
	}
	for offset < len(line.text) {
		if err := check(); err != nil {
			return "", err
		}
		display, size, w := findGlyph(line.text[offset:])
		if offset+size > first {
			break
		}
		prefix = append(prefix, glyph{display, offset, offset + size, w})
		cells += w
		for head < len(prefix) && cells > max(0, width/2-1) {
			cells -= prefix[head].cells
			head++
		}
		if head > width {
			prefix = append(prefix[:0], prefix[head:]...)
			head = 0
		}
		offset += size
	}
	visible := prefix[head:]
	left := offset > 0 && (len(visible) == 0 || visible[0].start > 0)
	budget := width
	if left {
		budget--
	}
	for offset < len(line.text) {
		if err := check(); err != nil {
			return "", err
		}
		display, size, w := findGlyph(line.text[offset:])
		reserve := 0
		if offset+size < len(line.text) {
			reserve = 1 // ellipsis for clipped trailing text/matches
		}
		if cells+w+reserve > budget {
			break
		}
		visible = append(visible, glyph{display, offset, offset + size, w})
		cells += w
		offset += size
	}
	var out strings.Builder
	if left {
		out.WriteString("…")
	}
	hi := 0
	for _, g := range visible {
		for hi < len(hit.spans) && hit.spans[hi].end <= g.start+line.position.offset {
			hi++
		}
		if hi < len(hit.spans) && hit.spans[hi].start < g.end+line.position.offset {
			out.WriteString(st.Body.Bold(true).Underline(true).Render(g.text))
		} else {
			out.WriteString(g.text)
		}
	}
	if offset < len(line.text) && cells < budget {
		out.WriteString("…")
	}
	return out.String(), ctx.Err()
}

func prepareFindPicker(ctx context.Context, c *findCorpus, messages []vault.TranscriptMessage, query string, width int, st Styles, previous *findPickerResults) (*findPickerResults, error) {
	var hits []findFuzzyHit
	if previous != nil && previous.query == query {
		hits = previous.hits
	} else {
		var err error
		hits, err = findFuzzy(ctx, c, query)
		if err != nil {
			return nil, err
		}
	}
	result := &findPickerResults{query: query, width: width, corpus: c, hits: hits, rows: make([]string, len(hits))}
	for i, hit := range hits {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := c.lines[hit.line]
		msg := messages[line.position.message]
		field := "body"
		if line.position.field == findSummary {
			field = "summary"
		}
		if msg.Collapsed {
			field += " → detail"
		}
		metadata := fmt.Sprintf("%s m%d:l%d %s · ", msg.Role, line.position.message+1, line.ordinal+1, field)
		// Keep an excerpt usable on small terminals; full metadata returns on grow.
		metadata = ansi.Truncate(metadata, max(0, (width-2)/2), "…")
		snippet, err := findPickerSnippet(ctx, line, hit, width-2-ansi.StringWidth(metadata), st)
		if err != nil {
			return nil, err
		}
		result.rows[i] = metadata + snippet
	}
	return result, ctx.Err()
}

func (m viewerModel) startFindPicker() viewerModel {
	m = m.startFind()
	if !m.find.editing {
		return m
	}
	m.find.picking, m.find.picker = true, findPicker{}
	m.find.input.Prompt = "fuzzy> "
	m.find.input.SetValue("")
	return m.queueFind("", m.find.anchor, m.find.anchor, false, false).sizeFindInput()
}

func (m viewerModel) acceptFindPicker() viewerModel {
	p := m.find.picker
	if m.find.applied != m.find.id() || p.results == nil || len(p.results.hits) == 0 {
		return m
	}
	hit := p.results.hits[p.cursor]
	m = m.invalidateFind()
	m.find.editing, m.find.picking, m.find.snapshot = false, false, nil
	m.find.input.Blur()
	m.find.picker = findPicker{}
	view := m.find.view
	if view.corpus != p.results.corpus {
		view.corpus = p.results.corpus
		view.projection, view.ownerProjection = nil, nil
	}
	view.query, view.fuzzy = "", true
	view.hits = hit.spans
	if len(hit.spans) == 0 {
		// Browsing has a point anchor, without invented matched characters.
		start := view.corpus.lines[hit.line].position.offset
		view.hits = []findHit{{hit.line, start, start}}
	}
	view.selected = 0
	anchor := view.corpus.position(view.hits[0])
	m = m.queueFind("", anchor, anchor, true, false)
	m.find.latest.view, m.find.latest.navigation = view, true
	return m
}

func (m viewerModel) updateFindPicker(msg tea.Msg) (viewerModel, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		delta := 0
		switch key.String() {
		case "esc":
			return m.cancelFind(), nil
		case "enter":
			return m.acceptFindPicker(), nil
		case "up", "ctrl+p":
			delta = -1
		case "down", "ctrl+n":
			delta = 1
		case "pgup":
			delta = -max(1, m.height-3)
		case "pgdown":
			delta = max(1, m.height-3)
		}
		if delta != 0 {
			if m.find.applied == m.find.id() {
				m.find.picker.cursor += delta
				m.find.picker = m.find.picker.window(m.height - 3)
			}
			return m.sizeFindInput(), nil
		}
	}
	before := m.find.input.Value()
	var cmd tea.Cmd
	m.find.input, cmd = m.find.input.Update(msg)
	if query := m.find.input.Value(); query != before {
		m.find.picker.cursor, m.find.picker.top = 0, 0
		m = m.queueFind(query, m.find.anchor, m.find.anchor, false, false)
	}
	return m, cmd
}

func (m viewerModel) findPickerView() string {
	width, height := max(1, m.width), max(1, m.height)
	input := m.findFooter()
	if height == 1 {
		return input
	}
	header := ansi.Truncate("fuzzy lines · "+m.findOwnerTarget().scope, width, "")
	rows := []string{header, input}
	if height == 2 {
		return strings.Join(rows, "\n")
	}
	p := m.find.picker.window(height - 3)
	if p.results != nil && m.find.applied == m.find.id() {
		for i := p.top; i < min(len(p.results.rows), p.top+height-3); i++ {
			prefix := "  "
			if i == p.cursor {
				prefix = "> "
			}
			rows = append(rows, ansi.Truncate(prefix+p.results.rows[i], width, ""))
		}
	}
	rows = append(rows, ansi.Truncate("↑/↓ ctrl+p/n select · pgup/pgdown page · enter open · esc cancel", width, ""))
	return strings.Join(rows, "\n")
}
