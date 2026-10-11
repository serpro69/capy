package server

import (
	"database/sql"
	"fmt"
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

func TestFetchTTLInvalidBeforeSideEffects(t *testing.T) {
	var transports atomic.Int64
	old := newFetchTransport
	newFetchTransport = func() http.RoundTripper {
		transports.Add(1)
		return &http.Transport{}
	}
	t.Cleanup(func() { newFetchTransport = old })
	for _, batch := range []bool{false, true} {
		for _, tc := range invalidFetchTTLs() {
			t.Run(fmt.Sprintf("batch=%t/%s", batch, tc.name), func(t *testing.T) {
				srv := newTestServer(t, nil)
				args := fetchArgs("http://127.0.0.1:1/never", batch, true)
				args["ttl"] = tc.value
				r := callFetchAndIndex(t, srv, args)
				require.True(t, r.IsError)
				assert.Contains(t, resultText(r), `invalid parameter "ttl"`)
				assert.Nil(t, srv.store)
				assert.NoFileExists(t, srv.config.Store.Path)
			})
		}
	}
	assert.Zero(t, transports.Load())
}

func TestFetchTTLSubsecondAndOverrides(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch=%t", batch), func(t *testing.T) {
			disableSSRFValidation(t)
			var calls atomic.Int64
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "text/plain")
				fmt.Fprint(w, "constant millisecond reference")
			}))
			t.Cleanup(ts.Close)
			srv := newTestServer(t, nil)
			srv.config.Store.Cache.FetchTTLHours = 7
			start := time.UnixMilli(1700000000750) // expiry crosses a second boundary
			var clock atomic.Int64
			clock.Store(start.UnixMilli())
			srv.fetchNow = func() time.Time { return time.UnixMilli(clock.Load()) }
			key := composeFetchCacheKey("freshness", ts.URL)
			_, err := srv.getStore().IndexFetched("constant millisecond reference", key, "plaintext", store.KindDurable, start)
			require.NoError(t, err)
			for _, tc := range []struct {
				name      string
				elapsed   time.Duration
				ttl       any
				force     bool
				wantCalls int64
				want      string
			}{
				{name: "499ms hit", elapsed: 499 * time.Millisecond, ttl: 500, want: "cache hit"},
				{name: "500ms expires", elapsed: 500 * time.Millisecond, ttl: "500", wantCalls: 1, want: "revalidated, unchanged"},
				{name: "renewed hit", elapsed: 999 * time.Millisecond, ttl: 500, wantCalls: 1, want: "cache hit"},
				{name: "strict renewed expiry", elapsed: time.Second, ttl: 500, wantCalls: 2, want: "revalidated, unchanged"},
				{name: "zero bypass", elapsed: time.Second, ttl: 0, wantCalls: 3, want: "revalidated, unchanged"},
				{name: "force bypass", elapsed: time.Second, ttl: 500, force: true, wantCalls: 4, want: "revalidated, unchanged"},
				{name: "larger request ttl", elapsed: 2 * time.Second, ttl: 5000, wantCalls: 4, want: "cache hit"},
				{name: "configured fallback", elapsed: 2 * time.Hour, wantCalls: 4, want: "cache hit"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					clock.Store(start.Add(tc.elapsed).UnixMilli())
					args := fetchArgs(ts.URL, batch, tc.force)
					if tc.ttl != nil {
						args["ttl"] = tc.ttl
					}
					r := callFetchAndIndex(t, srv, args)
					require.False(t, r.IsError, resultText(r))
					text := strings.ToLower(resultText(r))
					assert.Contains(t, text, tc.want)
					assert.Contains(t, text, "retention: durable scoring")
					assert.NotContains(t, text, "0 sections")
					assert.Equal(t, tc.wantCalls, calls.Load())
					if tc.ttl == nil {
						assert.Contains(t, text, "freshness: 25200000 ms")
					}
					if tc.force {
						assert.Contains(t, text, "cache bypassed by force: true")
					}
				})
			}
			// Neither an explicit TTL nor a cache hit is persisted as a new clock.
			require.NoError(t, srv.getStore().Close())
			meta, err := srv.getStore().GetSourceMeta(key)
			require.NoError(t, err)
			require.NotNil(t, meta.FetchValidatedAtMS)
			assert.Equal(t, start.Add(time.Second).UnixMilli(), *meta.FetchValidatedAtMS)
			r := callFetchAndIndex(t, srv, fetchArgs(ts.URL, batch, false))
			assert.Contains(t, strings.ToLower(resultText(r)), "cache hit")
			assert.Equal(t, int64(4), calls.Load())
		})
	}
}

