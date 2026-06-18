package server

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// SSRF guard. Two complementary layers protect capy_fetch_and_index:
//
//  1. validateFetchScheme rejects non-http(s) URLs cheaply at the boundary
//     (file://, gopher://, data:, javascript:, empty scheme).
//  2. newSSRFSafeTransport classifies every resolved IP at connect time and
//     dials the validated IP directly. Resolution and validation happen in the
//     same step as the dial, so there is no DNS-rebinding TOCTOU window between
//     a pre-flight check and the connection (and each redirect to a new host is
//     re-validated because it triggers a fresh DialContext).
//
// capy blocks both upstream's "hard-block" categories (unspecified, link-local,
// multicast, reserved) and "private" categories (loopback, RFC1918, ULA) — it is
// stricter than upstream by default, with no opt-out toggle.

// validateFetchScheme rejects any URL whose scheme is not http or https.
func validateFetchScheme(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	switch parsed.Scheme {
	case "http", "https":
		return nil
	default:
		return fmt.Errorf("scheme %q is forbidden (only http and https are allowed)", parsed.Scheme)
	}
}

// classifyIP returns an error if rawIP belongs to a forbidden range. It accepts
// the raw string (not a net.IP) so it can strip RFC 6874 zone identifiers before
// classification. Malformed input fails closed (blocked).
func classifyIP(rawIP string) error {
	// Strip RFC 6874 zone identifier (fe80::1%eth0, URL-encoded fe80::1%25eth0)
	// before parsing — net.ParseIP rejects any address that carries a zone.
	s := rawIP
	if i := strings.IndexByte(s, '%'); i >= 0 {
		s = s[:i]
	}

	ip := net.ParseIP(s)
	if ip == nil {
		return fmt.Errorf("malformed IP address %q is forbidden", rawIP)
	}
	return classifyParsedIP(ip)
}

// classifyParsedIP classifies an already-parsed net.IP. The DialContext path
// calls it directly to avoid re-stringifying and re-parsing resolved addresses.
func classifyParsedIP(ip net.IP) error {
	// IPv4 and IPv4-mapped IPv6 (::ffff:a.b.c.d): To4 yields the 4-byte form.
	if v4 := ip.To4(); v4 != nil {
		return classifyIPv4(v4)
	}

	// Standard IPv6 categories first so ::, ::1, fe80::, ff00::, fc00:: get
	// their precise classification.
	if err := classifyIPv6(ip); err != nil {
		return err
	}

	// Then catch IPv6 forms that embed an IPv4 address but are NOT unwrapped by
	// To4: NAT64 (64:ff9b::/96, routed through a translator) and deprecated
	// IPv4-compatible (::a.b.c.d). Without this, e.g. 64:ff9b::169.254.169.254
	// (IMDS) or ::127.0.0.1 (loopback) would slip past every net.IP.Is* check.
	// :: and ::1 are already handled above, so only real embedded addresses
	// reach here; NAT64-to-public (64:ff9b::8.8.8.8) stays allowed.
	if v4 := embeddedIPv4(ip); v4 != nil {
		return classifyIPv4(v4)
	}
	return nil
}

// classifyIPv4 blocks current-network, loopback, link-local (incl. IMDS),
// RFC1918 private, multicast, and reserved IPv4 ranges.
func classifyIPv4(ip net.IP) error {
	switch {
	case ip[0] == 0: // 0.0.0.0/8 "this network"
		return fmt.Errorf("current-network address %s is forbidden", ip)
	case ip.IsLoopback(): // 127.0.0.0/8
		return fmt.Errorf("loopback address %s is forbidden", ip)
	case ip.IsLinkLocalUnicast(): // 169.254.0.0/16, includes IMDS 169.254.169.254
		return fmt.Errorf("link-local address %s is forbidden", ip)
	case ip.IsPrivate(): // 10/8, 172.16/12, 192.168/16
		return fmt.Errorf("private network address %s is forbidden", ip)
	case ip.IsMulticast(): // 224.0.0.0/4
		return fmt.Errorf("multicast address %s is forbidden", ip)
	case ip[0] >= 240: // 240.0.0.0/4 reserved, incl. 255.255.255.255 broadcast
		return fmt.Errorf("reserved address %s is forbidden", ip)
	}
	return nil
}

