# Design: Port MCP Core to Go

> Reference implementation: `context-mode/` (TypeScript)
> Target: `capy` — Go MCP server and Claude Code plugin

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
- CLI: `capy serve` (default), `capy hook <event>`, `capy setup`, `capy doctor`, `capy cleanup`
- Config: `.capy.toml` > `.capy/config.toml` > XDG, TOML format

**Designed-for but deferred:**
- Session continuity (SessionDB, event extraction, snapshots, resume flow) — see context-mode's `src/session/` directory
- Multi-platform adapters (Gemini CLI, VS Code Copilot, OpenCode, Cursor, Codex, OpenClaw, Antigravity, Kiro) — see context-mode's `src/adapters/` directory
- `posttooluse`, `precompact`, `sessionstart`, `userpromptsubmit` hook implementations (stubs registered, handlers pass-through)
- Self-update mechanism (Go binaries use package managers or `go install`)

---

## 2. Project Structure

```
cmd/
  capy/
    main.go              → Entry point, cobra/subcommand dispatch (default: serve)

internal/
  server/                → MCP server, tool handlers, session stats
    server.go            → Server struct, stdio transport, tool registration
    tools.go             → Tool handler implementations (execute, search, etc.)
    stats.go             → Session statistics tracking
    snippet.go           → Smart snippet extraction around match positions
    lifecycle.go         → Orphan prevention (ppid polling, stdin, signals)

  store/                 → ContentStore: FTS5 knowledge base
    store.go             → ContentStore struct, DB lifecycle, lazy init
    schema.go            → SQL schema (sources, chunks, chunks_trigram, vocabulary)
    index.go             → Indexing: markdown, JSON, plaintext chunking
    search.go            → Three-tier search: Porter → trigram → fuzzy
    chunk.go             → Chunking strategies (markdown, JSON, plaintext)
    vocabulary.go        → Vocabulary extraction, fuzzy correction (Levenshtein)
    terms.go             → Distinctive terms (IDF scoring)
    cleanup.go           → Tiered lifecycle, cold-source pruning

  executor/              → Polyglot code executor
    executor.go          → PolyglotExecutor struct, process spawning
    runtime.go           → Runtime detection, language dispatch, fallback chains
    truncate.go          → Smart output truncation (60% head + 40% tail)
    wrap.go              → Language-specific auto-wrapping (Go, PHP, Elixir)
    env.go               → Sandbox environment (denylist-based)
    exit_classify.go     → Shell soft-fail classification

  security/              → Permission enforcement
    security.go          → Evaluate commands against deny/allow rules
    glob.go              → Glob-to-regex conversion (bash patterns, file paths)
    split.go             → Chained command splitting (&&, ||, ;, |)
    settings.go          → Parse .claude/settings*.json rules (3-tier)
    shell_escape.go      → Shell-escape detection in non-shell code

  hook/                  → Hook subcommand handlers
    hook.go              → Dispatcher: reads JSON stdin, routes to handler
    pretooluse.go        → PreToolUse: security check, tool routing/redirection
    posttooluse.go       → PostToolUse: stub (future: session event capture)
    precompact.go        → PreCompact: stub (future: resume snapshot)
    sessionstart.go      → SessionStart: stub (routing instructions only)
    userpromptsubmit.go  → UserPromptSubmit: stub (future: user decision capture)
    routing.go           → Routing instruction block (XML for context injection)

  adapter/               → Platform adapter interface
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

### Reference: context-mode structure mapping

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
| `src/exit-classify.ts` | `internal/executor/exit_classify.go` |
| `src/lifecycle.ts` | `internal/server/lifecycle.go` |
| `src/cli.ts` | `cmd/capy/main.go` |
| `src/session/` | `internal/session/` (deferred) |
| `src/adapters/types.ts` | `internal/adapter/adapter.go` |
| `src/adapters/claude-code/` | `internal/adapter/claudecode.go` |
| `hooks/*.mjs` | `internal/hook/` (compiled into the binary) |
| `hooks/core/routing.mjs` | `internal/hook/pretooluse.go` |
| `hooks/core/formatters.mjs` | `internal/adapter/claudecode.go` |

---

## 3. ContentStore (Knowledge Base)

The ContentStore is the core differentiator. It's a persistent SQLite database with two FTS5 virtual tables — one using Porter stemming, one using trigram tokenization — enabling three-tier search fallback with 8-layer granularity.

**Reference:** `context-mode/src/store.ts` (full implementation), `context-mode/docs/llms-full.txt` lines 231-378.

### 3.1 Database Schema

```sql
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA busy_timeout = 5000;
PRAGMA foreign_keys = ON;

-- Sources table (extended from context-mode with freshness + type metadata)
CREATE TABLE IF NOT EXISTS sources (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  label TEXT NOT NULL,
  content_type TEXT NOT NULL DEFAULT 'plaintext',  -- NEW: source-level type tracking
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

The schema is identical to context-mode's except for four new columns on `sources`: `content_type`, `last_accessed_at`, `access_count`, and `content_hash`. These enable tiered freshness management and source-type awareness without changing the search algorithm. The `content_type` on sources is forward-looking: when session continuity is added, session events will be indexed with `content_type = "session-event"`, enabling type-based filtering and cleanup policies.

### 3.2 Three-Tier Search with 8-Layer Fallback

```
Layer 1a: Porter + AND (most precise)
Layer 1b: Porter + OR  (relaxed)
Layer 2a: Trigram + AND
Layer 2b: Trigram + OR
Layer 3a: Fuzzy correction → Porter + AND
Layer 3b: Fuzzy correction → Porter + OR
Layer 3c: Fuzzy correction → Trigram + AND
Layer 3d: Fuzzy correction → Trigram + OR
```

Stops at the first layer that returns results. Each layer supports optional source filtering via `LIKE '%source%'` on `sources.label`. Separate prepared statements exist for filtered vs unfiltered queries.

**BM25 ranking** at the SQL level:
```sql
SELECT *, bm25(chunks, 2.0, 1.0) AS rank,
       highlight(chunks, 1, char(2), char(3)) AS highlighted
FROM chunks WHERE chunks MATCH ?
ORDER BY rank  -- BM25 returns negative scores; more negative = better
```

- `2.0` — title weight (titles matter more)
- `1.0` — content weight
- Highlight markers: `char(2)` (STX start) and `char(3)` (ETX end) for match position extraction

**AND mode:** Terms are quoted and space-joined: `"word1" "word2"` (all must match)
**OR mode:** Terms joined with OR: `"word1" OR "word2"` (any can match)

**Reference:** `context-mode/src/store.ts` — `searchWithFallback()` function.

### 3.3 Fuzzy Search (Levenshtein)

**Adaptive edit distance thresholds:**

| Word length | Max edit distance |
|-------------|-------------------|
| 1-4 chars | 1 |
| 5-12 chars | 2 |
| 13+ chars | 3 |

Vocabulary is built during indexing: words extracted by splitting on `[^\p{L}\p{N}_-]+` (Unicode-aware), filtered to 3+ characters, excluding 88 stopwords. Uses `INSERT OR IGNORE` (no frequency tracking). Fuzzy correction retrieves candidates from vocabulary where `length(word) BETWEEN wordLength-maxDist AND wordLength+maxDist`, computes Levenshtein distance, returns the closest match within threshold.

**Reference:** `context-mode/src/store.ts` — `levenshtein()`, `maxEditDistance()`, `fuzzyCorrect()` functions.

### 3.4 Chunking Strategies

Three chunking strategies, matching context-mode exactly. All produce chunks of max `MAX_CHUNK_BYTES = 4096` bytes to optimize BM25 length normalization.

**Markdown chunking** (`chunkMarkdown`):
- Splits on H1-H4 headings (`/^(#{1,4})\s+(.+)$/)`)
- Maintains heading stack for breadcrumb titles ("H1 > H2 > H3")
- Preserves code blocks as atomic units (tracks fence state with `` ``` `` markers)
- Flushes on new heading or horizontal rule (`/^[-_*]{3,}\s*$/`)
- Max chunk size: 4096 bytes. Oversized chunks split at paragraph boundaries (double newlines) with numbered suffixes ("Title (1)", "Title (2)")
- Code block detection: chunks containing `` ```\w*\n[\s\S]*?``` `` get `content_type = "code"`, others get `"prose"`

**Plain text chunking** (`chunkPlainText`):
- Phase 1: blank-line splitting (`\n\s*\n`). Used when 3-200 sections with each under 5000 bytes. Title = first line (up to 80 chars) or "Section N".
- Phase 2 (fallback): fixed 20-line groups with 2-line overlap. Step size = 18. Titles show line ranges ("Lines 1-20").
- Single chunk titled "Output" if input has fewer lines than `linesPerChunk`.

**JSON chunking** (`walkJSON`):
- Recursively walks object tree, key paths as chunk titles ("data > users > 0")
- Small flat objects (< 4096 bytes, no nested objects/arrays): single chunk
- Nested objects: always recurse for searchable key-path titles (even when subtree fits in one chunk)
- Arrays: batch items by accumulated size up to 4096 bytes. Identity fields checked in order: `id`, `name`, `title`, `path`, `slug`, `key`, `label` for human-readable batch titles
- Falls back to `indexPlainText` if JSON parsing fails

**Reference:** `context-mode/src/store.ts` — `#chunkMarkdown()`, `#chunkPlainText()`, `#walkJSON()`, `#findIdentityField()`, `#chunkJSONArray()`.

### 3.5 Smart Snippet Extraction

Search results include smart snippets (up to 1500 bytes for search, 3000 bytes for batch_execute) centered on match positions. Match positions are derived from FTS5 highlight markers (`char(2)`/`char(3)` delimiters). Overlapping 300-character windows around each match are merged until the byte limit.

Fallback: if no highlight markers, use `strings.Index` on raw query terms.

**Reference:** `context-mode/src/server.ts` — `extractSnippet()`, `positionsFromHighlight()`.

### 3.6 Distinctive Terms (IDF Scoring)

```
score = IDF + lengthBonus + identifierBonus
```

- **IDF:** `log(totalChunks / count)` where `count` = chunks containing the word
- **Length bonus:** `min(wordLength / 20, 0.5)` — rewards longer, more specific words
- **Identifier bonus:** 1.5 for words with underscores, 0.8 for words ≥12 chars (likely code identifiers)
- Words must be 3+ characters, not in stopword list
- Filter: 2 ≤ appearances ≤ 40% of source chunks
- Default: 40 terms per source

**Reference:** `context-mode/src/store.ts` — `getDistinctiveTerms()`.

### 3.7 Progressive Search Throttling

Per 60-second sliding window:

| Call count | Behavior |
|------------|----------|
| 1-3 | Normal: max 2 results per query |
| 4-8 | Reduced: 1 result per query, warning emitted |
| 9+ | Blocked: returns error, demands `batch_execute` usage |

Output caps: 40 KB for `search`, 80 KB for `batch_execute`.

**Reference:** `context-mode/src/server.ts` — search handler throttle logic.

### 3.8 Tiered Freshness (New in capy)

Sources are classified by access recency:

| Tier | Criteria | Behavior |
|------|----------|----------|
| Hot | Accessed within 7 days | Normal BM25 ranking |
| Warm | Accessed within 30 days | Normal BM25 ranking |
| Cold | Not accessed for 30+ days | Candidates for pruning |

- `access_count` incremented on each search hit (background goroutine)
- `last_accessed_at` updated on each search hit
- `content_hash` (SHA-256 of raw content) enables re-index detection: same hash = skip, different hash = delete + re-index
- Pruning is **never automatic** — triggered by `capy_cleanup` tool or `capy cleanup` CLI command
- Cold threshold days configurable via `.capy.toml`
- Freshness metadata does NOT affect search ranking — BM25 scores are pure relevance

### 3.9 Stopword List

88 stopwords matching context-mode exactly:

Common English: the, and, for, are, but, not, you, all, can, had, her, was, one, our, out, has, his, how, its, may, new, now, old, see, way, who, did, get, got, let, say, she, too, use, will, with, this, that, from, they, been, have, many, some, them, than, each, make, like, just, over, such, take, into, year, your, good, could, would, about, which, their, there, other, after, should, through, also, more, most, only, very, when, what, then, these, those, being, does, done, both, same, still, while, where, here, were, much.

Code/changelog: update, updates, updated, deps, dev, tests, test, add, added, fix, fixed, run, running, using.

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

Runtime detection uses `exec.LookPath()` for each runtime on first use. Results cached via `sync.Once` for the server lifetime. Initial port targets **macOS and Linux** (Windows deferred). Platform differences handled explicitly where they arise: SSL cert paths (macOS `/etc/ssl/cert.pem` vs Debian/RHEL paths), dynamic linker denylist (`DYLD_INSERT_LIBRARIES` on macOS, `LD_PRELOAD` on Linux), process group semantics.

### 4.2 Process Isolation

- Each execution creates a temp directory, writes the script file
- Process spawned in its own **process group** (`syscall.SysProcAttr{Setpgid: true}`)
- Cleanup kills the **entire process group** (`syscall.Kill(-pgid, syscall.SIGKILL)`)
- Shell commands run in the **project directory** (respects git working tree)
- Other languages run in the temp directory
- Shell scripts get `0o700` permissions (executable)

### 4.3 Auto-Wrapping

| Language | Condition | Wrapping |
|----------|-----------|----------|
| Go | Code doesn't contain `package ` | Wraps in `package main` + `import "fmt"` + `func main() { ... }` |
| PHP | Code doesn't start with `<?` | Prepends `<?php\n` |
| Elixir | `mix.exs` exists in project root | Prepends `Path.wildcard` to add `*/ebin` to code path |
| Rust | Always | Compiled with `rustc` to temp binary, then executed (two-step) |

**Reference:** `context-mode/src/executor.ts` — `#writeScript()`.