func TestFetchTTLInvalidMarkersAndKind(t *testing.T) {
	for _, batch := range []bool{false, true} {
		for _, marker := range []string{"null", "future", "invalid", "kind mismatch"} {
			t.Run(fmt.Sprintf("batch=%t/%s", batch, marker), func(t *testing.T) {
				disableSSRFValidation(t)
				ts := newPlainTextServer(t, "marker reference")
				srv := newTestServer(t, nil)
				now := time.UnixMilli(1700000000750)
				srv.fetchNow = func() time.Time { return now }
				key := composeFetchCacheKey("freshness", ts.URL)
				switch marker {
				case "null":
					_, err := srv.getStore().Index("marker reference", key, "plaintext", store.KindDurable)
					require.NoError(t, err)
				case "invalid":
					_, err := srv.getStore().IndexFetched("marker reference", key, "plaintext", store.KindDurable, now)
					require.NoError(t, err)
					db, err := sql.Open("sqlite3", store.EncryptedDSN(srv.config.Store.Path, store.EncryptionKeyFromEnv()))
					require.NoError(t, err)
					_, err = db.Exec(`UPDATE sources SET fetch_validated_at_ms = 'invalid' WHERE label = ?`, key)
					require.NoError(t, err)
					require.NoError(t, db.Close())
				case "future":
					_, err := srv.getStore().IndexFetched("marker reference", key, "plaintext", store.KindDurable, now.Add(time.Millisecond))
					require.NoError(t, err)
				case "kind mismatch":
					_, err := srv.getStore().IndexFetched("marker reference", key, "plaintext", store.KindEphemeral, now)
					require.NoError(t, err)
				}
				args := fetchArgs(ts.URL, batch, false)
				args["ttl"] = 500
				r := callFetchAndIndex(t, srv, args)
				require.False(t, r.IsError, resultText(r))
				assert.Contains(t, strings.ToLower(resultText(r)), "revalidated, unchanged")
				assert.Contains(t, strings.ToLower(resultText(callFetchAndIndex(t, srv, args))), "cache hit")
			})
		}
	}
}

func TestFetchTTLSeparateFromRetention(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch=%t", batch), func(t *testing.T) {
			disableSSRFValidation(t)
			ts := newPlainTextServer(t, "ephemeral reference")
			srv := newTestServer(t, nil)
			srv.config.Store.Cleanup.EphemeralTTLHours = 72
			key := composeFetchCacheKey("freshness", ts.URL)
			_, err := srv.getStore().IndexFetched("ephemeral reference", key, "plaintext", store.KindEphemeral, time.Now())
			require.NoError(t, err)
			db, err := sql.Open("sqlite3", store.EncryptedDSN(srv.config.Store.Path, store.EncryptionKeyFromEnv()))
			require.NoError(t, err)
			_, err = db.Exec(`UPDATE sources SET indexed_at = '2001-01-01 00:00:00' WHERE label = ?`, key)
			require.NoError(t, err)
			require.NoError(t, db.Close())
			args := fetchArgs(ts.URL, batch, true)
			args["kind"], args["ttl"] = "ephemeral", 500
			r := callFetchAndIndex(t, srv, args)
			require.False(t, r.IsError)
			assert.Contains(t, resultText(r), "Fetch freshness: 500 ms")
			assert.Contains(t, resultText(r), "Retention: 72h0m0s from indexing")
			assert.Contains(t, resultText(r), "unchanged revalidation does not extend it")
			evicted, err := srv.getStore().Cleanup(false, srv.ephemeralTTL(), 0)
			require.NoError(t, err)
			require.Len(t, evicted, 1)
		})
	}
}
