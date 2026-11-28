package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testPageURL is the final response URL conversion tests resolve links against.
var testPageURL = mustParseURL("https://docs.example.test/guide/page")

func mustParseURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}

func TestConvertHTMLToMarkdown_References(t *testing.T) {
	tests := []struct {
		name    string
		base    string // final URL; testPageURL when empty
		html    string
		want    []string
		notWant []string
	}{
		{
			name: "relative, root-relative and protocol-relative destinations resolve",
			html: `<body><p><a href="../api/x.html">API</a> <a href="/root">Root</a> <a href="//cdn.example.test/lib">CDN</a></p></body>`,
			want: []string{
				"[API](https://docs.example.test/api/x.html)",
				"[Root](https://docs.example.test/root)",
				"[CDN](https://cdn.example.test/lib)",
			},
		},
		{
			name: "fragment becomes the absolute final page URL",
			html: `<body><p>See <a href="#examples">examples</a>.</p></body>`,
			want: []string{"[examples](https://docs.example.test/guide/page#examples)"},
		},
		{
			name:    "surrounding whitespace and embedded newlines are dropped",
			html:    "<body><p><a href=\"  /a\n/b \">split</a></p></body>",
			want:    []string{"[split](https://docs.example.test/a/b)"},
			notWant: []string{"%0A", "%20"},
		},
		{
			name:    "image renders alt text only",
			html:    `<body><p>Before <img src="diagram.png" alt="Request flow diagram"> after <img src="data:image/png;base64,iVBORw0KGgo=" alt="Inline chart"> <img src="noalt.png"></p></body>`,
			want:    []string{"Before Request flow diagram after Inline chart"},
			notWant: []string{"diagram.png", "data:", "noalt", "!["},
		},
		{
			name:    "link around an image keeps the alt text as its label",
			html:    `<body><p><a href="/home"><img src="logo.svg" alt="Home"></a> text</p></body>`,
			want:    []string{"[Home](https://docs.example.test/home)"},
			notWant: []string{"logo.svg"},
		},
		{
			name:    "unsupported schemes render only their text",
			html:    `<body><p><a href="javascript:alert(1)">run demo</a>, <a href="mailto:a@example.test">mail us</a>, <a href="ftp://example.test/f">ftp copy</a>, <a href="http:opaque">opaque</a></p></body>`,
			want:    []string{"run demo", "mail us", "ftp copy", "opaque"},
			notWant: []string{"javascript", "mailto", "ftp://", "](", "http:opaque"},
		},
		{
			name:    "credential-bearing destination renders only its text",
			html:    `<body><p><a href="https://user:hunter2@example.test/x">private mirror</a></p></body>`,
			want:    []string{"private mirror"},
			notWant: []string{"hunter2", "user:", "]("},
		},
		{
			name:    "credentials inherited from the final URL render only text",
			base:    "https://user:hunter2@docs.example.test/guide/page",
			html:    `<body><p><a href="other">sibling page</a> and <a href="#top">top</a></p></body>`,
			want:    []string{"sibling page", "top"},
			notWant: []string{"hunter2", "]("},
		},
		{
			name:    "link without visible text renders nothing",
			html:    `<body><p>Keep this <a href="/ghost"><span> </span></a><a href="/icon"><img src="i.png"></a><a href="/redacted"><private>x</private></a>text.</p></body>`,
			want:    []string{"Keep this text."},
			notWant: []string{"ghost", "icon", "/redacted", "](", "REDACTED"},
		},
		{
			name:    "a missing, empty or blank href is not a link",
			html:    `<body><p><a name="anchor">named anchor</a> <a href="">empty href</a> <a href="  ">blank href</a></p></body>`,
			want:    []string{"named anchor", "empty href", "blank href"},
			notWant: []string{"]("},
		},
		{
			name:    "a secret-shaped scheme is validated before redaction",
			html:    `<body><p><a href="token:abcdefghijklmnopqrstuvwxyz0123">token link</a></p></body>`,
			want:    []string{"token link"},
			notWant: []string{"](", "REDACTED", "abcdefghij"},
		},
		{
			name:    "a token split by a removed image or unwrapped link is redacted whole",
			html:    `<body><p>alt ghp_<img src="a.png" alt="` + strings.Repeat("A1b2C3", 6) + `"> end</p><p>link ghp_<a href="javascript:x">` + strings.Repeat("A1b2C3", 6) + `</a> end</p></body>`,
			want:    []string{"alt", "link", "end", "REDACTED"},
			notWant: []string{"A1b2C3A1b2C3"},
		},
		{
			name:    "links inside code render as code text, images as alt text",
			html:    `<body><pre><code><a href="/src/main.go#L12">12</a> <a href="/pkg/fmt">fmt</a>.Println(<img src="i.png" alt="icon">)</code></pre><p>press <kbd><img src="k.png" alt="Ctrl"></kbd> and <code><a href="/x">x</a></code></p></body>`,
			want:    []string{"12 fmt.Println(icon)", "`Ctrl`", "`x`"},
			notWant: []string{"main.go", "/pkg/fmt", "i.png", "]("},
		},
		{
			name:    "text that redacts to a private placeholder does not select a root",
			html:    `<body><main><p>&lt;private&gt;escaped&lt;/private&gt;</p></main><main><p>real article</p></main></body>`,
			want:    []string{"real article"},
			notWant: []string{"REDACTED", "escaped"},
		},
		{
			name:    "a textless link keeps a space only where it held whitespace",
			html:    `<body><p>end<a href="/x"><private>s</private></a>. Keep <a href="/y"> </a>apart.</p></body>`,
			want:    []string{"end. Keep apart."},
			notWant: []string{"REDACTED"},
		},
		{
			name:    "a bare fragment points to the page itself",
			base:    "https://docs.example.test/guide/page#old-section",
			html:    `<body><p><a href="#">top</a> <a href="#new">new</a></p></body>`,
			want:    []string{"[top](https://docs.example.test/guide/page)", "[new](https://docs.example.test/guide/page#new)"},
			notWant: []string{"old-section"},
		},
		{
			name:    "a secret in the path drops the destination",
			html:    `<body><p><a href="/files/ghp_` + strings.Repeat("A1b2C3", 6) + `/x">download</a></p></body>`,
			want:    []string{"download"},
			notWant: []string{"](", "A1b2C3"},
		},
		{
			name:    "document base element is ignored",
			html:    `<html><head><base href="https://evil.example.test/root/"></head><body><base href="https://evil2.example.test/"><p><a href="ref.html">reference</a></p></body></html>`,
			want:    []string{"[reference](https://docs.example.test/guide/ref.html)"},
			notWant: []string{"evil"},
		},
		{
			name:    "fragment inheriting a secret query is redacted",
			base:    "https://docs.example.test/guide/page?access_token=abcdefghijklmnopqrstuvwxyz",
			html:    `<body><p><a href="#install">install</a></p></body>`,
			want:    []string{"[install](https://docs.example.test/guide/page?", "REDACTED", "#install)"},
			notWant: []string{"abcdefghijklmnopqrstuvwxyz"},
		},
		{
			name:    "a percent-encoded inherited credential is redacted as a whole field",
			base:    "https://docs.example.test/guide/page?lang=go&token=abc%2Fdefghijklmnopqrstuvwxyz",
			html:    `<body><p><a href="#install">install</a> and <a href="/plain">plain</a></p></body>`,
			want:    []string{"[install](https://docs.example.test/guide/page?lang=go&", "REDACTED", "#install)", "[plain](https://docs.example.test/plain)"},
			notWant: []string{"defghijklmnopqrstuvwxyz", "abc"},
		},
		{
			name:    "a partly matching credential leaves no tail",
			base:    "https://docs.example.test/guide/page?token=ABCDEFGHIJKLMNOPQRSTUVWXYZ%2Ftailsecret",
			html:    `<body><p><a href="#install">install</a></p></body>`,
			want:    []string{"[install](https://docs.example.test/guide/page?", "REDACTED"},
			notWant: []string{"tailsecret", "ABCDEFGHIJ"},
		},
		{
			name:    "a form-encoded credential assignment is redacted",
			base:    "https://docs.example.test/guide/page?token+=+abcdefghijklmnopqrstuvwxyz",
			html:    `<body><p><a href="#install">install</a></p></body>`,
			want:    []string{"[install](", "REDACTED"},
			notWant: []string{"abcdefghijklmnopqrstuvwxyz"},
		},
		{
			name:    "a private region spanning query fields drops the destination",
			base:    "https://docs.example.test/guide/page?a=%3Cprivate%3E&b=sentinelvalue&c=%3C%2Fprivate%3E",
			html:    `<body><p><a href="#install">install</a></p></body>`,
			want:    []string{"install"},
			notWant: []string{"sentinelvalue", "]("},
		},
		{
			name:    "an empty final query delimiter is kept only for the page itself",
			base:    "https://docs.example.test/guide/page?",
			html:    `<body><p><a href="#install">install</a> <a href="https://example.org">other</a> <a href="/root">root</a></p></body>`,
			want:    []string{"[install](https://docs.example.test/guide/page?#install)", "[other](https://example.org)", "[root](https://docs.example.test/root)"},
			notWant: []string{"example.org?", "root?"},
		},
		{
			name: "an encoded leading slash is path-relative",
			html: `<body><p><a href="%2Ftopic">topic</a></p></body>`,
			want: []string{"[topic](https://docs.example.test/guide/%2Ftopic)"},
		},
		{
			name:    "a private region spanning the page path and a fragment drops the destination",
			base:    "https://docs.example.test/guide/%3Cprivate%3Esentinel",
			html:    `<body><p><a href="#tail%3C%2Fprivate%3E">jump</a></p></body>`,
			want:    []string{"jump"},
			notWant: []string{"](", "sentinel"},
		},
		{
			name:    "a private region spanning an explicit link's query and fragment drops it",
			html:    `<body><p><a href="https://docs.example.test/p?a=%3Cprivate%3Esentinel#b%3C%2Fprivate%3E">explicit</a></p></body>`,
			want:    []string{"explicit"},
			notWant: []string{"](", "sentinel"},
		},
		{
			name:    "a private opener erased by query redaction still protects a fragment",
			base:    "https://docs.example.test/p?a=%3Cprivate%3Etoken%3Dabcdefghijklmnopqrstuvwxyz",
			html:    `<body><p><a href="#sentinel%3C%2Fprivate%3E">inherited</a> <a href="https://docs.example.test/p?a=%3Cprivate%3Etoken%3Dabcdefghijklmnopqrstuvwxyz#sentinel%3C%2Fprivate%3E">explicit</a></p></body>`,
			want:    []string{"inherited explicit"},
			notWant: []string{"](", "sentinel"},
		},
		{
			name:    "a non-ASCII look-alike closer does not end an open private region",
			base:    "https://docs.example.test/%3Cprivate%3Ea%3C%2Fpr%C4%B0vate%3E",
			html:    `<body><p><a href="#sentinel%3C%2Fprivate%3E">jump</a></p></body>`,
			want:    []string{"jump"},
			notWant: []string{"](", "sentinel"},
		},
		{
			name: "a broken empty-authority link points to the page",
			base: "https://docs.example.test/p?lang=go#old",
			html: `<body><p><a href="//">broken</a></p></body>`,
			want: []string{"[broken](https://docs.example.test/p?lang=go)"},
		},
		{
			name: "a harmless form-encoded search query keeps fragment links",
			base: "https://docs.example.test/search?q=token%3Dread+the+next+section+here",
			html: `<body><p><a href="#results">results</a></p></body>`,
			// The renderer re-encodes the query ("+" becomes "%20").
			want: []string{"[results](https://docs.example.test/search?q=token%3Dread", "section", "#results)"},
		},
		{
			name:    "a private delimiter sharing a field with a credential drops the destination",
			base:    "https://docs.example.test/guide/page?a=%3Cprivate%3Etoken%3Dabcdefghijklmnopqrstuvwxyz&b=sentinelvalue&c=%3C%2Fprivate%3E",
			html:    `<body><p><a href="#install">install</a></p></body>`,
			want:    []string{"install"},
			notWant: []string{"sentinelvalue", "]("},
		},
		{
			name:    "a credential continuing into alt text is redacted whole",
			html:    `<body><p>key sk-ant-` + strings.Repeat("A", 24) + `<img src="x.png" alt="Zq9TailSecretValue"> end</p></body>`,
			want:    []string{"key", "REDACTED", "end"},
			notWant: []string{"TailSecret", "AAAAAAAA"},
		},
		{
			name: "a stray percent sign keeps the link",
			base: "https://docs.example.test/guide/page?p=50%&q=1",
			html: `<body><p><a href="#install">install</a></p></body>`,
			want: []string{"[install](https://docs.example.test/guide/page?p=50", "#install)"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := testPageURL
			if tt.base != "" {
				base = mustParseURL(tt.base)
			}
			md, err := convertHTMLToMarkdown(tt.html, "text/html", base, store.DefaultMaxSourceBytes)
			require.NoError(t, err)
			for _, w := range tt.want {
				assert.Contains(t, md, w)
			}
			for _, nw := range tt.notWant {
				assert.NotContains(t, md, nw)
			}
		})
	}
}

