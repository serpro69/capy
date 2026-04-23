# Design: Upstream Sync — context-mode v1.0.54→v1.0.89+

> **Scope:** Port meaningful algorithm, performance, and reliability changes from the context-mode TypeScript reference (commits 358fa3a..2de4b58, ~258 commits) to the capy Go implementation.
>
> **Reference submodule pin:** `2de4b58` (v1.0.89+19)
>
> **Previous sync:** `/docs/done/upstream-sync-v1.0.54/`
>
> **Reference files:** `/context-mode/src/store.ts`, `/context-mode/src/db-base.ts`, `/context-mode/src/server.ts`

## 1. Overview

The context-mode TS reference accumulated ~258 commits since v1.0.54. After filtering out CI/bundle (110+), platform-specific adapters (Codex, Cursor, Gemini CLI, VS Code Copilot, Pi, OpenCode, OpenClaw, KiloCode, Kiro, Zed — 40+), docs/web/README (30+), install stats (20+), test-infrastructure-only (10+), and features not applicable to the Go port (insight dashboard, AnalyticsEngine, FS tracking, session snapshots), **9 changes** remain relevant to capy's core:

| Priority | Area | Change | Decision |
|----------|------|--------|----------|
| P0 | Search | Filter stopwords from FTS5 query sanitization | Port |
| P0 | Search | Filter stopwords from proximity/title reranking | Port |
| P0 | Search | Content-type-aware title boost in reranking | Port |
| P0 | Search | Skip fuzzy correction for stopword terms | Port |
| P1 | Perf | Periodic FTS5 `optimize` to defragment indexes | Port |
| P1 | Perf | `mmap_size` pragma for read-heavy FTS5 search | Port with Go adaptation |
| P1 | Reliability | `withRetry` for SQLITE_BUSY on store operations | Port with Go adaptation |
| P1 | Reliability | Corrupt DB detection and recovery | Port |
| P2 | Robustness | Coerce string-typed numeric tool inputs | Already handled — no action |

### Not applicable (skipped)

