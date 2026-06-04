package tui

import (
	"errors"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/serpro69/capy/internal/vault"
)

func sampleResults() []vault.SearchResult {
	return []vault.SearchResult{
		{SessionUUID: "aaaa1111", Role: "assistant", LineIndex: 4, Snippet: "[match] here", EndTime: time.Now()},
		{SessionUUID: "bbbb2222", SubagentID: "xyz", Role: "tool", LineIndex: 7, Snippet: "sub [hit]"},
	}
}

func TestSearch_ResultsPopulateAndCursorNav(t *testing.T) {
	st := &stubStore{results: sampleResults()}
	m := newSearchModel(st, DefaultStyles(), 80, 24)

	m, _ = m.Update(searchResultsMsg{seq: m.seq, results: st.results})
	require.Len(t, m.results, 2)
	assert.Equal(t, 0, m.cursor)

	r, ok := m.selected()
	require.True(t, ok)
	assert.Equal(t, "aaaa1111", r.SessionUUID)

	m, _ = m.Update(key("down"))
	r, _ = m.selected()
	assert.Equal(t, "bbbb2222", r.SessionUUID)
	assert.Equal(t, "xyz", r.SubagentID)

	// Cursor clamps at the ends.
	m, _ = m.Update(key("down"))
	assert.Equal(t, 1, m.cursor)
	m, _ = m.Update(key("up"))
	m, _ = m.Update(key("up"))
	assert.Equal(t, 0, m.cursor)
}

func TestSearch_StaleResultsIgnored(t *testing.T) {
	st := &stubStore{results: sampleResults()}
	m := newSearchModel(st, DefaultStyles(), 80, 24)
	m.seq = 5

	// A results message from an earlier sequence is dropped.
	m, _ = m.Update(searchResultsMsg{seq: 4, results: sampleResults()})
	assert.Empty(t, m.results)
}

func TestSearch_DebounceFiresLatestOnly(t *testing.T) {
	st := &stubStore{}
	m := newSearchModel(st, DefaultStyles(), 80, 24)
	m.input.SetValue("query")
	m.seq = 3

	// A debounce tick for a superseded sequence does nothing.
	_, cmd := m.Update(debounceMsg{seq: 2})
	assert.Nil(t, cmd)

	// A debounce tick for the current sequence fires the query command.
	_, cmd = m.Update(debounceMsg{seq: 3})
	require.NotNil(t, cmd)
	msg := cmd()
	res, ok := msg.(searchResultsMsg)
	require.True(t, ok)
	assert.Equal(t, 3, res.seq)
	assert.Equal(t, 1, st.searchCalls)
	assert.Equal(t, "query", st.lastQuery)
}

func TestSearch_TypingReschedules(t *testing.T) {
	st := &stubStore{}
	m := newSearchModel(st, DefaultStyles(), 80, 24)
	before := m.seq
	m, cmd := m.Update(key("a"))
	assert.Equal(t, "a", m.input.Value())
	assert.Greater(t, m.seq, before, "a value change bumps the debounce sequence")
	require.NotNil(t, cmd)
}

func TestSearch_ErrorSurfaced(t *testing.T) {
	st := &stubStore{searchErr: errors.New("boom")}
	m := newSearchModel(st, DefaultStyles(), 80, 24)
	m, _ = m.Update(searchResultsMsg{seq: m.seq, err: st.searchErr})
	assert.Contains(t, m.status, "boom")
	assert.Empty(t, m.results)
}

func TestSearch_SetQueryReturnsCommand(t *testing.T) {
	st := &stubStore{}
	m := newSearchModel(st, DefaultStyles(), 80, 24)
	m, cmd := m.setQuery("hello")
	assert.Equal(t, "hello", m.input.Value())
	// A debounced tick is scheduled (not executed here — tea.Tick sleeps; the
	// debounce gating itself is covered by TestSearch_DebounceFiresLatestOnly).
	require.NotNil(t, cmd)
}

func TestSearch_View(t *testing.T) {
	st := &stubStore{results: sampleResults()}
	m := newSearchModel(st, DefaultStyles(), 80, 24)

	// Empty input → prompt help.
	assert.Contains(t, m.View(), "type to search")

	// With results → rows visible (including the subagent flag).
	m, _ = m.Update(searchResultsMsg{seq: m.seq, results: st.results})
	out := m.View()
	assert.Contains(t, out, "[match] here")
	assert.Contains(t, out, "/sub", "a subagent hit is flagged in the row")

	// Error state.
	m.status = "search error: boom"
	assert.Contains(t, m.View(), "boom")

	// Non-empty query, no results.
	m2 := newSearchModel(st, DefaultStyles(), 80, 24)
	m2.input.SetValue("zzz")
	assert.Contains(t, m2.View(), "no matches")
}

var _ tea.Model = Model{} // app.Model satisfies tea.Model
