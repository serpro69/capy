package tui

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/serpro69/capy/internal/vault"
)

func TestSessionItem_Display(t *testing.T) {
	it := sessionItem{sess: vault.Session{
		UUID: "abcdef0123", Title: "My session", ProjectPath: "/home/u/proj",
		MessageCount: 12, SizeBytes: 2048, EndTime: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
	}}
	assert.Equal(t, "My session", it.Title())
	desc := it.Description()
	assert.Contains(t, desc, "abcdef01")
	assert.Contains(t, desc, "2026-05-01")
	assert.Contains(t, desc, "12msg")
	assert.Contains(t, desc, "2.0KB")
	fv := it.FilterValue()
	assert.Contains(t, fv, "My session")
	assert.Contains(t, fv, "/home/u/proj")
}

func TestSessionItem_UntitledFallback(t *testing.T) {
	it := sessionItem{sess: vault.Session{UUID: "deadbeef00"}}
	assert.Equal(t, "(untitled)", it.Title())
}

func TestListModel_Selected(t *testing.T) {
	sessions := []vault.Session{
		{UUID: "1111aaaa", Title: "one"},
		{UUID: "2222bbbb", Title: "two"},
	}
	m := newListModel(sessions, DefaultStyles(), 80, 24)
	sel, ok := m.selected()
	require.True(t, ok)
	assert.Equal(t, "1111aaaa", sel.UUID)
}

func TestListModel_SelectedEmpty(t *testing.T) {
	m := newListModel(nil, DefaultStyles(), 80, 24)
	_, ok := m.selected()
	assert.False(t, ok)
}
