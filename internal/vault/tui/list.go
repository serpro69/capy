package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/serpro69/capy/internal/vault"
)

// sessionItem adapts a vault.Session to bubbles/list.DefaultItem. It carries the
// full Session so selecting it can open the viewer without a second lookup of the
// list metadata (the raw_jsonl blob is still fetched lazily on open).
type sessionItem struct {
	sess vault.Session
}

func (i sessionItem) Title() string {
	if t := strings.TrimSpace(i.sess.Title); t != "" {
		return t
	}
	return "(untitled)"
}

// Description is the secondary line: short uuid · date · messages · size · project.
func (i sessionItem) Description() string {
	return fmt.Sprintf("%s · %s · %dmsg · %s · %s",
		shortID(i.sess.UUID), fmtDate(i.sess.EndTime), i.sess.MessageCount,
		fmtSize(i.sess.SizeBytes), displayPath(i.sess.ProjectPath))
}

// FilterValue feeds the list's built-in filter (currently disabled — see
// listModel) and any future fuzzy filter; title + project + uuid covers the
// fields a user would search the list by.
func (i sessionItem) FilterValue() string {
	return i.sess.Title + " " + i.sess.ProjectPath + " " + i.sess.UUID
}

// listModel is the session browser (left/primary panel). It wraps bubbles/list.
// The built-in "/" filter is disabled so "/" opens the global FTS search instead
// (design key bindings + Task 6.8); in-list fuzzy filtering is a deferred nicety
// (see the tasks.md follow-up).
type listModel struct {
	list   list.Model
	styles Styles
}

func newListModel(sessions []vault.Session, styles Styles, width, height int) listModel {
	items := make([]list.Item, len(sessions))
	for i, s := range sessions {
		items[i] = sessionItem{sess: s}
	}
	l := list.New(items, list.NewDefaultDelegate(), width, height)
	l.Title = fmt.Sprintf("Vault — %d session(s)", len(sessions))
	l.SetShowHelp(true)
	l.SetFilteringEnabled(false)
	l.DisableQuitKeybindings() // the app owns q/ctrl+c quit
	return listModel{list: l, styles: styles}
}

func (m listModel) setSize(width, height int) listModel {
	m.list.SetSize(width, height)
	return m
}

// Update delegates to the wrapped list (cursor movement, paging). The app
// intercepts enter/"/"/q before delegating, so this only handles navigation.
func (m listModel) Update(msg tea.Msg) (listModel, tea.Cmd) {
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m listModel) View() string {
	return m.list.View()
}

// selected returns the highlighted session, or false when the list is empty.
func (m listModel) selected() (vault.Session, bool) {
	it, ok := m.list.SelectedItem().(sessionItem)
	if !ok {
		return vault.Session{}, false
	}
	return it.sess, true
}

func fmtDate(t time.Time) string {
	if t.IsZero() {
		return "----------"
	}
	return t.Format("2006-01-02")
}
