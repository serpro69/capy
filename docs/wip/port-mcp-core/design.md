# Design: Port MCP Core to Go

## 1. Overview

**capy** is a Go port of the [context-mode](../../context-mode/) TypeScript MCP server and Claude Code plugin. It reduces LLM context window consumption by ~98% by intercepting data-heavy tool calls (Bash, Read, WebFetch, Grep), executing them in sandboxed subprocesses, and indexing raw output into a persistent, per-project SQLite FTS5 knowledge base. Only concise summaries and BM25-ranked search results enter the context window.

### Key differences from context-mode

| Aspect | context-mode (TypeScript) | capy (Go) |
|--------|--------------------------|-----------|
| Knowledge base lifecycle | Ephemeral per-session (`/tmp/context-mode-<PID>.db`) | **Persistent per-project** (survives across sessions) |
| Distribution | npm package, Node.js runtime required | Single static binary, zero runtime dependencies |
| Binary model | `node start.mjs` for server, `node hooks/*.mjs` for hooks | Single `capy` binary with subcommands |
| Tool prefix | `ctx_` | `capy_` |
| Configuration | Reads `.claude/settings.json` only | Own config system (`.capy.toml` / `.capy/config.toml` / XDG) plus reads `.claude/settings.json` for security rules |
| Content freshness | None (DB dies with process) | Tiered freshness with metadata (`last_accessed_at`, `access_count`, `content_hash`) |
| DB portability | Not portable | Configurable location; can be committed to VCS for team sharing |
| SQLite driver | `better-sqlite3` (native addon) | `mattn/go-sqlite3` (CGO) |
| MCP SDK | `@modelcontextprotocol/sdk` | `mcp-go` (`github.com/mark3labs/mcp-go`) |

### Scope

**Initial port (this document):**
- ContentStore with persistent per-project FTS5, tiered freshness, portable DB
- PolyglotExecutor with 11 runtimes, smart truncation, auto-indexing
- MCP server with 9 tools (`capy_` prefix) via `mcp-go`
- Security layer reading deny/allow from `.claude/settings.json`
- Claude Code hook integration via `capy hook` subcommands (`pretooluse` fully implemented, others stubbed)
- `capy setup` for auto-configuration
- CLI: `capy serve`, `capy hook <event>`, `capy setup`, `capy doctor`
- Config: `.capy.toml` > `.capy/config.toml` > XDG, TOML format

**Designed-for but deferred:**
- Session continuity (SessionDB, event extraction, snapshots, resume flow) — see context-mode's `src/session/` directory
- Multi-platform adapters (Gemini CLI, VS Code Copilot, OpenCode, Cursor, Codex, OpenClaw, Antigravity, Kiro) — see context-mode's `src/adapters/` directory
- `posttooluse`, `precompact`, `sessionstart`, `userpromptsubmit` hook implementations
- Self-update mechanism (Go binaries use package managers or `go install`)

---

## 2. Project Structure

```
cmd/
  capy/
    main.go              → Entry point, cobra/subcommand dispatch

internal/
  server/                → MCP server, tool handlers, session stats
    server.go            → Server struct, stdio transport, tool registration
    tools.go             → Tool handler implementations (execute, search, etc.)
    stats.go             → Session statistics tracking

  store/                 → ContentStore: FTS5 knowledge base
    store.go             → ContentStore struct, DB lifecycle, lazy init
    schema.go            → SQL schema (sources, chunks, chunks_trigram, vocabulary)
    index.go             → Indexing: markdown, JSON, plaintext chunking
    search.go            → Three-tier search: Porter → trigram → fuzzy
    chunk.go             → Chunking strategies (markdown, JSON, plaintext)
    vocabulary.go        → Vocabulary extraction, fuzzy correction (Levenshtein)
    terms.go             → Distinctive terms (IDF scoring)
    snippet.go           → Smart snippet extraction around match positions
    cleanup.go           → Tiered lifecycle, cold-source pruning

  executor/              → Polyglot code executor
    executor.go          → PolyglotExecutor struct, process spawning
    runtime.go           → Runtime detection, language dispatch, fallback chains
    truncate.go          → Smart output truncation (60% head + 40% tail)
    wrap.go              → Language-specific auto-wrapping (Go, PHP, Elixir)
    env.go               → Environment variable passthrough for sandboxed processes

  security/              → Permission enforcement
    security.go          → Evaluate commands against deny/allow rules
    glob.go              → Glob-to-regex conversion (bash patterns, file paths)
    split.go             → Chained command splitting (&&, ||, ;, |)
    settings.go          → Parse .claude/settings.json rules

  hook/                  → Hook subcommand handlers
    hook.go              → Dispatcher: reads JSON stdin, routes to handler
    pretooluse.go        → PreToolUse: security check, tool routing/redirection
    posttooluse.go       → PostToolUse: stub (future: session event capture)
    precompact.go        → PreCompact: stub (future: resume snapshot)
    sessionstart.go      → SessionStart: stub (future: session restore)
    routing.go           → Routing instruction block (XML for context injection)

  adapter/               → Platform adapter interface (future: multi-platform)
    adapter.go           → HookAdapter interface, PlatformCapabilities
    claudecode.go        → Claude Code adapter: JSON stdin/stdout, session ID extraction

  config/                → Configuration system
    config.go            → Config struct, TOML parsing, precedence resolution
    paths.go             → DB path resolution, project hash, XDG defaults

  session/               → Designed-for but deferred
    (empty — placeholder for SessionDB, event extraction, snapshots)
```

