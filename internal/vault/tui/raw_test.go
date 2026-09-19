package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatRawJSONL(t *testing.T) {
	for _, tt := range []struct{ name, input, want string }{
		{"empty", "", "(empty archived transcript)"},
		{"records", "{\"z\":9007199254740993,\"a\":1e+09}\n{\"unknown\":\"\\u0061\"}", "{\n  \"z\": 9007199254740993,\n  \"a\": 1e+09\n}\n{\n  \"unknown\": \"\\u0061\"\n}"},
		{"blanks and trailing newline", "\n{\"a\":true}\n\n", "\n{\n  \"a\": true\n}\n\n"},
		{"duplicate fields", "{\"a\":1,\"a\":2}", "{\n  \"a\": 1,\n  \"a\": 2\n}"},
		{"unicode and literal escapes", "{\"text\":\"你好 café \\n \\u001b\"}", "{\n  \"text\": \"你好 café \\n \\u001b\"\n}"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(tt.input)
			got, err := formatRawJSONL(context.Background(), raw)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.input, string(raw), "archive is unchanged")
		})
	}
}

func TestFormatRawJSONL_InvalidRecordsRemainVisible(t *testing.T) {
	raw := "{\"ok\":1}\nnot JSON\n{\"after\":true}\n{\"torn\":"
	got, err := formatRawJSONL(context.Background(), []byte(raw))
	require.NoError(t, err)
	assert.Contains(t, got, "invalid JSON at source line 2:")
	assert.Contains(t, got, "\nnot JSON\n")
	assert.Contains(t, got, "\"after\": true")
	assert.Contains(t, got, "invalid JSON at source line 4:")
	assert.True(t, strings.HasSuffix(got, "{\"torn\":"))
}

func TestFormatRawJSONL_LargeRecordAndTerminalControls(t *testing.T) {
	body := strings.Repeat("x", 2*1024*1024)
	got, err := formatRawJSONL(context.Background(), []byte("{\"body\":\""+body+"\"}\n{\"tail\":true}"))
	require.NoError(t, err)
	assert.Contains(t, got, body)
	assert.Contains(t, got, "\"tail\": true")
	got, err = formatRawJSONL(context.Background(), []byte("bad\x1b]52;c;payload\a\r\xff\u009b\n{}"))
	require.NoError(t, err)
	assert.Contains(t, got, `bad\u001b]52;c;payload\u0007\u000d\xff\u009b`)
	assert.NotContains(t, got, "\x1b")
	assert.True(t, strings.HasSuffix(got, "{}"))
}

func TestFormatRawJSONL_Cancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := formatRawJSONL(ctx, []byte(`{"ok":true}`))
	assert.ErrorIs(t, err, context.Canceled)
}

// openRaw executes the load command deterministically, as Bubble Tea would.
func openRaw(t *testing.T, m Model) Model {
	t.Helper()
	next, cmd := m.Update(keyMsg("v"))
	m = next.(Model)
	require.Equal(t, modeRaw, m.mode)
	require.NotNil(t, cmd)
	assert.Contains(t, m.View(), "loading")
	next, _ = m.Update(cmd())
	m = next.(Model)
	require.Empty(t, m.raw.err)
	require.False(t, m.raw.loading)
	return m
}

func TestApp_RawListReturnAndReadOnly(t *testing.T) {
	m, st := newTestApp(t, Options{})
	before := append([]byte(nil), st.sessions[0].RawJSONL...)
	selected, _ := m.list.selected()
	m = openRaw(t, m)
	assert.Contains(t, m.View(), "archived JSONL")
	assert.Contains(t, m.View(), `"type": "user"`)
	m = press(t, m, "r", "R", "e", "c")
	assert.False(t, m.quitting)
	assert.False(t, m.renaming)
	assert.Equal(t, ActionNone, m.action.Kind)
	m = press(t, m, "q")
	assert.Equal(t, modeList, m.mode)
	after, _ := m.list.selected()
	assert.Equal(t, selected.UUID, after.UUID)
	assert.Equal(t, before, st.sessions[0].RawJSONL)
}

