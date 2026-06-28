package tui

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// twoProjectApp builds a list-mode app over two sessions in distinct projects so
// the project filter has something to narrow.
func twoProjectApp(t *testing.T) (Model, *stubStore) {
	t.Helper()
	st := &stubStore{
		sessions: []vault.Session{
			{UUID: "aaaa1111", Title: "alpha work", ProjectPath: "/home/u/alpha"},
			{UUID: "bbbb2222", Title: "beta work", ProjectPath: "/home/u/beta"},
		},
		files: map[string][]vault.File{},
	}
	m, err := newModel(context.Background(), st, Options{})
	require.NoError(t, err)
	tm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return tm.(Model), st
}

func typeRunes(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		next, _ := m.Update(keyMsg(string(r)))
		m = next.(Model)
	}
	return m
}

func TestApp_FilterNarrowsByProject(t *testing.T) {
	m, st := twoProjectApp(t)
	require.Len(t, m.list.list.Items(), 2)

	// f opens the project-filter input.
	next, _ := m.Update(keyMsg("f"))
	m = next.(Model)
	require.True(t, m.list.filtering)

	// Typing re-queries the store and narrows to the matching project.
	m = typeRunes(t, m, "alph")
	assert.Equal(t, "alph", m.list.filterValue())
	assert.Equal(t, "alph", st.lastListOpts.Project, "the filter substring is passed to ListSessions(Project:)")
	require.Len(t, m.list.list.Items(), 1, "only the alpha session survives the filter")
	sel, ok := m.list.selected()
	require.True(t, ok)
	assert.Equal(t, "aaaa1111", sel.UUID)

	// enter applies the filter and returns to navigation (items stay narrowed).
	next, _ = m.Update(keyMsg("enter"))
	m = next.(Model)
	assert.False(t, m.list.filtering)
	assert.Len(t, m.list.list.Items(), 1)
	assert.Equal(t, "alph", m.list.project)
}

func TestApp_FilterEscClearsAndRestoresAll(t *testing.T) {
	m, _ := twoProjectApp(t)

	next, _ := m.Update(keyMsg("f"))
	m = next.(Model)
	m = typeRunes(t, m, "alpha")
	require.Len(t, m.list.list.Items(), 1)

	// esc clears the filter: all sessions return and the input closes.
	next, _ = m.Update(keyMsg("esc"))
	m = next.(Model)
	assert.False(t, m.list.filtering)
	assert.Equal(t, "", m.list.project)
	assert.Len(t, m.list.list.Items(), 2)
}

func TestApp_FilterViewShowsInput(t *testing.T) {
	m, _ := twoProjectApp(t)
	next, _ := m.Update(keyMsg("f"))
	m = next.(Model)
	out := m.View()
	assert.Contains(t, out, "filter project:", "the filter prompt is visible while filtering")
	assert.Contains(t, out, "esc clear")
}

func TestApp_ListRestoreIntent(t *testing.T) {
	m, _ := newTestApp(t, Options{}) // list mode, one sample session selected
	next, cmd := m.Update(keyMsg("r"))
	m = next.(Model)

	require.NotNil(t, cmd)
	assert.Equal(t, tea.Quit(), cmd(), "r quits so the CLI restores with a restored terminal")
	assert.True(t, m.quitting)
	assert.Equal(t, ActionRestore, m.action.Kind)
	assert.Equal(t, "abcdef0123456789", m.action.SessionUUID)
}

func TestApp_ListResumeIntent(t *testing.T) {
	m, _ := newTestApp(t, Options{})
	next, cmd := m.Update(keyMsg("R"))
	m = next.(Model)

	require.NotNil(t, cmd)
	assert.Equal(t, tea.Quit(), cmd())
	assert.Equal(t, ActionResume, m.action.Kind)
	assert.Equal(t, "abcdef0123456789", m.action.SessionUUID)
}

func TestApp_ViewRestoreResumeIntentUsesOpenSession(t *testing.T) {
	m, _ := newTestApp(t, Options{})
	next, _ := m.Update(keyMsg("enter")) // open the session in the viewer
	m = next.(Model)
	require.Equal(t, modeView, m.mode)

	next, cmd := m.Update(keyMsg("R"))
	m = next.(Model)
	require.NotNil(t, cmd)
	assert.Equal(t, tea.Quit(), cmd())
	assert.Equal(t, ActionResume, m.action.Kind)
	assert.Equal(t, "abcdef0123456789", m.action.SessionUUID, "the viewer's open session is the target")
}

func TestApp_RestoreNoopWhenListEmpty(t *testing.T) {
	st := &stubStore{sessions: nil, files: map[string][]vault.File{}}
	m, err := newModel(context.Background(), st, Options{})
	require.NoError(t, err)
	tm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = tm.(Model)

	next, cmd := m.Update(keyMsg("r"))
	m = next.(Model)
	assert.Nil(t, cmd, "no selection → no quit")
	assert.False(t, m.quitting)
	assert.Equal(t, ActionNone, m.action.Kind)
}