### 4.4 FILE_CONTENT Variable Injection (execute_file)

| Language | Variable | Loading mechanism |
|----------|----------|-------------------|
| JavaScript/TypeScript | `FILE_CONTENT` | `require("fs").readFileSync(path, "utf-8")` |
| Python | `FILE_CONTENT` | `open(path, "r", encoding="utf-8").read()` |
| Shell | `FILE_CONTENT` | `$(cat 'path')` (single-quoted for safety) |
| Ruby | `FILE_CONTENT` | `File.read(path, encoding: "utf-8")` |
| Go | `FILE_CONTENT` | `os.ReadFile(path)` converted to string |
| Rust | `file_content` | `fs::read_to_string(path).unwrap()` |
| PHP | `$FILE_CONTENT` | `file_get_contents(path)` |
| Perl | `$FILE_CONTENT` | Filehandle with `<:encoding(UTF-8)` and `local $/` slurp |
| R | `FILE_CONTENT` | `readLines(path, warn=FALSE, encoding="UTF-8")` joined with newlines |
| Elixir | `file_content` | `File.read!(path)` |

`FILE_CONTENT_PATH` and `file_path` are also set to the absolute file path.

**Reference:** `context-mode/src/executor.ts` — `#wrapWithFileContent()`.

### 4.5 Smart Truncation

