# Implementation Guide: Upstream Sync — context-mode v1.0.26→v1.0.54

> **Design:** [./design.md](./design.md)
>
> **Reference TS code:** `/context-mode/src/store.ts`, `/context-mode/src/server.ts`
>
> **Existing Go code:** `/internal/store/`, `/internal/server/`, `/internal/hook/`

---

## 1. Search Options Struct + ContentType Filter

### 1.1 New Types

**File:** `internal/store/types.go`

Add `SearchOptions` struct:

```go
type SearchOptions struct {
    Source          string // partial match filter (LIKE '%source%')
    ContentType     string // "code", "prose", or "" (no filter)
    SourceMatchMode string // "like" (default) or "exact"
}
```

### 1.2 Update SearchWithFallback Signature

**File:** `internal/store/search.go`

Change from:
```go
func (s *ContentStore) SearchWithFallback(query string, limit int, source string) ([]SearchResult, error)
```

To:
```go
func (s *ContentStore) SearchWithFallback(query string, limit int, opts SearchOptions) ([]SearchResult, error)
```

All callers must be updated:
- `internal/server/tool_search.go` — `handleSearch`
- `internal/server/tool_batch.go` — `handleBatchExecute`
- `internal/store/search_test.go` — all test functions

### 1.3 Dynamic SQL for Filtered Queries

**File:** `internal/store/search.go`

Add a helper that builds the WHERE clause dynamically:

```go
func (s *ContentStore) searchPorterDynamic(query string, limit int, opts SearchOptions) []SearchResult
func (s *ContentStore) searchTrigramDynamic(query string, limit int, opts SearchOptions) []SearchResult
```

Logic:
- Start with base SQL (the prepared `porterSQL` / `trigramSQL` constants)
- If `opts.Source != ""`: append source filter clause
  - If `opts.SourceMatchMode == "exact"`: `AND s.label = ?`
  - Else: `AND s.label LIKE '%' || ? || '%'` (existing behavior)
- If `opts.ContentType != ""`: append `AND c.content_type = ?`
- Build params slice accordingly
- Use `db.Query()` (not prepared statement) for the dynamic path

The existing prepared statements (`stmtSearchPorter`, `stmtSearchPorterFiltered`, etc.) can remain for the hot path (no contentType, no exact match). The dynamic path handles the combinatorial cases.

### 1.4 BM25 Title Weight

**File:** `internal/store/store.go`

In `prepareStatements`, change all `bm25(chunks, 2.0, 1.0)` to `bm25(chunks, 5.0, 1.0)` and all `bm25(chunks_trigram, 2.0, 1.0)` to `bm25(chunks_trigram, 5.0, 1.0)`.

There are exactly 4 occurrences: `porterSQL`, `stmtSearchPorterFiltered`, `trigramSQL`, `stmtSearchTrigramFiltered`.

Also update the dynamic SQL builder to use `5.0` weight.

---

## 2. Reciprocal Rank Fusion (RRF)

### 2.1 RRF Core

**File:** `internal/store/search.go`

Replace the cascading loop in `SearchWithFallback` with RRF:

```go
func (s *ContentStore) SearchWithFallback(query string, limit int, opts SearchOptions) ([]SearchResult, error) {
    // ... getDB check ...

    // RRF pass 1: direct query
    results := s.rrfSearch(query, limit, opts)

    // RRF pass 2: fuzzy correction (only if pass 1 returned fewer than limit)
    if len(results) < limit {
        corrected := s.fuzzyCorrectQuery(query)
        if corrected != "" && corrected != query {
            fuzzyResults := s.rrfSearch(corrected, limit, opts)
            // Tag fuzzy results before merging
            for i := range fuzzyResults {
                fuzzyResults[i].MatchLayer = "fuzzy+" + fuzzyResults[i].MatchLayer
            }
            // Deduplicate: merge pass 2 into pass 1 by (sourceID, title) key.
            // If a result exists in both passes, keep pass 1's version (higher confidence).
            results = mergeRRFResults(results, fuzzyResults, limit)
        }
    }

    if len(results) > 0 {
        s.trackAccess(results)
    }
    return results, nil
}
```

### 2.2 rrfSearch Implementation

**File:** `internal/store/search.go`

New private method:

```go
func (s *ContentStore) rrfSearch(query string, limit int, opts SearchOptions) []SearchResult
```

