# Implementation Plan: Upstream Sync v1.0.89→v1.0.136

> **Design doc:** `design.md` (same directory)
> **Language:** Go 1.23+
> **Key constraint:** All changes must preserve backward compatibility — existing databases, MCP schemas, and capy-specific features (synonym expansion, entity boosting, diversification, source-kind separation) must work identically after the sync.

## Implementation Order

Changes are ordered by dependency chain and priority. Security fixes come before feature work to avoid building on vulnerable foundations.

### Phase 1: Security Hardening (P0)

**Task 1: SSRF Guard Improvements** (design §6a)

Create `internal/server/ssrf.go` with three functions:
- `classifyIP(rawIP string) error` — accepts raw IP string, strips zone-IDs, handles IPv4-mapped IPv6 via recursion, classifies against all categories (see design §6a.2 for full list). Return descriptive errors ("loopback address forbidden", "private network forbidden", "link-local address forbidden", etc.)
- `validateFetchScheme(rawURL string) error` — parse URL, reject any scheme not in `{"http", "https"}`. Block `file`, `gopher`, `javascript`, `data`, empty scheme explicitly
- `newSSRFSafeTransport() *http.Transport` — custom `DialContext` that resolves DNS via `net.DefaultResolver.LookupIPAddr`, classifies every IP via `classifyIP`, dials first passing IP. Use `net.Dialer` for the actual TCP connection after validation

Modify `tool_fetch.go`:
- Replace `validateFetchURLFunc(url)` call with `validateFetchScheme(url)` (scheme check before anything else)
- Replace default `http.Client{}` with `&http.Client{Transport: newSSRFSafeTransport(), ...}` — keep existing Timeout and CheckRedirect
- Remove `validateFetchURL` function and `validateFetchURLFunc` var
- Update tests: `validateFetchURLFunc` override pattern replaced by Transport-level testing. Test helper uses a custom Transport that allows localhost for `httptest.NewServer`

→ verify: `go test ./internal/server/ -run TestSSRF` passes; `go test ./internal/server/ -run TestFetch` still passes with httptest servers

**Task 2: Path Traversal Bypass Fix** (design §6c)

Modify `internal/security/eval.go`:
- Change `EvaluateFilePath` signature to accept `projectRoot string` as third parameter
- When `projectRoot != ""` and `filePath` is relative (not starting with `/`): resolve to `filepath.Clean(filepath.Join(projectRoot, filePath))`
- Match deny globs against both raw `filePath` AND resolved absolute path
- Return denied=true if either matches

Update all three callers (grep for `EvaluateFilePath` before implementation to catch any additions):
- `internal/server/security_check.go:45`: pass `s.projectDir` as projectRoot
- `internal/hook/pretooluse.go:157`: pass `projectDir` (already available in the hook context)
- `internal/server/server.go` (deny checker closure wired in Task 5): pass `s.projectDir`

→ verify: `go test ./internal/security/ -run TestEvaluateFilePath` including test case: relative `../../.ssh/id_rsa` from projectRoot `/home/user/project` is caught by glob `/home/user/.ssh/**`

**Task 3: Executor Env Deny List Expansion** (design §6d)

Modify `internal/executor/env.go`:
- Add .NET/C# entries to `deniedEnvVars` map: `CORECLR_PROFILER`, `CORECLR_PROFILER_PATH`, `CORECLR_PROFILER_PATH_32`, `CORECLR_PROFILER_PATH_64`, `CORECLR_PROFILER_PATH_ARM32`, `CORECLR_PROFILER_PATH_ARM64`, `CORECLR_ENABLE_PROFILING`, `DOTNET_PROFILER_PATH`, `DOTNET_PROFILER_PATH_32`, `DOTNET_PROFILER_PATH_64`, `DOTNET_PROFILER_PATH_ARM32`, `DOTNET_PROFILER_PATH_ARM64`, `DOTNET_DiagnosticPorts`, `DOTNET_BUNDLE_EXTRACT_BASE_DIR`
- Add `COMPlus_` prefix check in `BuildSafeEnv` alongside existing `BASH_FUNC_` check