When output exceeds `maxOutputBytes` (102,400 bytes / 100 KB):

1. Split output into lines
2. Collect head lines until 60% of byte budget consumed
3. Collect tail lines (from end) until 40% of byte budget consumed
4. Insert separator: `"... [N lines / X.XKB truncated — showing first M + last K lines] ..."`
5. All calculations use byte length, snapping to line boundaries for UTF-8 safety

**Hard cap**: 100 MB (`hardCapBytes`). If combined stdout+stderr exceeds this during streaming, the entire process group is killed immediately. Prevents memory exhaustion from `yes`, `cat /dev/urandom`, etc.

**Reference:** `context-mode/src/truncate.ts` — `smartTruncate()`, `context-mode/src/executor.ts` — `#spawn()` hard cap logic.

### 4.6 Exit Code Classification

- Exit code 0 → success (return stdout)
- Exit code 1 with non-empty stdout → **soft failure** (return stdout, not an error — e.g., `grep` no matches)
- Exit code 1 with empty stdout → real error (return stderr)
- Exit code > 1 → real error (return stdout + stderr combined)

**Reference:** `context-mode/src/exit-classify.ts`.

### 4.7 Sandbox Environment Security (Denylist)

The executor builds a safe environment by starting with the full parent process environment, stripping ~50 dangerous variables, then applying sandbox overrides. This denylist approach preserves user env vars (database URLs, API keys) while removing known attack vectors.

