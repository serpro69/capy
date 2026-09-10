# Implementation Plan: Go Port of context-mode

> Design: [./design.md](./design.md)
> Reference implementation: `context-mode/` (TypeScript)

This document provides a detailed implementation plan for porting context-mode to Go. Each section corresponds to a task in [tasks.md](./tasks.md) and contains enough detail for an experienced Go developer with zero capy/context-mode context to implement it.

---

## Project scaffolding

### Go module and directory structure

Initialize the Go module at the project root:
- Module path: decide on a Go module path (e.g., `github.com/serpro69/capy`)
- Go version: 1.23+ (for `slices`, `maps`, `slog` packages)
- Create the full directory tree as specified in [design.md § Architecture](./design.md#2-architecture)

### CLI framework

Use `spf13/cobra` for subcommand routing. The entry point is `cmd/capy/main.go`.

**Root command:** `capy` — prints help/version.

**Subcommands:**
- `serve` — starts MCP server (stdin/stdout). Initially a placeholder that prints "MCP server starting..." to stderr and exits.
- `hook <event>` — hook handler. `event` is a positional argument: `pretooluse`, `posttooluse`, `precompact`, `sessionstart`. Initially all stubs that read stdin and write an empty JSON response.
- `setup` — platform setup. Initially a placeholder.
- `doctor` — diagnostics. Initially a placeholder.
- `cleanup` — knowledge base cleanup. Initially a placeholder.

**Global flags on root:**
- `--project-dir` (string) — override project directory
- `--version` (bool) — print version and exit

**Version:** Embed via `go:embed` or `-ldflags` at build time. Use a `internal/version/version.go` with a `Version` variable.

### Build verification

The scaffolding task is complete when:
- `go build ./cmd/capy` produces a binary
- `./capy --version` prints the version
- `./capy serve`, `./capy hook pretooluse`, `./capy setup`, `./capy doctor`, `./capy cleanup` all run without panic (stubs are fine)
- `go vet ./...` and `go test ./...` pass (even if no tests yet)

### Files to create

```
cmd/capy/main.go
internal/version/version.go
go.mod
go.sum (after go mod tidy)
```

---

## Configuration system

### Config struct

Create `internal/config/config.go` with a `Config` struct matching the schema in [design.md § Configuration](./design.md#8-configuration-system). All fields have default values.

### Loading logic

Create `internal/config/loader.go`:

1. Start with defaults (hardcoded in Go struct tags or a `DefaultConfig()` function)
2. Check `$XDG_CONFIG_HOME/capy/config.toml` (or `~/.config/capy/config.toml` if XDG unset) — merge if exists
3. Check `.capy/config.toml` relative to project root — merge if exists
4. Check `.capy.toml` in project root — merge if exists (highest priority)

**Merging:** Only non-zero values from higher-priority configs override lower-priority ones. Use struct comparison with defaults to detect "was this field set?" or use pointer fields for optional values.

**Project root detection:** Walk up from the current directory looking for `.git/`, `.capy.toml`, or `.capy/`. Fall back to current working directory. This should be a utility function in `internal/config/` since other parts of the system need it too (hooks, server).

### Project hash

Implement the project hash function: `SHA256(absoluteProjectPath)[:16]`. This determines the default XDG database path: `$XDG_DATA_HOME/capy/<hash>/knowledge.db`.

Reference: `context-mode/hooks/session-helpers.mjs` — `getSessionDbPath()` uses the same hashing pattern.

### TOML parsing

Use `pelletier/go-toml/v2` for unmarshaling. The TOML structure maps directly to the Go struct.

### Files to create

```
internal/config/config.go      — Config struct, defaults
internal/config/loader.go      — Load, merge, project root detection
internal/config/project.go     — Project hash, path resolution
```

---

## SQLite foundation and ContentStore schema

### Database connection management

Create `internal/store/db.go`:

- Open SQLite with `mattn/go-sqlite3` via `database/sql`
- Apply pragmas on open: WAL mode, NORMAL synchronous, busy timeout 5s, foreign keys ON
- Schema initialization: check if tables exist, create if not (idempotent)
- Prepared statement management: prepare all SQL statements once on open, store in struct fields
- Close method: finalize statements, WAL checkpoint (`PRAGMA wal_checkpoint(TRUNCATE)`), close DB

Reference: `context-mode/src/db-base.ts` — constructor applies pragmas; `context-mode/src/store.ts` — `ensureSchema()`, prepared statements pattern.

### CGO build tag

`mattn/go-sqlite3` requires CGO. Ensure the build works with `CGO_ENABLED=1`. Add a comment in `go.mod` or the Makefile noting this requirement. FTS5 is enabled via the `fts5` build tag: `go build -tags fts5`.

### Schema creation

Create the full schema from [design.md § ContentStore](./design.md#3-contentstore-knowledge-base):
- `sources` table with freshness metadata columns
- `chunks` FTS5 virtual table (Porter tokenizer)
- `chunks_trigram` FTS5 virtual table (trigram tokenizer)
- `vocabulary` table

All in a single `initSchema()` function using `IF NOT EXISTS` for idempotency.

### ContentStore struct

Create `internal/store/store.go`:

```
ContentStore struct:
    db          *sql.DB
    stmts       preparedStatements   // struct of *sql.Stmt
    projectDir  string
    dbPath      string
```

**Constructor:** `NewContentStore(dbPath, projectDir string) (*ContentStore, error)` — opens DB, applies pragmas, initializes schema, prepares statements.

**Lazy initialization pattern:** The MCP server won't call `NewContentStore` until a tool that needs it is invoked. The server holds a `sync.Once`-guarded initializer.

### Files to create

```
internal/store/db.go       — connection management, pragmas, schema
internal/store/store.go    — ContentStore struct, constructor, close
```

---

## ContentStore — Chunking and indexing

### Chunking strategies

Create `internal/store/chunk.go` with three chunker functions:

**`chunkMarkdown(content string, maxBytes int) []Chunk`**

- Split by heading lines (`^#{1,6}\s`)
- Preserve code blocks: track fenced code block state (` ``` `), never split inside a block
- Use heading hierarchy as chunk title (e.g., `"## API > ### Authentication"`)
- If a section exceeds `maxBytes`, split on paragraph boundaries (double newline)
- Default `maxBytes = 4096` (the `MAX_CHUNK_BYTES` constant)

Reference: `context-mode/src/store.ts` — `chunkMarkdown()` method. Pay close attention to the code block tracking and heading hierarchy logic.

**`chunkJSON(content string, maxBytes int) []Chunk`**

- Parse JSON into `interface{}`
- Walk the object tree recursively
- Use dot-notation key paths as chunk titles (e.g., `"response.data.users"`)
- Arrays: batch items together until hitting size limit
- Serialize each chunk as a JSON string

Reference: `context-mode/src/store.ts` — `chunkJson()` method.

**`chunkPlaintext(content string, maxBytes int, linesPerChunk int) []Chunk`**

- Split into lines
- Group into chunks of `linesPerChunk` lines (default 20)
- 2-line overlap between consecutive chunks
- Title is `"Lines N-M"`

Reference: `context-mode/src/store.ts` — `chunkPlaintext()` method.

### Content type detection

Create `internal/store/detect.go`:

- `detectContentType(content string) string` — returns `"markdown"`, `"json"`, or `"plaintext"`
- Check for valid JSON first (`json.Valid()`)
- Check for markdown indicators (headings, code fences, links)
- Default to plaintext

### Indexing function

Add to `internal/store/store.go`:

**`Index(content, label, contentType string) (sourceID int64, chunkCount int, err error)`**

1. Compute `content_hash` (SHA-256 of content)
2. Check if a source with same `label` and `content_hash` exists — if so, update `last_accessed_at` and return (dedup)
3. If same label but different hash exists — delete old source and its chunks (re-index)
4. Auto-detect content type if not provided
5. Chunk the content using the appropriate strategy
6. Insert source row
7. Insert chunks into both `chunks` and `chunks_trigram` FTS5 tables (in a transaction)
8. Extract vocabulary from chunks and upsert into `vocabulary` table
9. Return source ID and chunk count

**Vocabulary extraction:** Split chunk content on word boundaries, lowercase, filter stopwords, upsert into vocabulary with frequency increment.

**Stopword list:** Port context-mode's `STOPWORDS` constant from `context-mode/src/store.ts`.

Reference: `context-mode/src/store.ts` — `index()` method, `extractVocabulary()`, `STOPWORDS`.

### Chunk struct

```
Chunk struct:
    Title       string
    Content     string
    ContentType string  // "code" or "prose"
}
```

### Files to create/modify

```
internal/store/chunk.go     — chunkMarkdown, chunkJSON, chunkPlaintext
internal/store/detect.go    — detectContentType
internal/store/store.go     — add Index() method
internal/store/stopwords.go — STOPWORDS list
```

---

## ContentStore — Three-tier search

### Search function

Add to `internal/store/store.go`:

**`Search(queries []string, limit int) ([]SearchResult, error)`**

For each query:
1. Sanitize the query (remove special chars, split on whitespace, filter stopwords)
2. Try Tier 1 (Porter stemming) — if sufficient results, return
3. Try Tier 2 (trigram substring) — if results found, merge with Tier 1 and return
4. Try Tier 3 (fuzzy Levenshtein) — correct terms, re-search via Tier 1
5. Deduplicate results across tiers (same chunk ID)
6. Apply freshness boost to final scores
7. Sort by final score, limit results

### Tier implementations

Create `internal/store/search.go`:

**`searchPorter(query string, limit int) ([]SearchResult, error)`**
- `SELECT title, content, source_id, content_type, rank FROM chunks WHERE chunks MATCH ? ORDER BY bm25(chunks, 2.0, 1.0) LIMIT ?`
- The MATCH query uses FTS5 query syntax (space-separated terms are AND-ed)

**`searchTrigram(query string, limit int) ([]SearchResult, error)`**
- `SELECT title, content, source_id, content_type, rank FROM chunks_trigram WHERE chunks_trigram MATCH ? ORDER BY bm25(chunks_trigram, 2.0, 1.0) LIMIT ?`
- Trigram MATCH syntax: quote the search string for substring matching

**`searchFuzzy(query string, limit int) ([]SearchResult, error)`**
- For each term in the query:
  - Query vocabulary table for words within Levenshtein distance
  - Adaptive max distance: 1 for ≤4 chars, 2 for ≤12 chars, 3 for >12 chars
- Build corrected query from best matches
- Re-run `searchPorter` with corrected query

### Levenshtein distance

Create `internal/store/levenshtein.go`:
- `levenshteinDistance(a, b string) int` — standard dynamic programming implementation
- Reference: `context-mode/src/store.ts` — `levenshteinDistance()` function

### Query sanitization

Create `internal/store/sanitize.go`:
- `sanitizeQuery(query string) string` — remove quotes, brackets, special FTS5 chars, split on whitespace, filter stopwords, rejoin
- Reference: `context-mode/src/store.ts` — `sanitizeQuery()` method

### Freshness boost

Add to search scoring:
- Join with `sources` table to get `last_accessed_at` and `access_count`
- Compute a freshness multiplier based on time since last access (exponential decay)
- Multiply BM25 rank by freshness boost (cap the boost to prevent freshness from dominating relevance)

### Search result struct

```
SearchResult struct:
    Title       string
    Content     string
    Source      string   // source label
    Rank        float64  // final score (BM25 * freshness)
    ContentType string   // "code" or "prose"
    MatchTier   string   // "porter", "trigram", or "fuzzy"
}
```

### Access tracking

When search returns results, update `last_accessed_at` and increment `access_count` on the matching sources. Do this in a background goroutine to avoid slowing down search responses.

### Files to create/modify

```
internal/store/search.go       — searchPorter, searchTrigram, searchFuzzy, Search
internal/store/levenshtein.go  — levenshteinDistance
internal/store/sanitize.go     — sanitizeQuery, stopword filtering
internal/store/store.go        — add Search() method, freshness boost logic
```

---

## ContentStore — Tiered lifecycle and cleanup

### Tier classification

Add to `internal/store/store.go`:

**`ClassifySources() ([]SourceInfo, error)`**
- Query all sources with their metadata
- Classify each as hot/warm/cold based on `last_accessed_at` and configured thresholds
- Return with tier label attached

### Cleanup function

**`Cleanup(maxAgeDays int, dryRun bool) ([]SourceInfo, error)`**
1. Find all sources where `last_accessed_at` is older than `maxAgeDays` AND `access_count` is below a threshold (e.g., 0 or configurable)
2. If `dryRun`, return the list without deleting
3. If not dry run, delete in a transaction: remove chunks from both FTS5 tables, remove vocabulary entries, remove source row
4. Return the list of pruned sources

### Stats function

**`Stats() (*StoreStats, error)`**
- Total sources, chunks, vocabulary size
- DB file size on disk
- Tier distribution (count per tier)
- Oldest and newest source timestamps

### StoreStats struct

```
StoreStats struct:
    SourceCount     int
    ChunkCount      int
    VocabSize       int
    DBSizeBytes     int64
    TierDistribution map[string]int  // "hot": N, "warm": N, "cold": N
    OldestSource    time.Time
    NewestSource    time.Time
}
```

### SourceInfo struct

```
SourceInfo struct:
    ID             int64
    Label          string
    ContentType    string
    ChunkCount     int
    ContentHash    string
    IndexedAt      time.Time
    LastAccessedAt time.Time
    AccessCount    int
    Tier           string  // "hot", "warm", "cold"
}
```

### Files to create/modify

```
internal/store/store.go     — add ClassifySources(), Cleanup(), Stats() methods
internal/store/types.go     — StoreStats, SourceInfo structs
```

---

## PolyglotExecutor

### Runtime detection

Create `internal/executor/runtime.go`:

- `DetectRuntimes() map[string]string` — for each supported language, use `exec.LookPath()` to find the binary. Returns a map of language → binary path.
- Preference order for languages with multiple runtimes (e.g., JS: bun > node, TS: bun > tsx > ts-node, Python: python3 > python)
- Cache results in a package-level `sync.Once`
- Reference: `context-mode/src/executor.ts` — `LANGUAGE_CONFIGS` and runtime detection logic

### Executor struct and execution

Create `internal/executor/executor.go`:

```
Executor struct:
    runtimes    map[string]string  // language → binary path
    projectDir  string
    config      ExecutorConfig
}

