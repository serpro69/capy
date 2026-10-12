package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchStatsRejectedURLs(t *testing.T) {
	// Exercise the real SSRF transport, including its typed error through net/http.
	transport := newSSRFSafeTransport()
	transport.Proxy = nil
	old := newFetchTransport
	newFetchTransport = func() http.RoundTripper { return transport }
	t.Cleanup(func() { newFetchTransport = old; transport.CloseIdleConnections() })
	for _, batch := range []bool{false, true} {
		for _, tc := range []struct{ name, url string }{
			{name: "scheme", url: "file:///etc/passwd"},
			{name: "syntax", url: "http://[invalid"},
			{name: "missing host", url: "http:///missing"},
			{name: "opaque", url: "https:missing"},
			{name: "invalid port", url: "https://example.invalid:bad"},
			{name: "out of range port", url: "https://example.invalid:65536"},
			{name: "overflow port", url: "https://example.invalid:99999999999999999999"},
			{name: "multiple colons", url: "https://example.invalid:80:90/"},
			{name: "unbracketed IPv6", url: "https://2001:db8::1/"},
			{name: "invalid IPv6 literal", url: "https://[invalid]/"},
			{name: "mapped IPv4 loopback rejected by transport", url: "https://[::ffff:127.0.0.1]:1/"},
			{name: "IPv6 loopback", url: "http://[::1]:1/"},
			{name: "git redirect", url: "https://github.com/owner/repo/issues/1"},
			{name: "loopback", url: "http://127.0.0.1:1"},
			{name: "private", url: "http://10.0.0.1"},
			{name: "link local", url: "http://169.254.169.254"},
		} {
			t.Run(fmt.Sprintf("batch=%t/%s", batch, tc.name), func(t *testing.T) {
				srv := newTestServer(t, nil)
				r := callFetchAndIndex(t, srv, fetchArgs(tc.url, batch, true))
				assert.NotEmpty(t, resultText(r))
				snap := srv.stats.Snapshot()
				assert.Zero(t, snap.CacheHits)
				assert.Zero(t, snap.CacheMisses)
				assert.Zero(t, snap.CacheBytesSaved)
			})
		}
	}
}

func TestFetchStatsValidURLSyntax(t *testing.T) {
	for _, tc := range []struct{ name, url string }{
		{name: "IPv6", url: "https://[2001:db8::1]/reference"},
		// Mapped IPv4 is valid URL syntax; keep this admission check offline.
		{name: "mapped IPv4", url: "https://[::ffff:192.0.2.1]/"},
		{name: "IPv6 with port", url: "https://[2001:db8::1]:443/"},
		{name: "IPv6 zone", url: "http://[fe80::1%25en0]:80/"},
		{name: "maximum port", url: "https://example.invalid:65535/"},
		{name: "zero port", url: "https://example.invalid:0/"},
		{name: "leading zeros", url: "https://example.invalid:000443/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Syntax admission is separate from connect-time IP policy.
			assert.NoError(t, validateFetchScheme(tc.url))
		})
	}
}

func TestFetchStatsCacheLifecycle(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch=%t", batch), func(t *testing.T) {
			disableSSRFValidation(t)
			ts := newPlainTextServer(t, "constant cache accounting fixture")
			srv := newTestServer(t, nil)
			srv.config.Store.Cache.FetchTTLHours = 7
			srv.stats.SessionStart = time.Now().Add(-100 * time.Hour)
			var clock atomic.Int64
			clock.Store(time.Now().UnixMilli())
			srv.fetchNow = func() time.Time { return time.UnixMilli(clock.Load()) }
			for _, tc := range []struct {
				name         string
				force        bool
				ttl          any
				advance      time.Duration
				hits, misses int64
			}{
				{name: "cold", misses: 1},
				{name: "hit", hits: 1, misses: 1},
				{name: "force", force: true, hits: 1, misses: 2},
				{name: "zero ttl", ttl: 0, hits: 1, misses: 3},
				{name: "short ttl expired", ttl: 500, advance: time.Second, hits: 1, misses: 4},
				{name: "long ttl hit", ttl: 100000, hits: 2, misses: 4},
			} {
				t.Run(tc.name, func(t *testing.T) {
					clock.Add(tc.advance.Milliseconds())
					args := fetchArgs(ts.URL, batch, tc.force)
					if tc.ttl != nil {
						args["ttl"] = tc.ttl
					}
					r := callFetchAndIndex(t, srv, args)
					require.False(t, r.IsError, resultText(r))
					snap := srv.stats.Snapshot()
					assert.Equal(t, tc.hits, snap.CacheHits)
					assert.Equal(t, tc.misses, snap.CacheMisses)
				})
			}
			text := resultText(callStats(t, srv))
			assert.Contains(t, text, "| Default fetch freshness | **7h** |")
			assert.Contains(t, text, "| Hit rate (hits / (hits + attempted misses)) | **33.3%** |")
			assert.NotContains(t, text, "TTL remaining")
		})
	}
}