**Denied categories:**

| Category | Vars stripped | Risk |
|----------|--------------|------|
| Shell | `BASH_ENV`, `ENV`, `PROMPT_COMMAND`, `PS4`, `SHELLOPTS`, `BASHOPTS`, `CDPATH`, `INPUTRC`, `BASH_XTRACEFD` | Auto-execute, stdout dump |
| Node.js | `NODE_OPTIONS`, `NODE_PATH` | `--require` injection |
| Python | `PYTHONSTARTUP`, `PYTHONHOME`, `PYTHONWARNINGS`, `PYTHONBREAKPOINT`, `PYTHONINSPECT` | Startup injection |
| Ruby | `RUBYOPT`, `RUBYLIB` | CLI option injection |
| Perl | `PERL5OPT`, `PERL5LIB`, `PERLLIB`, `PERL5DB` | Module injection |
| Elixir/Erlang | `ERL_AFLAGS`, `ERL_FLAGS`, `ELIXIR_ERL_OPTIONS`, `ERL_LIBS` | Eval injection |
| Go | `GOFLAGS`, `CGO_CFLAGS`, `CGO_LDFLAGS` | Compiler injection |
| Rust | `RUSTC`, `RUSTC_WRAPPER`, `RUSTC_WORKSPACE_WRAPPER`, `CARGO_BUILD_RUSTC*`, `RUSTFLAGS` | Compiler substitution |
| PHP | `PHPRC`, `PHP_INI_SCAN_DIR` | `auto_prepend_file` → RCE |
| R | `R_PROFILE`, `R_PROFILE_USER`, `R_HOME` | Startup script injection |
| Dynamic linker | `LD_PRELOAD`, `DYLD_INSERT_LIBRARIES` | Shared library injection |
| OpenSSL | `OPENSSL_CONF`, `OPENSSL_ENGINES` | Engine module loading |
| Compiler | `CC`, `CXX`, `AR` | Binary substitution |
| Git | `GIT_TEMPLATE_DIR`, `GIT_CONFIG_GLOBAL`, `GIT_CONFIG_SYSTEM`, `GIT_EXEC_PATH`, `GIT_SSH`, `GIT_SSH_COMMAND`, `GIT_ASKPASS` | Hook/command injection |