func TestApp_RawViewReturnsToSameDetail(t *testing.T) {
	for _, target := range []string{"main", "subagent", "tool"} {
		t.Run(target, func(t *testing.T) {
			m, _ := newTestApp(t, Options{})
			m = press(t, m, "enter")
			want := m.viewer.sess.RawJSONL
			switch target {
			case "subagent":
				m.viewer = m.viewer.openSubagent("xyz", 1)
				want = m.viewer.subagentBytes("xyz")
			case "tool":
				m.viewer = m.viewer.openInlineContent(vault.TranscriptMessage{Body: "expanded tool result"})
			}
			before := m.viewer
			m = openRaw(t, m)
			formatted, err := formatRawJSONL(context.Background(), want)
			require.NoError(t, err)
			// Use enough viewport space to compare all content, including metadata.
			m.raw.vp.Width, m.raw.vp.Height = 200, m.raw.vp.TotalLineCount()
			rows := strings.Split(m.raw.vp.View(), "\n")
			for i := range rows {
				rows[i] = strings.TrimRight(rows[i], " ") // viewport pads each terminal row
			}
			assert.Equal(t, strings.TrimSpace(formatted), strings.TrimSpace(strings.Join(rows, "\n")))
			m = press(t, m, "esc")
			assert.Equal(t, modeView, m.mode)
			assert.Equal(t, before.View(), m.viewer.View())
			assert.Equal(t, before.inSub, m.viewer.inSub)
			assert.Equal(t, before.inInline, m.viewer.inInline)
		})
	}
}

func TestApp_RawCodexChildPreservesParentChain(t *testing.T) {
	parent := codexParentSession(t, codexParentID, codexChildID, 40)
	child := codexChildSession(t, codexChildID, codexParentID)
	m, _ := codexFamilyApp(t, parent, child)
	m = press(t, m, "enter", "]")
	parentOffset, parentFocus := m.viewer.vp.YOffset, m.viewer.focusedMarker
	m = press(t, m, "enter")
	require.Equal(t, codexChildID, m.viewer.sess.UUID)
	m = openRaw(t, m)
	assert.Contains(t, m.View(), codexChildID)
	assert.Contains(t, m.View(), `"payload"`)
	m = press(t, m, "q", "q")
	assert.Equal(t, codexParentID, m.viewer.sess.UUID)
	assert.Equal(t, parentOffset, m.viewer.vp.YOffset)
	assert.Equal(t, parentFocus, m.viewer.focusedMarker)
}

func TestApp_RawSearchAndFilterKeepPrintableV(t *testing.T) {
	m, _ := newTestApp(t, Options{})
	m = press(t, m, "f", "v")
	assert.Equal(t, modeList, m.mode)
	assert.Equal(t, "v", m.list.filterValue())
	m = press(t, m, "esc", "/", "v")
	assert.Equal(t, modeSearch, m.mode)
	assert.Equal(t, "v", m.search.input.Value())
	m.search.results = []vault.SearchResult{{SessionUUID: "abcdef0123456789", SubagentID: "xyz", LineIndex: 1}}
	m = press(t, m, "enter")
	m = openRaw(t, m)
	m = press(t, m, "q", "q", "q")
	assert.Equal(t, modeSearch, m.mode)
	assert.Equal(t, "v", m.search.input.Value())
}

func TestApp_RawStaleLoadAndCancel(t *testing.T) {
	m, _ := newTestApp(t, Options{})
	next, oldCmd := m.Update(keyMsg("v"))
	m = next.(Model)
	oldMsg := oldCmd() // completed but not yet delivered
	m = press(t, m, "esc")
	next, cmd := m.Update(keyMsg("v"))
	m = next.(Model)
	next, _ = m.Update(oldMsg)
	m = next.(Model)
	assert.True(t, m.raw.loading, "an older completion cannot replace the new view")
	m = press(t, m, "q")
	msg := cmd().(rawLoadedMsg)
	assert.ErrorIs(t, msg.err, context.Canceled)
	next, _ = m.Update(msg)
	assert.Equal(t, modeList, next.(Model).mode)
}

