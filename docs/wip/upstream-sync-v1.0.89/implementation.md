# Implementation: Upstream Sync — context-mode v1.0.54→v1.0.89

> Design: [./design.md](./design.md)
> Tasks: [./tasks.md](./tasks.md)

## 1. Shared query term helper + stopword filtering + dedup {#stopword-pipeline}

**Files:** `internal/store/search.go`

### 1.1 Add `filterQueryTerms`

Add a new function to `search.go`:

```
func filterQueryTerms(query string) []string
```

Steps:
- Strip FTS5 special chars using the existing `ftsSpecialRe`
- Split on whitespace, lowercase each word
- Deduplicate: track seen lowercased forms in a `map[string]bool`, keep first occurrence only
- Filter stopwords via existing `IsStopword`
- Fallback: if filtering removes all words, return the pre-filter (deduplicated) list

This function is reused by `sanitizePorterQuery`, `sanitizeTrigramQuery`, and `proximityRerank`.

### 1.2 Update `sanitizePorterQuery`

Current flow: split words → filter FTS5 keywords → optionally expand synonyms → join.

New flow: call `filterQueryTerms(query)` to get cleaned terms → for each term, optionally expand synonyms → build quoted groups → join with mode separator.

The FTS5 keyword check (`ftsKeywords`) is redundant after `filterQueryTerms` since FTS5 keywords are either stopwords (AND, OR, NOT) or are already stripped. Keep the check as a safety belt — it's cheap.

### 1.3 Update `sanitizeTrigramQuery`

Same pattern as porter: use `filterQueryTerms` for the initial word list, then apply the trigram-specific `trigramCleanRe` and ≥3-char filter on each term before building the query string.

Note: trigram needs its own regex cleaning (`trigramCleanRe` strips non-alphanumeric chars differently from `ftsSpecialRe`). Apply `filterQueryTerms` first (stopword + dedup), then `trigramCleanRe` on each surviving term.

### Verify

- Run `go test -tags fts5 ./internal/store/ -run TestSearch` — existing search tests pass
- New tests: query `"the error in the code"` produces FTS5 query with only `"error"` (stopwords removed); query `"Error error ERROR"` deduplicates to single `"error"` term; all-stopword query `"the and for"` falls back to unfiltered terms

---

## 2. Skip fuzzy correction on stopwords {#fuzzy-stopword-skip}

**File:** `internal/store/search.go`, function `fuzzyCorrectQuery`

### 2.1 Add stopword check

In the word loop of `fuzzyCorrectQuery`, after the existing `len(word) < 3` early-continue, add:

```
if IsStopword(word) {
    result = append(result, word)
    continue
}
```

This skips the DB query and levenshtein scan for stopwords.

### Verify

- New test: `fuzzyCorrectQuery("the errro in code")` does NOT attempt correction on "the", "in" — only on "errro" (corrected) and "code" (exact match in vocab, no correction)
- Existing fuzzy tests still pass

---

## 3. Title-match boost in reranking {#title-boost}

**File:** `internal/store/search.go`, function `proximityRerank` → renamed to `rerank`

### 3.1 Rename and add title-match boost

Rename `proximityRerank` to `rerank` — the function now handles title-match boost and stopword filtering beyond just proximity. Update the call site in `rrfSearch`.

At the start of `rerank`, after computing `words` from the query, use `filterQueryTerms` to get non-stopword deduplicated terms. Then inside the `for i := range results` loop, before the existing proximity span calculation:

1. Lowercase `results[i].Title`
2. Count how many terms from `filterQueryTerms` appear in the lowercased title (`titleHits`)
3. Determine weight from `results[i].ContentType`: `"code"` → `0.6`, everything else → `0.3`
4. Compute: `titleBoost = weight * (float64(titleHits) / float64(len(terms)))` where `terms` is the filtered term list
5. Apply: `r.FusedScore *= (1.0 + titleBoost)`

The existing proximity boost then multiplies on top of the already-boosted score.

### 3.2 Guard against single-term queries