ExecutorConfig struct:
    DefaultTimeout   time.Duration
    MaxOutputBytes   int
}
```

**`NewExecutor(projectDir string, cfg ExecutorConfig) *Executor`** — creates executor, triggers runtime detection.

**`Execute(ctx context.Context, req ExecRequest) (*ExecResult, error)`**

1. Validate language is supported (runtime detected)
2. Create temp directory
3. Write script file with appropriate extension (`.sh`, `.py`, `.js`, `.ts`, `.go`, `.rs`, `.rb`, `.php`, `.pl`, `.exs`)
4. Build command: `cmd := exec.CommandContext(ctx, runtime, args...)`
5. Set working directory: project dir for shell, temp dir for others
6. Set process group isolation: `cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}`
7. Capture stdout and stderr via pipes
8. Start process, wait with timeout
9. On timeout: kill process group (`syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)`), set `TimedOut: true`
10. Apply smart truncation to output
11. Clean up temp directory (defer)
12. Return `ExecResult`

**Background mode:**
- If `req.Background` is true, start the process, record PID, return immediately
- Don't wait for completion, don't capture output
- Set `Backgrounded: true` and `PID` in result

Reference: `context-mode/src/executor.ts` — `execute()` method. Pay attention to the language-specific command construction (e.g., Go uses `go run file.go`, Rust compiles then runs, etc.).

### Language-specific command construction

Each language needs specific handling:

| Language | Command | Extension | Notes |
|----------|---------|-----------|-------|
| bash | `bash script.sh` | `.sh` | |
| sh | `sh script.sh` | `.sh` | |
| python | `python3 script.py` | `.py` | |
| javascript | `node script.js` or `bun script.js` | `.js` | |
| typescript | `bun script.ts` or `tsx script.ts` | `.ts` | |
| go | `go run script.go` | `.go` | Needs temp module or single-file mode |
| rust | `rustc script.rs -o out && ./out` | `.rs` | Two-step: compile + run |
| ruby | `ruby script.rb` | `.rb` | |
| php | `php script.php` | `.php` | |
| perl | `perl script.pl` | `.pl` | |
| elixir | `elixir script.exs` | `.exs` | |

Reference: `context-mode/src/executor.ts` — `LANGUAGE_CONFIGS` object has the full mapping.

### Smart truncation

Create `internal/executor/truncate.go`:

**`SmartTruncate(output string, maxBytes int) string`**

1. If `len(output) <= maxBytes`, return as-is
2. Split into lines
3. Compute line counts: `headLines = totalLines * 0.6`, `tailLines = totalLines * 0.4`
4. Take first `headLines` lines and last `tailLines` lines
5. Compute truncated stats: lines removed, bytes removed
6. Join with annotation: `\n[{N} lines / {M} KB truncated]\n`
7. Verify result doesn't exceed maxBytes (trim further if needed)

Line-boundary splitting ensures no UTF-8 corruption.

Reference: `context-mode/src/truncate.ts` — `smartTruncate()` function.

### ExecRequest and ExecResult structs

```
ExecRequest struct:
    Language   string
    Code       string
    Timeout    time.Duration  // 0 means use default
    Background bool
    Intent     string         // for auto-indexing
    FilePath   string         // for execute_file
}

