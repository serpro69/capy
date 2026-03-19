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

---

## 13. Addendum: Gaps from Code Review

The following details were discovered during a thorough review of the context-mode source code and `context-mode/docs/llms-full.txt`. They are critical for feature parity and must be incorporated into the implementation.

### 13.1 Sandbox Environment Security

Reference: `context-mode/src/executor.ts` — `#buildSafeEnv()` method.

The executor strips ~50 dangerous environment variables from the sandbox process, organized by category:

| Category | Vars stripped | Risk if kept |
|----------|--------------|--------------|
| Shell | `BASH_ENV`, `ENV`, `PROMPT_COMMAND`, `PS4`, `SHELLOPTS`, `BASHOPTS`, `CDPATH`, `INPUTRC`, `BASH_XTRACEFD` | Auto-execute scripts, dump to stdout |
| Node.js | `NODE_OPTIONS`, `NODE_PATH` | `--require` injection, inspector |
| Python | `PYTHONSTARTUP`, `PYTHONHOME`, `PYTHONWARNINGS`, `PYTHONBREAKPOINT`, `PYTHONINSPECT` | Startup injection, arbitrary callable |
| Ruby | `RUBYOPT`, `RUBYLIB` | CLI option injection, module search path |
| Perl | `PERL5OPT`, `PERL5LIB`, `PERLLIB`, `PERL5DB` | Option/module injection |
| Elixir/Erlang | `ERL_AFLAGS`, `ERL_FLAGS`, `ELIXIR_ERL_OPTIONS`, `ERL_LIBS` | Eval injection |
| Go | `GOFLAGS`, `CGO_CFLAGS`, `CGO_LDFLAGS` | Compiler/linker injection |
| Rust | `RUSTC`, `RUSTC_WRAPPER`, `RUSTC_WORKSPACE_WRAPPER`, `RUSTFLAGS`, `CARGO_BUILD_RUSTC*` | Compiler substitution |
| PHP | `PHPRC`, `PHP_INI_SCAN_DIR` | `auto_prepend_file` → RCE |
| R | `R_PROFILE`, `R_PROFILE_USER`, `R_HOME` | Startup script injection |
| Dynamic linker | `LD_PRELOAD`, `DYLD_INSERT_LIBRARIES` | Shared library injection |
| OpenSSL | `OPENSSL_CONF`, `OPENSSL_ENGINES` | Engine module loading |
| Compiler | `CC`, `CXX`, `AR` | Binary substitution |
| Git | `GIT_TEMPLATE_DIR`, `GIT_CONFIG_GLOBAL`, `GIT_CONFIG_SYSTEM`, `GIT_EXEC_PATH`, `GIT_SSH`, `GIT_SSH_COMMAND`, `GIT_ASKPASS` | Hook/command injection |

Additionally, `BASH_FUNC_*` prefixed vars are stripped (bash exported functions).

**Sandbox overrides (always set):**
- `TMPDIR` = temp directory
- `HOME` = real home (not sandbox temp)
- `LANG` = `en_US.UTF-8`
- `PYTHONDONTWRITEBYTECODE=1`, `PYTHONUNBUFFERED=1`, `PYTHONUTF8=1`
- `NO_COLOR=1`
- `SSL_CERT_FILE` = auto-detected from well-known system paths

**Windows-specific:**
- `MSYS_NO_PATHCONV=1`, `MSYS2_ARG_CONV_EXCL=*`
- Git Bash unix tools ensured on PATH
- `Path` → `PATH` normalization

### 13.2 Shell-Escape Detection in Non-Shell Code

Reference: `context-mode/src/security.ts` — `extractShellCommands()`, `SHELL_ESCAPE_PATTERNS`.

When `capy_execute` runs non-shell code (Python, JS, Ruby, etc.), the security layer scans the source code for embedded shell calls using regex patterns. Extracted commands are checked against the same Bash deny patterns used for direct shell execution.

**Patterns detected per language:**

