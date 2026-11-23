package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/serpro69/capy/internal/vault"
)

// sessionItem adapts a vault.Session to bubbles/list.DefaultItem. It carries the
// full Session so selecting it can open the viewer without a second lookup of the
// list metadata (the raw_jsonl blob is still fetched lazily on open).
type sessionItem struct {
	sess  vault.Session
	depth int // number of visible ancestors
}

// Title indents children below their visible parent. A child whose parent is
// absent carries the parent ID inline so it reads as a standalone row.
func (i sessionItem) Title() string {
	t := strings.TrimSpace(i.sess.EffectiveTitle())
	if t == "" {
		t = "(untitled)"
	}
	if i.sess.ParentUUID == "" {
		return t
	}
	if i.depth == 0 {
		return "(parent " + shortID(i.sess.ParentUUID) + ")  " + t
	}
	return strings.Repeat("  ", i.depth) + "↳ " + shortID(i.sess.ParentUUID) + "  " + t
}

// Description is the secondary line: short uuid · platform · date · messages ·
// size · project. The platform is the stored token ("claude-code" / "codex"),
// the same value the CLI's PLATFORM column prints and `--platform` accepts.
func (i sessionItem) Description() string {
	return fmt.Sprintf("%s · %s · %s · %dmsg · %s · %s",
		shortID(i.sess.UUID), i.sess.Platform.OrClaude(), fmtDate(i.sess.EndTime), i.sess.MessageCount,
		fmtSize(i.sess.SizeBytes), displaySessionProject(i.sess))
}

// FilterValue feeds the list's built-in filter (currently disabled — see
// listModel) and any future fuzzy filter; effective title + project + uuid
// covers the fields a user would search the list by.
func (i sessionItem) FilterValue() string {
	return i.sess.EffectiveTitle() + " " + i.sess.EffectiveProject() + " " + i.sess.UUID
}

// filterSessions returns the sessions matching needle across effective title,
// effective project, and UUID, using the same Unicode-folding literal matcher as
// `vault list --name` (vault.ContainsFold — design §Read Surfaces and Name
// Lookup). An empty needle matches everything.
func filterSessions(sessions []vault.Session, needle string) []vault.Session {
	if needle == "" {
		return sessions
	}
	var out []vault.Session
	for _, s := range sessions {
		if vault.ContainsFold(s.EffectiveTitle(), needle) ||
			vault.ContainsFold(s.EffectiveProject(), needle) ||
			vault.ContainsFold(s.UUID, needle) {
			out = append(out, s)
		}
	}
	return out
}

// listModel is the session browser (left/primary panel). It wraps bubbles/list.
// The built-in "/" fuzzy filter is disabled so "/" opens the global FTS search
// instead (design key bindings + Task 6.8); "f" drives a session filter across
// effective title, effective project, and UUID (filterSessions) over a cached
// snapshot of the vault (all) — the app owns the store reads that refresh the
// snapshot (it holds the store + ctx; see Model.reloadSessions), the listModel
// owns the snapshot, the input widget, and the filtering flag.
type listModel struct {
	list   list.Model
	styles Styles

	// all is the unfiltered session list the displayed items are derived from.
	// Filtering runs in memory over it on every keystroke; it is refreshed from
	// the store only when the filter opens, after a rename, and when the
	// children toggle flips, so a keystroke never costs a database scan (design
	// Assumption 6 accepts one bounded scan per lookup, not one per character).
	all []vault.Session

	filter    textinput.Model // session-filter input, shown only while filtering
	filtering bool            // input focused; keystrokes edit the filter
	applied   string          // applied filter substring ("" == all)

	listOptions vault.ListOptions
	returnToAll bool // S restores roots on a direct child-only launch
	scopeRows   []string

	width, height int
}

// listChildrenKey toggles includeChildren in list navigation. Chosen against
// both key maps the list mode answers to: the app's own bindings (q / f e r R
// enter) and bubbles/list's default navigation (h j k l, b u f d for paging,
// g G, ?, esc) — TestListKeys_ChildrenToggleIsUnbound pins the latter.
const listChildrenKey = "s"