### Package dependency graph

```
cmd/capy/main.go
  ├── internal/server     (MCP server)
  │     ├── internal/store      (ContentStore)
  │     ├── internal/executor   (PolyglotExecutor)
  │     ├── internal/security   (Permission enforcement)
  │     └── internal/config     (Configuration)
  ├── internal/hook       (Hook subcommands)
  │     ├── internal/security
  │     ├── internal/adapter
  │     └── internal/config
  └── internal/config     (Setup subcommand)
```

Key constraint: `internal/store` and `internal/executor` must NOT import each other. The server package orchestrates their interaction (executor produces output → server indexes it into store when auto-indexing triggers).

### Reference: context-mode structure
See `context-mode/CONTRIBUTING.md` lines 17-47 for the TypeScript project layout. The Go structure maps as follows:

| context-mode | capy |
|-------------|------|
| `src/server.ts` | `internal/server/` |
| `src/store.ts` | `internal/store/` |
| `src/executor.ts` | `internal/executor/` |
| `src/security.ts` | `internal/security/` |
| `src/runtime.ts` | `internal/executor/runtime.go` |
| `src/truncate.ts` | `internal/executor/truncate.go` |
| `src/db-base.ts` | Embedded in `internal/store/store.go` (Go doesn't need a base class) |
| `src/types.ts` | Types co-located with their packages |
| `src/cli.ts` | `cmd/capy/main.go` + `internal/hook/` |
| `src/session/` | `internal/session/` (deferred) |
| `src/adapters/` | `internal/adapter/` |
| `hooks/*.mjs` | `internal/hook/` (compiled into the binary) |

---

## 3. ContentStore (Knowledge Base)

The ContentStore is the core differentiator. It's a persistent SQLite database with two FTS5 virtual tables — one using Porter stemming, one using trigram tokenization — enabling three-tier search fallback.

**Reference:** `context-mode/src/store.ts` (full implementation), `context-mode/docs/llms-full.txt` lines 231-378 (schema, chunking, search, fuzzy details).

### 3.1 Database Schema

```sql
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;

-- Sources table (extended from context-mode with freshness metadata)
CREATE TABLE IF NOT EXISTS sources (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  label TEXT NOT NULL,
  chunk_count INTEGER NOT NULL DEFAULT 0,
  code_chunk_count INTEGER NOT NULL DEFAULT 0,
  indexed_at TEXT DEFAULT CURRENT_TIMESTAMP,
  last_accessed_at TEXT DEFAULT CURRENT_TIMESTAMP,  -- NEW: freshness tracking
  access_count INTEGER NOT NULL DEFAULT 0,          -- NEW: usage frequency
  content_hash TEXT                                  -- NEW: change detection
);

-- Porter stemming FTS5 table
CREATE VIRTUAL TABLE IF NOT EXISTS chunks USING fts5(
  title,
  content,
  source_id UNINDEXED,
  content_type UNINDEXED,
  tokenize='porter unicode61'
);

-- Trigram FTS5 table (substring matching)
CREATE VIRTUAL TABLE IF NOT EXISTS chunks_trigram USING fts5(
  title,
  content,
  source_id UNINDEXED,
  content_type UNINDEXED,
  tokenize='trigram'
);

-- Vocabulary table (for fuzzy Levenshtein correction)
CREATE TABLE IF NOT EXISTS vocabulary (
  word TEXT PRIMARY KEY
);
```

The schema is identical to context-mode's except for three new columns on `sources`: `last_accessed_at`, `access_count`, and `content_hash`. These enable tiered freshness management without changing the search algorithm.

### 3.2 Three-Tier Search

```
Layer 1: Porter stemming FTS5 MATCH
  |-- match found → return results with matchLayer: "porter"
  |-- no match → fall through

Layer 2: Trigram substring FTS5 MATCH
  |-- match found → return results with matchLayer: "trigram"
  |-- no match → fall through

Layer 3: Fuzzy Levenshtein correction
  |-- correct each query word against vocabulary
  |-- re-search with corrected query on Porter, then Trigram
  |-- match found → return results with matchLayer: "fuzzy"
  |-- no match → return empty array
```

Each layer supports optional source filtering via `LIKE` match on `sources.label`.

**BM25 ranking** at the SQL level:
```sql
SELECT *, bm25(chunks, 2.0, 1.0) AS rank
FROM chunks WHERE chunks MATCH ?
ORDER BY rank  -- BM25 returns negative scores; more negative = better
```

- `k1 = 2.0` — term frequency saturation (higher = TF matters more)
- `b = 1.0` — full document length normalization (shorter documents boosted)
- Highlight markers: `char(2)` (start) and `char(3)` (end) for match position extraction

**Reference:** `context-mode/src/store.ts` — `searchWithFallback()` function.

### 3.3 Fuzzy Search (Levenshtein)

**Adaptive edit distance thresholds:**

| Word length | Max edit distance |
|-------------|-------------------|
| 1-4 chars | 1 |
| 5-12 chars | 2 |
| 13+ chars | 3 |

Vocabulary is built during indexing: words extracted by splitting on whitespace, filtered to 3+ characters, excluding 88 stopwords (common English + code/changelog terms). Fuzzy correction retrieves candidates from vocabulary where `length(word) BETWEEN wordLength-maxDist AND wordLength+maxDist`, computes Levenshtein distance, returns the closest match within threshold.

**Reference:** `context-mode/src/store.ts` — `levenshteinDistance()`, `maxEditDistance()`, `fuzzyCorrect()` functions. `context-mode/docs/llms-full.txt` lines 346-378.

### 3.4 Chunking Strategies

Three chunking strategies, matching context-mode exactly:

**Markdown chunking** (`chunkMarkdown`):
- Splits on H1-H4 headings (`/^(#{1,4})\s+(.+)$/`)
- Maintains heading stack for breadcrumb titles ("H1 > H2 > H3")
- Preserves code blocks as atomic units (tracks fence state)
- Flushes on new heading or horizontal rule (`/^[-_*]{3,}\s*$/`)
- Max chunk size: 4096 bytes. Oversized chunks split at paragraph boundaries (double newlines) with numbered suffixes ("Title (1)", "Title (2)")

**Plain text chunking** (`chunkPlainText`):
- Phase 1: blank-line splitting (`\n\s*\n`). Used when 3-200 sections with each under 5000 bytes. Title = first line (up to 80 chars) or "Section N".
- Phase 2 (fallback): fixed 20-line groups with 2-line overlap. Titles show line ranges ("Lines 1-20", "Lines 19-38").
- Single chunk if input < 20 lines, titled "Output".

**JSON chunking** (`walkJSON`):
- Recursively walks object tree, key paths as chunk titles ("data > users > 0")
- Small flat objects (< 4096 bytes, no nested objects/arrays): single chunk
- Nested objects: always recurse for searchable key-path titles
- Arrays: batch items by accumulated size up to 4096 bytes. Identity fields checked in order: `id`, `name`, `title`, `slug`, `key`, `label`
- Falls back to `indexPlainText` if JSON parsing fails

**Code block detection:** After chunking, each chunk is scanned for fenced code blocks (`` ```\w*\n[\s\S]*?``` `` pattern). Chunks containing code blocks get `content_type = "code"`, others get `content_type = "prose"`. The `code_chunk_count` in `IndexResult` is the sum of code-containing chunks. The `content_type` UNINDEXED column in FTS5 enables future filtering by content type.

**Reference:** `context-mode/src/store.ts` — `#chunkMarkdown()`, `#chunkPlainText()`, `#walkJSON()` methods.

### 3.5 Smart Snippet Extraction

Search results include smart snippets (up to 1500 bytes) centered on match positions. Match positions are derived from FTS5 highlight markers (`char(2)`/`char(3)` delimiters). Overlapping windows of 300 characters around each match are merged until the 1500-byte limit.

Fallback: if no highlight markers, use `indexOf` on raw query terms.

**Reference:** `context-mode/docs/llms-full.txt` lines 156.

### 3.6 Distinctive Terms (IDF Scoring)

After returning search results, the response includes searchable terms for each source:

```
score = log(totalChunks / count) + lengthBonus + identifierBonus
```

- **IDF**: `log(totalChunks / count)` where `count` = chunks containing the word
- **Length bonus**: rewards longer words (more specific terms)
- **Identifier bonus**: rewards words with underscores or camelCase patterns
- Words must be 3+ characters, not in stopword list
- Default: 40 terms per source

**Reference:** `context-mode/src/store.ts` — `getDistinctiveTerms()` method.

### 3.7 Progressive Search Throttling

Per 60-second window:

| Call count | Behavior |
|------------|----------|
| 1-3 | Normal: max 2 results per query |
| 4-8 | Reduced: 1 result per query, warning emitted |
| 9+ | Blocked: returns error, demands `batch_execute` usage |

Output cap: 40 KB for `search`, 80 KB for `batch_execute`.

**Reference:** `context-mode/docs/llms-full.txt` lines 144-154.

### 3.8 Tiered Freshness (New in capy)

Sources are classified by access recency:

| Tier | Criteria | Behavior |
|------|----------|----------|
| Hot | Accessed within 7 days | Normal BM25 ranking |
| Warm | Accessed within 30 days | Normal BM25 ranking |
| Cold | Not accessed for 30+ days | Candidates for pruning |

- `access_count` incremented on each search hit
- `last_accessed_at` updated on each search hit
- `content_hash` (SHA-256 of raw content) enables re-index detection
- Pruning is **never automatic** — triggered by `capy_cleanup` tool or `capy cleanup` CLI command
- Cold threshold days and auto-prune behavior are configurable via `.capy.toml`

### 3.9 Stale DB Cleanup

Context-mode scans for `context-mode-*.db` files in `/tmp/` and deletes those belonging to dead processes. Capy does not need this — the DB is persistent and intentionally long-lived. Instead, capy implements content-level cleanup via the tiered freshness system.

---

## 4. PolyglotExecutor

The executor spawns isolated child processes for code execution. Its job: run code, capture output, keep raw data out of context.

**Reference:** `context-mode/src/executor.ts` (full implementation), `context-mode/src/runtime.ts` (runtime detection), `context-mode/docs/llms-full.txt` lines 380-460.

### 4.1 Supported Languages and Runtimes

| Language | Primary Runtime | Fallback 1 | Fallback 2 |
|----------|----------------|------------|------------|
| JavaScript | bun | node | — |
| TypeScript | bun | tsx | ts-node |
| Python | python3 | python | — |
| Shell | bash | sh | — |
| Ruby | ruby | — | — |
| Go | go run | — | — |
| Rust | rustc (compile + run) | — | — |
| PHP | php | — | — |
| Perl | perl | — | — |
| R | Rscript | r | — |
| Elixir | elixir | — | — |

Runtime detection uses `exec.LookPath()` for each runtime on first use. Results cached for the server lifetime.

**Note:** context-mode also supports Windows-specific PowerShell fallback for shell. capy can add this later if Windows support is needed. Initial port targets Unix (macOS/Linux).

### 4.2 Process Isolation

- Each execution creates a temp directory, writes the script file
- Process spawned in its own **process group** (`syscall.SysProcAttr{Setpgid: true}`) on Unix
- Cleanup kills the **entire process group** (`syscall.Kill(-pgid, syscall.SIGTERM)`) — this prevents orphaned child processes
- Shell commands run in the **project directory** (respects git working tree)
- Other languages run in the temp directory

### 4.3 Auto-Wrapping

Language-specific wrapping applied before execution:

| Language | Condition | Wrapping |
|----------|-----------|----------|
| Go | Code doesn't contain `package ` | Wraps in `package main` + `import "fmt"` + `func main() { ... }` |
| PHP | Code doesn't start with `<?` | Prepends `<?php\n` |
| Elixir | `mix.exs` exists in project root | Prepends `Path.wildcard` to add `*/ebin` to code path |
| Rust | Always | Compiled with `rustc` to temp binary, then executed (not interpreted) |

**Reference:** `context-mode/docs/llms-full.txt` lines 400-406.

### 4.4 FILE_CONTENT Variable Injection (execute_file)

When using `capy_execute_file`, the file content is loaded into a language-specific variable:

| Language | Variable | Loading mechanism |
|----------|----------|-------------------|
| JavaScript/TypeScript | `FILE_CONTENT` | `require("fs").readFileSync(path, "utf-8")` |
| Python | `FILE_CONTENT` | `open(path, "r", encoding="utf-8").read()` |
| Shell | `FILE_CONTENT` | `$(cat path)` |
| Ruby | `FILE_CONTENT` | `File.read(path, encoding: "utf-8")` |
| Go | `FILE_CONTENT` | `os.ReadFile(path)` converted to string |
| Rust | `file_content` | `fs::read_to_string(path).unwrap()` |
| PHP | `$FILE_CONTENT` | `file_get_contents(path)` |
| Perl | `$FILE_CONTENT` | Filehandle with `<:encoding(UTF-8)` and slurp |
| R | `FILE_CONTENT` | `readLines(path, warn=FALSE, encoding="UTF-8")` joined with newlines |
| Elixir | `file_content` | `File.read!(path)` |

`FILE_CONTENT_PATH` (or language-appropriate equivalent) is also set to the absolute file path.

**Reference:** `context-mode/docs/llms-full.txt` lines 82-97.

### 4.5 Smart Truncation

When output exceeds `maxOutputBytes` (102,400 bytes / 100 KB):

1. Split output into lines
2. Collect head lines until 60% of byte budget consumed
3. Collect tail lines (from end) until 40% of byte budget consumed
4. Insert separator: `"... [N lines / X.XKB truncated -- showing first M + last K lines] ..."`
5. All calculations use byte length for UTF-8 safety, snapping to line boundaries

**Hard cap**: 100 MB (`hardCapBytes`). If combined stdout+stderr exceeds this during streaming, the entire process group is killed immediately. Prevents memory exhaustion from `yes`, `cat /dev/urandom`, etc.

In Go, this is implemented by reading from `io.Reader` with a counting wrapper that triggers `cmd.Process.Kill()` on threshold breach.

**Reference:** `context-mode/src/executor.ts` — `#smartTruncate()`, `context-mode/src/truncate.ts`.

### 4.6 Exit Code Classification

Shell commands that return exit code 1 with stdout present are classified as **soft failures** (e.g., `grep` with no matches returns exit code 1 but isn't a real error). This matters for `capy_batch_execute` — a soft failure should not abort the batch or mark the result as an error.

Classification logic:
- Exit code 0 → success
- Exit code 1 with non-empty stdout → soft failure (return stdout, no error flag)
- Exit code 1 with empty stdout → real error (return stderr)
- Exit code > 1 → real error (return stdout + stderr combined)

**Reference:** `context-mode/src/exit-classify.ts` — `ExitClassification` type.

### 4.7 Intent-Driven Search Flow

When `intent` is provided to `capy_execute` or `capy_execute_file` and output exceeds 5000 bytes:

1. Full output indexed into FTS5 via `store.IndexPlainText()` with source label `execute:<language>` or `file:<path>`
2. `store.SearchWithFallback(intent, 5)` runs three-tier search against indexed content
3. If matches found: returns section count, total output size, matched sections with titles and content snippets
4. If no matches: returns total line count, byte size, all source labels, and distinctive searchable terms
5. Raw output bytes tracked as `bytesIndexed` (kept out of context); only search results enter context

**Reference:** `context-mode/docs/llms-full.txt` lines 785-793.

### 4.8 Network I/O Tracking

Context-mode tracks network bytes consumed inside JS/TS subprocesses via a `__CM_NET__` stderr marker. The code is wrapped in an async IIFE with a `fetch` interceptor that reports response body sizes. This is JS-specific and complex.

For capy's initial port: network I/O tracking is limited to `capy_fetch_and_index` (response body size tracked via `bytesSandboxed`). Tracking network bytes from arbitrary `capy_execute` code that does HTTP is deferred — it would require language-specific instrumentation that isn't worth the complexity for the initial port.

**Reference:** `context-mode/docs/llms-full.txt` lines 63-65.

### 4.9 Environment Passthrough

The following environment variables are forwarded to sandboxed subprocesses:

**Authentication:** `GITHUB_TOKEN`, `GH_TOKEN`, `ANTHROPIC_API_KEY`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`, `AWS_REGION`, `AWS_DEFAULT_REGION`, `AWS_PROFILE`, `GOOGLE_APPLICATION_CREDENTIALS`

**Infrastructure:** `DOCKER_HOST`, `KUBECONFIG`, `NPM_TOKEN`, `NODE_AUTH_TOKEN`, `npm_config_registry`

**Network:** `SSH_AUTH_SOCK`, `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY`, `ALL_PROXY`, `CURL_CA_BUNDLE`, `NODE_EXTRA_CA_CERTS`

**Configuration:** `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_CACHE_HOME`, `XDG_STATE_HOME`, `GOROOT`, `GOPATH`

**Python-specific (always set):** `PYTHONDONTWRITEBYTECODE=1`, `PYTHONUNBUFFERED=1`, `PYTHONUTF8=1`

**Reference:** `context-mode/docs/llms-full.txt` lines 424-453.

---

## 5. MCP Server and Tools

The server is a JSON-RPC stdio process using `mcp-go`. It exposes 9 tools with the `capy_` prefix, lazy-loads the ContentStore on first use, and tracks session statistics.

**Reference:** `context-mode/src/server.ts` (full implementation), `context-mode/docs/llms-full.txt` lines 26-230.

### 5.1 Tools

| Tool | Purpose | Reference |
|------|---------|-----------|
| `capy_execute` | Sandbox code execution (language, code, timeout, intent) | `llms-full.txt` lines 29-66 |
| `capy_execute_file` | Process a file in sandbox (`FILE_CONTENT` injection) | `llms-full.txt` lines 67-100 |
| `capy_batch_execute` | Run multiple commands + search in one call | `llms-full.txt` lines 185-210 |
| `capy_index` | Chunk and index markdown/JSON/plaintext into FTS5 | `llms-full.txt` lines 101-125 |
| `capy_search` | Query knowledge base with three-tier fallback | `llms-full.txt` lines 126-159 |
| `capy_fetch_and_index` | Fetch URL → detect content type → chunk → index | `llms-full.txt` lines 160-184 |
| `capy_stats` | Show context savings, call counts, KB size | `llms-full.txt` lines 211-230 |
| `capy_doctor` | Diagnose runtimes, hooks, FTS5, config | N/A (new) |
| `capy_cleanup` | Prune cold-tier sources by age/access policy | N/A (new) |

**Dropped from context-mode:** `ctx_upgrade` — Go binaries are upgraded via package managers or `go install`, not self-update scripts.

**New tools:** `capy_doctor` (moved from CLI-only to also be an MCP tool), `capy_cleanup` (exposes tiered freshness pruning to the LLM).

### 5.2 Tool Parameters

#### capy_execute
```
language: string (required) — one of 11 supported languages
code: string (required) — source code to execute
timeout: int (optional) — milliseconds, default 30000
intent: string (optional) — semantic filter for large output (triggers auto-index when output > 5KB)
```

#### capy_execute_file
```
path: string (required) — absolute or relative file path
language: string (required) — one of 11 supported languages
code: string (required) — processing code (FILE_CONTENT variable available)
timeout: int (optional) — milliseconds, default 30000
intent: string (optional) — semantic filter
```

#### capy_batch_execute
```
commands: array (required) — [{label: string, command: string}, ...]
queries: array (required) — search queries to run against indexed output
timeout: int (optional) — milliseconds, default 60000
```

Execution flow:
1. All commands run sequentially in a single shell process, each prefixed with `## label`
2. Combined output indexed into FTS5 via markdown chunking
3. Section inventory built (all sections with byte sizes)
4. Each query searched with three-tier fallback, scoped to batch source first, then global
5. Results returned with inventory + search results

#### capy_index
```
content: string (optional) — raw text to index (mutually exclusive with path)
path: string (optional) — file path to read and index (mutually exclusive with content)
source: string (optional) — label for retrieval, defaults to path or "untitled"
```

Returns: `{sourceId, label, totalChunks, codeChunks}`

#### capy_search
```
queries: array (required) — array of search terms
limit: int (optional) — results per query, default 3 (max 2 in normal throttle mode)
source: string (optional) — filter to specific source (partial LIKE match)
```

#### capy_fetch_and_index
```
url: string (required) — URL to fetch
source: string (optional) — label, defaults to URL
```

Content-type routing:
- HTML → Markdown conversion (strip script, style, nav, header, footer) → markdown chunking
- JSON → JSON chunking (key-path titles)
- Plain text → plain text chunking (line groups)

Preview: first 3072 bytes of converted content returned. Rest truncated with `"...[truncated -- use search() for full content]"`.

**Implementation note:** context-mode uses Turndown + domino for HTML→Markdown. In Go, use `github.com/JohannesKaufmann/html-to-markdown` or a similar library. The key requirement is GFM table support and stripping of script/style/nav elements.

#### capy_stats
No parameters. Returns:
- Total bytes returned to context (per-tool breakdown)
- Total call count (per-tool breakdown)
- Bytes indexed (in FTS5, never entered context)
- Bytes sandboxed (network I/O inside subprocesses)
- Session uptime
- Estimated token usage (`totalBytesReturned / 4`)
- Context savings ratio and reduction percentage
- Knowledge base size (total sources, total chunks)

#### capy_doctor
No parameters. Checks:
- Runtime availability for all 11 languages
- FTS5 SQLite extension availability
- Hook registration and paths
- Config file existence and content
- Knowledge base status (path, size, source count)

#### capy_cleanup
```
max_age_days: int (optional) — prune sources not accessed for N days (default from config)
dry_run: bool (optional) — show what would be pruned without deleting (default true)
```

### 5.3 Lazy Initialization

The ContentStore opens its SQLite connection only when a tool that needs it is first called. This keeps `capy_doctor` and `capy_stats` (when no DB exists yet) fast.

### 5.4 Session Statistics

In-memory tracking matching context-mode:

```go
type SessionStats struct {
    SessionStart time.Time
    Calls        map[string]int    // tool name → call count
    BytesReturned map[string]int64 // tool name → bytes returned to context
    BytesIndexed  int64            // bytes stored in FTS5
    BytesSandboxed int64           // network I/O inside sandbox
}
```

Savings calculation:
```
keptOut = bytesIndexed + bytesSandboxed
totalProcessed = keptOut + totalBytesReturned
savingsRatio = totalProcessed / max(totalBytesReturned, 1)
reductionPct = (1 - totalBytesReturned / totalProcessed) * 100
estimatedTokens = totalBytesReturned / 4
```

**Reference:** `context-mode/docs/llms-full.txt` lines 795-819.

---

## 6. Security

Port of context-mode's permission system. Reads deny/allow rules from Claude Code's settings files.

**Reference:** `context-mode/src/security.ts` (full implementation), `context-mode/docs/llms-full.txt` lines 462-553.

### 6.1 Settings Hierarchy

Three-tier, highest priority first:
1. `.claude/settings.local.json` — project-local, not committed
2. `.claude/settings.json` — project-shared, committed
3. `~/.claude/settings.json` — global user settings

Each file may contain `permissions.deny` and `permissions.allow` arrays.

### 6.2 Pattern Formats

**Bash patterns:**
```
Bash(command:argsGlob)   — colon format: "rm:*" matches "rm" with any args
Bash(command argsGlob)   — space format: "sudo *" matches "sudo" with any args
Bash(glob)               — plain glob: "* --force" matches any command with --force
```

Pattern conversion to regex:
- Colon format: command is literal, args use glob-to-regex. Produces `/^command(\s+argsRegex)?$/`
- Space format: split at first space, command literal, rest glob. Produces `/^command\s+argsRegex$/`
- Plain glob: entire pattern converted. `*` → `[^\s]*`, `**` → `.*`

**Tool patterns:**
```
ToolName(glob)   — e.g., Read(.env), Read(**/*.key)
```

Parsed via `/^(\w+)\((.+)\)$/`. Glob evaluated against file paths using globstar matching.

### 6.3 Chained Command Splitting

Shell commands split on chain operators (`&&`, `||`, `;`, `|`) before evaluation. The splitter is **quote-aware**: respects single quotes, double quotes, and backticks. Each segment checked independently.

Example: `echo ok && sudo rm -rf /` → `["echo ok", "sudo rm -rf /"]` → second segment blocked.

### 6.4 Shell-Escape Detection

Non-shell languages scanned for embedded shell commands:

| Language | Patterns |
|----------|----------|
| Python | `os.system(...)`, `subprocess.run/call/Popen/check_output/check_call(...)` |
| JavaScript/TypeScript | `exec/execSync/execFile(...)`, `spawn/spawnSync(...)` |
| Ruby | `system(...)`, backticks |
| Go | `exec.Command(...)` |
| PHP | `shell_exec(...)`, `exec(...)`, `system(...)`, `passthru(...)`, `proc_open(...)` |
| Rust | `Command::new(...)` |

Extracted commands checked against the same Bash deny patterns.

**Reference:** `context-mode/docs/llms-full.txt` lines 505-541.

### 6.5 Deny Wins

If a command matches both deny and allow, **deny takes precedence**. This is a security-first design.

---

## 7. Hook System and Claude Code Integration

Hooks intercept tool calls before they reach the LLM context. Claude Code fires hooks as shell commands, passing JSON on stdin and reading JSON from stdout.

**Reference:** `context-mode/hooks/pretooluse.mjs` (main hook), `context-mode/docs/llms-full.txt` lines 556-637 (hook registration), `context-mode/docs/platform-support.md` (hook format comparison).

### 7.1 Subcommand Dispatch

| Subcommand | Purpose | Initial Scope |
|------------|---------|---------------|
| `capy hook pretooluse` | Security check + tool routing/redirection | **Fully implemented** |
| `capy hook posttooluse` | Session event capture | Stub (future) |
| `capy hook precompact` | Resume snapshot builder | Stub (future) |
| `capy hook sessionstart` | Session restore, routing instruction injection | Stub (routing instructions only) |
| `capy hook userpromptsubmit` | User decision capture | Stub (future) |

### 7.2 PreToolUse Hook Logic

The PreToolUse hook is the core routing engine. It intercepts tool calls and decides how to handle them:

**Tool routing table:**

| Tool | Action |
|------|--------|
| Bash (curl/wget) | Block with redirect message → use `capy_fetch_and_index` |
| Bash (inline HTTP: fetch(), requests.get(), http.get()) | Block with redirect → use `capy_execute` |
| Bash (other) | Security check against deny rules, pass through if allowed |
| Read | Pass through, append guidance: "use `capy_execute_file` for analysis, Read for editing" |
| Grep | Pass through, append guidance: "use `capy_execute` with shell for searches" |
| WebFetch | Block with redirect → use `capy_fetch_and_index` |
| Agent/Task (subagent) | Inject routing block into prompt |
| capy_execute / capy_execute_file / capy_batch_execute | Security checks only |

**Response format (Claude Code):**
```json
{"permissionDecision": "deny", "reason": "Use capy_fetch_and_index instead of curl"}
```
or
```json
{"additionalContext": "Guidance: prefer capy_execute_file for file analysis"}
```

**Reference:** `context-mode/hooks/pretooluse.mjs`, `context-mode/docs/llms-full.txt` lines 558-573.

### 7.3 Routing Instructions

The `capy hook sessionstart` and `capy setup` commands inject/generate XML routing instructions:

```xml
<context_window_protection>
  <priority_instructions>
    Raw tool output floods your context window. You MUST use capy
    MCP tools to keep raw data in the sandbox.
  </priority_instructions>

  <tool_selection_hierarchy>
    1. GATHER: capy_batch_execute(commands, queries)
    2. FOLLOW-UP: capy_search(queries: ["q1", "q2", ...])
    3. PROCESSING: capy_execute(language, code) | capy_execute_file(path, language, code)
  </tool_selection_hierarchy>

  <forbidden_actions>
    - DO NOT use Bash for commands producing >20 lines of output.
    - DO NOT use Read for analysis (use capy_execute_file).
    - DO NOT use WebFetch (use capy_fetch_and_index instead).
    - Bash is ONLY for git/mkdir/rm/mv/navigation.
  </forbidden_actions>

  <output_constraints>
    Keep final response under 500 words.
    Write artifacts to FILES, not inline text.
  </output_constraints>
</context_window_protection>
```

### 7.4 Adapter Interface (Designed-for)

The hook subcommand handler is behind an adapter interface. Claude Code is the first implementation. Adding other platforms later means implementing the interface for their hook formats.

```go
// internal/adapter/adapter.go
type HookAdapter interface {
    ParsePreToolUse(input []byte) (*PreToolUseEvent, error)
    FormatBlock(reason string) ([]byte, error)
    FormatAllow(guidance string) ([]byte, error)
    ParsePostToolUse(input []byte) (*PostToolUseEvent, error)
    FormatPostToolUse(context string) ([]byte, error)
    ParseSessionStart(input []byte) (*SessionStartEvent, error)
    FormatSessionStart(context string) ([]byte, error)
    // ... etc
    Capabilities() PlatformCapabilities
    SessionDir() string
}
```

**Reference:** `context-mode/src/adapters/types.ts` for the full `HookAdapter` interface. `context-mode/docs/platform-support.md` for per-platform capability differences.

---

## 8. Configuration System

### 8.1 Precedence

Three-level, highest wins:

1. `.capy.toml` — project root (visible, explicit intent)
2. `.capy/config.toml` — project dotdir (co-located with DB)
3. `$XDG_CONFIG_HOME/capy/config.toml` — user-level defaults

### 8.2 Configuration Schema

```toml
[store]
path = ".capy/knowledge.db"   # relative to project root, or absolute
# default: $XDG_DATA_HOME/capy/<project-hash>/knowledge.db

[store.cleanup]
cold_threshold_days = 30       # sources unaccessed for N days are "cold"
auto_prune = false             # never delete automatically

[executor]
timeout = 30000                # default execution timeout in milliseconds
max_output_bytes = 102400      # 100 KB truncation cap

[security]
# capy reads deny/allow from .claude/settings.json directly
# no duplication here — single source of truth
```

### 8.3 DB Path Resolution

When using the default XDG location: `$XDG_DATA_HOME/capy/<hash>/knowledge.db` where `<hash>` is derived from the absolute project path (SHA-256, first 12 hex chars). When `$XDG_DATA_HOME` is not set, defaults to `~/.local/share/capy/`.

When a user configures `store.path` as a relative path (e.g., `.capy/knowledge.db`), it's resolved relative to the project root.

### 8.4 Project Root Detection

The project root is determined by:
1. `CLAUDE_PROJECT_DIR` environment variable (set by Claude Code)
2. Git root (`git rev-parse --show-toplevel`)
3. Current working directory (fallback)

---

## 9. CLI

Single binary with subcommands:

| Command | Purpose |
|---------|---------|
| `capy serve` | Start MCP server on stdio (default when no subcommand) |
| `capy hook <event>` | Handle Claude Code lifecycle hooks |
| `capy setup` | Auto-configure host platform (hooks, MCP, routing instructions) |
| `capy doctor` | Diagnose installation from terminal |
| `capy cleanup` | Prune cold-tier sources from terminal |

### 9.1 `capy serve`

Starts the MCP server on stdin/stdout JSON-RPC transport. This is the default behavior when `capy` is invoked without a subcommand (for MCP server registration compatibility: `"command": "capy"`).

**Lifecycle guard:** The server must detect when it becomes orphaned and exit cleanly. Context-mode implements this via `src/lifecycle.ts` which monitors: parent process death (ppid changes to 0 or 1), stdin close (pipe broken), and OS signals (SIGTERM, SIGINT, SIGHUP). In Go, `mcp-go`'s stdio transport handles stdin EOF naturally. Additionally, capy should handle SIGTERM/SIGINT for graceful DB shutdown (flush WAL). A simple signal handler + stdin EOF detection is sufficient — no ppid polling loop needed since Go's stdin read returns EOF when the parent dies.

**Reference:** `context-mode/src/lifecycle.ts`.

### 9.2 `capy setup`

Auto-configures Claude Code integration:

1. Detects `capy` binary location (from `$PATH` or explicit `--binary` flag)
2. Writes/merges `.claude/settings.json` with hook entries:
   ```json
   {
     "hooks": {
       "PreToolUse": [{"matcher": "Bash|Read|Grep|WebFetch|Agent|Task|mcp__*capy*", "hooks": [{"type": "command", "command": "capy hook pretooluse"}]}]
     }
   }
   ```
3. Writes/merges `.mcp.json` or project-level MCP config
4. Generates `CLAUDE.md` routing instructions section (appends if file exists, creates if not)
5. Adds `.capy/` to `.gitignore` (if not already present)

Idempotent — running twice doesn't duplicate entries. Merges with existing settings.

**Designed-for:** `--platform` flag for future multi-platform setup (gemini-cli, vscode-copilot, etc.).

### 9.3 `capy doctor`

Checks (same as the `capy_doctor` MCP tool):
- Runtime availability for all 11 languages
- FTS5 support in SQLite
- Hook registration in `.claude/settings.json`
- Config file discovery and validation
- Knowledge base status

---

## 10. Designed-For: Session Continuity

This section documents the session continuity architecture that is **deferred** from the initial port but must be accommodated in the design.

**Reference:** `context-mode/src/session/` directory — `db.ts`, `extract.ts`, `snapshot.ts`. `context-mode/CONTRIBUTING.md` lines 56-79.

### 10.1 Architecture

Two-database system:
1. **ContentStore** (persistent per-project): FTS5 knowledge base (already in scope)
2. **SessionDB** (persistent per-project, per-session): `$XDG_DATA_HOME/capy/<hash>/sessions/<session-id>.db`

SessionDB stores events captured by hooks:
- PostToolUse → file edits, git operations, errors, tasks, env changes
- UserPromptSubmit → user decisions, corrections, role directives
- PreCompact → triggers snapshot build
- SessionStart → triggers restore from snapshot

### 10.2 Event Categories

13 categories with priority tiers (P1-P5):

| Category | Priority | Hook |
|----------|----------|------|
| Files (read/edit/write) | P1 (Critical) | PostToolUse |
| Tasks (create/update/complete) | P1 | PostToolUse |
| Rules (CLAUDE.md paths) | P1 | SessionStart |
| User prompts | P1 | UserPromptSubmit |
| Decisions/corrections | P2 (High) | UserPromptSubmit |
| Git operations | P2 | PostToolUse |
| Errors | P2 | PostToolUse |
| Environment | P2 | PostToolUse |
| MCP tool usage | P3 (Normal) | PostToolUse |
| Subagent tasks | P3 | PostToolUse |
| Skills invoked | P3 | PostToolUse |
| Session intent | P4 (Low) | UserPromptSubmit |
| Data references | P4 | UserPromptSubmit |

### 10.3 Resume Snapshot

Built by PreCompact hook from stored events. Budget: 2048 bytes of compact XML. Priority-tiered allocation: P1=50%, P2=35%, P3-P4=15%. Lower-priority events dropped first when budget is tight.

### 10.4 Design Implications for Core

The core ContentStore and hook infrastructure must accommodate session continuity without redesign:
- `internal/store/` must support indexing session event files (already possible via `IndexPlainText`)
- `internal/hook/` dispatcher must route all 5 hook events even if handlers are stubs
- `internal/adapter/` interface must include all hook methods
- The `capy serve` command must accept session-related parameters (session ID, session dir)

---

## 11. Designed-For: Multi-Platform Adapters

**Reference:** `context-mode/src/adapters/` directory, `context-mode/docs/platform-support.md`.

The adapter interface (Section 7.4) abstracts all platform differences. Adding a new platform requires:

1. Implement `HookAdapter` for the platform's hook format
2. Add platform detection logic to `internal/adapter/detect.go`
3. Add platform-specific `capy setup --platform <name>` configuration
4. Add platform-specific routing instruction file generation

The `internal/adapter/` package is already structured for this. The Claude Code adapter is the reference implementation.

---

## 12. Dependencies

| Package | Purpose |
|---------|---------|
| `github.com/mark3labs/mcp-go` | MCP server framework (JSON-RPC stdio) |
| `github.com/mattn/go-sqlite3` | SQLite with FTS5 support (CGO) |
| `github.com/pelletier/go-toml/v2` | TOML config parsing |
| `github.com/spf13/cobra` | CLI subcommand framework |
| `github.com/stretchr/testify` | Test assertions (dev dependency) |

HTML-to-Markdown conversion for `capy_fetch_and_index`: evaluate `github.com/JohannesKaufmann/html-to-markdown` at implementation time. Must support GFM tables and element stripping (script, style, nav, header, footer).