| Language | Patterns |
|----------|----------|
| Python | `os.system("...")`, `subprocess.run("...")`, `subprocess.run(["rm", "-rf", "/"])` (list form) |
| JavaScript/TypeScript | `execSync("...")`, `spawn("...")`, `execFile("...")` |
| Ruby | `system("...")`, `` `backtick` `` |
| Go | `exec.Command("...")` |
| PHP | `shell_exec("...")`, `exec("...")`, `system("...")`, `passthru("...")`, `proc_open("...")` |
| Rust | `Command::new("...")` |

Python's `subprocess.run(["rm", "-rf", "/"])` list form is specially handled — array elements are extracted and joined with spaces for deny-pattern evaluation.

### 13.3 Exit Code Classification

Reference: `context-mode/src/exit-classify.ts`.

Non-zero exit codes are classified before being reported to the LLM:

- **Soft fail:** `language === "shell"` AND `exitCode === 1` AND `stdout` has non-whitespace content → treated as success (e.g., `grep` returning 1 for "no matches")
- **Hard fail:** everything else → reported as error with stdout + stderr combined

This prevents the LLM from treating `grep` "no matches" as an error and retrying or escalating.

### 13.4 Lifecycle Guard (Orphan Prevention)

Reference: `context-mode/src/lifecycle.ts`.

MCP servers can become orphaned when their parent process dies (e.g., Claude Code crashes). The lifecycle guard detects this and shuts down cleanly:

1. **Periodic parent PID check** (every 30s) — if `ppid` changed from original (reparented to init/systemd), parent is dead
2. **Stdin close detection** — broken pipe from parent
3. **OS signal handling** — `SIGTERM`, `SIGINT`, `SIGHUP` (Unix only)

Go equivalent: goroutine polling `os.Getppid()` + signal handling via `os/signal`.

### 13.5 Hard Cap (100 MB Stream Kill)

Reference: `context-mode/src/executor.ts` — `#hardCapBytes`.

Beyond the soft truncation cap (102.4 KB), there's a hard cap at 100 MB. If combined stdout+stderr exceeds this during streaming, the process group is killed immediately. This prevents memory exhaustion from commands like `yes` or `cat /dev/urandom | base64`.

The hard cap is configurable: `hardCapBytes` option on `PolyglotExecutor`.

### 13.6 Language List Correction

Reference: `context-mode/src/runtime.ts` — `Language` type.

The correct language list is **11 languages** with `shell` (not `bash`/`sh` separately):

`javascript`, `typescript`, `python`, `shell`, `ruby`, `go`, `rust`, `php`, `perl`, `r`, `elixir`

The `shell` language maps to `bash` or `sh` (or PowerShell/cmd.exe on Windows) via runtime detection. **R** was missing from our earlier design — it uses `Rscript` or `r` binary.

### 13.7 Language Auto-Wrapping

Reference: `context-mode/src/executor.ts` — `#writeScript()`.

Before writing the script file, the executor wraps code for certain languages:

- **Go:** If code doesn't contain `package `, wraps in `package main` with `import "fmt"` and `func main() { ... }`
- **PHP:** If code doesn't start with `<?`, prepends `<?php\n`
- **Elixir:** If `mix.exs` exists in project root, prepends `Path.wildcard` to add compiled BEAM paths (`*/ebin`)

### 13.8 File Content Wrapping (`execute_file`)

Reference: `context-mode/src/executor.ts` — `#wrapWithFileContent()`.

The `execute_file` tool injects file-reading boilerplate per language, providing `FILE_CONTENT_PATH`, `file_path`, and `FILE_CONTENT` variables:

| Language | Loading mechanism |
|----------|-------------------|
| JS/TS | `require("fs").readFileSync(path, "utf-8")` |
| Python | `open(path, "r", encoding="utf-8").read()` |
| Shell | `$(cat 'path')` (single-quoted for safety) |
| Ruby | `File.read(path, encoding: "utf-8")` |
| Go | `os.ReadFile(path)` converted to string |
| Rust | `fs::read_to_string(path).unwrap()` |
| PHP | `file_get_contents(path)` |
| Perl | Filehandle with `<:encoding(UTF-8)` and `local $/` slurp |
| R | `readLines(path, warn=FALSE, encoding="UTF-8")` joined with newlines |
| Elixir | `File.read!(path)` |

### 13.9 Three-Tier Settings Precedence

Reference: `context-mode/src/security.ts` — `readBashPolicies()`.

Security settings are read from **three** files (not two):

