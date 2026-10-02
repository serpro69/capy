package tui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/serpro69/capy/internal/vault"
)

type viewerTargetKind uint8

const (
	viewerTargetMain viewerTargetKind = iota + 1
	viewerTargetSidecar
	viewerTargetTool
)

// A tool inherits its containing transcript's source, including a sidecar's
// platform and archive path. The owning session never changes on local opens.
type viewerTranscriptSource struct {
	session  string
	subagent string
	platform vault.Platform
}

type viewerTarget struct {
	kind   viewerTargetKind
	source viewerTranscriptSource
	scope  string
	label  string
	// Tool provenance uses the original scope and message/field, never a
	// SourceLine lookup (several messages can share one physical JSONL line).
	ownerScope string
	origin     findPosition
	// A search-selected tool is a presentation of its immediate parent's scope.
	// It occupies one replaceable frame until a visible hit, clear, or back.
	searchSelected bool
}

// A frame owns one target's presentation and committed search. Parsed messages,
// rendered rows, corpus, projection and result slices are immutable and shared.
// Viewport offsets, marker focus and query/selection are values. clone detaches
// work/editor state; neither a suspended frame nor a transaction owns a worker.
type viewerTargetFrame struct {
	target        viewerTarget
	active        renderedTranscript
	vp            viewport.Model
	focusedMarker int
	find          findController
	wrapWidth     int
	anchor        findPosition // captured when suspended; vp owns the live offset
}

func (f viewerTargetFrame) clone() viewerTargetFrame {
	f.find = findController{
		ctx: f.find.ctx, epoch: f.find.epoch, revision: f.find.revision,
		layout: f.find.layout, applied: f.find.applied, view: f.find.view,
		err: f.find.err, wrapped: f.find.wrapped,
	}
	return f
}

func (m viewerModel) inDetail() bool {
	return m.target.kind == viewerTargetSidecar || m.target.kind == viewerTargetTool
}

func (m viewerModel) activePlatform() vault.Platform { return m.target.source.platform.OrClaude() }

// targetSnapshot is also the editor's transaction snapshot. It captures actual
// prepared width and any restoration in flight, not merely terminal dimensions.
func (m viewerModel) targetSnapshot() findSnapshot {
	f := m.viewerTargetFrame.clone()
	f.anchor = m.findReadingAnchor()
	if f.find.view.plain {
		f.wrapWidth = f.find.view.projection.width
	} else if m.find.latest != nil && m.find.latest.normal {
		f.wrapWidth = m.find.view.normalWidth
	}
	snapshot := findSnapshot{frame: f, parents: m.parents}
	if desired := m.find.latest; desired != nil && (desired.normal || desired.restoreAnchor) {
		snapshot.normalRestore, snapshot.frame.anchor = desired.normal, desired.anchor
	}
	return snapshot
}

func (m viewerModel) pushTarget(target viewerTarget, messages []vault.TranscriptMessage, line int) viewerModel {
	if m.find.editing {
		m = m.cancelFind()
	}
	saved := m.targetSnapshot()
	// A pending clear must survive suspension even if plain rows are still on
	// screen. Retain its desired restoration in the saved controller.
	if saved.normalRestore {
		r := findRequest{normal: true, restoreAnchor: true, anchor: saved.frame.anchor}
		saved.frame.find.latest = &r
	}
	// Force append to detach the slice from earlier Bubble Tea value models.
	m.parents = append(m.parents[:len(m.parents):len(m.parents)], saved.frame)
	m = m.resetFind()
	m.target = target
	rt := renderTranscript(m.activePlatform(), messages, m.styles, m.contentWidth())
	return m.setActive(rt, rt.rowForLine(line))
}

func (m viewerModel) returnToParent() viewerModel {
	n := len(m.parents)
	if n == 0 {
		return m
	}
	saved := m.parents[n-1]
	// Copy the retained prefix so discarded targets do not stay in the backing
	// array, and previous value models retain their own stack.
	m.parents = append([]viewerTargetFrame(nil), m.parents[:n-1]...)
	m = m.resetFind()
	return m.restoreTarget(saved)
}

