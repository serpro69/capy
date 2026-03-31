# Tasks: Upstream Sync — context-mode v1.0.26→v1.0.54

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> Status: pending
> Created: 2026-03-31

## Task 1: SearchOptions struct + BM25 title weight
- **Status:** pending
- **Depends on:** —
- **Docs:** [implementation.md#1-search-options-struct--contenttype-filter](./implementation.md#1-search-options-struct--contenttype-filter)

### Subtasks
- [ ] 1.1 Add `SearchOptions` struct to `internal/store/types.go` with `Source`, `ContentType`, and `SourceMatchMode` fields
- [ ] 1.2 Add `FusedScore float64` field to `SearchResult` in `internal/store/types.go`
- [ ] 1.3 Change `SearchWithFallback` signature in `internal/store/search.go` from `(query, limit, source)` to `(query, limit, SearchOptions)` — update the function body to read from `opts`
- [ ] 1.4 Update all callers of `SearchWithFallback`: `internal/server/tool_search.go` (`handleSearch`), `internal/server/tool_batch.go` (`handleBatchExecute`) — pass `SearchOptions{Source: source}` to preserve existing behavior
- [ ] 1.5 Update all tests in `internal/store/search_test.go` to use the new `SearchOptions` signature
- [ ] 1.6 Change all 4 BM25 weights in `internal/store/store.go` `prepareStatements` from `bm25(chunks, 2.0, 1.0)` to `bm25(chunks, 5.0, 1.0)` and `bm25(chunks_trigram, 2.0, 1.0)` to `bm25(chunks_trigram, 5.0, 1.0)`
- [ ] 1.7 Run all store and server tests to verify the refactor is clean

## Task 2: Reciprocal Rank Fusion (RRF)
- **Status:** pending
- **Depends on:** Task 1
- **Docs:** [implementation.md#2-reciprocal-rank-fusion-rrf](./implementation.md#2-reciprocal-rank-fusion-rrf)

### Subtasks
- [ ] 2.1 Add `rrfSearch(query string, limit int, opts SearchOptions) []SearchResult` to `internal/store/search.go` — runs all 4 layers **concurrently** (goroutines + WaitGroup) with `max(limit*2, 10)` fetch limit, builds fusion map keyed by `sourceID:title`, computes `1/(60+position)` scores (position = slice index, not BM25 rank float), sorts by fused score
- [ ] 2.2 Add `proximityRerank(results []SearchResult, query string) []SearchResult` to `internal/store/search.go` — for 2+ word queries, extract match positions from FTS5 highlight markers (char(2)/char(3)) in `Highlighted` field, find minimum window, apply `1 + 1/(1+minDist/100)` boost. Fall back to `strings.Index` on content when `Highlighted` is empty.
- [ ] 2.3 Add `mergeRRFResults(primary, secondary []SearchResult, limit int) []SearchResult` — deduplicates by `(sourceID, title)` key, keeps primary version on conflict, truncates to limit
- [ ] 2.4 Replace cascading loop in `SearchWithFallback` with: RRF pass → if < limit results, fuzzy correct → second RRF pass → merge via `mergeRRFResults`. Tag fuzzy results with `"fuzzy+"` prefix on MatchLayer
- [ ] 2.5 Update `searchPorter` and `searchTrigramQuery` to accept dynamic limit (they already do via parameter — verify the `fetchLimit` flows through correctly)
- [ ] 2.6 Write tests in `internal/store/search_test.go`: RRF returns results blended from multiple layers; multi-layer hits rank above single-layer hits; fuzzy correction only triggers when RRF returns < limit; fuzzy results don't duplicate direct results
- [ ] 2.7 Write tests for proximity reranking: multi-term query boosts close terms; single-term skips reranking; all terms absent from content gives boost=1.0; highlight-marker-based positions match expected offsets

## Task 3: ContentType filter on search
- **Status:** pending
- **Depends on:** Task 2
- **Docs:** [implementation.md#1-search-options-struct--contenttype-filter](./implementation.md#1-search-options-struct--contenttype-filter) (section 1.3)

### Subtasks
- [ ] 3.1 Add dynamic SQL builder helpers in `internal/store/search.go`: `searchPorterDynamic(query, limit, opts)` and `searchTrigramDynamic(query, limit, opts)` that build WHERE clauses with optional `content_type = ?` and source match mode (`=` vs `LIKE`)
- [ ] 3.2 Update `rrfSearch` to use dynamic helpers when `opts.ContentType != ""` or `opts.SourceMatchMode == "exact"`, falling back to prepared statements for the hot path (no filters)
- [ ] 3.3 Add `contentType` parameter to `toolSearch()` in `internal/server/tools.go` — `mcp.WithString("contentType", mcp.Enum("code", "prose"))`
- [ ] 3.4 Pass `contentType` through in `handleSearch` (`internal/server/tool_search.go`) via `SearchOptions`
- [ ] 3.5 Write tests: `ContentType="code"` returns only code chunks; `ContentType="prose"` returns only prose chunks; empty contentType returns all

## Task 4: Source metadata + TTL cache
- **Status:** pending
- **Depends on:** Task 1
- **Docs:** [implementation.md#3-source-metadata--ttl-cache](./implementation.md#3-source-metadata--ttl-cache)

### Subtasks
- [ ] 4.1 Add `SourceMeta` struct to `internal/store/types.go`
- [ ] 4.2 Add `stmtGetSourceMeta` prepared statement to `internal/store/store.go` — `SELECT label, chunk_count, indexed_at FROM sources WHERE label = ?`; add to `Close()` cleanup list
- [ ] 4.3 Implement `GetSourceMeta(label string) (*SourceMeta, error)` on `ContentStore` in `internal/store/search.go` — returns `nil, nil` when not found
- [ ] 4.4 Add `IsEmpty() (bool, error)` to `internal/store/cleanup.go` — `SELECT COUNT(*) FROM sources`
- [ ] 4.5 Add `CacheConfig` struct with `FetchTTLHours int` to `internal/config/config.go` under `StoreConfig`; default to 24 when unset
- [ ] 4.6 Add `force` boolean parameter to `toolFetchAndIndex()` in `internal/server/tools.go`
- [ ] 4.7 Add TTL cache logic at the start of `handleFetchAndIndex` in `internal/server/tool_fetch.go` — check `GetSourceMeta`, compare against configured TTL from `s.config.Store.Cache.FetchTTLHours`, return cache-hit when fresh and `!force`, call `stats.AddCacheHit`
- [ ] 4.8 Add `CleanupStaleSources(maxAgeDays int) (int, error)` to `internal/store/cleanup.go` — deletes sources where `last_accessed_at` is older than `maxAgeDays` regardless of access_count (unlike existing `Cleanup` which requires `access_count = 0`); execute chunk + trigram + source deletes in a single transaction
- [ ] 4.9 Write tests in `internal/store/search_test.go`: `GetSourceMeta` returns nil for unknown label; returns correct label/chunkCount/indexedAt after indexing
- [ ] 4.10 Write tests in `internal/store/cleanup_test.go`: `IsEmpty` returns true on fresh DB, false after indexing; `CleanupStaleSources` removes old sources but preserves recently accessed ones
- [ ] 4.11 Write tests in `internal/server/tool_knowledge_test.go`: second `fetch_and_index` within TTL returns cache hit text; `force=true` bypasses cache; stats track cache hit
- [ ] 4.12 Write config test: `FetchTTLHours` defaults to 24 when omitted; custom value is loaded from TOML

## Task 5: Batch execute — exact source scoping + remove global fallback
- **Status:** pending
- **Depends on:** Task 1, Task 3
- **Docs:** [implementation.md#4-batch-execute-exact-source-scoping](./implementation.md#4-batch-execute-exact-source-scoping)

### Subtasks
- [ ] 5.1 In `handleBatchExecute` (`internal/server/tool_batch.go`), change the scoped search to use `SearchOptions{Source: sourceLabel, SourceMatchMode: "exact"}`
- [ ] 5.2 Remove the Tier 2 global fallback entirely — delete the `crossSource` variable, the fallback `SearchWithFallback(query, 3, "")` call, the cross-source warning message, and the `_(source: ...)_` source tags
- [ ] 5.3 Append cross-batch search tip line to the batch output before the distinctive terms section
- [ ] 5.4 Write test in `internal/server/tool_batch_test.go`: index content with a similar label beforehand, verify batch search doesn't leak results from the similarly-labeled source; verify no global fallback results appear when scoped search returns nothing

## Task 6: Empty index early return
- **Status:** pending
- **Depends on:** Task 4
- **Docs:** [implementation.md#5-empty-index-early-return](./implementation.md#5-empty-index-early-return)

### Subtasks
- [ ] 6.1 In `handleSearch` (`internal/server/tool_search.go`), after getting the store, call `st.IsEmpty()` — if true, return `errorResult` with guidance message listing available indexing tools
- [ ] 6.2 Write test in `internal/server/tool_knowledge_test.go`: search on empty store returns isError response containing guidance text

## Task 7: Hook — smart curl/wget allowance
- **Status:** pending
- **Depends on:** —
- **Docs:** [implementation.md#6-hook-smart-curlwget-allowance](./implementation.md#6-hook-smart-curlwget-allowance)

### Subtasks
- [ ] 7.1 Add `splitChainedCommands(cmd string) []string` to `internal/hook/helpers.go` — splits on `&&`, `||`, `;` respecting quoted strings
- [ ] 7.2 Add `isCurlWgetSafe(segment string) bool` to `internal/hook/helpers.go` — checks for file-output flags (`-o`/`--output` for curl, `-O`/`--output-document` for wget, or `>`/`>>`) AND silent flags (`-s`/`--silent` for curl, `-q`/`--quiet` for wget)
- [ ] 7.3 Update `routeBash` in `internal/hook/pretooluse.go` — when `isCurlOrWget(stripped)` is true, split into segments, check each segment with `isCurlWgetSafe`, only block if any segment is unsafe
- [ ] 7.4 Write tests in `internal/hook/hook_test.go`: `curl -sL url -o file.tar.gz` passes through; `curl url` blocked; `curl -o file url && curl api` blocked (mixed); `wget -q -O file url` passes; chained safe commands all pass

## Task 8: TTL cache statistics
- **Status:** pending
- **Depends on:** Task 4
- **Docs:** [implementation.md#7-ttl-cache-statistics](./implementation.md#7-ttl-cache-statistics)

### Subtasks
- [ ] 8.1 Add `CacheHits int64` and `CacheBytesSaved int64` fields to `SessionStats` in `internal/server/stats.go`
- [ ] 8.2 Add `AddCacheHit(estimatedBytes int64)` method to `SessionStats` — atomically increments `CacheHits` and adds to `CacheBytesSaved`
- [ ] 8.3 Update `Snapshot()` in `internal/server/stats.go` to copy the new fields
- [ ] 8.4 Update top-level savings calculation in `handleStats` (`internal/server/tool_stats.go`) to include cache: `totalProcessed := keptOut + totalBytesReturned + snap.CacheBytesSaved`
- [ ] 8.5 Add TTL Cache section to `handleStats` — display when `snap.CacheHits > 0`: cache hits, data avoided, network requests saved, TTL remaining (using configured TTL from config, not hardcoded 24h)
- [ ] 8.6 Write test: call `AddCacheHit` twice, verify `Snapshot()` returns correct accumulated values; verify stats report includes cache section

## Task 9: Final verification
- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3, Task 4, Task 5, Task 6, Task 7, Task 8

### Subtasks
- [ ] 9.1 Run `testing-process` skill to verify all tasks — full test suite with `-tags fts5`, integration tests, edge cases
- [ ] 9.2 Run `documentation-process` skill to update any relevant docs
- [ ] 9.3 Run `solid-code-review` skill with Go input to review the implementation
- [ ] 9.4 Run `implementation-review` skill to verify implementation matches design and implementation docs
