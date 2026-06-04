package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/serpro69/capy/internal/vault"
)

// viewerAction signals the app what to do after a viewer Update.
type viewerAction int

const (
	viewerNone viewerAction = iota
	viewerBack              // leave the viewer (return to the previous mode)
)

// viewerChromeRows is the number of rows the viewer reserves outside the
// scrolling viewport: one header line + one help line.
const viewerChromeRows = 2

// viewerModel renders a session transcript and, on demand, a single subagent
// transcript standalone. It owns no store handle: the app fetches the session +
// sidecars and hands them in via loadSession, and the viewer keeps the sidecar
// File set so it can open a subagent's transcript (search-jump or marker) without
// another DB round-trip.
//
// Subagents are markers-only (the chosen v1 fallback, design.md §Viewer Model):
// launch points render as visible markers; a subagent transcript is viewed
// standalone — opened by a search hit (exact, via subagent_id) or by selecting an
// openable marker — with esc/q returning to the main session. No inline interleave.
type viewerModel struct {
	styles        Styles
	width, height int

	sess  vault.Session
	files []vault.File
	subIDs []string // sorted subagent ids, for ParseTranscript's launch mapping

	main   renderedTranscript // the main session transcript
	active renderedTranscript // == main, or a subagent transcript when inSub
	inSub  bool
	subID  string
	savedMainLine int // main source line at the top of the viewport when a subagent was opened; restored (via rowForLine) on return so a resize re-wrap can't stale it
	focusedMarker int // index into active.markers, or -1

	vp    viewport.Model
	ready bool
}

func newViewerModel(styles Styles, width, height int) viewerModel {
	return viewerModel{
		styles:        styles,
		width:         width,
		height:        height,
		focusedMarker: -1,
		vp:            viewport.New(width, max(1, height-viewerChromeRows)),
	}
}

// loadSession resets the viewer to a new session's main transcript. files are the
// session's sidecars (subagent transcripts among them). Call jumpTo afterwards to
// land on a specific line / subagent.
func (m viewerModel) loadSession(sess vault.Session, files []vault.File) viewerModel {
	m.sess = sess
	m.files = files
	m.subIDs = sortedSubagentIDs(files)
	m.inSub = false
	m.subID = ""
	m.savedMainLine = 0
	m.main = renderTranscript(vault.ParseTranscript(sess.RawJSONL, m.subIDs), m.styles, m.contentWidth())
	m.ready = true
	return m.setActive(m.main, 0)
}

// jumpTo scrolls to a search hit. An empty subagentID targets the main
// transcript; a set subagentID opens that subagent standalone. Unknown subagent
// ids fall back to the main transcript so a jump never dead-ends.
func (m viewerModel) jumpTo(subagentID string, line int) viewerModel {
	if subagentID == "" {
		if m.inSub {
			m = m.returnToMain()
		}
		m.vp.SetYOffset(m.main.rowForLine(line))
		return m
	}
	return m.openSubagent(subagentID, line)
}

// openSubagent loads a subagent transcript as the active target and scrolls to
// line. If the subagent's bytes are not archived, it stays on the current target.
func (m viewerModel) openSubagent(id string, line int) viewerModel {
	raw := m.subagentBytes(id)
	if raw == nil {
		return m // not archived; caller's search/marker shouldn't have offered it
	}
	if !m.inSub {
		// Remember the main top as a source line (not a row offset) so a resize
		// re-wrap while in the subagent can't stale it; rowForLine re-derives the
		// row on return.
		m.savedMainLine = m.main.lineForRow(m.vp.YOffset)
	}
	// nil subIDs: a subagent transcript has no nested subagent markers to map.
	sub := renderTranscript(vault.ParseTranscript(raw, nil), m.styles, m.contentWidth())
	m.inSub = true
	m.subID = id
	return m.setActive(sub, sub.rowForLine(line))
}

// returnToMain restores the main transcript at the source line that was on top
// when the subagent was opened (re-derived to the current wrap width).
func (m viewerModel) returnToMain() viewerModel {
	if !m.inSub {
		return m
	}
	m.inSub = false
	m.subID = ""
	return m.setActive(m.main, m.main.rowForLine(m.savedMainLine))
}

// setActive swaps the active transcript into the viewport at the given offset
// and clears any marker focus. Value receiver returning the model (the bubbletea
// value-model convention) — every viewerModel method passes/returns by value.
func (m viewerModel) setActive(rt renderedTranscript, yOffset int) viewerModel {
	m.active = rt
	m.focusedMarker = -1
	m.vp.SetContent(m.viewportContent())
	m.vp.SetYOffset(yOffset)
	return m
}

// viewportContent is the active transcript's content with the focused marker row
// (if any) re-styled as focused. Computing the focus overlay at content time —
// rather than mutating the rows slice — avoids aliasing the backing array shared
// between m.main and m.active. The per-keystroke copy is cheap (a slice of string
// headers).
func (m viewerModel) viewportContent() string {
	if m.focusedMarker < 0 || m.focusedMarker >= len(m.active.markers) {
		return m.active.content()
	}
	mi := m.active.markers[m.focusedMarker]
	row := m.active.msgRowStart[mi]
	if row < 0 || row >= len(m.active.rows) {
		return m.active.content()
	}
	rows := make([]string, len(m.active.rows))
	copy(rows, m.active.rows)
	rows[row] = m.styles.markerRow(m.active.messages[mi], true)
	return strings.Join(rows, "\n")
}