→ verify: `go test ./internal/executor/ -run TestBuildSafeEnv` including test that CORECLR_PROFILER and COMPlus_EnableDiagnostics are stripped

**Task 3b: Apply Read Deny-Policy to `capy_index(path)`** (design §6e)

Modify `internal/server/tool_index.go`:
- Add `s.checkFilePathDenyPolicy(path)` call immediately after extracting the `path` parameter (before the `filepath.IsAbs` resolution logic at line 26). When `path == ""` (inline content), skip the check
- If denied, return the deny-policy error result and never proceed to file I/O

→ verify: `go test ./internal/server/ -run TestIndexDenyPolicy` covering: denied absolute path returns error and produces no FTS5 chunks; denied relative path with `../` traversal returns error; inline `content` with a `source` label matching a deny pattern still indexes successfully (deny only applies to `path`)

### Phase 2: Search Quality (P0-P1)

**Task 4: Phrase Frequency Reranker** (design §2)

Add to `internal/store/search.go`:
- `countAdjacentPairs(positionLists [][]int, terms []string, gap int) int` — sweep-line algorithm counting ordered adjacent pairs within gap window. Each right position consumed at most once
- Inside `rerank()`, in the multi-term proximity block: `countAdjacentPairs` builds its own position lists from raw (non-synonym-expanded) `terms` against `strings.ToLower(r.Content)`. This scan is always performed — it cannot reuse the existing `posLists` because those are synonym-expanded (wrong `terms[i].length` offsets) and may be nil when `minSpan` was found via the highlight fast path (`search.go:284-286`)
- Compute `phraseBoost := 0.5 * math.Min(1.0, float64(adjacentPairs)/4.0)`. Add to proximity boost: `r.FusedScore *= (1.0 + proximityBoost + phraseBoost)`. Title boost remains a separate multiplicative pass (capy's existing two-pass approach, deliberate divergence from TS's single additive pass)

→ verify: `go test ./internal/store/ -run TestCountAdjacentPairs` + `go test ./internal/store/ -run TestRerank` including test case: short doc with 4+ adjacent pairs outranks long doc with 1 occurrence at same minSpan

**Task 5: Hash-Based Stale Detection** (design §3)

**5a. Schema + migration:**
- `internal/store/schema.go`: add `file_path TEXT` to `CREATE TABLE sources` definition
- `internal/store/migrate.go`: add migration (next version number) that runs `ALTER TABLE sources ADD COLUMN file_path TEXT`

**5b. Index changes:**
- `internal/store/store.go:175-177`: modify `stmtInsertSource` from 6 columns to 7 — add `file_path` after `kind`
- `internal/store/index.go`: add `filePath string` parameter to `indexPreparedChunks` signature (currently has `content, label, contentType string, kind SourceKind, chunks []Chunk`). Convert to `sql.NullString{String: filePath, Valid: filePath != ""}` before `stmtInsertSource.Exec`. Update both callers: `Index` passes `""`, new `IndexWithFilePath` passes the actual path. Add `IndexWithFilePath(content, label, contentType string, kind SourceKind, filePath string) (*IndexResult, error)` as a public entry point
- Update `stmtFindSourceByLabel` to also return `file_path` — needed to detect the NULL→non-NULL transition in the dedup path
- In the same-hash dedup path (`index.go:113-133`): when `filePath != ""` and the existing source has `file_path = NULL`, update the source row to set `file_path` via new `stmtUpdateSourceFilePath`. This handles the case where a source was first indexed inline (file_path=NULL) then re-indexed from a file path with identical content
- Add any new prepared statements (`stmtUpdateSourceFilePath`, stale detection queries) to the `Close()` method's statement list (`store.go:317-324`). Missing entries leak statements