func TestApp_RawLoadErrorAndEmptySelection(t *testing.T) {
	m, st := newTestApp(t, Options{})
	st.getErr = errors.New("archive unavailable")
	next, cmd := m.Update(keyMsg("v"))
	next, _ = next.(Model).Update(cmd())
	m = next.(Model)
	assert.Contains(t, m.View(), "archive unavailable")
	m = press(t, m, "q")
	assert.Equal(t, modeList, m.mode)
	st.sessions = nil
	m, err := newModel(context.Background(), st, Options{})
	require.NoError(t, err)
	next, cmd = m.Update(keyMsg("v"))
	assert.Nil(t, cmd)
	assert.Equal(t, modeList, next.(Model).mode)
}

func TestApp_RawScrollResizeAndReturn(t *testing.T) {
	m, st := newTestApp(t, Options{})
	st.sessions[0].RawJSONL = []byte(strings.Repeat(`{"wide":"`+strings.Repeat("x", 150)+`TAIL"}`+"\n", 40))
	m = openRaw(t, m)
	m = press(t, m, "l", "l", "l", "l", "l", "l", "l", "l", "l", "l", "l", "l")
	assert.Contains(t, m.raw.vp.View(), "TAIL")
	m = press(t, m, "0")
	assert.Contains(t, m.raw.vp.View(), `"wide"`)
	m = press(t, m, "G")
	assert.True(t, m.raw.vp.AtBottom())
	for _, size := range []tea.WindowSizeMsg{{Width: 200, Height: 20}, {Width: 20, Height: 8}, {Width: 10, Height: 2}, {Width: 1, Height: 1}} {
		next, _ := m.Update(size)
		m = next.(Model)
		assert.LessOrEqual(t, displayWidth(m.View()), size.Width)
		assert.LessOrEqual(t, len(strings.Split(m.View(), "\n")), size.Height)
	}
	m = press(t, m, "g")
	assert.True(t, m.raw.vp.AtTop())
	m = press(t, m, "v")
	assert.Equal(t, modeList, m.mode)
}

func TestApp_RawResizePreservesSuspendedViewer(t *testing.T) {
	parent := codexParentSession(t, codexParentID, codexChildID, 40)
	m, _ := codexFamilyApp(t, parent)
	m = press(t, m, "enter", "]")
	before := m.viewer
	m = openRaw(t, m)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 18})
	m = next.(Model)
	assert.Equal(t, before.View(), m.viewer.View(), "raw resize suspends transcript rendering")
	m = press(t, m, "esc")
	expected := before.setSize(60, 18)
	assert.Equal(t, expected.View(), m.viewer.View())
	assert.Equal(t, before.focusedMarker, m.viewer.focusedMarker)
}

func TestApp_RawPanClampsOnShortLinesAndResize(t *testing.T) {
	m, st := newTestApp(t, Options{})
	st.sessions[0].RawJSONL = []byte(`{"wide":"` + strings.Repeat("x", 120) + `"}`)
	m = openRaw(t, m)
	m = press(t, m, "h")
	assert.Zero(t, m.raw.xOffset)
	for range 40 {
		m = press(t, m, "l")
	}
	assert.Positive(t, m.raw.xOffset)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 20})
	m = next.(Model)
	m = press(t, m, "h", "l")
	assert.Zero(t, m.raw.xOffset)
	assert.Contains(t, m.raw.vp.View(), `"wide"`)
}

func TestApp_RawLoadingResizeAndQuit(t *testing.T) {
	m, _ := newTestApp(t, Options{})
	next, cmd := m.Update(keyMsg("v"))
	m = next.(Model)
	next, _ = m.Update(tea.WindowSizeMsg{Width: 10, Height: 6})
	m = next.(Model)
	assert.LessOrEqual(t, displayWidth(m.View()), 10, "loading text fits a narrow terminal")
	next, quit := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	assert.True(t, next.(Model).quitting)
	require.NotNil(t, quit)
	assert.Equal(t, tea.Quit(), quit())
	assert.ErrorIs(t, cmd().(rawLoadedMsg).err, context.Canceled)
}
