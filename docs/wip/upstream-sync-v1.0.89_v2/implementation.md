# Implementation: Upstream Sync — context-mode v1.0.54→v1.0.89+

> **Design:** [./design.md](./design.md)

## 1. Stopword-Aware Search Pipeline {#stopword-pipeline}

All changes in this section touch `internal/store/search.go`.

### 1.1 Add `filterNonStopwords` helper

Add a package-level helper used by three call sites:

```
func filterNonStopwords(words []string) []string
```

- Iterate words, keep those where `!IsStopword(strings.ToLower(w))`
- Return filtered slice; return original words if filtering removes everything (all-stopword queries still need to produce *some* FTS5 query)
- Place near the top of `search.go` alongside existing helpers (`ftsSpecialRe`, `trigramCleanRe`)

**Verify:** Unit test — `TestFilterNonStopwords` with cases: mixed words, all stopwords, no stopwords, empty input.

### 1.2 Filter stopwords in `sanitizePorterQuery`

In `sanitizePorterQuery` (line ~426):

- After the `for _, w := range words` loop that builds `groups`, the stopword check should be *inside* the loop, before synonym expansion
- At the top of the loop body, before the `expandSyns` branch: check `IsStopword(strings.ToLower(w))` and `continue` if true
- Collect all groups as before; the loop simply skips stopword entries
- After the loop, if `groups` is empty (all words were stopwords), re-run the loop WITHOUT the stopword filter (fall back to all terms)
- Alternatively: build two slices in one pass (meaningful groups + all groups), use meaningful if non-empty

**Verify:** Unit test — `TestSanitizePorterQuery_StopwordFiltering`:
- `"update authentication"` → should produce query containing "authentication" but not "update"
- `"update fix added"` (all stopwords) → should produce query with all three terms (fallback)
- Existing synonym expansion tests still pass

### 1.3 Filter stopwords in `sanitizeTrigramQuery`

In `sanitizeTrigramQuery` (line ~460):

- Same pattern as porter: add `IsStopword(strings.ToLower(w))` check inside the loop, before synonym expansion
- The existing `len(w) >= 3` filter stays; stopword check is additive
- Same all-stopword fallback

**Verify:** Unit test — `TestSanitizeTrigramQuery_StopwordFiltering`: same cases as porter.

### 1.4 Filter stopwords in reranking + add title boost

Rename `proximityRerank` → `rerank`. Update the single call site in `rrfSearch` (line ~172).

**Title boost (new, applies to all queries including single-term):**

After computing `terms` from the query (with stopword filtering):

1. For each result, lowercase `r.Title`
2. Count `titleHits` — terms that appear in the title
3. Determine `titleWeight` by `r.ContentType`: `"code"` → 0.6, default → 0.3
4. `titleBoost = titleWeight * (float64(titleHits) / float64(len(terms)))` if `titleHits > 0`, else 0

**Proximity boost (existing, stopword-filtered, 2+ terms only):**

- Apply `filterNonStopwords` to the `words` slice before building `termGroups`
- The rest of the proximity logic (highlight extraction, findMinSpan, content-length normalization) stays unchanged

**Combined score:**

```
r.FusedScore *= (1.0 + titleBoost + proximityBoost)
```

Re-sort by boosted score, ties broken by `r.Rank` (ascending — lower BM25 rank is better).

**Remove the early return for single-term queries** (`if len(words) < 2 { return results }`) — title boost applies to single-term queries too.

**Verify:**
- `TestRerank_TitleBoost_Code` — code chunk with query term in title gets higher boost than prose chunk with same term in title
- `TestRerank_TitleBoost_SingleTerm` — single-term query gets title boost (previously returned unranked)
- `TestRerank_StopwordFiltered` — proximity calculation excludes stopwords
- Existing proximity tests still pass (rename function reference)

### 1.5 Skip fuzzy correction for stopwords

In `fuzzyCorrectQuery` (line ~692):

- Change the loop condition from `if len(word) < 3` to `if len(word) < 3 || IsStopword(word)`
- Stopword terms are appended to `result` unchanged (same as short words)

**Verify:** Unit test — `TestFuzzyCorrectQuery_SkipsStopwords`:
- Query `"updaet authentication"` — "updaet" is not a stopword so it gets corrected; but if "update" were the input (it IS a stopword), it should be skipped
- Performance: no measurable test, but the code path is visibly shorter

---

## 2. FTS5 Periodic Optimize {#fts5-optimize}

Changes to `internal/store/store.go` and `internal/store/index.go`.

### 2.1 Add optimize infrastructure to ContentStore

In `internal/store/store.go`:

- Add field: `insertCount atomic.Int64` to `ContentStore` struct
- Add constant: `optimizeEvery int64 = 50`
- Add method `optimizeFTS()`:
  - Execute `INSERT INTO chunks(chunks) VALUES('optimize')`
  - Execute `INSERT INTO chunks_trigram(chunks_trigram) VALUES('optimize')`
  - Both best-effort: log errors at Debug level, don't return them
  - Acquires its own DB connection via `s.db.Exec()` — no prepared statement needed

### 2.2 Trigger optimize after indexing

In `internal/store/index.go`, after each successful index operation (`IndexContent`, `IndexPlainText`, `IndexJSON`) — specifically after the transaction commits:

- `s.insertCount.Add(1)` 
- `if s.insertCount.Load() % optimizeEvery == 0 { s.optimizeFTS() }`

### 2.3 Optimize on Close

In `ContentStore.Close()` (`store.go`), call `s.optimizeFTS()` before closing prepared statements. This defragments the index one final time before WAL checkpoint.