**5c. Stale detection:**
- `internal/store/search.go`: add `denyChecker func(string) bool` field, `SetDenyChecker` method, `lastRefreshTime atomic.Int64` (Unix nanos, 5-second cooldown). `refreshStaleSources() int` returns refresh count (not stored globally — avoids concurrency issues). Call at top of `SearchWithFallback` before RRF pass — early-return if cooldown hasn't elapsed
- `refreshStaleSources` queries `SELECT label, file_path, content_hash, indexed_at, content_type, kind FROM sources WHERE file_path IS NOT NULL`. Parse `indexed_at` with format `"2006-01-02 15:04:05"` (consistent with `cleanup.go:231`). Compare `mtime.UTC()` against parsed time (SQLite `CURRENT_TIMESTAMP` is UTC). Before reading, calls `s.denyChecker(filePath)` — skip if denied. Use fd-bound reads: `os.Open(filePath)` → `f.Stat()` (verify `IsRegular()`) → `io.ReadAll(f)` to prevent swap between deny check and read. **Hash comparison must use sanitized content:** `sanitize.StripSecrets(rawContent)` then `contentHash(sanitized)` — capy's stored `content_hash` is computed from post-`StripSecrets` content (`index.go:46`). Comparing raw bytes would cause infinite refresh for secret-bearing files. After confirming change, re-index via `s.IndexWithFilePath(sanitizedContent, label, contentType, kind, filePath)`

**5d. Tool + server wiring:**
- `internal/server/tool_index.go`: when file is read from disk, use fd-bound pattern (`os.Open` → `f.Stat` → verify `IsRegular()` → `io.ReadAll`), then call `st.IndexWithFilePath(content, source, "", store.KindDurable, path)`
- `internal/server/server.go`: after creating the store, call `store.SetDenyChecker(...)` wiring to `security.EvaluateFilePath` with `s.projectDir` and `s.readDenyGlobs`

→ verify: `go test ./internal/store/ -run TestStaleDetection` covering: fresh file (no refresh), modified file (auto-refresh), content-only source (no stale check), deleted file (graceful skip), denied file (skip on refresh), **second-update regression** (modify file twice — source remains file-backed after first refresh and detects second change), **secret-bearing file** (file with secrets produces matching hash after sanitization), **NULL→non-NULL file_path** (inline-indexed source re-indexed from file path gets file_path attached)

### Phase 3: Server/Tool Fixes (P1-P2)

**Task 6: Canonicalize Index Source Label** (design §4)

Modify `internal/server/tool_index.go` line 52-53:
- Change `source = filepath.Base(path)` to `source = path` (which is the resolved absolute path by this point)

→ verify: `go test ./internal/server/ -run TestIndex` including: two relative paths to same file produce one source; two files with same basename in different dirs produce two sources

**Task 7: Fetch Cache Key Includes URL** (design §5)

Modify `internal/server/tool_fetch.go`:
- Add `composeFetchCacheKey(label, url string) string` returning `label + "|" + url`
- Line 76: change `st.GetSourceMeta(label)` to `st.GetSourceMeta(composeFetchCacheKey(label, url))` for cache lookup
- When indexing (around line 155-168): use `composeFetchCacheKey(source, url)` as the **storage label** (not just for lookup). This ensures the cache lookup on the next call for the same label+url hits the correct entry. Search via `source:` partial matching still works because `LIKE '%' || ? || '%'` matches the user's label substring within the composite key

→ verify: `go test ./internal/server/ -run TestFetchCache` including: two URLs with same explicit source get separate cache entries

**Task 8: Batch Concurrency** (design §7)

Add `golang.org/x/sync` dependency (not currently in `go.mod`): `go get golang.org/x/sync`.

Modify `internal/server/tool_batch.go`:
- Parse `concurrency` parameter: `concurrency := int(req.GetFloat("concurrency", 1))`, clamp to [1, 8], then `min(concurrency, len(commands))`
- Extract existing serial loop into `executeBatchSerial(ctx, commands, timeout, executor) []string`
- Add `executeBatchParallel(ctx, commands, timeout, concurrency, executor) []string`:
  - Pre-allocate `results := make([]string, len(commands))`
  - Each command gets the **full** `timeout` value (matching upstream). Commands run concurrently so wall-clock is bounded by timeout, not timeout*N. A timed-out command records `(timed out)` in its slot without affecting siblings
  - Use `errgroup.Group` with `g.SetLimit(concurrency)`
  - Each goroutine writes to `results[i]` — no shared state
  - Errors are handled per-command (written as error text to result slot)
