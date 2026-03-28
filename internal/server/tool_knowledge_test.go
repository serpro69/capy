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
	indexTestContent(t, srv)

	r := callSearch(t, srv, map[string]any{
		"queries": []any{"xyznonexistentterm123"},
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "No results found")
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

func callFetchAndIndex(t *testing.T, srv *Server, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	result, err := srv.handleFetchAndIndex(context.Background(), req)
	require.NoError(t, err)
	return result
}

func TestFetchAndIndex_HTML(t *testing.T) {
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
