package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fetchArgs(url string, batch, force bool) map[string]any {
	args := map[string]any{"kind": "durable", "force": force}
	if batch {
		args["requests"] = []any{map[string]any{"url": url, "source": "freshness"}}
	} else {
		args["url"], args["source"] = url, "freshness"
	}
	return args
}

func TestFetchFreshnessRenewal(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch=%t", batch), func(t *testing.T) {
			disableSSRFValidation(t)
			var calls atomic.Int64
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "text/plain")
				fmt.Fprint(w, "unchanging wombat reference")
			}))
			t.Cleanup(ts.Close)
			srv := newTestServer(t, nil)
			srv.config.Store.Cache.FetchTTLHours = 1
			var clock atomic.Int64
			clock.Store(time.Now().UnixMilli())
			srv.fetchNow = func() time.Time { return time.UnixMilli(clock.Load()) }
			key := composeFetchCacheKey("freshness", ts.URL)
			// Old/generic content with a recent indexed_at is still a miss until
			// it has an explicit successful remote validation.
			_, err := srv.getStore().Index("unchanging wombat reference", key, "plaintext", store.KindDurable)
			require.NoError(t, err)
			for _, tc := range []struct {
				name      string
				advance   time.Duration
				force     bool
				wantCalls int64
				want      string
			}{
				{"legacy revalidation", 0, false, 1, "revalidated, unchanged"},
				{"hit", time.Minute, false, 1, "cache hit"},
				{"expired revalidation", time.Hour, false, 2, "revalidated, unchanged"},
				{"renewed hit", time.Minute, false, 2, "cache hit"},
				{"forced revalidation", time.Minute, true, 3, "revalidated, unchanged"},
				{"forced renewed hit", time.Minute, false, 3, "cache hit"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					clock.Add(tc.advance.Milliseconds())
					before, err := srv.getStore().GetSourceMeta(key)
					require.NoError(t, err)
					r := callFetchAndIndex(t, srv, fetchArgs(ts.URL, batch, tc.force))
					require.False(t, r.IsError, resultText(r))
					assert.Contains(t, strings.ToLower(resultText(r)), tc.want)
					assert.NotContains(t, resultText(r), "0 sections")
					assert.Equal(t, tc.wantCalls, calls.Load())
					after, err := srv.getStore().GetSourceMeta(key)
					require.NoError(t, err)
					require.NotNil(t, after.FetchValidatedAtMS)
					assert.Equal(t, before.IndexedAt, after.IndexedAt)
					if tc.want == "cache hit" {
						assert.Equal(t, before.FetchValidatedAtMS, after.FetchValidatedAtMS)
					} else {
						assert.Equal(t, clock.Load(), *after.FetchValidatedAtMS)
					}
				})
			}
			require.NoError(t, srv.getStore().Close())
			r := callFetchAndIndex(t, srv, fetchArgs(ts.URL, batch, false))
			require.False(t, r.IsError)
			assert.Contains(t, strings.ToLower(resultText(r)), "cache hit")
			assert.Equal(t, int64(3), calls.Load())
		})
	}
}

func TestFetchFreshnessFailedRevalidation(t *testing.T) {
	for _, batch := range []bool{false, true} {
		for _, failure := range []string{"http", "empty", "binary", "body too large", "read", "index", "transform"} {
			t.Run(fmt.Sprintf("batch=%t/%s", batch, failure), func(t *testing.T) {
				disableSSRFValidation(t)
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/plain")
					switch failure {
					case "http":
						w.WriteHeader(http.StatusServiceUnavailable)
					case "empty":
					case "binary":
						w.Header().Set("Content-Type", "application/pdf")
						fmt.Fprint(w, "binary")
					case "body too large":
						fmt.Fprint(w, strings.Repeat("x", fetchMaxBody+1))
					case "read":
						w.Header().Set("Content-Length", "100")
						fmt.Fprint(w, "short")
					case "index":
						fmt.Fprint(w, strings.Repeat("x", store.DefaultMaxSourceBytes+1))
					case "transform":
						w.Header().Set("Content-Type", "text/html")
						fmt.Fprint(w, strings.Repeat("<div>", 600)+"too deeply nested")
					}
				}))
				t.Cleanup(ts.Close)
				srv := newTestServer(t, nil)
				key := composeFetchCacheKey("freshness", ts.URL)
				_, err := srv.getStore().IndexFetched("retained content", key, "plaintext", store.KindDurable, time.Now().Add(-48*time.Hour))
				require.NoError(t, err)
				before, err := srv.getStore().GetSourceMeta(key)
				require.NoError(t, err)
				r := callFetchAndIndex(t, srv, fetchArgs(ts.URL, batch, true))
				if batch {
					assert.Contains(t, resultText(r), "✗")
				} else {
					assert.True(t, r.IsError)
				}
				after, err := srv.getStore().GetSourceMeta(key)
				require.NoError(t, err)
				assert.Equal(t, before, after)
			})
		}
	}
}

