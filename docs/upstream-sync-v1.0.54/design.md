# Design: Upstream Sync — context-mode v1.0.26→v1.0.54

> **Scope:** Port meaningful algorithm, feature, and UX changes from the context-mode TypeScript reference (commits 42c994b..358fa3a) to the capy Go implementation.
>
> **Reference:** `/context-mode/src/store.ts`, `/context-mode/src/server.ts`, `/context-mode/src/hooks/`
>
> **Existing port docs:** `/docs/done/port-mcp-core-merged/`

## 1. Overview

The context-mode TS reference accumulated ~90 commits since the version capy was ported from. After filtering out CI, bundle, platform-specific (Windows, Bun, KiloCode, Kiro, Zed, Pi, OpenClaw, OpenCode), and docs-only changes, **12 changes** remain relevant to capy's core:

| Priority | Area | Change | Decision |
|----------|------|--------|----------|
| P0 | Search | Replace cascading fallback with Reciprocal Rank Fusion (RRF) | Port |
| P0 | Search | Add proximity reranking for multi-term queries | Port |
| P0 | Search | Configurable BM25 title weight (TS hardcodes 5x) | Port with improvement |
| P1 | Search | Add `contentType` filter parameter | Internal only (not in MCP schema) |
| P1 | Fetch | TTL cache (configurable, default 24h) for `fetch_and_index` | Port with improvement |
| P1 | Store | Source metadata API (`GetSourceMeta`) | Port |
| P1 | Store | `CleanupStaleSources` | Skip — existing `Cleanup` is better |
| P2 | Batch | Exact source label scoping + remove global fallback | Port |
| P2 | Search | Empty index early return with guidance | Port |
| P2 | Hook | curl/wget: allow file-output downloads | Port |
| P2 | Batch | Cross-batch search tip in output | Port |
| P2 | Stats | TTL cache statistics in session report | Port |

### Not applicable (skipped)

- Bun/SQLite driver selection — Go uses CGO mattn/go-sqlite3
- Windows/nvm4w compatibility — Go compiles to native binary
- Platform adapters (KiloCode, Kiro, Zed, Pi, OpenClaw, OpenCode) — deferred in original design
- OpenClaw compaction changes — not applicable
- AGENTS.md writing — not applicable
- Hook self-heal/stale cleanup — not applicable (Go binary is self-contained)
- `execFileSync` for rustc — Go already uses `exec.Command` without shell

### Deliberate divergences from upstream

- **`contentType` filter**: wired internally in `SearchOptions` + SQL, but NOT exposed in MCP tool schema (no current caller; avoids LLM prompt noise)
- **`cleanupStaleSources`**: not ported — existing `Cleanup()` is smarter for persistent DB (considers access patterns, not just age)
- **BM25 title weight**: configurable via `[store] title_weight` instead of hardcoded 5x (auto-generated headings could be hurt by aggressive boosting)
- **Proximity formula**: normalized by content length instead of magic constant 100
- **RRF layers**: 2 (porter OR + trigram OR) instead of TS's porter+trigram with AND/OR variants — OR is a superset, AND is unnecessary with RRF

---

## 2. Search Algorithm Overhaul

### 2.1 Reciprocal Rank Fusion (RRF)

**Problem:** The current `SearchWithFallback` in `internal/store/search.go` uses cascading layers — tries porter+AND, porter+OR, trigram+AND, trigram+OR in sequence and **stops at the first layer returning results**. This means if porter+AND returns 3 mediocre results, better trigram matches are never seen.

**Solution:** Reciprocal Rank Fusion (Cormack et al. 2009). Run both search strategies (porter + trigram), then fuse results:

```
For each result appearing in layer L at position i:
    score += 1 / (K + i)    where K = 60 (standard RRF constant)
```

If the same chunk appears in multiple layers, scores **add up** — appearing in both porter and trigram boosts the result.

**Algorithm:**

1. Run **2 layers** — porter OR and trigram OR — each fetching `max(limit*2, 10)` candidates. With RRF handling ranking, the AND/OR distinction is unnecessary — OR mode returns a superset, and RRF's fusion scoring handles precision. **Go improvement:** run both layers concurrently via goroutines — SQLite WAL supports concurrent reads. The TS runs them sequentially (single-threaded JS).
2. Build a map: `chunkKey → { result, fusedScore }`
   - Key by `(source_id, title)` to deduplicate across layers
   - For each appearance at position `i` (0-indexed in the ordered result slice, NOT the BM25 rank float): `fusedScore += 1.0 / (K + float64(i))`
3. Sort by fused score descending, return top `limit`
4. **Fuzzy fallback:** If RRF returns fewer than `limit` results, run `fuzzyCorrectQuery`, then repeat the RRF process with the corrected query. Merge results, **deduplicating by the same `(source_id, title)` key** — if a result appeared in both passes, keep the higher fused score.

**Key difference from cascading:** Both retrieval strategies always contribute. A chunk that ranks #1 in trigram and #3 in porter gets a combined score that beats a chunk appearing only in porter at #1.

### 2.2 Proximity Reranking

Applied as a **post-processing step** after RRF fusion, only for multi-term queries (2+ words).

For each result:
1. Find the minimum character window containing all query terms in the content
2. Compute boost: `proximityBoost = 1.0 / (1.0 + float64(minSpan) / float64(max(len(content), 1)))`
   - Normalizes by content length — a 50-char span in a 100-char chunk is treated differently than a 50-char span in a 10,000-char chunk
   - Range: (0, 1] — close proximity → ~1.0, scattered → ~0.0