ExecResult struct:
    Stdout       string
    Stderr       string
    ExitCode     int
    TimedOut     bool
    Backgrounded bool
    PID          int
}
```

### Files to create

```
internal/executor/executor.go   — Executor struct, Execute method
internal/executor/runtime.go    — DetectRuntimes, language configs
internal/executor/truncate.go   — SmartTruncate
internal/executor/types.go      — ExecRequest, ExecResult
```

---

## Security

### Settings parsing

Create `internal/security/settings.go`:

- `LoadRules(projectDir string) (*Rules, error)`
- Read `.claude/settings.json` from project dir (if exists)
- Read `~/.claude/settings.json` (if exists)
- Parse `permissions.deny` and `permissions.allow` arrays from both
- Merge: project-level rules take precedence over global
- Return `Rules{Deny: []string, Allow: []string}`

The settings.json format is Claude Code's own format — capy reads it but never writes to it (setup command writes hooks, not security rules).

### Permission evaluation

Create `internal/security/eval.go`:

**`Check(rules *Rules, tool string, input string) Decision`**

1. Format the check string as `Tool(input)` (e.g., `Bash(sudo rm -rf /)`)
2. For `Bash` tool: split chained commands (`&&`, `;`, `|`) and check each part independently
3. Check all deny patterns — if any match, return `Deny`
4. Check all allow patterns — if any match, return `Allow`
5. Default: return `Allow` (no rules = no restrictions)

**`Decision`** is an enum: `Allow`, `Deny`, `Suspicious` (near-miss)

### Pattern matching

Create `internal/security/pattern.go`:

**`matchPattern(pattern, input string) bool`**

- Handle colon syntax: `git:*` → replace `:` with space for matching (`git *`)
- Handle `Tool(pattern)` wrapper: extract tool name and inner pattern
- Glob matching: `*` matches non-separator chars, `**` matches anything including separators, `?` matches single char
- Use `filepath.Match` for simple cases, custom implementation for `**` and colon syntax

Reference: `context-mode/src/security.ts` — `matchPattern()`, `checkPermission()`, `splitChainedCommands()`.

### Command splitting

**`splitChainedCommands(command string) []string`**

- Split on `&&`, `||`, `;`, `|` (pipe)
- Trim whitespace from each part
- Handle quoted strings (don't split inside quotes)

Reference: `context-mode/src/security.ts` — `splitChainedCommands()`.

### Levenshtein near-miss detection

**`checkNearMiss(rules *Rules, tool, input string) bool`**

- For each deny pattern, compute Levenshtein distance to the input
- If distance ≤ 2 but not 0 (not an exact match), flag as suspicious
- This catches typo-based bypass attempts

### Files to create

```
internal/security/settings.go  — LoadRules, settings.json parsing
internal/security/eval.go      — Check, Decision type
internal/security/pattern.go   — matchPattern, splitChainedCommands
internal/security/types.go     — Rules, Decision structs
```

---

## MCP Server — Core setup and tool registration

### Server struct

Create `internal/server/server.go`:

```
Server struct:
    store       *store.ContentStore  // nil until lazy-init
    storeOnce   sync.Once
    executor    *executor.Executor
    security    *security.Rules
    config      *config.Config
    stats       *SessionStats
    projectDir  string
}
```

### mcp-go integration

Use `mcp-go` to set up the MCP server:

1. Create a new `mcp-go` server instance with stdio transport
2. Register all 9 tools with their JSON Schema input definitions
3. Each tool handler is a method on the `Server` struct
4. Start the server (blocks on stdin/stdout)

Reference: `mcp-go` documentation for tool registration patterns. Also reference `context-mode/src/server.ts` — the tool registration section with `server.setRequestHandler(ListToolsRequestSchema, ...)`.

### Lazy ContentStore initialization

The ContentStore is expensive to open (SQLite connection, schema check, statement preparation). It's initialized on first use:

```go
func (s *Server) getStore() (*store.ContentStore, error) {
    var initErr error
    s.storeOnce.Do(func() {
        dbPath := s.config.Store.ResolvePath(s.projectDir)
        s.store, initErr = store.NewContentStore(dbPath, s.projectDir)
    })
    return s.store, initErr
}
```

### Session stats

Create `internal/server/stats.go`:

```
SessionStats struct:
    BytesReturned   int64   // total bytes sent to LLM context
    BytesSandboxed  int64   // total bytes kept out of context
    CallCounts      map[string]int  // per-tool invocation counts
    mu              sync.Mutex
}
```

Thread-safe via mutex. Incremented by each tool handler.

### Tool input schemas

Define JSON Schema for each tool's inputs. mcp-go supports defining these programmatically. Reference `context-mode/src/server.ts` — the `inputSchema` objects in each tool registration.

### Serve command integration

Update `cmd/capy/main.go` to wire the `serve` subcommand:
1. Load config (using project dir detection)
2. Load security rules
3. Create executor
4. Create server (store is lazy)
5. Start MCP server on stdin/stdout

### Files to create/modify

```
internal/server/server.go    — Server struct, constructor, getStore(), serve loop
internal/server/stats.go     — SessionStats
internal/server/tools.go     — tool registration (JSON Schema definitions)
cmd/capy/main.go             — wire serve command
```

---

## MCP Tools — Execution tools

### `capy_execute` handler

Create `internal/server/tool_execute.go`:

1. Parse inputs: `language`, `code`, `timeout`, `background`, `intent`
2. Run security check: `security.Check(rules, "Bash", code)` (for shell) or appropriate tool name
3. If denied, return error with reason
4. Call `executor.Execute(ctx, req)`
5. If output exceeds auto-index threshold AND intent is provided:
   a. Call `store.Index(output, label, "plaintext")`
   b. Call `store.Search([]string{intent}, 5)`
   c. Return search results instead of raw output
6. Otherwise return `ExecResult` formatted as MCP tool response
7. Update session stats (bytes returned vs sandboxed)

Reference: `context-mode/src/server.ts` — `execute()` handler.

### `capy_execute_file` handler

Create `internal/server/tool_execute_file.go`:

1. Parse inputs: `path`, `language`, `code`, `timeout`, `intent`
2. Security check on the file path: `security.Check(rules, "Read", path)`
3. The file path is injected into the code as an environment variable or argument
4. Call `executor.Execute(ctx, req)` with `FilePath` set
5. Same auto-index logic as `capy_execute`

Reference: `context-mode/src/server.ts` — `execute_file()` handler.

### `capy_batch_execute` handler

Create `internal/server/tool_batch.go`:

1. Parse inputs: `commands` (array of `{language, code}`), `queries` (optional), `timeout`
2. Security check each command
3. Execute all commands (sequentially — they may depend on each other's side effects)
4. Auto-index ALL outputs regardless of size (batch is always a research operation)
5. If queries provided, search the knowledge base with them
6. Return combined results: execution summaries + search results

Reference: `context-mode/src/server.ts` — `batch_execute()` handler. This is the primary research tool — one call replaces many individual steps.

### Files to create

```
internal/server/tool_execute.go       — capy_execute handler
internal/server/tool_execute_file.go  — capy_execute_file handler
internal/server/tool_batch.go         — capy_batch_execute handler
```

---

## MCP Tools — Knowledge tools

### `capy_index` handler

Create `internal/server/tool_index.go`:

1. Parse inputs: `content`, `label`, `content_type` (optional)
2. Call `store.Index(content, label, contentType)`
3. Return confirmation: source ID, chunk count, content type used

Reference: `context-mode/src/server.ts` — `index()` handler.

### `capy_search` handler

Create `internal/server/tool_search.go`:

1. Parse inputs: `queries` (array of strings), `limit` (optional, default 10)
2. Call `store.Search(queries, limit)`
3. Format results: title, content snippet, source label, rank, match tier
4. Return formatted results

Reference: `context-mode/src/server.ts` — `search()` handler.

### `capy_fetch_and_index` handler

Create `internal/server/tool_fetch.go`:

1. Parse inputs: `url`, `label` (optional)
2. Fetch URL via `net/http` with reasonable defaults (timeout, redirect limit, User-Agent)
3. Detect content type from response headers
4. If HTML: convert to markdown (need HTML→markdown library)
5. If JSON: pass through as-is
6. If plain text: pass through
7. Use page title or URL as label if not provided
8. Call `store.Index(content, label, contentType)`
9. Return confirmation: source ID, chunk count

**HTML to markdown conversion:** Evaluate Go libraries:
- `jaytaylor/html2text` — simple, strips HTML to text
- `JohannesKaufmann/html-to-markdown` — more faithful conversion (preserves links, headings, code blocks)
- Custom implementation using `golang.org/x/net/html` — most control but most effort

The choice should preserve headings and code blocks (important for the markdown chunker). `JohannesKaufmann/html-to-markdown` is the closest equivalent to context-mode's `turndown` library.

Reference: `context-mode/src/server.ts` — `fetch_and_index()` handler. Context-mode uses `turndown` + `turndown-plugin-gfm` + `domino`.

### Files to create

```
internal/server/tool_index.go    — capy_index handler
internal/server/tool_search.go   — capy_search handler
internal/server/tool_fetch.go    — capy_fetch_and_index handler
```

---

## MCP Tools — Utility tools

### `capy_stats` handler

Create `internal/server/tool_stats.go`:

1. Collect session stats (bytes returned, sandboxed, call counts)
2. If store is initialized, call `store.Stats()` for knowledge base stats
3. Format as human-readable report with tier distribution

Reference: `context-mode/src/server.ts` — `stats()` handler. Capy extends this with tier distribution.

### `capy_doctor` handler

Create `internal/server/tool_doctor.go`:

1. Check capy version
2. Detect available runtimes (call `executor.DetectRuntimes()`)
3. Verify FTS5 availability (try creating a test FTS5 table)
4. Check config file resolution (which files were found)
5. Check knowledge base status (exists? accessible? stats?)
6. Check hook registration (read `.claude/settings.json`, verify hook commands exist)
7. Check MCP registration (read `.mcp.json`, verify capy entry exists)
8. Format as pass/warn/fail report

Reference: `context-mode/src/server.ts` — `doctor()` handler.

### `capy_cleanup` handler

Create `internal/server/tool_cleanup.go`:

1. Parse inputs: `max_age_days` (optional), `dry_run` (optional, default true)
2. Call `store.Cleanup(maxAgeDays, dryRun)`
3. Return list of pruned (or would-be-pruned) sources

This is a new tool not in context-mode.

### Files to create

```
internal/server/tool_stats.go    — capy_stats handler
internal/server/tool_doctor.go   — capy_doctor handler
internal/server/tool_cleanup.go  — capy_cleanup handler
```

---

## Hook system — PreToolUse

### Hook dispatch

Create `internal/hook/handler.go`:

**`HandleHook(event string, stdin io.Reader, stdout io.Writer) error`**

1. Read JSON from stdin
2. Dispatch based on event: `pretooluse` → `handlePreToolUse()`, others → pass-through stub
3. Write JSON response to stdout

### Claude Code hook protocol

The JSON format for Claude Code hooks:

**Input (stdin):**
```json
{
  "tool_name": "Bash",
  "tool_input": {"command": "find . -name '*.go' | head -50"}
}
```

**Output (stdout) — block:**
```json
{
  "decision": "block",
  "reason": "Use capy_batch_execute instead for commands with large output"
}
```

**Output (stdout) — allow:**
```json
{}
```

Reference: `context-mode/hooks/pretooluse.mjs` — full stdin/stdout format.

### PreToolUse interception logic

Create `internal/hook/pretooluse.go`:

**`handlePreToolUse(input HookInput) HookOutput`**

Decision matrix:
- **Bash** commands likely to produce large output → block with suggestion to use `capy_execute` or `capy_batch_execute`
- **Read** for analysis purposes (large files, non-edit targets) → block with suggestion to use `capy_execute_file`
- **WebFetch** → block with suggestion to use `capy_fetch_and_index`
- **Grep** with broad patterns → block with suggestion to use `capy_batch_execute`
- **capy_execute**, **capy_execute_file**, **capy_batch_execute** → run security check, block if denied
- Everything else → allow (pass-through)

The interception heuristics should match context-mode's pretooluse logic. Study `context-mode/hooks/pretooluse.mjs` carefully for the exact patterns and thresholds.

### Security integration

For capy's own tools (execute, batch_execute), the pretooluse hook runs security checks:
1. Load security rules (`security.LoadRules(projectDir)`)
2. Extract the command from the tool input
3. Call `security.Check(rules, tool, command)`
4. If denied, return block decision with reason

### Adapter interface

Create `internal/hook/adapter.go`:

```
Adapter interface {
    ParseInput(data []byte) (*HookInput, error)
    FormatOutput(output *HookOutput) ([]byte, error)
    PlatformName() string
}
```

Initially only `ClaudeCodeAdapter` implements this. The interface is in place for future platforms.

**Designed-for (deferred):** When multi-platform adapters are added, `internal/hook/adapter.go` moves to `internal/adapter/adapter.go`, and each platform gets its own implementation file. The hook handler dispatches to the correct adapter based on platform detection.

### Files to create

```
internal/hook/handler.go     — HandleHook dispatch
internal/hook/pretooluse.go  — handlePreToolUse, interception logic
internal/hook/adapter.go     — Adapter interface, ClaudeCodeAdapter
internal/hook/types.go       — HookInput, HookOutput structs
```

---

## CLI — Setup command

### Setup logic

Create `internal/platform/setup.go`:

**`SetupClaudeCode(binaryPath, projectDir string, global bool) error`**

1. **Detect binary path:** If not provided, use `exec.LookPath("capy")`. Error if not found.
2. **Write MCP config:**
   - Read existing `.mcp.json` (project or global `~/.claude/.mcp.json`)
   - Add/update capy entry: `{"mcpServers": {"capy": {"command": "capy", "args": ["serve"]}}}`
   - Write back, preserving other entries
3. **Write hook config:**
   - Read existing `.claude/settings.json` (project or global)
   - Add/update PreToolUse hook entry matching `Bash|Read|Grep|WebFetch|capy_execute|capy_execute_file|capy_batch_execute`
   - Hook command: `capy hook pretooluse`
   - Add stub entries for PostToolUse, PreCompact, SessionStart (for future use)
   - Write back, preserving other entries
4. **Generate routing instructions:**
   - Print a block of text for the user to add to their CLAUDE.md (or offer to write it)
   - Content matches `context-mode/configs/claude-code/CLAUDE.md` but with `capy_` tool names
5. **Create .capy/ directory** if using in-project DB:
   - `mkdir -p .capy/`
   - Write `.capy/.gitignore` with `knowledge.db` entry (opt-out of committing)

Reference: `context-mode/src/cli.ts` — `setup()` command and `context-mode/src/adapters/claude-code/config.ts` — config generation.

### JSON merging

The setup command must merge with existing settings, not overwrite. This means:
- Read existing JSON file into `map[string]interface{}`
- Deep-merge the new entries
- Write back with proper indentation (`json.MarshalIndent`)

This is critical — users have other hooks, MCP servers, and permissions configured. Overwriting would break their setup.

### Doctor command integration

Update `cmd/capy/main.go` to wire the `doctor` subcommand:
1. Load config
2. Run all diagnostic checks (runtimes, FTS5, hooks, MCP, config, knowledge base)
3. Print results in a formatted report

### Cleanup command integration

Update `cmd/capy/main.go` to wire the `cleanup` subcommand:
1. Load config
2. Open ContentStore
3. Run cleanup with flags (max-age-days, dry-run/force)
4. Print results

### Files to create/modify

```
internal/platform/setup.go     — SetupClaudeCode, JSON merging
internal/platform/routing.go   — routing instructions template
internal/platform/doctor.go    — diagnostic checks (non-MCP version)
cmd/capy/main.go               — wire setup, doctor, cleanup commands
```

---

## Integration testing

### End-to-end MCP test

Create a test that:
1. Starts the MCP server in a subprocess
2. Sends JSON-RPC requests for each tool
3. Verifies responses match expected format
4. Cleans up temp DB

Reference: `context-mode/tests/mcp-integration.ts` — integration test structure.

### Benchmark test

Create a benchmark test that:
1. Executes the same scenarios from context-mode's benchmarks
2. Measures context reduction ratios
3. Compares against context-mode's published numbers

Reference: `context-mode/BENCHMARK.md` and `context-mode/tests/live-benchmark.ts`.

### Test organization

```
internal/store/store_test.go         — ContentStore unit tests
internal/store/chunk_test.go         — chunking tests
internal/store/search_test.go        — search algorithm tests
internal/store/levenshtein_test.go   — Levenshtein distance tests
internal/executor/executor_test.go   — executor tests
internal/executor/truncate_test.go   — truncation tests
internal/security/eval_test.go       — security evaluation tests
internal/security/pattern_test.go    — pattern matching tests
internal/hook/pretooluse_test.go     — hook interception tests
internal/server/server_test.go       — MCP server integration tests
internal/config/loader_test.go       — config loading tests
internal/platform/setup_test.go      — setup command tests
```

---

## Addendum: Implementation details from code review

The following sections cover implementation details discovered during a thorough review of the context-mode source. They supplement the sections above and must be incorporated into the relevant tasks.

### Sandbox environment security

Create `internal/executor/env.go`:

**`BuildSafeEnv(workDir string) map[string]string`**

1. Start with current process environment (`os.Environ()`)
2. **Strip dangerous env vars** — maintain a `DENIED` set of ~50 variable names covering: shell (BASH_ENV, ENV, PROMPT_COMMAND, etc.), Node (NODE_OPTIONS, NODE_PATH), Python (PYTHONSTARTUP, PYTHONHOME, etc.), Ruby (RUBYOPT, RUBYLIB), Perl (PERL5OPT, etc.), Elixir/Erlang (ERL_AFLAGS, etc.), Go (GOFLAGS, CGO_CFLAGS, CGO_LDFLAGS), Rust (RUSTC, RUSTFLAGS, etc.), PHP (PHPRC, PHP_INI_SCAN_DIR), R (R_PROFILE, R_PROFILE_USER, R_HOME), dynamic linker (LD_PRELOAD, DYLD_INSERT_LIBRARIES), OpenSSL (OPENSSL_CONF, OPENSSL_ENGINES), compiler (CC, CXX, AR), Git (GIT_TEMPLATE_DIR, GIT_CONFIG_GLOBAL, etc.)
3. Also strip any env var starting with `BASH_FUNC_` (bash exported functions)
4. **Apply sandbox overrides:**
   - `TMPDIR` = workDir
   - `HOME` = real home directory
   - `LANG` = `en_US.UTF-8`
   - `PYTHONDONTWRITEBYTECODE=1`, `PYTHONUNBUFFERED=1`, `PYTHONUTF8=1`
   - `NO_COLOR=1`
5. **SSL cert detection:** If `SSL_CERT_FILE` not set, check common paths: `/etc/ssl/cert.pem`, `/etc/ssl/certs/ca-certificates.crt`, `/etc/pki/tls/certs/ca-bundle.crt`
6. **Ensure PATH exists:** Default to `/usr/local/bin:/usr/bin:/bin` if missing

The complete list of denied vars is in `context-mode/src/executor.ts` — `#buildSafeEnv()`. Port it exactly.