- **inheritEnvKeys** — added (4b88a82) then reverted (a2e9c71) in the same release cycle
- **ctx_purge tool** — capy already has `capy_cleanup purge_ephemeral=true`
- **Zero-truncation architecture / session snapshots** — TS session hook system, not applicable to Go MCP server
- **Platform-isolated content DB** — TS multi-platform concern (Go compiles to native binary)
- **AnalyticsEngine / FS read tracking** — extensive TS-specific analytics (27 metrics, ctx_stats integration); separate initiative if wanted
- **ctx_insight dashboard** — new TS feature (React + better-sqlite3); not core
- **MCP readiness sentinel** — TS-specific startup mechanism (Go server starts synchronously)
- **smartTruncate removal** — capy never had it (auto-index via intent search from the start)
- **Node:sqlite adapter** — Go uses CGO with mattn/go-sqlite3
- **WAL checkpoint on exit** — already done in capy's `ContentStore.Close()` (ADR-016)
- **Doctor resource leaks (#247)** — TS-specific (Go GC + explicit cleanup in `tool_doctor.go`)
- **Coerce string-typed numeric inputs** — mcp-go's `GetFloat()` already parses string→float64
- **Cache cleanup prepared statements** — capy's cleanup uses `beginImmediate` transactions with retention scoring; the dynamic SQL is appropriate for its more sophisticated eviction logic
- All platform adapters, CI/bundle, install stats, docs/web, test-infrastructure-only commits

### Deliberate divergences preserved

- **ADR-009:** Configurable BM25 title weight (not hardcoded 5x)
- **ADR-010:** RRF uses 2 search layers, not 4
- **ADR-011:** Conservative cleanup policy (retention scoring, not just age-based)
- **ADR-012:** ContentType filter internal only (not in MCP tool schema)
- **ADR-013:** Configurable fetch TTL (not hardcoded 24h)
- **ADR-014:** Proximity normalized by content length (not magic constant)
- **ADR-017:** Source-kind separation (capy's own improvement)
- **Synonym expansion** in search queries (Go improvement)
- **Entity-aware boosting** for capitalized identifiers and quoted phrases (Go improvement)
- **Per-source diversification** to prevent any single source from dominating results (Go improvement)

---

## 2. Stopword-Aware Search Pipeline

The P0 items are one logical unit: capy has the `stopwords` map (`internal/store/stopwords.go`) and uses it for vocabulary indexing and entity extraction, but the search pipeline itself — query sanitization, proximity reranking, and fuzzy correction — does not consult it. The TS added stopword filtering in three commits (#271, #272, #265) that together form a coherent improvement.

### 2.1 Filter Stopwords from FTS5 Query Sanitization

**Problem:** `sanitizePorterQuery` and `sanitizeTrigramQuery` (`search.go:426`, `search.go:460`) build FTS5 queries from all input words. A query like "update authentication" sends `"update" "authentication"` to FTS5, where "update" (a stopword appearing in thousands of chunks) dilutes BM25 ranking — the truly distinctive term "authentication" gets less relative weight.

**Solution:** After splitting and cleaning words, filter through `IsStopword()`. Fall back to unfiltered words if ALL terms are stopwords (prevents returning empty query).

**In `sanitizePorterQuery`** (search.go:426):
- After the `for _, w := range words` loop that builds groups, add a stopword filter step
- Collect non-stopword groups into `meaningful`; if `meaningful` is empty, keep all groups
- This integrates with the existing synonym expansion: a stopword term is dropped entirely, its synonyms are not expanded (stopwords should not contribute to queries at all)

**In `sanitizeTrigramQuery`** (search.go:460):
- Same pattern: filter stopword terms before building quoted groups
- For trigram, the minimum-3-char filter already exists; add `!IsStopword(w)` to the condition
- Fall back to all terms if filtering removes everything

**Go adaptation:** The TS filters at the word level before quoting. In capy, synonym expansion happens inline during the `for _, w := range words` loop. The stopword check should come first: `if IsStopword(strings.ToLower(w)) { continue }` early in the loop body, before synonym expansion. This means a stopword is not expanded via synonyms — correct behavior since synonyms of "update" would also be noise.

### 2.2 Filter Stopwords from Proximity/Title Reranking

**Problem:** `proximityRerank` (`search.go:246`) computes proximity boost using all query words. "the authentication handler" includes "the", which matches at hundreds of positions in any content, making the minimum span meaningless — every chunk gets a high proximity score.

**Solution:** Filter stopwords from the `words` slice before building term groups. Use the same fall-back-to-all pattern.

**In `proximityRerank`** (search.go:246):
- After `words := strings.Fields(cleaned)`, filter: `meaningful := filterNonStopwords(words)`; use `meaningful` if non-empty, else `words`
- This also affects the synonym-expanded `termGroups` — fewer groups means a tighter, more meaningful proximity calculation

**Helper:** Add `filterNonStopwords(words []string) []string` as a package-level helper since it's used in three places (sanitizePorter, sanitizeTrigram, proximityRerank).

### 2.3 Content-Type-Aware Title Boost in Reranking

**Problem:** capy's `proximityRerank` only boosts results for multi-term queries based on proximity. Single-term queries get no reranking at all. Additionally, title matches are not weighted by content type — a function name match in a code chunk's title is more signal than a generic heading match in a prose chunk.

**Solution:** Rename `proximityRerank` to `rerank` (it now does more than proximity) and add a title-match boost that applies to ALL queries (including single-term):

1. For each result, compute `titleBoost`:
   - Lowercase the title and count how many query terms appear in it (`titleHits`)
   - Weight by content type: code → 0.6, prose → 0.3 (code chunk titles are function/class names — high signal; prose titles are headings — useful but body carries more weight)
   - `titleBoost = weight * (titleHits / len(terms))` — proportional to term coverage
   - `titleBoost = 0` if no title hits

2. Proximity boost (unchanged, only for 2+ terms):
   - Same findMinSpan logic, same content-length normalization (ADR-014 preserved)

3. Combined: `FusedScore *= (1.0 + titleBoost + proximityBoost)`

4. Re-sort by boosted fused score, breaking ties with original BM25 rank

**Interaction with capy improvements:**
- Entity-aware boosting (`BoostByEntities`) runs AFTER reranking in the `SearchWithFallback` caller. The title boost here is complementary: entities handle capitalized identifiers and quoted phrases; title boost handles general term-in-title matches weighted by content type.
- Configurable BM25 title weight (ADR-009) operates at the SQL level (`bm25(chunks, weight, 1.0)`). The reranking title boost is a post-retrieval signal that catches cases where BM25 title weight alone is insufficient (e.g., term appears in title but BM25 ranked it low due to a very common title).

### 2.4 Skip Fuzzy Correction for Stopwords

**Problem:** `fuzzyCorrectQuery` (`search.go:692`) runs levenshtein distance computation for every word ≥ 3 chars, including stopwords like "update", "added", "tests". Each call queries the vocabulary table and computes edit distances against all candidates in the length range. This is wasted work — stopwords appear everywhere and correcting them changes nothing meaningful.

**Solution:** Add `IsStopword(word)` check in `fuzzyCorrectQuery` before calling `fuzzyCorrectWord`:

```
for _, word := range words {
    if len(word) < 3 || IsStopword(word) {
        result = append(result, word)
        continue
    }
    ...
}
```

This is a targeted change — one line added to the existing loop condition.

---

## 3. FTS5 Performance: Periodic Optimize

**Problem:** FTS5 b-trees fragment over many insert/delete cycles. capy's content deduplication (ADR-007) deletes and re-indexes sources on update, creating fragmentation. Long-running sessions accumulate this debt.

**Solution:** Track insert count and run FTS5 `optimize` command periodically:

1. Add `insertCount int` field to `ContentStore`
2. Add a constant `optimizeEvery = 50` (matches TS)
3. After each successful `IndexContent` / `IndexPlainText` / `IndexJSON` call, increment `insertCount`; when `insertCount % optimizeEvery == 0`, call `optimizeFTS()`
4. Call `optimizeFTS()` in `Close()` before WAL checkpoint
5. `optimizeFTS()` executes:
   - `INSERT INTO chunks(chunks) VALUES('optimize')`
   - `INSERT INTO chunks_trigram(chunks_trigram) VALUES('optimize')`
   - Best-effort: catch and log errors, don't block indexing

**Go adaptation:**
- The insert count and optimize call should be inside the `mu` lock that protects store operations, OR use `atomic.Int64` for lock-free counting. Since `IndexContent` already holds the write lock via `beginImmediate`, calling optimize inside the same transaction scope is not appropriate (optimize should run outside transactions). Use `atomic.Int64` for the counter and call `optimizeFTS()` after the index transaction commits.
- `optimizeFTS()` opens a one-shot `db.Exec()` call — no prepared statement needed since it runs every 50 inserts, not per-query.

---

## 4. mmap_size Pragma

**Problem:** Without memory-mapping, every FTS5 search issues `read()` syscalls to load index pages from disk. On a warm page cache this adds unnecessary kernel transitions.

**Solution:** Set `PRAGMA mmap_size = 268435456` (256MB) on database connections. SQLite only maps up to the actual file size, so this is a safe upper bound. Falls back gracefully on platforms where mmap is restricted.

**Go adaptation:**
- capy uses `database/sql` with `mattn/go-sqlite3`, which supports DSN-based pragma configuration: `file:path.db?_mmap_size=268435456`
- Add `_mmap_size=268435456` to the DSN string in `openDB()` (`internal/store/store.go`). This sets it on every connection in the pool.
- Also add it to the checkpoint DSN in `checkpoint()` (`store.go:292`) for consistency.
- The `_mmap_size` DSN parameter is supported by `mattn/go-sqlite3` — verify via driver documentation.

---

## 5. withRetry for SQLITE_BUSY on Store Operations

**Problem:** capy has SQLITE_BUSY retry logic in `beginImmediate()` (migrate.go, cleanup.go) for write transactions, but general store operations — `IndexContent`, `IndexPlainText`, `IndexJSON`, search queries — are not wrapped. Under concurrent access (MCP server + CLI + hooks), these can fail with SQLITE_BUSY.

**Solution:** Introduce a general-purpose `withRetry` helper and wrap the operations that touch SQLite:

1. Extract `isBusy()` from `migrate.go` to a shared `retry.go` file in `internal/store/`
2. Add `withRetry[T](fn func() (T, error)) (T, error)` generic helper with backoff delays `[100ms, 500ms, 2000ms]` (matches TS)
3. Wrap:
   - `IndexContent` — the `beginImmediate` transaction already has retry, but the surrounding content-hash lookup and chunk count query don't
   - Search operations in `execDynamicSearch` — `rows.Scan` under read contention
   - `TrackAccess` — the goroutine-based async writer (`trackAccessAsync`) can race with index operations

**Go adaptation:**
- The TS uses synchronous busy-wait (`while (Date.now() - start < delay) {}`). Go should use `time.Sleep()` — idiomatic and doesn't burn CPU.
- The TS wraps at the function level. Go should wrap at the `db.Exec` / `db.Query` level where SQLITE_BUSY actually surfaces, not at the high-level method level. This avoids re-running indexing logic (chunking, hashing) on retry — only the DB operation is retried.
- `isBusy()` already handles both typed `sqlite3.Error` and string fallback — reuse it.

**Note on `beginImmediate`:** The existing `beginImmediate` in `migrate.go` has its own retry loop (10 retries, exponential backoff from 10ms). The new `withRetry` is for non-transactional operations. `beginImmediate` stays as-is.

---

## 6. Corrupt DB Detection and Recovery

**Problem:** If the knowledge DB file becomes corrupt (crash during WAL write, disk full, external tool modification), all store operations fail permanently for the session. The user sees cryptic SQLite errors.

**Solution:** Add detection at DB open time and recovery by renaming the corrupt file:

1. Add `isCorruptionError(err error) bool` in `internal/store/retry.go`:
   - Check for `sqlite3.ErrCorrupt`, `sqlite3.ErrNotADB`
   - String fallback: "database disk image is malformed", "file is not a database"

2. Add `renameCorruptDB(dbPath string)` in `internal/store/retry.go`:
   - Rename `db`, `db-wal`, `db-shm` to `db.corrupt-<timestamp>` (best-effort per file)

3. In `NewContentStore` / `openDB`:
   - If the initial schema exec or migration fails with a corruption error, call `renameCorruptDB`, then retry opening with a fresh DB
   - Log a warning: "Knowledge DB was corrupt and has been renamed. A fresh DB has been created."
   - Only attempt recovery once — if the second open fails, return the error

**Go adaptation:**
- The TS only detects corruption at open time (constructor). Same approach for Go.
- The TS renames synchronously with `renameSync`. Go uses `os.Rename`.
- WAL/SHM cleanup files may not exist (e.g., clean shutdown removed them). Use `os.Rename` and ignore `os.ErrNotExist`.

---

## 7. Summary of Files Changed

| File | Changes |
|------|---------|
| `internal/store/search.go` | Stopword filtering in sanitizePorterQuery, sanitizeTrigramQuery, proximityRerank→rerank (title boost + stopword filter), fuzzyCorrectQuery stopword skip |
| `internal/store/store.go` | FTS5 optimize counter + call in Close, mmap_size in DSN |
| `internal/store/retry.go` | New file: shared `isBusy`, `isCorruptionError`, `renameCorruptDB`, `withRetry` generic |
| `internal/store/migrate.go` | Extract `isBusy` to retry.go (import change only) |
| `internal/store/cleanup.go` | Import `isBusy` from retry.go |
| `internal/store/index.go` | Wrap DB operations with `withRetry` |
