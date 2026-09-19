package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// rawModel displays archived bytes independently of the transcript decoder and
// Markdown renderer. No records are filtered, merged, recovered, or redacted.
type rawModel struct {
	styles        Styles
	title         string
	width, height int
	vp            viewport.Model
	loading       bool
	err           string
	xOffset       int
	contentWidth  int
}

type rawLoadedMsg struct {
	seq          int
	vp           viewport.Model
	err          error
	contentWidth int
}

// startRaw runs both archive reads (when needed) and formatting outside Update.
// The command owns the new viewport until delivery; raw bytes are immutable.
func (m Model) startRaw(title string, load func(context.Context) ([]byte, error)) (tea.Model, tea.Cmd) {
	ctx, cancel := context.WithCancel(m.ctx)
	m.rawCancel = cancel
	m.rawSeq++
	seq := m.rawSeq
	m.rawReturn = m.mode
	m.mode = modeRaw
	m.raw = rawModel{styles: m.styles, title: title, loading: true,
		vp: viewport.New(max(1, m.width), max(1, m.bodyHeight()-viewerChromeRows))}
	m.raw = m.raw.setSize(m.width, m.bodyHeight())
	width, height := m.raw.vp.Width, m.raw.vp.Height
	return m, func() tea.Msg {
		defer cancel()
		if err := ctx.Err(); err != nil {
			return rawLoadedMsg{seq: seq, err: err}
		}
		raw, err := load(ctx)
		if err != nil {
			return rawLoadedMsg{seq: seq, err: err}
		}
		content, err := formatRawJSONL(ctx, raw)
		if err != nil {
			return rawLoadedMsg{seq: seq, err: err}
		}
		// Width on the whole archive allocates another all-lines slice. Measure
		// one line at a time; the viewport already retains its own split content.
		contentWidth := 0
		for remaining := content; remaining != ""; {
			if err := ctx.Err(); err != nil {
				return rawLoadedMsg{seq: seq, err: err}
			}
			line, rest, _ := strings.Cut(remaining, "\n")
			contentWidth = max(contentWidth, lipgloss.Width(line))
			remaining = rest
		}
		vp := viewport.New(width, height)
		vp.SetContent(content)
		return rawLoadedMsg{seq: seq, vp: vp, contentWidth: contentWidth, err: ctx.Err()}
	}
}

func (m Model) updateRaw(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc", "q", "v":
		if m.rawCancel != nil {
			m.rawCancel()
			m.rawCancel = nil
		}
		m.mode = m.rawReturn
		m.raw = rawModel{} // release the formatted archive on close
		// Keep the exact viewport offset unless a resize requires re-wrapping.
		if m.mode == modeView && (m.viewer.width != m.width || m.viewer.height != m.bodyHeight()) {
			m.viewer = m.viewer.setSize(m.width, m.bodyHeight())
		}
		return m, nil
	case "g", "home":
		m.raw.vp.GotoTop()
	case "G", "end":
		m.raw.vp.GotoBottom()
	case "h", "left":
		m.raw = m.raw.pan(m.raw.xOffset - 8)
	case "l", "right":
		m.raw = m.raw.pan(m.raw.xOffset + 8)
	case "0":
		m.raw = m.raw.pan(0)
	default:
		var cmd tea.Cmd
		m.raw.vp, cmd = m.raw.vp.Update(key)
		return m, cmd
	}
	return m, nil
}

func (m rawModel) setSize(width, height int) rawModel {
	m.width, m.height = max(1, width), max(1, height)
	m.vp.Width = m.width
	m.vp.Height = max(1, height-viewerChromeRows)
	m.vp.SetYOffset(m.vp.YOffset)
	// Re-clamp a horizontal offset after the terminal grows.
	return m.pan(m.xOffset)
}

// Clamp before calling SetXOffset: bubbles v1 permits negative offsets when the
// longest line is narrower than the viewport. Keep all text reachable after a resize.
func (m rawModel) pan(offset int) rawModel {
	m.xOffset = max(0, min(offset, m.contentWidth-m.vp.Width))
	m.vp.SetXOffset(m.xOffset)
	return m
}

func (m rawModel) View() string {
	header := m.styles.Title.MaxWidth(m.width).Render("Raw · " + oneLine(m.title))
	help := m.styles.Help.MaxWidth(m.width).Render("j/k scroll · h/l pan · 0 left · g/G top/bottom · esc/q back")
	if m.height == 1 {
		return header
	}
	if m.height == 2 {
		return header + "\n" + help
	}
	body := m.vp.View()
	if m.loading {
		body = m.styles.Help.MaxWidth(m.width).Render("loading archived JSONL…")
	} else if m.err != "" {
		body = m.styles.ErrorMsg.MaxWidth(m.width).Render(oneLine(m.err))
	}
	return strings.Join([]string{header, body, help}, "\n")
}

// formatRawJSONL formats each physical record separately, preserving field order
// and numeric/escape spellings. Cut avoids Scanner's token limit. Invalid lines
// remain visible with a source-line diagnostic, including torn final records.
func formatRawJSONL(ctx context.Context, raw []byte) (string, error) {
	if len(raw) == 0 {
		return "(empty archived transcript)", ctx.Err()
	}
	var out strings.Builder
	var pretty bytes.Buffer
	newlineSep := []byte{'\n'}
	for lineNo := 1; len(raw) > 0; lineNo++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		line, rest, newline := bytes.Cut(raw, newlineSep)
		raw = rest
		pretty.Reset()
		if len(bytes.TrimSpace(line)) == 0 {
			out.Write(line)
		} else if err := json.Indent(&pretty, line, "", "  "); err != nil {
			fmt.Fprintf(&out, "[invalid JSON at source line %d: %v]\n", lineNo, err)
			out.Write(line)
		} else {
			out.Write(pretty.Bytes())
		}
		if newline {
			out.WriteByte('\n')
		}
	}
	return rawDisplayText(ctx, out.String())
}

// Show terminal controls and invalid UTF-8 as escapes instead of executing or
// dropping them. In particular a malformed record may contain a literal OSC/CSI.
// The archive itself is never changed; ordinary JSON string escapes stay literal.
func rawDisplayText(ctx context.Context, s string) (string, error) {
	var out strings.Builder
	out.Grow(len(s))
	nextCheck := 0
	for i := 0; i < len(s); {
		// Check at byte intervals, including for multibyte text whose rune starts
		// may never coincide with exact multiples of 4096.
		if i >= nextCheck {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			nextCheck = i + 4096
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&out, "\\x%02x", s[i])
		case r != '\n' && unicode.IsControl(r):
			fmt.Fprintf(&out, "\\u%04x", r)
		default:
			out.WriteString(s[i : i+size])
		}
		i += size
	}
	return out.String(), ctx.Err()
}
