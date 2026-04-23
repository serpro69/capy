# Tasks: Upstream Sync — context-mode v1.0.54→v1.0.89+

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> Status: pending
> Created: 2026-04-23

## Task 1: Stopword-aware search pipeline
- **Status:** pending
- **Depends on:** —
- **Docs:** [implementation.md#stopword-pipeline](./implementation.md#stopword-pipeline)

### Subtasks
- [ ] 1.1 Add `filterNonStopwords(words []string) []string` helper in `internal/store/search.go` — filters via `IsStopword`, falls back to original if all are stopwords. Add `TestFilterNonStopwords` with mixed/all-stopword/empty cases.
- [ ] 1.2 Filter stopwords in `sanitizePorterQuery` (`search.go:~426`) — add `IsStopword` check inside the word loop before synonym expansion; fall back to all terms if filtering empties the groups. Add `TestSanitizePorterQuery_StopwordFiltering`.
- [ ] 1.3 Filter stopwords in `sanitizeTrigramQuery` (`search.go:~460`) — same pattern as porter, additive to existing `len(w) >= 3` check. Add `TestSanitizeTrigramQuery_StopwordFiltering`.
- [ ] 1.4 Rename `proximityRerank` → `rerank` in `search.go`; update the call site in `rrfSearch` (line ~172). Add content-type-aware title boost (code=0.6, prose=0.3) that applies to all queries including single-term. Apply `filterNonStopwords` to terms before building termGroups. Remove the early-return for `len(words) < 2`. Add tests: `TestRerank_TitleBoost_Code`, `TestRerank_TitleBoost_SingleTerm`, `TestRerank_StopwordFiltered`.
- [ ] 1.5 In `fuzzyCorrectQuery` (`search.go:~692`), add `IsStopword(word)` to the skip condition alongside `len(word) < 3`. Add `TestFuzzyCorrectQuery_SkipsStopwords`.

## Task 2: FTS5 periodic optimize
- **Status:** pending
- **Depends on:** —
- **Docs:** [implementation.md#fts5-optimize](./implementation.md#fts5-optimize)

### Subtasks
- [ ] 2.1 Add `insertCount atomic.Int64` field and `optimizeEvery` constant to `ContentStore` in `internal/store/store.go`. Add `optimizeFTS()` method that executes the FTS5 optimize command on both `chunks` and `chunks_trigram` tables (best-effort, log errors at Debug).
- [ ] 2.2 In `internal/store/index.go`, after each successful index transaction commit (`IndexContent`, `IndexPlainText`, `IndexJSON`), increment `insertCount` and call `optimizeFTS()` when threshold reached.
- [ ] 2.3 Call `optimizeFTS()` in `ContentStore.Close()` before closing prepared statements. Add `TestOptimizeFTS_RunsAfterThreshold` and `TestClose_OptimizesBeforeCheckpoint`.

## Task 3: mmap_size pragma
- **Status:** pending
- **Depends on:** —
- **Docs:** [implementation.md#mmap-pragma](./implementation.md#mmap-pragma)

### Subtasks
- [ ] 3.1 Append `&_mmap_size=268435456` to the DSN in `openDB()` (`internal/store/store.go`). Also add to the checkpoint DSN in `checkpoint()`. Add `TestOpenDB_MmapPragma` — open store, query `PRAGMA mmap_size`, verify value.

## Task 4: withRetry for SQLITE_BUSY + corrupt DB recovery
- **Status:** pending
- **Depends on:** —
- **Docs:** [implementation.md#with-retry](./implementation.md#with-retry), [implementation.md#corrupt-recovery](./implementation.md#corrupt-recovery)

### Subtasks
- [ ] 4.1 Create `internal/store/retry.go` — move `isBusy()` from `migrate.go`, add `isCorruptionError()`, `renameCorruptDB()`, generic `withRetry[T]()` and `withRetryVoid()`. Update imports in `migrate.go` and `cleanup.go`. Add tests: `TestWithRetry_RetriesOnBusy`, `TestWithRetry_NoRetryOnOtherError`, `TestWithRetry_ExhaustsRetries`, `TestIsCorruptionError`, `TestRenameCorruptDB`.
- [ ] 4.2 Wrap DB operations in `internal/store/index.go` (chunk insert exec, source-label query) and `internal/store/search.go` (`execDynamicSearch` query) with `withRetry`/`withRetryVoid`.
- [ ] 4.3 Add corrupt DB recovery in `openDB()` / `NewContentStore()` — detect corruption on schema exec or migration, rename corrupt files, retry once. Add `TestOpenDB_CorruptRecovery` and `TestOpenDB_CorruptRecovery_OnlyOnce`.

## Task 5: Final verification
- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3, Task 4

### Subtasks
- [ ] 5.1 Run `test` skill to verify all tasks — full test suite with `-tags fts5`, integration tests, edge cases
- [ ] 5.2 Run `document` skill to update any relevant docs
- [ ] 5.3 Run `review-code` skill with Go input to review the implementation
- [ ] 5.4 Run `review-spec` skill to verify implementation matches design and implementation docs
