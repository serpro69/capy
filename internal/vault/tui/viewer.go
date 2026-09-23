package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/serpro69/capy/internal/vault"
)

// viewerActionKind enumerates what the viewer asks the app to do after an
// Update. The zero value is "nothing", so a viewerAction{} is a no-op.
type viewerActionKind int

const (
	viewerActionNone      viewerActionKind = iota
	viewerActionBack                       // leave the viewer (return to the previous mode)
	viewerActionOpenChild                  // open the child SESSION named by childUUID (root-routed)
)

// viewerAction signals the app what to do after a viewer Update. It is a struct
// rather than a bare enum because opening a child session must carry the uuid:
// the viewer owns no store handle (see viewerModel), so it cannot load the child
// itself and instead hands the root Model (app.go) the identity to resolve
// through dataStore (design § TUI — root-routed child open). viewerNone and
// viewerBack are the two payload-free values; openChildAction builds the third.
type viewerAction struct {
	kind      viewerActionKind
	childUUID string // viewerActionOpenChild only
}

// Logically constants (a struct cannot be a Go const) — never reassign them; the
// app dispatches on action.kind and the tests compare against these values.
var (
	viewerNone = viewerAction{}
	viewerBack = viewerAction{kind: viewerActionBack}
)

// openChildAction is the viewer's request to open the child session uuid.
func openChildAction(uuid string) viewerAction {
	return viewerAction{kind: viewerActionOpenChild, childUUID: uuid}
}

// viewerChromeRows is the number of rows the viewer reserves outside the
// scrolling viewport: one header line + one help line. A differing original
// project path adds one more row (see viewportHeight).
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
	m.vp.Height = m.viewportHeight()
	m.files = files
	m.subIDs = sortedSubagentIDs(files)
	m.inSub = false
	m.subID = ""
	m.inInline = false
	m.inlineLabel = ""
	m.savedMainLine = 0
	// The stored platform (migration 0006) selects the decoder for the main
	// transcript; sidecars stay Claude (openSubagent). OrClaude: a Session built
	// in memory without the field (tests) is a Claude session.
	p := m.platform()
	m.main = renderTranscript(p, vault.ParseTranscript(p, sess.RawJSONL, m.subIDs), m.styles, m.contentWidth())
	m.ready = true
	return m.setActive(m.main, 0)
}

// platform is the loaded session's platform with the in-memory zero value
// resolved to Claude (Platform.OrClaude) — the value every decode and render of
// the MAIN transcript dispatches on. Sidecar detail views use PlatformClaudeCode
// directly (see activePlatform).
func (m viewerModel) platform() vault.Platform { return m.sess.Platform.OrClaude() }

