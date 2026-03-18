# Design: Go Port of context-mode

> Reference implementation: `context-mode/` (TypeScript)
> Target: `capy` — Go MCP server and Claude Code plugin

## 1. System Overview

**capy** is a Go MCP server and CLI tool that reduces LLM context window consumption by ~98%. It intercepts data-heavy tool calls (Bash, Read, WebFetch, Grep), executes them in sandboxed subprocesses, and indexes the raw output into a persistent, per-project SQLite FTS5 knowledge base. Only concise summaries and search results enter the context window.

### Key differences from context-mode

| Aspect | context-mode (TypeScript) | capy (Go) |
|--------|--------------------------|-----------|
| Knowledge base lifecycle | Ephemeral, per-session (`/tmp/context-mode-<PID>.db`) | Persistent, per-project |
| Distribution | npm package + plugin marketplace | Single Go binary |
| Hook system | `.mjs` files spawned by shell | Subcommands of the same binary |
| Tool prefix | `ctx_` | `capy_` |
| Configuration | Implicit (env vars, Claude settings) | Explicit TOML config with three-level precedence |
| Content freshness | None (ephemeral DB, no need) | Tiered hot/warm/cold with access tracking |
| Stale content | Not applicable | Configurable cleanup policy |
| DB portability | Not portable | Opt-in committable DB for collaborative development |

### Reference files

- System overview and tool routing: `context-mode/CLAUDE.md`
- Architecture and dev workflow: `context-mode/CONTRIBUTING.md`
- Performance benchmarks: `context-mode/BENCHMARK.md`
- Full README with feature overview: `context-mode/README.md`

## 2. Architecture

```
┌─────────────────────────────────────────────────────┐
│                    Claude Code                       │
│                                                     │
│  hooks (PreToolUse, PostToolUse*, PreCompact*, ...)  │
│       │                              │              │
│       ▼                              ▼              │
│  ┌──────────┐                  ┌───────────┐        │
│  │ capy hook│ (stdin/stdout)   │ capy serve│ (MCP)  │
│  │ pretooluse│                 │           │        │
│  └────┬─────┘                  └─────┬─────┘        │
│       │                              │              │
└───────┼──────────────────────────────┼──────────────┘
        │                              │
        ▼                              ▼
   ┌─────────┐    ┌──────────┐   ┌──────────┐
   │Security │    │Executor  │   │Content   │
   │(deny/   │    │(polyglot │   │Store     │
   │ allow)  │    │ sandbox) │   │(FTS5+    │
   └─────────┘    └────┬─────┘   │ BM25)    │
                       │         └────┬─────┘
                       │              │
                       ▼              ▼
                  ┌─────────────────────┐
                  │  SQLite (per-project│
                  │  knowledge.db)      │
                  └─────────────────────┘

  * PostToolUse, PreCompact, SessionStart hooks are
    stubbed in initial scope, implemented with session
    continuity feature later.
```

### Single binary, multiple roles

The `capy` binary uses subcommands:

| Command | Role |
|---------|------|
| `capy serve` | MCP server (JSON-RPC over stdin/stdout) |
| `capy hook <event>` | Claude Code hook handler (pretooluse, posttooluse, precompact, sessionstart) |
| `capy setup` | Auto-configure host platform (Claude Code initially) |
| `capy doctor` | Diagnose installation (runtimes, hooks, FTS5, config) |
| `capy cleanup` | Prune cold-tier knowledge base sources |

### Package structure

```
cmd/capy/main.go          — entry point, CLI routing
internal/
  server/                  — MCP server, tool handlers, session stats
  store/                   — ContentStore, FTS5, chunking, search, tiering
  executor/                — PolyglotExecutor, process management, truncation
  security/                — Permission enforcement (deny/allow rules)
  hook/                    — Hook subcommand handlers, adapter interface
  config/                  — Configuration loading, TOML parsing, precedence
  platform/                — Platform detection, setup command logic
```

**Designed-for (deferred):**

