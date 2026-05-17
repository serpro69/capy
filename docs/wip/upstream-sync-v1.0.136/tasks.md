# Tasks: Upstream Sync — context-mode v1.0.89→v1.0.136

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> Status: pending
> Created: 2026-05-17

## Task 1: SSRF guard improvements
- **Status:** pending
- **Depends on:** —
- **Docs:** [design.md#6a-ssrf-guard-scheme-validation--dns-rebinding-defense](./design.md#6a-ssrf-guard-scheme-validation--dns-rebinding-defense), [implementation.md#task-1-ssrf-guard-improvements](./implementation.md#task-1-ssrf-guard-improvements)

### Subtasks
- [ ] 1.1 Create `internal/server/ssrf.go` with `classifyIP(ip net.IP) error` — extract IP classification from existing `validateFetchURL`, return descriptive errors per IP category
- [ ] 1.2 Add `validateFetchScheme(rawURL string) error` in `ssrf.go` — reject any scheme not in `{"http", "https"}`
- [ ] 1.3 Add `newSSRFSafeTransport() *http.Transport` in `ssrf.go` — custom `DialContext` that resolves DNS via `net.DefaultResolver.LookupIPAddr`, classifies every IP via `classifyIP`, dials first passing IP via `net.Dialer`
- [ ] 1.4 Update `tool_fetch.go`: replace `validateFetchURLFunc(url)` with `validateFetchScheme(url)`, replace default `http.Client{}` with one using `newSSRFSafeTransport()`, remove old `validateFetchURL` and `validateFetchURLFunc`
- [ ] 1.5 Create `internal/server/ssrf_test.go` — test scheme blocking (file://, gopher://, data://), IP classification (loopback, private, link-local), Transport-level DNS rebinding defense
- [ ] 1.6 Update `tool_fetch_test.go` — replace `validateFetchURLFunc` override pattern with Transport-level test helper that allows localhost for `httptest.NewServer`

## Task 2: Path traversal bypass fix
- **Status:** pending
- **Depends on:** —
- **Docs:** [design.md#6c-path-traversal-bypass-in-file-deny-evaluation](./design.md#6c-path-traversal-bypass-in-file-deny-evaluation), [implementation.md#task-2-path-traversal-bypass-fix](./implementation.md#task-2-path-traversal-bypass-fix)

### Subtasks
- [ ] 2.1 Modify `EvaluateFilePath` in `internal/security/eval.go` to accept `projectRoot string` as third parameter — when non-empty and path is relative, resolve to absolute via `filepath.Clean(filepath.Join(projectRoot, filePath))`, match deny globs against both raw and resolved
- [ ] 2.2 Update `checkFilePathDenyPolicy` in `internal/server/security_check.go` to pass `s.projectDir` as projectRoot
- [ ] 2.3 Add tests in `internal/security/eval_test.go` — relative `../../.ssh/id_rsa` from projectRoot `/home/user/project` caught by glob `/home/user/.ssh/**`; absolute paths still work; empty projectRoot preserves old behavior

## Task 3: Executor env deny list expansion
- **Status:** pending
- **Depends on:** —
- **Docs:** [design.md#6d-executor-env-deny-list-netc-profiler-hijack-vectors](./design.md#6d-executor-env-deny-list-netc-profiler-hijack-vectors), [implementation.md#task-3-executor-env-deny-list-expansion](./implementation.md#task-3-executor-env-deny-list-expansion)

### Subtasks
- [ ] 3.1 Add 14 .NET/C# entries to `deniedEnvVars` in `internal/executor/env.go`: CORECLR_PROFILER, CORECLR_PROFILER_PATH (+ _32/_64/_ARM32/_ARM64), CORECLR_ENABLE_PROFILING, DOTNET_PROFILER_PATH (+ _32/_64/_ARM32/_ARM64), DOTNET_DiagnosticPorts, DOTNET_BUNDLE_EXTRACT_BASE_DIR
- [ ] 3.2 Add `COMPlus_` prefix check in `BuildSafeEnv` alongside existing `BASH_FUNC_` prefix check
- [ ] 3.3 Add tests in `internal/executor/env_test.go` — verify CORECLR_PROFILER and COMPlus_EnableDiagnostics are stripped from env output

## Task 4: Phrase frequency reranker
- **Status:** pending
- **Depends on:** —
- **Docs:** [design.md#2-phrase-frequency-reranker](./design.md#2-phrase-frequency-reranker), [implementation.md#task-4-phrase-frequency-reranker](./implementation.md#task-4-phrase-frequency-reranker)

### Subtasks
- [ ] 4.1 Add `countAdjacentPairs(positionLists [][]int, terms []string, gap int) int` to `internal/store/search.go` — sweep-line algorithm, each right position consumed at most once
- [ ] 4.2 Integrate into `rerank()`: after minSpan computation, build raw-term position lists (not synonym-expanded), compute `phraseBoost = 0.5 * min(1.0, adjacentPairs/4.0)`, add to proximity boost before applying to `FusedScore`
- [ ] 4.3 Add unit tests for `countAdjacentPairs` in `internal/store/search_test.go` — 0 pairs when terms don't appear, 1 pair for single adjacent occurrence, saturation at 4+, greedy consumption (no double-counting)
- [ ] 4.4 Add integration test: short doc with 4 adjacent pairs outranks long doc with 1 occurrence at same minSpan

## Task 5: Hash-based stale detection with auto-refresh
- **Status:** pending
- **Depends on:** Task 2 (for deny checker wiring)
- **Docs:** [design.md#3-hash-based-stale-detection-with-auto-refresh-on-search](./design.md#3-hash-based-stale-detection-with-auto-refresh-on-search), [implementation.md#task-5-hash-based-stale-detection](./implementation.md#task-5-hash-based-stale-detection)

### Subtasks
- [ ] 5.1 Add `file_path TEXT` to `CREATE TABLE sources` in `internal/store/schema.go`
- [ ] 5.2 Add migration in `internal/store/migrate.go` for `ALTER TABLE sources ADD COLUMN file_path TEXT`
- [ ] 5.3 Add `IndexWithFilePath(content, label, contentType string, kind SourceKind, filePath string) (*IndexResult, error)` in `internal/store/index.go` — modify `stmtInsertSource` to accept optional `file_path` via `sql.NullString`
- [ ] 5.4 Add `denyChecker func(string) bool` field, `SetDenyChecker` method, `LastRefreshCount int` field, and `refreshStaleSources()` method to `internal/store/search.go` — query file-backed sources, mtime gate → SHA-256 compare → re-index, check deny before read
- [ ] 5.5 Call `refreshStaleSources()` at the top of `SearchWithFallback` before the RRF pass
- [ ] 5.6 Update `internal/server/tool_index.go` — when file is read from disk, call `st.IndexWithFilePath` instead of `st.Index`, passing resolved absolute path
- [ ] 5.7 Wire deny checker in `internal/server/server.go` — after creating store, call `store.SetDenyChecker(...)` using `security.EvaluateFilePath` with `s.projectDir` and `s.readDenyGlobs`
- [ ] 5.8 Add tests in `internal/store/search_test.go` (or new `stale_test.go`) — fresh file (no refresh), modified file (auto-refresh), content-only source (no stale check), deleted file (graceful skip), denied file (skip on refresh)

## Task 6: Canonicalize index source label
- **Status:** pending
- **Depends on:** —
- **Docs:** [design.md#4-canonicalize-index-source-label](./design.md#4-canonicalize-index-source-label), [implementation.md#task-6-canonicalize-index-source-label](./implementation.md#task-6-canonicalize-index-source-label)

### Subtasks
- [ ] 6.1 Change `internal/server/tool_index.go` line 52-53: replace `source = filepath.Base(path)` with `source = path` (resolved absolute path)
- [ ] 6.2 Add tests: two relative paths to same file produce one source; two files with same basename in different dirs produce two sources

## Task 7: Fetch cache key includes URL
- **Status:** pending
- **Depends on:** Task 1 (SSRF changes modify tool_fetch.go)
- **Docs:** [design.md#5-fetch-cache-key-includes-url](./design.md#5-fetch-cache-key-includes-url), [implementation.md#task-7-fetch-cache-key-includes-url](./implementation.md#task-7-fetch-cache-key-includes-url)

### Subtasks
- [ ] 7.1 Add `composeFetchCacheKey(label, url string) string` to `internal/server/tool_fetch.go` — returns `label + "|" + url`
- [ ] 7.2 Use `composeFetchCacheKey` for cache lookup (`GetSourceMeta`) and as storage label when indexing fetched content
- [ ] 7.3 Add test: two URLs with same explicit `source` label get separate cache entries

## Task 8: Batch concurrency
- **Status:** pending
- **Depends on:** —
- **Docs:** [design.md#7-batch-concurrency](./design.md#7-batch-concurrency), [implementation.md#task-8-batch-concurrency](./implementation.md#task-8-batch-concurrency)

### Subtasks
- [ ] 8.1 Add `golang.org/x/sync` dependency if not already in `go.mod`
- [ ] 8.2 Parse `concurrency` parameter in `handleBatchExecute` — `int(req.GetFloat("concurrency", 1))`, clamp to [1, min(8, len(commands))]
- [ ] 8.3 Extract existing serial loop into `executeBatchSerial(ctx context.Context, commands []CommandInput, timeout int, exec *executor.PolyglotExecutor) []string`
- [ ] 8.4 Add `executeBatchParallel(ctx context.Context, commands []CommandInput, timeout, concurrency int, exec *executor.PolyglotExecutor) []string` — `errgroup.Group` with `SetLimit`, pre-allocated results slice, per-command timeout, per-command error handling
- [ ] 8.5 Route: `concurrency <= 1` → serial, else → parallel. Rest of handler unchanged
- [ ] 8.6 Add `concurrency` to `capy_batch_execute` MCP tool schema in `internal/server/server.go` — optional integer, min 1, max 8, with description guiding LLM usage
- [ ] 8.7 Add tests: serial at concurrency=1 (identical behavior), parallel speedup with sleep commands, output ordering preserved, per-command error isolation

## Task 9: Extend cleanup with project-scope purge
- **Status:** pending
- **Depends on:** —
- **Docs:** [design.md#8-extend-cleanup-with-project-scope-purge](./design.md#8-extend-cleanup-with-project-scope-purge), [implementation.md#task-9-extend-cleanup-with-purge_all](./implementation.md#task-9-extend-cleanup-with-purge_all)

### Subtasks
- [ ] 9.1 Add `PurgeAll(dryRun bool) (int, error)` to `internal/store/cleanup.go` — if dryRun return source count, else DELETE FROM sources/chunks/chunks_trigram/vocabulary then VACUUM
- [ ] 9.2 Add `purge_all` boolean parameter to `handleCleanup` in `internal/server/tool_cleanup.go` — mutual exclusion with source/purge_ephemeral/purge_session, call `st.PurgeAll`, call `s.stats.Reset()` if not dryRun
- [ ] 9.3 Add `purge_all` to `capy_cleanup` MCP tool schema in `internal/server/server.go`
- [ ] 9.4 Add tests: dry run reports counts, actual purge empties all tables, mutual exclusion enforced

## Task 10: Preserve shell executor PATH
- **Status:** pending
- **Depends on:** —
- **Docs:** [design.md#9-preserve-shell-executor-path](./design.md#9-preserve-shell-executor-path), [implementation.md#task-10-preserve-shell-executor-path](./implementation.md#task-10-preserve-shell-executor-path)

### Subtasks
- [ ] 10.1 Add `quotePosixSingle(value string) string` to `internal/executor/executor.go` — single-quote with `'` → `'\''` escaping
- [ ] 10.2 Add `buildShellScript(code string) string` — reads `os.Getenv("PATH")`, if non-empty prepends `export PATH=<quoted>\n`
- [ ] 10.3 Use `buildShellScript(code)` in `Execute` when `req.Language == Shell`, before writing to script file
- [ ] 10.4 Add test: shell script with PATH override in code still has original PATH available at the top

## Task 11: Final verification
- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3, Task 4, Task 5, Task 6, Task 7, Task 8, Task 9, Task 10

### Subtasks
- [ ] 11.1 Run `test` skill to verify all tasks — full test suite with `go test ./...`
- [ ] 11.2 Run `document` skill to update any relevant docs (MCP schema changes, ADRs if needed)
- [ ] 11.3 Run `review-code` skill with Go language input to review the implementation
- [ ] 11.4 Run `review-spec` skill to verify implementation matches design and implementation docs
