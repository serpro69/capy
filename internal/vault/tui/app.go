package tui

import (
	"context"
	"fmt"
	"io"
	"os"

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
// instead of an encrypted DB. Each method takes a context to mirror VaultStore's
// real signatures; the TUI supplies the program context (see Model.ctx).
type dataStore interface {
	ListSessions(ctx context.Context, opts vault.ListOptions) ([]vault.Session, error)
	GetSession(ctx context.Context, prefix string) (*vault.Session, error)
	GetFiles(ctx context.Context, sessionUUID string) ([]vault.File, error)
	Search(ctx context.Context, opts vault.SearchOptions) ([]vault.SearchResult, error)
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
	// ctx is the program context from Run. Carrying it on the model is the
	// deliberate exception to "don't store a context in a struct": bubbletea owns
	// the Update/View signatures, so a context cannot be threaded as a parameter
	// through the keystroke-driven DB reads (openSession, search). The model's
	// lifetime is exactly the program context's lifetime — both are created in Run
	// and discarded when Run returns — so there is no ambiguity about which context
	// is active. It lets the async FTS query (searchModel.runSearch) and the
	// per-keystroke reads honor program cancellation (ctrl+c / tea.WithContext).
	ctx context.Context

	store  dataStore
	styles Styles

	mode     mode
	prevMode mode // where view mode returns to (list or search)

	list   listModel
	viewer viewerModel
	search searchModel

	// clipOut is where the OSC-52 clipboard escape is written for the `c` key —
	// os.Stderr in production (the same TTY as the renderer, but out-of-band), a
	// buffer in tests. See copyToClipboard.
	clipOut io.Writer
	// action is the deferred CLI intent (restore/resume) recorded when the user
	// presses r/R; Run returns it after the program exits so the CLI performs the
	// exec with a restored terminal. ActionNone until requested.
	action Action

	width, height int
	status        string // transient one-line status; reserves the bottom row when set
	statusErr     bool   // render status with the error style (red) vs. neutral info
	initCmd       tea.Cmd
	quitting      bool
}

// Run starts the interactive TUI against st, blocking until the user quits. The
// caller owns st's lifecycle (the TUI never opens or closes it). ctx (the
// command context) cancels the program on interrupt.
//
// It returns the deferred Action the user requested (restore/resume), or
// ActionNone when they simply quit. The CLI performs that action AFTER Run
// returns — by then bubbletea has torn down the alt-screen and restored the raw
// TTY, which the restore/exec surface requires (it writes files / hands the
// terminal to `claude --resume`).
func Run(ctx context.Context, st *vault.VaultStore, opts Options) (Action, error) {
	m, err := newModel(ctx, st, opts)
	if err != nil {
		return Action{}, err
	}
	final, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx)).Run()
	if err != nil {
		return Action{}, err
	}
	fm, ok := final.(Model)
	if !ok {
		return Action{}, nil
	}
	return fm.action, nil
}

