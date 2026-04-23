# Tasks: Upstream Sync — context-mode v1.0.54→v1.0.89

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> Status: pending
> Created: 2026-04-22

## Task 1: Shared query term helper + stopword filtering + dedup
- **Status:** done
- **Depends on:** —
- **Docs:** [implementation.md#1-shared-query-term-helper--stopword-filtering--dedup](./implementation.md#1-shared-query-term-helper--stopword-filtering--dedup)

### Subtasks
- [x] 1.1 Add `filterQueryTerms(query string) []string` to `internal/store/search.go` — strips FTS5 special chars, splits, lowercases, deduplicates case-insensitively, filters stopwords via `IsStopword`, falls back to unfiltered list if all removed
- [x] 1.2 Update `sanitizePorterQuery` in `internal/store/search.go` to use `filterQueryTerms` for initial word extraction, then apply synonym expansion and group building on the filtered terms
- [x] 1.3 Update `sanitizeTrigramQuery` in `internal/store/search.go` to use `filterQueryTerms` for initial word extraction, then apply `trigramCleanRe` and ≥3-char filter on surviving terms
- [x] 1.4 Write tests in `internal/store/search_test.go`: query with stopwords produces FTS5 query without them; duplicate-cased terms deduplicate; all-stopword query falls back to unfiltered; existing search tests still pass
- [x] 1.5 Verify: `go test -tags fts5 -race ./internal/store/...` — all pass

## Task 2: Skip fuzzy correction on stopwords
- **Status:** done
- **Depends on:** Task 1
- **Docs:** [implementation.md#2-skip-fuzzy-correction-on-stopwords](./implementation.md#2-skip-fuzzy-correction-on-stopwords)

### Subtasks
- [x] 2.1 In `fuzzyCorrectQuery` in `internal/store/search.go`, add `IsStopword(word)` check after the `len(word) < 3` guard — skip the word (append as-is) without calling `fuzzyCorrectWord`
- [x] 2.2 Write test in `internal/store/search_test.go`: `fuzzyCorrectQuery` with mixed stopwords and a typo corrects only the typo and leaves stopwords untouched
- [x] 2.3 Verify: `go test -tags fts5 -race ./internal/store/...` — all pass

## Task 3: Title-match boost in proximity reranking
- **Status:** pending
- **Depends on:** Task 1
- **Docs:** [implementation.md#3-title-match-boost-in-proximity-reranking](./implementation.md#3-title-match-boost-in-proximity-reranking)

### Subtasks
- [ ] 3.1 Rename `proximityRerank` → `rerank` in `internal/store/search.go` and update call site in `rrfSearch` — replace `words` extraction with `filterQueryTerms(query)` call; move the `len < 2` early-return to guard only the proximity span section (not title-match boost)
- [ ] 3.2 Add title-match boost inside the `for i := range results` loop — count term hits in lowercased title, apply content-type-aware weight (code: 0.6, prose: 0.3), multiply `FusedScore *= (1.0 + titleBoost)`
- [ ] 3.3 Write tests in `internal/store/search_test.go`: single-term query boosts code chunk with matching title over one with generic title; multi-term query combines title and proximity boosts; prose chunks get lower title weight than code chunks
- [ ] 3.4 Verify: `go test -tags fts5 -race ./internal/store/...` — all pass, existing proximity tests unchanged

## Task 4: Fuzzy correction cache
- **Status:** pending
- **Depends on:** Task 2
- **Docs:** [implementation.md#4-fuzzy-correction-cache](./implementation.md#4-fuzzy-correction-cache)

### Subtasks
- [ ] 4.1 Add `fuzzyCacheMu sync.Mutex` and `fuzzyCache map[string]*string` fields to `ContentStore` in `internal/store/store.go`; initialize map in `NewContentStore`; add `fuzzyCacheMaxSize = 256` constant
- [ ] 4.2 Add cache read at top of `fuzzyCorrectWord` in `internal/store/search.go` — lock, check map, unlock, return on hit
- [ ] 4.3 Add cache write before return in `fuzzyCorrectWord` — lock, evict-all if over max, store `*string` (nil for no correction), unlock
- [ ] 4.4 Add cache invalidation at end of `extractAndStoreVocabulary` in `internal/store/index.go` — lock, replace map with fresh empty map, unlock
- [ ] 4.5 Write tests in `internal/store/search_test.go`: second fuzzy call for same word uses cache; cache cleared after vocab insert; cache size cap triggers eviction
- [ ] 4.6 Verify: `go test -tags fts5 -race ./internal/store/...` — all pass (race detector catches any mutex issues)

## Task 5: Periodic FTS5 optimize
- **Status:** pending
- **Depends on:** —
- **Docs:** [implementation.md#5-periodic-fts5-optimize](./implementation.md#5-periodic-fts5-optimize)

### Subtasks
- [ ] 5.1 Add `insertCount atomic.Int64` field to `ContentStore` in `internal/store/store.go`; add `optimizeEvery int64 = 50` constant
- [ ] 5.2 Add `optimizeFTS()` method to `ContentStore` — runs `INSERT INTO chunks(chunks) VALUES ('optimize')` and same for `chunks_trigram`, logs warning on failure
- [ ] 5.3 In `Index` in `internal/store/index.go`, after successful `tx.Commit()`, increment via `s.insertCount.Add(int64(len(chunks)))` and call `s.optimizeFTS()` + `s.insertCount.Store(0)` when threshold reached
- [ ] 5.4 Write tests in `internal/store/search_test.go` or new `internal/store/optimize_test.go`: index enough chunks to trigger optimize, verify no error; verify optimize failure doesn't break indexing
- [ ] 5.5 Verify: `go test -tags fts5 -race ./internal/store/...` — all pass

## Task 6: mmap_size pragma
- **Status:** pending
- **Depends on:** —
- **Docs:** [implementation.md#6-mmap_size-pragma](./implementation.md#6-mmap_size-pragma)

### Subtasks
- [ ] 6.1 In `getDB()` in `internal/store/store.go`, add `db.Exec("PRAGMA mmap_size = 268435456")` after `sql.Open` and before `db.Exec(schemaSQL)` — log warning on failure (non-fatal)
- [ ] 6.2 Write test in `internal/store/store_test.go`: after store init, query `PRAGMA mmap_size` returns non-zero value
- [ ] 6.3 Verify: `go test -tags fts5 -race ./internal/store/...` — all pass

## Task 7: Corrupt DB detection and recovery
- **Status:** pending
- **Depends on:** Task 6 (mmap pragma is part of the open sequence)
- **Docs:** [implementation.md#7-corrupt-db-detection-and-recovery](./implementation.md#7-corrupt-db-detection-and-recovery)

### Subtasks
- [ ] 7.1 Create `internal/store/retry.go` — move `isBusy` from `migrate.go`, add `isSQLiteCorruption(err error) bool` (checks `"malformed"`, `"not a database"`, `"corrupt"`, `"disk image is malformed"`), add `backupCorruptDB(dbPath string)` (renames `.db`, `.db-wal`, `.db-shm` to `.corrupt.<timestamp>`, ignores missing WAL/SHM)
- [ ] 7.2 Extract the open sequence in `getDB()` into `openDB() (*sql.DB, error)` — covers sql.Open → mmap pragma → schemaSQL → migrations → prepareStatements
- [ ] 7.3 Update `getDB()` to call `openDB()`, and on corruption error + file exists, call `backupCorruptDB()` then retry `openDB()` once; propagate error on retry failure
- [ ] 7.4 Write tests: garbage file at DB path triggers recovery and produces working store; `.corrupt.<timestamp>` backup file exists; non-corruption error doesn't trigger recovery; retry failure propagates; `isBusy` still works from retry.go (move test if needed)
- [ ] 7.5 Verify: `go test -tags fts5 -race ./internal/store/...` — all pass

## Task 8: Final verification
- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3, Task 4, Task 5, Task 6, Task 7

### Subtasks
- [ ] 8.1 Run `test` skill — full test suite with `-tags fts5 -race`, all pass
- [ ] 8.2 Run `document` skill to update any relevant docs
- [ ] 8.3 Run `review-code` skill with Go input to review the implementation
- [ ] 8.4 Run `review-spec` skill to verify implementation matches design and implementation docs
