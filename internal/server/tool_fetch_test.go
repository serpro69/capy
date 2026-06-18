package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

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

// ─── Batch fetch (requests[]) tests ────────────────────────────────────────────

// countingServer returns an httptest server serving body with the given
// content-type, and a pointer to its hit counter.
func countingServer(t *testing.T, contentType, body string) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", contentType)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(ts.Close)
	return ts, &calls
}

// TestCoerceFetchRequests pins the batch-request parsing: []any of maps, the
// double-serialized JSON-string form, and skipping entries without a url.
func TestCoerceFetchRequests(t *testing.T) {
	t.Run("array of maps", func(t *testing.T) {
		got := coerceFetchRequests([]any{
			map[string]any{"url": "https://a", "source": "doc-a"},
			map[string]any{"url": "https://b"},
		})
		require.Len(t, got, 2)
		assert.Equal(t, fetchRequest{URL: "https://a", Source: "doc-a"}, got[0])
		assert.Equal(t, fetchRequest{URL: "https://b", Source: ""}, got[1])
	})

	t.Run("double-serialized JSON string", func(t *testing.T) {
		got := coerceFetchRequests(`[{"url":"https://a","source":"doc-a"}]`)
		require.Len(t, got, 1)
		assert.Equal(t, fetchRequest{URL: "https://a", Source: "doc-a"}, got[0])
	})

	t.Run("skips entries without url", func(t *testing.T) {
		got := coerceFetchRequests([]any{
			map[string]any{"source": "no-url"},
			map[string]any{"url": "https://b"},
			"not-a-map",
		})
		require.Len(t, got, 1)
		assert.Equal(t, "https://b", got[0].URL)
	})

	t.Run("non-array yields nil", func(t *testing.T) {
		assert.Nil(t, coerceFetchRequests(42))
		assert.Nil(t, coerceFetchRequests(nil))
	})
}

// TestFetchBatch_IndexesAllURLs verifies batch mode fetches every URL and makes
// each one's content searchable by its own label.
func TestFetchBatch_IndexesAllURLs(t *testing.T) {
	disableSSRFValidation(t)
	ts1 := newPlainTextServer(t, "alpaca shearing happens once per year in spring")
	ts2 := newPlainTextServer(t, "capybara grooming is a social bonding activity")

	srv := newTestServer(t, nil)
	r := callFetchAndIndex(t, srv, map[string]any{
		"requests": []any{
			map[string]any{"url": ts1.URL, "source": "alpaca-doc"},
			map[string]any{"url": ts2.URL, "source": "capy-doc"},
		},
		"concurrency": float64(2),
		"kind":        "durable",
	})
	require.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "2/2 URLs")
	assert.Contains(t, text, "alpaca-doc")
	assert.Contains(t, text, "capy-doc")

	// Both sources are independently searchable.
	assertSourceKind(t, srv, "alpaca-doc", ts1.URL, store.KindDurable)
	assertSourceKind(t, srv, "capy-doc", ts2.URL, store.KindDurable)

	sr := callSearch(t, srv, map[string]any{"queries": []any{"alpaca shearing"}, "source": "alpaca-doc"})
	assert.Contains(t, resultText(sr), "shearing")
}

// TestFetchBatch_OrderPreserved verifies per-URL response lines follow request
// order even though fetches run concurrently.
func TestFetchBatch_OrderPreserved(t *testing.T) {
	disableSSRFValidation(t)
	ts1 := newPlainTextServer(t, "first")
	ts2 := newPlainTextServer(t, "second")
	ts3 := newPlainTextServer(t, "third")

	srv := newTestServer(t, nil)
	r := callFetchAndIndex(t, srv, map[string]any{
		"requests": []any{
			map[string]any{"url": ts1.URL, "source": "one"},
			map[string]any{"url": ts2.URL, "source": "two"},
			map[string]any{"url": ts3.URL, "source": "three"},
		},
		"concurrency": float64(3),
	})
	require.False(t, r.IsError)
	text := resultText(r)
	// Anchor to the per-URL status lines ("✓ <label> —") so the search can't match
	// an incidental substring elsewhere in the output.
	i1 := strings.Index(text, "✓ one —")
	i2 := strings.Index(text, "✓ two —")
	i3 := strings.Index(text, "✓ three —")
	require.True(t, i1 >= 0 && i2 >= 0 && i3 >= 0)
	assert.True(t, i1 < i2 && i2 < i3, "response lines must follow request order")
}

