package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateFetchScheme(t *testing.T) {
	t.Run("allowed schemes", func(t *testing.T) {
		for _, u := range []string{
			"http://example.com",
			"https://example.com/path?q=1",
			"HTTP://example.com", // url.Parse lowercases the scheme
		} {
			assert.NoError(t, validateFetchScheme(u), u)
		}
	})

	t.Run("blocked schemes", func(t *testing.T) {
		for _, u := range []string{
			"file:///etc/passwd",
			"gopher://evil.example/x",
			"data:text/html,<script>alert(1)</script>",
			"javascript:alert(1)",
			"ftp://example.com/file",
			"example.com/path", // empty scheme
		} {
			err := validateFetchScheme(u)
			assert.Error(t, err, u)
		}
	})

	t.Run("malformed URL returns error", func(t *testing.T) {
		assert.Error(t, validateFetchScheme("://not-a-url"))
	})
}

func TestClassifyIP_Blocked(t *testing.T) {
	// Each entry maps an IP to a substring its error message must contain.
	cases := map[string]string{
		// IPv4 hard-block ranges
		"0.0.0.0":         "current-network",
		"0.1.2.3":         "current-network",
		"127.0.0.1":       "loopback",
		"169.254.169.254": "link-local", // AWS/GCP IMDS
		"224.0.0.1":       "multicast",
		"240.0.0.1":       "reserved",
		// IPv4 private (RFC1918)
		"10.0.0.1":       "private",
		"172.16.0.1":     "private",
		"172.31.255.255": "private",
		"192.168.1.1":    "private",
		// IPv6
		"::":      "unspecified",
		"::1":     "loopback",
		"fe80::1": "link-local",
		"ff02::1": "multicast",
		"fc00::1": "private",
		// Zone-ID stripping (RFC 6874) — must classify after stripping %eth0
		"fe80::1%eth0":   "link-local",
		"fe80::1%25eth0": "link-local", // URL-encoded zone
		// IPv4-mapped IPv6 — must unwrap and classify as IPv4
		"::ffff:127.0.0.1": "loopback",
		"::ffff:10.0.0.1":  "private",
		// IPv4-compatible IPv6 (::a.b.c.d) — To4 does NOT unwrap these; embedded
		// IPv4 must still be classified.
		"::127.0.0.1": "loopback",
		"::10.0.0.1":  "private",
		// NAT64 (64:ff9b::/96) — embedded IPv4 routed through a translator.
		"64:ff9b::169.254.169.254": "link-local", // IMDS via NAT64
		"64:ff9b::10.0.0.1":        "private",
		// Malformed / non-IP — fail closed
		"not-an-ip": "malformed",
		"":          "malformed",
		"999.1.1.1": "malformed",
	}

	for ip, want := range cases {
		ip, want := ip, want
		t.Run(ip, func(t *testing.T) {
			err := classifyIP(ip)
			require.Error(t, err, "expected %q to be blocked", ip)
			assert.Contains(t, err.Error(), want, "ip %q", ip)
		})
	}
}

func TestClassifyIP_AllowedPublic(t *testing.T) {
	for _, ip := range []string{
		"8.8.8.8",
		"1.1.1.1",
		"93.184.216.34",                      // example.com (IPv4)
		"2606:2800:220:1:248:1893:25c8:1946", // example.com (IPv6)
		"2001:4860:4860::8888",               // Google DNS (IPv6)
		"64:ff9b::8.8.8.8",                   // NAT64 to a public IPv4 — must stay allowed
	} {
		ip := ip
		t.Run(ip, func(t *testing.T) {
			assert.NoError(t, classifyIP(ip), ip)
		})
	}
}

// TestSSRFTransport_BlocksLoopbackDial exercises the connect-time classification
// directly: the transport resolves the host, classifies the IP, and refuses to
// dial when the resolved address is forbidden — the core DNS-rebinding defense.
func TestSSRFTransport_BlocksLoopbackDial(t *testing.T) {
	tr := newSSRFSafeTransport()
	require.NotNil(t, tr.DialContext)

	_, err := tr.DialContext(context.Background(), "tcp", "127.0.0.1:80")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loopback")
}

func TestSSRFTransport_BlocksLinkLocalDial(t *testing.T) {
	tr := newSSRFSafeTransport()
	_, err := tr.DialContext(context.Background(), "tcp", "169.254.169.254:80")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "link-local")
}

// TestFetchAndIndex_SSRFTransportBlocksLoopback verifies the wiring end-to-end:
// with the real (non-disabled) transport, a loopback httptest URL is rejected at
// connect time rather than fetched.
func TestFetchAndIndex_SSRFTransportBlocksLoopback(t *testing.T) {
	ts := newPlainTextServer(t, "secret internal data")

	srv := newTestServer(t, nil)
	r := callFetchAndIndex(t, srv, map[string]any{
		"url":    ts.URL,
		"source": "ssrf-block",
	})
	assert.True(t, r.IsError)
	assert.Contains(t, resultText(r), "Failed to fetch")
}
