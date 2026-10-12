package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fetchRoundTripFunc func(*http.Request) (*http.Response, error)

func (f fetchRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type fetchTestTimeout struct{}

func (fetchTestTimeout) Error() string   { return "synthetic network timeout" }
func (fetchTestTimeout) Timeout() bool   { return true }
func (fetchTestTimeout) Temporary() bool { return true }

type fetchErrorBody struct{ err error }

func (b fetchErrorBody) Read([]byte) (int, error) { return 0, b.err }
func (b fetchErrorBody) Close() error             { return nil }

func TestFetchRetryGuidance(t *testing.T) {
	blocked := classifyIP("127.0.0.1")
	require.Error(t, blocked)
	for _, batch := range []bool{false, true} {
		for _, tc := range []struct {
			name     string
			err      error
			wantHint bool
		}{
			{name: "temporary dns", err: &net.DNSError{Err: "temporary DNS", IsTemporary: true}, wantHint: true},
			{name: "timeout dns", err: fmt.Errorf("lookup: %w", &net.DNSError{Err: "timeout DNS", IsTimeout: true}), wantHint: true},
			{name: "network timeout", err: &net.OpError{Op: "dial", Net: "tcp", Err: fetchTestTimeout{}}, wantHint: true},
			{name: "wrapped timeout", err: fmt.Errorf("transport: %w", fetchTestTimeout{}), wantHint: true},
			{name: "client deadline", err: context.DeadlineExceeded, wantHint: true},
			{name: "network unreachable", err: &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ENETUNREACH}}, wantHint: true},
			{name: "host unreachable", err: fmt.Errorf("connect: %w", syscall.EHOSTUNREACH), wantHint: true},
			{name: "canceled", err: fmt.Errorf("request: %w", context.Canceled)},
			{name: "ssrf", err: &fetchPolicyError{cause: blocked}},
			{name: "nxdomain", err: &net.DNSError{Err: "no such host", IsNotFound: true}},
			{name: "nxdomain precedence", err: &net.DNSError{Err: "no such host", IsNotFound: true, IsTimeout: true, IsTemporary: true}},
			{name: "permanent dns", err: &net.DNSError{Err: "permanent DNS failure"}},
			{name: "certificate", err: x509.UnknownAuthorityError{Cert: &x509.Certificate{}}},
			{name: "tls", err: tls.RecordHeaderError{Msg: "invalid TLS record"}},
			{name: "permission", err: &os.SyscallError{Syscall: "connect", Err: syscall.EACCES}},
			{name: "eperm", err: fmt.Errorf("dial: %w", syscall.EPERM)},
			{name: "refused", err: syscall.ECONNREFUSED},
			{name: "error words", err: errors.New("timeout getaddrinfo EPERM network unreachable")},
		} {
			t.Run(fmt.Sprintf("batch=%t/%s", batch, tc.name), func(t *testing.T) {
				var attempts atomic.Int64
				original := newFetchTransport
				newFetchTransport = func() http.RoundTripper {
					return fetchRoundTripFunc(func(*http.Request) (*http.Response, error) {
						attempts.Add(1)
						return nil, tc.err
					})
				}
				t.Cleanup(func() { newFetchTransport = original })
				srv := newTestServer(t, nil)
				args := fetchArgs("https://example.invalid/reference", batch, true)
				args["ttl"] = 0
				count := int64(1)
				if batch {
					args["requests"] = []any{
						map[string]any{"url": "https://example.invalid/one"},
						map[string]any{"url": "https://example.invalid/two"},
					}
					args["concurrency"], count = 2, 2
				}
				r := callFetchAndIndex(t, srv, args)
				text := resultText(r)
				assert.Contains(t, text, tc.err.Error(), "retain the cause")
				if tc.wantHint {
					assert.Equal(t, int(count), strings.Count(text, fetchRetryHint))
				} else {
					assert.NotContains(t, text, fetchRetryHint)
				}
				assert.Equal(t, count, attempts.Load(), "no automatic retry")
				wantMisses := count
				if tc.name == "ssrf" {
					wantMisses = 0
				}
				snap := srv.stats.Snapshot()
				assert.Equal(t, wantMisses, snap.CacheMisses)
				assert.Zero(t, snap.CacheHits)
				assert.NoFileExists(t, srv.config.Store.Path)
			})
		}
	}
	require.Less(t, len(fetchRetryHint), 160, "fixed retry advice stays bounded")
}

func TestFetchRetryHintExcludesHTTPAndBody(t *testing.T) {
	for _, batch := range []bool{false, true} {
		for _, tc := range []struct {
			name   string
			status int
			body   func() io.ReadCloser
		}{
			{name: "http unavailable", status: http.StatusServiceUnavailable, body: func() io.ReadCloser { return io.NopCloser(strings.NewReader("retry later")) }},
			{name: "body timeout", status: http.StatusOK, body: func() io.ReadCloser { return fetchErrorBody{err: fetchTestTimeout{}} }},
			{name: "body unreachable", status: http.StatusOK, body: func() io.ReadCloser { return fetchErrorBody{err: syscall.ENETUNREACH} }},
		} {
			t.Run(fmt.Sprintf("batch=%t/%s", batch, tc.name), func(t *testing.T) {
				original := newFetchTransport
				newFetchTransport = func() http.RoundTripper {
					return fetchRoundTripFunc(func(req *http.Request) (*http.Response, error) {
						return &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: tc.body(), Request: req}, nil
					})
				}
				t.Cleanup(func() { newFetchTransport = original })
				srv := newTestServer(t, nil)
				r := callFetchAndIndex(t, srv, fetchArgs("https://example.invalid", batch, true))
				assert.Contains(t, resultText(r), "Failed")
				assert.NotContains(t, resultText(r), fetchRetryHint)
				assert.Equal(t, int64(1), srv.stats.Snapshot().CacheMisses)
			})
		}
	}
}

func TestFetchRetryHintExcludesParentCancellation(t *testing.T) {
	for _, batch := range []bool{false, true} {
		for _, deadline := range []bool{false, true} {
			t.Run(fmt.Sprintf("batch=%t/deadline=%t", batch, deadline), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				if deadline {
					cancel()
					ctx, cancel = context.WithDeadline(context.Background(), time.Unix(1, 0))
				} else {
					cancel()
				}
				defer cancel()
				original := newFetchTransport
				newFetchTransport = func() http.RoundTripper {
					return fetchRoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, fetchTestTimeout{} })
				}
				t.Cleanup(func() { newFetchTransport = original })
				srv := newTestServer(t, nil)
				req := mcp.CallToolRequest{}
				req.Params.Arguments = fetchArgs("https://example.invalid", batch, true)
				r, err := srv.handleFetchAndIndex(ctx, req)
				require.NoError(t, err)
				assert.NotContains(t, resultText(r), fetchRetryHint)
				assert.Zero(t, srv.stats.Snapshot().CacheMisses)
			})
		}
	}
}
