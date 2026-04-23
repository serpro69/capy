# Design: Upstream Sync — context-mode v1.0.54→v1.0.89

> **Scope:** Port search quality, performance, and robustness improvements from the context-mode TypeScript reference (commits 358fa3a..2de4b58, versions v1.0.54→v1.0.89+19) to the capy Go implementation.
>
> **Reference:** `context-mode/src/store.ts`, `context-mode/src/db-base.ts`, `context-mode/src/server.ts`
>
> **Previous sync:** `/docs/done/upstream-sync-v1.0.54/`

## 1. Overview

The context-mode TS reference accumulated ~258 commits since the v1.0.54 sync point. After filtering out CI, bundle, platform-specific (Windows, Bun, KiloCode, Kiro, Zed, Pi, OpenClaw, OpenCode), docs-only, and test-infrastructure changes, **7 changes** are relevant to capy's core. An additional **6 changes** are deliberately skipped with documented rationale.

### Ported changes

| Priority | Area | Change | TS Commit(s) |
|----------|------|--------|--------------|
| P0 | Search | Stopword filtering from FTS5 queries + token deduplication | #271 |
| P0 | Search | Skip fuzzy correction on stopwords | #272 |
| P0 | Search | Title-match boost in proximity reranking (content-type aware) | #271 |
| P1 | Search | Fuzzy correction cache (process-local, max 256 entries) | #266 range |
| P1 | Perf | Periodic FTS5 optimize every 50 chunk-inserts | #266 |
| P1 | Perf | mmap_size pragma for read-heavy FTS5 search | #267 |
| P2 | Robustness | Corrupt DB detection and recovery on open | #244 |

### Not ported (with rationale)