// A real transport with a controlled Close delay proves capture occurs after
// body admission, before response cleanup and the batch serial-index queue.
type fetchClosingTransport struct {
	base  *http.Transport
	clock *atomic.Int64
}

func (tr fetchClosingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := tr.base.RoundTrip(req)
	if err == nil {
		resp.Body = fetchClosingBody{resp.Body, tr.clock}
	}
	return resp, err
}

type fetchClosingBody struct {
	io.ReadCloser
	clock *atomic.Int64
}

func (b fetchClosingBody) Close() error {
	b.clock.Add((2 * time.Hour).Milliseconds())
	return b.ReadCloser.Close()
}

func TestFetchFreshnessCapturedBeforeQueueAndCleanup(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch=%t", batch), func(t *testing.T) {
			var clock atomic.Int64
			start := time.Now().UnixMilli()
			clock.Store(start)
			transport := &http.Transport{}
			original := newFetchTransport
			newFetchTransport = func() http.RoundTripper { return fetchClosingTransport{transport, &clock} }
			t.Cleanup(func() { newFetchTransport = original; transport.CloseIdleConnections() })
			ts := newPlainTextServer(t, "queued reference content")
			srv := newTestServer(t, nil)
			srv.fetchNow = func() time.Time { return time.UnixMilli(clock.Load()) }
			srv.config.Store.Cache.FetchTTLHours = 1
			args := fetchArgs(ts.URL, batch, true)
			if batch {
				args["requests"] = []any{
					map[string]any{"url": ts.URL, "source": "first"},
					map[string]any{"url": ts.URL, "source": "second"},
				}
				args["concurrency"] = 1
			}
			r := callFetchAndIndex(t, srv, args)
			require.False(t, r.IsError, resultText(r))
			labels := []string{"freshness"}
			if batch {
				labels = []string{"first", "second"}
			}
			for i, label := range labels {
				meta, err := srv.getStore().GetSourceMeta(composeFetchCacheKey(label, ts.URL))
				require.NoError(t, err)
				require.NotNil(t, meta)
				require.NotNil(t, meta.FetchValidatedAtMS)
				assert.Equal(t, start+int64(i)*(2*time.Hour).Milliseconds(), *meta.FetchValidatedAtMS)
				assert.False(t, fetchCacheFresh(meta, store.KindDurable, srv.fetchTime(), time.Hour), "queue/cleanup time must not restart freshness")
			}
		})
	}
}

func TestFetchFreshnessMarkerAdmission(t *testing.T) {
	now := time.UnixMilli(1700000000500)
	for _, tc := range []struct {
		name   string
		marker int64
		want   bool
	}{
		{"invalid", -1, false}, {"zero", 0, false},
		{"future", now.UnixMilli() + 1, false}, {"now", now.UnixMilli(), true},
		{"within", now.Add(-time.Hour).UnixMilli() + 1, true},
		{"expired", now.Add(-time.Hour).UnixMilli(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			meta := &store.SourceMeta{Kind: store.KindDurable, FetchValidatedAtMS: &tc.marker}
			assert.Equal(t, tc.want, fetchCacheFresh(meta, store.KindDurable, now, time.Hour))
		})
	}
	assert.False(t, fetchCacheFresh(nil, store.KindDurable, now, time.Hour))
	assert.False(t, fetchCacheFresh(&store.SourceMeta{Kind: store.KindDurable}, store.KindDurable, now, time.Hour))
	// Context cancellation never produces successful validation.
	srv := newTestServer(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, at, errMsg := srv.fetchRemoteContent(ctx, "https://example.com")
	assert.NotEmpty(t, errMsg)
	assert.True(t, at.IsZero())
}
