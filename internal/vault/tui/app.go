package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textinput"
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
	RenameSession(ctx context.Context, prefix string, opts vault.RenameOptions) (*vault.Session, error)
}

// The rename editor's fixed texts share one reserved row: prompt, value area,
// two-space gap, hint. Every rune here is single-width, so rune counts are
// display widths (the same measurement truncate uses), which is what
// layoutSubmodels relies on to size the value area.
const (
	renamePrompt     = "name (empty clears): "
	renameHint       = "enter save · esc cancel"
	renameSavingHint = "saving…"
)

// renameResultMsg carries the outcome of an asynchronous RenameSession write
// back into Update. sess is the store's post-write session metadata (imported
// title plus name state), nil on error.
type renameResultMsg struct {
	sess    *vault.Session
	cleared bool
	err     error
}

// viewerFrame is one suspended level of a parent → child session chain: the
// parent's viewer as it was the moment its child marker was opened. It wraps the
// whole viewerModel rather than the (session, files, offset, focused marker)
// tuple the plan sketched: the value model already carries exactly that state
// plus the parent's rendered transcript and any open detail view, so a pop is a
// plain reassignment — no re-parse, no re-derived offset, no lost detail state.
// The cost is holding each suspended level's render in memory for the depth of
// the chain, which is bounded by how many children the user drills into (Codex
// nests one or two levels). A pop re-runs setSize so a terminal resize while
// in the child re-wraps the parent at the current width (setSize preserves the
// top source line across the re-wrap).
type viewerFrame struct {
	viewer viewerModel
}

