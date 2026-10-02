package tui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/serpro69/capy/internal/vault"
)

// Only Update mutates a controller. Workers receive value requests referring to
// immutable corpus/projection data; snapshots share those caches, never input or
// mutable selection. Each local frame owns its controller and committed view.
type findViewState struct {
	query      string
	corpus     *findCorpus
	projection *findProjection
	// The owner projection survives temporary tool targets; only the currently
	// displayed detail is cached, bounding memory across repeated n/N cycles.
	ownerProjection *findProjection
	hits            []findHit
	selected        int
	plain           bool

	normal      renderedTranscript
	normalVP    viewport.Model
	normalWidth int
	normalFocus int
}

type findSnapshot struct {
	frame         viewerTargetFrame
	parents       []viewerTargetFrame // immutable stack snapshot; edits detach it
	normalRestore bool
}

type findWorkID struct{ epoch, revision, layout uint64 }

type findRequest struct {
	id            findWorkID
	view          findViewState
	query         string
	scope         string
	platform      vault.Platform
	styles        Styles
	width         int
	anchor        findPosition
	fallback      findPosition
	keepSelection bool
	normal        bool
	restoreAnchor bool
	focusedMarker int
	toolDetail    bool
	target        viewerTarget // fallback/restore presentation
	owner         viewerTarget // query/corpus scope
	wrapped       bool
	navigation    bool // selection is pending, not part of the applied frame yet
}

type findResultMsg struct {
	id      findWorkID
	view    findViewState
	offset  int
	err     error
	target  viewerTarget
	wrapped bool
}

type findController struct {
	// Like Model.ctx, this is the program lifetime context: Bubble Tea's fixed
	// Update signature cannot thread it as a parameter into asynchronous commands.
	ctx      context.Context
	epoch    uint64
	revision uint64
	layout   uint64
	running  findWorkID
	cancel   context.CancelFunc
	pending  *findRequest // replaceable latest request, at most one
	latest   *findRequest // immutable desired presentation until application
	applied  findWorkID
	submit   uint64

	view     findViewState
	input    textinput.Model
	editing  bool
	snapshot *findSnapshot
	anchor   findPosition // fixed for an entire editing transaction
	err      string
	wrapped  bool
}

func (f findController) id() findWorkID { return findWorkID{f.epoch, f.revision, f.layout} }

func (m viewerModel) resetFind() viewerModel {
	if m.find.cancel != nil {
		m.find.cancel()
	}
	m.findEpoch = max(m.findEpoch, m.find.epoch) + 1
	m.find = findController{ctx: m.find.ctx, epoch: m.findEpoch}
	return m
}

func (m viewerModel) findReadingAnchor() findPosition {
	if f := m.find.view; f.plain && f.projection != nil {
		return f.projection.positionForRow(f.corpus, m.vp.YOffset)
	}
	mi := max(0, sort.Search(len(m.active.msgRowStart), func(i int) bool { return m.active.msgRowStart[i] > m.vp.YOffset })-1)
	return findPosition{message: mi}
}

func (m viewerModel) startFind() viewerModel {
	if !m.ready {
		return m
	}
	snapshot := m.targetSnapshot()
	if !m.find.view.plain {
		m.find.view.normal, m.find.view.normalVP = m.active, m.vp
		m.find.view.normalWidth = snapshot.frame.wrapWidth
		m.find.view.normalFocus = m.focusedMarker
		// First entry needs a normal-render restoration source too.
		snapshot.frame.find.view = m.find.view
	}
	anchor := m.findReadingAnchor()
	m.find.snapshot = &snapshot
	m.find.anchor = anchor
	m.find.editing = true
	m.find.input = textinput.New()
	// The widget's Ctrl+V uses external clipboard utilities. Local search must
	// stay in-process; terminal-delivered (bracketed) paste is still ordinary input.
	m.find.input.KeyMap.Paste.SetEnabled(false)
	m.find.input.Prompt = "/ "
	m.find.input.CharLimit = 256
	m.find.input.SetValue(m.find.view.query)
	m.find.input.CursorEnd()
	m.find.input.Focus()
	m = m.sizeFindInput()
	return m.queueFind(m.find.input.Value(), anchor, anchor, false, false)
}