// TestFetchBatch_PartialCacheHits verifies a second batch over an already-indexed
// URL is served from cache (no re-fetch) while a new URL is fetched.
func TestFetchBatch_PartialCacheHits(t *testing.T) {
	disableSSRFValidation(t)
	ts1, calls1 := countingServer(t, "text/plain", "cached content one")
	ts2, calls2 := countingServer(t, "text/plain", "fresh content two")

	srv := newTestServer(t, nil)

	// First batch: only ts1 — indexes and caches it.
	r := callFetchAndIndex(t, srv, map[string]any{
		"requests": []any{map[string]any{"url": ts1.URL, "source": "doc-1"}},
	})
	require.False(t, r.IsError)
	assert.Equal(t, 1, *calls1)

	// Second batch: ts1 (cached) + ts2 (fresh).
	r = callFetchAndIndex(t, srv, map[string]any{
		"requests": []any{
			map[string]any{"url": ts1.URL, "source": "doc-1"},
			map[string]any{"url": ts2.URL, "source": "doc-2"},
		},
		"concurrency": float64(2),
	})
	require.False(t, r.IsError)
	text := resultText(r)
	assert.Equal(t, 1, *calls1, "cached URL must not be re-fetched")
	assert.Equal(t, 1, *calls2, "new URL must be fetched once")
	assert.Contains(t, text, "cache hit")
	assert.Contains(t, text, "2/2 URLs")
}

// TestFetchBatch_PreviewCapping verifies each per-URL preview is capped at
// fetchBatchPreviewLen so the aggregate stays small.
func TestFetchBatch_PreviewCapping(t *testing.T) {
	disableSSRFValidation(t)
	longBody := strings.Repeat("A", 500)
	ts := newPlainTextServer(t, longBody)

	srv := newTestServer(t, nil)
	r := callFetchAndIndex(t, srv, map[string]any{
		"requests": []any{map[string]any{"url": ts.URL, "source": "long-doc"}},
	})
	require.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "…", "capped preview must carry the truncation marker")
	assert.Contains(t, text, strings.Repeat("A", fetchBatchPreviewLen), "preview must include the capped prefix")
	assert.NotContains(t, text, longBody, "full body must not appear in a capped preview")
}

// TestFetchBatch_PerURLErrorIsolation verifies a bad URL (blocked scheme) is
// reported without aborting sibling fetches, and the batch is not an error result.
func TestFetchBatch_PerURLErrorIsolation(t *testing.T) {
	disableSSRFValidation(t)
	ts := newPlainTextServer(t, "good content")

	srv := newTestServer(t, nil)
	r := callFetchAndIndex(t, srv, map[string]any{
		"requests": []any{
			map[string]any{"url": "file:///etc/passwd", "source": "evil"},
			map[string]any{"url": ts.URL, "source": "good"},
		},
		"concurrency": float64(2),
	})
	require.False(t, r.IsError, "a single bad URL must not fail the whole batch")
	text := resultText(r)
	assert.Contains(t, text, "Blocked URL", "blocked scheme must be reported per URL")
	assert.Contains(t, text, "1/2 URLs", "only the good URL is indexed")
	assertSourceKind(t, srv, "good", ts.URL, store.KindEphemeral)
}

// TestFetchBatch_ConcurrencyClampedAboveCount verifies a concurrency larger than
// the request count is clamped and still indexes every URL.
func TestFetchBatch_ConcurrencyClampedAboveCount(t *testing.T) {
	disableSSRFValidation(t)
	ts1 := newPlainTextServer(t, "one")
	ts2 := newPlainTextServer(t, "two")

	srv := newTestServer(t, nil)
	r := callFetchAndIndex(t, srv, map[string]any{
		"requests": []any{
			map[string]any{"url": ts1.URL, "source": "c1"},
			map[string]any{"url": ts2.URL, "source": "c2"},
		},
		"concurrency": float64(8),
	})
	require.False(t, r.IsError)
	assert.Contains(t, resultText(r), "2/2 URLs")
}

// TestFetchBatch_GitPlatformRedirect verifies a git platform issue/PR URL in a
// batch is flagged as "not fetched" (mirroring the single-URL redirect) without
// failing the batch or blocking sibling fetches.
func TestFetchBatch_GitPlatformRedirect(t *testing.T) {
	disableSSRFValidation(t)
	ts := newPlainTextServer(t, "regular indexable content")

	srv := newTestServer(t, nil)
	r := callFetchAndIndex(t, srv, map[string]any{
		"requests": []any{
			map[string]any{"url": "https://github.com/serpro69/capy/issues/1", "source": "gh-issue"},
			map[string]any{"url": ts.URL, "source": "regular"},
		},
		"concurrency": float64(2),
	})
	require.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "not fetched (git platform")
	assert.Contains(t, text, "1/2 URLs", "the git URL is not indexed; the regular one is")
	assertSourceKind(t, srv, "regular", ts.URL, store.KindEphemeral)
}