Additionally, `BASH_FUNC_*` prefixed vars are stripped (bash exported functions).

**Sandbox overrides (always set):**
- `TMPDIR` = temp directory
- `HOME` = real home directory
- `LANG` = `en_US.UTF-8`
- `PYTHONDONTWRITEBYTECODE=1`, `PYTHONUNBUFFERED=1`, `PYTHONUTF8=1`
- `NO_COLOR=1`
- `SSL_CERT_FILE` = auto-detected from well-known system paths
- `PATH` defaults to `/usr/local/bin:/usr/bin:/bin` if missing

**Reference:** `context-mode/src/executor.ts` — `#buildSafeEnv()`.

### 4.8 Background Mode

When `background: true`, the process is detached after timeout: PID recorded, output streams destroyed, partial output returned immediately with `backgrounded: true`. Background PIDs tracked in a set and killed on server shutdown via `cleanupBackgrounded()`.

### 4.9 Intent-Driven Search Flow

When `intent` is provided and output exceeds 5000 bytes:

1. Full output indexed into FTS5 via `store.IndexPlainText()` with source label `execute:<language>` or `file:<path>`
2. `store.SearchWithFallback(intent, 5, source)` runs against indexed content
3. If matches found: returns section count, total output size, matched section titles and first-line previews + distinctive terms
4. If no matches: returns total line count, byte size, all source labels, and distinctive searchable terms
5. Raw output bytes tracked as `bytesIndexed` (kept out of context)

**Reference:** `context-mode/src/server.ts` — `intentSearch()` function.

---

## 5. MCP Server and Tools

The server is a JSON-RPC stdio process using `mcp-go`. It exposes 9 tools with the `capy_` prefix, lazy-loads the ContentStore on first use, and tracks session statistics.

**Reference:** `context-mode/src/server.ts` (full implementation), `context-mode/docs/llms-full.txt` lines 26-230.

### 5.1 Tools

| Tool | Purpose |
|------|---------|
| `capy_execute` | Sandbox code execution (language, code, timeout, background, intent) |
| `capy_execute_file` | Process a file in sandbox (`FILE_CONTENT` injection) |
| `capy_batch_execute` | Run multiple shell commands + search in one call |
| `capy_index` | Chunk and index markdown/JSON/plaintext into FTS5 |
| `capy_search` | Query knowledge base with three-tier fallback |
| `capy_fetch_and_index` | Fetch URL → detect content type → chunk → index |
| `capy_stats` | Show context savings, call counts, KB size |
| `capy_doctor` | Diagnose runtimes, hooks, FTS5, config |
| `capy_cleanup` | Prune cold-tier sources by age/access policy |

**Dropped from context-mode:** `ctx_upgrade` — Go binaries are upgraded via package managers or `go install`.

**New tools:** `capy_doctor` (also available as MCP tool, not just CLI), `capy_cleanup` (exposes tiered freshness pruning to the LLM).

### 5.2 Tool Parameters

#### capy_execute
```
language: string (required) — one of 11 supported languages
code: string (required) — source code to execute
timeout: int (optional) — milliseconds, default 30000
background: bool (optional) — keep process running after timeout
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
1. Each command runs as a **separate** shell execution (not concatenated — prevents head/tail truncation from dropping middle commands)
2. Each command gets its own smartTruncate budget and remaining timeout
3. Combined output indexed into FTS5 via markdown chunking (labels become `#` headings)
4. Section inventory built via `getChunksBySource()` (all sections with byte sizes)
5. Each query searched with three-tier fallback: scoped to batch source → global fallback with cross-source warning
6. Default timeout 60 seconds. Output cap: 80 KB.