func newModel(ctx context.Context, st dataStore, opts Options) (Model, error) {
	styles := DefaultStyles()

	sessions, err := st.ListSessions(ctx, vault.ListOptions{})
	if err != nil {
		return Model{}, fmt.Errorf("loading sessions: %w", err)
	}

	m := Model{
		ctx:     ctx,
		store:   st,
		styles:  styles,
		mode:    modeList,
		list:    newListModel(sessions, styles, 0, 0),
		viewer:  newViewerModel(styles, 0, 0),
		search:  newSearchModel(ctx, st, styles, 0, 0),
		clipOut: os.Stderr,
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

// bodyHeight is the height available to the active sub-model: one row is reserved
// for the status line whenever a status is set, so the composed View never
// exceeds m.height (which would scroll the alt-screen and flicker every frame).
func (m Model) bodyHeight() int {
	if m.status != "" {
		return max(1, m.height-1)
	}
	return m.height
}

// layoutSubmodels (re)sizes every sub-model to the current width and bodyHeight.
// Routing both WindowSizeMsg and status changes through here keeps the reserved
// status row consistent across resizes.
func (m Model) layoutSubmodels() Model {
	h := m.bodyHeight()
	m.list = m.list.setSize(m.width, h)
	m.viewer = m.viewer.setSize(m.width, h)
	m.search = m.search.setSize(m.width, h)
	return m
}

// withStatus shows a neutral (info) status; withError shows an error-styled one;
// clearStatus removes it. Each re-lays-out the sub-models so the reserved row
// appears/disappears. clearStatus is a no-op (and skips the relayout) when no
// status is set.
func (m Model) withStatus(s string) Model {
	m.status = s
	m.statusErr = false
	return m.layoutSubmodels()
}

func (m Model) withError(s string) Model {
	m.status = s
	m.statusErr = true
	return m.layoutSubmodels()
}

func (m Model) clearStatus() Model {
	if m.status == "" {
		return m
	}
	m.status = ""
	m.statusErr = false
	return m.layoutSubmodels()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m.layoutSubmodels(), nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
		// A transient status (copy confirmation, error) clears on the next keystroke
		// so it never lingers. The "c" copy key is exempt — it sets its own status
		// this same update; clearing first would just relayout twice.
		if msg.String() != "c" {
			m = m.clearStatus()
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
	if m.list.filtering {
		return m.updateListFilter(msg)
	}
	switch msg.String() {
	case "q":
		m.quitting = true
		return m, tea.Quit
	case "/":
		m.mode = modeSearch
		m.prevMode = modeList
		return m, nil
	case "f":
		m.list = m.list.startFilter()
		return m, nil
	case "r", "R":
		sess, ok := m.list.selected()
		if !ok {
			return m, nil
		}
		return m.requestAction(actionFor(msg.String()), sess.UUID)
	case "enter":
		sess, ok := m.list.selected()
		if !ok {
			return m, nil
		}
		loaded, err := m.openSession(sess.UUID, modeList, "", 0)
		if err != nil {
			return m.withError(err.Error()), nil
		}
		return loaded, nil
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

// updateListFilter handles keys while the list's project-filter input is active.
// esc clears the filter (restoring all sessions); enter applies it and returns to
// navigation; arrow keys move the (filtered) selection; any other key edits the
// input and re-queries ListSessions(Project:) on a value change.
func (m Model) updateListFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.list = m.list.stopFilter()
		return m.applyProjectFilter("")
	case "enter":
		m.list = m.list.stopFilter()
		return m, nil
	case "up", "down", "ctrl+p", "ctrl+n":
		// Let the highlight move through the filtered results without leaving the
		// input, mirroring the search panel's feel.
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}
	before := m.list.filterValue()
	var cmd tea.Cmd
	m.list, cmd = m.list.updateFilterInput(msg)
	if m.list.filterValue() != before {
		next, applyCmd := m.applyProjectFilter(m.list.filterValue())
		return next, tea.Batch(cmd, applyCmd)
	}
	return m, cmd
}

// applyProjectFilter re-queries the store with a project substring and swaps the
// list's items. On error it surfaces the message in the status line and leaves the
// current items in place (a failed re-query must not blank the browser).
func (m Model) applyProjectFilter(project string) (tea.Model, tea.Cmd) {
	sessions, err := m.store.ListSessions(m.ctx, vault.ListOptions{Project: project})
	if err != nil {
		return m.withError(err.Error()), nil
	}
	m = m.clearStatus()
	var cmd tea.Cmd
	m.list, cmd = m.list.setSessions(sessions, project)
	return m, cmd
}

// requestAction records a deferred restore/resume intent for the given session
// and quits so the CLI can perform it with a restored terminal. A blank uuid (no
// selection) is a no-op.
func (m Model) requestAction(kind ActionKind, uuid string) (tea.Model, tea.Cmd) {
	if uuid == "" {
		return m, nil
	}
	m.action = Action{Kind: kind, SessionUUID: uuid}
	m.quitting = true
	return m, tea.Quit
}

// actionFor maps the r/R keys to their action kind (r = restore, R = resume).
func actionFor(k string) ActionKind {
	if k == "R" {
		return ActionResume
	}
	return ActionRestore
}

func (m Model) updateView(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// The app owns the destructive/exec and clipboard keys (the viewer binds none
	// of them) so they reach the action/clipboard machinery rather than the
	// scrolling viewport. They act on the open session / visible message.
	switch msg.String() {
	case "r", "R":
		return m.requestAction(actionFor(msg.String()), m.viewer.sess.UUID)
	case "c":
		tm, ok := m.viewer.currentMessage()
		if !ok {
			return m.withStatus("nothing to copy"), nil
		}
		return m.withStatus("copied current message (OSC-52 — terminal may not support clipboard)"),
			copyToClipboard(m.clipOut, tm.Body)
	}

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
			return m.withError(err.Error()), nil
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
	sess, err := m.store.GetSession(m.ctx, sessionID)
	if err != nil {
		return m, fmt.Errorf("opening session %s: %w", sessionID, err)
	}
	files, err := m.store.GetFiles(m.ctx, sess.UUID)
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
	if m.status == "" {
		return body
	}
	// The status sits on the row bodyHeight reserved for it; oneLine + truncate
	// guarantee exactly one row so the composed View never exceeds m.height.
	style := m.styles.StatusBar
	if m.statusErr {
		style = m.styles.ErrorMsg
	}
	return body + "\n" + style.Render(truncate(oneLine(m.status), max(1, m.width)))
}
