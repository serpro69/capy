package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/serpro69/capy/internal/terminal"
	"github.com/serpro69/capy/internal/vault"
)

type deletePhase int

const (
	deletePreparing deletePhase = iota
	deleteReady
	deleteApplying
)

type deleteReview struct {
	phase         deletePhase
	cancelPrepare context.CancelFunc
	plan          *vault.DeletePlan
	vp            viewport.Model
	content       string // escaped before wrapping, retained for terminal resizes
	showHelp      bool
}

type deletePreparedMsg struct {
	seq  uint64
	plan *vault.DeletePlan
	err  error
}

type deleteAppliedMsg struct {
	seq uint64
	err error
}

func withDroppedMarks(status string, n int) string {
	if n == 0 {
		return status
	}
	dropped := fmt.Sprintf("dropped %d mark(s)", n)
	if status == "" {
		return dropped
	}
	return dropped + " · " + status
}

func (m Model) beginDelete() (tea.Model, tea.Cmd) {
	if m.deleteNeedsReload {
		var err error
		m, _, err = m.reloadSessions()
		if err != nil {
			return m.withError("refreshing sessions failed: " + err.Error()), nil
		}
	}
	if len(m.list.marks) == 0 {
		if m.status != "" {
			return m, nil
		}
		return m.withStatus("Mark sessions with m or a before opening deletion review."), nil
	}
	if m.resolveRememberDeletions == nil {
		return m.withError("deletion policy unavailable"), nil
	}
	remember, err := m.resolveRememberDeletions()
	if err != nil {
		return m.withError("loading deletion policy: " + err.Error()), nil
	}
	ctx, cancel := context.WithCancel(m.ctx)
	ids := m.list.markedIDs()
	m.deleteSeq++
	seq, store := m.deleteSeq, m.store
	m.mode = modeDelete
	m.deletion = deleteReview{phase: deletePreparing, cancelPrepare: cancel}
	return m, func() tea.Msg {
		defer cancel()
		p, err := store.PrepareDelete(ctx, ids, vault.DeleteOptions{ExactIDs: true, Remember: remember})
		return deletePreparedMsg{seq, p, err}
	}
}

func (m Model) handleDeletePrepared(msg deletePreparedMsg) (tea.Model, tea.Cmd) {
	if m.mode != modeDelete || m.deletion.phase != deletePreparing || msg.seq != m.deleteSeq {
		return m, nil
	}
	m.deletion.cancelPrepare = nil
	if msg.err != nil {
		return m.deleteFailed(msg.err)
	}
	if msg.plan == nil {
		return m.deleteFailed(errors.New("vault returned no deletion plan"))
	}
	m.deletion.phase, m.deletion.plan = deleteReady, msg.plan
	m.deletion.content = deletePreviewText(msg.plan)
	m.deletion.vp = viewport.New(0, 0)
	m.deletion.resize(m.width, m.bodyHeight())
	return m, nil
}