func (m viewerModel) setSize(width, height int) viewerModel {
	m.width = width
	m.height = height
	m.vp.Width = width
	m.vp.Height = max(1, height-viewerChromeRows)
	if !m.ready {
		return m
	}
	// Re-wrap at the new width. Capture the top source line BEFORE re-rendering
	// (the offset is in old-render row space), then restore it via rowForLine in
	// the new render so the scroll position survives the re-wrap.
	topLine := m.active.lineForRow(m.vp.YOffset)
	m.main = renderTranscript(m.main.messages, m.styles, m.contentWidth())
	if m.inSub {
		m.active = renderTranscript(m.active.messages, m.styles, m.contentWidth())
	} else {
		m.active = m.main
	}
	m.vp.SetContent(m.viewportContent())
	m.vp.SetYOffset(m.active.rowForLine(topLine))
	return m
}

func (m viewerModel) Update(msg tea.Msg) (viewerModel, tea.Cmd, viewerAction) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd, viewerNone
	}

	switch key.String() {
	case "esc", "q":
		if m.inSub {
			return m.returnToMain(), nil, viewerNone
		}
		return m, nil, viewerBack
	case "j", "down":
		m.vp.ScrollDown(1)
	case "k", "up":
		m.vp.ScrollUp(1)
	case "g", "home":
		m.vp.GotoTop()
	case "G", "end":
		m.vp.GotoBottom()
	case "ctrl+d", "pgdown", " ":
		m.vp.HalfPageDown()
	case "ctrl+u", "pgup", "b":
		m.vp.HalfPageUp()
	case "]", "tab", "n":
		m = m.focusMarker(1)
	case "[", "shift+tab", "N":
		m = m.focusMarker(-1)
	case "enter":
		m = m.openFocusedMarker()
	}
	return m, nil, viewerNone
}

// focusMarker advances the focused openable marker by delta and scrolls to it.
// No-op when the active transcript has no openable markers.
func (m viewerModel) focusMarker(delta int) viewerModel {
	n := len(m.active.markers)
	if n == 0 {
		return m
	}
	next := m.focusedMarker + delta
	switch {
	case m.focusedMarker == -1 && delta > 0:
		next = 0
	case m.focusedMarker == -1 && delta < 0:
		next = n - 1
	case next < 0:
		next = n - 1
	case next >= n:
		next = 0
	}
	m.focusedMarker = next
	// Re-render content so the newly-focused marker is visibly highlighted (not
	// just scrolled into view), then scroll to it.
	m.vp.SetContent(m.viewportContent())
	if row := m.active.rowForMarker(next); row >= 0 {
		m.vp.SetYOffset(row)
	}
	return m
}

// openFocusedMarker opens the focused marker's subagent standalone, scrolled to
// its top. No-op when no marker is focused.
func (m viewerModel) openFocusedMarker() viewerModel {
	if m.focusedMarker < 0 || m.focusedMarker >= len(m.active.markers) {
		return m
	}
	mi := m.active.markers[m.focusedMarker]
	msg := m.active.messages[mi]
	if !msg.Openable || msg.AgentID == "" {
		return m
	}
	return m.openSubagent(msg.AgentID, 0)
}

func (m viewerModel) View() string {
	if !m.ready {
		return "no session loaded"
	}
	return strings.Join([]string{m.header(), m.vp.View(), m.helpLine()}, "\n")
}

func (m viewerModel) header() string {
	title := strings.TrimSpace(m.sess.Title)
	if title == "" {
		title = "(untitled)"
	}
	if m.inSub {
		title = fmt.Sprintf("%s › subagent %s", title, shortID(m.subID))
	}
	loc := fmt.Sprintf("%s · %s", shortID(m.sess.UUID), displayPath(m.sess.ProjectPath))
	return m.styles.Title.Render(truncate(title, max(1, m.width-len(loc)-3))) +
		"  " + m.styles.StatusBar.Render(loc)
}

func (m viewerModel) helpLine() string {
	keys := "j/k scroll · g/G top/bottom · q back"
	if len(m.active.markers) > 0 {
		keys = "j/k scroll · ]/[ subagent · enter open · q back"
	}
	if m.inSub {
		keys = "j/k scroll · esc/q return to session"
	}
	return m.styles.Help.Render(keys)
}

// contentWidth is the wrap width for body text (a small right margin avoids the
// terminal's last column).
func (m viewerModel) contentWidth() int {
	return max(1, m.width-1)
}

// subagentBytes returns the raw content of the session's subagent transcript with
// the given id, or nil when it is not among the archived sidecars.
func (m viewerModel) subagentBytes(id string) []byte {
	want := vault.SubagentRelPath(id)
	for _, f := range m.files {
		if f.RelativePath == want {
			return f.RawContent
		}
	}
	return nil
}

// sortedSubagentIDs returns the session's subagent ids in sorted order, the order
// ParseTranscript maps to launch points (see its doc for the mapping caveat).
// Sorting here is explicit rather than relying on GetFiles' ORDER BY, so the
// marker mapping stays deterministic even if that query's ordering changes.
func sortedSubagentIDs(files []vault.File) []string {
	var ids []string
	for _, f := range files {
		if id, ok := vault.SubagentIDFromPath(f.RelativePath); ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}
