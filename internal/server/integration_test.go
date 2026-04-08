package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── 16.1 MCP server integration tests ────────────────────────────────────────
//
// These tests exercise the full pipeline: MCP tool handler → executor → store
// indexing → search retrieval, verifying that the components work together
// end-to-end rather than in isolation.

func TestIntegration_ExecuteWithIntent_AutoIndexAndSearch(t *testing.T) {
	srv := newTestServer(t, nil)

	// Execute a command that produces >5KB output with an intent
	// The intent search mechanism should auto-index and return section titles
	lines := strings.Repeat("important data: configuration loaded successfully\n", 150) // ~7.5KB
	r := callTool(t, srv, map[string]any{
		"language": "shell",
		"code":     "echo '" + lines + "'",
		"intent":   "configuration loaded",
	})
	require.False(t, r.IsError)
	text := resultText(r)

	// Should have been auto-indexed (not raw output)
	assert.Contains(t, text, "Indexed")
	assert.Contains(t, text, "knowledge base")

	// Now search the knowledge base for the auto-indexed content
	r2 := callSearch(t, srv, map[string]any{
		"queries": []any{"configuration loaded"},
	})
	require.False(t, r2.IsError)
	text2 := resultText(r2)
	assert.Contains(t, text2, "configuration")
}

func TestIntegration_BatchExecute_MultiCommandSearchRoundTrip(t *testing.T) {
	srv := newTestServer(t, nil)

	// Batch execute: multiple commands producing distinct content, then search
	r := callBatch(t, srv, map[string]any{
		"commands": []any{
			map[string]any{"label": "Go Version", "command": "go version"},
			map[string]any{"label": "Env Vars", "command": "echo 'DATABASE_URL=postgres://localhost:5432/mydb\nREDIS_URL=redis://localhost:6379'"},
		},
		"queries": []any{"go version", "database connection"},
	})
	require.False(t, r.IsError)
	text := resultText(r)

	// Should show execution summary
	assert.Contains(t, text, "Executed 2 commands")
	assert.Contains(t, text, "Indexed")

	// Should contain search results for both queries
	assert.Contains(t, text, "go version")
	assert.Contains(t, text, "## go version")
	assert.Contains(t, text, "## database connection")
}

func TestIntegration_IndexAndSearchRoundTrip(t *testing.T) {
	srv := newTestServer(t, nil)

	// Index structured markdown documentation
	content := `# Capy Architecture

## Executor

The PolyglotExecutor runs code in sandboxed subprocesses. It supports shell,
python, javascript, and other runtimes. Each execution is isolated.

## Store

The ContentStore uses SQLite FTS5 for full-text search with BM25 ranking.
Content is chunked by markdown headers and code blocks.

## Search Pipeline

Search uses a three-tier fallback:
1. Porter stemming (default FTS5)
2. Trigram substring matching
3. Fuzzy Levenshtein correction for typos
`
	r := callIndex(t, srv, map[string]any{
		"content": content,
		"source":  "architecture-docs",
	})
	require.False(t, r.IsError)
	assert.Contains(t, resultText(r), "Indexed")
	assert.Contains(t, resultText(r), "architecture-docs")

	// Search for specific terms across sections
	r2 := callSearch(t, srv, map[string]any{
		"queries": []any{"FTS5 BM25 ranking", "PolyglotExecutor sandboxed"},
		"source":  "architecture-docs",
	})
	require.False(t, r2.IsError)
	text := resultText(r2)
	assert.Contains(t, text, "FTS5")
	assert.Contains(t, text, "BM25")

	// Verify source scoping works: search with wrong source yields nothing
	r3 := callSearch(t, srv, map[string]any{
		"queries": []any{"FTS5 BM25"},
		"source":  "nonexistent-source",
	})
	require.False(t, r3.IsError)
	assert.Contains(t, resultText(r3), "No results found")
}