// activePlatform is the platform the ACTIVE transcript renders under: Claude
// for a subagent sidecar (a Claude Code concept, whatever the session's
// platform), the session's platform otherwise (main, or an inline tool body
// lifted from main). It only affects the assistant header label.
func (m viewerModel) activePlatform() vault.Platform {
	if m.inSub {
		return vault.PlatformClaudeCode
	}
	return m.platform()
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
	// Sidecars are always Claude JSONL (a Claude Code concept), whatever the
	// session's platform.
	sub := renderTranscript(vault.PlatformClaudeCode, vault.ParseTranscript(vault.PlatformClaudeCode, raw, nil), m.styles, m.contentWidth())
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
	detail := renderTranscript(m.platform(),
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
	m.vp.Height = m.viewportHeight()
	if !m.ready {
		return m
	}
	// Re-wrap at the new width. Capture the top source line BEFORE re-rendering
	// (the offset is in old-render row space), then restore it via rowForLine in
	// the new render so the scroll position survives the re-wrap.
	topLine := m.active.lineForRow(m.vp.YOffset)
	m.main = renderTranscript(m.platform(), m.main.messages, m.styles, m.contentWidth())
	if m.inDetail() {
		m.active = renderTranscript(m.activePlatform(), m.active.messages, m.styles, m.contentWidth())
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
		var action viewerAction
		m, action = m.openFocusedMarker()
		return m, nil, action
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

// openFocusedMarker opens the focused marker. Two targets are viewer-local and
// open standalone, scrolled to their top: a Claude subagent sidecar (AgentID —
// its bytes are already in m.files) and a collapsed tool_result's inline body
// (A1). The third — a marker carrying a ChildUUID, i.e. a Codex sub-agent that
// is its own archived SESSION — needs a store read the viewer cannot perform,
// so it returns an openChildAction for the root Model to resolve (design § TUI)
// and leaves the viewer untouched. No-op (viewerNone) when no marker is focused.
func (m viewerModel) openFocusedMarker() (viewerModel, viewerAction) {
	if m.focusedMarker < 0 || m.focusedMarker >= len(m.active.markers) {
		return m, viewerNone
	}
	mi := m.active.markers[m.focusedMarker]
	msg := m.active.messages[mi]
	switch {
	case msg.Role == vault.RoleSubagent:
		if !msg.Openable {
			return m, viewerNone
		}
		if msg.ChildUUID != "" {
			return m, openChildAction(msg.ChildUUID)
		}
		if msg.AgentID == "" {
			return m, viewerNone
		}
		return m.openSubagent(msg.AgentID, 0), viewerNone
	case msg.Role == vault.RoleTool && msg.Collapsed:
		return m.openInlineContent(msg), viewerNone
	}
	return m, viewerNone
}

func (m viewerModel) View() string {
	if !m.ready {
		return "no session loaded"
	}
	rows := []string{m.header()}
	if m.hasOriginalPath() {
		rows = append(rows, fitRow(m.styles.StatusBar.Render("Original path: "+displayPath(m.sess.ProjectPath)), m.width))
	}
	return strings.Join(append(rows, m.vp.View(), m.helpLine()), "\n")
}

func (m viewerModel) hasOriginalPath() bool {
	return m.sess.EffectiveProject() != m.sess.ProjectPath
}

func (m viewerModel) viewportHeight() int {
	rows := viewerChromeRows
	if m.hasOriginalPath() {
		rows++
	}
	return max(1, m.height-rows)
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

// setSessionMeta refreshes the loaded session's metadata (title/name state)
// after a rename without touching the parsed transcript or sidecars: sess comes
// from a metadata-only store read, so the archived raw bytes already held by
// the viewer are retained. No-op when the viewer is empty or shows a different
// session.
func (m viewerModel) setSessionMeta(sess vault.Session) viewerModel {
	if !m.ready || m.sess.UUID != sess.UUID {
		return m
	}
	sess.RawJSONL = m.sess.RawJSONL
	m.sess = sess
	m.vp.Height = m.viewportHeight()
	return m
}

func (m viewerModel) header() string {
	title := strings.TrimSpace(m.sess.EffectiveTitle())
	if title == "" {
		title = "(untitled)"
	}
	if m.inSub {
		title = fmt.Sprintf("%s › subagent %s", title, shortID(m.subID))
	} else if m.inInline {
		title = fmt.Sprintf("%s › %s", title, m.inlineLabel)
	}
	// The location segment names the platform, and for a child session (a Codex
	// sub-agent rollout with parent_uuid set) its parent — "Codex · child of
	// 019dc606f552" (design § TUI) — so the user always knows which level of a
	// parent → child chain the viewer is showing.
	loc := fmt.Sprintf("%s · %s", shortID(m.sess.UUID), m.platform().DisplayName())
	if m.sess.ParentUUID != "" {
		loc += " · child of " + shortID(m.sess.ParentUUID)
	}
	loc += " · " + displaySessionProject(m.sess)
	// The title budget is measured in display cells (lipgloss.Width), not bytes:
	// loc carries several multibyte "·" separators, and a byte count would
	// truncate the title more aggressively than the terminal requires.
	return fitRow(m.styles.Title.Render(truncate(title, max(1, m.width-lipgloss.Width(loc)-3)))+
		"  "+m.styles.StatusBar.Render(loc), m.width)
}

func (m viewerModel) helpLine() string {
	keys := "j/k scroll · g/G top/bottom · c copy · e rename · r/R restore/resume · q back"
	if len(m.active.markers) > 0 {
		keys = "j/k scroll · ]/[ marker · enter open · c copy · e rename · r/R restore/resume · q back"
	}
	if m.inDetail() {
		// e still renames the owning session from a detail view (app.updateView
		// handles it before delegating), so the help must keep advertising it.
		keys = "j/k scroll · c copy · e rename · esc/q return to session"
	}
	return fitRow(m.styles.Help.Render("v raw JSONL · "+keys), m.width)
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