### Shell-escape detection

Create `internal/security/shell_escape.go`:

**`ExtractShellCommands(code string, language string) []string`**

For each supported language, apply regex patterns to extract embedded shell commands:

- Python: `os.system("cmd")`, `subprocess.run("cmd")`, `subprocess.run(["a", "b"])`
- JS/TS: `execSync("cmd")`, `spawn("cmd")`
- Ruby: `system("cmd")`, `` `cmd` ``
- Go: `exec.Command("cmd")`
- PHP: `shell_exec("cmd")`, `exec("cmd")`, `system("cmd")`, `passthru("cmd")`, `proc_open("cmd")`
- Rust: `Command::new("cmd")`

Python's list form `subprocess.run(["rm", "-rf", "/"])` needs special handling — extract array elements and join with spaces.

These extracted commands are then checked against the same Bash deny patterns used for direct shell execution.

Reference: `context-mode/src/security.ts` — `SHELL_ESCAPE_PATTERNS`, `extractShellCommands()`, `extractPythonSubprocessListArgs()`.

### Exit code classification

Create `internal/executor/exit_classify.go`:

**`ClassifyNonZeroExit(language string, exitCode int, stdout, stderr string) (isError bool, output string)`**

- If `language == "shell"` AND `exitCode == 1` AND `strings.TrimSpace(stdout) != ""` → soft fail: `isError = false`, `output = stdout`
- Otherwise → hard fail: `isError = true`, `output = fmt.Sprintf("Exit code: %d\n\nstdout:\n%s\n\nstderr:\n%s", exitCode, stdout, stderr)`