Algorithm:
1. `fetchLimit := max(limit*2, 10)`
2. Run all 4 layers **concurrently** with `fetchLimit`, collecting results. Use a `sync.WaitGroup` and a mutex-protected slice (or 4 separate result slices gathered after `wg.Wait()`). SQLite WAL mode supports concurrent readers.
3. Build fusion map: key = `fmt.Sprintf("%d:%s", r.SourceID, r.Title)`
   - For each result at **position `i`** in the layer's ordered result slice (0-indexed — this is the slice index, not the BM25 `rank` float): `score += 1.0 / (K + float64(i))`
   - K = 60
4. Sort by fused score descending
5. Apply proximity reranking (if multi-term query)
6. Return top `limit`

Each layer function (`searchPorter`, `searchTrigramQuery`) already returns `[]SearchResult` — reuse them as-is but with `fetchLimit`.

Add a `mergeRRFResults` helper for deduplicating across pass 1 (direct) and pass 2 (fuzzy):

```go
func mergeRRFResults(primary, secondary []SearchResult, limit int) []SearchResult
```

Build a set of keys from `primary`, append only `secondary` results whose key isn't in the set, truncate to `limit`.

### 2.3 Proximity Reranking

**File:** `internal/store/search.go`

New function:

```go
func proximityRerank(results []SearchResult, query string) []SearchResult
```

Only applies when query has 2+ words. For each result:

**Primary approach — use FTS5 highlight markers:**
The `Highlighted` field already contains char(2)/char(3) markers wrapping matched terms (set by `highlight(chunks, 1, char(2), char(3))`). Extract match positions by scanning for these markers:
1. Find all `char(2)` positions in `r.Highlighted` — these are match start positions
2. Map these positions back to the original content (strip markers to get real character offsets)
3. Find the minimum window containing at least one match per query term
4. `boost := 1.0 + (1.0 / (1.0 + float64(minWindow)/100.0))`
5. Multiply the result's fused score by boost
6. If any term has no highlight marker, boost = 1.0 (no penalty, no bonus)