#### capy_index
```
content: string (optional) — raw text to index (mutually exclusive with path)
path: string (optional) — file path to read and index
source: string (optional) — label for retrieval, defaults to path or "untitled"
```

Returns: `{sourceId, label, totalChunks, codeChunks}`

#### capy_search
```
queries: array (required) — array of search terms (also accepts `query: string`)
limit: int (optional) — results per query, default 3 (max 2 in normal throttle mode)
source: string (optional) — filter to specific source (partial LIKE match)
```

#### capy_fetch_and_index
```
url: string (required) — URL to fetch
source: string (optional) — label, defaults to URL
```

Content-type routing (via native Go `net/http`):
- HTML → Markdown conversion (strip script, style, nav, header, footer) via `JohannesKaufmann/html-to-markdown` → markdown chunking
- JSON → JSON chunking (key-path titles)
- Plain text → plain text chunking (line groups)

Preview: first 3072 bytes of converted content returned. Rest truncated with `"...[truncated — use search() for full content]"`.

#### capy_stats
No parameters. Returns per-tool byte/call breakdown, savings ratio, reduction percentage, estimated tokens, knowledge base size.

#### capy_doctor
No parameters. Checks runtimes, FTS5, hooks, config, knowledge base status.

#### capy_cleanup
```
max_age_days: int (optional) — prune sources not accessed for N days (default from config)
dry_run: bool (optional) — show what would be pruned without deleting (default true)
```

### 5.3 Lazy Store Initialization

The ContentStore opens its SQLite connection only when a tool that needs it is first called. `sync.Once`-guarded. Tools like `capy_doctor` and `capy_stats` (when no DB exists) work without touching the database.

### 5.4 Session Statistics

In-memory tracking matching context-mode:

```
SessionStats:
  SessionStart time.Time
  Calls        map[string]int     // tool name → call count
  BytesReturned map[string]int64  // tool name → bytes returned to context
  BytesIndexed  int64             // bytes stored in FTS5
  BytesSandboxed int64            // network I/O inside sandbox
```

Savings calculation:
```
keptOut = bytesIndexed + bytesSandboxed
totalProcessed = keptOut + totalBytesReturned
savingsRatio = totalProcessed / max(totalBytesReturned, 1)
reductionPct = (1 - totalBytesReturned / totalProcessed) * 100
estimatedTokens = totalBytesReturned / 4
```

### 5.5 Input Coercion

Claude Code may double-serialize array parameters as JSON strings (`"[\"a\",\"b\"]"` instead of `["a","b"]`). All array inputs (queries, commands) must be defensively parsed. Additionally, `batch_execute` coerces plain string commands into `{label: "cmd_N", command: string}` objects.

### 5.6 Lifecycle Guard (Orphan Prevention)

MCP servers can become orphaned when their parent process dies. The lifecycle guard detects this via three mechanisms:

1. **Periodic parent PID check** (every 30s) — if `os.Getppid()` changed from original, parent is dead
2. **Stdin close detection** — broken pipe from parent
3. **OS signal handling** — `SIGTERM`, `SIGINT`, `SIGHUP`

On shutdown: close DB (flush WAL), kill backgrounded processes, exit cleanly.

**Reference:** `context-mode/src/lifecycle.ts`.

---

## 6. Security

Port of context-mode's permission system. Reads deny/allow rules from Claude Code's settings files.

**Reference:** `context-mode/src/security.ts` (full implementation), `context-mode/docs/llms-full.txt` lines 462-553.

### 6.1 Settings Hierarchy

Three-tier, highest priority first:
1. `.claude/settings.local.json` — project-local, not committed
2. `.claude/settings.json` — project-shared, committed
3. `~/.claude/settings.json` — global user settings

Each file may contain `permissions.deny`, `permissions.allow`, and `permissions.ask` arrays.

### 6.2 Pattern Formats

**Bash patterns:**
```
Bash(command:argsGlob)   — colon format: "rm:*" matches "rm" with any args
Bash(command argsGlob)   — space format: "sudo *" matches "sudo" with any args
Bash(glob)               — plain glob: "* --force" matches any command with --force
```

Pattern conversion to regex:
- Colon format: command literal, args glob-to-regex. Produces `/^command(\s+argsRegex)?$/`
- Space format: split at first space, command literal, rest glob. Produces `/^command\s+argsRegex$/`
- Plain glob: entire pattern converted. `*` → `[^\s]*` (no whitespace), other regex chars escaped