3. Final score: `fusedScore * (1.0 + proximityBoost)`
4. Re-sort by final score

**Go improvement over TS formula:** The TS uses a magic constant (`minDist/100`). Normalizing by content length is more principled and adapts to chunk size variation.

**Go improvement:** Instead of re-scanning content with `strings.Index`, use the FTS5 `highlighted` field already present on `SearchResult`. The highlight markers (char(2)/char(3)) pinpoint exact match locations — extracting positions from these markers is faster and more precise than naive string search, especially for stemmed terms where the surface form may not match the query term exactly.

### 2.3 BM25 Title Weight (Configurable)

The TS upstream hardcodes `bm25(chunks, 5.0, 1.0)`. However, capy indexes a lot of auto-generated content with meaningless headings (`"Lines 1-20"`, `"Section 3"`, `"batch:git-log,npm-list"`) where boosting titles 5x could hurt ranking.

**Go improvement — configurable weight:**

```toml
[store]
title_weight = 2.0  # default (current behavior); set to 5.0 for upstream TS behavior
```

- Add `TitleWeight float64` to `StoreConfig` (default 2.0)
- Thread through `NewContentStore` → dynamic search SQL via `fmt.Sprintf("bm25(%s, %.1f, 1.0)", table, s.titleWeight)`
- Users who want the upstream 5x behavior can configure it per-project
- No SQL injection risk: value comes from config, not request input

---

## 3. SearchOptions Struct + ContentType Filter (Internal Only)

Introduce `SearchOptions` to replace the growing list of positional parameters:

```go
type SearchOptions struct {
    Source          string // LIKE filter (default)
    ContentType     string // "code", "prose", or "" (no filter)
    SourceMatchMode string // "like" (default) or "exact"
}
```

**New signature:**
```go
SearchWithFallback(query string, limit int, opts SearchOptions) ([]SearchResult, error)
```

**ContentType decision: internal only, NOT exposed in MCP tool schema.** Adding unused parameters to the tool schema adds token overhead to every prompt and gives the LLM another knob to hallucinate on. The `ContentType` field is wired into `SearchOptions` and the SQL WHERE clause so it's available when a use case emerges — exposing it in the schema is then one line. No current caller uses it.

**SQL approach:** Remove all 4 prepared search statements (`stmtSearchPorter`, `stmtSearchPorterFiltered`, `stmtSearchTrigram`, `stmtSearchTrigramFiltered`). With RRF always running through `rrfSearch`, there is no separate "hot path" for unfiltered queries. Replace with a single `execDynamicSearch(table, sanitized, limit, opts)` method that builds the WHERE clause dynamically:

- Always includes `{table} MATCH ?`
- Optionally adds `AND s.label = ?` (exact) or `AND s.label LIKE '%' || ? || '%'` (like) based on `SourceMatchMode`
- Optionally adds `AND c.content_type = ?` when `ContentType != ""`
- Uses `s.titleWeight` from config for BM25 weight
- No SQL injection risk: all dynamic parts are parameterized or from config

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

### 4.3 Cleanup: No Change (Deliberate Divergence)

The TS upstream added `cleanupStaleSources(maxAgeDays)` which deletes any source where `last_accessed_at` is older than N days, regardless of access count. **We do not port this.**

capy's existing `Cleanup()` is better for a persistent DB:
- Only removes sources with `access_count = 0` (never accessed via search) AND cold tier AND older than maxAgeDays
- The TS method would delete sources that were accessed but are old — surprising behavior for a persistent knowledge base ("I searched for React docs last month, why are they gone?")
- The TS method was designed for an originally-ephemeral DB where aggressive culling makes sense
- If cruft becomes a real problem, add an `aggressive` flag to existing `Cleanup` rather than a separate method

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

Use existing `store.Stats()` method (which already returns `SourceCount`) — no new method needed. Check `kbStats.SourceCount == 0`.

Error message guides the user to index content first using `batch_execute`, `fetch_and_index`, `index`, or `execute`.

---

## 7. Hook: Smart curl/wget Allowance

**Current:** `isCurlOrWget(stripped)` blanket-blocks all curl/wget.

**New:** Allow curl/wget when writing silently to a file (no stdout/stderr flooding):

1. Split command on chain operators (`&&`, `||`, `;`, `|`) respecting quoted strings
2. For each segment containing curl/wget, check 5 safety conditions:
   - **File output flags**: curl `-o`/`--output`, wget `-O`/`--output-document`, or shell redirect `>`/`>>` (excludes fd dups like `2>&1`)
   - **No stdout aliases**: `-o -` or `-o /dev/stdout` are blocked (output still goes to stdout)
   - **No verbose/trace flags**: `-v`, `--verbose`, `--trace`, `-D -` flood stderr into context
   - **Silent mode required**: curl `-s`/`--silent`, wget `-q`/`--quiet` (suppresses progress bar on stderr)
   - Combined short flags supported: `-sSLo`, `-qO` are correctly parsed
3. If ALL curl/wget segments pass all checks → pass through
4. If ANY segment fails → block with redirect message (existing behavior)

Example: `curl -sSL -o file.tar.gz https://example.com/file.tar.gz` → allowed.
Example: `curl -o file.tar.gz https://example.com/file.tar.gz` → blocked (no silent).
Example: `curl https://example.com/api` → blocked (no file output).
Example: `curl -sSL -o a.txt url && curl url` → blocked (mixed).

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