func TestFetchStatsConcurrentBatches(t *testing.T) {
	var attempts atomic.Int64
	old := newFetchTransport
	newFetchTransport = func() http.RoundTripper {
		return fetchRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			attempts.Add(1)
			if strings.HasSuffix(req.URL.Path, "/fail") {
				return nil, fetchTestTimeout{}
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/plain"}}, Body: io.NopCloser(strings.NewReader("fresh fixture")), Request: req}, nil
		})
	}
	t.Cleanup(func() { newFetchTransport = old })
	srv := newTestServer(t, nil)
	const cachedURL = "https://example.invalid/cached"
	_, err := srv.getStore().IndexFetched("cached fixture", composeFetchCacheKey("cached", cachedURL), "plaintext", store.KindEphemeral, time.Now())
	require.NoError(t, err)
	const batches = 8
	var wg sync.WaitGroup
	for i := range batches {
		wg.Add(1)
		go func() {
			defer wg.Done()
			requests := []fetchRequest{
				{URL: cachedURL, Source: "cached"},
				{URL: fmt.Sprintf("https://example.invalid/%d/new", i)},
				{URL: fmt.Sprintf("https://example.invalid/%d/fail", i)},
				{URL: "https://github.com/owner/repo/issues/1"},
				{URL: "file:///blocked"},
			}
			r, err := srv.handleFetchBatch(context.Background(), requests, store.KindEphemeral, false, time.Hour, 5)
			if assert.NoError(t, err) {
				assert.Contains(t, resultText(r), "Processed 2/5 URLs")
			}
			_ = srv.stats.Snapshot()
		}()
	}
	wg.Wait()
	snap := srv.stats.Snapshot()
	assert.Equal(t, int64(batches), snap.CacheHits)
	assert.Equal(t, int64(2*batches), snap.CacheMisses)
	assert.Equal(t, int64(batches*1600), snap.CacheBytesSaved)
	assert.Equal(t, snap.CacheMisses, attempts.Load())
}

func TestFetchStatsCancellationBeforeAndAfterDispatch(t *testing.T) {
	for _, batch := range []bool{false, true} {
		for _, before := range []bool{false, true} {
			t.Run(fmt.Sprintf("batch=%t/before=%t", batch, before), func(t *testing.T) {
				srv := newTestServer(t, nil)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var attempts atomic.Int64
				old := newFetchTransport
				newFetchTransport = func() http.RoundTripper {
					return fetchRoundTripFunc(func(*http.Request) (*http.Response, error) {
						attempts.Add(1)
						cancel()
						return nil, ctx.Err()
					})
				}
				t.Cleanup(func() { newFetchTransport = old })
				if before {
					cancel()
				}
				req := mcp.CallToolRequest{}
				req.Params.Arguments = fetchArgs("https://example.invalid", batch, true)
				r, err := srv.handleFetchAndIndex(ctx, req)
				require.NoError(t, err)
				assert.Contains(t, resultText(r), "context canceled")
				want := int64(1)
				if before {
					want = 0
				}
				assert.Equal(t, want, attempts.Load())
				assert.Equal(t, want, srv.stats.Snapshot().CacheMisses)
			})
		}
	}
}

func TestStatsSavingsDisplay(t *testing.T) {
	for _, tc := range []struct {
		name                string
		processed, returned int64
		want                string
	}{
		{name: "99.888 percent", processed: 100000, returned: 112, want: "99.9%"},
		{name: "rounding edge", processed: 1000000, returned: 1, want: "99.9%"},
		{name: "float precision", processed: 1 << 60, returned: 1, want: "99.9%"},
		{name: "zero returned", processed: 10000, want: "100.0%"},
		{name: "zero processed", want: "0.0%"},
		{name: "ordinary", processed: 15500, returned: 500, want: "96.8%"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(t, nil)
			srv.stats.AddBytesSandboxed(tc.processed - tc.returned)
			srv.stats.TrackResponse("capy_execute", tc.returned)
			text := resultText(callStats(t, srv))
			assert.Contains(t, text, "("+tc.want+" reduction)")
			if tc.processed > tc.returned {
				assert.Contains(t, text, "**"+tc.want+"** stayed in sandbox")
			}
		})
	}
}

func TestStatsAllMisses(t *testing.T) {
	srv := newTestServer(t, nil)
	srv.stats.recordFetchOutcome(fetchCacheMiss, 0)
	text := resultText(callStats(t, srv))
	assert.Contains(t, text, "| Cache hits | **0** |")
	assert.Contains(t, text, "| Attempted misses | **1** |")
	assert.Contains(t, text, "| Hit rate (hits / (hits + attempted misses)) | **0.0%** |")
}