**Verify:**
- `TestOptimizeFTS_RunsAfterThreshold` — index 50 items, verify optimize ran (mock or check that no error occurred)
- `TestOptimizeFTS_BestEffort` — corrupt table name doesn't panic or return error
- `TestClose_OptimizesBeforeCheckpoint` — Close() path includes optimize

---

## 3. mmap_size Pragma {#mmap-pragma}

Changes to `internal/store/store.go`.

### 3.1 Add mmap_size to DSN

In `openDB()`, append `_mmap_size=268435456` to the DSN query parameters. The DSN is currently built as:

```
file:<path>?_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL
```

Add `&_mmap_size=268435456` to this string. `mattn/go-sqlite3` passes DSN pragmas via `PRAGMA` on each new connection.

Also add to the checkpoint DSN in `checkpoint()` (line ~292) for consistency — though checkpoint is a single-shot operation, having mmap enabled doesn't hurt and may speed up the checkpoint read phase.

**Verify:**
- `TestOpenDB_MmapPragma` — open a store, query `PRAGMA mmap_size`, verify it returns 268435456 (or the file size if smaller)
- Existing store tests still pass (mmap is transparent to application logic)

---

## 4. withRetry for SQLITE_BUSY {#with-retry}

New file `internal/store/retry.go`, changes to `migrate.go`, `cleanup.go`, `index.go`.

### 4.1 Create `retry.go`

Move `isBusy()` from `migrate.go` to new `internal/store/retry.go`. Add:

- `isCorruptionError(err error) bool` — checks `sqlite3.ErrCorrupt`, `sqlite3.ErrNotADB`, and string patterns "database disk image is malformed", "file is not a database"
- `renameCorruptDB(dbPath string)` — renames `db`, `db-wal`, `db-shm` to `<path>.corrupt-<unix-ms>`, ignoring `os.ErrNotExist`
- Generic retry helper:

```
func withRetry[T any](fn func() (T, error), delays ...time.Duration) (T, error)
```

Default delays: `[100ms, 500ms, 2000ms]`. Retries only on `isBusy(err)`. Returns the first non-busy error immediately. Uses `time.Sleep` (not busy-wait).

Also add a void variant for operations that return only `error`:

```
func withRetryVoid(fn func() error, delays ...time.Duration) error
```

### 4.2 Update `migrate.go`

- Remove `isBusy()` function definition
- Import from same package (it's in the same `store` package, so just delete the duplicate)
- `beginImmediate()` stays — it has its own retry loop with different characteristics (10 retries, 10ms base, for write-lock acquisition)

### 4.3 Update `cleanup.go`

- Remove any local `isBusy` reference if duplicated; otherwise no change (cleanup uses `beginImmediate` which has its own retry)

### 4.4 Wrap index operations

In `internal/store/index.go`, the `insertChunks` function (or the transaction that inserts chunks) should be wrapped:

- The `beginImmediate` call already has retry for lock acquisition
- Wrap the `tx.Exec` calls for individual chunk inserts with `withRetryVoid` — these can fail under concurrent read contention from search goroutines
- Wrap `stmtFindSourceByLabel.QueryRow` in content-hash dedup path — this is a read that can hit SQLITE_BUSY under WAL contention

### 4.5 Wrap search operations

In `internal/store/search.go`, `execDynamicSearch`:

- The `db.Query()` call that runs the FTS5 MATCH can hit SQLITE_BUSY under write contention
- Wrap with `withRetry` — the query returns `*sql.Rows`, so use the generic form

**Verify:**
- `TestWithRetry_RetriesOnBusy` — mock function that fails with SQLITE_BUSY twice then succeeds
- `TestWithRetry_NoRetryOnOtherError` — non-busy error returned immediately
- `TestWithRetry_ExhaustsRetries` — all retries fail, returns final error with context
- `TestIsBusy` — covers typed `sqlite3.Error` and string-based detection (already exists in migrate_test.go; move or duplicate)

---

## 5. Corrupt DB Detection and Recovery {#corrupt-recovery}

Changes to `internal/store/store.go` and `internal/store/retry.go`.

### 5.1 Add corruption detection and rename (already in retry.go from §4.1)

`isCorruptionError` and `renameCorruptDB` are defined in §4.1.

### 5.2 Add recovery in `openDB`

In `openDB()` or `NewContentStore()`, after the initial `db.Exec(schemaSQL)` and `applyMigrations(db)` calls:

- If either returns an error where `isCorruptionError(err.Error())` is true:
  1. Close the failed DB connection
  2. Call `renameCorruptDB(dbPath)` 
  3. Log warning: `slog.Warn("knowledge DB was corrupt, renamed and recreated", "path", dbPath)`
  4. Retry `openDB` exactly once (recursive call with a `recovered bool` flag to prevent infinite loops)
- If the retry also fails, return the error normally

**Verify:**
- `TestOpenDB_CorruptRecovery` — create a file with garbage bytes at the DB path, open store → should rename the corrupt file and create a fresh DB
- `TestOpenDB_CorruptRecovery_OnlyOnce` — if the retry also produces corruption (e.g., disk full), return error instead of looping
- `TestRenameCorruptDB` — renames .db, .db-wal, .db-shm; ignores missing WAL/SHM files
- `TestIsCorruptionError` — covers all four error patterns

---

## 6. Testing Strategy

All changes should have unit tests in the corresponding `_test.go` files. Integration testing via existing `internal/server/integration_test.go` covers the end-to-end search path.

**Build tag:** All tests require `-tags fts5` (per existing project convention).

**Test data:** Use the existing test helpers (`testStore()`, `newTestDB()`) for store tests. For corrupt DB tests, write known-bad bytes to a temp file.

**Benchmarks:** Consider adding `BenchmarkSearch_WithStopwords` vs `BenchmarkSearch_WithoutStopwords` to quantify the improvement from stopword filtering. This is optional but useful for validating the P0 claims.