// Options configures the initial screen, set from the launching CLI command.
type Options struct {
	Mode      string // "list" (default) | "search" | "view"
	Query     string // initial query for search mode
	SessionID string // session to open for view mode (partial UUID, 8+ chars)
	// Platform scopes both the session list and live search for the lifetime of
	// the TUI. The empty value includes every platform.
	Platform vault.Platform
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

	// viewerStack holds the viewers a child-session open (viewerActionOpenChild)
	// stepped away from, innermost last: opening a child pushes the current
	// viewer and loads the child into m.viewer; esc/q pops back to the parent
	// exactly as it was (scroll offset, focused marker, detail state) instead of
	// returning to prevMode. Empty whenever the viewer was opened from list or
	// search, so those flows are unchanged (design § TUI — root-routed child
	// open). See viewerFrame for why a frame is the whole viewer.
	//
	// Suspended frames are not refreshed by handleRenameResult: a rename only
	// ever targets the session currently shown, and a chain cannot loop, so no
	// suspended frame can hold the renamed session.
	viewerStack []viewerFrame

	// clipOut is where the OSC-52 clipboard escape is written for the `c` key —
	// os.Stderr in production (the same TTY as the renderer, but out-of-band), a
	// buffer in tests. See copyToClipboard.
	clipOut io.Writer
	// action is the deferred CLI intent (restore/resume) recorded when the user
	// presses r/R; Run returns it after the program exits so the CLI performs the
	// exec with a restored terminal. ActionNone until requested.
	action Action

	// The rename editor is root-model state (not per-mode) because every mode —
	// list navigation (e), viewer (e), and search (ctrl+e) — opens the same
	// single-line input over the current body. While renaming, keys route to the
	// editor before mode routing; while renamePending, all input is consumed so
	// a duplicate submit cannot fire a second write and Esc cannot close the
	// editor mid-flight (an error result must find the editor open, text intact).
	renameInput   textinput.Model
	renaming      bool
	renamePending bool
	renameUUID    string // full UUID of the rename target

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

	// The initial read hides children, the listModel's default (children reach
	// the list through the toggle key — see listModel.includeChildren), while
	// retaining any platform scope supplied by the launching CLI command.
	sessions, err := st.ListSessions(ctx, vault.ListOptions{Platform: opts.Platform})
	if err != nil {
		return Model{}, fmt.Errorf("loading sessions: %w", err)
	}

	ri := textinput.New()
	ri.Prompt = renamePrompt
	ri.CharLimit = 256

	list := newListModel(sessions, styles, 0, 0)
	list.platform = opts.Platform
	search := newSearchModel(ctx, st, styles, 0, 0)
	search.platform = opts.Platform

	m := Model{
		ctx:         ctx,
		store:       st,
		styles:      styles,
		mode:        modeList,
		list:        list,
		viewer:      newViewerModel(styles, 0, 0),
		search:      search,
		renameInput: ri,
		clipOut:     os.Stderr,
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
// for the status line whenever a status is set, and one for the rename editor
// while it is open, so the composed View never exceeds m.height (which would
// scroll the alt-screen and flicker every frame).
func (m Model) bodyHeight() int {
	h := m.height
	if m.status != "" {
		h--
	}
	if m.renaming {
		h--
	}
	return max(1, h)
}

// layoutSubmodels (re)sizes every sub-model to the current width and bodyHeight.
// Routing both WindowSizeMsg and status changes through here keeps the reserved
// status row consistent across resizes.
func (m Model) layoutSubmodels() Model {
	h := m.bodyHeight()
	m.list = m.list.setSize(m.width, h)
	m.viewer = m.viewer.setSize(m.width, h)
	m.search = m.search.setSize(m.width, h)
	boundInputWidth(&m.renameInput, m.width, utf8.RuneCountInString(renameHint))
	return m
}

// boundInputWidth sizes a single-line textinput's value area so the whole row
// — prompt, value, cursor, and a trailing hint of hintWidth cells (0 when
// nothing follows the input) — fits in rowWidth. Without a bound, a value wider
// than the terminal runs past the edge, where the renderer truncates the row
// and the cursor vanishes (the user types blind); with one, textinput scrolls
// the value horizontally under the cursor. textinput renders up to Width value
// cells plus one cursor cell, hence the extra cell reserved here. A rowWidth
// of 0 (terminal size not yet known) leaves the input unbounded.
//
// textinput recomputes its scroll window only when the cursor moves outside it
// (handleOverflow), never on a Width change — so after a resize a cursor
// sitting inside the old, wider window would keep rendering that window.
// Jumping to the end always re-bounds the window to the new Width; moving back
// then either keeps that bounded window or re-bounds from the left.
func boundInputWidth(in *textinput.Model, rowWidth, hintWidth int) {
	if rowWidth <= 0 {
		in.Width = 0
		return
	}
	gap := 0
	if hintWidth > 0 {
		gap = 2 // the two-space separator before a hint
	}
	in.Width = max(1, rowWidth-utf8.RuneCountInString(in.Prompt)-1-gap-hintWidth)
	pos := in.Position()
	in.CursorEnd()
	in.SetCursor(pos)
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
		// this same update; clearing first would just relayout twice — but only in
		// normal routing: while renaming, "c" just types into the editor.
		if msg.String() != "c" || m.renaming {
			m = m.clearStatus()
		}
		// The open rename editor consumes every key before mode routing, so a
		// mode's own bindings (including a search input that would otherwise eat
		// the keystroke) never fire while the user is editing a name.
		if m.renaming {
			return m.updateRename(msg)
		}
		switch m.mode {
		case modeList:
			return m.updateList(msg)
		case modeView:
			return m.updateView(msg)
		case modeSearch:
			return m.updateSearch(msg)
		}
	case renameResultMsg:
		return m.handleRenameResult(msg)
	case debounceMsg, searchResultsMsg:
		// Search-owned messages always reach the search model regardless of the
		// active mode: the post-rename background rerun (handleRenameResult) and a
		// debounce tick that outlives leaving search mode must not be dropped by
		// mode routing.
		var cmd tea.Cmd
		m.search, cmd = m.search.Update(msg)
		return m, cmd
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
		// Opening the filter refreshes the list's snapshot once; keystrokes then
		// filter that snapshot in memory. A failed refresh keeps the current
		// items (never blank the browser) and says so in the status line.
		next, cmd, err := m.reloadSessions()
		if err != nil {
			next = m.withError("refreshing sessions failed: " + err.Error())
		}
		m = next
		m.list = m.list.startFilter()
		return m, cmd
	case "e":
		sess, ok := m.list.selected()
		if !ok {
			return m, nil
		}
		return m.startRename(sess.UUID, sess.EffectiveTitle())
	case "r", "R":
		sess, ok := m.list.selected()
		if !ok {
			return m, nil
		}
		return m.requestAction(actionFor(msg.String()), sess.UUID)
	case listChildrenKey:
		return m.toggleChildren()
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

// toggleChildren flips the list between hiding and showing child sessions
// (Codex sub-agent rollouts, parent_uuid set — hidden by default like Codex's
// own pickers, design § Sub-agent Model) and re-reads the snapshot with the new
// ListOptions.IncludeChildren, since the default read never fetched the
// children. A failed re-read reverts the toggle and keeps the current items
// (never blank the browser), reporting the failure in the status line.
func (m Model) toggleChildren() (tea.Model, tea.Cmd) {
	m.list = m.list.setIncludeChildren(!m.list.includeChildren)
	next, cmd, err := m.reloadSessions()
	if err != nil {
		m.list = m.list.setIncludeChildren(!m.list.includeChildren)
		return m.withError("refreshing sessions failed: " + err.Error()), nil
	}
	return next, cmd
}

// updateListFilter handles keys while the list's session-filter input is active.
// esc clears the filter (restoring all sessions); enter applies it and returns to
// navigation; arrow keys move the (filtered) selection; any other key edits the
// input (including "e" — the rename binding applies in navigation state only) and
// re-filters on a value change.
func (m Model) updateListFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.list = m.list.stopFilter()
		return m.applySessionFilter("")
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
		next, applyCmd := m.applySessionFilter(m.list.filterValue())
		return next, tea.Batch(cmd, applyCmd)
	}
	return m, cmd
}

// applySessionFilter narrows the list's cached snapshot to the sessions
// matching needle across effective title, project path, and UUID
// (filterSessions — the same Unicode-folding matcher as `vault list --name`).
// Purely in memory: the snapshot is refreshed by reloadSessions when the
// filter opens and after a rename, not per keystroke.
func (m Model) applySessionFilter(needle string) (tea.Model, tea.Cmd) {
	m = m.clearStatus()
	var cmd tea.Cmd
	m.list, cmd = m.list.setSessions(m.list.all, needle)
	return m, cmd
}

// reloadSessions re-reads the session list from the store into the list's
// snapshot and reapplies the active filter. It is the only path that refreshes
// the snapshot, called at the three moments it must be authoritative: when the
// filter opens, after a rename, and when the children toggle flips. The read
// honors the list's current children setting (listModel.listOptions). On error
// the current items are left in place and the error is returned for the caller
// to surface.
func (m Model) reloadSessions() (Model, tea.Cmd, error) {
	sessions, err := m.store.ListSessions(m.ctx, m.list.listOptions())
	if err != nil {
		return m, nil, err
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.setSessions(sessions, m.list.applied)
	return m, cmd, nil
}

// startRename opens the rename editor over the current body, prefilled with the
// target session's effective title so the user edits rather than retypes it.
// Submitting an emptied input writes a clear tombstone (see updateRename).
func (m Model) startRename(uuid, effectiveTitle string) (tea.Model, tea.Cmd) {
	m.renaming = true
	m.renameUUID = uuid
	m.renameInput.SetValue(effectiveTitle)
	m.renameInput.CursorEnd()
	m.renameInput.Focus()
	return m.layoutSubmodels(), nil
}

// closeRename tears down the editor state (cancel and success paths; an error
// keeps the editor open instead) and reclaims the reserved row.
func (m Model) closeRename() Model {
	m.renaming = false
	m.renamePending = false
	m.renameUUID = ""
	m.renameInput.Blur()
	return m.layoutSubmodels()
}

// updateRename handles keys while the rename editor is open. While a write is
// pending every key is consumed and ignored: Enter must not double-submit, and
// Esc must not close the editor mid-flight — an error result has to find the
// editor open with its text intact. The write is a local SQLite upsert, so the
// pending window is short; ctrl+c (handled before this routing) still quits.
func (m Model) updateRename(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.renamePending {
		return m, nil
	}
	switch msg.String() {
	case "esc":
		return m.closeRename(), nil
	case "enter":
		var opts vault.RenameOptions
		if v := m.renameInput.Value(); strings.TrimSpace(v) == "" {
			opts.Clear = true
		} else {
			// The raw value is passed through: NormalizeSessionName (trim, secret
			// redaction, validation) is owned by the store, shared with the CLI.
			opts.Name = v
		}
		m.renamePending = true
		return m, m.runRename(m.renameUUID, opts)
	}
	var cmd tea.Cmd
	m.renameInput, cmd = m.renameInput.Update(msg)
	return m, cmd
}

// runRename executes the store write off the Update goroutine (the same
// bubbletea command convention as searchModel.runSearch). uuid is the full
// session UUID, so the prefix lookup inside RenameSession is exact.
func (m Model) runRename(uuid string, opts vault.RenameOptions) tea.Cmd {
	store, ctx := m.store, m.ctx
	return func() tea.Msg {
		sess, err := store.RenameSession(ctx, uuid, opts)
		return renameResultMsg{sess: sess, cleared: opts.Clear, err: err}
	}
}

// handleRenameResult closes the editor and refreshes presentation state on
// success, or keeps the editor open (text intact) with an error status on
// failure. Refresh is authoritative, not a local mutation (design §TUI
// Interaction), and covers every cached surface so a rename is immediately
// reflected everywhere (Success Criterion 1): the list re-queries and reapplies
// its active filter (the renamed item may legitimately disappear from a
// non-matching filter), an active search query reruns in the background, and an
// open viewer refreshes its session metadata without reparsing the transcript.
func (m Model) handleRenameResult(msg renameResultMsg) (tea.Model, tea.Cmd) {
	m.renamePending = false
	if msg.err != nil {
		return m.withError("rename error: " + msg.err.Error()), nil
	}
	if msg.sess == nil {
		// The store contract is session-or-error; a nil session without an error
		// is a store bug. Fail loud in the status line rather than panic inside
		// Update, and keep the editor open so the user can retry or cancel.
		return m.withError("rename error: store returned no session"), nil
	}
	m = m.closeRename()

	title := msg.sess.EffectiveTitle()
	status := fmt.Sprintf("renamed %s to %q", shortID(msg.sess.UUID), title)
	if msg.cleared {
		status = fmt.Sprintf("cleared custom name for %s — title is now %q", shortID(msg.sess.UUID), title)
	}

	// The viewer needs only the returned session, so refresh it before the list
	// re-read: a failing re-read must not leave a committed rename stale in the
	// header the user is looking at.
	m.viewer = m.viewer.setSessionMeta(*msg.sess)

	m, listCmd, err := m.reloadSessions()
	if err != nil {
		return m.withError("rename succeeded, but refreshing sessions failed: " + err.Error()), nil
	}
	m.list = m.list.selectSession(msg.sess.UUID)

	var searchCmd tea.Cmd
	m.search, searchCmd = m.search.refresh()

	return m.withStatus(status), tea.Batch(listCmd, searchCmd)
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
	case "e":
		// Renames the session owning the viewer, even from a subagent/inline
		// detail view — a detail is still part of the same archived session.
		return m.startRename(m.viewer.sess.UUID, m.viewer.sess.EffectiveTitle())
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
	switch action.kind {
	case viewerActionBack:
		return m.popViewer(), cmd
	case viewerActionOpenChild:
		return m.openChild(action.childUUID), cmd
	}
	return m, cmd
}

// openChild is the root-routed half of opening a Codex child session from its
// parent's launch marker (design § TUI): the viewer emitted the child's uuid
// (viewerModel.openFocusedMarker), and the root Model — the only holder of the
// store and the context — loads it. The current viewer is pushed onto
// viewerStack so esc/q returns to the parent exactly where it was (popViewer),
// and the mode stays modeView with prevMode untouched, so the eventual exit
// from the whole chain still lands where the parent was opened from.
//
// A child that is not archived (the parent was imported but the child rollout
// was not — it may never have been written to disk, or was imported on another
// machine) is a transient status line, not an error dialog: the viewer stays
// on the parent and nothing is pushed. Any other store failure is reported in
// the error style, likewise without mutating the viewer.
func (m Model) openChild(uuid string) Model {
	sess, err := m.store.GetSession(m.ctx, uuid)
	if errors.Is(err, vault.ErrSessionNotFound) {
		return m.withStatus(fmt.Sprintf("child session %s not archived", shortID(uuid)))
	}
	if err != nil {
		return m.withError(fmt.Sprintf("opening child session %s: %v", shortID(uuid), err))
	}
	files, err := m.store.GetFiles(m.ctx, sess.UUID)
	if err != nil {
		return m.withError(fmt.Sprintf("loading child session files: %v", err))
	}
	m.viewerStack = append(m.viewerStack, viewerFrame{viewer: m.viewer})
	m.viewer = m.viewer.loadSession(*sess, files)
	return m
}

// popViewer handles the viewer's back action: with a suspended parent on
// viewerStack it restores that frame; with an empty stack it leaves the viewer
// for prevMode exactly as before child sessions existed, so opens from list and
// search are unchanged.
//
// The frame is restored verbatim — same scroll offset, same focused marker —
// unless the terminal was resized while the child was open, in which case it
// is re-wrapped through setSize. setSize is deliberately NOT run when the size
// is unchanged: it re-derives the offset from the top message's source line, so
// an offset the viewport had clamped mid-message (a focused marker in the last
// page) would drift up to that message's first row. A real resize accepts that
// message-level fidelity, exactly as any resize of an open viewer does today.
func (m Model) popViewer() Model {
	if n := len(m.viewerStack); n > 0 {
		frame := m.viewerStack[n-1]
		m.viewerStack = m.viewerStack[:n-1]
		m.viewer = frame.viewer
		if h := m.bodyHeight(); m.viewer.width != m.width || m.viewer.height != h {
			m.viewer = m.viewer.setSize(m.width, h)
		}
		return m
	}
	m.mode = m.prevMode
	return m
}

func (m Model) updateSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = m.prevMode
		return m, nil
	case "ctrl+e":
		// ctrl+e, not bare e: the search query input is always focused, so a
		// printable rename key would make that letter untypeable in queries
		// (design.md §TUI Interaction). This shadows textinput's default ctrl+e
		// line-end binding; `end` still moves the cursor to the end.
		r, ok := m.search.selected()
		if !ok {
			return m, nil
		}
		return m.startRename(r.SessionUUID, r.Title)
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
	// A fresh open from list or search starts a new chain: any parents suspended
	// by a previous child drill-down must not resurface behind this session.
	m.viewerStack = nil
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
	rows := []string{body}
	if m.renaming {
		rows = append(rows, m.renameLine())
	}
	if m.status != "" {
		// The status sits on a row bodyHeight reserved for it; oneLine + truncate
		// guarantee exactly one row so the composed View never exceeds m.height.
		style := m.styles.StatusBar
		if m.statusErr {
			style = m.styles.ErrorMsg
		}
		rows = append(rows, style.Render(truncate(oneLine(m.status), max(1, m.width))))
	}
	return strings.Join(rows, "\n")
}

// renameLine renders the open rename editor on its reserved row (above the
// status row, which carries any validation error while the editor stays open).
func (m Model) renameLine() string {
	hint := renameHint
	if m.renamePending {
		hint = renameSavingHint
	}
	return m.renameInput.View() + "  " + m.styles.Help.Render(hint)
}