// sessionListItems receives visible sessions in descending end-time order.
// Visiting each parent's children in input order preserves that order among
// both the top-level rows and each group of siblings.
func sessionListItems(sessions []vault.Session, includeChildren bool) []list.Item {
	items := make([]list.Item, 0, len(sessions))
	if !includeChildren {
		for _, s := range sessions {
			items = append(items, sessionItem{sess: s})
		}
		return items
	}

	byID := make(map[string]int, len(sessions))
	for i := range sessions {
		byID[sessions[i].UUID] = i
	}
	children := make([][]int, len(sessions))
	var roots []int
	for i, s := range sessions {
		if parent, ok := byID[s.ParentUUID]; s.ParentUUID != "" && ok {
			children[parent] = append(children[parent], i)
		} else {
			roots = append(roots, i)
		}
	}
	seen := make([]bool, len(sessions))
	var add func(int, int)
	add = func(i, depth int) {
		if seen[i] {
			return
		}
		seen[i] = true
		items = append(items, sessionItem{sess: sessions[i], depth: depth})
		for _, child := range children[i] {
			add(child, depth+1)
		}
	}
	for _, root := range roots {
		add(root, 0)
	}
	// Imported parent IDs have no foreign key. A cycle has no root, but its
	// sessions must still be accessible and appear only once.
	if len(items) < len(sessions) {
		for i := range sessions {
			add(i, 0)
		}
	}
	return items
}

func newListModel(sessions []vault.Session, opts vault.ListOptions, styles Styles, width, height int) listModel {
	l := list.New(nil, list.NewDefaultDelegate(), width, height)
	l.SetShowHelp(true)
	l.SetFilteringEnabled(false)
	l.DisableQuitKeybindings() // the app owns q/ctrl+c quit
	// Surface the app-level keybindings in the list's own help line (bubbles/list
	// only documents its built-in navigation otherwise).
	l.AdditionalShortHelpKeys = func() []key.Binding {
		return []key.Binding{
			key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "search")),
			key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "filter")),
			key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "rename")),
			key.NewBinding(key.WithKeys("ctrl+g"), key.WithHelp("ctrl+g", "project")),
			key.NewBinding(key.WithKeys(listChildrenKey), key.WithHelp(listChildrenKey, "include children")),
			key.NewBinding(key.WithKeys("S"), key.WithHelp("S", "children only")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
			key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "raw JSONL")),
			key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "restore")),
			key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "resume")),
			key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		}
	}

	fi := textinput.New()
	fi.Prompt = "filter: "
	fi.Placeholder = "substring of title, project, or uuid"
	fi.CharLimit = 256

	m := listModel{list: l, listOptions: opts, styles: styles, filter: fi, width: width, height: height}
	m, _ = m.setSessions(sessions, "") // the built-in asynchronous filter is disabled
	return m.setSize(width, height)
}

// listFilterHint sits to the right of the filter input on its row; setSize
// reserves its width so a long needle scrolls instead of running off-screen.
const listFilterHint = "enter apply · esc clear · ctrl+g project"

func (m listModel) setSize(width, height int) listModel {
	m.width, m.height = width, height
	m.scopeRows = listScopeRows(m.listOptions, width)
	m.sizeList()
	boundInputWidth(&m.filter, width, utf8.RuneCountInString(listFilterHint))
	return m
}

// The title already carries the number of sessions. In a short pane the
// redundant item count can yield its rows to the selected item and finder.
func (m *listModel) sizeList() {
	height := m.listHeight()
	m.list.SetShowStatusBar(height >= minBrowserRows)
	m.list.SetSize(m.width, height)
}

// listHeight reserves one row for the filter prompt while filtering so the input
// line doesn't clip the list's bottom row.
func (m listModel) listHeight() int {
	h := m.height - len(m.visibleScopeRows())
	if m.filtering {
		h--
	}
	return max(1, h)
}

// The default list renders its title, status, pagination and help around a
// two-line selected item. Below ten rows the footer yields to the browser.
const minBrowserRows = 10

// Keep the list and its filter usable when the terminal shrinks. At normal
// heights every scope has room independent of the length of the other fields.
func (m listModel) visibleScopeRows() []string {
	available := m.height - minBrowserRows
	if m.filtering {
		available--
	}
	return m.scopeRows[:min(len(m.scopeRows), max(0, available))]
}