func TestIntegration_IndexSearchMultipleSources(t *testing.T) {
	srv := newTestServer(t, nil)

	// Index two different sources
	callIndex(t, srv, map[string]any{
		"content": "# Go Programming\n\nGoroutines enable lightweight concurrency. Channels provide communication between goroutines.",
		"source":  "go-docs",
	})
	callIndex(t, srv, map[string]any{
		"content": "# Rust Programming\n\nOwnership ensures memory safety without garbage collection. Borrowing allows references without taking ownership.",
		"source":  "rust-docs",
	})

	// Search scoped to go-docs should find goroutines, not ownership
	r := callSearch(t, srv, map[string]any{
		"queries": []any{"concurrency"},
		"source":  "go-docs",
	})
	require.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "go-docs")

	// Search scoped to rust-docs should find ownership
	r2 := callSearch(t, srv, map[string]any{
		"queries": []any{"memory safety"},
		"source":  "rust-docs",
	})
	require.False(t, r2.IsError)
	assert.Contains(t, resultText(r2), "rust-docs")
}

func TestIntegration_StatsAccumulation(t *testing.T) {
	srv := newTestServer(t, nil)

	// Execute some commands
	callTool(t, srv, map[string]any{
		"language": "shell",
		"code":     "echo 'hello world'",
	})
	callIndex(t, srv, map[string]any{
		"content": "# Docs\n\nSome documentation.",
		"source":  "test-stats-docs",
	})
	callSearch(t, srv, map[string]any{
		"queries": []any{"documentation"},
	})

	// Check stats reflect all calls
	req := mcp.CallToolRequest{}
	r, err := srv.handleStats(context.Background(), req)
	require.NoError(t, err)
	text := resultText(r)

	assert.Contains(t, text, "Context Window Protection")
	assert.Contains(t, text, "capy_execute")
	assert.Contains(t, text, "capy_index")
	assert.Contains(t, text, "capy_search")
	assert.Contains(t, text, "Knowledge Base")
	assert.Contains(t, text, "Sources")

	// Verify stats counters
	snap := srv.stats.Snapshot()
	assert.Equal(t, 1, snap.Calls["capy_execute"])
	assert.Equal(t, 1, snap.Calls["capy_index"])
	assert.Equal(t, 1, snap.Calls["capy_search"])
	assert.Greater(t, snap.BytesSandboxed, int64(0))
	assert.Greater(t, snap.BytesIndexed, int64(0))
}

func TestIntegration_CleanupAfterIndex(t *testing.T) {
	srv := newTestServer(t, nil)

	// Index content
	callIndex(t, srv, map[string]any{
		"content": "# Test Content\n\nSome test data for cleanup.",
		"source":  "cleanup-test",
	})

	// Verify it's searchable
	r := callSearch(t, srv, map[string]any{
		"queries": []any{"test data cleanup"},
	})
	require.False(t, r.IsError)
	assert.NotContains(t, resultText(r), "No results found")

	// Dry-run cleanup — freshly indexed content has a high retention score,
	// so it should NOT be an eviction candidate.
	r2 := callCleanup(t, srv, map[string]any{})
	require.False(t, r2.IsError)
	text := resultText(r2)
	assert.Contains(t, text, "No evictable sources")
	_ = text
}

// ─── 16.3 Full pipeline: execute → auto-index → search → retrieve ─────────────