func TestConvertHTMLToMarkdown_LinkBudget(t *testing.T) {
	const dest = "https://docs.example.test/a" // what href="/a" resolves to
	twoLinks := `<body><p><a href="/a">one</a> <a href="/a">two</a></p></body>`

	t.Run("total equal to the limit is accepted", func(t *testing.T) {
		md, err := convertHTMLToMarkdown(twoLinks, "text/html", testPageURL, 2*len(dest))
		require.NoError(t, err)
		assert.Contains(t, md, "[two]("+dest+")")
	})

	t.Run("total above the limit fails naming the limit", func(t *testing.T) {
		limit := 2*len(dest) - 1
		md, err := convertHTMLToMarkdown(twoLinks, "text/html", testPageURL, limit)
		require.EqualError(t, err, fmt.Sprintf(
			"resolved link destinations exceed the %d-byte source limit (configure store.max_source_bytes to adjust)", limit))
		assert.Empty(t, md)
	})

	t.Run("a link nested in a link counts once", func(t *testing.T) {
		page := `<body><p>body text</p><svg><a href="/a"><a href="/a"><text>label</text></a></a></svg></body>`
		_, err := convertHTMLToMarkdown(page, "text/html", testPageURL, len(dest))
		require.NoError(t, err)
	})

	t.Run("equivalent relative and absolute destinations cost the same", func(t *testing.T) {
		query := "?access_token=" + strings.Repeat("z", 200)
		for _, href := range []string{"https://docs.example.test/a" + query, "/a" + query} {
			page := `<body><p><a href="` + href + `">tokened</a></p></body>`
			md, err := convertHTMLToMarkdown(page, "text/html", testPageURL, 100)
			require.NoError(t, err, href)
			assert.Contains(t, md, "[tokened](https://docs.example.test/a?", href)
		}
	})

	t.Run("sibling links cost the same as their root-relative spelling", func(t *testing.T) {
		// A long file name is not part of a sibling's destination.
		base := mustParseURL("https://docs.example.test/a/" + strings.Repeat("f", 150))
		var outputs []string
		for _, href := range []string{"next", "/a/next"} {
			page := `<body><p>` + strings.Repeat(`<a href="`+href+`">n</a> `, 32) + `</p></body>`
			md, err := convertHTMLToMarkdown(page, "text/html", base, 4096)
			require.NoError(t, err, href)
			outputs = append(outputs, md)
		}
		assert.Equal(t, outputs[0], outputs[1])
	})

	t.Run("dropped path-relative links count the base path they read", func(t *testing.T) {
		// Inherited userinfo drops every relative link.
		base := mustParseURL("https://user:pw@docs.example.test/" + strings.Repeat("d", 99) + "/page")
		page := `<body><p>` + strings.Repeat(`<a href="b">x</a> `, 10) + `</p></body>`
		_, err := convertHTMLToMarkdown(page, "text/html", base, 999)
		require.ErrorContains(t, err, "resolved link destinations exceed")
	})

	t.Run("a long final path does not multiply per-link work", func(t *testing.T) {
		base := mustParseURL("https://docs.example.test/token=" + strings.Repeat("A", 24) + "/" + strings.Repeat("p", 64*1024) + "/page")
		for _, tc := range []struct {
			name, link string
			n          int
			wantErr    bool
		}{
			// The page itself is rejected once; its fragment links render as text.
			{"fragment links", `<a href="#x">x</a> `, 100000, false},
			// Resolved against the origin, so only their short destinations count.
			{"root-relative links", `<a href="/x">x</a> `, 50000, false},
			// Each reads the long base path and is charged for it.
			{"path-relative links", `<a href="y">x</a> `, 50000, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				page := `<body><p>` + strings.Repeat(tc.link, tc.n) + `</p></body>`
				start := time.Now()
				_, err := convertHTMLToMarkdown(page, "text/html", base, store.DefaultMaxSourceBytes)
				if tc.wantErr {
					require.ErrorContains(t, err, "resolved link destinations exceed")
				} else {
					require.NoError(t, err)
				}
				assert.Less(t, time.Since(start), 10*time.Second, "work must stay near what is charged")
			})
		}
	})

	t.Run("links inside code do not count", func(t *testing.T) {
		page := `<body><pre>` + strings.Repeat(`<a href="/a">x</a> `, 50) + `</pre><p><a href="/a">kept</a></p></body>`
		md, err := convertHTMLToMarkdown(page, "text/html", testPageURL, len(dest))
		require.NoError(t, err)
		assert.Contains(t, md, "[kept]("+dest+")")
	})

	t.Run("links rendered as text do not count", func(t *testing.T) {
		page := `<body><p>` + strings.Repeat(`<a href="javascript:void(0)">x</a> `, 50) + `<a href="/a">kept</a></p></body>`
		md, err := convertHTMLToMarkdown(page, "text/html", testPageURL, len(dest))
		require.NoError(t, err)
		assert.Contains(t, md, "[kept]("+dest+")")
	})

	t.Run("budget counts redacted destinations", func(t *testing.T) {
		secret := "https://docs.example.test/a?access_token=" + strings.Repeat("z", 200)
		page := `<body><p><a href="` + secret + `">tokened</a></p></body>`
		md, err := convertHTMLToMarkdown(page, "text/html", testPageURL, 100)
		require.NoError(t, err, "the redacted destination fits although the raw one does not")
		assert.Contains(t, md, "[tokened](")
		assert.NotContains(t, md, "zzzz")
	})
}