func listScopeRows(opts vault.ListOptions, width int) []string {
	if width <= 0 {
		return nil
	}
	var fields []string
	if opts.Project != "" {
		fields = append(fields, fmt.Sprintf("project %q", opts.Project))
	}
	if opts.Platform != "" {
		fields = append(fields, "platform "+string(opts.Platform))
	}
	if opts.Name != "" {
		fields = append(fields, fmt.Sprintf("name %q", opts.Name))
	}
	if opts.Limit > 0 {
		fields = append(fields, fmt.Sprintf("limit %d", opts.Limit))
	}
	var rows []string
	for _, field := range fields {
		field = ansi.Truncate(field, width, "…")
		last := len(rows) - 1
		if last >= 0 && ansi.StringWidth(rows[last]+" · "+field) <= width {
			rows[last] += " · " + field
		} else {
			rows = append(rows, field)
		}
	}
	return rows
}

// startFilter focuses the session-filter input, pre-filled with the active filter
// so the user edits rather than retypes it.
func (m listModel) startFilter() listModel {
	m.filtering = true
	m.filter.SetValue(m.applied)
	m.filter.CursorEnd()
	m.filter.Focus()
	m.sizeList()
	return m
}

// stopFilter blurs the input and returns the list to navigation. The applied
// filter (m.applied) and the current item set are left untouched — the caller
// decides whether to clear them (esc) or keep them (enter).
func (m listModel) stopFilter() listModel {
	m.filtering = false
	m.filter.Blur()
	m.sizeList()
	return m
}

func (m listModel) filterValue() string { return m.filter.Value() }

// updateFilterInput feeds a keystroke to the filter text input.
func (m listModel) updateFilterInput(msg tea.Msg) (listModel, tea.Cmd) {
	var cmd tea.Cmd
	m.filter, cmd = m.filter.Update(msg)
	return m, cmd
}

// setSessions replaces the cached snapshot with all, displays the subset
// matching applied (filterSessions), and records applied for the title and the
// next startFilter pre-fill. Pass the current snapshot (m.all) to re-filter in
// memory, or a fresh store read to refresh it.
func (m listModel) setSessions(all []vault.Session, applied string) (listModel, tea.Cmd) {
	shown := filterSessions(all, applied)
	opts := m.listOptions
	cmd := m.list.SetItems(sessionListItems(shown, opts.IncludeChildren || opts.ChildrenOnly))
	// SetItems clamps the page, but its cursor can sit past the last row on
	// that page after filtering or hiding children.
	if len(shown) > 0 && m.list.Index() >= len(shown) {
		m.list.Select(len(shown) - 1)
	}
	m.all = all
	m.applied = applied
	title := fmt.Sprintf("Vault — %d session(s)", len(shown))
	switch {
	case opts.ChildrenOnly:
		title += " · children only"
	case opts.IncludeChildren:
		title += " · all sessions"
	default:
		title += " · roots"
	}
	if applied != "" {
		title += fmt.Sprintf(" · filter %q", applied)
	}
	m.list.Title = title
	return m, cmd
}

// selectedRootUUID keeps the current family in focus across a children toggle.
// The nearest preceding top-level row owns the selected subtree.
func (m listModel) selectedRootUUID() string {
	items := m.list.Items()
	for i := m.list.Index(); i >= 0 && i < len(items); i-- {
		row := items[i].(sessionItem)
		if row.depth == 0 {
			return row.sess.UUID
		}
	}
	return ""
}

// selectSession moves the highlight to the session with the given UUID when it
// is present in the current (possibly filtered) item set. When it is not — a
// rename made the item disappear from a non-matching active filter — the
// selection is left where setSessions clamped it, a sensible neighbor.
func (m listModel) selectSession(uuid string) listModel {
	for i, it := range m.list.Items() {
		if si, ok := it.(sessionItem); ok && si.sess.UUID == uuid {
			m.list.Select(i)
			break
		}
	}
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
	body := m.list.View()
	if m.filtering {
		hint := m.styles.Help.Render(listFilterHint)
		body = fitRow(m.filter.View()+"  "+hint, m.width) + "\n" + body
	}
	if rows := m.visibleScopeRows(); len(rows) > 0 {
		body += "\n" + m.styles.Help.Render(strings.Join(rows, "\n"))
	}
	return body
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
