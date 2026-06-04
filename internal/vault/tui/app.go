package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/serpro69/capy/internal/vault"
)

// mode is the active screen. The TUI is mode-based (one full-screen pane at a
// time) rather than the side-by-side list+viewer split sketched in design.md
// §Layout — a deliberate v1 simplification that satisfies the Task 6.8 flow
// (browse → view → search → jump) with less layout/focus machinery. The split
// layout is a deferred UX refinement (see the tasks.md follow-up).
type mode int

const (
	modeList mode = iota
	modeView
	modeSearch
)

// dataStore is the slice of *vault.VaultStore the TUI reads through. Defined at
// the consumer (Go convention) so tests can drive the app with an in-memory stub
// instead of an encrypted DB.
type dataStore interface {
	ListSessions(opts vault.ListOptions) ([]vault.Session, error)
	GetSession(prefix string) (*vault.Session, error)
	GetFiles(sessionUUID string) ([]vault.File, error)
	Search(opts vault.SearchOptions) ([]vault.SearchResult, error)
}

// Options configures the initial screen, set from the launching CLI command.
type Options struct {
	Mode      string // "list" (default) | "search" | "view"
	Query     string // initial query for search mode
	SessionID string // session to open for view mode (partial UUID, 8+ chars)
}

// Model is the root bubbletea model composing the list, viewer, and search
// sub-models and routing keys by mode.
type Model struct {
	store  dataStore
	styles Styles

	mode     mode
	prevMode mode // where view mode returns to (list or search)

	list   listModel
	viewer viewerModel
	search searchModel

	width, height int
	status        string
	initCmd       tea.Cmd
	quitting      bool
}

// Run starts the interactive TUI against st, blocking until the user quits. The
// caller owns st's lifecycle (the TUI never opens or closes it). ctx (the
// command context) cancels the program on interrupt.
func Run(ctx context.Context, st *vault.VaultStore, opts Options) error {
	m, err := newModel(st, opts)
	if err != nil {
		return err
	}
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	_, err = p.Run()
	return err
}

func newModel(st dataStore, opts Options) (Model, error) {
	styles := DefaultStyles()

	sessions, err := st.ListSessions(vault.ListOptions{})
	if err != nil {
		return Model{}, fmt.Errorf("loading sessions: %w", err)
	}

	m := Model{
		store:  st,
		styles: styles,
		mode:   modeList,
		list:   newListModel(sessions, styles, 0, 0),
		viewer: newViewerModel(styles, 0, 0),
		search: newSearchModel(st, styles, 0, 0),
	}

	switch opts.Mode {
	case "search":
		m.mode = modeSearch
		m.prevMode = modeList
		var cmd tea.Cmd
		m.search, cmd = m.search.setQuery(opts.Query)
		m.initCmd = cmd
	case "view":
		loaded, err := m.openSession(opts.SessionID, modeList, "", 0)
		if err != nil {
			return Model{}, err
		}
		m = loaded
	}
	return m, nil
}

func (m Model) Init() tea.Cmd { return m.initCmd }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.list = m.list.setSize(msg.Width, msg.Height)
		m.viewer = m.viewer.setSize(msg.Width, msg.Height)
		m.search = m.search.setSize(msg.Width, msg.Height)
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
		switch m.mode {
		case modeList:
			return m.updateList(msg)
		case modeView:
			return m.updateView(msg)
		case modeSearch:
			return m.updateSearch(msg)
		}
	}

	// Non-key messages (debounce ticks, search results, viewport msgs) go to the
	// active sub-model.
	switch m.mode {
	case modeView:
		var cmd tea.Cmd
		m.viewer, cmd, _ = m.viewer.Update(msg)
		return m, cmd
	case modeSearch:
		var cmd tea.Cmd
		m.search, cmd = m.search.Update(msg)
		return m, cmd
	default:
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}
}

func (m Model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		m.quitting = true
		return m, tea.Quit
	case "/":
		m.mode = modeSearch
		m.prevMode = modeList
		return m, nil
	case "enter":
		sess, ok := m.list.selected()
		if !ok {
			return m, nil
		}
		loaded, err := m.openSession(sess.UUID, modeList, "", 0)
		if err != nil {
			m.status = err.Error()
			return m, nil
		}
		return loaded, nil
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m Model) updateView(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var (
		cmd    tea.Cmd
		action viewerAction
	)
	m.viewer, cmd, action = m.viewer.Update(msg)
	if action == viewerBack {
		m.mode = m.prevMode
	}
	return m, cmd
}

func (m Model) updateSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = m.prevMode
		return m, nil
	case "enter":
		r, ok := m.search.selected()
		if !ok {
			return m, nil
		}
		loaded, err := m.openSession(r.SessionUUID, modeSearch, r.SubagentID, r.LineIndex)
		if err != nil {
			m.status = err.Error()
			return m, nil
		}
		return loaded, nil
	}
	var cmd tea.Cmd
	m.search, cmd = m.search.Update(msg)
	return m, cmd
}

// openSession loads a session into the viewer and switches to view mode. After
// loading it jumps to (subagentID, line) so a search hit lands on its match;
// pass "" / 0 to open at the top. returnTo is the mode the viewer's back key
// returns to. Value receiver returning the model (the bubbletea value-model
// convention — consistent with Update/View); on error the model is returned
// unchanged (mutations happen only after both lookups succeed).
func (m Model) openSession(sessionID string, returnTo mode, subagentID string, line int) (Model, error) {
	sess, err := m.store.GetSession(sessionID)
	if err != nil {
		return m, fmt.Errorf("opening session %s: %w", sessionID, err)
	}
	files, err := m.store.GetFiles(sess.UUID)
	if err != nil {
		return m, fmt.Errorf("loading session files: %w", err)
	}
	m.viewer = m.viewer.loadSession(*sess, files)
	if subagentID != "" || line > 0 {
		m.viewer = m.viewer.jumpTo(subagentID, line)
	}
	m.prevMode = returnTo
	m.mode = modeView
	return m, nil
}

func (m Model) View() string {
	if m.quitting {
		return ""
	}
	var body string
	switch m.mode {
	case modeView:
		body = m.viewer.View()
	case modeSearch:
		body = m.search.View()
	default:
		body = m.list.View()
	}
	if m.status != "" {
		body += "\n" + m.styles.ErrorMsg.Render(m.status)
	}
	return body
}