Reference: `context-mode/src/exit-classify.ts`.

### Lifecycle guard

Create `internal/server/lifecycle.go`:

**`StartLifecycleGuard(onShutdown func()) func()`**

1. Record original `os.Getppid()`
2. Start a goroutine that checks every 30 seconds: if `os.Getppid()` changed from original (reparented to init/systemd), call `onShutdown()`
3. Handle OS signals: `SIGTERM`, `SIGINT`, `SIGHUP` → call `onShutdown()`
4. Return a cleanup function that stops the goroutine and removes signal handlers

Reference: `context-mode/src/lifecycle.ts`.

### Hard cap implementation

In `internal/executor/executor.go`, the `Execute` method must implement stream-level byte tracking:

1. Read stdout/stderr through `io.LimitedReader` or manual byte counting
2. If combined bytes exceed `hardCapBytes` (100 MB default), kill the process group immediately
3. Append `[output capped at 100MB — process killed]` to stderr

Reference: `context-mode/src/executor.ts` — `#spawn()` method, `capExceeded` logic.

### Language auto-wrapping

In `internal/executor/executor.go` — the script writing function must handle:

- **Go:** If code doesn't contain `package `, wrap in `package main` with `import "fmt"` and `func main() { ... }`
- **PHP:** If code doesn't start with `<?`, prepend `<?php\n`
- **Elixir:** If `mix.exs` exists in project root, prepend BEAM path loading code
- **Shell:** Set file permissions to `0o700` (executable)