func (m viewerModel) sizeFindInput() viewerModel {
	boundInputWidth(&m.find.input, m.width, len(m.findCounter()))
	return m
}

func (m viewerModel) queueFind(query string, anchor, fallback findPosition, keepSelection, normal bool) viewerModel {
	m.find.revision++
	m.find.submit = 0
	m.find.err = ""
	if m.find.cancel != nil {
		m.find.cancel()
	}
	owner := m.findOwnerTarget()
	target := m.target
	if query == "" && !normal && m.find.snapshot != nil {
		target = m.find.snapshot.frame.target
	}
	r := findRequest{
		id: m.find.id(), view: m.find.view, query: query, scope: owner.scope,
		platform: m.activePlatform(), styles: m.styles, width: m.contentWidth(),
		anchor: anchor, fallback: fallback, keepSelection: keepSelection, normal: normal,
		focusedMarker: m.focusedMarker, toolDetail: owner.kind == viewerTargetTool,
		target: target, owner: owner,
	}
	m.find.pending = &r
	m.find.latest = &r
	return m
}

// The scheduler does not clear running on cancellation. Its typed completion
// retires the slot before starting the latest pending request, even when stale.
func (m viewerModel) nextFindCommand() (viewerModel, tea.Cmd) {
	if m.find.running != (findWorkID{}) || m.find.pending == nil {
		return m, nil
	}
	r := *m.find.pending
	m.find.pending = nil
	ctx, cancel := context.WithCancel(m.find.ctx)
	m.find.cancel = cancel
	m.find.running = r.id
	return m, func() tea.Msg {
		defer cancel()
		return runFindRequest(ctx, r)
	}
}

// A manually opened tool is its own scope, with separate summary and Body
// fields. Ordinary transcripts keep the usual no-duplication policy. Parsed
// messages and the published corpus remain immutable.
func buildFindScopeCorpus(ctx context.Context, scope string, messages []vault.TranscriptMessage, toolDetail bool) (*findCorpus, error) {
	if !toolDetail || len(messages) == 0 {
		return buildFindCorpus(ctx, scope, messages)
	}
	msg := messages[0]
	msg.Collapsed = true
	corpus, err := buildFindCorpus(ctx, scope, []vault.TranscriptMessage{msg})
	if err != nil {
		return nil, err
	}
	for i := range corpus.lines {
		corpus.lines[i].hidden = false
	}
	return corpus, nil
}

