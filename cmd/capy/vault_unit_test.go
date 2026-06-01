package main

import (
	"testing"
	"time"

	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDateFlag(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		endOfDay bool
		wantErr  bool
		zero     bool
	}{
		{name: "empty is zero, no error", in: "", zero: true},
		{name: "date only", in: "2026-05-01"},
		{name: "rfc3339", in: "2026-05-01T10:30:00Z"},
		{name: "garbage", in: "not-a-date", wantErr: true},
		{name: "out of range", in: "2026-13-99", wantErr: true},
		{name: "endOfDay ignored when empty", in: "", endOfDay: true, zero: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDateFlag(tt.in, tt.endOfDay)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.zero, got.IsZero())
		})
	}
}

func TestParseDateFlag_EndOfDaySemantics(t *testing.T) {
	// A date-only --before must cover the whole target day (inclusive).
	before, err := parseDateFlag("2026-05-01", true)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 5, 1, 23, 59, 59, 0, time.UTC), before)

	// A date-only --after stays at start-of-day (already inclusive of the day).
	after, err := parseDateFlag("2026-05-01", false)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), after)

	// An explicit RFC3339 timestamp is used verbatim regardless of endOfDay.
	exact, err := parseDateFlag("2026-05-01T08:15:00Z", true)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 5, 1, 8, 15, 0, 0, time.UTC), exact)
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{name: "shorter than max", in: "abc", max: 5, want: "abc"},
		{name: "equal to max", in: "abcde", max: 5, want: "abcde"},
		{name: "longer truncates with ellipsis", in: "abcdef", max: 5, want: "abcd…"},
		{name: "multibyte counts runes", in: "héllo wörld", max: 6, want: "héllo…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, truncate(tt.in, tt.max))
		})
	}
}

func TestSubagentDisplayID(t *testing.T) {
	tests := []struct {
		name string
		rel  string
		want string
	}{
		{name: "agent jsonl", rel: "subagents/agent-abc123.jsonl", want: "abc123"},
		{name: "non-agent subagent jsonl", rel: "subagents/other.jsonl", want: "other"},
		{name: "subagent meta json not rendered", rel: "subagents/agent-x.meta.json", want: ""},
		{name: "tool result not a subagent", rel: "tool-results/t1.json", want: ""},
		{name: "loose file", rel: "notes.txt", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, subagentDisplayID(tt.rel))
		})
	}
}

func TestValidRole(t *testing.T) {
	for _, r := range []string{"user", "assistant", "tool", "system"} {
		assert.True(t, validRole(r), r)
	}
	assert.False(t, validRole("bogus"))
	assert.False(t, validRole(""))
}

func TestShortUUID(t *testing.T) {
	assert.Equal(t, "abcd1234", shortUUID("abcd1234-5678-90ab"))
	assert.Equal(t, "short", shortUUID("short"))
}

func TestDisplayPath(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	assert.Equal(t, "-", displayPath(""))
	assert.Equal(t, "~/proj/capy", displayPath("/home/tester/proj/capy"))
	assert.Equal(t, "/var/data/x", displayPath("/var/data/x"))
}

func TestOneLine(t *testing.T) {
	assert.Equal(t, "a b c", oneLine("a\nb  c"))
	assert.Equal(t, "x y", oneLine("  x\t\ny  "))
}

func TestHandleLookupError(t *testing.T) {
	t.Run("ambiguous lists candidates", func(t *testing.T) {
		amb := &vault.AmbiguousUUIDError{
			Prefix: "abcd1234",
			Candidates: []vault.Session{
				{UUID: "abcd1234-1111", Title: "one", ProjectPath: "/p", EndTime: time.Now()},
				{UUID: "abcd1234-2222", Title: "two", ProjectPath: "/p", EndTime: time.Now()},
			},
		}
		err := handleLookupError("abcd1234", amb)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ambiguous")
	})

	t.Run("not found", func(t *testing.T) {
		err := handleLookupError("zzzzzzzz", vault.ErrSessionNotFound)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no session matches")
	})

	t.Run("other error passes through", func(t *testing.T) {
		orig := assert.AnError
		err := handleLookupError("x", orig)
		assert.Equal(t, orig, err)
	})
}
