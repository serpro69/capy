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
	"github.com/serpro69/capy/internal/vault"
)

// sessionItem adapts a vault.Session to bubbles/list.DefaultItem. It carries the
// full Session so selecting it can open the viewer without a second lookup of the
// list metadata (the raw_jsonl blob is still fetched lazily on open).
type sessionItem struct {
	sess vault.Session
}

// Title is the primary line. A child session (a Codex sub-agent rollout with
// parent_uuid set, listed only while includeChildren is on) is indented under
// its parent's short id — "  ↳ 019dc606f552  <title>" — mirroring the CLI's
// sessionTitleCell so the two list surfaces read alike.
func (i sessionItem) Title() string {
	t := strings.TrimSpace(i.sess.EffectiveTitle())
	if t == "" {
		t = "(untitled)"
	}
	if i.sess.ParentUUID == "" {
		return t
	}
	return "  ↳ " + shortID(i.sess.ParentUUID) + "  " + t
}

// Description is the secondary line: short uuid · platform · date · messages ·
// size · project. The platform is the stored token ("claude-code" / "codex"),
// the same value the CLI's PLATFORM column prints and `--platform` accepts.
func (i sessionItem) Description() string {
	return fmt.Sprintf("%s · %s · %s · %dmsg · %s · %s",
		shortID(i.sess.UUID), i.sess.Platform.OrClaude(), fmtDate(i.sess.EndTime), i.sess.MessageCount,
		fmtSize(i.sess.SizeBytes), displayPath(i.sess.ProjectPath))
}

// FilterValue feeds the list's built-in filter (currently disabled — see
// listModel) and any future fuzzy filter; effective title + project + uuid
// covers the fields a user would search the list by.
func (i sessionItem) FilterValue() string {
	return i.sess.EffectiveTitle() + " " + i.sess.ProjectPath + " " + i.sess.UUID
}

// filterSessions returns the sessions matching needle across effective title,
// project path, and UUID, using the same Unicode-folding literal matcher as
// `vault list --name` (vault.ContainsFold — design §Read Surfaces and Name
// Lookup). An empty needle matches everything.
func filterSessions(sessions []vault.Session, needle string) []vault.Session {
	if needle == "" {
		return sessions
	}
	var out []vault.Session
	for _, s := range sessions {
		if vault.ContainsFold(s.EffectiveTitle(), needle) ||
			vault.ContainsFold(s.ProjectPath, needle) ||
			vault.ContainsFold(s.UUID, needle) {
			out = append(out, s)
		}
	}
	return out
}

// listModel is the session browser (left/primary panel). It wraps bubbles/list.
// The built-in "/" fuzzy filter is disabled so "/" opens the global FTS search
// instead (design key bindings + Task 6.8); "f" drives a session filter across
// effective title, project path, and UUID (filterSessions) over a cached
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

	// includeChildren mirrors vault.ListOptions.IncludeChildren for the store
	// reads that refresh the snapshot (listOptions): child sessions (Codex
	// sub-agent rollouts, parent_uuid set) are hidden by default like Codex's own
	// pickers and toggled in with listChildrenKey. It is a store-side predicate,
	// not an in-memory filter — the default snapshot never contains the children,
	// so every flip re-reads (Model.toggleChildren).
	includeChildren bool
	// platform is the immutable scope supplied by `vault list --platform` when
	// the TUI starts. Every refresh and children toggle must preserve it.
	platform vault.Platform

	width, height int
}

// listChildrenKey toggles includeChildren in list navigation. Chosen against
// both key maps the list mode answers to: the app's own bindings (q / f e r R
// enter) and bubbles/list's default navigation (h j k l, b u f d for paging,
// g G, ?, esc) — TestListKeys_ChildrenToggleIsUnbound pins the latter.
const listChildrenKey = "s"

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
			key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "rename")),
			key.NewBinding(key.WithKeys(listChildrenKey), key.WithHelp(listChildrenKey, "children")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
			key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "raw JSONL")),
			key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "restore")),
			key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "resume")),
			key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		}
	}

	fi := textinput.New()
	fi.Prompt = "filter: "
	fi.Placeholder = "substring of title, project path, or uuid"
	fi.CharLimit = 256

	return listModel{list: l, styles: styles, all: sessions, filter: fi, width: width, height: height}
}

// listFilterHint sits to the right of the filter input on its row; setSize
// reserves its width so a long needle scrolls instead of running off-screen.
const listFilterHint = "enter apply · esc clear"

func (m listModel) setSize(width, height int) listModel {
	m.width, m.height = width, height
	m.list.SetSize(width, m.listHeight())
	boundInputWidth(&m.filter, width, utf8.RuneCountInString(listFilterHint))
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

// startFilter focuses the session-filter input, pre-filled with the active filter
// so the user edits rather than retypes it.
func (m listModel) startFilter() listModel {
	m.filtering = true
	m.filter.SetValue(m.applied)
	m.filter.CursorEnd()
	m.filter.Focus()
	m.list.SetSize(m.width, m.listHeight())
	return m
}

// stopFilter blurs the input and returns the list to navigation. The applied
// filter (m.applied) and the current item set are left untouched — the caller
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

// setSessions replaces the cached snapshot with all, displays the subset
// matching applied (filterSessions), and records applied for the title and the
// next startFilter pre-fill. Pass the current snapshot (m.all) to re-filter in
// memory, or a fresh store read to refresh it.
func (m listModel) setSessions(all []vault.Session, applied string) (listModel, tea.Cmd) {
	shown := filterSessions(all, applied)
	items := make([]list.Item, len(shown))
	for i, s := range shown {
		items[i] = sessionItem{sess: s}
	}
	cmd := m.list.SetItems(items)
	m.all = all
	m.applied = applied
	title := fmt.Sprintf("Vault — %d session(s)", len(shown))
	if m.includeChildren {
		title += " · incl. children"
	}
	if applied != "" {
		title += fmt.Sprintf(" · filter %q", applied)
	}
	m.list.Title = title
	return m, cmd
}

// setIncludeChildren records the children setting for the next snapshot read.
// It does not touch the items: the caller (Model.toggleChildren) follows it
// with reloadSessions, whose setSessions call re-derives the title.
func (m listModel) setIncludeChildren(on bool) listModel {
	m.includeChildren = on
	return m
}

// listOptions is the store query the snapshot is refreshed with: unfiltered
// (matching happens in the TUI — filterSessions) apart from the children
// setting, which only the store can apply since hidden children are never in
// the snapshot.
func (m listModel) listOptions() vault.ListOptions {
	return vault.ListOptions{IncludeChildren: m.includeChildren, Platform: m.platform}
}

// selectSession moves the highlight to the session with the given UUID when it
// is present in the current (possibly filtered) item set. When it is not — a
// rename made the item disappear from a non-matching active filter — the
// selection is left where SetItems clamped it, a sensible neighbor.
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
	if m.filtering {
		hint := m.styles.Help.Render(listFilterHint)
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