**Tool patterns:**
```
ToolName(glob)   — e.g., Read(.env), Read(**/*.key)
```

File path globs: `**` matches path segments (including zero), `*` matches non-separator chars, `?` matches single non-separator char.

### 6.3 Chained Command Splitting

Shell commands split on `&&`, `||`, `;`, `|` before evaluation. **Quote-aware**: respects single quotes, double quotes, and backticks. Each segment checked independently.

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

Python's `subprocess.run(["rm", "-rf", "/"])` list form is specially handled — array elements extracted and joined.

### 6.5 Evaluation

- **Server-side** (`evaluateCommandDenyOnly`): only enforce deny patterns. Returns "deny" or "allow". No "ask" — server has no UI for prompts.
- **Hook-side** (`evaluateCommand`): full deny > ask > allow evaluation. First definitive match wins. Default (no match): "ask".
- **File paths** (`evaluateFilePath`): normalizes backslashes, uses `fileGlobToRegex` for path-aware matching.

**Invariant:** Deny always wins over allow. If any segment of a chained command matches any deny rule, the entire command is blocked.

---

## 7. Hook System and Claude Code Integration

Hooks intercept tool calls before they reach the LLM context. Claude Code fires hooks as shell commands, passing JSON on stdin and reading JSON from stdout.

**Reference:** `context-mode/hooks/pretooluse.mjs`, `context-mode/hooks/core/routing.mjs`, `context-mode/docs/llms-full.txt` lines 556-637.

### 7.1 Subcommand Dispatch

| Subcommand | Purpose | Initial Scope |
|------------|---------|---------------|
| `capy hook pretooluse` | Security check + tool routing/redirection | **Fully implemented** |
| `capy hook posttooluse` | Session event capture | Stub (future) |
| `capy hook precompact` | Resume snapshot builder | Stub (future) |
| `capy hook sessionstart` | Session restore, routing instruction injection | Stub (routing instructions only) |
| `capy hook userpromptsubmit` | User decision capture | Stub (future) |

### 7.2 PreToolUse Hook Logic

**Tool routing table:**

| Tool | Action |
|------|--------|
| Bash (curl/wget) | Block with redirect → use `capy_fetch_and_index` |
| Bash (inline HTTP: fetch(), requests.get(), http.get()) | Block with redirect → use `capy_execute` |
| Bash (build tools: gradle, maven) | Block with redirect → use `capy_execute` in sandbox |
| Bash (other) | Security check against deny rules. Pass through with guidance (once per session) |
| Read | Pass through with guidance (once per session): "use `capy_execute_file` for analysis, Read for editing" |
| Grep | Pass through with guidance (once per session): "use `capy_execute` with shell for searches" |
| WebFetch | Deny with redirect → use `capy_fetch_and_index` |
| Agent/Task (subagent) | Inject routing block into prompt. Upgrade `subagent_type` from "Bash" to "general-purpose" |
| capy_execute (shell) | Security check against Bash deny patterns |
| capy_execute_file | Check file path against Read deny patterns AND code against Bash deny patterns |
| capy_batch_execute | Check each command individually against Bash deny patterns |

**Guidance throttle:** Read, Grep, and Bash advisory messages are shown at most once per session (first invocation only). Prevents repetitive guidance that wastes context.

**Response format (Claude Code):**
```json
{"hookSpecificOutput": {"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": "..."}}
```
or
```json
{"hookSpecificOutput": {"hookEventName": "PreToolUse", "additionalContext": "..."}}
```
or
```json
{"hookSpecificOutput": {"hookEventName": "PreToolUse", "permissionDecision": "allow", "updatedInput": {...}}}
```

**Reference:** `context-mode/hooks/core/routing.mjs` for pure routing logic, `context-mode/hooks/core/formatters.mjs` for Claude Code response format.

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

The hook handler is behind an adapter interface. Claude Code is the first implementation. The `internal/adapter/` package is structured for multi-platform support from day 1.

```go
// internal/adapter/adapter.go
type HookAdapter interface {
    ParsePreToolUse(input []byte) (*PreToolUseEvent, error)
    FormatBlock(reason string) ([]byte, error)
    FormatAllow(guidance string) ([]byte, error)
    FormatModify(updatedInput map[string]interface{}) ([]byte, error)
    ParsePostToolUse(input []byte) (*PostToolUseEvent, error)
    FormatPostToolUse(context string) ([]byte, error)
    ParseSessionStart(input []byte) (*SessionStartEvent, error)
    FormatSessionStart(context string) ([]byte, error)
    Capabilities() PlatformCapabilities
}
```