func TestIntegration_FullPipeline_ExecuteIndexSearchRetrieve(t *testing.T) {
	srv := newTestServer(t, nil)

	// Step 1: Execute a command that generates structured output
	execResult := callTool(t, srv, map[string]any{
		"language": "shell",
		"code":     `printf '# System Report\n\n## CPU Info\n\nProcessor: Apple M1 Pro\nCores: 10\n\n## Memory Info\n\nTotal: 32GB\nAvailable: 16GB\n\n## Disk Info\n\nMount: /\nSize: 1TB\nUsed: 500GB\n'`,
	})
	require.False(t, execResult.IsError)

	// Step 2: Index the output
	output := resultText(execResult)
	indexResult := callIndex(t, srv, map[string]any{
		"content": output,
		"source":  "system-report",
	})
	require.False(t, indexResult.IsError)
	assert.Contains(t, resultText(indexResult), "Indexed")

	// Step 3: Search for specific information
	searchResult := callSearch(t, srv, map[string]any{
		"queries": []any{"CPU processor cores", "memory available"},
		"source":  "system-report",
	})
	require.False(t, searchResult.IsError)
	text := resultText(searchResult)
	assert.Contains(t, text, "CPU")
	assert.Contains(t, text, "Memory")

	// Step 4: Verify stats show the full pipeline
	snap := srv.stats.Snapshot()
	assert.Equal(t, 1, snap.Calls["capy_execute"])
	assert.Equal(t, 1, snap.Calls["capy_index"])
	assert.Equal(t, 1, snap.Calls["capy_search"])
	assert.Greater(t, snap.BytesSandboxed, int64(0))
	assert.Greater(t, snap.BytesIndexed, int64(0))
}

func TestIntegration_BatchPipeline_ExecuteAndQueryInOneCall(t *testing.T) {
	srv := newTestServer(t, nil)

	// Single batch_execute call: executes commands AND searches results
	r := callBatch(t, srv, map[string]any{
		"commands": []any{
			map[string]any{
				"label":   "Git Log",
				"command": "echo 'commit abc1234 - Add feature X\ncommit def5678 - Fix bug Y\ncommit ghi9012 - Refactor Z'",
			},
			map[string]any{
				"label":   "Test Results",
				"command": "echo 'PASS: TestFoo (0.01s)\nPASS: TestBar (0.02s)\nFAIL: TestBaz (0.03s)\n3 tests, 2 passed, 1 failed'",
			},
		},
		"queries": []any{"feature commit", "failed test"},
	})
	require.False(t, r.IsError)
	text := resultText(r)

	// Should have executed both commands
	assert.Contains(t, text, "Executed 2 commands")

	// Should have search results for both queries
	assert.Contains(t, text, "## feature commit")
	assert.Contains(t, text, "## failed test")

	// Verify the indexed content is also searchable via standalone search
	r2 := callSearch(t, srv, map[string]any{
		"queries": []any{"Refactor Z"},
	})
	require.False(t, r2.IsError)
	assert.Contains(t, resultText(r2), "Refactor")
}

// ─── 16.3b TTL cache → stats pipeline ─────────────────────────────────────────

func TestIntegration_FetchCacheHit_StatsReport(t *testing.T) {
	disableSSRFValidation(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "# Cached Docs\n\nThis content should only be fetched once during the TTL window.")
	}))
	defer ts.Close()

	srv := newTestServer(t, nil)

	// Step 1: First fetch — should actually fetch and index
	r1 := callFetchAndIndex(t, srv, map[string]any{"url": ts.URL, "source": "pipeline-cache"})
	require.False(t, r1.IsError)
	assert.Contains(t, resultText(r1), "sections")

	// Step 2: Second fetch — should return cache hit
	r2 := callFetchAndIndex(t, srv, map[string]any{"url": ts.URL, "source": "pipeline-cache"})
	require.False(t, r2.IsError)
	assert.Contains(t, resultText(r2), "Cache hit")

	// Step 3: Stats report should include TTL Cache section
	r3 := callStats(t, srv)
	require.False(t, r3.IsError)
	text := resultText(r3)
	assert.Contains(t, text, "TTL Cache")
	assert.Contains(t, text, "Cache hits")
	assert.Contains(t, text, "Data avoided by cache")

	// Step 4: Verify stats snapshot has correct values
	snap := srv.stats.Snapshot()
	assert.Equal(t, int64(1), snap.CacheHits)
	assert.Greater(t, snap.CacheBytesSaved, int64(0))
}

