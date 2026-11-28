package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/serpro69/capy/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// syntheticGitHubToken matches the sanitizer's ghp_ pattern; it is not a real
// credential.
var syntheticGitHubToken = "ghp_" + strings.Repeat("A1b2C3", 6)

func TestConvertHTMLToMarkdown_ArticleSelection(t *testing.T) {
	tests := []struct {
		name    string
		html    string
		want    []string
		notWant []string
	}{
		{
			name: "table and indented example under main, sidebar outside",
			html: `<html><head><title>Manual</title></head><body>
<aside>sidebar wombat wombat wombat</aside>
<main><h1>Options</h1>
<table><tr><th>Flag</th><th>Meaning</th></tr><tr><td>--quiet</td><td>suppress output</td></tr></table>
<pre><code>func main() {
    run()
}</code></pre></main></body></html>`,
			want:    []string{"# Options", "| Flag", "--quiet", "func main() {\n    run()\n}"},
			notWant: []string{"wombat", "# Manual"},
		},
		{
			name: "hidden duplicate main is skipped",
			html: `<body><main hidden><p>stale duplicate wombat</p></main>
<main aria-hidden="true"><p>aria duplicate wombat</p></main>
<main><p>visible article text</p></main></body>`,
			want:    []string{"visible article text"},
			notWant: []string{"wombat"},
		},
		{
			name:    "empty main falls back to visible body text",
			html:    `<body><main> <div></div> </main><div><p>text outside the empty main</p></div></body>`,
			want:    []string{"text outside the empty main"},
			notWant: nil,
		},
		{
			name: "sole article inside main wins over file listing",
			html: `<body><main>
<table><tr><td>package.json</td><td>initial commit</td></tr></table>
<article><header><h1>Express</h1></header><p>Fast web framework.</p>
<table><tr><th>Method</th></tr><tr><td>app.listen</td></tr></table></article>
</main></body>`,
			want:    []string{"# Express", "Fast web framework.", "app.listen"},
			notWant: []string{"package.json", "initial commit"},
		},
		{
			name: "two sibling articles keep the outer root",
			html: `<body><main><p>intro paragraph</p>
<article><h2>First</h2><p>first body</p></article>
<article><h2>Second</h2><p>second body</p></article></main></body>`,
			want: []string{"intro paragraph", "first body", "second body"},
		},
		{
			name:    "nested comment articles do not break a sole article",
			html:    `<body><article><h1>Post</h1><p>post body</p><article><p>a comment</p></article></article><p>outside text</p></body>`,
			want:    []string{"# Post", "post body", "a comment"},
			notWant: []string{"outside text"},
		},
		{
			name:    "short valid page with title",
			html:    `<html><head><title>  Tiny   Spec </title></head><body><p>Returns 42.</p></body></html>`,
			want:    []string{"# Tiny Spec", "Returns 42."},
			notWant: nil,
		},
		{
			name:    "existing h1 suppresses the title",
			html:    `<html><head><title>Site Title</title></head><body><h1>Page Heading</h1><p>body</p></body></html>`,
			want:    []string{"# Page Heading"},
			notWant: []string{"Site Title"},
		},
		{
			name: "chrome, inline hiding, navigation role and form controls are removed",
			html: `<body><header>site banner</header><nav>menu</nav>
<div role="search navigation">role nav</div>
<div style="color: red; DISPLAY : none !important">inline hidden</div>
<div style="visibility:hidden">invisible</div>
<p>kept text <button>Copy</button><input value="typed"><select><option>opt</option></select></p>
<div hidden="until-found">findable section</div>
<template><p>template text</p></template><iframe>frame</iframe>
<footer>copyright</footer></body>`,
			want:    []string{"kept text", "findable section"},
			notWant: []string{"site banner", "menu", "role nav", "inline hidden", "invisible", "Copy", "typed", "opt", "template text", "frame", "copyright"},
		},
		{
			name:    "loading placeholder is a short page",
			html:    `<body><div id="root"><p>Loading…</p></div><script>boot()</script></body>`,
			want:    []string{"Loading…"},
			notWant: []string{"boot"},
		},
		{
			name:    "private block is redacted on the tree",
			html:    `<body><p>public text</p><private><p>private sentinel</p></private></body>`,
			want:    []string{"public text", "REDACTED]"},
			notWant: []string{"private sentinel"},
		},
		{
			name:    "private table rows foster-parented out of the wrapper stay redacted",
			html:    `<body><p>pub</p><table><private><tr><td>rowsecret</td></tr></private></table></body>`,
			want:    []string{"pub", "REDACTED]"},
			notWant: []string{"rowsecret"},
		},
		{
			name:    "nested private regions are redacted whole",
			html:    `<body><p>pub</p><private>outer1 <private>inner</private> outerTail</private><p>after</p></body>`,
			want:    []string{"pub", "REDACTED]", "after"},
			notWant: []string{"outer1", "inner", "outerTail"},
		},
		{
			name:    "private wrapper with attributes around table rows is redacted",
			html:    `<body><p>pub</p><table><PRIVATE class="x"><tr><td>rowsecret</td></tr></PRIVATE></table></body>`,
			want:    []string{"pub"},
			notWant: []string{"rowsecret"},
		},
		{
			name:    "self-closing private opens a region",
			html:    `<body><p>pub</p><private/>secret text</private><p>after</p></body>`,
			want:    []string{"pub", "after"},
			notWant: []string{"secret text"},
		},
		{
			name:    "custom element named like private is ordinary markup",
			html:    `<body><private-note>visible note</private-note><svg><![CDATA[x]]></svg><p>after</p></body>`,
			want:    []string{"visible note", "after"},
			notWant: []string{"REDACTED"},
		},
		{
			name:    "svg sprite definitions are not rendered",
			html:    `<body><p>diagram caption</p><svg><defs><symbol id="n"><text>node label</text></symbol></defs><use href="#n"/></svg></body>`,
			want:    []string{"diagram caption"},
			notWant: []string{"node label"},
		},
		{
			name:    "self-closing private in a table keeps rows inside the region",
			html:    `<body><p>pub</p><table><private/><tr><td>rowsecret</td></tr></private></table><p>after</p></body>`,
			want:    []string{"pub", "after"},
			notWant: []string{"rowsecret"},
		},
		{
			name:    "self-closing private nested in a region counts as an open element",
			html:    `<body><p>pub</p><private>a <private/> b</private> tail text</private><p>after</p></body>`,
			want:    []string{"pub", "after"},
			notWant: []string{"tail text"},
		},
		{
			name:    "private markers inside script text are not regions",
			html:    `<body><script>var s = "<private>";</script><p>visible paragraph</p></body>`,
			want:    []string{"visible paragraph"},
			notWant: []string{"REDACTED"},
		},
		{
			name:    "secret split by a pruned element is redacted",
			html:    `<body><p>` + syntheticGitHubToken[:10] + `<span hidden>x</span>` + syntheticGitHubToken[10:] + `</p></body>`,
			notWant: []string{"A1b2C3A1b2C3"},
		},
		{
			name:    "secret split by body meta and link is redacted",
			html:    `<body><p>` + syntheticGitHubToken[:8] + `<meta itemprop="x">` + syntheticGitHubToken[8:20] + `<link rel="x">` + syntheticGitHubToken[20:] + `</p></body>`,
			notWant: []string{"A1b2C3A1b2C3"},
		},
		{
			name:    "secret split by a comment is redacted",
			html:    `<body><p>` + syntheticGitHubToken[:10] + `<!-- c -->` + syntheticGitHubToken[10:] + `</p></body>`,
			notWant: []string{"A1b2C3A1b2C3"},
		},
		{
			name:    "template title in head is not the document title",
			html:    `<html><head><template><title>Inert</title></template><title>Real</title></head><body><p>b</p></body></html>`,
			want:    []string{"# Real"},
			notWant: []string{"Inert"},
		},
		{
			name:    "hidden private element leaves no marker that could win selection",
			html:    `<body><main><private hidden><p>x</p></private></main><article><p>real article</p></article></body>`,
			want:    []string{"real article"},
			notWant: []string{"REDACTED"},
		},
		{
			name: "inactive tab panels stay, aria-hidden panels do not",
			html: `<body><h1>Install</h1><div role="tabpanel"><pre><code>npm i capy</code></pre></div>
<div role="tabpanel" hidden><pre><code>yarn add capy</code></pre></div>
<div role="tabpanel" aria-hidden="true"><pre><code>pnpm add capy</code></pre></div></body>`,
			want:    []string{"npm i capy", "yarn add capy"},
			notWant: []string{"pnpm add capy"},
		},
		{
			name: "token-bearing attributes keep markup intact",
			html: `<body><script src="https://x.test/e.js?token=abcdefghijklmnopqrstuvwxyz"></script>
<main><p><a href="https://api.test/v1?access_token=abcdefghijklmnopqrstuvwxyz">example</a> following text</p>
<p><img src="https://m.test/t.png?access_token=abcdefghijklmnopqrstuvwxyz" alt="map"> caption text</p><p class="x">next para</p></main></body>`,
			want:    []string{"[example](", "following text", "caption text", "next para", "REDACTED"},
			notWant: []string{"abcdefghijklmnopqrstuvwxyz", "%3C", "<p"},
		},
		{
			name: "secrets are redacted before Markdown escaping",
			html: `<body><p>token ` + syntheticGitHubToken + `</p><pre><code>export T=` + syntheticGitHubToken + `</code></pre>
<p><a href="https://example.test/?k=` + syntheticGitHubToken + `" title="` + syntheticGitHubToken + `">link</a></p></body>`,
			want:    []string{"token", "export T=", "link"},
			notWant: []string{"A1b2C3A1b2C3"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			md, err := convertHTMLToMarkdown(tt.html, "text/html", testPageURL, store.DefaultMaxSourceBytes)
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

func TestConvertHTMLToMarkdown_AmbiguousPrivate(t *testing.T) {
	tests := []struct {
		name string
		html string
	}{
		{"missing end tag", `<body><p>pub</p><private><p>secret</p></body>`},
		{"end tag hidden by tokenizer raw text", `<body><p>pub</p><private><svg><title/></svg>secret</private><p>rest of article</p></body>`},
		{"opener hidden by tokenizer raw text", `<body><p>pub</p><svg><title>icon</svg><private>secret text</private><p>after</p></body>`},
		{"opener hidden before foster-parented rows", `<body><p>pub</p><svg><title/></svg><table><private><tr><td>rowsecret</td></tr></private></table></body>`},
		{"stray end tag", `<body><p>pub</p></private><p>after</p></body>`},
		{"cdata with a closing marker and no real closer", `<body><private>s<svg><![CDATA[a></private>]]></svg> tail</body>`},
		{"cdata with a closing marker before the real closer", `<body><private><svg><![CDATA[</private>]]></svg>secret</private><p>after</p></body>`},
		{"cdata read as raw text by the tokenizer", `<body><private>s<svg><title/><![CDATA[</title></private>]]></svg> tail</body>`},
		// Accepted conservative cost: both markers as script string literals.
		{"private and cdata markers in script text", `<body><script>var a = "<private>", b = "<![CDATA[";</script><p>visible</p></body>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			md, err := convertHTMLToMarkdown(tt.html, "text/html", testPageURL, store.DefaultMaxSourceBytes)
			require.ErrorIs(t, err, errPrivateBoundary)
			assert.Empty(t, md)
		})
	}
}

func TestConvertHTMLToMarkdown_NoReadableContent(t *testing.T) {
	tests := []struct {
		name string
		html string
	}{
		{"application shell with title", `<html><head><title>App</title></head><body><div id="root"></div><script>render()</script></body></html>`},
		{"only hidden and chrome text", `<body><nav>menu</nav><div hidden>secret page</div><footer>footer</footer></body>`},
		{"whitespace and nbsp only", "<body> \n  </body>"},
		{"hidden body with title", `<html><head><title>App</title></head><body hidden><p>x</p></body></html>`},
		{"display:none body with title", `<html><head><title>App</title></head><body style="display:none"><p>x</p></body></html>`},
		{"icon-only shell with svg metadata", `<html><head><title>App</title></head><body><div id="root"><svg><title>logo</title><desc>d</desc></svg></div></body></html>`},
		{"only private content", `<body><private><p>all of it</p></private></body>`},
		{"private region completed by removing an image", `<html><head><title>App</title></head><body><main><p>&lt;private&gt;<img src="x.png">hidden&lt;/private&gt;</p></main></body></html>`},
		{"svg sprite definitions only", `<body><svg><defs><text id="a">defs text</text></defs><symbol id="b"><text>symbol text</text></symbol></svg></body>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			md, err := convertHTMLToMarkdown(tt.html, "text/html", testPageURL, store.DefaultMaxSourceBytes)
			require.ErrorIs(t, err, errNoReadableContent)
			assert.Empty(t, md)
		})
	}
}

func utf16LEWithBOM(s string) string {
	b := []byte{0xFF, 0xFE}
	for _, u := range utf16.Encode([]rune(s)) {
		b = append(b, byte(u), byte(u>>8))
	}
	return string(b)
}

func TestConvertHTMLToMarkdown_Charset(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		contentType string
		want        string
		wantErr     string
	}{
		{"content-type windows-1252", "<p>caf\xe9 cr\xe8me</p>", "text/html; charset=windows-1252", "café crème", ""},
		{"content-type iso-8859-1 uppercase", "<p>na\xefve</p>", "TEXT/HTML; Charset=ISO-8859-1", "naïve", ""},
		{"meta charset", `<html><head><meta charset="windows-1252"></head><body><p>r` + "\xe9sum\xe9" + `</p></body></html>`, "text/html", "résumé", ""},
		{"meta http-equiv", `<html><head><meta http-equiv="Content-Type" content="text/html; charset=iso-8859-1"></head><body><p>d` + "\xe9j\xe0" + `</p></body></html>`, "text/html", "déjà", ""},
		{"meta http-equiv single-quoted", `<html><head><meta http-equiv="content-type" content="text/html; charset='windows-1252'"></head><body><p>caf` + "\xe9" + `</p></body></html>`, "text/html", "café", ""},
		{"meta utf-16 means utf-8", `<html><head><meta charset="utf-16"></head><body><p>ok é</p></body></html>`, "text/html", "ok é", ""},
		{"utf-16le BOM overrides header", utf16LEWithBOM("<p>wide café</p>"), "text/html; charset=windows-1252", "wide café", ""},
		{"utf-8 BOM stripped", "\xEF\xBB\xBF<p>bom text</p>", "text/html", "bom text", ""},
		{"undeclared passes through as utf-8", "<p>plain é</p>", "text/html", "plain é", ""},
		{"unknown label is not echoed", "<p>x</p>", "text/html; charset=x" + syntheticGitHubToken, "", "unsupported HTML charset declared in Content-Type"},
		{"unknown meta label is not echoed", `<html><head><meta charset="x-made-up"></head><body><p>x</p></body></html>`, "text/html", "", "unsupported HTML charset declared in <meta>"},
		{"replacement label is not echoed", "<p>x</p>", "text/html; charset=iso-2022-kr", "", "unsupported HTML charset declared in Content-Type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			md, err := convertHTMLToMarkdown(tt.body, tt.contentType, testPageURL, store.DefaultMaxSourceBytes)
			if tt.wantErr != "" {
				// Exact match: no remote label may be appended.
				require.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Contains(t, md, tt.want)
			assert.NotContains(t, md, "\uFEFF")
		})
	}
}

func fetchSourceInfo(t *testing.T, srv *Server, key string) store.SourceInfo {
	t.Helper()
	sources, err := srv.getStore().ListSources()
	require.NoError(t, err)
	for _, s := range sources {
		if s.Label == key {
			return s
		}
	}
	t.Fatalf("source %q not indexed", key)
	return store.SourceInfo{}
}

func htmlServer(t *testing.T, contentType string, body *string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		fmt.Fprint(w, *body)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// A converted short page is indexed as Markdown, so its only heading stays the
// section title, and preview and stored chunks carry the same redacted text.
func TestFetchReadablePage_IndexedAsMarkdownAndSanitized(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch=%t", batch), func(t *testing.T) {
			disableSSRFValidation(t)
			page := `<html><head><title>Ignored</title></head><body><nav>menu wombat</nav>
<main><h1>Quokka Reference</h1><p>Token ` + syntheticGitHubToken + ` rotates daily.</p>
<private><p>private sentinel</p></private></main></body></html>`
			ts := htmlServer(t, "text/html; charset=utf-8", &page)
			srv := newTestServer(t, nil)
			r := callFetchAndIndex(t, srv, fetchArgs(ts.URL, batch, true))
			require.False(t, r.IsError, resultText(r))
			text := resultText(r)
			assert.Contains(t, text, "Quokka Reference")
			// Tree-level placeholders pass through Markdown escaping.
			assert.Contains(t, text, `REDACTED\_SECRET`)
			assert.Contains(t, text, `REDACTED]`)
			assert.NotContains(t, text, syntheticGitHubToken)
			assert.NotContains(t, text, "private sentinel")
			assert.NotContains(t, text, "wombat")

			info := fetchSourceInfo(t, srv, composeFetchCacheKey("freshness", ts.URL))
			assert.Equal(t, "markdown", info.ContentType)
			chunks, err := srv.getStore().GetChunksBySource(info.ID)
			require.NoError(t, err)
			require.NotEmpty(t, chunks)
			assert.Equal(t, "Quokka Reference", chunks[0].Title)
			var stored strings.Builder
			for _, c := range chunks {
				stored.WriteString(c.Content)
			}
			assert.NotContains(t, stored.String(), syntheticGitHubToken)
			assert.NotContains(t, stored.String(), "private sentinel")
			assert.NotContains(t, stored.String(), "wombat")
		})
	}
}

