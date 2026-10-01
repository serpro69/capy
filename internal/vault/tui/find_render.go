package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/serpro69/capy/internal/vault"
)

// A row maps a contiguous range of original field bytes. Escapes and grapheme
// highlights are derived only for visible rows, avoiding a per-character map
// over the full archive. Structural rows have line == -1 and no searchable text.
type findRow struct {
	line       int
	start, end int
}

type findProjection struct {
	width      int
	transcript renderedTranscript
	rows       []findRow
	lineRows   []int
	viewport   viewport.Model // immutable prepared content; copy before scrolling
}

// findGlyph returns safe presentation text and the original byte length. The
// printable-ASCII fast path still consults the grapheme segmenter when a Unicode
// continuation might attach to the preceding ASCII rune (e.g. e + combining mark).
func findGlyph(s string) (display string, size, cells int) {
	if s[0] >= ' ' && s[0] < 0x7f && (len(s) == 1 || s[1] < utf8.RuneSelf) {
		return s[:1], 1, 1
	}
	r, n := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError && n == 1 {
		return fmt.Sprintf("\\x%02x", s[0]), 1, 4
	}
	if unicode.IsControl(r) {
		return fmt.Sprintf("\\u%04x", r), n, 6
	}
	cluster, width := ansi.FirstGraphemeCluster(s, ansi.GraphemeWidth)
	return cluster, len(cluster), width
}

// wrapFindLine prefers whitespace boundaries but retains the whitespace itself.
// No source bytes are consumed without a row, even when a glyph is too wide for
// a tiny viewport. View clips such a glyph whole until more cells are available.
func wrapFindLine(ctx context.Context, line findLine, width int, emit func(start, end int, text string)) error {
	text := line.text
	if text == "" {
		emit(line.position.offset, line.position.offset, "")
		return ctx.Err()
	}
	nextCheck := 0
	for start := 0; start < len(text); {
		end, cells, space := start, 0, -1
		for end < len(text) {
			if end >= nextCheck {
				if err := ctx.Err(); err != nil {
					return err
				}
				nextCheck = end + findCheckBytes
			}
			_, size, w := findGlyph(text[end:])
			if cells+w > width && end > start {
				if space > start {
					end = space
				}
				break
			}
			r, _ := utf8.DecodeRuneInString(text[end:])
			end += size
			cells += w
			if unicode.IsSpace(r) {
				space = end
			}
		}
		var safe strings.Builder
		safe.Grow(end - start)
		for offset := start; offset < end; {
			display, size, _ := findGlyph(text[offset:end])
			safe.WriteString(display)
			offset += size
		}
		emit(line.position.offset+start, line.position.offset+end, safe.String())
		start = end
	}
	return ctx.Err()
}

func buildFindProjection(ctx context.Context, c *findCorpus, messages []vault.TranscriptMessage, platform vault.Platform, st Styles, width int) (*findProjection, error) {
	p := &findProjection{width: width, lineRows: make([]int, len(c.lines))}
	p.transcript.messages = messages
	p.transcript.msgRowStart = make([]int, len(messages))
	for i := range p.lineRows {
		p.lineRows[i] = -1
	}
	structural := func(text string) {
		p.transcript.rows = append(p.transcript.rows, ansi.Truncate(text, max(1, width), ""))
		p.rows = append(p.rows, findRow{line: -1})
	}
	li := 0
	for mi, msg := range messages {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p.transcript.msgRowStart[mi] = len(p.rows)
		switch {
		case msg.Collapsed:
			p.transcript.markers = append(p.transcript.markers, mi)
			structural(st.MarkerOpenable.Render("▸ tool result · enter to expand"))
		case msg.Role == vault.RoleSubagent:
			if msg.Openable {
				p.transcript.markers = append(p.transcript.markers, mi)
			}
			structural(st.MarkerOpenable.Render("▸ subagent"))
		default:
			structural(st.messageHeader(msg.Role, msg.Queued, platform))
		}
		for li < len(c.lines) && c.lines[li].position.message == mi {
			line := c.lines[li]
			if !line.hidden {
				p.lineRows[li] = len(p.rows)
				err := wrapFindLine(ctx, line, max(1, width-2), func(start, end int, text string) {
					p.rows = append(p.rows, findRow{li, start, end})
					p.transcript.rows = append(p.transcript.rows, text)
				})
				if err != nil {
					return nil, err
				}
			}
			li++
		}
		structural("")
	}
	// SetContent measures every row; do this once per scope/width, off Update.
	p.viewport = viewport.New(max(1, width), 1)
	p.viewport.SetContent(p.transcript.content())
	return p, ctx.Err()
}