Reference: `context-mode/src/executor.ts` — `#writeScript()`.

### File content wrapping (`execute_file`)

In `internal/executor/executor.go` — implement a `wrapWithFileContent(absolutePath, language, code string) string` function that prepends file-reading boilerplate for each of the 11 languages, providing `FILE_CONTENT_PATH`, `file_path`, and `FILE_CONTENT` variables.

Reference: `context-mode/src/executor.ts` — `#wrapWithFileContent()`. Port the exact boilerplate for each language.

### Three-tier settings in security

Update `internal/security/settings.go` — `LoadRules` must read from **three** files:

1. `.claude/settings.local.json` — project-local
2. `.claude/settings.json` — project-shared
3. `~/.claude/settings.json` — global

Each produces a separate `SecurityPolicy` with `deny`, `allow`, and `ask` arrays. Policies are evaluated in precedence order (most local first).

The server uses `evaluateCommandDenyOnly()` (no "ask" prompting). The hook system uses the full `evaluateCommand()` which includes "ask" support.

### File path deny patterns

Update `internal/security/` — add:

**`LoadToolDenyPatterns(toolName, projectDir string) [][]string`**
- Extract deny globs for a specific tool (e.g., `Read(.env)` → `.env`)
- Returns an array of arrays (one per settings file, in precedence order)