1. `.claude/settings.local.json` — project-local, not committed
2. `.claude/settings.json` — project-shared, committed
3. `~/.claude/settings.json` — global user settings

Each file can contain `permissions.deny`, `permissions.allow`, and `permissions.ask` arrays. The `ask` decision type is used by hooks (prompt the user) but not by the server (which uses deny-only evaluation via `evaluateCommandDenyOnly()`).

### 13.10 File Path Deny Patterns

Reference: `context-mode/src/security.ts` — `readToolDenyPatterns()`, `evaluateFilePath()`, `fileGlobToRegex()`.

File path patterns are separate from Bash command patterns. They use a different glob syntax (`**` matches path segments, `*` matches non-separator chars) and are extracted from `Tool(glob)` format (e.g., `Read(.env)`, `Read(**/.env*)`).

The `execute_file` tool checks both: file path against Read deny patterns AND code against Bash deny patterns (or shell-escape detection for non-shell languages).

### 13.11 Search: AND/OR Modes and 8-Layer Fallback

Reference: `context-mode/src/store.ts` — `searchWithFallback()`.

The search fallback is more nuanced than "3 tiers". Each tier has AND and OR modes:

```
Layer 1a: Porter + AND (most precise)
Layer 1b: Porter + OR  (relaxed)
Layer 2a: Trigram + AND
Layer 2b: Trigram + OR
Layer 3a: Fuzzy + Porter + AND
Layer 3b: Fuzzy + Porter + OR
Layer 3c: Fuzzy + Trigram + AND
Layer 3d: Fuzzy + Trigram + OR
```

Stops at the first layer that returns results. AND mode spaces terms as required words; OR mode allows any term to match.

### 13.12 Search: Source Filtering

All search methods accept an optional `source` parameter that filters results using `LIKE '%source%'` on `sources.label`. This allows scoped searches (e.g., search only within batch output, or only within a specific indexed document). Separate prepared statements exist for filtered vs unfiltered queries.

### 13.13 Progressive Search Throttling

Reference: `context-mode/src/server.ts` — search tool handler.

To prevent the LLM from flooding context with many individual search calls:

| Calls in 60s window | Behavior |
|---------------------|----------|
| 1–3 | Normal: max 2 results per query |
| 4–8 | Reduced: 1 result per query, warning emitted |
| 9+ | Blocked: returns error demanding `batch_execute` usage |

The window resets every 60 seconds. This is critical for directing the LLM toward batch operations.

### 13.14 Search Output Caps and Snippet Extraction

Reference: `context-mode/src/server.ts` — `extractSnippet()`, `positionsFromHighlight()`.

**Output caps:**
- `capy_search`: 40 KB total across all queries
- `capy_batch_execute`: 80 KB total for search results

**Smart snippet extraction (per search result):**
1. FTS5 `highlight()` function marks matched terms with STX (char 2) / ETX (char 3) markers
2. `positionsFromHighlight()` extracts character offsets of matches
3. 300-character windows are built around each match position
4. Overlapping windows are merged
5. Collected until the snippet budget is reached (1500 bytes for search, 3000 bytes for batch)
6. Fallback to `indexOf` on raw query terms when highlight markers are absent

### 13.15 Distinctive Terms (IDF Scoring)

Reference: `context-mode/src/store.ts` — `getDistinctiveTerms()`.

After indexing, distinctive terms are computed for each source using document frequency scoring:

```
score = IDF + lengthBonus + identifierBonus
```

- **IDF:** `log(totalChunks / count)` — rarer terms score higher
- **Length bonus:** `min(wordLength / 20, 0.5)` — longer words are more specific
- **Identifier bonus:** 1.5 for words with underscores, 0.8 for words ≥12 chars (likely code identifiers)

These are included in tool responses as "Searchable terms" to help the LLM formulate follow-up queries.

### 13.16 Network I/O Tracking (JS/TS Only)

Reference: `context-mode/src/server.ts` — execute handler, JS/TS instrumentation code.

For JavaScript and TypeScript execution, the code is wrapped in an async IIFE that:
1. Intercepts `globalThis.fetch` to track response body sizes
2. Shadows `require('http')`/`require('https')` with wrappers that track data events
3. Reports total network bytes via `__CM_NET__:<bytes>` on stderr at process exit
4. The server parses this marker, adds to `sessionStats.bytesSandboxed`, strips the marker from stderr