The existing `if len(words) < 2 { return results }` early-return should be re-evaluated. Title-match boost is valuable even for single-term queries. Move the early-return to only guard the proximity span calculation (multi-term only), not the title-match boost.

Updated structure:
```
terms := filterQueryTerms(query)
if len(terms) == 0 { return results }

// Title-match boost (applies to all queries, including single-term)
for i := range results { ... titleBoost ... }

// Proximity span boost (multi-term only)
if len(terms) >= 2 {
    // existing proximity logic, using terms instead of words
}

// Re-sort
sort.Slice(...)
```

### Verify

- New test: single-term query `"ContentStore"` boosts a code chunk titled `"ContentStore"` above one titled `"Lines 1-20"` with same content
- New test: multi-term query `"content store"` — code chunk with title containing "ContentStore" gets both title boost and proximity boost
- Existing proximity tests still pass (proximity logic unchanged, just augmented)

---

## 4. Fuzzy correction cache {#fuzzy-cache}

**Files:** `internal/store/store.go` (struct + init), `internal/store/search.go` (read/write), `internal/store/index.go` (invalidation)

### 4.1 Add cache fields to `ContentStore`

In `store.go`, add to the `ContentStore` struct:

```
fuzzyCacheMu sync.Mutex
fuzzyCache   map[string]*string  // nil value = "no correction"; key-missing = "not cached"
```

Initialize `fuzzyCache` to `make(map[string]*string)` in `NewContentStore`.

Add a constant: `const fuzzyCacheMaxSize = 256`

### 4.2 Read path in `fuzzyCorrectWord`

At the top of `fuzzyCorrectWord` in `search.go`, before the DB query:

```
s.fuzzyCacheMu.Lock()
if cached, ok := s.fuzzyCache[word]; ok {
    s.fuzzyCacheMu.Unlock()
    if cached == nil { return "" }
    return *cached
}
s.fuzzyCacheMu.Unlock()
```

### 4.3 Write path in `fuzzyCorrectWord`

After computing the result (whether correction found or not), before the return:

```
s.fuzzyCacheMu.Lock()
if len(s.fuzzyCache) >= fuzzyCacheMaxSize {
    s.fuzzyCache = make(map[string]*string)
}
if bestDist <= maxDist {
    s.fuzzyCache[word] = &bestWord
} else {
    s.fuzzyCache[word] = nil
}
s.fuzzyCacheMu.Unlock()
```

### 4.4 Invalidation in `extractAndStoreVocabulary`

In `index.go`, at the end of `extractAndStoreVocabulary`, after inserting new vocabulary words:

```
s.fuzzyCacheMu.Lock()
s.fuzzyCache = make(map[string]*string)
s.fuzzyCacheMu.Unlock()
```

This is conservative — clears on any vocab insert, even if the new words don't affect existing cache entries. The cost is negligible (cache refills on next search).

### Verify

- New test: call `fuzzyCorrectWord("errro")` twice — second call should not hit the DB (verify via mock or timing)
- New test: after indexing new content (which adds vocab), cache is cleared — next fuzzy call hits DB again
- New test: cache size cap — insert 300 entries, verify map gets cleared and rebuilt
- Existing fuzzy tests still pass

---

## 5. Periodic FTS5 optimize {#fts5-optimize}

**Files:** `internal/store/store.go` (field), `internal/store/index.go` (trigger)

### 5.1 Add insert counter to `ContentStore`

In `store.go`, add to the struct:

```
insertCount atomic.Int64
```

Add a constant: `const optimizeEvery int64 = 50`

Use `atomic.Int64` — concurrent `Index` calls serialize on the write transaction, but the post-commit counter update runs after the lock is released and could race.

### 5.2 Trigger optimize after commit in `Index`

In `index.go`, after the successful `tx.Commit()` and before vocabulary extraction:

```
s.insertCount.Add(int64(len(chunks)))
if s.insertCount.Load() >= optimizeEvery {
    s.optimizeFTS()
    s.insertCount.Store(0)
}
```

### 5.3 Add `optimizeFTS` method

In `store.go` or `index.go`:

```
func (s *ContentStore) optimizeFTS() {
    db, err := s.getDB()
    if err != nil { return }
    if _, err := db.Exec("INSERT INTO chunks(chunks) VALUES ('optimize')"); err != nil {
        slog.Warn("FTS5 optimize failed for chunks", "error", err)
    }
    if _, err := db.Exec("INSERT INTO chunks_trigram(chunks_trigram) VALUES ('optimize')"); err != nil {
        slog.Warn("FTS5 optimize failed for chunks_trigram", "error", err)
    }
}
```

### Verify

- New test: index 50+ chunks across multiple `Index` calls, verify optimize runs (check via SQLite's `fts5vocab` table or simply verify no error)
- New test: optimize failure is logged but doesn't break indexing
- Existing index tests still pass

---

## 6. mmap_size pragma {#mmap-pragma}

**File:** `internal/store/store.go`, function `getDB`

### 6.1 Add pragma after sql.Open

In `getDB()`, after `sql.Open` succeeds and before `db.Exec(schemaSQL)`:

```
if _, err := db.Exec("PRAGMA mmap_size = 268435456"); err != nil {
    slog.Warn("failed to set mmap_size pragma", "error", err)
    // Non-fatal — search still works, just without mmap optimization
}
```

256 MB (268435456 bytes) matches the TS reference. The actual mapped region is bounded by file size.

**Why not DSN:** mattn/go-sqlite3 does not support `_mmap_size` as a DSN parameter (verified via context7 docs). The `Exec` approach applies to the initial pool connection; `database/sql` reuses idle connections, so most operations benefit.

### Verify

- New test: after `NewContentStore` + forced init (any operation), verify `PRAGMA mmap_size` returns a non-zero value
- Existing tests still pass (mmap is transparent to operations)

---

## 7. Corrupt DB detection and recovery {#corrupt-recovery}

**Files:** `internal/store/retry.go` (new), `internal/store/store.go`, `internal/store/migrate.go`

### 7.1 Create `retry.go` with shared SQLite error helpers

Create `internal/store/retry.go`. Move `isBusy` from `migrate.go` here (same package — just delete the definition from migrate.go). Add:

```
func isSQLiteCorruption(err error) bool
```

Check error string for: `"malformed"`, `"not a database"`, `"corrupt"`, `"disk image is malformed"`. mattn/go-sqlite3 surfaces these from SQLite's C library as error messages.

```
func backupCorruptDB(dbPath string)
```

Renames `.db`, `.db-wal`, `.db-shm` to `.corrupt.<timestamp>` suffix (timestamp via `time.Now().Format("20060102T150405")`). Ignores `os.ErrNotExist` for WAL/SHM. Logs warning with backup paths.

### 7.2 Add backup-and-recreate logic to `getDB`

Wrap the open sequence (sql.Open → pragma → schema → migrations → statements) in a helper function. If it fails with a corruption error:

1. Close the failed DB handle
2. Check the DB file exists (don't act on permission errors or missing directories)
3. Generate timestamp suffix: `time.Now().Format("20060102T150405")`
4. Rename `<path>` → `<path>.corrupt.<timestamp>`
5. Rename `<path>-wal` → `<path>-wal.corrupt.<timestamp>` (if exists)
6. Rename `<path>-shm` → `<path>-shm.corrupt.<timestamp>` (if exists)
7. Log warning with backup path
8. Retry the open sequence once
9. If retry fails, return the retry error (wrapped with context)

### 7.3 Structure

Extract the open sequence into a private method:

```
func (s *ContentStore) openDB() (*sql.DB, error)
```

Then `getDB` becomes:

```
db, err := s.openDB()
if err != nil && isSQLiteCorruption(err) && fileExists(s.dbPath) {
    s.backupCorruptDB()
    db, err = s.openDB()  // retry once
}
if err != nil { return nil, err }
```

### Verify

- New test: create a file with garbage content at the DB path, call `getDB()` — verify it renames the file and creates a working DB
- New test: verify `.corrupt.<timestamp>` file exists after recovery
- New test: non-corruption errors (e.g., bad directory) don't trigger recovery
- New test: if retry also fails (e.g., filesystem read-only), error propagates
- Existing tests still pass