func (p *findProjection) rowForHit(hit findHit) int {
	start := p.lineRows[hit.line]
	if start < 0 {
		return 0
	}
	end := sort.Search(len(p.rows)-start, func(i int) bool {
		row := p.rows[start+i]
		return row.line != hit.line || row.end > hit.start
	})
	return min(start+end, len(p.rows)-1)
}

func (p *findProjection) rowForPosition(c *findCorpus, position findPosition) int {
	li := sort.Search(len(c.lines), func(i int) bool {
		line := c.lines[i]
		end := line.position
		end.offset += len(line.text)
		return !end.before(position)
	})
	if li < len(c.lines) && p.lineRows[li] >= 0 {
		if c.lines[li].text == "" {
			return p.lineRows[li] // a point anchor on an empty row has no hit span
		}
		return p.rowForHit(findHit{li, position.offset, position.offset})
	}
	if position.message < len(p.transcript.msgRowStart) {
		return p.transcript.msgRowStart[position.message]
	}
	return 0
}

func (p *findProjection) positionForRow(c *findCorpus, row int) findPosition {
	if row >= 0 && row < len(p.rows) {
		r := p.rows[row]
		if r.line >= 0 {
			pos := c.lines[r.line].position
			pos.offset = r.start
			return pos
		}
	}
	mi := max(0, sort.Search(len(p.transcript.msgRowStart), func(i int) bool { return p.transcript.msgRowStart[i] > row })-1)
	return findPosition{message: mi}
}

func findLineStyle(st Styles, msg vault.TranscriptMessage, line string) lipgloss.Style {
	if msg.Diff {
		switch {
		case strings.HasPrefix(line, "@@"):
			return st.DiffHunk
		case strings.HasPrefix(line, "+"):
			return st.DiffAdd
		case strings.HasPrefix(line, "-"):
			return st.DiffDel
		}
	}
	return st.Body
}

// Highlight only visible rows. Selection brackets are an overlay, never corpus
// text, and every row always wraps with the same two-cell reservation.
func (p *findProjection) highlightRow(c *findCorpus, rowIndex int, hits []findHit, selected int, st Styles) string {
	row := p.rows[rowIndex]
	if row.line < 0 {
		return p.transcript.rows[rowIndex]
	}
	line := c.lines[row.line]
	style := findLineStyle(st, p.transcript.messages[line.position.message], line.text)
	hi := sort.Search(len(hits), func(i int) bool {
		return hits[i].line > row.line || (hits[i].line == row.line && hits[i].end > row.start)
	})
	var chosen findHit
	if selected >= 0 && selected < len(hits) {
		chosen = hits[selected]
	} else {
		chosen.line = -1
	}
	var out, run strings.Builder
	state := -1
	flush := func() {
		if run.Len() == 0 {
			return
		}
		text := run.String()
		switch state {
		case 2:
			if p.width >= 3 {
				text = "[" + text + "]"
			}
			out.WriteString(st.ResultSelected.Render(text))
		case 1:
			out.WriteString(style.Underline(true).Bold(true).Render(text))
		default:
			out.WriteString(style.Render(text))
		}
		run.Reset()
	}
	for offset := row.start; offset < row.end; {
		display, size, _ := findGlyph(line.text[offset-line.position.offset : row.end-line.position.offset])
		next := offset + size
		for hi < len(hits) && hits[hi].line == row.line && hits[hi].end <= offset {
			hi++
		}
		current := 0
		if hi < len(hits) && hits[hi].line == row.line && hits[hi].start < next {
			current = 1
		}
		if chosen.line == row.line && chosen.start < next && chosen.end > offset {
			current = 2
		}
		if current != state {
			flush()
			state = current
		}
		run.WriteString(display)
		offset = next
	}
	flush()
	return ansi.Truncate(out.String(), max(1, p.width), "")
}