// Revalidating a good source against an application shell reports the outcome
// and keeps the retained hash, kind and validation clock. In a batch, a later
// readable URL still indexes.
func TestFetchReadablePage_EmptyShellKeepsGoodSource(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch=%t", batch), func(t *testing.T) {
			disableSSRFValidation(t)
			page := `<html><body><main><h1>Manual</h1><p>Original wombat manual.</p></main></body></html>`
			ts := htmlServer(t, "text/html", &page)
			good := `<body><p>sibling article</p></body>`
			sibling := htmlServer(t, "text/html", &good)
			srv := newTestServer(t, nil)
			srv.config.Store.Cache.FetchTTLHours = 1
			clock := time.Now()
			srv.fetchNow = func() time.Time { return clock }
			key := composeFetchCacheKey("freshness", ts.URL)

			r := callFetchAndIndex(t, srv, fetchArgs(ts.URL, batch, false))
			require.False(t, r.IsError, resultText(r))
			before := fetchSourceInfo(t, srv, key)
			beforeMeta, err := srv.getStore().GetSourceMeta(key)
			require.NoError(t, err)

			page = `<html><head><title>App</title></head><body><div id="root"></div><script>boot()</script></body></html>`
			clock = clock.Add(2 * time.Hour)
			args := fetchArgs(ts.URL, batch, false)
			if batch {
				args["requests"] = []any{
					map[string]any{"url": ts.URL, "source": "freshness"},
					map[string]any{"url": sibling.URL, "source": "sibling"},
				}
			}
			r = callFetchAndIndex(t, srv, args)
			text := resultText(r)
			if batch {
				require.False(t, r.IsError, text)
				assert.Contains(t, text, "✗ "+ts.URL+" — no readable content")
				assert.Contains(t, text, "✓ sibling — indexed")
			} else {
				require.True(t, r.IsError)
				assert.Contains(t, text, "No readable content in "+ts.URL)
			}
			assert.Contains(t, text, "previously indexed copy and its validation time are unchanged")

			after := fetchSourceInfo(t, srv, key)
			afterMeta, err := srv.getStore().GetSourceMeta(key)
			require.NoError(t, err)
			assert.Equal(t, before.ContentHash, after.ContentHash)
			assert.Equal(t, store.KindDurable, after.Kind)
			assert.Equal(t, beforeMeta, afterMeta, "validation clock and kind must not move")

			results, err := srv.getStore().SearchWithFallback("wombat", 5, store.SearchOptions{Source: "freshness"})
			require.NoError(t, err)
			require.NotEmpty(t, results, "the last good copy must stay searchable")
		})
	}
}

// UTF-16 XHTML passes the binary gate and is decoded by the HTML path in both
// fetch modes.
func TestFetchReadablePage_UTF16XHTML(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch=%t", batch), func(t *testing.T) {
			disableSSRFValidation(t)
			page := utf16LEWithBOM(`<html xmlns="http://www.w3.org/1999/xhtml"><body><p>wide numbat text</p></body></html>`)
			ts := htmlServer(t, "application/xhtml+xml; charset=utf-16", &page)
			srv := newTestServer(t, nil)
			r := callFetchAndIndex(t, srv, fetchArgs(ts.URL, batch, true))
			require.False(t, r.IsError, resultText(r))
			assert.Contains(t, resultText(r), "wide numbat text")
			assert.NotContains(t, resultText(r), "✗")
		})
	}
}