func TestApp_CopyEmitsOSC52AndConfirms(t *testing.T) {
	m, _ := newTestApp(t, Options{})
	next, _ := m.Update(keyMsg("enter")) // open the viewer at the top message
	m = next.(Model)
	require.Equal(t, modeView, m.mode)

	var buf bytes.Buffer
	m.clipOut = &buf

	next, cmd := m.Update(keyMsg("c"))
	m = next.(Model)
	require.NotNil(t, cmd)
	assert.Contains(t, m.status, "copied", "a status-bar confirmation is shown (OSC-52 cannot be verified)")

	cmd() // execute the clipboard command
	out := buf.String()
	require.True(t, strings.HasPrefix(out, "\x1b]52;c;"), "writes the OSC-52 set-clipboard escape")
	require.True(t, strings.HasSuffix(out, "\x07"))

	b64 := strings.TrimSuffix(strings.TrimPrefix(out, "\x1b]52;c;"), "\x07")
	dec, err := base64.StdEncoding.DecodeString(b64)
	require.NoError(t, err)
	assert.Equal(t, "first question about timeouts", string(dec),
		"the copied payload is the message at the top of the viewport")
}

func TestOSC52Sequence(t *testing.T) {
	want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("hello")) + "\x07"
	assert.Equal(t, want, osc52Sequence("hello"))
}

func TestViewer_CurrentMessage(t *testing.T) {
	v := loadedViewer(t)
	msg, ok := v.currentMessage()
	require.True(t, ok)
	assert.Equal(t, vault.RoleUser, msg.Role, "top of viewport is the first (user) message")

	// An unloaded viewer has no current message.
	_, ok = newViewerModel(DefaultStyles(), 80, 24).currentMessage()
	assert.False(t, ok)
}

func TestApp_CopyStatusClearsOnNextKey(t *testing.T) {
	m, _ := newTestApp(t, Options{})
	next, _ := m.Update(keyMsg("enter"))
	m = next.(Model)
	var buf bytes.Buffer
	m.clipOut = &buf

	next, _ = m.Update(keyMsg("c"))
	m = next.(Model)
	require.Contains(t, m.status, "copied")

	// A subsequent navigation key clears the transient confirmation so it doesn't
	// linger and permanently consume a row.
	next, _ = m.Update(keyMsg("j"))
	m = next.(Model)
	assert.Equal(t, "", m.status, "the copy confirmation clears on the next keystroke")
}

func TestApp_StatusReservesRowNoOverflow(t *testing.T) {
	// A transcript taller than the viewport so the body alone fills the height.
	var lines []map[string]any
	for i := range 80 {
		lines = append(lines, assistantLine(fmt.Sprintf("m%d", i),
			[]map[string]any{textBlock(fmt.Sprintf("body line %d", i))}))
	}
	sess := vault.Session{UUID: "tall00000000", Title: "tall", RawJSONL: jsonlLines(t, lines...)}
	st := &stubStore{sessions: []vault.Session{sess}, files: map[string][]vault.File{}}
	m, err := newModel(context.Background(), st, Options{Mode: "view", SessionID: "tall00000000"})
	require.NoError(t, err)
	tm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m = tm.(Model)
	require.Equal(t, modeView, m.mode)

	require.LessOrEqual(t, len(strings.Split(m.View(), "\n")), 20, "body alone fits the height")

	var buf bytes.Buffer
	m.clipOut = &buf
	next, _ := m.Update(keyMsg("c"))
	m = next.(Model)
	require.Contains(t, m.status, "copied")
	// The status row is reserved by shrinking the viewer (bodyHeight), not appended
	// on top — so the composed View never exceeds the terminal height.
	assert.LessOrEqual(t, len(strings.Split(m.View(), "\n")), 20,
		"status must not push the View past the terminal height")
}

func TestApp_CopyNothingToCopyWhenEmpty(t *testing.T) {
	// A session whose transcript parses to no messages → nothing to copy.
	st := &stubStore{
		sessions: []vault.Session{{UUID: "deadbeef00", RawJSONL: nil}},
		files:    map[string][]vault.File{"deadbeef00": nil},
	}
	m, err := newModel(context.Background(), st, Options{Mode: "view", SessionID: "deadbeef00"})
	require.NoError(t, err)
	tm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = tm.(Model)
	require.Equal(t, modeView, m.mode)

	var buf bytes.Buffer
	m.clipOut = &buf
	next, cmd := m.Update(keyMsg("c"))
	m = next.(Model)
	assert.Nil(t, cmd, "no message → no clipboard write")
	assert.Equal(t, "nothing to copy", m.status)
	assert.Empty(t, buf.String())
}