// restoreTarget resumes a detached frame under the caller's fresh execution
// epoch. Local back, child return and raw return share the same position and
// pending-clear contract. Set the viewer dimensions before calling.
func (m viewerModel) restoreTarget(saved viewerTargetFrame) viewerModel {
	epoch, ctx := m.find.epoch, m.find.ctx
	m.viewerTargetFrame = saved.clone()
	m.find.epoch, m.find.ctx = epoch, ctx
	m.find.applied = m.find.id()
	m.vp.Width, m.vp.Height = m.width, m.viewportHeight()
	m.vp.SetYOffset(m.vp.YOffset)
	if desired := saved.find.latest; desired != nil && desired.normal {
		m = m.queueFind("", saved.anchor, saved.anchor, true, true)
		m.find.latest.restoreAnchor = true
		return m
	}
	if m.wrapWidth == m.contentWidth() {
		return m
	}
	if m.find.view.plain {
		m = m.queueFind(m.find.view.query, saved.anchor, saved.anchor, true, false)
		m.find.latest.restoreAnchor = true
		return m
	}
	m.active = renderTranscript(m.activePlatform(), m.active.messages, m.styles, m.contentWidth())
	m.wrapWidth = m.contentWidth()
	m.vp.SetContent(m.viewportContent())
	m.vp.SetYOffset(m.rowForMessage(saved.anchor.message))
	return m
}

func (m viewerModel) rowForMessage(message int) int {
	if message >= 0 && message < len(m.active.msgRowStart) {
		return m.active.msgRowStart[message]
	}
	return 0
}

// jumpTo preserves global FTS's transcript/SourceLine contract. Global entry
// returns through the main frame, independently of any local nested detail.
func (m viewerModel) jumpTo(subagentID string, line int) viewerModel {
	if subagentID != "" && m.subagentBytes(subagentID) == nil {
		return m
	}
	for len(m.parents) > 0 {
		m = m.returnToParent()
	}
	if subagentID != "" {
		return m.openSubagent(subagentID, line)
	}
	m.vp.SetYOffset(m.active.rowForLine(line))
	return m
}

func (m viewerModel) openSubagent(id string, line int) viewerModel {
	raw := m.subagentBytes(id)
	if raw == nil {
		return m
	}
	target := viewerTarget{
		kind: viewerTargetSidecar, scope: m.sess.UUID + "/" + vault.SubagentRelPath(id),
		source: viewerTranscriptSource{session: m.sess.UUID, subagent: id, platform: vault.PlatformClaudeCode},
	}
	return m.pushTarget(target, vault.ParseTranscript(vault.PlatformClaudeCode, raw, nil), line)
}

// Open by ordinal to keep exact provenance even for duplicate SourceLine/body
// values. The message copy retains the original Body, summary and diff flag.
func (m viewerModel) openInlineContent(message int) viewerModel {
	if message < 0 || message >= len(m.active.messages) {
		return m
	}
	msg := m.active.messages[message]
	target := toolViewerTarget(m.target, msg, findPosition{message: message, field: findBody})
	msg.Collapsed = false
	return m.pushTarget(target, []vault.TranscriptMessage{msg}, 0)
}

func toolViewerTarget(owner viewerTarget, msg vault.TranscriptMessage, origin findPosition) viewerTarget {
	label := msg.ToolSummary
	if label == "" {
		label = "tool result"
	}
	return viewerTarget{
		kind: viewerTargetTool, source: owner.source,
		scope: fmt.Sprintf("%s/tool/%d", owner.scope, origin.message), label: label,
		ownerScope: owner.scope, origin: origin,
	}
}

func (m viewerModel) findOwnerTarget() viewerTarget {
	if m.target.searchSelected {
		return m.parents[len(m.parents)-1].target
	}
	return m.target
}

// Keep the live controller while exchanging presentation frames. Search-driven
// navigation saves the owner once; subsequent hidden hits replace that target.
func (m viewerModel) showFindTarget(target viewerTarget) viewerModel {
	switch {
	case target.searchSelected && !m.target.searchSelected:
		m.parents = append(m.parents[:len(m.parents):len(m.parents)], m.targetSnapshot().frame)
		m.focusedMarker = -1
	case !target.searchSelected && m.target.searchSelected:
		n := len(m.parents)
		m.focusedMarker = m.parents[n-1].focusedMarker
		m.parents = append([]viewerTargetFrame(nil), m.parents[:n-1]...)
	}
	m.target = target
	return m
}

// Clear the saved owner as well as the displayed result so back cannot revive
// the query. A pending normal rewrap is retained by the existing frame contract.
func (m viewerModel) clearFindOwner() viewerModel {
	if !m.target.searchSelected {
		return m
	}
	n := len(m.parents)
	owner := m
	owner.viewerTargetFrame = m.parents[n-1].clone()
	owner = owner.clearFindAt(m.target.origin)
	saved := owner.targetSnapshot()
	if saved.normalRestore {
		saved.frame.find.latest = &findRequest{normal: true, restoreAnchor: true, anchor: saved.frame.anchor}
	}
	m.parents = append([]viewerTargetFrame(nil), m.parents...)
	m.parents[n-1] = saved.frame
	return m
}