func runFindRequest(ctx context.Context, r findRequest) findResultMsg {
	result := findResultMsg{id: r.id, view: r.view, target: r.target, wrapped: r.wrapped}
	f := &result.view
	if err := ctx.Err(); err != nil {
		result.err = err
		return result
	}
	if r.normal {
		rewrap := f.normalWidth != r.width
		if rewrap {
			f.normal = renderTranscript(r.platform, f.normal.messages, r.styles, r.width)
			f.normalWidth = r.width
		}
		if rewrap || f.normalFocus != r.focusedMarker {
			f.normalVP = viewport.New(r.width, 1)
			f.normalVP.SetContent(f.normal.focusedContent(r.styles, r.focusedMarker))
			f.normalFocus = r.focusedMarker
		}
		f.query, f.hits, f.selected, f.plain = "", nil, -1, false
		if r.anchor.message < len(f.normal.msgRowStart) {
			result.offset = f.normal.msgRowStart[r.anchor.message]
		}
		result.err = ctx.Err()
		return result
	}
	if f.corpus == nil || f.corpus.scope != r.scope {
		f.corpus, result.err = buildFindScopeCorpus(ctx, r.scope, f.normal.messages, r.toolDetail)
		if result.err != nil {
			return result
		}
		f.projection, f.ownerProjection = nil, nil
	}
	if f.ownerProjection == nil || f.ownerProjection.width != r.width {
		f.ownerProjection, result.err = buildFindProjection(ctx, f.corpus, f.normal.messages, r.platform, r.styles, r.width)
		if result.err != nil {
			return result
		}
	}
	if !f.plain || f.query != r.query {
		f.hits, result.err = findExact(ctx, f.corpus, r.query)
		if result.err != nil {
			return result
		}
	}
	f.query, f.plain = r.query, true
	if !r.keepSelection || f.selected >= len(f.hits) {
		f.selected = -1
		if len(f.hits) > 0 {
			f.selected = sort.Search(len(f.hits), func(i int) bool { return !f.corpus.position(f.hits[i]).before(r.anchor) })
			if f.selected == len(f.hits) {
				f.selected = 0
			}
		}
	}
	if !r.restoreAnchor && f.selected >= 0 && f.selected < len(f.hits) {
		position := f.corpus.position(f.hits[f.selected])
		msg := f.normal.messages[position.message]
		result.target = r.owner
		if msg.Collapsed && !r.toolDetail {
			result.target = toolViewerTarget(r.owner, msg, position)
			result.target.searchSelected = true
		}
	}
	if result.target.searchSelected {
		message := result.target.origin.message
		if f.projection == nil || !f.projection.detail || f.projection.messageOffset != message || f.projection.width != r.width {
			f.projection, result.err = buildFindTargetProjection(ctx, f.corpus, f.normal.messages, r.platform, r.styles, r.width, message)
			if result.err != nil {
				return result
			}
		}
	} else {
		f.projection = f.ownerProjection
	}
	result.offset = f.projection.rowForPosition(f.corpus, r.fallback)
	if !r.restoreAnchor && f.selected >= 0 && f.selected < len(f.hits) {
		result.offset = f.projection.rowForHit(f.hits[f.selected])
	}
	result.err = ctx.Err()
	return result
}

func (m viewerModel) applyFindResult(result findResultMsg, apply bool) viewerModel {
	if result.id != m.find.running {
		return m // another controller's completion cannot retire this controller
	}
	m.find.running = findWorkID{}
	m.find.cancel = nil
	if !apply || result.id != m.find.id() {
		return m
	}
	if result.err != nil {
		if !errors.Is(result.err, context.Canceled) {
			m.find.err = "find error: " + result.err.Error()
		}
		return m
	}
	if !result.view.plain && result.view.normalFocus != m.focusedMarker {
		// Marker keys remain live while normal rendering is prepared. Never
		// install an overlay that disagrees with the target Enter would open.
		desired := m.find.latest
		return m.queueFind("", desired.anchor, desired.fallback, true, true)
	}
	m = m.showFindTarget(result.target)
	m.find.view = result.view
	m.find.wrapped = result.wrapped
	m.find.applied = result.id
	m.find.latest = nil
	m = m.showFindView(result.offset)
	if m.find.submit == result.id.revision {
		m = m.commitFind()
	}
	return m.sizeFindInput()
}

func (m viewerModel) showFindView(offset int) viewerModel {
	f := m.find.view
	if f.plain {
		m.active, m.vp = f.projection.transcript, f.projection.viewport
	} else {
		m.active, m.vp = f.normal, f.normalVP
	}
	if f.plain {
		m.wrapWidth = f.projection.width
	} else {
		m.wrapWidth = f.normalWidth
	}
	m.vp.Width, m.vp.Height = m.width, m.viewportHeight()
	m.vp.SetYOffset(offset)
	return m
}

func (m viewerModel) invalidateFind() viewerModel {
	m.find.revision++
	m.find.pending = nil
	m.find.latest = nil
	m.find.submit = 0
	if m.find.cancel != nil {
		m.find.cancel()
	}
	return m
}

