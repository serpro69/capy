# Tasks: Upstream Sync — context-mode v1.0.26→v1.0.54

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> Status: pending
> Created: 2026-03-31

## Task 1: SearchOptions + configurable BM25 + dynamic SQL
- **Status:** done
- **Depends on:** —
- **Docs:** [implementation.md#1-searchoptions-struct--configurable-bm25--dynamic-sql](./implementation.md#1-searchoptions-struct--configurable-bm25--dynamic-sql)

### Subtasks
- [x] 1.1 Add `SearchOptions` struct to `internal/store/types.go` with `Source`, `ContentType`, and `SourceMatchMode` fields
- [x] 1.2 Add `FusedScore float64` field to `SearchResult` in `internal/store/types.go`
- [x] 1.3 Add `TitleWeight float64` to `StoreConfig` in `internal/config/config.go` (default 2.0); add `titleWeight float64` field to `ContentStore`; update `NewContentStore` to accept and store it
- [x] 1.4 Update `server.go` to pass `s.config.Store.TitleWeight` to `NewContentStore`; update `cmd/capy/cleanup.go` and test helpers similarly
- [x] 1.5 Add `execDynamicSearch(table, sanitized string, limit int, opts SearchOptions) []SearchResult` to `internal/store/search.go` — builds WHERE clause dynamically, uses `s.titleWeight` for BM25 weight
- [x] 1.6 Remove 4 prepared search statements (`stmtSearchPorter`, `stmtSearchPorterFiltered`, `stmtSearchTrigram`, `stmtSearchTrigramFiltered`) from `ContentStore` struct, `prepareStatements()`, and `Close()`; remove `execSearch`
- [x] 1.7 Update `searchPorter` and `searchTrigramQuery` to always use OR mode and delegate to `execDynamicSearch`
- [x] 1.8 Change `SearchWithFallback` signature from `(query, limit, source)` to `(query, limit, SearchOptions)` — update function body to read from `opts`
- [x] 1.9 Update all callers: `tool_search.go`, `tool_batch.go`, `intent_search.go` — pass `SearchOptions{Source: source}` to preserve existing behavior
- [x] 1.10 Update all tests in `internal/store/search_test.go` to use new signature and `NewContentStore` with titleWeight param
- [x] 1.11 Add config test: `TitleWeight` defaults to 2.0 when omitted; custom value loads from TOML
- [x] 1.12 Run all store and server tests to verify the refactor is clean

## Task 2: Reciprocal Rank Fusion (RRF)
- **Status:** pending
- **Depends on:** Task 1
- **Docs:** [implementation.md#2-reciprocal-rank-fusion-rrf](./implementation.md#2-reciprocal-rank-fusion-rrf)

### Subtasks
- [ ] 2.1 Add `rrfSearch(query string, limit int, opts SearchOptions) []SearchResult` to `internal/store/search.go` — runs **2 layers** (porter OR + trigram OR) **concurrently** (goroutines + WaitGroup) with `max(limit*2, 10)` fetch limit, builds fusion map keyed by `sourceID:title`, computes `1/(60+position)` scores (position = slice index, not BM25 rank float), sorts by fused score
- [ ] 2.2 Add `proximityRerank(results []SearchResult, query string) []SearchResult` — for 2+ word queries, extract match positions from FTS5 highlight markers (char(2)/char(3)) in `Highlighted` field, find minimum window via `findMinSpan`, apply content-length-normalized boost: `1 + 1/(1 + minSpan/contentLen)`. Fall back to `strings.Index` when `Highlighted` is empty.
- [ ] 2.3 Add helper functions: `findAllPositions(text, term string) []int` and `findMinSpan(positionLists [][]int) int` (sweep-line algorithm)
- [ ] 2.4 Add `mergeRRFResults(primary, secondary []SearchResult, limit int) []SearchResult` — deduplicates by `(sourceID, title)` key, keeps primary version on conflict, truncates to limit
- [ ] 2.5 Replace cascading loop in `SearchWithFallback` with: RRF pass → if < limit results, fuzzy correct → second RRF pass → merge via `mergeRRFResults`. Tag fuzzy results with `"fuzzy+"` prefix on MatchLayer
- [ ] 2.6 Write tests: RRF returns results from both layers; multi-layer hits rank above single-layer; fuzzy correction only triggers when RRF returns < limit; fuzzy results don't duplicate direct results
- [ ] 2.7 Write tests for proximity: multi-term query boosts close terms; single-term skips; content-length normalization works correctly; `findMinSpan` and `findAllPositions` unit tests
- [ ] 2.8 Write test: ContentType="code" returns only code chunks; empty contentType returns all (internal SearchOptions, not MCP schema)

## Task 3: Source metadata + TTL cache
- **Status:** pending
- **Depends on:** Task 1
- **Docs:** [implementation.md#3-source-metadata--ttl-cache](./implementation.md#3-source-metadata--ttl-cache)

### Subtasks
- [ ] 3.1 Add `SourceMeta` struct to `internal/store/types.go`
- [ ] 3.2 Add `stmtGetSourceMeta` prepared statement to `internal/store/store.go` — `SELECT label, chunk_count, indexed_at FROM sources WHERE label = ?`; add to `Close()` cleanup list
- [ ] 3.3 Implement `GetSourceMeta(label string) (*SourceMeta, error)` on `ContentStore` — returns `nil, nil` when not found
- [ ] 3.4 Add `CacheConfig` struct with `FetchTTLHours int` to `internal/config/config.go` under `StoreConfig`; default to 24 when unset
- [ ] 3.5 Add `force` boolean parameter to `toolFetchAndIndex()` in `internal/server/tools.go`
- [ ] 3.6 Add `formatAge(d time.Duration) string` helper in `internal/server/tool_fetch.go`
- [ ] 3.7 Add TTL cache logic at the start of `handleFetchAndIndex` in `internal/server/tool_fetch.go` — check `GetSourceMeta`, compare against configured TTL, return cache-hit when fresh and `!force`, call `stats.AddCacheHit`
- [ ] 3.8 Write tests in `internal/store/search_test.go`: `GetSourceMeta` returns nil for unknown label; returns correct metadata after indexing
- [ ] 3.9 Write tests in `internal/server/tool_knowledge_test.go`: second `fetch_and_index` within TTL returns cache hit; `force=true` bypasses cache; expired source re-fetches; stats track cache hit
- [ ] 3.10 Write config test: `FetchTTLHours` defaults to 24 when omitted; custom value is loaded from TOML

## Task 4: Batch execute — exact source scoping + remove global fallback
- **Status:** pending
- **Depends on:** Task 1, Task 2
- **Docs:** [implementation.md#4-batch-execute-exact-source-scoping](./implementation.md#4-batch-execute-exact-source-scoping)

### Subtasks
- [ ] 4.1 In `handleBatchExecute` (`internal/server/tool_batch.go`), change the scoped search to use `SearchOptions{Source: sourceLabel, SourceMatchMode: "exact"}`
- [ ] 4.2 Remove the Tier 2 global fallback entirely — delete the `crossSource` variable, the fallback `SearchWithFallback(query, 3, "")` call, the cross-source warning message, and the `_(source: ...)_` source tags
- [ ] 4.3 Append cross-batch search tip line to the batch output before the distinctive terms section
- [ ] 4.4 Write test: index content with a similar label beforehand, verify batch search doesn't leak results; verify no global fallback results appear when scoped search returns nothing

## Task 5: Empty index early return
- **Status:** pending
- **Depends on:** Task 1
- **Docs:** [implementation.md#5-empty-index-early-return](./implementation.md#5-empty-index-early-return)

### Subtasks
- [ ] 5.1 In `handleSearch` (`internal/server/tool_search.go`), after getting the store, call `st.Stats()` — if `SourceCount == 0`, return `isError` result with guidance message listing available indexing tools
- [ ] 5.2 Write test in `internal/server/tool_knowledge_test.go`: search on empty store returns isError response containing guidance text

## Task 6: Hook — smart curl/wget allowance
- **Status:** pending
- **Depends on:** —
- **Docs:** [implementation.md#6-hook-smart-curlwget-allowance](./implementation.md#6-hook-smart-curlwget-allowance)

### Subtasks
- [ ] 6.1 Add `splitChainedCommands(cmd string) []string` to `internal/hook/helpers.go` — splits on `&&`, `||`, `;` respecting quoted strings
- [ ] 6.2 Add `isCurlWgetSafe(segment string) bool` to `internal/hook/helpers.go` — checks for file-output flags only (`-o`/`--output` for curl, `-O`/`--output-document` for wget, or `>`/`>>`). Silent flags NOT required (matching TS behavior).
- [ ] 6.3 Update `routeBash` in `internal/hook/pretooluse.go` — when `isCurlOrWget(stripped)` is true, split into segments, check each with `isCurlWgetSafe`, only block if any segment is unsafe
- [ ] 6.4 Write tests: `curl -o file url` passes; `curl --output file url` passes; `curl url` blocked; `curl -o a url && curl b` blocked (mixed); `curl -o a url && curl -o b url` passes; `wget -O file url` passes; `wget url` blocked

## Task 7: TTL cache statistics
- **Status:** pending
- **Depends on:** Task 3
- **Docs:** [implementation.md#7-ttl-cache-statistics](./implementation.md#7-ttl-cache-statistics)

### Subtasks
- [ ] 7.1 Add `CacheHits int64` and `CacheBytesSaved int64` fields to `SessionStats` in `internal/server/stats.go`
- [ ] 7.2 Add `AddCacheHit(estimatedBytes int64)` method to `SessionStats` — atomically increments `CacheHits` and adds to `CacheBytesSaved`
- [ ] 7.3 Update `Snapshot()` in `internal/server/stats.go` to copy the new fields
- [ ] 7.4 Update top-level savings calculation in `handleStats` to include cache: `totalProcessed := keptOut + totalBytesReturned + snap.CacheBytesSaved`
- [ ] 7.5 Add TTL Cache section to `handleStats` — display when `snap.CacheHits > 0`, using configured TTL from config
- [ ] 7.6 Write test: call `AddCacheHit` twice, verify `Snapshot()` returns correct accumulated values; verify stats report includes cache section

## Task 8: Final verification
- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3, Task 4, Task 5, Task 6, Task 7

### Subtasks
- [ ] 8.1 Run `testing-process` skill to verify all tasks — full test suite with `-tags fts5`, integration tests, edge cases
- [ ] 8.2 Run `documentation-process` skill to update any relevant docs
- [ ] 8.3 Run `solid-code-review` skill with Go input to review the implementation
- [ ] 8.4 Run `implementation-review` skill to verify implementation matches design and implementation docs