// fetchRequestLog records the path and negotiation headers of every request a
// test server receives.
type fetchRequestLog struct {
	mu   sync.Mutex
	reqs []*http.Request
}

func (l *fetchRequestLog) add(r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reqs = append(l.reqs, r.Clone(r.Context()))
}

func (l *fetchRequestLog) snapshot() []*http.Request {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]*http.Request(nil), l.reqs...)
}

// A redirect across path bases resolves relative references against the final
// URL while the requested URL stays the cache identity. Conversion fetches
// nothing it references, both requests negotiate, and a repeat call hits the
// cache.
func TestFetchRedirect_ResolvesAgainstFinalURL(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch=%t", batch), func(t *testing.T) {
			disableSSRFValidation(t)
			var log fetchRequestLog
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				log.add(r)
				switch r.URL.Path {
				case "/old/start":
					http.Redirect(w, r, "/new/docs/page", http.StatusFound)
				case "/new/docs/page":
					w.Header().Set("Content-Type", "text/html")
					fmt.Fprint(w, `<html><head><base href="https://evil.example.test/"></head><body><main><h1>Wombat Guide</h1>
<p>Read the <a href="ref.html">reference</a>, jump to <a href="#examples">examples</a>.</p>
<p><img src="pic.png" alt="Wombat burrow diagram"></p></main></body></html>`)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(ts.Close)
			requested := ts.URL + "/old/start"
			srv := newTestServer(t, nil)
			srv.config.Store.Cache.FetchTTLHours = 1

			r := callFetchAndIndex(t, srv, fetchArgs(requested, batch, false))
			require.False(t, r.IsError, resultText(r))
			text := resultText(r)
			// The batch preview is capped, so assert stored content in both modes.
			info := fetchSourceInfo(t, srv, composeFetchCacheKey("freshness", requested))
			chunks, err := srv.getStore().GetChunksBySource(info.ID)
			require.NoError(t, err)
			var stored strings.Builder
			for _, c := range chunks {
				stored.WriteString(c.Content)
			}
			for _, got := range []string{text, stored.String()} {
				assert.Contains(t, got, "[reference]("+ts.URL+"/new/docs/ref.html)")
				assert.Contains(t, got, "Wombat burrow diagram")
				assert.NotContains(t, got, "pic.png")
				assert.NotContains(t, got, "evil.example.test")
			}
			assert.Contains(t, stored.String(), "[examples]("+ts.URL+"/new/docs/page#examples)")
			if !batch {
				assert.Contains(t, text, "[examples]("+ts.URL+"/new/docs/page#examples)")
			}

			reqs := log.snapshot()
			require.Len(t, reqs, 2, "only the redirect and the page; nothing referenced is fetched")
			assert.Equal(t, "/old/start", reqs[0].URL.Path)
			assert.Equal(t, "/new/docs/page", reqs[1].URL.Path)
			for _, req := range reqs {
				assert.Equal(t, fetchUserAgent, req.Header.Get("User-Agent"), req.URL.Path)
				// The agreed list, pinned independently of the production constant.
				assert.Equal(t, "text/html, application/xhtml+xml;q=0.9, application/json;q=0.8, text/plain;q=0.7, */*;q=0.1",
					req.Header.Get("Accept"), req.URL.Path)
			}

			r = callFetchAndIndex(t, srv, fetchArgs(requested, batch, false))
			require.False(t, r.IsError, resultText(r))
			assert.Contains(t, strings.ToLower(resultText(r)), "cache hit")
			assert.Len(t, log.snapshot(), 2, "the requested URL is the cache identity")
		})
	}
}