func (m viewerModel) cancelFind() viewerModel {
	snapshot := m.find.snapshot
	m = m.invalidateFind()
	m.find.editing, m.find.snapshot, m.find.err = false, nil, ""
	if snapshot == nil {
		return m
	}
	// Keep the live scheduler so its cancelled completion can retire. Restore
	// the complete pre-edit frame's target and presentation.
	controller := m.find
	m.viewerTargetFrame = snapshot.frame.clone()
	m.parents = snapshot.parents
	m.find = controller
	m.find.view = snapshot.frame.find.view
	m.find.wrapped = snapshot.frame.find.wrapped
	m.vp.Width, m.vp.Height = m.width, m.viewportHeight()
	m.vp.SetYOffset(m.vp.YOffset)
	if snapshot.frame.wrapWidth == m.contentWidth() && !snapshot.normalRestore {
		m.find.applied = m.find.id()
		return m
	}
	normal := snapshot.normalRestore || !snapshot.frame.find.view.plain
	m = m.queueFind(m.find.view.query, snapshot.frame.anchor, snapshot.frame.anchor, true, normal)
	// The occurrence counter and the reading position are independent after
	// manual scrolling. Cancel restores both, rather than jumping to selection.
	m.find.latest.restoreAnchor = true
	return m
}

func (m viewerModel) commitFind() viewerModel {
	if m.find.input.Value() == "" {
		anchor := m.find.anchor
		m.find.editing, m.find.snapshot = false, nil
		return m.clearFindAt(anchor)
	}
	m.find.editing, m.find.snapshot, m.find.submit = false, nil, 0
	m.find.input.Blur()
	return m
}

func (m viewerModel) clearFindAt(anchor findPosition) viewerModel {
	if m.target.searchSelected {
		m = m.clearFindOwner()
		m.target.searchSelected = false
		// This becomes an ordinary manually scoped detail. Normal rendering is
		// prepared off Update; the old corpus remains only for the pending view.
		m.find.view.normal = renderedTranscript{messages: m.active.messages}
		m.find.view.normalWidth, m.find.view.normalFocus = 0, -1
		anchor.message = 0
	}
	m = m.invalidateFind()
	m.find.editing, m.find.snapshot, m.find.err = false, nil, ""
	m.find.view.query, m.find.view.hits, m.find.view.selected = "", nil, -1
	if m.find.view.normalWidth != m.contentWidth() || m.find.view.normalFocus != m.focusedMarker {
		return m.queueFind("", anchor, anchor, false, true)
	}
	m.find.view.plain = false
	offset := 0
	if anchor.message < len(m.find.view.normal.msgRowStart) {
		offset = m.find.view.normal.msgRowStart[anchor.message]
	}
	m.find.applied = m.find.id()
	return m.showFindView(offset)
}

func (m viewerModel) clearFind() viewerModel {
	anchor := m.findReadingAnchor()
	f := m.find.view
	if f.selected >= 0 && f.selected < len(f.hits) {
		anchor = f.corpus.position(f.hits[f.selected])
	}
	return m.clearFindAt(anchor)
}

func (m viewerModel) updateFindInput(msg tea.Msg) (viewerModel, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "esc":
			return m.cancelFind(), nil
		case "enter":
			if m.find.err != "" {
				m = m.queueFind(m.find.input.Value(), m.find.anchor, m.findReadingAnchor(), false, false)
			}
			if m.find.applied == m.find.id() {
				return m.commitFind(), nil
			}
			m.find.submit = m.find.revision
			return m, nil
		}
	}
	before := m.find.input.Value()
	var cmd tea.Cmd
	m.find.input, cmd = m.find.input.Update(msg)
	if query := m.find.input.Value(); query != before {
		fallback := m.findReadingAnchor()
		if query == "" {
			fallback = m.find.anchor
		}
		m = m.queueFind(query, m.find.anchor, fallback, false, false)
	}
	return m, cmd
}

func (m viewerModel) stepFind(delta int) viewerModel {
	view := m.find.view
	if m.find.applied != m.find.id() {
		if m.find.latest == nil || !m.find.latest.navigation {
			return m
		}
		view = m.find.latest.view // repeated keys advance the latest requested hit
	}
	n := len(view.hits)
	if n == 0 {
		return m
	}
	next := view.selected + delta
	selected := (next + n) % n
	anchor := view.corpus.position(view.hits[selected])
	m = m.queueFind(view.query, anchor, anchor, true, false)
	// Selection belongs to the prepared target. Keep the applied view coherent
	// if slash/cancel snapshots it before this navigation command completes.
	m.find.latest.view = view
	m.find.latest.view.selected = selected
	m.find.latest.wrapped = next < 0 || next >= n
	m.find.latest.navigation = true
	return m
}