**Reference:** `context-mode/src/adapters/types.ts` for the full `HookAdapter` interface. `context-mode/src/adapters/claude-code/` for the Claude Code implementation.

---

## 8. Configuration System

### 8.1 Precedence

Three-level, highest wins:

1. `.capy.toml` — project root (visible, explicit intent)
2. `.capy/config.toml` — project dotdir (co-located with DB)
3. `$XDG_CONFIG_HOME/capy/config.toml` — user-level defaults

Configs are **merged**, not replaced. A project-root `.capy.toml` that only sets `store.path` inherits all other values from XDG defaults.

### 8.2 Configuration Schema

```toml
[store]
path = ""  # relative to project root, or absolute
# default: $XDG_DATA_HOME/capy/<project-hash>/knowledge.db

[store.cleanup]
cold_threshold_days = 30    # sources unaccessed for N days are "cold"
auto_prune = false          # never delete automatically

[executor]
timeout = 30                # default execution timeout in seconds (converted to ms internally)
max_output_bytes = 102400   # 100 KB truncation cap

[server]
log_level = "info"          # "debug", "info", "warn", "error"
```

### 8.3 DB Path Resolution

Default XDG location: `$XDG_DATA_HOME/capy/<hash>/knowledge.db` where `<hash>` is SHA-256 of the absolute project path, first **16 hex chars**. When `$XDG_DATA_HOME` is not set, defaults to `~/.local/share/capy/`.

Relative `store.path` values are resolved against the project root.

### 8.4 Project Root Detection

1. `CLAUDE_PROJECT_DIR` environment variable (set by Claude Code)
2. Git root (`git rev-parse --show-toplevel`)
3. Current working directory (fallback)

---

## 9. CLI

Single binary with subcommands. **Default behavior (no subcommand): start MCP server** — for MCP config compatibility (`"command": "capy"`).

| Command | Purpose |
|---------|---------|
| `capy` (no args) | Start MCP server on stdio (same as `capy serve`) |
| `capy serve` | Start MCP server on stdio |
| `capy hook <event>` | Handle Claude Code lifecycle hooks |
| `capy setup` | Auto-configure host platform (hooks, MCP, routing instructions) |
| `capy doctor` | Diagnose installation from terminal |
| `capy cleanup` | Prune cold-tier sources from terminal |

### 9.1 `capy setup`

Auto-configures Claude Code integration:

1. Detects `capy` binary location (from `$PATH` or `--binary` flag)
2. Writes/merges `.claude/settings.json` with hook entries (PreToolUse + all stubs)
3. Writes/merges `.mcp.json` with MCP server entry
4. Appends routing instructions to `CLAUDE.md` (check for existing `<context_window_protection>` block)
5. Adds `.capy/` to `.gitignore`

Idempotent — running twice doesn't duplicate entries. Merges with existing settings via deep JSON merge.

**Designed-for:** `--platform` flag for future multi-platform setup.

---

## 10. Designed-For: Session Continuity (Deferred)

Session continuity tracks what the LLM is doing across context compactions. Two-database system:
1. **ContentStore** (persistent per-project): FTS5 knowledge base (in scope)
2. **SessionDB** (persistent per-project): `$XDG_DATA_HOME/capy/<hash>/sessions/<session-id>.db`

Session events are indexed into the ContentStore with `content_type = "session-event"`. The tiering system handles lifecycle naturally.

**Reference:** `context-mode/src/session/` directory.

---

## 11. Designed-For: Multi-Platform Adapters (Deferred)

The adapter interface (Section 7.4) abstracts all platform differences. Adding a new platform requires implementing `HookAdapter` for the platform's hook format.

**Reference:** `context-mode/src/adapters/` directory, `context-mode/docs/platform-support.md`.

---

## 12. Dependencies

| Package | Purpose |
|---------|---------|
| `github.com/mark3labs/mcp-go` | MCP server framework (JSON-RPC stdio) |
| `github.com/mattn/go-sqlite3` | SQLite with FTS5 support (CGO) |
| `github.com/pelletier/go-toml/v2` | TOML config parsing |
| `github.com/spf13/cobra` | CLI subcommand framework |
| `github.com/stretchr/testify` | Test assertions (dev dependency) |

HTML-to-Markdown conversion for `capy_fetch_and_index`: `github.com/JohannesKaufmann/html-to-markdown`. Must support GFM tables and element stripping (script, style, nav, header, footer).
