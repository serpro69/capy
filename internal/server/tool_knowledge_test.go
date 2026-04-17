package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── Index tests ───────────────────────────────────────────────────────────────

func callIndex(t *testing.T, srv *Server, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	result, err := srv.handleIndex(context.Background(), req)
	require.NoError(t, err)
	return result
}

func TestIndex_Content(t *testing.T) {
	srv := newTestServer(t, nil)
	r := callIndex(t, srv, map[string]any{
		"content": "# Hello\n\nSome documentation content here.\n\n## Section 2\n\nMore content.",
		"source":  "test-docs",
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "Indexed")
	assert.Contains(t, text, "test-docs")
	assert.Contains(t, text, "search(queries:")
}

func TestIndex_ContentAutoLabel(t *testing.T) {
	srv := newTestServer(t, nil)
	r := callIndex(t, srv, map[string]any{
		"content": "Some plain text content",
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "indexed-content")
}

func TestIndex_Path(t *testing.T) {
	srv := newTestServer(t, nil)

	// Write a temp file
	tmpFile := filepath.Join(t.TempDir(), "docs.md")
	err := os.WriteFile(tmpFile, []byte("# Test File\n\nFile content here.\n\n## Section\n\nMore."), 0644)
	require.NoError(t, err)

	r := callIndex(t, srv, map[string]any{
		"path":   tmpFile,
		"source": "file-docs",
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "Indexed")
	assert.Contains(t, text, "file-docs")
}

func TestIndex_PathAutoLabel(t *testing.T) {
	srv := newTestServer(t, nil)

	tmpFile := filepath.Join(t.TempDir(), "readme.md")
	err := os.WriteFile(tmpFile, []byte("# Readme\n\nContent."), 0644)
	require.NoError(t, err)

	r := callIndex(t, srv, map[string]any{
		"path": tmpFile,
	})
	assert.False(t, r.IsError)
	assert.Contains(t, resultText(r), "readme.md")
}

func TestIndex_NoInput(t *testing.T) {
	srv := newTestServer(t, nil)
	r := callIndex(t, srv, map[string]any{})
	assert.True(t, r.IsError)
	assert.Contains(t, resultText(r), "Either content or path must be provided")
}

func TestIndex_BadPath(t *testing.T) {
	srv := newTestServer(t, nil)
	r := callIndex(t, srv, map[string]any{
		"path": "/nonexistent/file.md",
	})
	assert.True(t, r.IsError)
	assert.Contains(t, resultText(r), "Failed to read file")
}

func TestIndex_StatsTracking(t *testing.T) {
	srv := newTestServer(t, nil)
	content := "# Test\n\nSome content."
	callIndex(t, srv, map[string]any{
		"content": content,
	})
	snap := srv.stats.Snapshot()
	assert.Equal(t, int64(len(content)), snap.BytesIndexed)
	assert.Equal(t, 1, snap.Calls["capy_index"])
}

// ─── Search tests ──────────────────────────────────────────────────────────────

func callSearch(t *testing.T, srv *Server, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	result, err := srv.handleSearch(context.Background(), req)
	require.NoError(t, err)
	return result
}

func TestSearch_EmptyIndex(t *testing.T) {
	srv := newTestServer(t, nil)
	r := callSearch(t, srv, map[string]any{
		"queries": []any{"anything"},
	})
	assert.True(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "knowledge base is empty")
	assert.Contains(t, text, "capy_batch_execute")
	assert.Contains(t, text, "capy_fetch_and_index")
	assert.Contains(t, text, "capy_index")
}

func indexTestContent(t *testing.T, srv *Server) {
	t.Helper()
	content := "# React Hooks\n\nuseEffect runs side effects in functional components.\n\n## useState\n\nuseState manages local state in components.\n\n## useCallback\n\nuseCallback memoizes callback functions to prevent unnecessary re-renders."
	r := callIndex(t, srv, map[string]any{
		"content": content,
		"source":  "react-docs",
	})
	require.False(t, r.IsError)
}

func TestSearch_BasicQuery(t *testing.T) {
	srv := newTestServer(t, nil)
	indexTestContent(t, srv)

	r := callSearch(t, srv, map[string]any{
		"queries": []any{"useState local state"},
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "useState")
}

func TestSearch_MultipleQueries(t *testing.T) {
	srv := newTestServer(t, nil)
	indexTestContent(t, srv)

	r := callSearch(t, srv, map[string]any{
		"queries": []any{"useEffect side effects", "useCallback memoize"},
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "useEffect")
	assert.Contains(t, text, "useCallback")
}

func TestSearch_SingleQueryString(t *testing.T) {
	srv := newTestServer(t, nil)
	indexTestContent(t, srv)

	// Accept "query" (string) as alternative to "queries" (array)
	r := callSearch(t, srv, map[string]any{
		"query": "useState",
	})
	assert.False(t, r.IsError)
	assert.Contains(t, resultText(r), "useState")
}

func TestSearch_CoerceStringArray(t *testing.T) {
	srv := newTestServer(t, nil)
	indexTestContent(t, srv)

	// Double-serialized JSON array
	r := callSearch(t, srv, map[string]any{
		"queries": `["useEffect side effects"]`,
	})
	assert.False(t, r.IsError)
	assert.Contains(t, resultText(r), "useEffect")
}

func TestSearch_SourceFilter(t *testing.T) {
	srv := newTestServer(t, nil)
	indexTestContent(t, srv)

	// Index a second source
	callIndex(t, srv, map[string]any{
		"content": "# Vue Composition API\n\nref and reactive manage state in Vue.",
		"source":  "vue-docs",
	})

	// Search scoped to react-docs
	r := callSearch(t, srv, map[string]any{
		"queries": []any{"state management"},
		"source":  "react-docs",
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "react-docs")
}

func TestSearch_NoQueries(t *testing.T) {
	srv := newTestServer(t, nil)
	r := callSearch(t, srv, map[string]any{})
	assert.True(t, r.IsError)
	assert.Contains(t, resultText(r), "provide query or queries")
}

func TestSearch_NoResults_ShowsSources(t *testing.T) {
	srv := newTestServer(t, nil)
	indexTestContent(t, srv) // seeds a durable "react-docs" source

	r := callSearch(t, srv, map[string]any{
		"queries": []any{"xyznonexistentterm123"},
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "No results found")
	assert.Contains(t, text, "Indexed sources:", "must show the source list when all queries return empty")
	assert.Contains(t, text, "react-docs", "must name the indexed durable source in the fallback listing")
}

func TestSearch_NoResults_HidesEphemeralSources(t *testing.T) {
	srv := newTestServer(t, nil)
	seedMixedKindCorpus(t, srv) // seeds one durable (k8s-docs) and one ephemeral (execute:shell)

	// extractIndexedSourcesSection returns the text after "Indexed sources:" so that
	// assertions target only the fallback listing, not the ephemeral-recovery hint
	// (which names "execute:shell" as an example).
	extract := func(text string) string {
		_, after, ok := strings.Cut(text, "Indexed sources:")
		if !ok {
			return ""
		}
		return after
	}

	// Default call: ephemeral is excluded from both search and the fallback listing.
	r := callSearch(t, srv, map[string]any{
		"queries": []any{"xyznonexistentterm123"},
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	listing := extract(text)
	require.NotEmpty(t, listing, "fallback listing must be present")
	assert.Contains(t, listing, "k8s-docs", "durable source must appear in fallback listing")
	assert.NotContains(t, listing, "execute:shell", "ephemeral source must NOT appear in the listing by default")

	// Opting into ephemeral lists both kinds.
	r = callSearch(t, srv, map[string]any{
		"queries":       []any{"xyznonexistentterm123"},
		"include_kinds": []any{"durable", "ephemeral"},
	})
	assert.False(t, r.IsError)
	listing = extract(resultText(r))
	require.NotEmpty(t, listing)
	assert.Contains(t, listing, "k8s-docs")
	assert.Contains(t, listing, "execute:shell", "opting into ephemeral surfaces it in the fallback listing")
}

// seedMixedKindCorpus seeds the store with one durable and one ephemeral source,
// both matching the query "kubernetes pods".
func seedMixedKindCorpus(t *testing.T, srv *Server) {
	t.Helper()
	st := srv.getStore()
	_, err := st.Index(
		"# Kubernetes Reference\n\nkubernetes orchestrates containers across nodes.\n\n"+
			"## Pods\n\nA pod is the smallest deployable unit in kubernetes.",
		"k8s-docs",
		"markdown",
		store.KindDurable,
	)
	require.NoError(t, err)

	_, err = st.Index(
		"# kubectl get pods output\n\nkubernetes cluster shows running pods here.",
		"execute:shell",
		"markdown",
		store.KindEphemeral,
	)
	require.NoError(t, err)
}

func TestSearch_DefaultExcludesEphemeral(t *testing.T) {
	srv := newTestServer(t, nil)
	seedMixedKindCorpus(t, srv)

	r := callSearch(t, srv, map[string]any{
		"queries": []any{"kubernetes pods"},
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "k8s-docs", "default search must surface durable sources")
	assert.NotContains(t, text, "execute:shell", "default search must hide ephemeral sources")
}

func TestSearch_IncludeKindsEphemeralOnly(t *testing.T) {
	srv := newTestServer(t, nil)
	seedMixedKindCorpus(t, srv)

	r := callSearch(t, srv, map[string]any{
		"queries":       []any{"kubernetes pods"},
		"include_kinds": []any{"ephemeral"},
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "execute:shell", "ephemeral-only filter must surface execute:shell")
	assert.NotContains(t, text, "k8s-docs", "ephemeral-only filter must hide durable sources")
}

func TestSearch_IncludeKindsBoth(t *testing.T) {
	srv := newTestServer(t, nil)
	seedMixedKindCorpus(t, srv)

	r := callSearch(t, srv, map[string]any{
		"queries":       []any{"kubernetes pods"},
		"include_kinds": []any{"durable", "ephemeral"},
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "k8s-docs")
	assert.Contains(t, text, "execute:shell")
}

func TestSearch_IncludeKindsRejectsUnknown(t *testing.T) {
	srv := newTestServer(t, nil)
	seedMixedKindCorpus(t, srv)

	r := callSearch(t, srv, map[string]any{
		"queries":       []any{"kubernetes"},
		"include_kinds": []any{"durable", "scratch"},
	})
	assert.True(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "scratch", "error must name the offending value")
	assert.Contains(t, text, "durable", "error must name the accepted set")
	assert.Contains(t, text, "ephemeral", "error must name the accepted set")
}

func TestSearch_ZeroResultsNamesRecoveryPaths(t *testing.T) {
	srv := newTestServer(t, nil)
	seedMixedKindCorpus(t, srv)

	// Query matches ONLY the ephemeral row (kubectl is unique to it).
	r := callSearch(t, srv, map[string]any{
		"queries": []any{"kubectl"},
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "No results found")
	// Both recovery paths must be named.
	assert.Contains(t, text, `include_kinds: ["durable","ephemeral"]`)
	assert.Contains(t, text, `source: "execute:`)
	assert.Contains(t, text, "ephemeral source(s) present but excluded")
}

// Session-recovery journey: capy_execute writes ephemeral content via intent
// search; default capy_search excludes it; include_kinds: ["ephemeral"] recovers it.
func TestSearch_SessionRecoveryJourney(t *testing.T) {
	srv := newTestServer(t, nil)

	// Generate >5KB of output containing the searchable phrase so intent search
	// triggers and writes the content to the store as ephemeral.
	code := `for i in $(seq 1 200); do echo "line ${i} contains kryptonite-marker payload data here"; done`
	r := callTool(t, srv, map[string]any{
		"language": "shell",
		"code":     code,
		"intent":   "kryptonite-marker payload",
	})
	require.False(t, r.IsError, "execute should succeed: %s", resultText(r))

	// Assert on observable store state rather than intent-path output text —
	// avoids coupling to the intent-search threshold or its output format.
	ephemeralCount, err := srv.getStore().CountSourcesByKind(store.KindEphemeral)
	require.NoError(t, err)
	require.Greater(t, ephemeralCount, 0, "intent path must have indexed ephemeral content")

	// Default search excludes ephemeral — must return zero results AND name both recovery paths.
	r = callSearch(t, srv, map[string]any{
		"queries": []any{"kryptonite-marker payload"},
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "No results found")
	assert.Contains(t, text, `include_kinds: ["durable","ephemeral"]`)
	assert.Contains(t, text, `source: "execute:`)

	// Same query with include_kinds: ["ephemeral"] must surface the ephemeral hit.
	r = callSearch(t, srv, map[string]any{
		"queries":       []any{"kryptonite-marker payload"},
		"include_kinds": []any{"ephemeral"},
	})
	assert.False(t, r.IsError)
	text = resultText(r)
	assert.Contains(t, text, "kryptonite-marker", "ephemeral content must appear when include_kinds=ephemeral")
}

func TestSearch_ProgressiveThrottling(t *testing.T) {
	srv := newTestServer(t, nil)
	indexTestContent(t, srv)

	// Make 4 calls — should get throttle warning on 3rd+
	for i := range 3 {
		callSearch(t, srv, map[string]any{
			"queries": []any{fmt.Sprintf("query %d", i)},
		})
	}

	r := callSearch(t, srv, map[string]any{
		"queries": []any{"useEffect"},
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "search call #4")
	assert.Contains(t, text, "Results limited to 1/query")
}

func TestSearch_ThrottleBlock(t *testing.T) {
	srv := newTestServer(t, nil)
	indexTestContent(t, srv)

	// Make 9 calls — should be blocked
	for i := range 8 {
		callSearch(t, srv, map[string]any{
			"queries": []any{fmt.Sprintf("query %d", i)},
		})
	}

	r := callSearch(t, srv, map[string]any{
		"queries": []any{"useEffect"},
	})
	assert.True(t, r.IsError)
	assert.Contains(t, resultText(r), "BLOCKED")
}

func TestSearch_OutputCap(t *testing.T) {
	srv := newTestServer(t, nil)

	// Index a large document with big sections that exceed snippet limits
	var b strings.Builder
	for i := range 50 {
		fmt.Fprintf(&b, "# Section %d\n\n", i)
		// Each section ~4KB so snippets are 1500 bytes each
		for j := range 40 {
			fmt.Fprintf(&b, "Line %d of section %d with unique keyword_%d data content.\n", j, i, i)
		}
		b.WriteString("\n")
	}
	callIndex(t, srv, map[string]any{
		"content": b.String(),
		"source":  "big-docs",
	})

	// Many queries to trigger the 40KB cap
	queries := make([]any, 30)
	for i := range queries {
		queries[i] = fmt.Sprintf("keyword_%d data content", i)
	}
	r := callSearch(t, srv, map[string]any{
		"queries": queries,
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "output cap reached")
}

// ─── Fetch and Index tests ─────────────────────────────────────────────────────

// disableSSRFValidation disables SSRF checks for tests using httptest.NewServer (localhost).
func disableSSRFValidation(t *testing.T) {
	t.Helper()
	orig := validateFetchURLFunc
	validateFetchURLFunc = func(string) error { return nil }
	t.Cleanup(func() { validateFetchURLFunc = orig })
}

func callFetchAndIndex(t *testing.T, srv *Server, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	result, err := srv.handleFetchAndIndex(context.Background(), req)
	require.NoError(t, err)
	return result
}

func TestFetchAndIndex_HTML(t *testing.T) {
	disableSSRFValidation(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html>
<html>
<head><title>Test</title></head>
<body>
<nav><a href="/">Home</a></nav>
<h1>Documentation</h1>
<p>This is test documentation content.</p>
<h2>API Reference</h2>
<p>The API supports GET and POST requests.</p>
<script>console.log('should be removed')</script>
<footer>Copyright 2024</footer>
</body>
</html>`)
	}))
	defer ts.Close()

	srv := newTestServer(t, nil)
	r := callFetchAndIndex(t, srv, map[string]any{
		"url":    ts.URL,
		"source": "test-html",
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "sections")
	assert.Contains(t, text, "test-html")
	assert.Contains(t, text, "Documentation")
	// Script/nav/footer content should be stripped
	assert.NotContains(t, text, "console.log")
	assert.NotContains(t, text, "Copyright 2024")
}

func TestFetchAndIndex_JSON(t *testing.T) {
	disableSSRFValidation(t)
	data := map[string]any{
		"name":    "capy",
		"version": "1.0.0",
		"config": map[string]any{
			"timeout": 30,
			"retries": 3,
		},
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(data)
	}))
	defer ts.Close()

	srv := newTestServer(t, nil)
	r := callFetchAndIndex(t, srv, map[string]any{
		"url":    ts.URL,
		"source": "test-json",
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "sections")
	assert.Contains(t, text, "test-json")
}

func TestFetchAndIndex_PlainText(t *testing.T) {
	disableSSRFValidation(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "Line 1: plain text content\nLine 2: more content\nLine 3: even more")
	}))
	defer ts.Close()

	srv := newTestServer(t, nil)
	r := callFetchAndIndex(t, srv, map[string]any{
		"url": ts.URL,
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "sections")
	assert.Contains(t, text, "plain text content")
}

func TestFetchAndIndex_Preview(t *testing.T) {
	disableSSRFValidation(t)
	// Large content — verify preview is truncated
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, strings.Repeat("A long line of text. ", 200))
	}))
	defer ts.Close()

	srv := newTestServer(t, nil)
	r := callFetchAndIndex(t, srv, map[string]any{
		"url": ts.URL,
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "truncated")
	assert.Contains(t, text, "search()")
}

func TestFetchAndIndex_MissingURL(t *testing.T) {
	srv := newTestServer(t, nil)
	r := callFetchAndIndex(t, srv, map[string]any{})
	assert.True(t, r.IsError)
	assert.Contains(t, resultText(r), "Missing required parameter: url")
}

func TestFetchAndIndex_HTTPError(t *testing.T) {
	disableSSRFValidation(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	srv := newTestServer(t, nil)
	r := callFetchAndIndex(t, srv, map[string]any{
		"url": ts.URL,
	})
	assert.True(t, r.IsError)
	assert.Contains(t, resultText(r), "HTTP 404")
}

func TestFetchAndIndex_EmptyBody(t *testing.T) {
	disableSSRFValidation(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		// Write nothing
	}))
	defer ts.Close()

	srv := newTestServer(t, nil)
	r := callFetchAndIndex(t, srv, map[string]any{
		"url": ts.URL,
	})
	assert.True(t, r.IsError)
	assert.Contains(t, resultText(r), "empty content")
}

func TestFetchAndIndex_AutoSource(t *testing.T) {
	disableSSRFValidation(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "content")
	}))
	defer ts.Close()

	srv := newTestServer(t, nil)
	r := callFetchAndIndex(t, srv, map[string]any{
		"url": ts.URL,
	})
	assert.False(t, r.IsError)
	// Should use URL as source label
	assert.Contains(t, resultText(r), ts.URL)
}

func TestFetchAndIndex_StatsTracking(t *testing.T) {
	disableSSRFValidation(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "some content for stats")
	}))
	defer ts.Close()

	srv := newTestServer(t, nil)
	callFetchAndIndex(t, srv, map[string]any{
		"url": ts.URL,
	})
	snap := srv.stats.Snapshot()
	assert.Greater(t, snap.BytesIndexed, int64(0))
	assert.Equal(t, 1, snap.Calls["capy_fetch_and_index"])
}

// ─── HTML to Markdown conversion tests ─────────────────────────────────────────

func TestConvertHTMLToMarkdown_Basic(t *testing.T) {
	html := `<h1>Title</h1><p>Paragraph text.</p>`
	md, err := convertHTMLToMarkdown(html)
	require.NoError(t, err)
	assert.Contains(t, md, "Title")
	assert.Contains(t, md, "Paragraph text")
}

func TestConvertHTMLToMarkdown_StripsElements(t *testing.T) {
	html := `
<html>
<body>
<nav><a href="/">Nav Link</a></nav>
<header><h1>Site Header</h1></header>
<main><p>Main content</p></main>
<footer><p>Footer text</p></footer>
<script>alert('xss')</script>
<style>body { color: red }</style>
</body>
</html>`

	md, err := convertHTMLToMarkdown(html)
	require.NoError(t, err)
	assert.Contains(t, md, "Main content")
	assert.NotContains(t, md, "Nav Link")
	assert.NotContains(t, md, "Site Header")
	assert.NotContains(t, md, "Footer text")
	assert.NotContains(t, md, "alert")
	assert.NotContains(t, md, "color: red")
}

func TestConvertHTMLToMarkdown_PreservesCodeBlocks(t *testing.T) {
	html := `<pre><code>func main() {
    fmt.Println("hello")
}</code></pre>`

	md, err := convertHTMLToMarkdown(html)
	require.NoError(t, err)
	assert.Contains(t, md, "func main()")
	assert.Contains(t, md, "fmt.Println")
}

func TestConvertHTMLToMarkdown_GFMTables(t *testing.T) {
	html := `<table>
<thead><tr><th>Name</th><th>Age</th></tr></thead>
<tbody>
<tr><td>Alice</td><td>30</td></tr>
<tr><td>Bob</td><td>25</td></tr>
</tbody>
</table>`

	md, err := convertHTMLToMarkdown(html)
	require.NoError(t, err)
	assert.Contains(t, md, "Name")
	assert.Contains(t, md, "Alice")
	assert.Contains(t, md, "Bob")
	assert.Contains(t, md, "|")   // GFM pipe table syntax
	assert.Contains(t, md, "---") // GFM separator row
}

func TestConvertHTMLToMarkdown_StripsNoscript(t *testing.T) {
	html := `<p>Visible content</p><noscript><p>JavaScript required</p></noscript>`

	md, err := convertHTMLToMarkdown(html)
	require.NoError(t, err)
	assert.Contains(t, md, "Visible content")
	assert.NotContains(t, md, "JavaScript required")
}

func TestConvertHTMLToMarkdown_MalformedHTML(t *testing.T) {
	// Unclosed tags, missing elements — should not error
	html := `<h1>Title<p>No closing h1<div><span>Deeply nested</p></div>`

	md, err := convertHTMLToMarkdown(html)
	require.NoError(t, err)
	assert.Contains(t, md, "Title")
	assert.Contains(t, md, "Deeply nested")
}

func TestConvertHTMLToMarkdown_EmptyInput(t *testing.T) {
	md, err := convertHTMLToMarkdown("")
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(md))
}

func TestIsBinaryContent(t *testing.T) {
	t.Run("image content type", func(t *testing.T) {
		assert.True(t, isBinaryContent("image/png", []byte("PNG data")))
	})

	t.Run("pdf content type", func(t *testing.T) {
		assert.True(t, isBinaryContent("application/pdf", []byte("%PDF-1.4")))
	})

	t.Run("octet-stream", func(t *testing.T) {
		assert.True(t, isBinaryContent("application/octet-stream", []byte{0x00, 0x01}))
	})

	t.Run("html is not binary", func(t *testing.T) {
		assert.False(t, isBinaryContent("text/html", []byte("<html><body>Hello</body></html>")))
	})

	t.Run("json is not binary", func(t *testing.T) {
		assert.False(t, isBinaryContent("application/json", []byte(`{"key": "value"}`)))
	})

	t.Run("null bytes in body with non-text type", func(t *testing.T) {
		body := []byte("data\x00with\x00nulls")
		assert.True(t, isBinaryContent("application/json", body))
	})

	t.Run("clean text body", func(t *testing.T) {
		assert.False(t, isBinaryContent("text/plain", []byte("clean text content")))
	})

	t.Run("content type with charset parameter", func(t *testing.T) {
		assert.True(t, isBinaryContent("image/png; name=photo.png", []byte("PNG")))
		assert.False(t, isBinaryContent("text/html; charset=utf-8", []byte("<html>")))
	})

	t.Run("uppercase content type", func(t *testing.T) {
		assert.True(t, isBinaryContent("APPLICATION/PDF", []byte("%PDF")))
	})

	t.Run("text type skips null-byte check to avoid UTF-16 false positive", func(t *testing.T) {
		// UTF-16LE "Hi" = 0x48 0x00 0x69 0x00 — contains null bytes
		utf16Body := []byte{0x48, 0x00, 0x69, 0x00}
		assert.False(t, isBinaryContent("text/html; charset=utf-16", utf16Body))
		assert.False(t, isBinaryContent("text/plain", utf16Body))
	})

	t.Run("null byte beyond 512-byte window", func(t *testing.T) {
		body := make([]byte, 1024)
		for i := range body[:600] {
			body[i] = 'A'
		}
		body[600] = 0x00
		assert.False(t, isBinaryContent("application/json", body))
	})

	t.Run("empty body", func(t *testing.T) {
		assert.False(t, isBinaryContent("application/json", []byte{}))
	})
}

func TestFetchAndIndex_BinaryContent(t *testing.T) {
	disableSSRFValidation(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte{0x89, 0x50, 0x4E, 0x47}) // PNG magic bytes
	}))
	defer ts.Close()

	srv := newTestServer(t, nil)
	r := callFetchAndIndex(t, srv, map[string]any{"url": ts.URL, "source": "test-binary"})
	assert.True(t, r.IsError)
	assert.Contains(t, resultText(r), "binary content")
}

// ─── TTL cache tests ─────────────────────────────────────────────────────────

func TestFetchAndIndex_CacheHit(t *testing.T) {
	disableSSRFValidation(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "cacheable content for testing")
	}))
	defer ts.Close()

	srv := newTestServer(t, nil)

	// First call — should fetch and index
	r := callFetchAndIndex(t, srv, map[string]any{"url": ts.URL, "source": "cache-test"})
	assert.False(t, r.IsError)
	assert.Contains(t, resultText(r), "sections")

	// Second call within TTL — should return cache hit
	r = callFetchAndIndex(t, srv, map[string]any{"url": ts.URL, "source": "cache-test"})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "Cache hit")
	assert.Contains(t, text, "cache-test")

	// Stats should track the cache hit and estimated bytes saved
	snap := srv.stats.Snapshot()
	assert.Equal(t, int64(1), snap.CacheHits)
	assert.Greater(t, snap.CacheBytesSaved, int64(0), "cache bytes saved should be estimated from chunk count")
}

func TestFetchAndIndex_ForceBypassesCache(t *testing.T) {
	disableSSRFValidation(t)
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "content version %d", calls)
	}))
	defer ts.Close()

	srv := newTestServer(t, nil)

	// First call
	callFetchAndIndex(t, srv, map[string]any{"url": ts.URL, "source": "force-test"})
	assert.Equal(t, 1, calls)

	// Second call with force — should re-fetch
	r := callFetchAndIndex(t, srv, map[string]any{"url": ts.URL, "source": "force-test", "force": true})
	assert.False(t, r.IsError)
	assert.Equal(t, 2, calls)
	assert.NotContains(t, resultText(r), "Cache hit")
}

func TestFetchAndIndex_ExpiredCacheRefetches(t *testing.T) {
	disableSSRFValidation(t)
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "content version %d", calls)
	}))
	defer ts.Close()

	srv := newTestServer(t, nil)
	// Set TTL to 0 so everything is immediately expired
	srv.config.Store.Cache.FetchTTLHours = 0

	callFetchAndIndex(t, srv, map[string]any{"url": ts.URL, "source": "expire-test"})
	assert.Equal(t, 1, calls)

	// Second call — TTL is 0h so cache is expired, should re-fetch
	r := callFetchAndIndex(t, srv, map[string]any{"url": ts.URL, "source": "expire-test"})
	assert.False(t, r.IsError)
	assert.Equal(t, 2, calls)
	assert.NotContains(t, resultText(r), "Cache hit")
}

// ─── SSRF validation tests ────────────────────────────────────────────────────

func TestValidateFetchURL(t *testing.T) {
	t.Run("public URL allowed", func(t *testing.T) {
		// Use a well-known public hostname
		assert.NoError(t, validateFetchURL("https://example.com/page"))
	})

	t.Run("localhost blocked", func(t *testing.T) {
		err := validateFetchURL("http://localhost:8080/admin")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "local/private")
	})

	t.Run("127.0.0.1 blocked", func(t *testing.T) {
		err := validateFetchURL("http://127.0.0.1:9090/internal")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "local/private")
	})

	t.Run("metadata endpoint blocked", func(t *testing.T) {
		err := validateFetchURL("http://169.254.169.254/latest/meta-data/")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "local/private")
	})

	t.Run("invalid URL returns error", func(t *testing.T) {
		err := validateFetchURL("://not-a-url")
		assert.Error(t, err)
	})

	t.Run("unresolvable hostname allowed through", func(t *testing.T) {
		// Can't resolve → allow (HTTP client will fail with better error)
		assert.NoError(t, validateFetchURL("http://this-host-does-not-exist-12345.invalid/path"))
	})
}

func TestFetchAndIndex_BinaryBodyWithTextContentType(t *testing.T) {
	disableSSRFValidation(t)
	// Misconfigured server sends binary data with text Content-Type
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-custom")
		w.Write([]byte{0x00, 0x01, 0x02, 0x03}) // null bytes
	}))
	defer ts.Close()

	srv := newTestServer(t, nil)
	r := callFetchAndIndex(t, srv, map[string]any{"url": ts.URL, "source": "test-null-bytes"})
	assert.True(t, r.IsError)
	assert.Contains(t, resultText(r), "binary content")
}
