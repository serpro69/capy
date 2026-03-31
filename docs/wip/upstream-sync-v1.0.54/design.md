# Design: Upstream Sync — context-mode v1.0.26→v1.0.54

> **Scope:** Port meaningful algorithm, feature, and UX changes from the context-mode TypeScript reference (commits 42c994b..358fa3a) to the capy Go implementation.
>
> **Reference:** `/context-mode/src/store.ts`, `/context-mode/src/server.ts`, `/context-mode/src/hooks/`
>
> **Existing port docs:** `/docs/done/port-mcp-core-merged/`

## 1. Overview

The context-mode TS reference accumulated ~90 commits since the version capy was ported from. After filtering out CI, bundle, platform-specific (Windows, Bun, KiloCode, Kiro, Zed, Pi, OpenClaw, OpenCode), and docs-only changes, **12 changes** remain relevant to capy's core:

| Priority | Area | Change |
|----------|------|--------|
| P0 | Search | Replace cascading fallback with Reciprocal Rank Fusion (RRF) |
| P0 | Search | Add proximity reranking for multi-term queries |
| P0 | Search | Increase BM25 title weight from 2x to 5x |
| P1 | Search | Add `contentType` filter parameter |
| P1 | Fetch | TTL cache (24h) for `fetch_and_index` |
| P1 | Store | Source metadata API (`GetSourceMeta`) |
| P1 | Store | `CleanupStaleSources` (stale = by last_accessed_at, not just access_count=0) |
| P2 | Batch | Exact source label scoping (= instead of LIKE) |
| P2 | Search | Empty index early return with guidance |
| P2 | Hook | curl/wget: allow silent file-output downloads |
| P2 | Batch | Cross-batch search tip in output |
| P2 | Stats | TTL cache statistics in session report |

### Not applicable (skipped)

- Bun/SQLite driver selection — Go uses CGO mattn/go-sqlite3
- Windows/nvm4w compatibility — Go compiles to native binary
- Platform adapters (KiloCode, Kiro, Zed, Pi, OpenClaw, OpenCode) — deferred in original design
- OpenClaw compaction changes — not applicable
- AGENTS.md writing — not applicable
- Hook self-heal/stale cleanup — not applicable (Go binary is self-contained)
- `execFileSync` for rustc — Go already uses `exec.Command` without shell

---

## 2. Search Algorithm Overhaul

### 2.1 Reciprocal Rank Fusion (RRF)

**Problem:** The current `SearchWithFallback` in `internal/store/search.go` uses cascading layers — tries porter+AND, porter+OR, trigram+AND, trigram+OR in sequence and **stops at the first layer returning results**. This means if porter+AND returns 3 mediocre results, better trigram matches are never seen.

**Solution:** Reciprocal Rank Fusion (Cormack et al. 2009). Run all search layers, then fuse results:

```
For each result appearing in layer L at position i:
    score += 1 / (K + i)    where K = 60 (standard RRF constant)
```

If the same chunk appears in multiple layers, scores **add up** — appearing in both porter and trigram boosts the result.

**Algorithm:**

1. Run all 4 layers (porter+AND, porter+OR, trigram+AND, trigram+OR), each fetching `max(limit*2, 10)` candidates. **Go improvement:** run all 4 layers concurrently via goroutines — SQLite WAL supports concurrent reads, and Go's goroutines make this trivial. The TS runs them sequentially (single-threaded JS).
2. Build a map: `chunkKey → { result, fusedScore }`
   - Key by `(source_id, title)` to deduplicate across layers
   - For each appearance at position `i` (0-indexed in the ordered result slice, NOT the BM25 rank float): `fusedScore += 1.0 / (K + float64(i))`
3. Sort by fused score descending, return top `limit`
4. **Fuzzy fallback:** If RRF returns fewer than `limit` results, run `fuzzyCorrectQuery`, then repeat the RRF process with the corrected query. Merge results, **deduplicating by the same `(source_id, title)` key** — if a result appeared in both passes, keep the higher fused score.