// TestFetchAndIndex_MissingURLAndRequests verifies the handler errors when neither
// url nor requests is provided (url is no longer a hard-required schema param).
func TestFetchAndIndex_MissingURLAndRequests(t *testing.T) {
	srv := newTestServer(t, nil)
	r := callFetchAndIndex(t, srv, map[string]any{})
	assert.True(t, r.IsError)
	assert.Contains(t, resultText(r), "Missing required parameter")
}

// TestFetchBatch_InvalidRequestsRejected verifies that a `requests` array with no
// usable entries fails loud with a batch-specific error rather than the confusing
// "missing url" message.
func TestFetchBatch_InvalidRequestsRejected(t *testing.T) {
	srv := newTestServer(t, nil)
	r := callFetchAndIndex(t, srv, map[string]any{
		"requests": []any{map[string]any{"source": "no-url-here"}},
	})
	assert.True(t, r.IsError)
	assert.Contains(t, resultText(r), "Invalid requests")
}

// TestFetchBatch_TooLargeRejected verifies the batch-size cap fails loud before
// buffering an unbounded number of remote bodies (OOM guard).
func TestFetchBatch_TooLargeRejected(t *testing.T) {
	srv := newTestServer(t, nil)
	reqs := make([]any, maxFetchBatchRequests+1)
	for i := range reqs {
		reqs[i] = map[string]any{"url": fmt.Sprintf("https://example.com/%d", i)}
	}
	r := callFetchAndIndex(t, srv, map[string]any{"requests": reqs})
	assert.True(t, r.IsError)
	assert.Contains(t, resultText(r), "Batch too large")
}

// TestFetchBatch_PreviewRuneSafe verifies preview truncation never splits a
// multibyte UTF-8 rune — the whole response must remain valid UTF-8 even when the
// byte cap falls in the middle of a rune.
func TestFetchBatch_PreviewRuneSafe(t *testing.T) {
	disableSSRFValidation(t)
	// "a" + many 3-byte runes so the byte cap (fetchBatchPreviewLen) lands mid-rune.
	body := "a" + strings.Repeat("世", 300)
	require.False(t, utf8.RuneStart(body[fetchBatchPreviewLen]), "cap must fall mid-rune for this test to be meaningful")
	ts := newPlainTextServer(t, body)

	srv := newTestServer(t, nil)
	r := callFetchAndIndex(t, srv, map[string]any{
		"requests": []any{map[string]any{"url": ts.URL, "source": "utf8-doc"}},
	})
	require.False(t, r.IsError)
	text := resultText(r)
	assert.True(t, utf8.ValidString(text), "response must be valid UTF-8 (truncation must not split a rune)")
	assert.NotContains(t, text, "�", "no replacement char from a split rune")
}

// TestFetchRemoteContent_ContextCanceled verifies the fetch honors its context:
// a cancelled context aborts the in-flight request instead of running to the
// fetchTimeout ceiling. Regression guard for the context-threading fix — before
// it, fetchRemoteContent used http.NewRequest (no ctx) and ignored cancellation.
func TestFetchRemoteContent_ContextCanceled(t *testing.T) {
	disableSSRFValidation(t)
	// Handler blocks until the client gives up, so the only way the call returns
	// is via context cancellation (not a normal response).
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the request starts

	_, _, errMsg := fetchRemoteContent(ctx, ts.URL)
	require.NotEmpty(t, errMsg, "cancelled context must produce a fetch error")
	assert.Contains(t, errMsg, "context canceled")
}

// TestTruncateRunes unit-tests the rune-safe truncation helper directly.
func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		name string
		s    string
		n    int
		want string
	}{
		{name: "shorter than n", s: "hello", n: 10, want: "hello"},
		{name: "exact length", s: "hello", n: 5, want: "hello"},
		{name: "ascii cut", s: "hello world", n: 5, want: "hello"},
		{name: "cut mid 3-byte rune backtracks", s: "a世", n: 2, want: "a"}, // byte 2 is mid-世
		{name: "cut on rune boundary keeps it", s: "a世", n: 1, want: "a"},
		{name: "zero", s: "abc", n: 0, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateRunes(tt.s, tt.n)
			assert.Equal(t, tt.want, got)
			assert.True(t, utf8.ValidString(got))
		})
	}
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
