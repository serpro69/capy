package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/serpro69/capy/internal/vault"
)

// searchDebounce is how long input must settle before an FTS query fires, so
// typing doesn't spawn a query per keystroke (design.md §Search Model: 200ms).
const searchDebounce = 200 * time.Millisecond

// debounceMsg fires searchDebounce after a keystroke; seq lets the model ignore
// all but the latest pending tick.
type debounceMsg struct{ seq int }

// searchResultsMsg carries the outcome of a fired query back into Update; seq
// guards against a slow earlier query overwriting a newer one's results.
type searchResultsMsg struct {
	seq     int
	results []vault.SearchResult
	err     error
}

// searcher is the slice of VaultStore the search model needs. Defined here (the
// consumer) per Go interface-placement convention, and so tests can inject a stub
// without a real encrypted DB.
type searcher interface {
	Search(opts vault.SearchOptions) ([]vault.SearchResult, error)
}

// searchModel is the FTS search panel: a text input over a results list. Queries
// are debounced and run in a tea.Cmd; results carry the subagent_id/line_index
// anchors the viewer jumps to. Selecting a result is handled by the app (it loads
// the target session, then tells the viewer to jump).
type searchModel struct {
	store  searcher
	styles Styles
	width  int
	height int

	input   textinput.Model
	results []vault.SearchResult
	cursor  int
	seq     int    // increments per keystroke; latest-wins for debounce + results
	status  string // transient status / error line
}

func newSearchModel(store searcher, styles Styles, width, height int) searchModel {
	ti := textinput.New()
	ti.Placeholder = "search archived sessions…"
	ti.Prompt = "/ "
	ti.CharLimit = 256
	ti.Focus()
	return searchModel{store: store, styles: styles, width: width, height: height, input: ti}
}

func (m searchModel) setSize(width, height int) searchModel {
	m.width = width
	m.height = height
	return m
}

// setQuery prefills the input (e.g. `search <query> --tui`) and returns the
// command that fires the initial query.
func (m searchModel) setQuery(q string) (searchModel, tea.Cmd) {
	m.input.SetValue(q)
	return m.scheduleSearch()
}

// scheduleSearch bumps the sequence and schedules a debounced fire for the
// current input value.
func (m searchModel) scheduleSearch() (searchModel, tea.Cmd) {
	m.seq++
	seq := m.seq
	return m, tea.Tick(searchDebounce, func(time.Time) tea.Msg { return debounceMsg{seq: seq} })
}

// runSearch returns a command that executes the query off the Update goroutine.
func (m searchModel) runSearch(seq int) tea.Cmd {
	query := strings.TrimSpace(m.input.Value())
	store := m.store
	return func() tea.Msg {
		if query == "" {
			return searchResultsMsg{seq: seq}
		}
		res, err := store.Search(vault.SearchOptions{Query: query})
		return searchResultsMsg{seq: seq, results: res, err: err}
	}
}

func (m searchModel) Update(msg tea.Msg) (searchModel, tea.Cmd) {
	switch msg := msg.(type) {
	case debounceMsg:
		if msg.seq != m.seq {
			return m, nil // superseded by a later keystroke
		}
		return m, m.runSearch(msg.seq)
	case searchResultsMsg:
		if msg.seq != m.seq {
			return m, nil // stale results
		}
		if msg.err != nil {
			// User-visible only: the TUI has no logger, and the error is surfaced
			// in the status line rather than returned (a search failure must not
			// tear down the program). The raw message may be high-cardinality
			// (SQLite internals) but that is acceptable in an interactive status line.
			m.status = "search error: " + msg.err.Error()
			m.results = nil
			return m, nil
		}
		m.results = msg.results
		m.cursor = 0
		m.status = ""
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "down", "ctrl+n":
			if m.cursor < len(m.results)-1 {
				m.cursor++
			}
			return m, nil
		case "up", "ctrl+p":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil
		}
		// Any other key edits the query; re-debounce on a value change.
		before := m.input.Value()
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		if m.input.Value() != before {
			var schedule tea.Cmd
			m, schedule = m.scheduleSearch()
			return m, tea.Batch(cmd, schedule)
		}
		return m, cmd
	}
	return m, nil
}

// selected returns the highlighted result, or false when there are none.
func (m searchModel) selected() (vault.SearchResult, bool) {
	if m.cursor < 0 || m.cursor >= len(m.results) {
		return vault.SearchResult{}, false
	}
	return m.results[m.cursor], true
}

func (m searchModel) View() string {
	var b strings.Builder
	b.WriteString(m.input.View())
	b.WriteString("\n\n")

	if m.status != "" {
		b.WriteString(m.styles.ErrorMsg.Render(m.status))
		b.WriteString("\n")
		return b.String()
	}
	if len(m.results) == 0 {
		if strings.TrimSpace(m.input.Value()) != "" {
			b.WriteString(m.styles.Help.Render("no matches"))
		} else {
			b.WriteString(m.styles.Help.Render("type to search · ↑/↓ select · enter open · esc back"))
		}
		return b.String()
	}

	rows := max(1, m.height-4) // input (1) + blank (1) + help (1) + margin
	for i := 0; i < len(m.results) && i < rows; i++ {
		b.WriteString(m.resultRow(m.results[i], i == m.cursor))
		b.WriteString("\n")
	}
	b.WriteString(m.styles.Help.Render("↑/↓ select · enter open · esc back"))
	return b.String()
}

// resultRow renders a single search result: a meta prefix (date, role, project)
// plus the snippet, highlighted when selected. A subagent hit is flagged so the
// user knows enter will open a subagent transcript.
func (m searchModel) resultRow(r vault.SearchResult, selected bool) string {
	role := r.Role
	if r.SubagentID != "" {
		role += "/sub"
	}
	meta := fmt.Sprintf("%s  %-12s  %s", fmtDate(r.EndTime), truncate(role, 12), truncate(displayPath(r.ProjectPath), 24))
	line := meta + "  " + oneLine(r.Snippet)
	line = truncate(line, max(1, m.width-1))
	if selected {
		return m.styles.ResultSelected.Render(line)
	}
	return m.styles.ResultMeta.Render(meta) + "  " + m.styles.Snippet.Render(truncate(oneLine(r.Snippet), max(1, m.width-len(meta)-3)))
}