**Key difference from cascading:** Every layer contributes. A chunk that ranks #1 in trigram and #3 in porter gets a combined score that beats a chunk appearing only in porter at #1.

### 2.2 Proximity Reranking

Applied as a **post-processing step** after RRF fusion, only for multi-term queries (2+ words).

For each result:
1. Find the minimum character window containing all query terms in the content
2. Compute boost: `proximityBoost = 1.0 + (1.0 / (1.0 + minDistance/100.0))`
3. Final score: `fusedScore * proximityBoost`
4. Re-sort by final score

Results where terms appear close together get up to 2x boost. Results where terms are scattered across the content get minimal boost (~1.01x for very distant terms).

**Go improvement:** Instead of re-scanning content with `strings.Index`, use the FTS5 `highlighted` field already present on `SearchResult`. The highlight markers (char(2)/char(3)) pinpoint exact match locations — extracting positions from these markers is faster and more precise than naive string search, especially for stemmed terms where the surface form may not match the query term exactly.

### 2.3 BM25 Title Weight

All prepared statements change from `bm25(chunks, 2.0, 1.0)` to `bm25(chunks, 5.0, 1.0)` (and same for `chunks_trigram`). This gives headings 5x weight over body content, improving precision for heading-targeted queries.

**Affected statements:** `porterSQL`, `stmtSearchPorterFiltered`, `trigramSQL`, `stmtSearchTrigramFiltered`, and all new variants added for contentType/exact-source filtering.

---

## 3. ContentType Filter

Add an optional `contentType` parameter (`"code"` or `"prose"`) to search. When provided, SQL queries add `AND c.content_type = ?` to the WHERE clause.

**Current signature:**
```go
SearchWithFallback(query string, limit int, source string) ([]SearchResult, error)
```

**New signature:**
```go
SearchWithFallback(query string, limit int, opts SearchOptions) ([]SearchResult, error)
```

Where:
```go
type SearchOptions struct {
    Source          string // LIKE filter (default)
    ContentType    string // "code", "prose", or "" (no filter)
    SourceMatchMode string // "like" (default) or "exact"
}
```

Using an options struct is idiomatic Go and avoids adding more positional parameters as the API evolves. It also cleanly bundles the `SourceMatchMode` needed for batch_execute exact scoping (Section 5).

**SQL approach:** Instead of pre-preparing N*M statement combinations (the TS approach), Go dynamically builds the WHERE clause and uses `db.Query()` for search queries with optional filters. The base porter/trigram statements (no filters) remain prepared for the hot path. When contentType or exact source matching is needed, the query is built dynamically — this is a cold path and the overhead is negligible for SQLite FTS5.

---

## 4. TTL Cache + Source Metadata

### 4.1 Source Metadata (`GetSourceMeta`)

New method on `ContentStore`:
```go
type SourceMeta struct {
    Label      string
    ChunkCount int
    IndexedAt  time.Time
}

func (s *ContentStore) GetSourceMeta(label string) (*SourceMeta, error)
```

Query: `SELECT label, chunk_count, indexed_at FROM sources WHERE label = ?`

Returns `nil, nil` when no matching source exists. This is general-purpose — used by TTL cache, stats, doctor, cleanup.

**Prepared statement:** New `stmtGetSourceMeta` added to `ContentStore`.

### 4.2 TTL Cache on `fetch_and_index`

Before fetching a URL, check if the source was recently indexed:

1. Call `GetSourceMeta(label)` where label is `source` param or URL
2. If meta exists and `time.Since(meta.IndexedAt) < TTL`:
   - Return cache-hit response with source info and age
   - Track stats: `stats.AddCacheHit(meta.ChunkCount * 1600)` (estimated ~1.6KB/chunk)
3. If `force: true` parameter is set, skip the TTL check

**New tool parameter:** `force` (boolean, optional) on `capy_fetch_and_index`.

**Go improvement — configurable TTL:** The TS hardcodes 24h. Since capy has its own config system, the TTL is configurable:

```toml
[store.cache]
fetch_ttl_hours = 24  # default
```

Loaded via `config.Config` and passed to the fetch handler. This lets users tune caching per-project (e.g., shorter TTL for fast-moving API docs, longer for stable references).

### 4.3 Cleanup Enhancement (cleanupStaleSources)

The current `Cleanup` method only removes sources with `access_count = 0`. The TS reference added `cleanupStaleSources` which removes any source where `last_accessed_at` is older than N days, regardless of access_count.

Add a new method:
```go
func (s *ContentStore) CleanupStaleSources(maxAgeDays int) (int, error)
```

This deletes sources (and their chunks) where `last_accessed_at < datetime('now', '-N days')`. Returns count of deleted sources. Used internally by the server on startup or by the cleanup tool.

---

## 5. Batch Execute: Exact Source Scoping + Remove Global Fallback

**Problem 1:** `handleBatchExecute` searches with `source = "batch:label1,label2,..."` using LIKE matching. This can accidentally match previously indexed content with similar labels.

**Problem 2:** The current Go implementation has a Tier 2 "global fallback" — when scoped search returns no results, it re-searches with no source filter, pulling results from any previously indexed content. The TS removed this in the same commit range, replacing it with scoped-only search. The global fallback is confusing: users see results from unrelated prior batches mixed into their current output with a "cross-source" warning.

**Solution:**
- Batch execute uses `SearchOptions{SourceMatchMode: "exact"}` when searching its own output. This generates `s.label = ?` instead of `s.label LIKE '%' || ? || '%'`.
- **Remove the Tier 2 global fallback entirely.** If a query doesn't match the current batch output, return "No matching sections found." The cross-batch search tip (below) directs users to `capy_search` for broader queries.

The `capy_search` tool continues to use LIKE (default) since partial matching is valuable for user-facing queries.

**Cross-batch search tip:** Append to batch_execute output:
```
💡 To search across ALL indexed content (not just this batch), use capy_search(queries: [...])
```

---

## 6. Empty Index Early Return

When `handleSearch` runs and the store has no indexed content, return an `isError: true` response with guidance instead of running queries against an empty database.

New method:
```go
func (s *ContentStore) IsEmpty() (bool, error)
```

Query: `SELECT COUNT(*) FROM sources` — returns true if count is 0.

Error message guides the user to index content first using `batch_execute`, `fetch_and_index`, `index`, or `execute`.

---

## 7. Hook: Smart curl/wget Allowance

**Current:** `isCurlOrWget(stripped)` blanket-blocks all curl/wget.

**New:** Allow curl/wget when writing to a file silently (no stdout flooding):

1. Split command on chain operators (`&&`, `||`, `;`)
2. For each segment containing curl/wget:
   - Check for **file output flags**: curl `-o`/`--output`, wget `-O`/`--output-document`, or shell redirect `>`/`>>`
   - Check for **silent flags**: curl `-s`/`--silent`, wget `-q`/`--quiet`
   - If file output AND silent → segment is **safe**
   - If stdout output or not silent → segment is **unsafe**
3. If ALL curl/wget segments are safe → pass through
4. If ANY segment is unsafe → block with redirect message (existing behavior)

Example: `curl -sL https://example.com/file.tar.gz -o file.tar.gz` → allowed.
Example: `curl https://example.com/api` → blocked.

---

## 8. TTL Cache Statistics

Add to `SessionStats`:
```go
CacheHits      int64
CacheBytesSaved int64
```

New method: `AddCacheHit(estimatedBytes int64)` — atomically increments both.

In `handleStats`, when `CacheHits > 0`, add a "TTL Cache" section:

| Metric | Value |
|--------|------:|
| Cache hits | N |
| Data avoided by cache | X KB |
| Network requests saved | N |
| TTL remaining | ~Xh |

Also update the top-level savings calculation to include cache savings: `totalProcessed = keptOut + totalBytesReturned + cacheBytesSaved`. This gives a more accurate picture of total data that would have entered context without capy.