```
internal/
  session/                 — SessionDB, event storage, snapshots, resume
  adapter/                 — Multi-platform adapters (Gemini CLI, VS Code, etc.)
    claude/                — Claude Code adapter (initial, in hook/ for now)
    gemini/                — Gemini CLI adapter
    vscode/                — VS Code Copilot adapter
    opencode/              — OpenCode adapter
    ...
```

When session continuity is added, `internal/session/` will mirror context-mode's `src/session/` structure. The adapter interface in `internal/hook/` is designed to be extracted into `internal/adapter/` when multiple platforms are supported.

## 3. ContentStore (Knowledge Base)

The ContentStore is a persistent, per-project SQLite database providing full-text search with BM25 ranking. This is the core differentiator of capy.

### Reference files

- FTS5 implementation, chunking, search: `context-mode/src/store.ts`
- SQLite base class, connection management: `context-mode/src/db-base.ts`
- Type definitions: `context-mode/src/types.ts`
- Tests: `context-mode/tests/store.test.ts`

### Database schema

Two FTS5 virtual tables for complementary search strategies:

```sql
-- Source tracking with freshness metadata
CREATE TABLE sources (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    label TEXT NOT NULL,
    content_type TEXT NOT NULL DEFAULT 'plaintext',
    chunk_count INTEGER NOT NULL DEFAULT 0,
    content_hash TEXT,
    indexed_at TEXT NOT NULL DEFAULT (datetime('now')),
    last_accessed_at TEXT NOT NULL DEFAULT (datetime('now')),
    access_count INTEGER NOT NULL DEFAULT 0
);

-- Primary search: Porter stemming with BM25
CREATE VIRTUAL TABLE chunks USING fts5(
    title,
    content,
    source_id UNINDEXED,
    content_type UNINDEXED,
    tokenize='porter unicode61'
);

-- Fallback search: trigram substring matching
CREATE VIRTUAL TABLE chunks_trigram USING fts5(
    title,
    content,
    source_id UNINDEXED,
    content_type UNINDEXED,
    tokenize='trigram'
);

-- Vocabulary for fuzzy matching
CREATE TABLE vocabulary (
    word TEXT PRIMARY KEY,
    frequency INTEGER NOT NULL DEFAULT 1
);
```

Key differences from context-mode's schema:
- `sources` table adds `last_accessed_at`, `access_count`, `content_hash` for tiered lifecycle management
- Schema is otherwise identical to maintain algorithmic compatibility with the reference implementation

### SQLite pragmas

```sql
PRAGMA journal_mode = WAL;          -- concurrent readers during writes
PRAGMA synchronous = NORMAL;        -- safe under WAL, avoids extra fsync
PRAGMA busy_timeout = 5000;         -- 5s wait on lock contention
PRAGMA foreign_keys = ON;
```

Reference: `context-mode/src/db-base.ts` — `constructor()` method applies these pragmas.

### Three-tier search algorithm

Search executes tiers sequentially, stopping when sufficient results are found:

**Tier 1 — Porter stemming (FTS5 MATCH + BM25):**
Standard full-text search. "caching" matches "cached", "caches". Uses `bm25(chunks, 2.0, 1.0)` ranking function (title weight 2.0, content weight 1.0).

**Tier 2 — Trigram substring (FTS5 trigram MATCH):**
Catches partial matches that Porter misses. "useEff" finds "useEffect", "authenticat" finds "authentication". Uses the `chunks_trigram` table.

**Tier 3 — Fuzzy Levenshtein correction:**
Queries the `vocabulary` table for words within Levenshtein distance of search terms, generates corrected query, re-searches via Tier 1. Adaptive max edit distance: 1 for words ≤4 chars, 2 for ≤12 chars, 3 for >12 chars.

Reference: `context-mode/src/store.ts` — `search()`, `searchPorter()`, `searchTrigram()`, `searchFuzzy()`, `levenshteinDistance()` methods.

### Search result freshness boosting

On top of BM25's TF-IDF score, results are boosted by source freshness:
- `last_accessed_at` recency provides a time-decay signal
- `access_count` provides a frequency signal
- The boost is multiplicative but capped to prevent freshness from overwhelming relevance