- Route: if `concurrency <= 1`, call `executeBatchSerial`; else `executeBatchParallel`
- Rest of handler (indexing, search, output) remains unchanged — operates on `perCommandOutputs` regardless of execution path

Modify `internal/server/server.go`:
- Add `concurrency` to `capy_batch_execute` tool schema as optional integer, min 1, max 8

→ verify: `go test ./internal/server/ -run TestBatchConcurrency` covering: serial at concurrency=1 (same behavior), parallel speedup at concurrency=4 with sleep commands, output ordering preserved, per-command error isolation. Note: executor is thread-safe (`sync.Once` for detection, `sync.Mutex` for background PIDs). Stats tracking happens after the parallel phase completes (serial).

**Task 8b: Fetch-and-Index Batch Requests** (design §7b)

Modify `internal/server/tool_fetch.go`:
- Add `requests` parameter parsing (array of `{url, source?}` objects) as alternative to `url`+`source`
- When `requests` provided: fetch all URLs concurrently via `errgroup.Group` with `SetLimit(concurrency)`, serialize FTS5 writes after all fetches complete
- Per-URL cache check via `composeFetchCacheKey`. Cached URLs skipped
- Batch response: per-URL preview capped at 384 chars, aggregate summary

Modify `internal/server/server.go`:
- Add `requests` and `concurrency` to `capy_fetch_and_index` MCP schema

→ verify: `go test ./internal/server/ -run TestFetchBatch` covering: single-URL backward compat, batch mode, partial cache hits, output preview capping

**Task 9: Extend Cleanup with purge_all** (design §8)

Add to `internal/store/cleanup.go`:
- `PurgeAll(dryRun bool) (PurgeCounts, error)` — if dryRun, return counts of sources/chunks/vocab; else DELETE FROM sources, chunks, chunks_trigram, vocabulary, clear fuzzy cache (`s.fuzzyCacheMu.Lock(); s.fuzzyCache = make(map[string]*string); s.fuzzyCacheMu.Unlock()`), then VACUUM

Add to `internal/server/stats.go`:
- `Reset()` method on `SessionStats` — zero all counters, re-initialize maps under mutex

Modify `internal/server/tool_cleanup.go`:
- Parse `purge_all` boolean parameter
- Mutual exclusion with `source`, `purge_ephemeral`, `purge_session`
- When set, call `st.PurgeAll(dryRun)` and also `s.stats.Reset()` if not dryRun
- Format response from structured `PurgeCounts`: "Purged N sources, M chunks, K vocab entries. Knowledge base reset."

Modify `internal/server/server.go`:
- Add `purge_all` to `capy_cleanup` tool schema as optional boolean

→ verify: `go test ./internal/server/ -run TestCleanupPurgeAll` covering: dry run reports counts, actual purge empties all tables + clears fuzzy cache, mutual exclusion enforced, post-purge fuzzy correction returns no stale results

### Phase 4: Executor (P2)

**Task 10: Preserve Shell Executor PATH** (design §9)

Add to `internal/executor/executor.go`:
- `quotePosixSingle(value string) string` — wraps in single quotes with `'` → `'\''` escaping
- `buildShellScript(code, inheritedPath string) string` — pure function taking PATH as explicit parameter (matching TS's `buildShellScriptContent(code, inheritedPath, platform)` pattern for testability). If `inheritedPath` non-empty, prepends `export PATH=<quoted>\n` to code

Modify `Execute` method in the shell branch (line 83-86):
- When `req.Language == Shell`, write `buildShellScript(code, os.Getenv("PATH"))` instead of raw `code` to the script file

→ verify: `go test ./internal/executor/ -run TestShellPATH` — test that a shell script sourcing a profile that overrides PATH still has the original PATH available

### Phase 5: Verification

**Task 13: Final Verification**

- Run full test suite: `go test ./...`
- Run `review-code` skill on the accumulated diff
- Run `review-spec` to verify implementation matches this design
- Run `test` skill for any coverage gaps
- Update `document` skill if API surface changed

→ verify: all tests pass, no regressions, code review clean
