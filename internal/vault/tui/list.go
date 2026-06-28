package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
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
// The built-in "/" fuzzy filter is disabled so "/" opens the global FTS search
// instead (design key bindings + Task 6.8); "f" drives a project filter that
// re-queries the store (ListSessions(Project:)) rather than filtering in memory —
// the app owns the re-query (it holds the store + ctx), the listModel owns the
// input widget and the filtering flag.
type listModel struct {
	list   list.Model
	styles Styles

	filter    textinput.Model // project-filter input, shown only while filtering
	filtering bool            // input focused; keystrokes edit the filter
	project   string          // applied filter substring ("" == all)

	width, height int
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
	// Surface the app-level keybindings in the list's own help line (bubbles/list
	// only documents its built-in navigation otherwise).
	l.AdditionalShortHelpKeys = func() []key.Binding {
		return []key.Binding{
			key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "search")),
			key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "filter")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
			key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "restore")),
			key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "resume")),
			key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		}
	}

	fi := textinput.New()
	fi.Prompt = "filter project: "
	fi.Placeholder = "substring of the project path"
	fi.CharLimit = 256

	return listModel{list: l, styles: styles, filter: fi, width: width, height: height}
}

func (m listModel) setSize(width, height int) listModel {
	m.width, m.height = width, height
	m.list.SetSize(width, m.listHeight())
	return m
}

// listHeight reserves one row for the filter prompt while filtering so the input
// line doesn't clip the list's bottom row.
func (m listModel) listHeight() int {
	if m.filtering {
		return max(1, m.height-1)
	}
	return m.height
}

// startFilter focuses the project-filter input, pre-filled with the active filter
// so the user edits rather than retypes it.
func (m listModel) startFilter() listModel {
	m.filtering = true
	m.filter.SetValue(m.project)
	m.filter.CursorEnd()
	m.filter.Focus()
	m.list.SetSize(m.width, m.listHeight())
	return m
}

// stopFilter blurs the input and returns the list to navigation. The applied
// filter (m.project) and the current item set are left untouched — the caller
// decides whether to clear them (esc) or keep them (enter).
func (m listModel) stopFilter() listModel {
	m.filtering = false
	m.filter.Blur()
	m.list.SetSize(m.width, m.listHeight())
	return m
}

func (m listModel) filterValue() string { return m.filter.Value() }

// updateFilterInput feeds a keystroke to the filter text input.
func (m listModel) updateFilterInput(msg tea.Msg) (listModel, tea.Cmd) {
	var cmd tea.Cmd
	m.filter, cmd = m.filter.Update(msg)
	return m, cmd
}

// setSessions swaps the displayed sessions (after a filter re-query) and records
// the applied filter for the title and the next startFilter pre-fill.
func (m listModel) setSessions(sessions []vault.Session, project string) (listModel, tea.Cmd) {
	items := make([]list.Item, len(sessions))
	for i, s := range sessions {
		items[i] = sessionItem{sess: s}
	}
	cmd := m.list.SetItems(items)
	m.project = project
	title := fmt.Sprintf("Vault — %d session(s)", len(sessions))
	if project != "" {
		title += fmt.Sprintf(" · filter %q", project)
	}
	m.list.Title = title
	return m, cmd
}

// Update delegates to the wrapped list (cursor movement, paging). The app
// intercepts enter/"/"/q before delegating, so this only handles navigation.
func (m listModel) Update(msg tea.Msg) (listModel, tea.Cmd) {
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m listModel) View() string {
	if m.filtering {
		hint := m.styles.Help.Render("enter apply · esc clear")
		return m.filter.View() + "  " + hint + "\n" + m.list.View()
	}
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
