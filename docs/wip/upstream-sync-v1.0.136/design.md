# Design: Upstream Sync — context-mode v1.0.89→v1.0.136

> **Scope:** Port search quality, security hardening, server/tool, and executor improvements from the context-mode TypeScript reference (commits 2de4b58..f8d4639, versions v1.0.89→v1.0.136) to the capy Go implementation.
>
> **Reference:** `context-mode/src/store.ts`, `context-mode/src/db-base.ts`, `context-mode/src/server.ts`, `context-mode/src/executor.ts`, `context-mode/src/security.ts`
>
> **Previous sync:** `/docs/done/upstream-sync-v1.0.89/`

## 1. Overview

The context-mode TS reference accumulated ~703 commits since the v1.0.89 sync point. After filtering out CI, bundle, platform-specific (Windows, Bun, KiloCode, Kiro, Zed, Pi, OpenClaw, OpenCode, Codex, Cursor, VS Code, Copilot, headless), docs-only, test-infra, stats/analytics/insight, adapter, lifecycle, and version-bump changes, **12 changes** are relevant to capy's core. An additional **10+ categories** are deliberately skipped with documented rationale.

### Ported changes

| Priority | Area | Change | TS Commit(s) |
|----------|------|--------|--------------|
| P0 | Search | Phrase frequency boost in proximity reranker | #349 (`2c63add`) |
| P0 | Search | Hash-based stale detection with auto-refresh on search | #317 (`472634a`) |
| P0 | Security | SSRF guard: scheme validation + DNS rebinding defense | #476, #401 (`ef9eeaa`, `65aa685`, `6017d85`, `8ef04c6`) |
| P0 | Security | Apply Read deny-policy to `capy_index(path)` before file read | #442, #451 (`82690b8`) |
| P0 | Security | Path traversal bypass in file deny evaluation | (`02f71f8`) |
| P1 | Security | TOCTOU fix: re-check deny policy on stale auto-refresh | #442 (`8b0f2d4`) |
| P1 | Security | Executor env deny list: .NET/C# profiler hijack vectors | (`e0e79b7`) |
| P1 | Server | Canonicalize index source label to resolved absolute path | (`e7a3eda`) |
| P1 | Server | Fetch cache key includes URL to prevent cross-URL collision | (`1f1243e`) |
| P1 | Server | Batch concurrency: opt-in `concurrency: 1-8` for I/O-bound batches | #349, (`1d991a2`, `b392c2f`) |
| P2 | Server | Extend cleanup with project-scope purge (`purge_all`) | #520 (`823fd36`, `d11c583`) |
| P2 | Executor | Preserve shell executor PATH after startup | #459 (`702dc75`) |

### Not ported (with rationale)