This is a **new feature** not present in context-mode (which has no need for it since DBs are ephemeral).

### Chunking strategies

All chunkers produce chunks of max `MAX_CHUNK_BYTES = 4096` bytes to optimize BM25 length normalization.

**Markdown chunker:**
Splits by headings (`#`, `##`, `###`, etc.), keeps code blocks intact (never splits mid-block), uses heading hierarchy as chunk titles. If a section exceeds max size, splits on paragraph boundaries.

**JSON chunker:**
Walks the object tree, uses dot-notation key paths as chunk titles (e.g., `response.data.users`). Arrays are batched — items grouped until they hit the size limit.

**Plaintext chunker:**
Fixed-size line groups (20 lines default) with configurable overlap (2 lines). Simple but effective for logs and command output.

Reference: `context-mode/src/store.ts` — `chunkMarkdown()`, `chunkJson()`, `chunkPlaintext()` methods.

### Stopword filtering

Common English words (40+) are filtered from search queries to improve precision. The stopword list matches context-mode's implementation.

Reference: `context-mode/src/store.ts` — `STOPWORDS` constant and `sanitizeQuery()` method.

### Content hash for deduplication

When indexing, the `content_hash` (SHA-256 of raw content) prevents re-indexing identical content. If a source with the same label and hash already exists, the index operation is a no-op (but updates `last_accessed_at`).

### Tiered lifecycle management

Sources are classified by access recency:

| Tier | Criteria | Behavior |
|------|----------|----------|
| Hot | Accessed within 7 days | Normal search priority |
| Warm | Accessed within 30 days | Normal search priority |
| Cold | Not accessed for 30+ days | Candidates for pruning |

**Cleanup policy:**
- `capy cleanup` CLI command prunes cold sources matching configurable criteria
- `capy_cleanup` MCP tool allows the LLM to trigger cleanup
- No automatic deletion — pruning is always explicit
- Thresholds configurable via `.capy.toml` (`store.cleanup.cold_threshold_days`)

**Designed-for (deferred):** When session continuity is added, session events will be indexed into the same per-project ContentStore with a distinct `content_type` (e.g., `"session-event"`). This allows session data to benefit from the same search infrastructure. The tiering system naturally handles session event lifecycle — recent events are hot, old session data goes cold.

## 4. PolyglotExecutor

The executor spawns isolated child processes for code execution across multiple language runtimes.

### Reference files

- Executor implementation: `context-mode/src/executor.ts`
- Smart truncation: `context-mode/src/truncate.ts`
- Runtime detection: `context-mode/src/runtime.ts`
- Tests: `context-mode/tests/executor.test.ts`

### Process isolation

Each execution:
1. Creates a temp directory (`os.MkdirTemp("", "capy-exec-*")`)
2. Writes the script file with appropriate extension
3. Spawns the process in its own process group (`syscall.SysProcAttr{Setpgid: true}`)
4. Captures stdout and stderr separately
5. On completion or timeout, kills the entire process group (`syscall.Kill(-pid, syscall.SIGKILL)`)
6. Cleans up the temp directory

**Working directory:** Shell commands (`bash`, `sh`) run in the project directory (the directory where capy was invoked). Other languages run in the temp directory. This matches context-mode's behavior — shell commands are typically project-aware (e.g., `git status`, `ls src/`), while other languages don't need project context.

### Runtime detection

On first executor use, capy probes for available runtimes:

| Language | Binary probed | Notes |
|----------|--------------|-------|
| bash | `bash` | |
| sh | `sh` | |
| python | `python3`, `python` | Prefers python3 |
| javascript | `bun`, `node` | Prefers Bun (faster) |
| typescript | `bun`, `tsx`, `ts-node` | Prefers Bun |
| go | `go` | Uses `go run` |
| rust | `rustc` | Compiles + runs |
| ruby | `ruby` | |
| php | `php` | |
| perl | `perl` | |
| elixir | `elixir` | |