// negotiatedType returns the offer with the highest positive q-value in an
// Accept header, earlier offers winning ties, or "" when none is acceptable.
func negotiatedType(accept string, offers ...string) string {
	best, bestQ := "", 0.0
	for _, offer := range offers {
		q := 0.0
		for part := range strings.SplitSeq(accept, ",") {
			fields := strings.Split(part, ";")
			media := strings.TrimSpace(fields[0])
			if media != offer && media != "*/*" && media != strings.Split(offer, "/")[0]+"/*" {
				continue
			}
			pq := 1.0
			for _, p := range fields[1:] {
				if v, ok := strings.CutPrefix(strings.TrimSpace(p), "q="); ok {
					pq, _ = strconv.ParseFloat(v, 64)
				}
			}
			q = max(q, pq)
		}
		if q > bestQ {
			best, bestQ = offer, q
		}
	}
	return best
}

func TestFetchAcceptNegotiation(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch=%t/html preferred after redirect", batch), func(t *testing.T) {
			disableSSRFValidation(t)
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/start" {
					http.Redirect(w, r, "/resource", http.StatusMovedPermanently)
					return
				}
				// JSON offered first: only the client's q-values make HTML win.
				switch negotiatedType(r.Header.Get("Accept"), "application/json", "text/html") {
				case "text/html":
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					fmt.Fprint(w, `<body><main><h1>Numbat Page</h1><p>browser representation</p></main></body>`)
				case "application/json":
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"representation": "api payload"}`)
				default:
					w.WriteHeader(http.StatusNotAcceptable)
				}
			}))
			t.Cleanup(ts.Close)
			srv := newTestServer(t, nil)
			r := callFetchAndIndex(t, srv, fetchArgs(ts.URL+"/start", batch, true))
			require.False(t, r.IsError, resultText(r))
			assert.Contains(t, resultText(r), "browser representation")
			assert.NotContains(t, resultText(r), "api payload")
			info := fetchSourceInfo(t, srv, composeFetchCacheKey("freshness", ts.URL+"/start"))
			assert.Equal(t, "markdown", info.ContentType)
		})

		t.Run(fmt.Sprintf("batch=%t/vendor json only", batch), func(t *testing.T) {
			disableSSRFValidation(t)
			const vendor = "application/vnd.example+json"
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if negotiatedType(r.Header.Get("Accept"), vendor) == "" {
					w.WriteHeader(http.StatusNotAcceptable)
					return
				}
				w.Header().Set("Content-Type", vendor)
				fmt.Fprint(w, `{"vendor": "quoll payload"}`)
			}))
			t.Cleanup(ts.Close)
			srv := newTestServer(t, nil)
			r := callFetchAndIndex(t, srv, fetchArgs(ts.URL, batch, true))
			require.False(t, r.IsError, resultText(r))
			assert.NotContains(t, resultText(r), "✗")
			assert.Contains(t, resultText(r), "quoll payload")
			info := fetchSourceInfo(t, srv, composeFetchCacheKey("freshness", ts.URL))
			assert.Equal(t, "json", info.ContentType)
		})
	}
}

// A link-heavy page whose resolved destinations exceed a small
// max_source_bytes fails before the store write: the prior copy and its
// validation time stay, and a later batch item still indexes.
func TestFetchLinkBudget_KeepsPriorCopy(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch=%t", batch), func(t *testing.T) {
			disableSSRFValidation(t)
			// Short fragments expand to the deep final page URL: the body stays
			// small while its resolved destinations grow past the limit.
			var links strings.Builder
			for i := range 80 {
				fmt.Fprintf(&links, `<a href="#s%02d">s</a> `, i)
			}
			page := `<body><main><h1>Index</h1><p>` + links.String() + `</p></main></body>`
			pageServer := htmlServer(t, "text/html", &page)
			pageURL := pageServer.URL + "/" + strings.Repeat("deep/", 20) + "index.html"
			good := `<body><p>sibling article</p></body>`
			sibling := htmlServer(t, "text/html", &good)

			srv := newTestServer(t, nil)
			srv.config.Store.MaxSourceBytes = 4096
			require.Equal(t, 4096, srv.getStore().MaxSourceBytes())
			require.Less(t, len(page), 4096, "the body alone fits; only resolved destinations exceed")
			key := composeFetchCacheKey("freshness", pageURL)
			_, err := srv.getStore().IndexFetched("retained wombat content", key, "plaintext", store.KindDurable, time.Now().Add(-48*time.Hour))
			require.NoError(t, err)
			before, err := srv.getStore().GetSourceMeta(key)
			require.NoError(t, err)

			args := fetchArgs(pageURL, batch, true)
			if batch {
				args["requests"] = []any{
					map[string]any{"url": pageURL, "source": "freshness"},
					map[string]any{"url": sibling.URL, "source": "sibling"},
				}
			}
			r := callFetchAndIndex(t, srv, args)
			text := resultText(r)
			if batch {
				require.False(t, r.IsError, text)
				assert.Contains(t, text, "✗ "+pageURL+" — index error: converting fetched HTML: resolved link destinations exceed the 4096-byte source limit")
				assert.Contains(t, text, "✓ sibling — indexed")
			} else {
				require.True(t, r.IsError)
				assert.Contains(t, text, "converting fetched HTML: resolved link destinations exceed the 4096-byte source limit")
			}
			assert.Contains(t, text, "configure store.max_source_bytes")

			after, err := srv.getStore().GetSourceMeta(key)
			require.NoError(t, err)
			assert.Equal(t, before, after, "prior copy and validation time must not move")
			results, err := srv.getStore().SearchWithFallback("wombat", 5, store.SearchOptions{Source: "freshness"})
			require.NoError(t, err)
			require.NotEmpty(t, results)
		})
	}
}

// A credential in the redirect target's query, which no fragment link on the
// page spells out, reaches neither the preview nor the stored chunks.
func TestFetchRedirect_InheritedQueryCredentialRedacted(t *testing.T) {
	const credential = "abc%2Fdefghijklmnopqrstuvwxyz"
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch=%t", batch), func(t *testing.T) {
			disableSSRFValidation(t)
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/start" {
					http.Redirect(w, r, "/docs/page?lang=go&token="+credential, http.StatusFound)
					return
				}
				w.Header().Set("Content-Type", "text/html")
				fmt.Fprint(w, `<body><main><h1>Bilby</h1><p>Jump to <a href="#usage">usage</a>.</p></main></body>`)
			}))
			t.Cleanup(ts.Close)
			srv := newTestServer(t, nil)
			r := callFetchAndIndex(t, srv, fetchArgs(ts.URL+"/start", batch, true))
			require.False(t, r.IsError, resultText(r))

			info := fetchSourceInfo(t, srv, composeFetchCacheKey("freshness", ts.URL+"/start"))
			chunks, err := srv.getStore().GetChunksBySource(info.ID)
			require.NoError(t, err)
			var stored strings.Builder
			for _, c := range chunks {
				stored.WriteString(c.Content)
			}
			for _, got := range []string{resultText(r), stored.String()} {
				assert.Contains(t, got, "[usage]("+ts.URL+"/docs/page?lang=go&")
				assert.Contains(t, got, "REDACTED")
				assert.NotContains(t, got, "defghijklmnopqrstuvwxyz")
			}
		})
	}
}