| Area | Change | Why skipped |
|------|--------|-------------|
| Store | Empty content fallback (#350) | Already correct in Go. `tool_index.go:19` checks `content == ""` which catches both null and empty string — Go's zero-value semantics handle the case that tripped TS's `content ?? readFileSync(path)` |
| Server | Batch env prefix by shell (#407) | TS-specific. Injects `NODE_OPTIONS="--require ${CM_FS_PRELOAD}"` for FS read tracking in spawned Node processes. Capy doesn't have this FS tracking subsystem |
| Robustness | EXCLUSIVE locking + lockfile series (5 commits, #560) | TS-specific multi-instance WAL contention. Go uses `_busy_timeout=5000` at the C driver level. Same rationale as v1.0.89 sync's `withRetry` skip |
| Session | Unified persistent memory & timeline search (#367) | Major new TS subsystem (3700+ lines across 39 files). Not a sync item — would be its own feature if needed |
| Session | Stats/analytics/insight (~dozens of commits) | Capy has its own stats implementation. TS analytics engine rewrite, session tracking, statusline, narrative renderer are all TS-subsystem-specific |
| Runtime | C# runtime addition (#546) | New language runtime. Capy can add runtimes independently |
| Store | Prose-style retirement (#482) | Go never had prose-style enforcement |
| Store | Case-fold migration (#520, `6d00a00`, `a32cc29`) | TS-specific project-dir hash resolution for Mac/Win case-insensitive filesystems. Go uses different path resolution |
| Server | Suppress startup banner (#522) | TS MCP SDK-specific stdout management |
| DB | Node:sqlite gate (#461, #551) | TS-specific adapter for Node's built-in SQLite vs better-sqlite3 |
| Platform | All platform-specific changes | Windows, Bun, adapters (Pi, Codex, OpenCode, Cursor, VS Code, Copilot, headless, etc.) — not applicable to Go's single-binary architecture |
| CI/Docs | All CI, docs, test-infra, bundle changes | Infrastructure-only, no runtime behavior |

### Capy-specific features not in upstream (preserved)

These capy features have no upstream equivalent and must be preserved during the sync:

- **Synonym expansion** in search queries (`synonyms.go`) — query terms expanded into synonym groups before FTS5
- **Entity-aware boosting** (`entity.go`) — quoted phrases and capitalized identifiers boost matching results
- **Per-source diversification** (`search.go`) — caps results from any single source to avoid dominance
- **Source-kind separation** (ADR-017) — durable vs ephemeral vs session sources with separate lifecycle
- **Secret stripping** before indexing (`sanitize` package)
- **Configurable fetch TTL** via `.capy.toml`

### Deliberate divergences from upstream (unchanged)

| Area | Capy behavior | TS behavior | Rationale |
|------|--------------|-------------|-----------|
| RRF layers | 2 (porter OR + trigram OR) | 4 (porter+trigram × AND+OR) | OR is superset; RRF handles precision (ADR-010) |
| Proximity formula | Normalized by content length | Magic constant `/100` | More principled, adapts to chunk size (ADR-014) |
| BM25 title weight | Configurable, default 2.0 | Hardcoded 5.0 | Auto-generated titles hurt by 5x boost (ADR-009) |
| Cleanup policy | Conservative (never-accessed + cold + age) | Aggressive (age-only stale deletion) | Persistent DB needs conservative pruning (ADR-011) |
| SSRF default | Block loopback + private + link-local | Allow loopback + private | Capy is stricter by default — no `CTX_FETCH_STRICT` toggle |
| Fetch TTL | Configurable via `.capy.toml` | Hardcoded 24h | Config system makes this trivial (ADR-013) |

---

## 2. Phrase Frequency Reranker

### 2.1 Problem

The existing `rerank` function in `search.go` computes title-match boost and proximity boost (minSpan). The minSpan proximity formula `1/(1 + minSpan/contentLen)` rewards longer documents — a long document with one tight query-term occurrence outranks a short document with multiple tight occurrences at the same span.

### 2.2 Solution

Add a `countAdjacentPairs(positionLists [][]int, terms []string, gap int) int` helper that counts ordered adjacent-pair occurrences of consecutive query terms within a gap window (30 chars).

**Algorithm:** For each consecutive pair `(terms[i], terms[i+1])`, sweep left positions against right positions. For each left position `p`, find the nearest right position within `[p + len(terms[i]), p + len(terms[i]) + gap]`. Each right position is consumed at most once (greedy left-to-right matching), so `"foo foo bar"` counts 1 pair, not 2 — matching IR phrase-occurrence intent.

**Boost formula:** `phraseBoost = 0.5 * min(1.0, float64(adjacentPairs) / 4.0)` — saturates at 4 hits to prevent keyword-stuffed documents from dominating. Cap of 0.5 sits below max proximity (~1.0) and in the title-boost range (0.3-0.6).

**Integration point:** Inside `rerank()` in the multi-term proximity block (after computing `minSpan`), compute `adjacentPairs` using the same `posLists` already built for span calculation. `phraseBoost` is added to `proximityBoost` and applied as a single multiplicative factor: `r.FusedScore *= (1.0 + proximityBoost + phraseBoost)`. Title boost remains a separate multiplicative pass (`r.FusedScore *= (1.0 + titleBoost)`), matching capy's existing two-pass approach. This differs from the TS reference which combines all three additively — capy's multiplicative separation is a deliberate divergence preserved from ADR-014. No interaction with capy's synonym expansion, entity boosting, or diversification.

### 2.3 Files touched

- `internal/store/search.go`: add `countAdjacentPairs` helper, integrate `phraseBoost` into `rerank`
- `internal/store/search_test.go`: tests for `countAdjacentPairs` isolation + integration test showing short-multi-hit doc outranking long-single-hit doc

---

## 3. Hash-Based Stale Detection with Auto-Refresh on Search

### 3.1 Problem

When files are indexed via `capy_index(path)`, the indexed content becomes stale if the file changes on disk. Currently there's no mechanism to detect this — search returns outdated results until the user manually re-indexes.

### 3.2 Solution

Store the file's absolute path in the sources table. On every search, check file-backed sources for staleness using a mtime gate + SHA-256 comparison, and auto re-index changed files before returning results.

### 3.3 Schema change

Add a nullable `file_path TEXT` column to the `sources` table. The existing `content_hash TEXT` column already stores SHA-256 hashes and is reused for stale detection.

**Migration:** Add to `migrate.go` using the existing migration pattern:

```
ALTER TABLE sources ADD COLUMN file_path TEXT
```

O(1) in SQLite. No data migration needed — existing sources get `NULL` for `file_path`, which means "not file-backed, skip stale check."

### 3.4 Index path changes

**`tool_index.go`:** When a file is read from disk (the `path != "" && content == ""` branch), pass the resolved absolute path to a new store method. The store records `file_path = resolvedAbsPath` in the source row.

**`index.go`:** Add `IndexWithFilePath(content, label, contentType string, kind SourceKind, filePath string) (*IndexResult, error)`. Both `Index` and `IndexWithFilePath` funnel through `indexPreparedChunks` — add a `filePath string` parameter to its signature. `Index` passes `""`, `IndexWithFilePath` passes the actual path. `indexPreparedChunks` converts to `sql.NullString{String: filePath, Valid: filePath != ""}` before the `stmtInsertSource.Exec` call. Modify `stmtInsertSource` (currently 6 columns in `store.go:175-177`) to include `file_path` as the 7th column.

### 3.5 Search path changes

**`search.go`:** At the top of `SearchWithFallback`, before the RRF pass, call `s.refreshStaleSources()`:

1. Query `SELECT label, file_path, content_hash, indexed_at, content_type, kind FROM sources WHERE file_path IS NOT NULL`
2. For each source:
   - **Deny-policy check:** call `s.denyChecker(filePath)` before any I/O — skip if denied (see §6b TOCTOU fix)
   - **mtime gate (fast path):** `os.Stat(filePath).ModTime()` — skip if mtime ≤ `indexedAt`
   - **SHA-256 comparison:** `os.ReadFile(filePath)`, hash — skip if hash matches `content_hash`
   - **Re-index:** call `s.IndexWithFilePath(newContent, label, contentType, kind, filePath)` — preserving the existing `content_type`, `kind`, and `file_path` so the source remains file-backed and retains its lifecycle semantics after refresh. Must NOT use generic `s.Index()` which writes `file_path = NULL`, breaking future stale detection
3. Graceful handling:
   - Deleted files: `os.Stat` returns error → skip, keep cached results
   - Read errors: log warning, skip (never break search for stale detection)

**Cooldown:** Add a `lastRefreshTime time.Time` field on `ContentStore`. Skip `refreshStaleSources` entirely if called within the last 5 seconds. The TS reference has no throttling, but capy runs as a separate MCP server where each search is an IPC round-trip — the per-file `stat` syscalls add non-trivial latency when many file-backed sources are indexed. A 5-second cooldown is short enough that genuinely stale files are caught within a few searches, but avoids redundant stat storms on rapid query bursts (e.g., batch searches). This is a capy-specific addition matching the conservative approach from ADR-011.

**Observability:** `LastRefreshCount int` field on `ContentStore` — tracks how many sources were refreshed in the last `SearchWithFallback` call.

### 3.6 Interaction with existing features

- Stale detection runs before RRF, so synonym expansion, entity boosting, and diversification operate on fresh data
- `file_path` is orthogonal to `kind` — both durable and ephemeral file-backed sources get stale detection
- The existing `content_hash` dedup in `indexPreparedChunks` (index.go:114) handles the case where a file is "touched" but content hasn't changed — the SHA-256 comparison in `refreshStaleSources` catches this before even calling `Index`

### 3.7 Files touched

- `internal/store/schema.go`: add `file_path` to `CREATE TABLE` definition
- `internal/store/migrate.go`: add migration for `file_path` column
- `internal/store/index.go`: add `IndexWithFilePath`, modify `stmtInsertSource` to accept `file_path`
- `internal/store/search.go`: add `refreshStaleSources`, `denyChecker` field, call from `SearchWithFallback`
- `internal/store/detect.go`: add `DenyChecker` type and setter on `ContentStore`
- `internal/server/tool_index.go`: pass resolved path to `IndexWithFilePath`
- `internal/server/server.go`: wire deny checker into store at init

---

## 4. Canonicalize Index Source Label

### 4.1 Problem

`tool_index.go:52-53` uses `filepath.Base(path)` as the default label when no explicit `source` is provided. This drops the directory entirely, causing two issues:
1. Two different files (`docs/api.md` and `notes/api.md`) both get label `api.md` and collide
2. Same file referenced as `./foo.md`, `foo.md`, or `subdir/../foo.md` all resolve to `foo.md` — which happens to be the same, but only by accident

### 4.2 Solution

Change the default label from `filepath.Base(path)` to the resolved absolute `path`. By line 33, `path` is already resolved to an absolute, clean path. Change line 52-53 to:

```go
if source == "" {
    source = path  // resolved absolute path
}
```

Explicit `source` parameter still takes priority.

### 4.3 Files touched

- `internal/server/tool_index.go`: change default label assignment (one-line fix)
- `internal/server/tool_knowledge_test.go`: test that two different relative paths to the same file produce one source, and two different files with the same basename produce two sources

---

## 5. Fetch Cache Key Includes URL

### 5.1 Problem

`tool_fetch.go:76` does `meta, err := st.GetSourceMeta(label)` for the TTL cache check. But `label` can be a user-chosen name that maps to different URLs. Two distinct URLs with the same label silently return the first URL's cached response.

### 5.2 Solution

Compose a key from `label + "|" + url` and use it as **both the cache lookup key and the storage label**. This matches the upstream approach: `composeFetchCacheKey` is used for `GetSourceMeta` (cache check) and as the source label when indexing, so the cache lookup on subsequent calls hits the correct entry.

Add a `composeFetchCacheKey(label, url string) string` helper. The pipe separator is safe because URLs don't contain raw `|` (it's percent-encoded) and labels are either user-chosen or default to the URL itself.

`capy_search(source: "my-label")` still finds these sources because search uses `LIKE '%' || ? || '%'` matching — the user-chosen label substring is contained in the composite key. Document this in the tool's response message.

**Design constraint:** The composite storage label relies on `execDynamicSearch`'s LIKE-based source filter (`s.label LIKE '%' || ? || '%'`). If source matching is ever changed to exact-match by default, composite-keyed sources would become unsearchable by short label. This coupling is acceptable — the LIKE filter is capy's established behavior and matches the TS reference.

**One-time orphan effect:** Existing sources indexed under the old label-only key (`"my-docs"`) won't match a composite lookup (`"my-docs|https://..."`) — they'll be re-fetched once on the next call. This is benign: the old source remains searchable, the new one supersedes it for caching, and the old one eventually gets cleaned up by normal retention/TTL. No migration needed.

### 5.3 Files touched

- `internal/server/tool_fetch.go`: add `composeFetchCacheKey`, use for both cache lookup and storage label
- `internal/server/tool_fetch_test.go`: test that two URLs with same explicit source get separate cache entries; test that `source:` partial-match search still finds composite-keyed sources

---

## 6. Security Hardening

### 6a. SSRF Guard: Scheme Validation + DNS Rebinding Defense

#### 6a.1 Problem

The current `validateFetchURL` (tool_fetch.go:224-246) resolves DNS and checks IPs, but:
1. Missing scheme validation — `file://`, `gopher://`, `javascript:`, `data:` are not blocked
2. DNS rebinding window — `validateFetchURL` resolves DNS once, but `http.Client` does its own resolution for the actual TCP connection. An attacker with a low-TTL DNS record can serve a public IP for the pre-flight check and a private/IMDS IP for the connect.

#### 6a.2 Solution

**New file `internal/server/ssrf.go`:**

`classifyIP(rawIP string) error` — accepts a raw IP string (not `net.IP`) to handle zone-id stripping and IPv4-mapped IPv6 before classification. Returns an error if the IP is forbidden. Classification categories, matching the upstream `classifyIp`:

1. **Zone-ID stripping:** Strip RFC 6874 zone identifiers (`fe80::1%eth0`, URL-encoded `%25eth0`) before any classification. Without this, `::1%eth0` fails to match loopback and falls through to "allowed"
2. **IPv4-mapped IPv6:** Detect `::ffff:A.B.C.D` and recurse through the IPv4 classifier
3. **IPv6 hard-block:** unspecified (`::`) , link-local (`fe80::/10`), multicast (`ff00::/8`)
4. **IPv6 private:** loopback (`::1`), ULA (`fc00::/7`)
5. **IPv4 hard-block:** `0.0.0.0/8` (current network), `169.254.0.0/16` (link-local incl. IMDS), `224.0.0.0+` (multicast/reserved)
6. **IPv4 private:** loopback (`127.0.0.0/8`), RFC1918 (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`)
7. **Malformed/non-IP strings:** block (fail-closed)

Since capy blocks both "hard-block" and "private" categories (stricter than upstream's default), the function returns a single error for any non-public IP.

`validateFetchScheme(rawURL string) error` — parses URL, rejects any scheme not in `{"http", "https"}`. Explicitly blocks `file`, `gopher`, `javascript`, `data`, and empty scheme.

`newSSRFSafeTransport() *http.Transport` — returns a Transport with a custom `DialContext` that:
1. Resolves DNS via `net.Resolver.LookupIPAddr`
2. Classifies every returned IP via `classifyIP` — aborts before TCP if any IP is blocked
3. Dials only after IP validation passes

**`tool_fetch.go` changes:**
1. Replace the existing `validateFetchURL` call with `validateFetchScheme` (scheme-only check, cheap)
2. Replace the default `http.Client` with one using `newSSRFSafeTransport()` (IP classification at connect time)
3. Remove the existing `validateFetchURL` function (replaced by the two new checks)

This collapses the TOCTOU window to zero — DNS resolution and IP classification happen at connect time, not in a separate pre-flight step.

#### 6a.3 Files touched

- `internal/server/ssrf.go` (new): `classifyIP`, `validateFetchScheme`, `newSSRFSafeTransport`
- `internal/server/ssrf_test.go` (new): unit tests for scheme validation, IP classification covering: `0.0.0.0/8`, `::`, `::ffff:127.0.0.1`, `fe80::1%eth0` (zone-id), `224.0.0.1` (multicast), `169.254.169.254` (IMDS), `127.0.0.1`, `10.0.0.1`, `192.168.1.1`, malformed strings, valid public IPs
- `internal/server/tool_fetch.go`: use new SSRF functions, remove old `validateFetchURL`
- `internal/server/tool_fetch_test.go`: update tests for new SSRF guard

### 6b. TOCTOU Fix for Stale Auto-Refresh

#### 6b.1 Problem

When `refreshStaleSources()` (§3) auto-refreshes a file, the file's deny status may have changed since original indexing. Reading the file without re-checking the deny policy creates a TOCTOU vulnerability.

#### 6b.2 Solution

Add a `DenyChecker func(path string) bool` field on `ContentStore`, set by the server at initialization. In `refreshStaleSources`, before `os.ReadFile(filePath)`, call `s.denyChecker(filePath)` — skip the source if denied.

**Server wiring (`server.go`):** After creating the store, set the deny checker:

```go
store.SetDenyChecker(func(filePath string) bool {
    denied, _ := security.EvaluateFilePath(filePath, s.readDenyGlobs)
    return denied
})
```

Fail-closed: if the checker errors or returns true, skip the re-read rather than proceeding.

#### 6b.3 Files touched

- `internal/store/detect.go` (or inline in search.go): `DenyChecker` type, `SetDenyChecker` method
- `internal/store/search.go`: use `s.denyChecker` in `refreshStaleSources`
- `internal/server/server.go`: wire deny checker at store creation

### 6c. Path Traversal Bypass in File Deny Evaluation

#### 6c.1 Problem

`EvaluateFilePath` in `security/eval.go` matches deny globs only against the raw input string. A relative path like `../../.ssh/id_rsa` from a nested project directory bypasses an absolute-path deny glob like `/home/user/.ssh/**`.

#### 6c.2 Solution

Add an optional `projectRoot string` parameter to `EvaluateFilePath`. When non-empty:
1. Resolve the input path to absolute: `filepath.Join(projectRoot, filePath)` then `filepath.Clean`
2. Match deny globs against both the raw input AND the resolved absolute path
3. Either match triggers denial

Update `checkFilePathDenyPolicy` in `security_check.go` to pass `s.projectDir`. Callers that don't pass `projectRoot` (empty string) see identical behavior — backward compatible.

#### 6c.3 Files touched

- `internal/security/eval.go`: add `projectRoot` parameter to `EvaluateFilePath`
- `internal/security/eval_test.go`: test relative-path bypass is caught when projectRoot provided
- `internal/server/security_check.go`: pass `s.projectDir` to `EvaluateFilePath`
- `internal/hook/pretooluse.go:157`: update `EvaluateFilePath` call to pass `projectDir` (already available in the hook context)
- `internal/server/server.go`: update deny checker closure (§6b) to pass `s.projectDir` to `EvaluateFilePath`

### 6d. Executor Env Deny List: .NET/C# Profiler Hijack Vectors

#### 6d.1 Problem

The executor's `deniedEnvVars` in `env.go` covers shell injection, Node, Python, Ruby, Perl, Elixir, Go, Rust, PHP, R, shared libraries, OpenSSL, compilers, and Git — but not .NET/C# runtime hijack vectors.

#### 6d.2 Solution

Add to `deniedEnvVars`:

```
// .NET / C# — profiler attach (loads arbitrary DLL into dotnet host)
"CORECLR_PROFILER", "CORECLR_PROFILER_PATH",
"CORECLR_PROFILER_PATH_32", "CORECLR_PROFILER_PATH_64",
"CORECLR_PROFILER_PATH_ARM32", "CORECLR_PROFILER_PATH_ARM64",
"CORECLR_ENABLE_PROFILING",
"DOTNET_PROFILER_PATH", "DOTNET_PROFILER_PATH_32",
"DOTNET_PROFILER_PATH_64", "DOTNET_PROFILER_PATH_ARM32",
"DOTNET_PROFILER_PATH_ARM64",
// .NET diagnostic + extraction hijack
"DOTNET_DiagnosticPorts", "DOTNET_BUNDLE_EXTRACT_BASE_DIR",
```

For `COMPlus_*` back-compat prefix: add a prefix check in `BuildSafeEnv` alongside the existing `BASH_FUNC_` prefix check:

```go
if deniedEnvVars[key] || strings.HasPrefix(key, "BASH_FUNC_") || strings.HasPrefix(key, "COMPlus_") {
    continue
}
```

#### 6d.3 Files touched

- `internal/executor/env.go`: add .NET vars to `deniedEnvVars`, add `COMPlus_` prefix check
- `internal/executor/env_test.go`: test that CORECLR_PROFILER and COMPlus_* are stripped

### 6e. Apply Read Deny-Policy to `capy_index(path)`

#### 6e.1 Problem

`handleIndex` in `tool_index.go` accepts an arbitrary `path` and reads it with `os.Stat`/`os.ReadFile` without calling `checkFilePathDenyPolicy`. The parallel tool `handleExecuteFile` already guards file access via the same deny policy. This creates a search-index exfiltration path: any file readable by the MCP server process can be indexed into FTS5 and later surfaced by `capy_search`, bypassing the documented Read deny-pattern mitigation (e.g., `.ssh/id_rsa`, `.env`, `~/.aws/credentials`).

#### 6e.2 Solution

Add a `checkFilePathDenyPolicy` call in `handleIndex` before any file I/O, matching the pattern used by `handleExecuteFile`. The check runs when `path != ""` (file-backed indexing), and no-ops when only `content` is provided (inline content branch unaffected).

Placement: immediately after extracting the `path` parameter, before the `filepath.IsAbs` / path resolution logic. This ensures that both the raw user-supplied path AND (after §6c fix) its resolved absolute form are checked against deny globs.

#### 6e.3 Files touched

- `internal/server/tool_index.go`: add `checkFilePathDenyPolicy` call before file read
- `internal/server/tool_knowledge_test.go`: test that denied paths return error and never index into FTS5; inline content with a source label matching a deny pattern still works

---

## 7. Batch Concurrency

### 7.1 Problem

`capy_batch_execute` runs commands sequentially with a shared timeout budget. For I/O-bound batches (network requests, git operations), sequential execution wastes time on pure wait.

### 7.2 Solution

Add an optional `concurrency` parameter (int, 1-8, default 1) to the MCP tool schema. The new dependency `golang.org/x/sync/errgroup` provides a concurrency-limited goroutine pool.

**When `concurrency == 1` (default):** Existing serial path is preserved unchanged — shared timeout budget, cascading skip on timeout. No behavioral change.

**When `concurrency > 1`:** Switch to a parallel execution path:

1. Cap `concurrency` at `min(concurrency, len(commands), 8)` — never more goroutines than commands
2. Pre-allocate `results := make([]string, len(commands))` — index-keyed for order preservation
3. Create `errgroup.Group` with `g.SetLimit(concurrency)`
4. Each command gets the **full** `timeout` value (matching upstream). This is the correct semantic for parallel I/O: commands run concurrently, so the wall-clock time is bounded by `timeout` not `timeout * N`. A timed-out command records `(timed out)` in its result slot without affecting siblings
5. Each goroutine writes to `results[i]` — no shared state, no mutex needed
6. `g.Wait()` blocks until all complete. Errors in one command don't abort others (each goroutine handles its own errors and writes error text to its result slot)
7. After all commands complete, join `results` in order and proceed to indexing (serial)

**FTS5 indexing remains serial** — after all commands complete. SQLite write contention is avoided entirely since no concurrent writes happen.

**MCP schema addition:**
```json
"concurrency": {
  "type": "integer",
  "description": "Parallel workers (1-8). Default 1 = serial. Use 4-8 for I/O-bound batches (network, git). Use 1 for CPU-bound (build, test) or stateful (ports, locks).",
  "minimum": 1,
  "maximum": 8
}
```

### 7.3 Files touched

- `internal/server/tool_batch.go`: add `concurrency` parameter parsing, parallel execution path
- `internal/server/tool_batch_test.go`: test serial preserved at concurrency=1, parallel speedup at concurrency>1, output ordering, per-command error isolation
- `go.mod`: add `golang.org/x/sync` dependency (if not already present)

---

## 8. Extend Cleanup with Project-Scope Purge

### 8.1 Problem

`capy_cleanup` supports source-specific eviction, ephemeral purge, and session purge — but not a full "nuke everything" project-scope purge. Users who want to start fresh must manually delete the database file.

### 8.2 Solution

Add a `purge_all: true` boolean parameter to the cleanup tool. When set:

1. Delete ALL rows from `sources`, `chunks`, `chunks_trigram`, and `vocabulary`. Note: vocabulary is normally preserved across cleanups (it benefits fuzzy correction — see `cleanup.go:134`), but `purge_all` is a full knowledge-base reset. Including vocabulary ensures the store is truly empty and the fuzzy cache is invalidated cleanly
2. Reset stats via `s.stats.Reset()`
3. Run `VACUUM` to reclaim disk space

The existing `dry_run` parameter applies: when true, report total source/chunk counts that would be purged without acting. Mutually exclusive with `source`, `purge_ephemeral`, and `purge_session`.

Add a `PurgeAll(dryRun bool) (int, error)` method to `ContentStore` that returns the number of sources purged.

### 8.3 Files touched

- `internal/store/cleanup.go`: add `PurgeAll` method
- `internal/server/tool_cleanup.go`: add `purge_all` parameter, call `PurgeAll`
- `internal/server/server.go`: add `purge_all` to MCP tool schema

---

## 9. Preserve Shell Executor PATH

### 9.1 Problem

The executor writes shell scripts to temp files (executor.go:85). User shell startup files (`.bashrc`, `.zshrc`) can overwrite `PATH`, hiding tools that the MCP process had available. `BuildSafeEnv` sets PATH in the subprocess environment, but the sourced profile clobbers it.

### 9.2 Solution

Add a `buildShellScript(code, inheritedPath string) string` pure helper — takes the PATH value as an explicit parameter (matching the TS reference's `buildShellScriptContent(code, inheritedPath, platform)` pattern) so it can be unit-tested without mutating the process environment. The caller passes `os.Getenv("PATH")`. When `req.Language == Shell`, prepend a PATH restoration line before the user's code:

```go
func buildShellScript(code, inheritedPath string) string {
    if inheritedPath == "" {
        return code
    }
    return fmt.Sprintf("export PATH=%s\n%s", quotePosixSingle(inheritedPath), code)
}
```

`quotePosixSingle` wraps the value in single quotes with `'` → `'\''` escaping — same approach as the TS reference.

Modify the shell branch in `Execute` (line 83-86) to write `buildShellScript(code)` instead of raw `code`.

### 9.3 Files touched

- `internal/executor/executor.go`: add `buildShellScript`, `quotePosixSingle`, use in shell script writing
- `internal/executor/executor_test.go`: test that PATH is restored even when shell startup overrides it
