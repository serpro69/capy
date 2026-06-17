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

// assertSourceKind verifies the kind of the source a fetch stored. Fetched content
// is keyed by composeFetchCacheKey(label, url), so the exact-match GetSourceMeta
// lookup must use the same composite key — querying by the bare label would miss it.
func assertSourceKind(t *testing.T, srv *Server, label, url string, want store.SourceKind) {
	t.Helper()
	meta, err := srv.getStore().GetSourceMeta(composeFetchCacheKey(label, url))
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
	assertSourceKind(t, srv, "kind-default", ts.URL, store.KindEphemeral)
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
	assertSourceKind(t, srv, "kind-durable", ts.URL, store.KindDurable)
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
	assertSourceKind(t, srv, "kind-ephemeral", ts.URL, store.KindEphemeral)
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
	assertSourceKind(t, srv, "kind-mismatch", ts.URL, store.KindEphemeral)

	// Second fetch with kind=durable within cache TTL — must bypass cache and re-fetch
	r = callFetchAndIndex(t, srv, map[string]any{
		"url":    ts.URL,
		"source": "kind-mismatch",
		"kind":   "durable",
	})
	assert.False(t, r.IsError)
	assert.Equal(t, 2, calls, "cache must be bypassed when requested kind differs from cached kind")
	assert.NotContains(t, resultText(r), "Cache hit")
	assertSourceKind(t, srv, "kind-mismatch", ts.URL, store.KindDurable)
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
	assertSourceKind(t, srv, "kind-mismatch-rev", ts.URL, store.KindDurable)

	// Second fetch: default ephemeral — must bypass cache
	r = callFetchAndIndex(t, srv, map[string]any{
		"url":    ts.URL,
		"source": "kind-mismatch-rev",
	})
	assert.False(t, r.IsError)
	assert.Equal(t, 2, calls, "cache must be bypassed when requested kind differs from cached kind")
	assertSourceKind(t, srv, "kind-mismatch-rev", ts.URL, store.KindEphemeral)
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

// TestComposeFetchCacheKey pins the composite-key format. The pipe separator and
// label|url ordering are a cache-compatibility contract — changing them silently
// invalidates every previously cached fetch entry.
func TestComposeFetchCacheKey(t *testing.T) {
	tests := []struct {
		name  string
		label string
		url   string
		want  string
	}{
		{name: "explicit label", label: "my-docs", url: "https://example.com/a", want: "my-docs|https://example.com/a"},
		{name: "url-defaulted label", label: "https://example.com/a", url: "https://example.com/a", want: "https://example.com/a|https://example.com/a"},
		{name: "same label distinct url", label: "shared", url: "https://example.com/b", want: "shared|https://example.com/b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, composeFetchCacheKey(tt.label, tt.url))
		})
	}
}

// TestFetchAndIndex_CacheKeyIncludesURL verifies that two distinct URLs sharing the
// same explicit source label get separate cache entries — the URL is part of the
// cache key, so the first URL's cached response must not mask the second's.
func TestFetchAndIndex_CacheKeyIncludesURL(t *testing.T) {
	disableSSRFValidation(t)

	calls1, calls2 := 0, 0
	ts1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls1++
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "content from url one")
	}))
	t.Cleanup(ts1.Close)
	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls2++
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "content from url two")
	}))
	t.Cleanup(ts2.Close)

	srv := newTestServer(t, nil)

	// First URL under the shared label.
	r := callFetchAndIndex(t, srv, map[string]any{"url": ts1.URL, "source": "shared"})
	require.False(t, r.IsError)
	assert.Equal(t, 1, calls1)

	// Second URL under the SAME label — must not be served from the first URL's
	// cache. The handler should actually fetch ts2 and index its distinct content.
	r = callFetchAndIndex(t, srv, map[string]any{"url": ts2.URL, "source": "shared"})
	require.False(t, r.IsError)
	assert.Equal(t, 1, calls2, "second URL with shared label must not hit the first URL's cache")
	assert.NotContains(t, resultText(r), "Cache hit")
	assert.Contains(t, resultText(r), "content from url two")

	// Each URL retains its own cache entry, keyed by composeFetchCacheKey(label, url).
	assertSourceKind(t, srv, "shared", ts1.URL, store.KindEphemeral)
	assertSourceKind(t, srv, "shared", ts2.URL, store.KindEphemeral)

	// Re-fetching the first URL with the shared label now hits its own entry.
	r = callFetchAndIndex(t, srv, map[string]any{"url": ts1.URL, "source": "shared"})
	require.False(t, r.IsError)
	assert.Equal(t, 1, calls1, "re-fetch of first URL must be served from its own cache entry")
	assert.Contains(t, resultText(r), "Cache hit")
}

// TestFetchAndIndex_CompositeKeySearchableByLabel verifies that a source stored under
// the composite cache key ("label|url") is still findable by its friendly label,
// because the search source filter is LIKE-based (substring match).
func TestFetchAndIndex_CompositeKeySearchableByLabel(t *testing.T) {
	disableSSRFValidation(t)
	ts := newPlainTextServer(t, "alpaca husbandry covers feeding, shearing, and pasture rotation")

	srv := newTestServer(t, nil)
	r := callFetchAndIndex(t, srv, map[string]any{
		"url":    ts.URL,
		"source": "alpaca-guide",
		"kind":   "durable",
	})
	require.False(t, r.IsError)

	// Search scoped by the friendly label, not the composite key.
	sr := callSearch(t, srv, map[string]any{
		"queries": []any{"alpaca shearing"},
		"source":  "alpaca-guide",
	})
	assert.False(t, sr.IsError)
	text := resultText(sr)
	assert.Contains(t, text, "alpaca-guide", "LIKE source filter must match the friendly label substring of the composite key")
	assert.Contains(t, text, "husbandry", "the indexed content must actually be retrieved")
}