func (m viewerModel) resizeFind() viewerModel {
	m.find.layout++
	anchor := m.findReadingAnchor()
	query := m.find.view.query
	if m.find.editing {
		query = m.find.input.Value()
		anchor = m.find.anchor
	}
	submit := m.find.submit != 0
	keep := m.find.view.plain && query == m.find.view.query
	normal := !m.find.view.plain && !m.find.editing
	restoreAnchor := false
	navigation := m.find.latest != nil && m.find.latest.navigation
	requested := m.find.latest
	if desired := m.find.latest; desired != nil && (desired.normal || desired.restoreAnchor) {
		// A restore can still be preparing while a different presentation is
		// visible. Resizing follows the requested target, never that old view.
		query, anchor = desired.query, desired.anchor
		normal, restoreAnchor, keep = desired.normal, desired.restoreAnchor, true
	}
	m = m.queueFind(query, anchor, anchor, keep, normal)
	m.find.latest.restoreAnchor = restoreAnchor
	if navigation {
		// Width changes supersede prepared rows, not the requested occurrence.
		m.find.latest.view = requested.view
		m.find.latest.wrapped = requested.wrapped
		m.find.latest.navigation = true
	}
	if submit {
		m.find.submit = m.find.revision
	}
	return m.sizeFindInput()
}

func (m viewerModel) findCounter() string {
	if m.find.err != "" {
		return m.find.err
	}
	if m.find.pending != nil || m.find.running != (findWorkID{}) && m.find.applied != m.find.id() {
		return "finding…"
	}
	f := m.find.view
	if f.query == "" {
		return "find · plain text"
	}
	if len(f.hits) == 0 {
		return "no matches"
	}
	count := fmt.Sprintf("%d/%d", f.selected+1, len(f.hits))
	if m.find.wrapped {
		count += " · wrapped"
	}
	return count
}

func (m viewerModel) findFooter() string {
	counter := m.findCounter()
	value := "/ " + m.find.view.query
	if m.find.editing {
		value = m.find.input.View()
	}
	width := max(1, m.width)
	countWidth := ansi.StringWidth(counter)
	if countWidth+4 >= width {
		return ansi.Truncate(counter, width, "")
	}
	return ansi.Truncate(value, width-countWidth-2, "") + "  " + counter
}

func (m viewerModel) findViewport() string {
	f := m.find.view
	hits, selected := f.hits, f.selected
	if m.find.applied != m.find.id() {
		hits, selected = nil, -1 // never paint an obsolete query's highlights
	}
	rows := make([]string, m.vp.Height)
	for i := range rows {
		ri := m.vp.YOffset + i
		if ri >= len(f.projection.rows) {
			break
		}
		rows[i] = f.projection.highlightRow(f.corpus, ri, hits, selected, m.styles)
		if m.focusedMarker >= 0 && m.active.rowForMarker(m.focusedMarker) == ri {
			rows[i] = strings.Replace(rows[i], "▸", "▶", 1)
		}
		rows[i] = ansi.Truncate(rows[i], max(1, m.width), "")
	}
	return strings.Join(rows, "\n")
}

func (m viewerModel) findView() string {
	height := max(1, m.height)
	footer := m.findFooter()
	if height == 1 {
		return footer
	}
	label := "find · plain text · n/N next/previous · esc clear · q back"
	if m.find.editing {
		label = "find · plain text · enter commit · esc cancel"
	}
	header := ansi.Truncate(label, max(1, m.width), "")
	if height == 2 {
		return header + "\n" + footer
	}
	body := m.vp.View()
	if m.find.view.plain && m.find.view.projection != nil {
		body = m.findViewport()
	}
	// Search gives the footer priority at small sizes. It deliberately omits
	// the optional original-project metadata row while input owns the screen.
	rows := strings.Split(body, "\n")
	rows = rows[:min(len(rows), height-2)]
	for i := range rows {
		rows[i] = ansi.Truncate(rows[i], max(1, m.width), "")
	}
	return header + "\n" + strings.Join(rows, "\n") + "\n" + footer
}