**`EvaluateFilePath(filePath string, denyGlobs [][]string) (denied bool, matchedPattern string)`**
- Normalize backslashes to forward slashes
- Use `fileGlobToRegex` for path-aware matching (`**` matches path segments)

Reference: `context-mode/src/security.ts` — `readToolDenyPatterns()`, `evaluateFilePath()`, `fileGlobToRegex()`.

### Search AND/OR modes and full fallback chain

Update `internal/store/search.go`:

Both `searchPorter` and `searchTrigram` must accept a `mode` parameter (`"AND"` or `"OR"`). In AND mode, terms are quoted and space-joined. In OR mode, terms are joined with `" OR "`.

The `SearchWithFallback` function implements 8 layers:

```
1. Porter + AND
2. Porter + OR
3. Trigram + AND
4. Trigram + OR
5. Fuzzy correction → Porter + AND
6. Fuzzy correction → Porter + OR
7. Fuzzy correction → Trigram + AND
8. Fuzzy correction → Trigram + OR
```

Stop at first layer returning results.

All search functions accept an optional `source` filter (LIKE match on `sources.label`). Separate prepared statements for filtered vs unfiltered.

### Progressive search throttling

In `internal/server/tool_search.go`:

Maintain a call counter and window start time. Per 60-second window:
- Calls 1–3: `effectiveLimit = min(requestedLimit, 2)`
- Calls 4–8: `effectiveLimit = 1`, emit throttle warning
- Calls 9+: return error demanding `batch_execute`