// ─── 16.4 Performance tests ───────────────────────────────────────────────────

func TestIntegration_Performance_LargeDocumentIndexAndSearch(t *testing.T) {
	srv := newTestServer(t, nil)

	// Generate ~100KB document with realistic structure
	var b strings.Builder
	b.WriteString("# Large Documentation\n\n")
	for i := range 200 {
		fmt.Fprintf(&b, "## Section %d: Feature %d\n\n", i, i)
		fmt.Fprintf(&b, "This section describes feature number %d in detail. ", i)
		fmt.Fprintf(&b, "The feature was introduced in version %d.%d.0 and has been stable since.\n\n", i/10, i%10)
		fmt.Fprintf(&b, "The implementation uses algorithm_%d which provides O(n log n) performance.\n", i)
		fmt.Fprintf(&b, "It processes up to %d records per second under normal load conditions.\n\n", (i+1)*1000)
		fmt.Fprintf(&b, "Configuration is done via config_%d.yaml with the following options:\n\n", i)
		fmt.Fprintf(&b, "```yaml\nfeature_%d:\n  enabled: true\n  timeout: %dms\n  retries: %d\n  max_connections: %d\n  buffer_size: %d\n```\n\n", i, (i+1)*100, i%5+1, i%20+5, (i+1)*512)
		fmt.Fprintf(&b, "### Usage Notes for Feature %d\n\n", i)
		fmt.Fprintf(&b, "When using feature_%d, ensure that the prerequisite services are running.\n", i)
		fmt.Fprintf(&b, "The feature depends on service_%d and database_%d for proper operation.\n", i%10, i%5)
		fmt.Fprintf(&b, "See also: section %d for related functionality.\n\n", (i+1)%200)
	}
	doc := b.String()
	require.Greater(t, len(doc), 90*1024, "document should be ~100KB")

	// Index: should complete in <1s
	start := time.Now()
	r := callIndex(t, srv, map[string]any{
		"content": doc,
		"source":  "large-docs",
	})
	indexTime := time.Since(start)
	require.False(t, r.IsError)
	assert.Less(t, indexTime, 1*time.Second, "indexing 100KB should complete in under 1 second")
	t.Logf("Index time: %v for %dKB", indexTime, len(doc)/1024)

	// Search: should complete in <1s
	start = time.Now()
	r2 := callSearch(t, srv, map[string]any{
		"queries": []any{
			"algorithm performance O(n log n)",
			"configuration yaml timeout",
			"feature 42",
		},
		"source": "large-docs",
	})
	searchTime := time.Since(start)
	require.False(t, r2.IsError)
	assert.Less(t, searchTime, 1*time.Second, "searching 100KB indexed content should complete in under 1 second")
	t.Logf("Search time: %v", searchTime)

	// Verify search actually found relevant content
	text := resultText(r2)
	assert.Contains(t, text, "algorithm")
	assert.Contains(t, text, "config")
}

func TestIntegration_Performance_BatchExecuteWithSearch(t *testing.T) {
	srv := newTestServer(t, nil)

	// Build a batch of 5 commands producing ~20KB each
	commands := make([]any, 5)
	for i := range 5 {
		cmd := fmt.Sprintf("for j in $(seq 1 200); do echo 'Command %d output line $j: data_%d value=$j status=ok'; done", i, i)
		commands[i] = map[string]any{
			"label":   fmt.Sprintf("Command %d", i),
			"command": cmd,
		}
	}

	start := time.Now()
	r := callBatch(t, srv, map[string]any{
		"commands": commands,
		"queries":  []any{"data_0 status", "data_3 value", "Command 4 output"},
	})
	elapsed := time.Since(start)
	require.False(t, r.IsError)
	t.Logf("Batch execute+search time: %v", elapsed)

	text := resultText(r)
	assert.Contains(t, text, "Executed 5 commands")
	assert.Contains(t, text, "Indexed")
}