| Area | Change | Why skipped |
|------|--------|-------------|
| Robustness | `withRetry` for SQLITE_BUSY (#243) | Go's `_busy_timeout=5000` DSN handles this at the C driver level — more robust than app-level retry. See §8.1 |
| Server | Coerce string-typed numeric inputs (#281) | mcp-go's `GetFloat` already coerces `string → float64` via `strconv.ParseFloat`. No change needed |
| Server | Doctor resource cleanup (#247) | Capy's doctor uses the server's shared executor/store, never spawns a temp executor. TS bug is architecture-specific |
| Server | Remove smartTruncate/maxOutputBytes | Go never had this — capy indexes full output directly |
| Session | Analytics engine rewrite (AnalyticsEngine, FS tracking, session continuity) | Major new TS subsystem, not a sync item. Capy has its own stats |
| Platform | Windows WAL/SHM cleanup, Bun/native ABI, adapter fixes, duplicate hook prevention, lifecycle/stdin fixes, debug scripts, CLI upgrade logic | Not applicable to Go's single-binary architecture |

### Capy-specific features not in upstream

These capy features have no upstream equivalent and must be preserved during the sync:

- **Synonym expansion** in search queries (`synonyms.go`) — query terms are expanded into synonym groups before FTS5
- **Entity-aware boosting** (`entity.go`) — quoted phrases and capitalized identifiers boost matching results
- **Per-source diversification** (`search.go`) — caps results from any single source to avoid dominance
- **Source-kind separation** (ADR-017) — durable vs ephemeral sources with separate lifecycle
- **Secret stripping** before indexing (`sanitize` package)

---

## 2. Stopword Filtering from FTS5 Queries + Token Deduplication

### 2.1 Problem

`sanitizePorterQuery` and `sanitizeTrigramQuery` in `search.go` strip FTS5 operators (AND, OR, NOT, NEAR) but pass through all other words — including stopwords like "the", "for", "with". FTS5 wastes time matching these high-frequency terms against the index, producing noise without improving recall.

Additionally, FTS5's unicode61 tokenizer lowercases on both sides, so `"Error" OR "error"` produces redundant index lookups with no extra recall.

### 2.2 Solution

Add two transformations to both sanitize functions:

**Stopword filtering:** After splitting words, filter out any word where `IsStopword(strings.ToLower(w))` is true. The stopword list already exists in `stopwords.go`. If filtering removes ALL words (query was entirely stopwords like "the one"), fall back to the original unfiltered words — an imperfect query is better than no query.

**Case-insensitive token deduplication:** Add a `dedupeTokens` helper that tracks seen lowercased forms and keeps only the first occurrence.

Both changes apply before synonym expansion. The existing `uniqueWords` function in distinctive terms already uses `IsStopword` for filtering — this extends the same principle to query sanitization.

### 2.3 Shared helper

Extract a `filterQueryTerms(query string) []string` function that:
1. Strips FTS5 special characters
2. Splits on whitespace
3. Lowercases
4. Deduplicates (case-insensitive)
5. Filters stopwords (with fallback to unfiltered if all removed)

This is reused by `sanitizePorterQuery`, `sanitizeTrigramQuery`, and `proximityRerank` (§4).

---

## 3. Skip Fuzzy Correction on Stopwords

### 3.1 Problem

`fuzzyCorrectQuery` in `search.go` splits the query into words and calls `fuzzyCorrectWord` for each word ≥3 chars. `fuzzyCorrectWord` queries the entire vocabulary table for candidates within edit distance, then runs levenshtein against each — CPU-linear in vocabulary size. Stopwords should never be corrected: "the" → "thee" is worse than useless.

### 3.2 Solution

In `fuzzyCorrectQuery`, after the `len(word) < 3` check, add an `IsStopword(word)` check that skips the word (appends it as-is without calling `fuzzyCorrectWord`).

This pairs with the fuzzy cache (§5) — stopword skipping avoids the DB hit entirely, while the cache avoids repeated hits for the same non-stopword typo.

---

## 4. Title-Match Boost in Proximity Reranking

### 4.1 Problem

`proximityRerank` in `search.go` only computes a proximity boost (how close query terms appear in content). If query terms appear in the chunk title, that's a strong relevance signal — especially for code chunks where the title is typically a function/class name.

### 4.2 Solution

Rename `proximityRerank` to `rerank` (it now handles title-match boost + stopword filtering beyond just proximity) and add a title-match boost before the proximity calculation:

1. Use `filterQueryTerms(query)` (from §2.3) to get non-stopword, deduplicated terms
2. Count how many terms appear in the lowercased title (`titleHits`)
3. Weight by content type: code chunks get weight `0.6` (function/class names are high signal), prose chunks get `0.3` (headings useful but body carries more weight)
4. Compute: `titleBoost = weight * (float64(titleHits) / float64(len(terms)))`
5. Apply: `FusedScore *= (1.0 + titleBoost)`

The proximity boost still applies on top, so a result with both a title match AND close proximity gets the strongest combined boost.

### 4.3 Interaction with existing capy features

- **Entity boosting** runs after proximity reranking in `SearchWithFallback`. Title-match boost and entity boost are independent — a quoted phrase that appears in the title gets both boosts. This is intentional: title match rewards structural relevance, entity match rewards exact content match.
- **Synonym expansion** in `proximityRerank` is unaffected — synonym groups are still used for the proximity span calculation. The title-match boost uses the raw (non-expanded) terms since titles are typically short and exact matches are more meaningful than synonym matches.

---

## 5. Fuzzy Correction Cache

### 5.1 Problem

`fuzzyCorrectWord` hits the vocabulary DB every call — `SELECT word FROM vocabulary WHERE length(word) BETWEEN ? AND ?` — then runs levenshtein against every candidate. For repeated queries (common in batch search), this is redundant work.

### 5.2 Solution

Add a process-local cache on `ContentStore`:

- **Field:** `fuzzyCache map[string]*string` — `nil` value = "checked, no correction exists" (distinguishes from "not in cache" which is key-missing). Max 256 entries.
- **Read path:** In `fuzzyCorrectWord`, check `s.fuzzyCache[word]` before querying DB. On hit, return immediately.
- **Write path:** After computing a correction (or no correction), store in cache. If cache exceeds 256 entries, clear the entire map (simple eviction, same as TS).
- **Invalidation:** Clear cache in `extractAndStoreVocabulary` (in `index.go`) after inserting new words, since new vocab entries could make previously-uncorrectable words correctable.
- **Concurrency:** Protect with `sync.Mutex`. Multiple concurrent `SearchWithFallback` calls could race on the cache. The mutex is only held for map read/write (not the DB query), so contention is negligible.

---

## 6. Periodic FTS5 Optimize

### 6.1 Problem

FTS5 b-trees fragment over many insert/delete cycles. Over a session with dozens of batch executions and fetch-and-index calls, fragmentation degrades search latency.

### 6.2 Solution

Track inserts and run SQLite's built-in optimize command periodically:

- **Field:** `insertCount atomic.Int64` on `ContentStore` — atomic because concurrent `Index` calls serialize on the write transaction but the post-commit counter update could race
- **Increment:** At the end of `Index` in `index.go`, after successful commit, add `len(chunks)` via `s.insertCount.Add(int64(len(chunks)))`
- **Trigger:** When `s.insertCount.Load() >= 50`, run optimize on both tables:
  ```sql
  INSERT INTO chunks(chunks) VALUES ('optimize')
  INSERT INTO chunks_trigram(chunks_trigram) VALUES ('optimize')
  ```
  Then reset counter to 0.
- **Non-blocking:** Runs after the main transaction commits. Log warning on failure but don't propagate — the index is valid, just suboptimally structured.

Threshold of 50 is from the TS reference. Since capy indexes into two FTS5 tables per source, 50 chunk-inserts ≈ 25 sources, aligning with a moderately active session.

---

## 7. mmap_size Pragma

### 7.1 Problem

SQLite's default I/O uses `read()`/`write()` syscalls, copying data from kernel buffer cache into userspace. For read-heavy FTS5 search workloads, this is unnecessary overhead.

### 7.2 Solution

Set `PRAGMA mmap_size = 268435456` (256 MB) in `getDB()` after `sql.Open`, before schema init:

```
sql.Open(...)
db.Exec("PRAGMA mmap_size = 268435456")   // ← new
db.Exec(schemaSQL)
applyMigrations(db)
prepareStatements(db)
```

This memory-maps up to 256 MB of the DB file. The actual mapped region is bounded by file size, so a typical capy DB (1-50 MB) gets fully mapped.

**Driver limitation:** mattn/go-sqlite3 does not support `_mmap_size` as a DSN parameter (confirmed via context7 docs). A one-shot `Exec` after open is the correct approach. The pragma applies to the executing connection; `database/sql` pool reuse ensures coverage for most subsequent operations. If full per-connection coverage becomes important, switching to mattn's `ConnectHook` (custom driver registration) is the upgrade path.

---

## 8. Corrupt DB Detection and Recovery

### 8.1 Problem

If capy opens a corrupt database, `getDB()` fails and the error propagates — the server is stuck until manual intervention. Since capy's DB can be git-tracked and shared (ADR-015/016), blind deletion would destroy irreplaceable knowledge.

### 8.2 Solution

Detect corruption in `getDB()` and recover by renaming (not deleting):

1. Attempt normal open: `sql.Open` → `PRAGMA mmap_size` → `schemaSQL` → `applyMigrations` → `prepareStatements`
2. If any step fails with a corruption error, enter recovery:
   - Close the failed DB handle
   - Rename corrupt file to `<path>.corrupt.<timestamp>` (e.g., `capy.db.corrupt.20260422T143000`)
   - Also rename sidecars: `.db-wal` → `.db-wal.corrupt.<timestamp>`, `.db-shm` → `.db-shm.corrupt.<timestamp>`
   - Log warning: `"corrupt database detected, backed up to <path> and recreated — previously indexed content must be re-fetched/re-indexed"`
   - Retry the full open path once
   - If retry also fails, propagate the error
3. Only trigger recovery when the file exists AND the error is corruption-specific

**Corruption detection:** Check error string for `"malformed"`, `"not a database"`, `"corrupt"`, or `"disk image is malformed"`. mattn/go-sqlite3 surfaces these from SQLite's C library.

**Safety:**
- Corrupt file is preserved for forensic inspection or `sqlite3 .recover`
- If DB was git-tracked, `git checkout -- capy.db` restores the last committed version
- Only one retry — avoids infinite loops on persistent filesystem errors

**File organization:** Extract `isBusy` from `migrate.go` and place alongside the new corruption helpers (`isSQLiteCorruption`, `backupCorruptDB`) in a new `internal/store/retry.go`. This groups all SQLite error classification and recovery utilities in one place.

---

## 9. Not Ported — Detailed Rationale

### 9.1 withRetry for SQLITE_BUSY (#243)

The TS upstream added application-level retry with exponential backoff (100ms, 500ms, 2000ms) around DB operations that can fail with SQLITE_BUSY.

**Why skipped:** Go's mattn/go-sqlite3 handles busy waiting at the C driver level via `_busy_timeout=5000` in the DSN. SQLite's built-in busy handler sleeps and retries internally with proper backoff, covering ALL operations — not just the ones manually wrapped in a retry loop. The TS needed explicit retry because `better-sqlite3` is synchronous and its busy timeout support is limited. Adding Go-side retry on top would:
- Risk masking real contention bugs (two long-running write transactions fighting)
- Double the wait time (driver busy-waits 5s, then app retries and driver busy-waits another 5s)
- Add complexity with no measurable benefit for capy's single-user MCP workload

**If this decision needs revisiting:** The signal would be SQLITE_BUSY errors appearing in logs despite the 5s busy timeout. The fix would be increasing `_busy_timeout` in the DSN, not adding retry — the contention would indicate a design problem (e.g., overlapping long transactions) that retry only papers over.

### 9.2 Coerce string-typed numeric inputs (#281)

The TS upstream added `z.coerce.number()` to handle LLMs sending `timeout: "60000"` as a string.

**Why skipped:** mcp-go's `GetFloat(key, default)` already handles coercion. Its implementation includes a `case string:` branch that calls `strconv.ParseFloat`. No capy change needed — the Go MCP library is ahead of the TS reference on this.

### 9.3 Doctor resource cleanup (#247)

The TS upstream fixed a resource leak where `ctx_doctor` created a temporary `PolyglotExecutor` for server testing and didn't call `cleanupBackgrounded()` afterwards.

**Why skipped:** Capy's `handleDoctor` in `tool_doctor.go` uses the server's shared `s.executor` (for `Runtimes()`) and `s.getStore()` (for `Stats()`). It never creates a temporary executor. The TS bug is architecture-specific to their test-executor pattern.

### 9.4 Remove smartTruncate and maxOutputBytes

The TS removed its output truncation system (`smartTruncate` — 60% head + 40% tail) in favor of preserving full stdout for FTS5 indexing.

**Why skipped:** Go never had this. Capy's executor captures full output and indexes it directly. The TS was removing tech debt that capy never accumulated.

### 9.5 Session analytics engine rewrite

The TS added `AnalyticsEngine` class, `formatReport`, FS tracking for batch_execute, before/after stats comparison, and session continuity breakdown in `ctx_stats`.

**Why skipped:** This is a major new subsystem, not a bug fix or algorithm improvement. Capy has its own stats implementation in `stats.go` and `tool_stats.go`. If analytics become a priority, this should be its own design effort — not folded into a sync.

### 9.6 Platform-specific changes

Skipped wholesale:
- **Windows:** Orphaned WAL/SHM cleanup, path quoting for spaces (#239), corrupt DB test skipping
- **Bun/native:** ABI cache validation (#238), auto-rebuild better-sqlite3, NodeSQLiteAdapter, ensure-deps.mjs
- **Platform adapters:** Pi show command output (#279), Codex exec-mode (#256), OpenCode plugin, duplicate hook prevention (#282)
- **Lifecycle:** stdin listener removal/revert (#236) — MCP SDK-specific
- **CLI:** Upgrade file copy list, dynamic package.json reading, debug script
- **CI:** Test runner fixes, vitest worker caps, bundle updates, install stats
- **Docs:** README badges, YouTube embed, landing page, platform-support, label guidance

None of these apply to Go's single-binary, cross-platform architecture.

---

## 10. Deliberate Divergences from Upstream (Preserved)

These existing capy divergences from the TS reference are **unchanged** by this sync:

| Area | Capy behavior | TS behavior | Rationale |
|------|--------------|-------------|-----------|
| RRF layers | 2 (porter OR + trigram OR) | 4 (porter+trigram × AND+OR) | OR is superset; RRF handles precision (ADR-010) |
| Proximity formula | Normalized by content length | Magic constant `/100` | More principled, adapts to chunk size (ADR-014) |
| BM25 title weight | Configurable, default 2.0 | Hardcoded 5.0 | Auto-generated titles hurt by 5x boost (ADR-009) |
| Cleanup policy | Conservative (never-accessed + cold + age) | Aggressive (age-only stale deletion) | Persistent DB needs conservative pruning (ADR-011) |
| ContentType filter | Internal only, not in MCP schema | Internal only (same) | No current caller (ADR-012) |
| Fetch TTL | Configurable via `.capy.toml` | Hardcoded 24h | Config system makes this trivial (ADR-013) |
| Knowledge base | Persistent per-project | Ephemeral per-session (originally) | Designed for persistence from day one (ADR-006) |
| Synonym expansion | Bidirectional synonym groups | None | Capy-only feature for query recall |
| Entity boosting | Quoted phrases + capitalized identifiers | None | Capy-only feature for precision |
| Source diversification | Per-source result cap | None | Prevents single-source dominance |
| Source-kind separation | Durable vs ephemeral lifecycle | None | ADR-017; separate retention/TTL paths |