### Smart snippet extraction

Create `internal/server/snippet.go`:

**`ExtractSnippet(content, query string, maxLen int, highlighted string) string`**

1. If content ≤ maxLen, return as-is
2. If `highlighted` is provided, parse FTS5 STX/ETX markers to find match positions
3. Fallback: find positions via `strings.Index` on lowercase query terms
4. Build 300-character windows around each position
5. Merge overlapping windows
6. Collect windows until maxLen budget is reached
7. Add `…` markers for truncated regions

Reference: `context-mode/src/server.ts` — `extractSnippet()`, `positionsFromHighlight()`.

### Distinctive terms

Add to `internal/store/store.go`:

**`GetDistinctiveTerms(sourceID int64, maxTerms int) []string`**

1. Get chunk count for source. If < 3, return empty.
2. Stream chunks, count document frequency per word
3. Filter: 2 ≤ appearances ≤ 40% of chunks
4. Score: `IDF + lengthBonus + identifierBonus`
5. Return top N terms sorted by score

Reference: `context-mode/src/store.ts` — `getDistinctiveTerms()`.

### `batch_execute` implementation corrections

Update `internal/server/tool_batch.go`:

1. Commands always run as **shell** (`language: "shell"`, not configurable)
2. Each command runs with `2>&1` appended (merge stderr into stdout)
3. Each command gets its own `executor.Execute()` call with remaining timeout
4. After indexing, call `store.GetChunksBySource(sourceID)` to build section inventory
5. Search is three-tier: scoped to batch source → global fallback (with cross-source warning)
6. Snippet budget: 3000 bytes (larger than search's 1500)
7. Default timeout: 60 seconds
8. Output cap: 80 KB

### `fetch_and_index` implementation corrections

Update `internal/server/tool_fetch.go`:

In Go, the fetch can be done natively (no subprocess needed):

1. `http.Get(url)` with timeout, redirect limit, User-Agent
2. Read response body, detect Content-Type from header
3. HTML → markdown conversion via Go library
4. JSON → pass through (validate with `json.Valid()`)
5. Everything else → plaintext
6. Route to `store.Index()` (markdown), `store.IndexJSON()` (JSON), or `store.IndexPlainText()` (plaintext)
7. Return 3072-byte preview

Key difference from context-mode: no subprocess needed since Go can do HTTP natively. This means no `__CM_CT__:` marker protocol, no temp file bypass.

### Input coercion

In tool handlers, implement defensive parsing for array inputs:

```go
func coerceStringArray(val interface{}) []string {
    // If val is already []string, return
    // If val is a JSON string, try json.Unmarshal into []string
    // If val is []interface{}, convert elements to strings
}
```

This handles Claude Code's double-serialization bug where `["a","b"]` arrives as `"[\"a\",\"b\"]"`.

### Additional files to create

```
internal/executor/env.go             — BuildSafeEnv (sandbox environment)
internal/executor/exit_classify.go   — ClassifyNonZeroExit
internal/security/shell_escape.go    — ExtractShellCommands
internal/security/file_path.go       — LoadToolDenyPatterns, EvaluateFilePath
internal/server/lifecycle.go         — StartLifecycleGuard
internal/server/snippet.go          — ExtractSnippet, positionsFromHighlight
```