Results are cached in-memory for the server's lifetime. `exec.LookPath()` is the Go equivalent of context-mode's `command -v` probe.

Reference: `context-mode/src/runtime.ts` — `detectRuntimes()` and `context-mode/src/executor.ts` — `LANGUAGE_CONFIGS` constant.

### Smart truncation

When output exceeds the cap (102.4 KB default, configurable):
- Split output into lines
- Keep first 60% of lines (preserves initial context)
- Keep last 40% of lines (preserves error messages at the end)
- Insert `[N lines / M KB truncated]` annotation at the split point
- Line-boundary aware — never corrupts multi-byte UTF-8 characters

Reference: `context-mode/src/truncate.ts` — `smartTruncate()` function.

### Timeout and background mode

- **Default timeout:** 30 seconds, configurable per-call and via config
- **Timeout handling:** On timeout, kill process group, return partial output with `timedOut: true`
- **Background mode:** Detach process (don't wait for completion), return PID immediately. Useful for dev servers, watchers. Background processes are tracked for cleanup on server shutdown.

### Auto-indexing integration

When execution output exceeds a configurable threshold (e.g., 5 KB) and the caller provides an intent/query string:
1. Full output is indexed into the ContentStore (source label includes the command/language)
2. The indexed content is immediately searched with the provided intent
3. Only the search result snippet is returned to the LLM (not the raw output)

This is the core context-saving mechanism. A 56 KB Playwright snapshot becomes a 299-byte search result.

Reference: `context-mode/src/server.ts` — the `execute()` tool handler shows how auto-indexing is triggered based on output size and intent.

### Exec result structure

```
ExecResult {
    Stdout      string
    Stderr      string
    ExitCode    int
    TimedOut    bool
    Backgrounded bool
    PID         int      // only set if backgrounded
}
```

Reference: `context-mode/src/types.ts` — `ExecResult` interface.

## 5. MCP Server and Tools

### Reference files

- Server implementation, all tool handlers: `context-mode/src/server.ts`
- Type definitions for tool inputs/outputs: `context-mode/src/types.ts`
- MCP protocol integration: `context-mode/package.json` (dependency: `@modelcontextprotocol/sdk`)

### Server architecture

The MCP server communicates via JSON-RPC over stdin/stdout, implemented using the `mcp-go` SDK. Key design decisions:

**Lazy ContentStore:** The SQLite connection is opened only when a tool that needs it is first called (search, index, execute with auto-index). Tools like `capy_doctor` and `capy_stats` work without touching the database.

**Session stats (in-memory):**
- `bytesReturned` — total bytes sent back to the LLM context
- `bytesSandboxed` — total bytes kept out of context
- `callCounts` — per-tool invocation counts
- `indexSize` — number of sources and chunks in the knowledge base

Stats reset when the MCP server process exits. They are not persisted.

**Designed-for (deferred):** When session continuity is added, the server will also manage a SessionDB instance. The `posttooluse` hook will write events to SessionDB, and the server will auto-index session event files on startup (same flow as context-mode).

### Tool definitions

All tools use the `capy_` prefix. Input validation uses the schemas defined in tool registration (mcp-go handles JSON Schema validation).

#### `capy_execute`

Execute code in a sandboxed subprocess. Returns stdout/stderr or, if output exceeds threshold and intent is provided, returns a search result snippet.

**Inputs:** `language` (required), `code` (required), `timeout` (optional), `background` (optional), `intent` (optional)
**Output:** Execution result with stdout, stderr, exit code. If auto-indexed, includes search results instead of raw stdout.

Reference: `context-mode/src/server.ts` — `execute()` handler.

#### `capy_execute_file`

Process a file through user-provided code in the sandbox. The file path is passed to the script as an argument or environment variable.

**Inputs:** `path` (required), `language` (required), `code` (required), `timeout` (optional), `intent` (optional)
**Output:** Same as `capy_execute`.

Reference: `context-mode/src/server.ts` — `execute_file()` handler.

#### `capy_batch_execute`

The primary research tool. Runs multiple commands, auto-indexes all outputs, and searches. One call replaces many individual execute+search steps.

**Inputs:** `commands` (required, array of `{language, code}`), `queries` (optional, array of search strings), `timeout` (optional)
**Output:** Array of execution results + search results. All outputs are auto-indexed regardless of size.

Reference: `context-mode/src/server.ts` — `batch_execute()` handler.

#### `capy_index`

Manually index content into the knowledge base. Accepts markdown, JSON, or plaintext. Detects content type automatically or accepts an explicit type hint.

**Inputs:** `content` (required), `label` (required), `content_type` (optional: "markdown", "json", "plaintext")
**Output:** Confirmation with source ID and chunk count.

Reference: `context-mode/src/server.ts` — `index()` handler.

#### `capy_search`

Query the knowledge base with three-tier fallback. Accepts multiple queries in one call.

**Inputs:** `queries` (required, array of search strings), `limit` (optional, default 10)
**Output:** Array of search results with title, content snippet, source label, rank, match tier (porter/trigram/fuzzy), content type.

Reference: `context-mode/src/server.ts` — `search()` handler.

#### `capy_fetch_and_index`

Fetch a URL, convert HTML to markdown, detect content type, chunk, and index into the knowledge base. The LLM then uses `capy_search` to query the indexed content.

**Inputs:** `url` (required), `label` (optional, defaults to page title or URL)
**Output:** Confirmation with source ID and chunk count.

Implementation notes:
- HTTP fetching via Go's `net/http`
- HTML to markdown conversion (need a Go library — e.g., `jaytaylor/html2text` or a custom converter using `golang.org/x/net/html`)
- Follows redirects (configurable max)
- Respects robots.txt (best-effort)

Reference: `context-mode/src/server.ts` — `fetch_and_index()` handler. Note: context-mode uses `turndown` + `domino` for HTML→markdown. The Go port needs an equivalent.

#### `capy_stats`

Show context savings, call counts, and knowledge base statistics.

**Inputs:** None
**Output:** Session stats (bytes returned vs sandboxed, per-tool call counts) + knowledge base stats (source count, chunk count, DB file size, tier distribution).

Reference: `context-mode/src/server.ts` — `stats()` handler. Capy extends this with tier distribution info.

#### `capy_doctor`

Diagnose the installation. Checks: available runtimes, hook registration, FTS5 availability, config file resolution, DB accessibility, binary version.

**Inputs:** None
**Output:** Diagnostic report with pass/warn/fail per check.

Reference: `context-mode/src/server.ts` — `doctor()` handler.

#### `capy_cleanup`

Prune cold-tier sources from the knowledge base.

**Inputs:** `max_age_days` (optional, default from config), `dry_run` (optional, default true)
**Output:** List of sources that would be (or were) pruned, with labels and last access times.

This is a **new tool** not present in context-mode.

### Dropped tool: `ctx_upgrade`

context-mode's `ctx_upgrade` tool self-updates from GitHub. This is dropped in capy — Go binaries are upgraded via package managers (`go install`, `brew`, release downloads), not self-update scripts.

## 6. Security

### Reference files

- Security implementation: `context-mode/src/security.ts`
- Tests: `context-mode/tests/security.test.ts`

### Permission model

capy reads deny/allow rules from Claude Code's settings.json — the same files, same format. No separate security config. This means capy enforces the same rules the user has already configured for Claude Code.

**Settings locations (checked in order, project overrides global):**
1. `.claude/settings.json` (project-level)
2. `~/.claude/settings.json` (global)

**Rule format:**

```json
{
  "permissions": {
    "deny": ["Bash(sudo *)", "Bash(rm -rf /*)", "Read(.env)", "Read(**/.env*)"],
    "allow": ["Bash(git:*)", "Bash(npm:*)"]
  }
}
```

### Pattern matching

- `*` matches any sequence of non-separator characters
- `**` matches any sequence including separators (for file paths)
- `?` matches a single character
- Colon syntax: `git:*` matches `git status`, `git push`, etc. (the colon is replaced with a space for matching)

### Command splitting

Chained commands (`&&`, `;`, `|`) are split and each part is checked independently. `echo hello && sudo rm -rf /tmp` is blocked because the `sudo` part matches a deny rule.

### Evaluation order

1. Check all deny patterns — if any match, **deny** (deny always wins)
2. Check all allow patterns — if any match, **allow**
3. Default: **allow** (no rules = no restrictions)

### Levenshtein near-miss detection

Commands that are close to a deny pattern (Levenshtein distance ≤ 2) but don't exact-match are flagged as suspicious. This catches typo-based bypass attempts.

### Design principles

- **Pure function:** Takes rules + command → returns allow/deny. No state, no side effects, no file I/O during evaluation.
- **Deny-only firewall:** The security module only blocks. It never modifies commands or suggests alternatives.
- **Platform-agnostic rules:** Rules are read from Claude Code's settings format, but the evaluation logic is platform-independent.

**Designed-for (deferred):** When multi-platform adapters are added, each adapter will resolve its platform's settings path and feed rules into the same security evaluation function. The security module itself doesn't change.

## 7. Hook System

### Reference files

- Hook scripts: `context-mode/hooks/pretooluse.mjs`, `posttooluse.mjs`, `precompact.mjs`, `sessionstart.mjs`
- Hook helpers: `context-mode/hooks/session-helpers.mjs`
- Claude Code adapter: `context-mode/src/adapters/claude-code/`
- Adapter interface: `context-mode/src/adapters/types.ts`

### Hook protocol (Claude Code)

Claude Code fires hooks as shell commands. For each hook event:
1. Claude Code writes a JSON payload to the hook process's stdin
2. The hook process reads stdin, processes the event, writes a JSON response to stdout
3. Claude Code reads stdout and acts on the response

### Subcommand dispatch

All hooks route through `capy hook <event>`:

| Subcommand | Claude Code event | Initial scope |
|------------|-------------------|---------------|
| `capy hook pretooluse` | PreToolUse | **Fully implemented** |
| `capy hook posttooluse` | PostToolUse | Stubbed (pass-through) |
| `capy hook precompact` | PreCompact | Stubbed (pass-through) |
| `capy hook sessionstart` | SessionStart | Stubbed (pass-through) |

### PreToolUse handler

The pretooluse hook intercepts tool calls and decides whether to redirect them through the sandbox:

**Intercept logic:**
- `Bash` calls producing potentially large output → redirect to `capy_execute`
- `Read` calls for analysis (not for editing) → redirect to `capy_execute_file`
- `WebFetch` calls → redirect to `capy_fetch_and_index`
- `Grep` calls with potentially large results → redirect to `capy_batch_execute`
- `capy_execute`, `capy_execute_file`, `capy_batch_execute` → run security check before allowing

**Response format:**

```json
{"decision": "block", "reason": "Use capy_execute instead: <suggested tool call>"}
```

or pass-through (empty/allow response).

Reference: `context-mode/hooks/pretooluse.mjs` — the full interception logic with all tool matchers.

### Adapter interface

The hook handler is implemented behind an interface to support future platforms:

```
Adapter interface:
    ParseHookInput(stdin []byte) → HookEvent
    FormatHookOutput(decision HookDecision) → []byte
    PlatformName() → string
    SetupHooks(binaryPath string) → error
```

Initially, only the Claude Code adapter implements this interface. The interface lives in `internal/hook/` and will be extracted to `internal/adapter/` when more platforms are added.

**Designed-for (deferred):**
- PostToolUse will capture session events (tool calls, file edits, git operations) and write them to SessionDB
- PreCompact will build resume snapshots from SessionDB, injecting a compact summary into context
- SessionStart will detect resumed sessions and auto-index session events into the ContentStore
- Each platform adapter implements the interface for its hook JSON format

Reference for deferred hook implementations:
- `context-mode/hooks/posttooluse.mjs` — event extraction logic
- `context-mode/hooks/precompact.mjs` — snapshot building
- `context-mode/hooks/sessionstart.mjs` — session restore flow
- `context-mode/src/session/` — SessionDB, extractors, snapshots

## 8. Configuration System

### Configuration precedence

Three levels, highest wins:

| Priority | Path | Purpose |
|----------|------|---------|
| 1 (highest) | `.capy.toml` | Project root — visible, explicit override |
| 2 | `.capy/config.toml` | Project dotdir — co-located with DB |
| 3 (lowest) | `$XDG_CONFIG_HOME/capy/config.toml` | User-level defaults |

Configs are **merged**, not replaced. A project-root `.capy.toml` that only sets `store.path` inherits all other values from XDG defaults.

### Configuration schema

```toml
[store]
# Path to the knowledge base SQLite file.
# Relative paths are resolved from the project root.
# Default: $XDG_DATA_HOME/capy/<project-hash>/knowledge.db
path = ""

[store.cleanup]
# Sources not accessed for this many days are classified as "cold"
cold_threshold_days = 30
# If true, automatically prune cold sources on server startup
# Default: false (pruning is always explicit unless opted in)
auto_prune = false

[executor]
# Default execution timeout in seconds
timeout = 30
# Maximum output size before truncation, in bytes (102.4 KB)
max_output_bytes = 104857
# Output size threshold (bytes) that triggers auto-indexing
auto_index_threshold = 5120

[server]
# Log level: "debug", "info", "warn", "error"
log_level = "info"
```

### Project hash for default DB path

When `store.path` is not configured, the knowledge base lives at:
```
$XDG_DATA_HOME/capy/<hash>/knowledge.db
```

Where `<hash>` is the first 16 characters of SHA-256 of the absolute project path. This keeps per-project DBs isolated without any explicit configuration.

Reference: context-mode uses the same hashing approach for SessionDB paths — see `context-mode/hooks/session-helpers.mjs` — `getSessionDbPath()`.

### TOML parsing

Use `pelletier/go-toml/v2` or `BurntSushi/toml` for parsing. The config struct has default values; parsed TOML is merged on top.

## 9. CLI Commands

### `capy serve`

Starts the MCP server on stdin/stdout. This is what Claude Code invokes via `.mcp.json`.

**Flags:**
- `--project-dir` — override project directory detection (default: current working directory or git root)
- `--log-file` — path to log file (default: stderr, but stderr is the MCP transport so logs should go to a file)
- `--log-level` — override config log level

### `capy hook <event>`

Handles Claude Code hook events. Reads JSON from stdin, writes JSON to stdout.

**Events:** `pretooluse`, `posttooluse`, `precompact`, `sessionstart`

**Flags:**
- `--project-dir` — override project directory (hooks inherit the caller's working directory)

### `capy setup`

Auto-configures the host platform for capy integration.

**What it does for Claude Code:**
1. Writes/updates `.claude/settings.json` with hook entries
2. Writes/updates `.mcp.json` with MCP server entry (`capy serve`)
3. Generates routing instructions block for `CLAUDE.md`
4. Creates `.capy/` directory if using in-project DB

**Flags:**
- `--platform` — target platform (default: auto-detect, initially only "claude-code")
- `--binary` — explicit path to capy binary (default: auto-detect from `$PATH`)
- `--global` — configure globally (`~/.claude/`) instead of per-project

**Idempotent:** Running `capy setup` multiple times is safe — it merges with existing settings, never duplicates entries.

Reference: `context-mode/src/cli.ts` — `setup()` command, and `context-mode/src/adapters/claude-code/config.ts` — config generation.

### `capy doctor`

Runs diagnostics and prints a report.

**Checks:**
- capy version
- Available language runtimes (which of the 11 are installed)
- SQLite FTS5 availability (compile-time feature)
- Hook registration status (are hooks configured in settings.json?)
- MCP server registration (is capy in .mcp.json?)
- Config file resolution (which config files were found and loaded)
- Knowledge base status (exists? size? source count? tier distribution?)

### `capy cleanup`

Prune cold-tier sources from the knowledge base.

**Flags:**
- `--max-age-days` — override cold threshold (default: from config)
- `--dry-run` — show what would be pruned without deleting (default: true)
- `--force` — actually delete (sets dry-run to false)

## 10. Designed-for: Session Continuity (Deferred)

This section documents the session continuity architecture that will be implemented after the core port. It is included here so that the core design accounts for it.

### Reference files

- SessionDB: `context-mode/src/session/db.ts`
- Event extraction: `context-mode/src/session/extract.ts`
- Snapshot builder: `context-mode/src/session/snapshot.ts`
- Session tests: `context-mode/tests/session/`

### Overview

Session continuity tracks what the LLM is doing across context compactions. When Claude Code compacts the conversation, the PreCompact hook builds a resume snapshot from tracked events. On the next session start, the snapshot is injected as a compact summary (~275 tokens) with search queries the LLM can use to retrieve details.

### SessionDB schema

```sql
CREATE TABLE session_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    type TEXT NOT NULL,
    category TEXT NOT NULL,
    priority INTEGER NOT NULL,
    data TEXT NOT NULL,
    data_hash TEXT NOT NULL,
    source_hook TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE session_meta (
    session_id TEXT PRIMARY KEY,
    project_dir TEXT NOT NULL,
    started_at TEXT NOT NULL,
    event_count INTEGER NOT NULL DEFAULT 0,
    compact_count INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE session_resume (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    snapshot TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
```

### Integration with ContentStore

Session events are indexed into the same per-project ContentStore with `content_type = "session-event"`. The tiering system handles session event lifecycle naturally — recent events are hot, old session data goes cold and gets pruned.

### Event categories (15 types, 4 priority levels)

- **Critical (P4):** User prompts, active files, tasks, rules, decisions
- **High (P3):** Git operations, errors, environment changes
- **Normal (P2):** MCP tool usage, subagent invocations
- **Low (P1):** Intent classification, role directives

## 11. Designed-for: Multi-Platform Adapters (Deferred)

### Reference files

- Adapter interface: `context-mode/src/adapters/types.ts`
- Platform detection: `context-mode/src/adapters/detect.ts`
- All adapter implementations: `context-mode/src/adapters/`

### Adapter interface

Each platform implements:
- Hook input parsing (platform-specific JSON → normalized `HookEvent`)
- Hook output formatting (normalized `HookDecision` → platform-specific JSON)
- Platform detection (env vars, process names)
- Setup/config generation (platform-specific settings files)

### Supported platforms (future)

| Platform | Hook paradigm | Priority |
|----------|---------------|----------|
| Claude Code | JSON stdin/stdout | **Initial (implemented)** |
| Gemini CLI | JSON stdin/stdout | High |
| VS Code Copilot | JSON stdin/stdout | High |
| Cursor | JSON stdin/stdout (partial) | Medium |
| OpenCode | Different plugin format | Medium |
| Codex CLI | MCP-only (no hooks) | Low |
| Kiro | MCP-only (no hooks) | Low |

## 12. Dependencies

### Required

| Dependency | Purpose |
|------------|---------|
| `github.com/mark3labs/mcp-go` | MCP protocol SDK (JSON-RPC, tool registration) |
| `github.com/mattn/go-sqlite3` | SQLite with FTS5 support (CGO) |
| `github.com/pelletier/go-toml/v2` | TOML configuration parsing |
| `github.com/spf13/cobra` | CLI framework (subcommands, flags) |

### Likely needed

| Dependency | Purpose |
|------------|---------|
| HTML→markdown converter | For `capy_fetch_and_index` (evaluate `jaytaylor/html2text` or similar) |

### Standard library coverage

Most functionality uses Go's standard library: `os/exec` (process spawning), `crypto/sha256` (hashing), `database/sql` (via go-sqlite3), `net/http` (URL fetching), `encoding/json` (hook I/O), `path/filepath` (glob matching), `os` (file I/O, env vars), `strings`/`unicode` (text processing).