This tracking is **deferred for the Go port** (would require JS/TS-specific code instrumentation) but should be noted in the design for future implementation.

### 13.17 `fetch_and_index` Implementation Detail

Reference: `context-mode/src/server.ts` — `buildFetchCode()`, `ctx_fetch_and_index` handler.

The fetch is implemented as a JavaScript subprocess that:
1. Fetches the URL
2. Detects Content-Type from response headers
3. For HTML: converts to markdown using Turndown with GFM plugin (removes `script`, `style`, `nav`, `header`, `footer`, `noscript` elements)
4. **Writes content to a temp file** (not stdout) to bypass the executor's 100 KB truncation cap
5. Writes only a `__CM_CT__:<type>` marker to stdout for content-type routing
6. The handler reads the temp file, routes to appropriate indexing strategy (markdown/JSON/plaintext), returns a 3072-byte preview

For the Go port: the fetch can be done natively with `net/http` instead of a subprocess. HTML→markdown conversion needs a Go library.

### 13.18 `batch_execute` Implementation Details

Reference: `context-mode/src/server.ts` — `ctx_batch_execute` handler.

Key details not in the original design:

1. **Commands run sequentially** with individual `smartTruncate` budgets per command (not a single concatenated script — fixes issue #61 where middle commands could be dropped by head/tail truncation)
2. **Remaining timeout tracking:** Each command gets `totalTimeout - elapsedSoFar`. If exceeded, remaining commands are marked "(skipped — batch timeout exceeded)"
3. **Shell only:** `batch_execute` commands always run as shell (language is not configurable)
4. **stderr merged:** Each command runs as `${cmd.command} 2>&1`
5. **Section inventory:** After indexing, `getChunksBySource()` builds a list of all indexed sections with byte sizes
6. **Three-tier search fallback:** Scoped search (filtered to batch source label) → global search fallback (if no scoped results). Cross-source results get a warning note.
7. **Default timeout:** 60 seconds (not 30)

### 13.19 Input Coercion (Double-Serialization Workaround)

Reference: `context-mode/src/server.ts` — `coerceJsonArray()`, `coerceCommandsArray()`.

Claude Code has a bug where array parameters may be double-serialized as JSON strings (e.g., `"[\"a\",\"b\"]"` instead of `["a","b"]`). The server includes defensive coercion via `z.preprocess()` that parses stringified arrays. Additionally, `batch_execute` coerces plain string commands into `{label, command}` objects.

The Go port should include equivalent input normalization.

### 13.20 Plaintext Chunker: Blank-Line Splitting

Reference: `context-mode/src/store.ts` — `#chunkPlainText()`.

The plaintext chunker has a two-phase strategy not fully described in the original design:

1. **First:** Try blank-line splitting (`\n\s*\n`). If the result has 3–200 sections and each section is under 5000 bytes, use this path. Section title is the first line (up to 80 chars) or "Section N".
2. **Fallback:** Fixed-size 20-line groups with 2-line overlap.
3. **Small input:** If total lines ≤ `linesPerChunk`, emit a single chunk titled "Output".

### 13.21 JSON Chunker: Identity Field Detection

Reference: `context-mode/src/store.ts` — `#findIdentityField()`, `#chunkJSONArray()`.

When chunking JSON arrays, the chunker looks for identity fields on object elements (checked in order: `id`, `name`, `title`, `path`, `slug`, `key`, `label`). This produces human-readable batch titles like `users > alice...zoe` instead of `users > [0-25]`.

### 13.22 `code_chunk_count` Tracking

The `sources` table tracks both `chunk_count` and `code_chunk_count` (chunks containing code blocks). `IndexResult` includes `codeChunks`. This distinction is surfaced in tool responses.

### 13.23 Reference: Complete llms-full.txt Documentation

A comprehensive single-file reference for the entire context-mode feature set is available at `context-mode/docs/llms-full.txt`. This document covers all tools, schemas, chunking strategies, search algorithms, security model, hook system, and edge cases in detail. It should be the primary reference during implementation.