// classifyIPv6 blocks unspecified, loopback, link-local, multicast, and ULA
// (private) IPv6 ranges.
func classifyIPv6(ip net.IP) error {
	switch {
	case ip.IsUnspecified(): // ::
		return fmt.Errorf("unspecified address %s is forbidden", ip)
	case ip.IsLoopback(): // ::1
		return fmt.Errorf("loopback address %s is forbidden", ip)
	case ip.IsLinkLocalUnicast(): // fe80::/10
		return fmt.Errorf("link-local address %s is forbidden", ip)
	case ip.IsMulticast(): // ff00::/8 (includes link-local multicast)
		return fmt.Errorf("multicast address %s is forbidden", ip)
	case ip.IsPrivate(): // fc00::/7 ULA
		return fmt.Errorf("private network address %s is forbidden", ip)
	}
	return nil
}

// nat64Prefix is the 96-bit "Well-Known Prefix" 64:ff9b::/96 (RFC 6052).
// ipv4CompatPrefix is the all-zero 96-bit prefix of deprecated IPv4-compatible
// IPv6 addresses (::a.b.c.d, RFC 4291 §2.5.5.1).
//
// Both are read-only after init and MUST NOT be mutated — they are []byte (not
// const) only because make()/composite literals cannot be const. embeddedIPv4
// compares against them; a write would silently weaken the SSRF guard.
var (
	nat64Prefix      = []byte{0x00, 0x64, 0xff, 0x9b, 0, 0, 0, 0, 0, 0, 0, 0}
	ipv4CompatPrefix = make([]byte, 12)
)

// embeddedIPv4 returns the 4-byte IPv4 address embedded in the two IPv6 forms
// net.IP.To4 does not unwrap — NAT64 (64:ff9b::/96) and IPv4-compatible
// (::a.b.c.d) — or nil for any other address. Callers must handle :: and ::1
// before invoking this (both share the all-zero prefix).
func embeddedIPv4(ip net.IP) net.IP {
	ip = ip.To16()
	if ip == nil {
		return nil
	}
	if bytes.Equal(ip[:12], nat64Prefix) || bytes.Equal(ip[:12], ipv4CompatPrefix) {
		return net.IP{ip[12], ip[13], ip[14], ip[15]}
	}
	return nil
}

// newSSRFSafeTransport returns an http.Transport whose DialContext resolves DNS,
// classifies every returned IP via classifyParsedIP, and dials the first IP that
// passes — connecting to the exact validated IP rather than re-resolving the
// hostname. Cloning http.DefaultTransport preserves proxy, HTTP/2, and timeout
// defaults; only DialContext is replaced. For HTTPS, the transport still derives
// SNI and certificate verification from the request hostname, not the dial
// address, so dialing by IP is safe.
func newSSRFSafeTransport() *http.Transport {
	var transport *http.Transport
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = base.Clone()
	} else {
		transport = &http.Transport{}
	}
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}

	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("invalid address %q: %w", addr, err)
		}

		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("DNS lookup failed for %s: %w", host, err)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("no addresses resolved for %s", host)
		}

		var lastErr error
		for _, ipAddr := range ips {
			if cerr := classifyParsedIP(ipAddr.IP); cerr != nil {
				lastErr = cerr
				continue
			}
			conn, derr := dialer.DialContext(ctx, network, net.JoinHostPort(ipAddr.IP.String(), port))
			if derr != nil {
				lastErr = derr
				continue
			}
			return conn, nil
		}
		// Every resolved IP was blocked or undialable — fail closed.
		return nil, lastErr
	}

	return transport
}

// getFetchTransport lazily builds and caches the process-wide SSRF-safe
// transport. http.Transport is safe for concurrent use and pools connections, so
// it must be created once and shared rather than allocated per request (a
// per-request transport never pools and leaks idle conns + goroutines until
// IdleConnTimeout).
var (
	fetchTransport     *http.Transport
	fetchTransportOnce sync.Once
)

func getFetchTransport() *http.Transport {
	fetchTransportOnce.Do(func() {
		fetchTransport = newSSRFSafeTransport()
	})
	return fetchTransport
}
