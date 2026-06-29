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

	sess   vault.Session
	files  []vault.File
	subIDs []string // sorted subagent ids, for ParseTranscript's launch mapping

	main   renderedTranscript // the main session transcript
	active renderedTranscript // == main, or a detail transcript (subagent / inline tool body) when inDetail
	inSub  bool
	subID  string
	// inInline / inlineLabel mirror inSub for the A1 collapse-then-open detail view:
	// a collapsed tool_result's full body shown standalone (esc/q returns to main).
	// inSub and inInline are mutually exclusive — see inDetail.
	inInline      bool
	inlineLabel   string
	savedMainLine int // main source line at the top of the viewport when a detail was opened; restored (via rowForLine) on return so a resize re-wrap can't stale it
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

// inDetail reports whether a standalone detail transcript is open over the main
// session — a subagent (inSub) or a collapsed tool_result's body (inInline). esc/q
// returns to main from either; setSize re-wraps the active detail in both.
func (m viewerModel) inDetail() bool { return m.inSub || m.inInline }

// loadSession resets the viewer to a new session's main transcript. files are the
// session's sidecars (subagent transcripts among them). Call jumpTo afterwards to
// land on a specific line / subagent.
func (m viewerModel) loadSession(sess vault.Session, files []vault.File) viewerModel {
	m.sess = sess
	m.files = files
	m.subIDs = sortedSubagentIDs(files)
	m.inSub = false
	m.subID = ""
	m.inInline = false
	m.inlineLabel = ""
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
		if m.inDetail() {
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
	if !m.inDetail() {
		// Remember the main top as a source line (not a row offset) so a resize
		// re-wrap while in the detail can't stale it; rowForLine re-derives the
		// row on return.
		m.savedMainLine = m.main.lineForRow(m.vp.YOffset)
	}
	// nil subIDs: a subagent transcript has no nested subagent markers to map.
	sub := renderTranscript(vault.ParseTranscript(raw, nil), m.styles, m.contentWidth())
	m.inSub = true
	m.subID = id
	m.inInline = false
	m.inlineLabel = ""
	return m.setActive(sub, sub.rowForLine(line))
}

// openInlineContent opens a collapsed tool_result's full body as a standalone
// detail view (esc/q returns to the session). Unlike openSubagent — which fetches
// a sidecar transcript by id — the body is inline in raw_jsonl and already carried
// on msg, so this renders a single-message transcript from it: no DB round-trip
// and no separate "open inline content" file target (design.md § Addenda A1). The
// call summary surfaces in the header (inlineLabel) rather than the body.
func (m viewerModel) openInlineContent(msg vault.TranscriptMessage) viewerModel {
	// In normal flow inDetail() is false here (inline markers exist only on main,
	// and inSub/inInline are mutually exclusive); the guard mirrors openSubagent so
	// savedMainLine is captured once, from main, on any future nesting path.
	if !m.inDetail() {
		m.savedMainLine = m.main.lineForRow(m.vp.YOffset)
	}
	label := msg.ToolSummary
	if label == "" {
		label = "tool result"
	}
	// A non-collapsed RoleTool message so renderTranscript shows the body inline.
	// Carry Diff so an Edit/Write body renders as a colored diff (A3 renderDiffBody).
	detail := renderTranscript(
		[]vault.TranscriptMessage{{Role: vault.RoleTool, Body: msg.Body, Diff: msg.Diff}},
		m.styles, m.contentWidth(),
	)
	m.inSub = false
	m.subID = ""
	m.inInline = true
	m.inlineLabel = label
	return m.setActive(detail, 0)
}

// returnToMain restores the main transcript at the source line that was on top
// when the detail (subagent or inline body) was opened (re-derived to the current
// wrap width).
func (m viewerModel) returnToMain() viewerModel {
	if !m.inDetail() {
		return m
	}
	m.inSub = false
	m.subID = ""
	m.inInline = false
	m.inlineLabel = ""
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
	rows[row] = m.styles.markerRowFor(m.active.messages[mi], true)
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
	if m.inDetail() {
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
		if m.inDetail() {
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

// focusMarker moves the focused openable marker by delta (±1), scrolling to it
// only when it isn't already on screen. No-op when the active transcript has no
// openable markers.
//
// The reference point depends on whether the focused marker is still on screen
// (A4). If it is visible, ]/[ step sequentially from it (the classic cycle, with
// wraparound) — so repeatedly tapping ] walks marker-by-marker. If it has been
// scrolled out of view (or nothing is focused yet), the next/prev marker is
// resolved from the current viewport top instead, so navigation tracks where the
// user scrolled rather than replaying from a stale focusedMarker±1. The visible
// branch also sidesteps a clamped-offset trap: a marker in the last viewport
// height of the transcript can't sit at the top (the viewport clamps YOffset), so
// a YOffset-only "next" would re-select it forever — stepping from the marker
// index escapes that.
//
// Scrolling is conditional: when the newly-focused marker is already visible we
// leave the viewport where it is and only repaint the highlight, so walking ]/[
// through a screenful of markers doesn't jump the reading position. Only an
// off-screen target scrolls (to the top, via SetYOffset).
func (m viewerModel) focusMarker(delta int) viewerModel {
	n := len(m.active.markers)
	if n == 0 {
		return m
	}
	var next int
	if m.focusedMarkerVisible() {
		next = (m.focusedMarker + delta + n) % n
	} else {
		next = m.active.markerFromViewport(m.vp.YOffset, delta)
	}
	m.focusedMarker = next
	// Re-render so the newly-focused marker is highlighted (not just scrolled into
	// view). Then scroll only if it's off-screen — focusedMarkerVisible now reports
	// on `next`, since m.focusedMarker == next.
	m.vp.SetContent(m.viewportContent())
	if !m.focusedMarkerVisible() {
		if row := m.active.rowForMarker(next); row >= 0 {
			m.vp.SetYOffset(row)
		}
	}
	return m
}

// focusedMarkerVisible reports whether the focused marker's start row currently
// falls within the viewport's visible rows. It decides which reference focusMarker
// uses (see there): visible → sequential step from the marker index; not visible
// (scrolled away, or nothing focused) → resolve from the viewport top.
func (m viewerModel) focusedMarkerVisible() bool {
	row := m.active.rowForMarker(m.focusedMarker)
	if row < 0 {
		return false
	}
	// Visible rows are the half-open interval [YOffset, YOffset+Height): a row equal
	// to YOffset+Height is the first row scrolled off the bottom.
	return row >= m.vp.YOffset && row < m.vp.YOffset+m.vp.Height
}

// openFocusedMarker opens the focused marker standalone, scrolled to its top:
// a subagent transcript, or a collapsed tool_result's inline body (A1). No-op when
// no marker is focused.
func (m viewerModel) openFocusedMarker() viewerModel {
	if m.focusedMarker < 0 || m.focusedMarker >= len(m.active.markers) {
		return m
	}
	mi := m.active.markers[m.focusedMarker]
	msg := m.active.messages[mi]
	switch {
	case msg.Role == vault.RoleSubagent:
		if !msg.Openable || msg.AgentID == "" {
			return m
		}
		return m.openSubagent(msg.AgentID, 0)
	case msg.Role == vault.RoleTool && msg.Collapsed:
		return m.openInlineContent(msg)
	}
	return m
}

func (m viewerModel) View() string {
	if !m.ready {
		return "no session loaded"
	}
	return strings.Join([]string{m.header(), m.vp.View(), m.helpLine()}, "\n")
}

// currentMessage returns the transcript message rendered at the top of the
// viewport — what the `c` (copy) key acts on. It is the last message whose
// rendered rows start at or before the current scroll offset (the message the
// user is reading). Returns false for an unloaded or empty transcript.
func (m viewerModel) currentMessage() (vault.TranscriptMessage, bool) {
	if !m.ready || len(m.active.messages) == 0 {
		return vault.TranscriptMessage{}, false
	}
	idx := 0
	for i, start := range m.active.msgRowStart {
		if start <= m.vp.YOffset {
			idx = i
		} else {
			break
		}
	}
	return m.active.messages[idx], true
}

func (m viewerModel) header() string {
	title := strings.TrimSpace(m.sess.Title)
	if title == "" {
		title = "(untitled)"
	}
	if m.inSub {
		title = fmt.Sprintf("%s › subagent %s", title, shortID(m.subID))
	} else if m.inInline {
		title = fmt.Sprintf("%s › %s", title, m.inlineLabel)
	}
	loc := fmt.Sprintf("%s · %s", shortID(m.sess.UUID), displayPath(m.sess.ProjectPath))
	return m.styles.Title.Render(truncate(title, max(1, m.width-len(loc)-3))) +
		"  " + m.styles.StatusBar.Render(loc)
}

func (m viewerModel) helpLine() string {
	keys := "j/k scroll · g/G top/bottom · c copy · r/R restore/resume · q back"
	if len(m.active.markers) > 0 {
		keys = "j/k scroll · ]/[ marker · enter open · c copy · r/R restore/resume · q back"
	}
	if m.inDetail() {
		keys = "j/k scroll · c copy · esc/q return to session"
	}
	return m.styles.Help.Render(keys)
}

// contentWidth is the wrap width for body text (a small right margin avoids the
// terminal's last column). It falls back to a sane default before the first
// WindowSizeMsg arrives: launching straight into view mode (`vault show <id>
// --tui`) renders the transcript in newModel while m.width is still 0, and
// wrapping a whole transcript to 1 column would allocate enormously; the real
// width re-wraps it on the first resize.
func (m viewerModel) contentWidth() int {
	if m.width <= 0 {
		return 80
	}
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