This is faster and more accurate than re-scanning content with `strings.Index`, because FTS5 already tokenized and matched terms (including stemmed forms that wouldn't match via naive string search).

**Fallback** — if `Highlighted` is empty (shouldn't happen, but defensive), fall back to `strings.Index` on `strings.ToLower(r.Content)`.

Re-sort results by boosted score.

### 2.4 Fused Score Storage

The `SearchResult` struct needs a field to carry the fused score through proximity reranking. Add to `internal/store/types.go`:

```go
type SearchResult struct {
    // ... existing fields ...
    FusedScore float64 // RRF fusion score (internal, not exposed to callers)
}
```

---

## 3. Source Metadata + TTL Cache

### 3.1 GetSourceMeta

**File:** `internal/store/search.go` (query methods section)

New prepared statement in `internal/store/store.go`:

```go
stmtGetSourceMeta *sql.Stmt
```

SQL: `SELECT label, chunk_count, indexed_at FROM sources WHERE label = ?`

Method:
```go
func (s *ContentStore) GetSourceMeta(label string) (*SourceMeta, error)
```

Returns `nil, nil` when not found (no error). Parse `indexed_at` string to `time.Time` using the same format as `ListSources`.

Add `SourceMeta` to `internal/store/types.go`:
```go
type SourceMeta struct {
    Label      string
    ChunkCount int
    IndexedAt  time.Time
}
```

Don't forget to:
- Add `stmtGetSourceMeta` to the `Close()` cleanup list
- Add the prepared statement in `prepareStatements`

### 3.2 IsEmpty

**File:** `internal/store/cleanup.go` (store queries section)

```go
func (s *ContentStore) IsEmpty() (bool, error)
```

Query: `SELECT COUNT(*) FROM sources`. Returns true if count is 0. Add a prepared statement `stmtSourceCount` or use inline query (this is cold path, either works).

### 3.3 TTL Cache in fetch_and_index

**File:** `internal/server/tool_fetch.go`

At the start of `handleFetchAndIndex`, before the HTTP request:

1. Read `force` parameter: use `req.GetArguments()["force"]` with bool type assertion
2. Determine label: `label := source; if label == "" { label = url }`
3. Read TTL from config: `ttl := time.Duration(s.config.Store.Cache.FetchTTLHours) * time.Hour` (default 24)
4. If `!force`:
   - `meta, err := st.GetSourceMeta(label)` (need to call `s.getStore()` early)
   - If `meta != nil` and `time.Since(meta.IndexedAt) < ttl`:
     - Compute age string (hours/minutes)
     - `s.stats.AddCacheHit(int64(meta.ChunkCount) * 1600)`
     - Return cache-hit text response with source info, including configured TTL

**File:** `internal/server/tools.go`

Add `force` parameter to `toolFetchAndIndex()`:
```go
mcp.WithBoolean("force",
    mcp.Description("Skip cache and re-fetch even if content was recently indexed"),
),
```

**File:** `internal/config/config.go`

Add cache config to the store section:
```go
type StoreConfig struct {
    // ... existing fields ...
    Cache CacheConfig
}

type CacheConfig struct {
    FetchTTLHours int `toml:"fetch_ttl_hours"` // default: 24
}
```

Set default in config loading: if `FetchTTLHours == 0`, set to `24`.

### 3.4 CleanupStaleSources

**File:** `internal/store/cleanup.go`

New method:
```go
func (s *ContentStore) CleanupStaleSources(maxAgeDays int) (int, error)
```

Unlike existing `Cleanup` (which filters by `access_count = 0`), this deletes any source where `last_accessed_at` is older than `maxAgeDays` days, regardless of access count. Execute in a transaction:

```sql
DELETE FROM chunks WHERE source_id IN (
    SELECT id FROM sources WHERE last_accessed_at < datetime('now', '-' || ? || ' days')
);
DELETE FROM chunks_trigram WHERE source_id IN (
    SELECT id FROM sources WHERE last_accessed_at < datetime('now', '-' || ? || ' days')
);
DELETE FROM sources WHERE last_accessed_at < datetime('now', '-' || ? || ' days');
```

Return the count of deleted sources from `changes()`.

---

## 4. Batch Execute: Exact Source Scoping

### 4.1 Update Batch Search Calls

**File:** `internal/server/tool_batch.go`

In `handleBatchExecute`, the search loop currently does:

```go
results, searchErr := st.SearchWithFallback(query, 3, sourceLabel)
crossSource := false

// Tier 2: global fallback
if len(results) == 0 && searchErr == nil {
    results, searchErr = st.SearchWithFallback(query, 3, "")
    crossSource = len(results) > 0
}
```

**Replace with scoped-only search** (matching TS behavior — global fallback was removed upstream):

```go
results, searchErr := st.SearchWithFallback(query, 3, store.SearchOptions{
    Source:          sourceLabel,
    SourceMatchMode: "exact",
})
```

Remove the Tier 2 global fallback entirely. Remove the `crossSource` variable and all associated formatting logic (the `> **Note:** No results in current batch output...` message and `_(source: ...)_` tags). When a query has no results in the current batch, it simply shows "No matching sections found." — the cross-batch search tip (Section 4.2) directs users to `capy_search` for broader queries.

### 4.2 Cross-Batch Search Tip

**File:** `internal/server/tool_batch.go`

After the query results section, before the distinctive terms, append:

```go
out.WriteString("\n💡 To search across ALL indexed content (not just this batch), use capy_search(queries: [...])\n")
```

---

## 5. Empty Index Early Return

### 5.1 Search Handler

**File:** `internal/server/tool_search.go`

After parsing parameters and before running queries, check if store is empty:

```go
st := s.getStore()
empty, err := st.IsEmpty()
if err == nil && empty {
    return s.trackToolResponse("capy_search", errorResult(
        "The knowledge base is empty — no content has been indexed yet.\n\n" +
        "Index content first using:\n" +
        "  • capy_batch_execute(commands, queries) — run commands, auto-index output, and search in one call\n" +
        "  • capy_fetch_and_index(url) — fetch a URL, index it, then search with capy_search\n" +
        "  • capy_index(content, source) — manually index text content\n\n" +
        "After indexing, capy_search becomes available for follow-up queries.",
    )), nil
}
```

Also add `contentType` parameter to `toolSearch()` in `tools.go`:

```go
mcp.WithString("contentType",
    mcp.Description("Filter by content type: 'code' or 'prose'"),
    mcp.Enum("code", "prose"),
),
```

And pass it through in `handleSearch`:

```go
contentType := req.GetString("contentType", "")
// ...
results, err := st.SearchWithFallback(q, effectiveLimit, store.SearchOptions{
    Source:      source,
    ContentType: contentType,
})
```

---

## 6. Hook: Smart curl/wget Allowance

### 6.1 New Helper Functions

**File:** `internal/hook/helpers.go`

Replace the simple `isCurlOrWget` usage in `routeBash` with a smarter check:

```go
// isCurlWgetSafe checks if a curl/wget command writes to a file silently.
func isCurlWgetSafe(segment string) bool
```

Logic:
- Check if segment contains curl or wget
- If curl: check for `-o`/`--output` flags or `>`/`>>` redirect, AND `-s`/`--silent` flag
- If wget: check for `-O`/`--output-document` flags or `>`/`>>` redirect, AND `-q`/`--quiet` flag
- Return true only if file output AND silent

```go
// splitChainedCommands splits a shell command on &&, ||, ; operators.
func splitChainedCommands(cmd string) []string
```

### 6.2 Update routeBash

**File:** `internal/hook/pretooluse.go`

In `routeBash`, replace:

```go
if isCurlOrWget(stripped) {
    return a.FormatModify(...)
}
```

With:

```go
if isCurlOrWget(stripped) {
    segments := splitChainedCommands(stripped)
    allSafe := true
    for _, seg := range segments {
        if isCurlOrWget(seg) && !isCurlWgetSafe(seg) {
            allSafe = false
            break
        }
    }
    if !allSafe {
        return a.FormatModify(map[string]any{
            "command": `echo "capy: curl/wget blocked (stdout flood risk). Use capy_fetch_and_index(url, source) to fetch URLs, or capy_execute(language, code) to run HTTP calls in sandbox. File downloads with -o/-s are allowed."`,
        })
    }
    // All curl/wget segments write to file silently — allow through
}
```

---

## 7. TTL Cache Statistics

### 7.1 SessionStats Changes

**File:** `internal/server/stats.go`

Add fields:
```go
CacheHits      int64
CacheBytesSaved int64
```

Add method:
```go
func (s *SessionStats) AddCacheHit(estimatedBytes int64) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.CacheHits++
    s.CacheBytesSaved += estimatedBytes
}
```

Update `Snapshot()` to copy the new fields.

### 7.2 Stats Report

**File:** `internal/server/tool_stats.go`

After the "Knowledge Base" section, add TTL cache section when `snap.CacheHits > 0`:

```go
if snap.CacheHits > 0 {
    ttlHours := s.config.Store.Cache.FetchTTLHours
    ttlHoursLeft := max(0, ttlHours - int(time.Since(snap.SessionStart).Hours()))
    lines = append(lines, "", "### TTL Cache", "",
        "| Metric | Value |",
        "|--------|------:|",
        fmt.Sprintf("| Cache hits | **%d** |", snap.CacheHits),
        fmt.Sprintf("| Data avoided by cache | **%s** |", formatBytes(snap.CacheBytesSaved)),
        fmt.Sprintf("| Network requests saved | **%d** |", snap.CacheHits),
        fmt.Sprintf("| TTL remaining | **~%dh** |", ttlHoursLeft),
    )
}
```

Also update the top-level savings calculation (earlier in `handleStats`) to include cache savings:

```go
keptOut := snap.BytesIndexed + snap.BytesSandboxed
totalProcessed := keptOut + totalBytesReturned + snap.CacheBytesSaved
```

This gives a more accurate picture — cache hits represent data that *would have* been fetched and processed without TTL caching.

---

## 8. Testing Strategy

### Unit Tests

| Area | File | Key Tests |
|------|------|-----------|
| RRF search | `internal/store/search_test.go` | RRF returns results from multiple layers; RRF ranks multi-layer hits above single-layer; fuzzy correction activates when RRF returns < limit |
| Proximity | `internal/store/search_test.go` | Multi-term query boosts close proximity; single-term query skips reranking; missing terms get no penalty |
| ContentType | `internal/store/search_test.go` | ContentType="code" returns only code chunks; empty contentType returns all |
| Source meta | `internal/store/search_test.go` | GetSourceMeta returns nil for unknown; returns correct metadata after indexing |
| IsEmpty | `internal/store/cleanup_test.go` | True on fresh DB; false after indexing |
| TTL cache | `internal/server/tool_knowledge_test.go` | Second fetch within 24h returns cache hit; force=true bypasses cache; stats track cache hits |
| Exact source | `internal/server/tool_batch_test.go` | Batch search doesn't leak cross-source results |
| curl/wget | `internal/hook/hook_test.go` | `curl -sL url -o file` passes; `curl url` blocked; chained commands evaluated per-segment |
| Empty index | `internal/server/tool_knowledge_test.go` | Search on empty store returns error with guidance |
| Cache stats | `internal/server/stats_test.go` | AddCacheHit increments both fields; Snapshot copies them |

### Integration Tests

The existing `tool_knowledge_test.go` and `tool_batch_test.go` already test end-to-end flows. Add TTL cache and exact-source scenarios to these.
