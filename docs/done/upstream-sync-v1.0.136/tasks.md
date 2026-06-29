# Tasks: Upstream Sync — context-mode v1.0.89→v1.0.136

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> Status: done (all tasks complete; final verification passed 2026-06-18)
> Created: 2026-05-17

## Task 1: SSRF guard improvements
- **Status:** done
- **Depends on:** —
- **Docs:** [design.md#6a-ssrf-guard-scheme-validation--dns-rebinding-defense](./design.md#6a-ssrf-guard-scheme-validation--dns-rebinding-defense), [implementation.md#task-1-ssrf-guard-improvements](./implementation.md#task-1-ssrf-guard-improvements)

### Subtasks
- [x] 1.1 Create `internal/server/ssrf.go` with `classifyIP(rawIP string) error` — accepts raw IP string, strips zone-IDs (`%eth0`), handles IPv4-mapped IPv6 (`::ffff:A.B.C.D`) via recursion, classifies: IPv6 unspecified/link-local/multicast/loopback/ULA; IPv4 `0.0.0.0/8`/`169.254.0.0/16`/`224.0.0.0+`/loopback/RFC1918; malformed → block
- [x] 1.2 Add `validateFetchScheme(rawURL string) error` in `ssrf.go` — reject any scheme not in `{"http", "https"}`
- [x] 1.3 Add `newSSRFSafeTransport() *http.Transport` in `ssrf.go` — custom `DialContext` that resolves DNS via `net.DefaultResolver.LookupIPAddr`, classifies every IP via `classifyIP`, dials first passing IP via `net.Dialer`
- [x] 1.4 Update `tool_fetch.go`: replace `validateFetchURLFunc(url)` with `validateFetchScheme(url)`, replace default `http.Client{}` with one using `newSSRFSafeTransport()`, remove old `validateFetchURL` and `validateFetchURLFunc`
- [x] 1.5 Create `internal/server/ssrf_test.go` — test scheme blocking (file://, gopher://, data://), IP classification covering: `0.0.0.0` (current network), `::` (unspecified), `::ffff:127.0.0.1` (IPv4-mapped), `fe80::1%eth0` (zone-id), `224.0.0.1` (multicast), `169.254.169.254` (IMDS), `127.0.0.1` (loopback), `10.0.0.1`/`192.168.1.1` (private), malformed strings, valid public IPs; Transport-level DNS rebinding defense
- [x] 1.6 Update `tool_fetch_test.go` — replace `validateFetchURLFunc` override pattern with Transport-level test helper that allows localhost for `httptest.NewServer` (helper `disableSSRFValidation` lives in `tool_knowledge_test.go`; now swaps `newFetchTransport`)

## Task 2: Path traversal bypass fix
- **Status:** done
- **Depends on:** —
- **Docs:** [design.md#6c-path-traversal-bypass-in-file-deny-evaluation](./design.md#6c-path-traversal-bypass-in-file-deny-evaluation), [implementation.md#task-2-path-traversal-bypass-fix](./implementation.md#task-2-path-traversal-bypass-fix)

### Subtasks
- [x] 2.1 Modify `EvaluateFilePath` in `internal/security/eval.go` to accept `projectRoot string` as third parameter — match deny globs against three candidates: raw input, lexical absolute (`filepath.Clean(filepath.Join(projectRoot, filePath))`), and canonical realpath (`filepath.EvalSymlinks`)
- [x] 2.2 Update all three callers to pass `projectRoot`: `internal/server/security_check.go:45` (pass `s.projectDir`), `internal/hook/pretooluse.go:157` (pass `projectDir`), and deny checker closure in `server.go` (pass `s.projectDir`) — NOTE: the `server.go` deny-checker closure does not exist yet; it is created in Task 5 (subtask 5.7), which already passes `s.projectDir`. Only the two existing callers were updated here.
- [x] 2.3 Add tests — relative `../../.ssh/id_rsa` caught by glob, absolute paths work, empty projectRoot preserves old behavior, **symlink escape** (symlink pointing to denied target is caught via realpath), **physical traversal via symlinked dir** (`dir-link/../secrets/id_rsa` caught via realpath — regression test for the isolated-review P0 below); **non-regular file** guard in fd-bound read callers DEFERRED to Task 3b/Task 5 — `EvaluateFilePath` performs no file I/O, so the `IsRegular()` guard belongs to the fd-bound readers (`handleIndex` in tool_index.go, `refreshStaleSources` in search.go) introduced by those tasks, not to this glob-matching function.

> **Isolated review (P0, fixed):** The initial implementation fed the lexically-`Clean`ed path to `filepath.EvalSymlinks`. `Clean` collapses `..` lexically, discarding a symlinked directory before symlink resolution (`dir-link/../secrets` cleans to `secrets`), so a symlink-then-`..` physical escape bypassed the realpath candidate. Fix: feed `EvalSymlinks` an **uncleaned** anchored path (`physicalInput`) while keeping the `Clean`ed candidate for non-existent-file lexical traversal. Indexed as `kk:review-findings`. Also corrected a stale doc comment (claimed `fileGlobToRegex` recompiles per call; it caches via `fileRegexCache sync.Map`) and de-duplicated candidates for already-clean absolute inputs.

## Task 3: Executor env deny list expansion
- **Status:** done
- **Depends on:** —
- **Docs:** [design.md#6d-executor-env-deny-list-netc-profiler-hijack-vectors](./design.md#6d-executor-env-deny-list-netc-profiler-hijack-vectors), [implementation.md#task-3-executor-env-deny-list-expansion](./implementation.md#task-3-executor-env-deny-list-expansion)

### Subtasks
- [x] 3.1 Add 14 .NET/C# entries to `deniedEnvVars` in `internal/executor/env.go`: CORECLR_PROFILER, CORECLR_PROFILER_PATH (+ _32/_64/_ARM32/_ARM64), CORECLR_ENABLE_PROFILING, DOTNET_PROFILER_PATH (+ _32/_64/_ARM32/_ARM64), DOTNET_DiagnosticPorts, DOTNET_BUNDLE_EXTRACT_BASE_DIR
- [x] 3.2 Add `COMPlus_` prefix check in `BuildSafeEnv` alongside existing `BASH_FUNC_` prefix check
- [x] 3.3 Add tests in `internal/executor/env_test.go` — verify CORECLR_PROFILER and COMPlus_EnableDiagnostics are stripped from env output

## Task 3b: Apply Read deny-policy to capy_index(path)
- **Status:** done
- **Depends on:** Task 2 (uses updated EvaluateFilePath with projectRoot)
- **Docs:** [design.md#6e-apply-read-deny-policy-to-capy_indexpath](./design.md#6e-apply-read-deny-policy-to-capy_indexpath), [implementation.md#task-3b-apply-read-deny-policy-to-capy_indexpath](./implementation.md#task-3b-apply-read-deny-policy-to-capy_indexpath)

### Subtasks
- [x] 3b.1 Add `s.checkFilePathDenyPolicy(path)` call in `handleIndex` (`internal/server/tool_index.go`) when `path != ""`, before any file I/O (before the `filepath.IsAbs` resolution at line 26)
- [x] 3b.2 Add tests: denied absolute path returns error and produces no FTS5 chunks; denied relative `../` traversal path returns error; inline `content` with a `source` label matching a deny pattern still indexes successfully

## Task 4: Phrase frequency reranker
- **Status:** done
- **Depends on:** —
- **Docs:** [design.md#2-phrase-frequency-reranker](./design.md#2-phrase-frequency-reranker), [implementation.md#task-4-phrase-frequency-reranker](./implementation.md#task-4-phrase-frequency-reranker)

### Subtasks
- [x] 4.1 Add `countAdjacentPairs(positionLists [][]int, terms []string, gap int) int` to `internal/store/search.go` — sweep-line algorithm, each right position consumed at most once
- [x] 4.2 Integrate into `rerank()`: `countAdjacentPairs` always builds its own position lists from raw `terms` against `r.Content` (cannot reuse existing `posLists` — they're synonym-expanded, and may be nil when minSpan came from highlights). Compute `phraseBoost = 0.5 * min(1.0, adjacentPairs/4.0)`, add to proximity boost: `r.FusedScore *= (1.0 + proximityBoost + phraseBoost)`
- [x] 4.3 Add unit tests for `countAdjacentPairs` in `internal/store/search_test.go` — 0 pairs when terms don't appear, 1 pair for single adjacent occurrence, saturation at 4+, greedy consumption (no double-counting)
- [x] 4.4 Add integration test: short doc with 4 adjacent pairs outranks long doc with 1 occurrence at same minSpan

## Task 5: Hash-based stale detection with auto-refresh
- **Status:** done
- **Depends on:** Task 2 (for deny checker wiring)
- **Docs:** [design.md#3-hash-based-stale-detection-with-auto-refresh-on-search](./design.md#3-hash-based-stale-detection-with-auto-refresh-on-search), [implementation.md#task-5-hash-based-stale-detection](./implementation.md#task-5-hash-based-stale-detection)

### Subtasks
- [x] 5.1 Add `file_path TEXT` to `CREATE TABLE sources` in `internal/store/schema.go`
- [x] 5.2 Add migration in `internal/store/migrate.go` for `ALTER TABLE sources ADD COLUMN file_path TEXT` — implemented as `migrate019AddFilePath`, idempotent via the migrations-table row PLUS a `sourcesHasColumn` PRAGMA probe (fresh DBs already have the column from schemaSQL, so a bare ALTER would fail with "duplicate column name"). Updated `TestMigration018_AddsSessionKindOnFreshDB` count assertion 2→3.
- [x] 5.3 Modify `stmtInsertSource` from 6 to 7 columns (add `file_path`). Add `filePath string` parameter to `indexPreparedChunks` signature — `Index`/`IndexChunked` pass `""`, new `IndexWithFilePath` passes actual path. Convert to `sql.NullString` before `Exec`. In same-hash dedup path: when `filePath != ""` and existing source has `file_path = NULL`, update via new `stmtUpdateSourceFilePath`. `stmtFindSourceByLabel` now also selects `file_path`. Added new prepared statements to `Close()` list.
- [x] 5.4 Add `denyChecker func(string) bool` field, `SetDenyChecker` method, `lastRefreshTime atomic.Int64` (5-second cooldown via CAS), and `refreshStaleSources() int` method to `internal/store/search.go`. Parse `indexed_at` with `"2006-01-02 15:04:05"`, compare `mtime.UTC()`. fd-bound reads via `fileChangedSince`: `os.OpenFile(O_RDONLY|O_NONBLOCK)` → `f.Stat()` (verify `IsRegular()`) → `io.ReadAll`. Hash comparison: `sanitize.StripSecrets(rawContent)` then `contentHash(sanitized)`. Re-index via `IndexWithFilePath`.
- [x] 5.5 Call `refreshStaleSources()` at the top of `SearchWithFallback` before the RRF pass — early-return if cooldown hasn't elapsed
- [x] 5.6 Update `internal/server/tool_index.go` — fd-bound pattern (`os.OpenFile(O_NONBLOCK)` → `f.Stat` → verify `IsRegular()` → `io.ReadAll`) when reading file, then call `st.IndexWithFilePath` (gated on a `fileBacked` var so inline content with a `path` arg stays non-file-backed)
- [x] 5.7 Wire deny checker in `internal/server/server.go` — in `getStore`'s `sync.Once`, call `store.SetDenyChecker(...)` using `security.EvaluateFilePath` with `s.readDenyGlobs` and `s.projectDir`
- [x] 5.8 Tests in `internal/store/stale_test.go` covering: fresh file (no refresh), modified file (auto-refresh), content-only source (no stale check), deleted file (graceful skip), denied file (skip on refresh), second-update regression, secret-bearing file (sanitized hash matches), NULL→non-NULL file_path, symlink escape (denied target blocked), non-regular file (FIFO skipped), cooldown throttle, migration round-trip. Plus server tests `TestIndex_PathAutoRefreshOnSearch` and `TestIndex_PathNonRegularRejected`.

> **Notes (deliberate refinements + isolated review):**
> - **O_NONBLOCK (vs design's `os.Open`):** both fd-bound read sites open with `os.O_RDONLY|syscall.O_NONBLOCK`. A plain `os.Open` on a FIFO blocks until a writer appears — *before* the `IsRegular` guard can reject it — which would hang search/index. O_NONBLOCK is a no-op for regular files; `IsRegular` still rejects char/block devices, sockets, and dirs.
> - **Isolated review P1 (fixed) — size guard:** `fileChangedSince` now checks `info.Size() > maxSourceBytes` before `io.ReadAll`, mirroring `handleIndex`. Without it, a file that grew past the limit after indexing would be slurped into memory (OOM risk) only for `IndexWithFilePath` to reject it. Indexed as `kk:review-findings`.
> - **Isolated review P2 (fixed) — `indexed_at` advance:** when a file is touched (mtime bumped) but its sanitized content is unchanged, `refreshStaleSources` now advances `indexed_at` (new `stmtUpdateSourceIndexedAt`, keyed by source id) so the mtime fast-path stops re-reading+re-hashing it on every search. Scoped to the file-backed refresh path only — NOT the shared dedup statements, which would reset ephemeral/session TTLs. Indexed as `kk:review-findings`.

## Task 6: Canonicalize index source label
- **Status:** done
- **Depends on:** —
- **Docs:** [design.md#4-canonicalize-index-source-label](./design.md#4-canonicalize-index-source-label), [implementation.md#task-6-canonicalize-index-source-label](./implementation.md#task-6-canonicalize-index-source-label)

### Subtasks
- [x] 6.1 Change `internal/server/tool_index.go` line 52-53: replace `source = filepath.Base(path)` with `source = path` (resolved absolute path)
- [x] 6.2 Add tests: two relative paths to same file produce one source; two files with same basename in different dirs produce two sources

> **Isolated review (P2, fixed) — absolute inputs bypassed `filepath.Clean`:** `handleIndex` only resolved+`Clean`ed paths inside the `if !filepath.IsAbs(path)` branch, so an absolute input with traversal segments (`/tmp/a/../b.md`) was stored uncleaned as BOTH the new label and the `file_path` column — two spellings of one absolute file would then dedup-fail into two sources, defeating Task 6's canonicalization goal. Added an `else { path = filepath.Clean(path) }` branch (safe: the earlier deny check runs `EvaluateFilePath`, which Cleans+EvalSymlinks independently, so post-deny cleaning doesn't alter the security decision). Added regression test `TestIndex_PathAbsoluteCanonical`, and strengthened the pre-existing `TestIndex_PathAutoLabel` to assert the full resolved path instead of an incidental basename substring. The design §4.2 claim "by this point `path` is already an absolute, clean path" held only for relative inputs. Indexed as `kk:review-findings`. (pal HIGH suggesting project-relative slash labels was rejected: the `file_path` column is already absolute by necessity, the knowledge DB is single-machine, and Windows is out of scope — kept absolute labels per spec + upstream parity.)

## Task 7: Fetch cache key includes URL
- **Status:** done
- **Depends on:** Task 1 (SSRF changes modify tool_fetch.go)
- **Docs:** [design.md#5-fetch-cache-key-includes-url](./design.md#5-fetch-cache-key-includes-url), [implementation.md#task-7-fetch-cache-key-includes-url](./implementation.md#task-7-fetch-cache-key-includes-url)

### Subtasks
- [x] 7.1 Add `composeFetchCacheKey(label, url string) string` to `internal/server/tool_fetch.go` — returns `label + "|" + url`
- [x] 7.2 Use `composeFetchCacheKey` for **both** cache lookup (`GetSourceMeta`) and as **storage label** when indexing fetched content — ensures cache hit on next call for same label+url
- [x] 7.3 Add tests: two URLs with same explicit `source` label get separate cache entries; `capy_search(source: "my-label")` partial match still finds composite-keyed sources via LIKE

> **Implementation notes (deliberate decisions + isolated review):**
> - **Single `cacheKey` for lookup + storage.** Introduced `cacheKey := composeFetchCacheKey(label, url)` once; both the `GetSourceMeta` cache lookup AND all four indexing calls (`IndexJSON`/`IndexPlainText`/`Index`) use it. The now-redundant `if source == "" { source = url }` block was removed — `source` is read only once (into `label`); indexing no longer references `source`.
> - **`GetSourceMeta` is EXACT-match** (`store.go`: `WHERE label = ?`), so store-and-lookup by the same composite is consistent. This is why the existing `assertSourceKind` test helper had to gain a `url` param and query `composeFetchCacheKey(label, url)` — a bare-label exact lookup now misses. (Production search-by-label is unaffected: `search.go` uses LIKE.)
> - **Response messages surface the friendly `label`, not the composite `indexed.Label`/`meta.Label`.** The composite is an internal cache key; users get clean `source: "<label>"` guidance that still works via the LIKE source filter. This preserves the pre-Task-7 user-visible "from:"/"source:" behavior exactly — the composite is invisible to callers.
> - **No-source path yields `url|url`** (label defaults to url). Kept uniform (no `label == url` special-case) — pal review flagged the absence of a branch as a positive.
> - **Isolated review (both reviewers APPROVE, no P0/P1).** code-reviewer P2: docstring on `composeFetchCacheKey` was imprecise about `|`-safety for user-chosen labels — reworded to state the key is opaque (never split), so a `|` in a user label is harmless. P3 (url|url doubling) and pal LOW (multi-arg `fmt.Sprintf` formatting) rejected: keeping uniform compose + matching the file's pre-existing grouped-arg style. No findings indexed (none systemic).

## Task 8: Batch concurrency
- **Status:** done
- **Depends on:** —
- **Docs:** [design.md#7-batch-concurrency](./design.md#7-batch-concurrency), [implementation.md#task-8-batch-concurrency](./implementation.md#task-8-batch-concurrency)

### Subtasks
- [x] 8.1 Add `golang.org/x/sync` dependency (not currently in `go.mod`) — was a transitive dep at v0.18.0; `go get` promoted it to a direct require at v0.21.0, then `go mod tidy` cleared the `// indirect` marker
- [x] 8.2 Parse `concurrency` parameter in `handleBatchExecute` — `int(req.GetFloat("concurrency", 1))`, clamp to [1, min(8, len(commands))] via new `maxBatchConcurrency = 8` const
- [x] 8.3 Extract existing serial loop into `executeBatchSerial(ctx context.Context, commands []CommandInput, timeout int, exec *executor.PolyglotExecutor) []string` (verbatim extraction — shared timeout budget + cascading skip preserved)
- [x] 8.4 Add `executeBatchParallel(ctx context.Context, commands []CommandInput, timeout, concurrency int, exec *executor.PolyglotExecutor) []string` — `errgroup.Group` with `SetLimit`, pre-allocated index-keyed results slice, each command gets the **full** timeout, per-command error handling (each goroutine returns nil; errors land in-slot), timed-out commands append `(timed out)` to their partial output (mirrors serial's preserve-partial-output behavior) without affecting siblings
- [x] 8.5 Route: `concurrency <= 1` → serial, else → parallel. Rest of handler unchanged
- [x] 8.6 Add `concurrency` to `capy_batch_execute` MCP tool schema — **NOTE: schema lives in `internal/server/tools.go` (`toolBatchExecute`), not `server.go` as the design/impl docs stated.** Used `mcp.WithNumber` + `mcp.Min(1)`/`mcp.Max(8)` to match the existing `timeout` param (mcp-go has no `WithInteger`)
- [x] 8.7 Add tests: serial at concurrency=1 (`TestBatchExecute_ConcurrencySerialDefault`), parallel via handler + clamp 4→3 (`TestBatchExecute_ConcurrencyParallel`), speedup (`TestExecuteBatchParallel_Speedup`), ordering preserved (`TestExecuteBatchParallel_OrderPreserved`), per-command error isolation (`TestExecuteBatchParallel_ErrorIsolation`), timeout isolation + marker (`TestExecuteBatchParallel_TimeoutIsolated`), sub-second timeout regression (`TestExecuteBatchParallel_SubSecondTimeout`); all pass under `-race`

> **Isolated review (corroborated finding, fixed):** Both reviewers (kk:code-reviewer P2 @85%, pal/gemini HIGH) flagged that `executeBatchParallel`'s `timeoutSec := int((ms).Seconds())` truncates any sub-second `timeout` to `0`, which `executor.Execute` reads as "no timeout → 30s default" (the serial path is immune via its `remainingSec <= 0` skip guard). Fixed by clamping `if timeoutSec <= 0 && timeout > 0 { timeoutSec = 1 }`, added regression test `TestExecuteBatchParallel_SubSecondTimeout`, indexed as `kk:review-findings`. Also applied two pal LOW cleanups (preallocated the serial `outputs` slice; condensed the clamp to `max(1, min(concurrency, maxBatchConcurrency, len(commands)))`) and strengthened the `_ = g.Wait()` comment per code-reviewer P3. Rejected: code-reviewer P2 5-param signature (4 non-ctx params mirror `executeBatchSerial`; a config struct over-engineers two package-private twins) and P3 speedup-test flake (the 3s ceiling must stay below the ~4s serial time to actually prove concurrency; warmup mitigates).

## Task 8b: Fetch-and-index batch requests
- **Status:** done
- **Depends on:** Task 7 (fetch cache key), Task 8 (concurrency primitives)
- **Docs:** [design.md#7b-fetch-and-index-batch-requests-with-concurrency](./design.md#7b-fetch-and-index-batch-requests-with-concurrency), [implementation.md#task-8b-fetch-and-index-batch-requests](./implementation.md#task-8b-fetch-and-index-batch-requests)

### Subtasks
- [x] 8b.1 Add `requests` parameter parsing in `tool_fetch.go` — array of `{url, source?}` objects, alternative to `url`+`source`. Parser is `coerceFetchRequests` in `coerce.go` (mirrors `coerceCommandsArray`: tolerates double-serialized JSON string, skips entries without a `url`). New `fetchRequest{URL, Source}` struct.
- [x] 8b.2 Batch execution (`handleFetchBatch`): fetch concurrently via `errgroup.Group` + `SetLimit`, serialize FTS5 writes after all fetches complete. Per-URL cache check via `composeFetchCacheKey`. Refactored the single-URL fetch+index core into two shared helpers — `fetchRemoteContent` (concurrent-safe network read) and `indexFetchedContent` (serial SQLite write) — used by BOTH single and batch paths to prevent drift.
- [x] 8b.3 Batch response: per-URL preview capped at `fetchBatchPreviewLen` (384), aggregate summary line + per-URL status lines (`✓`/`✗`/`↪`)
- [x] 8b.4 Add `requests` and `concurrency` to `capy_fetch_and_index` MCP schema — **NOTE: schema lives in `internal/server/tools.go` (`toolFetchAndIndex`), NOT `server.go` as the design/impl docs stated** (same discrepancy Task 8.6 found). Also dropped `mcp.Required()` from `url` so batch-only calls are valid; the handler enforces url-XOR-requests.
- [x] 8b.5 Add tests: batch indexes all URLs + searchable, order preserved, partial cache hits, preview capping, per-URL error isolation, concurrency clamp, git-platform redirect, missing url+requests, `coerceFetchRequests` unit table. All pass under `-race`.

> **Deliberate decisions:**
> - **HTML→markdown conversion folded into the serial `indexFetchedContent` step** (design §7b prose said "each goroutine converts HTML→markdown"). Conversion is CPU-bound and negligible vs network latency; the hard invariant — *parallel fetches, serial FTS5 writes* — holds. Keeping one shared index helper for single+batch avoids two divergent content-type routers.
> - **`fetchRemoteContent` returns `errMsg string` (empty = success), not `error`** — preserves the pre-existing capitalized user-facing messages ("Failed to fetch…", "Invalid URL…") without the lowercase-error-string idiom friction.
> - **Git-platform issue/PR/MR URLs are flagged per-URL in batch** (`↪ … not fetched`), mirroring the single-URL redirect, instead of BM25-fragmenting them.
> - **Batch-wide `kind`/`force`/`concurrency`**; per-request objects carry only `{url, source?}`.

> **Isolated review (kk:code-reviewer + pal/gemini-3.1-pro; fixes applied):**
> - **pal HIGH (fixed) — unbounded batch → OOM/DoS:** `SetLimit` bounds in-flight fetches, but every fetched body (≤`fetchMaxBody` 10MB) is retained in `items[]` until the serial phase, so peak memory grows with `len(requests)`, not concurrency. Added `maxFetchBatchRequests = 20` cap with a fail-loud error before the pool spawns. Indexed as `kk:review-findings` (generalizable: fan-out handlers must cap item COUNT, not just worker count). Regression test `TestFetchBatch_TooLargeRejected`.
> - **pal MEDIUM (fixed) — UTF-8 truncation:** `s[:384]`/`s[:fetchPreviewLen]` byte-slicing can split a multibyte rune → garbled `�` in the JSON response. Added rune-safe `truncateRunes` helper (backtracks via `utf8.RuneStart`), applied to BOTH batch and single-URL previews (the single path was a pre-existing latent bug). Indexed as `kk:review-findings`. Tests `TestTruncateRunes` + `TestFetchBatch_PreviewRuneSafe`.
> - **code-reviewer P2 (fixed):** mirrored `executeBatchParallel`'s fuller `g.Wait()` discard comment; resolve `st := s.getStore()` once and pass into `fetchOne` (no per-goroutine `sync.Once` re-entry); fail-loud "Invalid requests" error when `requests` is supplied but yields no usable entries (`TestFetchBatch_InvalidRequestsRejected`).
> - **code-reviewer P3 (fixed):** renamed `indexedCount`→`okCount`/`totalBytes`→`freshBytes`, clarified the summary line ("Processed N/M URLs — X newly indexed, Y from cache"); anchored `TestFetchBatch_OrderPreserved` to `✓ <label> —` lines.
> - **Rejected (keep as-is):** code-reviewer P1 http.Client-per-call (the pooling `Transport` is already a shared singleton via `getFetchTransport`; the Client struct + CheckRedirect closure are negligible for an I/O-bound path and pre-existing); P3 tagged-union compile-safety on `fetchItemResult` (idiomatic Go; early returns + doc comment enforce single-state); P3 schema-marshal test (the handler path is already exercised by the batch tests).

## Task 9: Extend cleanup with project-scope purge
- **Status:** done
- **Depends on:** —
- **Docs:** [design.md#8-extend-cleanup-with-project-scope-purge](./design.md#8-extend-cleanup-with-project-scope-purge), [implementation.md#task-9-extend-cleanup-with-purge_all](./implementation.md#task-9-extend-cleanup-with-purge_all)

### Subtasks
- [x] 9.1 Add `PurgeCounts` struct and `PurgeAll(dryRun bool) (PurgeCounts, error)` to `internal/store/cleanup.go` — if dryRun return counts; else DELETE FROM sources/chunks/chunks_trigram/vocabulary, clear fuzzy cache (`fuzzyCacheMu.Lock; fuzzyCache = make(...); Unlock`), then VACUUM. Deletes wrapped in `sqliteutil.BeginImmediate` tx (mirrors `evict`); fuzzy cache cleared after commit; `Vacuum()` runs on its own connection after.
- [x] 9.2 Add `Reset()` method to `SessionStats` in `internal/server/stats.go` — zero all counters and re-initialize maps under mutex. `SessionStart` intentionally preserved (session hasn't restarted — uptime stays accurate).
- [x] 9.3 Add `purge_all` boolean parameter to `handleCleanup` in `internal/server/tool_cleanup.go` — mutual exclusion with source/purge_ephemeral/purge_session, call `st.PurgeAll`, call `s.stats.Reset()` if not dryRun (Reset runs before the cleanup's own response-tracking, so prior counters zero out).
- [x] 9.4 Add `purge_all` to `capy_cleanup` MCP tool schema — **NOTE: schema lives in `internal/server/tools.go` (`toolCleanup`), NOT `server.go` as the design/impl docs stated** (same discrepancy as Task 8.6 / 8b.4). Also updated the tool description.
- [x] 9.5 Add tests — store: dry-run reports counts without deleting, full purge empties all four tables, fuzzy cache cleared (no stale correction after purge), empty-store no-op. Server/handler: 3 mutual-exclusion combos, dry-run preserves searchability, non-dry-run resets KB (search returns empty-KB guidance) + stats (`capy_index`/`BytesIndexed` zeroed, `SessionStart` survives). All pass under `-race`.

> **Pre-existing issue flagged (NOT fixed — out of scope):** `internal/server/stats.go` is gofmt-unformatted at HEAD — the `CacheHits`/`CacheBytesSaved` struct-field block (and the matching `Snapshot()` literal) use wider tab alignment than the rows above. Confirmed pre-existing via `git show HEAD:...stats.go | gofmt -l`. The Task-9 `Reset()` addition did not introduce it; left untouched to keep this diff focused. Fix separately with `gofmt -w internal/server/stats.go`.

> **Isolated review (kk:code-reviewer APPROVE + pal/gemini-3.1-pro; one HIGH fixed):**
> - **pal HIGH (fixed) — VACUUM error masked a succeeded purge:** `PurgeAll` originally returned `fmt.Errorf("vacuuming after purge: %w", err)` if `Vacuum()` failed. But the purge had already committed (tables empty, cache cleared) — `Vacuum` only reclaims disk space. Propagating it made `handleCleanup` discard the counts and report a total failure to the MCP client (e.g. on `SQLITE_BUSY` under WAL contention), risking caller retry loops. Fixed to log-and-continue (`slog.Warn`, `return counts, nil`), mirroring the existing auto-vacuum path in `Cleanup`. Indexed as `kk:review-findings` (general rule: post-commit best-effort steps must not surface as operation failures).
> - **code-reviewer P2 (fixed) — unchecked `Scan` in test:** `TestPurgeAllDryRunReportsCountsWithoutDeleting` dropped the `Scan` error (a silent failure would pass the assertion vacuously); wrapped in `require.NoError`, matching `TestPurgeAllEmptiesEveryTable`.
> - **Rejected:** code-reviewer P2 (thread `ctx` into db calls) — pre-existing, file-wide pattern (`cleanupOversized`/`cleanupByTTL`/`evict` all non-context); a separate refactor, not Task 9. P3 (`BeginImmediate` lockTable `"sources"`) — cosmetic, matches `evict` precedent. P3 (lowercase "cleanup error") — user-facing MCP display string, not a Go `error` value; matches two pre-existing sites. P3 (`*s = SessionStats{...}` struct-reassignment in `Reset`) — **would introduce a bug**: `SessionStats` embeds a `sync.Mutex`, so reassigning the struct while holding the lock zeroes the mutex and the deferred `Unlock()` panics (`go vet`: "copies lock value"). Explicit field-by-field zeroing is the correct form; indexed alongside the HIGH.

## Task 10: Preserve shell executor PATH
- **Status:** done
- **Depends on:** —
- **Docs:** [design.md#9-preserve-shell-executor-path](./design.md#9-preserve-shell-executor-path), [implementation.md#task-10-preserve-shell-executor-path](./implementation.md#task-10-preserve-shell-executor-path)

### Subtasks
- [x] 10.1 Add `quotePosixSingle(value string) string` to `internal/executor/executor.go` — single-quote with `'` → `'\''` escaping
- [x] 10.2 Add `buildShellScript(code, inheritedPath string) string` — pure function (takes PATH as parameter, not `os.Getenv`), if `inheritedPath` non-empty prepends `export PATH=<quoted>\n`
- [x] 10.3 Use `buildShellScript(code, os.Getenv("PATH"))` in `Execute` when `req.Language == Shell`, before writing to script file (added in the existing `if req.Language == Shell` perm block, so `code` is transformed exactly once before `os.WriteFile`)
- [x] 10.4 Add test: `TestBuildShellScript` (restore line present, empty PATH unchanged, single-quote escaping) + `TestQuotePosixSingle` table + integration `TestExecuteShellPreservesPATH`. Both new funcs at 100% coverage; all pass under `-race`.

> **Deliberate refinements + isolated review:**
> - **Reused `quotePosixSingle` in `wrap.go`:** `injectFileContent`'s Shell case (`wrap.go:44`) had an identical inline `'`→`'\''` escape. Replaced it with the new helper — behavior-preserving (verified by the pre-existing `TestInjectFileContentShell`), removes a drift-prone duplicate of security-relevant escaping. Not in the original plan; small, related, low-risk.
> - **`bash <script>` invocation means no shebang on line 1** (`buildCommand` passes the script as an arg, not `./script.sh`), so prepending `export PATH=...` at the top is safe. `autoWrap` is a no-op for Shell. For `ExecuteFile`+Shell the order is `injectFileContent` → `autoWrap` (no-op) → `buildShellScript` prepend, which is correct (file-path quoting doesn't depend on PATH).
> - **gofmt comment quirk:** gofmt's doc-comment formatter rewrites a literal pair of single quotes (`''`) into a typographic `”`, which mangled the `'\''` idiom in prose. The doc comment was reworded to avoid an adjacent single-quote pair; the raw-string literal in code is the source of truth (documented inline).
> - **Isolated review (pal/gemini-3.1-pro APPROVE, 0 findings; kk:code-reviewer APPROVE).** No corroborated findings, no P0/P1/P2 accepted. Applied two P3 comment-only fixes (clarified the integration test verifies plumbing/visibility not the clobber mechanism; documented the gofmt constraint on `quotePosixSingle`). **Rejected** code-reviewer P2 (snapshot PATH in `NewExecutor`): the design mandates a call-site `os.Getenv("PATH")`, and `BuildSafeEnv` reads the live process env at exec time — snapshotting at construction would *introduce* divergence between the in-script export and the process env, not remove it. No findings indexed (none systemic).

## Task 11: Final verification
- **Status:** done
- **Depends on:** Task 1, Task 2, Task 3, Task 3b, Task 4, Task 5, Task 6, Task 7, Task 8, Task 8b, Task 9, Task 10

### Subtasks
- [x] 11.1 Run `test` skill to verify all tasks — full suite green via `make test` AND `make test-race` (all 18 packages `ok`, no race reports). Go profile detected; go/test rubric (table-driven + named subtests + `-race`) already satisfied by the per-task tests.
- [x] 11.2 Run `document` skill — reconciled design.md OUTDATED_DOC items (see notes below); README/AGENTS.md need no param-level edits (tools described at capability level; new params self-document via their MCP schema description strings in tools.go); go/document profile rubric is **N/A** (CLI/CI conditionals only — feature touches neither). No new ADR (see below).
- [x] 11.3 Run `review-code` skill (isolated: kk:code-reviewer **APPROVE** + pal/gemini-3.1-pro) — no P0; fixes applied for the actionable findings (see notes below).
- [x] 11.4 Run `review-spec` skill (isolated kk:spec-reviewer) — **CONFORMANT**, no P0/P1; all P2/P3 deviations documented/intentional. OUTDATED_DOC items reconciled.
- [x] 11.5 Re-ran `make bench-quality` at HEAD (1fbdf6f) — **no regression**. vs pre-feature baseline (`feat-bench` @7880796, identical dataset_hash): overall R@1 0.897→0.910, NDCG@10 0.950→0.955, MRR 0.938→0.945; curated R@1 +0.033, plaintext R@1 +0.033; json/markdown/transcript unchanged; every delta ≥ 0 (no negatives). The phrase-frequency reranker (Task 4) is the net gain. Reproduces the prior `upstream_sync` result exactly.

> **Final-verification fixes (option "Bug + context + docs"; all under `-race`, gofmt + vet clean):**
> - **truncateLabel UTF-8 (code-reviewer + pal MEDIUM, fixed):** `truncateLabel` in `tool_batch.go` byte-sliced `joined[:80]`, which can split a multibyte rune and emit an invalid sequence into the `batch:<labels>` source label. This is the exact bug class Task 8b fixed for fetch previews via `truncateRunes`, but that sweep missed `truncateLabel`. Replaced with `truncateRunes(joined, 80)`; regression test `TestTruncateLabel_RuneSafe`. Indexed as `kk:review-findings` (general rule: when you introduce a rune-safe truncation helper, grep the whole package for sibling `[:N]` slices on user data in the same sweep).
> - **Fetch context threading (pal HIGH, fixed):** `handleFetchAndIndex` ignored its `context.Context` (`_ context.Context`) and `fetchRemoteContent` used `http.NewRequest` (no ctx), so a client cancellation could not abort in-flight fetches — up to `maxFetchBatchRequests` goroutines would block on the network until `fetchTimeout`. Threaded `ctx` through `handleFetchAndIndex` → `handleFetchBatch` → `fetchOne` → `fetchRemoteContent` (now `http.NewRequestWithContext`). Aligns with the indexed project rule "thread ctx into long ops that have a live one in scope" (vault-compact finding). Regression test `TestFetchRemoteContent_ContextCanceled`. (Pre-existing single-URL behavior the batch path had inherited.)
> - **Hygiene comments (code-reviewer P3, applied):** `eval.go` — explicit "DO NOT replace with filepath.Join (it Cleans)" guard at the uncleaned `physicalInput` construction; `ssrf.go` — `nat64Prefix`/`ipv4CompatPrefix` documented read-only-after-init (mutating would silently weaken the guard); `search.go` — documented why stale re-index reuses the stored `content_type`; `tool_batch.go` — documented the deliberate `timeout == 0` → executor-default pass-through.
> - **Rejected (kept as-is):** http.Client-per-fetch-call (transport is the shared sync.Once singleton; pooling intact); `syscall.O_NONBLOCK` Windows portability (Windows is out of scope per design); content_type re-detect on refresh (now documented, behavior stable).

> **Documentation reconciliation (11.2) — design.md was written against the TS reference layout; corrected to match the Go implementation:**
> - §4.2: "by line 33 `path` is already absolute & clean" was true only for relative inputs; absolute inputs are now Cleaned in an `else` branch (Task 6 note). Corrected.
> - §6a.2: `classifyIP` is implemented as cooperating helpers (`classifyIP`/`classifyParsedIP`/`classifyIPv4`/`classifyIPv6`/`embeddedIPv4`), not one function; the impl also adds NAT64 (`64:ff9b::/96`) coverage beyond the design list. Noted.
> - §3.4 / §7b.3 / impl: "add schema param in server.go" is wrong for capy — schemas live in `internal/server/tools.go` (`toolXxx()` + `registerTools()`). Already captured in Task 8.6/8b.4/9.4 notes and indexed as `kk:project-conventions`; design Files-touched lists annotated.
> - **No new ADR:** the ported changes are syncs of decisions already covered by existing ADRs (009/010/011/013/014) or bug fixes; the notable capy-specific divergences (sanitized-hash stale detection, multiplicative reranker passes, SSRF-strict default) are preserved in design.md's "Deliberate divergences" table + §3.5 — consistent with how prior syncs under `docs/done/` preserve decisions.
