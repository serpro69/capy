package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/serpro69/capy/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newPlainTextServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func assertSourceKind(t *testing.T, srv *Server, label string, want store.SourceKind) {
	t.Helper()
	meta, err := srv.getStore().GetSourceMeta(label)
	require.NoError(t, err)
	require.NotNil(t, meta)
	assert.Equal(t, want, meta.Kind)
}

func TestFetchAndIndex_KindDefaultsToEphemeral(t *testing.T) {
	disableSSRFValidation(t)
	ts := newPlainTextServer(t, "ephemeral by default")

	srv := newTestServer(t, nil)
	r := callFetchAndIndex(t, srv, map[string]any{
		"url":    ts.URL,
		"source": "kind-default",
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "ephemeral")
	assertSourceKind(t, srv, "kind-default", store.KindEphemeral)
}

func TestFetchAndIndex_KindDurable(t *testing.T) {
	disableSSRFValidation(t)
	ts := newPlainTextServer(t, "durable content")

	srv := newTestServer(t, nil)
	r := callFetchAndIndex(t, srv, map[string]any{
		"url":    ts.URL,
		"source": "kind-durable",
		"kind":   "durable",
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "durable")
	assertSourceKind(t, srv, "kind-durable", store.KindDurable)
}

func TestFetchAndIndex_KindEphemeralExplicit(t *testing.T) {
	disableSSRFValidation(t)
	ts := newPlainTextServer(t, "explicit ephemeral")

	srv := newTestServer(t, nil)
	r := callFetchAndIndex(t, srv, map[string]any{
		"url":    ts.URL,
		"source": "kind-ephemeral",
		"kind":   "ephemeral",
	})
	assert.False(t, r.IsError)
	assertSourceKind(t, srv, "kind-ephemeral", store.KindEphemeral)
}

func TestFetchAndIndex_KindInvalidRejected(t *testing.T) {
	disableSSRFValidation(t)
	ts := newPlainTextServer(t, "should not be reached")

	srv := newTestServer(t, nil)
	r := callFetchAndIndex(t, srv, map[string]any{
		"url":  ts.URL,
		"kind": "invalid",
	})
	assert.True(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "Invalid kind")
	assert.Contains(t, text, "invalid")
}

func TestFetchAndIndex_KindSessionRejected(t *testing.T) {
	disableSSRFValidation(t)
	ts := newPlainTextServer(t, "should not be reached")

	srv := newTestServer(t, nil)
	r := callFetchAndIndex(t, srv, map[string]any{
		"url":  ts.URL,
		"kind": "session",
	})
	assert.True(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "Invalid kind")
	assert.Contains(t, text, "session")
}

func TestFetchAndIndex_CacheBypassedOnKindMismatch(t *testing.T) {
	disableSSRFValidation(t)
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "content version %d", calls)
	}))
	t.Cleanup(ts.Close)

	srv := newTestServer(t, nil)

	// First fetch: default (ephemeral)
	r := callFetchAndIndex(t, srv, map[string]any{
		"url":    ts.URL,
		"source": "kind-mismatch",
	})
	assert.False(t, r.IsError)
	assert.Equal(t, 1, calls)
	assertSourceKind(t, srv, "kind-mismatch", store.KindEphemeral)

	// Second fetch with kind=durable within cache TTL — must bypass cache and re-fetch
	r = callFetchAndIndex(t, srv, map[string]any{
		"url":    ts.URL,
		"source": "kind-mismatch",
		"kind":   "durable",
	})
	assert.False(t, r.IsError)
	assert.Equal(t, 2, calls, "cache must be bypassed when requested kind differs from cached kind")
	assert.NotContains(t, resultText(r), "Cache hit")
	assertSourceKind(t, srv, "kind-mismatch", store.KindDurable)
}

func TestFetchAndIndex_CacheBypassedOnKindMismatch_DurableToEphemeral(t *testing.T) {
	disableSSRFValidation(t)
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "content version %d", calls)
	}))
	t.Cleanup(ts.Close)

	srv := newTestServer(t, nil)

	// First fetch: durable
	r := callFetchAndIndex(t, srv, map[string]any{
		"url":    ts.URL,
		"source": "kind-mismatch-rev",
		"kind":   "durable",
	})
	require.False(t, r.IsError)
	assert.Equal(t, 1, calls)
	assertSourceKind(t, srv, "kind-mismatch-rev", store.KindDurable)

	// Second fetch: default ephemeral — must bypass cache
	r = callFetchAndIndex(t, srv, map[string]any{
		"url":    ts.URL,
		"source": "kind-mismatch-rev",
	})
	assert.False(t, r.IsError)
	assert.Equal(t, 2, calls, "cache must be bypassed when requested kind differs from cached kind")
	assertSourceKind(t, srv, "kind-mismatch-rev", store.KindEphemeral)
}

func TestFetchAndIndex_CacheHitWhenKindMatches(t *testing.T) {
	disableSSRFValidation(t)
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "content version %d", calls)
	}))
	t.Cleanup(ts.Close)

	srv := newTestServer(t, nil)

	// First fetch: durable
	callFetchAndIndex(t, srv, map[string]any{
		"url":    ts.URL,
		"source": "kind-cache-match",
		"kind":   "durable",
	})
	assert.Equal(t, 1, calls)

	// Second fetch: also durable — should hit cache
	r := callFetchAndIndex(t, srv, map[string]any{
		"url":    ts.URL,
		"source": "kind-cache-match",
		"kind":   "durable",
	})
	assert.False(t, r.IsError)
	assert.Equal(t, 1, calls, "same kind should use cache")
	assert.Contains(t, resultText(r), "Cache hit")
}

func TestFetchAndIndex_ResponseTextIncludesEphemeralHint(t *testing.T) {
	disableSSRFValidation(t)
	ts := newPlainTextServer(t, "ephemeral hint test content")

	srv := newTestServer(t, nil)
	r := callFetchAndIndex(t, srv, map[string]any{
		"url":    ts.URL,
		"source": "hint-test",
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "ephemeral")
	assert.Contains(t, text, "source:")
	assert.Contains(t, text, "include_kinds")
}

func TestFetchAndIndex_ResponseTextDurableNoEphemeralHint(t *testing.T) {
	disableSSRFValidation(t)
	ts := newPlainTextServer(t, "durable hint test content")

	srv := newTestServer(t, nil)
	r := callFetchAndIndex(t, srv, map[string]any{
		"url":    ts.URL,
		"source": "durable-hint",
		"kind":   "durable",
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "durable")
	assert.NotContains(t, text, "excluded from default search")
}