func deletePreviewText(plan *vault.DeletePlan) string {
	var b strings.Builder
	targets := plan.Targets()
	fmt.Fprintf(&b, "Review deletion · %d explicit, %d added\n\n", plan.ExplicitCount(), len(targets)-plan.ExplicitCount())
	if plan.RemembersDeletions() {
		b.WriteString("Capy will remember each UUID and project. Future imports exclude these sessions. A merge can delete copies in other vaults. Capy cannot undo this deletion.\n")
	} else {
		b.WriteString("One-time local deletion. These sessions can return through a later import or merge.\n")
	}
	b.WriteString("Archived transcripts, sidecars, overrides and search entries will be removed. Original files stay on disk.\n")
	if len(plan.RetainedChildren) > 0 {
		fmt.Fprintf(&b, "%d direct child session(s) will stay archived.\n", len(plan.RetainedChildren))
	}
	for _, target := range targets {
		fmt.Fprintf(&b, "\nUUID: %s\nTitle: %s\nPlatform: %s\nProject: %s\n", terminal.EscapeField(target.UUID), terminal.EscapeField(target.Title), terminal.EscapeField(string(target.Platform.OrClaude())), terminal.EscapeField(target.Project))
		if len(targets) == 1 {
			if target.Project != target.OriginalPath {
				fmt.Fprintf(&b, "Original path: %s\n", terminal.EscapeField(target.OriginalPath))
			}
			fmt.Fprintf(&b, "Messages: %d\nDates: %s – %s\n", target.MessageCount, fmtDate(target.StartTime), fmtDate(target.EndTime))
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func (d *deleteReview) resize(width, height int) {
	width = max(1, width)
	d.showHelp = height > 1
	d.vp.Height = max(1, height-1)
	if d.vp.Width != width {
		d.vp.Width = width
		d.vp.SetContent(ansi.Hardwrap(d.content, width, true))
	}
	d.vp.SetYOffset(d.vp.YOffset)
}

func (d deleteReview) view() string {
	switch d.phase {
	case deletePreparing:
		return "Preparing deletion preview…"
	case deleteApplying:
		return "Deleting reviewed sessions…"
	default:
		if !d.showHelp {
			return d.vp.View()
		}
		return d.vp.View() + "\n" + fitRow("y delete · esc cancel · ↑/↓ scroll · pgup/pgdn page", d.vp.Width)
	}
}

// Bubble Tea normally consumes signal-generated quit/interrupt messages before
// Update. While applying, route them through the same wait as a keyboard quit.
func waitForDelete(model tea.Model, msg tea.Msg) tea.Msg {
	switch msg.(type) {
	case tea.QuitMsg, tea.InterruptMsg:
		if m, ok := model.(Model); ok && m.mode == modeDelete && m.deletion.phase == deleteApplying {
			return tea.KeyMsg{Type: tea.KeyCtrlC}
		}
	}
	return msg
}

func (m Model) updateDelete(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	quit := key.String() == "ctrl+c" || key.String() == "q"
	if m.deletion.phase == deleteApplying {
		m.quitAfterDelete = m.quitAfterDelete || quit
		return m, nil
	}
	if key.String() == "esc" || quit {
		if m.deletion.cancelPrepare != nil {
			m.deletion.cancelPrepare()
		}
		m.mode, m.deletion = modeList, deleteReview{}
		if quit {
			m.quitting = true
			return m, tea.Quit
		}
		return m.clearStatus(), nil
	}
	if m.deletion.phase != deleteReady {
		return m, nil
	}
	switch key.String() {
	case "up":
		m.deletion.vp.ScrollUp(1)
	case "down":
		m.deletion.vp.ScrollDown(1)
	case "pgup":
		m.deletion.vp.PageUp()
	case "pgdown":
		m.deletion.vp.PageDown()
	case "y":
		m.deletion.phase = deleteApplying
		store, ctx, plan, seq := m.store, m.ctx, m.deletion.plan, m.deleteSeq
		return m, func() tea.Msg { return deleteAppliedMsg{seq, store.ApplyDelete(ctx, plan)} }
	}
	return m, nil
}

func (m Model) deleteFailed(err error) (Model, tea.Cmd) {
	m.mode, m.deletion = modeList, deleteReview{}
	detail := terminal.EscapeField(err.Error())
	status := "deletion failed: " + detail
	if !m.quitAfterDelete && (errors.Is(err, vault.ErrStaleDeletePlan) || errors.Is(err, vault.ErrSessionNotFound)) {
		cause := "project changed"
		if errors.Is(err, vault.ErrSessionNotFound) {
			cause = "target missing"
		}
		before := len(m.list.marks)
		m.deleteNeedsReload = true
		var reloadErr error
		m, _, reloadErr = m.reloadSessions()
		if reloadErr != nil {
			status = "stale preview · reload failed (x retries) · " + cause + ": " + terminal.EscapeField(reloadErr.Error()) + " · " + detail
		} else {
			status = fmt.Sprintf("stale preview · dropped %d mark(s) · %s · %s", before-len(m.list.marks), cause, detail)
		}
	}
	m = m.withError(status)
	if m.quitAfterDelete {
		m.quitting = true
		return m, tea.Quit
	}
	return m, nil
}

func (m Model) handleDeleteApplied(msg deleteAppliedMsg) (tea.Model, tea.Cmd) {
	if m.mode != modeDelete || m.deletion.phase != deleteApplying || msg.seq != m.deleteSeq {
		return m, nil
	}
	if msg.err != nil {
		return m.deleteFailed(msg.err)
	}
	targets := m.deletion.plan.Targets()
	m.mode, m.deletion = modeList, deleteReview{}
	deleted := make(map[string]bool, len(targets))
	for _, target := range targets {
		deleted[target.UUID] = true
		delete(m.list.marks, target.UUID)
	}
	remaining := slices.DeleteFunc(m.list.all, func(s vault.Session) bool { return deleted[s.UUID] })
	m.list, _ = m.list.setSessions(remaining)
	status := fmt.Sprintf("deleted %d session(s)", len(targets))
	m.search.results, m.search.cursor, m.search.status = nil, 0, ""
	var searchCmd tea.Cmd
	m.search, searchCmd = m.search.refresh(status + " · search refresh failed")
	if m.quitAfterDelete {
		m.quitting = true
		return m.withStatus(status), tea.Quit
	}
	var err error
	m, _, err = m.reloadSessions()
	if err != nil {
		detail := terminal.EscapeField(err.Error())
		if m.search.refreshReason != "" {
			m.search.refreshReason = status + " · list and search refresh failed · list: " + detail + " · search"
		}
		return m.withError(status + " · list refresh failed: " + detail), searchCmd
	}
	return m.withStatus(status), searchCmd
}
