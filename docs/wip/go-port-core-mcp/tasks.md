# Tasks: Go Port of context-mode

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> Status: pending
> Created: 2026-03-18

## Task 1: Project scaffolding and CLI framework
- **Status:** pending
- **Depends on:** —
- **Docs:** [implementation.md#project-scaffolding](./implementation.md#project-scaffolding)

### Subtasks
- [ ] 1.1 Initialize Go module (`go.mod`) with module path, Go 1.23+, add dependencies: `spf13/cobra`, `mattn/go-sqlite3`, `pelletier/go-toml/v2`, `mark3labs/mcp-go`
- [ ] 1.2 Create `cmd/capy/main.go` with cobra root command and subcommands: `serve`, `hook` (with positional arg for event type), `setup`, `doctor`, `cleanup` — all stubs that print a message and exit
- [ ] 1.3 Create `internal/version/version.go` with `Version` variable, wire `--version` flag on root command
- [ ] 1.4 Create the full `internal/` directory tree with package-level `doc.go` files: `server/`, `store/`, `executor/`, `security/`, `hook/`, `config/`, `platform/`
- [ ] 1.5 Add a `Makefile` with targets: `build` (with `-tags fts5` and CGO_ENABLED=1), `test`, `vet`, `clean`
- [ ] 1.6 Verify: `make build` produces binary, `./capy --version` works, all subcommands run without panic, `make vet` and `make test` pass
- [ ] 1.7 Write tests: verify CLI flag parsing, subcommand routing, version output
- [ ] 1.8 Update project `README.md` with build instructions and Go prerequisites (CGO, FTS5 build tag)

## Task 2: Configuration system
- **Status:** pending
- **Depends on:** Task 1
- **Docs:** [implementation.md#configuration-system](./implementation.md#configuration-system)

### Subtasks
- [ ] 2.1 Create `internal/config/config.go` with `Config` struct matching [design.md § Configuration](./design.md#8-configuration-system) — nested structs for `Store`, `Store.Cleanup`, `Executor`, `Server` sections, with default values via `DefaultConfig()` function
- [ ] 2.2 Create `internal/config/project.go` with `DetectProjectRoot(startDir string) string` (walk up looking for `.git/`, `.capy.toml`, `.capy/`) and `ProjectHash(projectDir string) string` (SHA-256 first 16 chars)
- [ ] 2.3 Create `internal/config/loader.go` with `Load(projectDir string) (*Config, error)` — load and merge from all three levels (XDG → `.capy/config.toml` → `.capy.toml`), non-zero values from higher priority override lower
- [ ] 2.4 Implement `ResolveStorePath(projectDir string) string` on Config — resolves relative paths against project root, computes XDG default path using project hash when unconfigured
- [ ] 2.5 Write tests: config defaults, single-level loading, three-level merge precedence, project root detection (with temp dirs containing `.git/`), project hash determinism, store path resolution for both configured and default cases
- [ ] 2.6 Document config file format and precedence in project `README.md` or `docs/configuration.md`

## Task 3: SQLite foundation and ContentStore schema
- **Status:** pending
- **Depends on:** Task 1, Task 2
- **Docs:** [implementation.md#sqlite-foundation-and-contentstore-schema](./implementation.md#sqlite-foundation-and-contentstore-schema)

### Subtasks
- [ ] 3.1 Create `internal/store/db.go` — `openDB(dbPath string) (*sql.DB, error)` function that opens SQLite, applies WAL/NORMAL/busy_timeout/foreign_keys pragmas, returns configured `*sql.DB`
- [ ] 3.2 Create `internal/store/schema.go` — `initSchema(db *sql.DB) error` that creates `sources`, `chunks` (FTS5 Porter), `chunks_trigram` (FTS5 trigram), `vocabulary` tables using `IF NOT EXISTS`
- [ ] 3.3 Create `internal/store/store.go` — `ContentStore` struct with `db *sql.DB`, `stmts` (prepared statements struct), `projectDir`, `dbPath` fields. Constructor `NewContentStore(dbPath, projectDir string)` opens DB, inits schema, prepares all statements. `Close()` finalizes statements, runs WAL checkpoint, closes DB.
- [ ] 3.4 Create `internal/store/types.go` — `SearchResult`, `SourceInfo`, `StoreStats`, `Chunk` structs as defined in implementation.md
- [ ] 3.5 Write tests: DB opens with correct pragmas (query `PRAGMA journal_mode` etc.), schema creation is idempotent (call twice without error), NewContentStore + Close lifecycle, FTS5 tables are functional (insert + MATCH query works on both Porter and trigram tables)
- [ ] 3.6 Document SQLite/CGO/FTS5 build requirements in `README.md` (CGO_ENABLED=1, `-tags fts5`)

## Task 4: ContentStore — Chunking and indexing
- **Status:** pending
- **Depends on:** Task 3
- **Docs:** [implementation.md#contentstore--chunking-and-indexing](./implementation.md#contentstore--chunking-and-indexing)

### Subtasks
- [ ] 4.1 Create `internal/store/stopwords.go` — port the `STOPWORDS` set from `context-mode/src/store.ts`, expose as `IsStopword(word string) bool`
- [ ] 4.2 Create `internal/store/chunk.go` — implement `chunkMarkdown(content string, maxBytes int) []Chunk` following `context-mode/src/store.ts` `chunkMarkdown()`: split by headings, preserve code blocks, heading hierarchy as titles, paragraph-boundary fallback for oversized sections
- [ ] 4.3 Implement `chunkJSON(content string, maxBytes int) []Chunk` in `chunk.go` — parse into `interface{}`, walk tree recursively, dot-notation key paths as titles, batch array items
- [ ] 4.4 Implement `chunkPlaintext(content string, maxBytes int, linesPerChunk int) []Chunk` in `chunk.go` — fixed-size line groups with 2-line overlap, `"Lines N-M"` titles
- [ ] 4.5 Create `internal/store/detect.go` — `DetectContentType(content string) string` returning `"markdown"`, `"json"`, or `"plaintext"`
- [ ] 4.6 Implement `Index(content, label, contentType string) (int64, int, error)` on ContentStore — content hash dedup, re-index on hash change, auto-detect content type, chunk, insert source + chunks into both FTS5 tables (in transaction), extract and upsert vocabulary
- [ ] 4.7 Write tests for each chunker: markdown with headings/code blocks/oversized sections, JSON with nested objects/arrays, plaintext with overlap. Test Index(): new content, duplicate content (dedup), changed content (re-index), content type auto-detection, vocabulary extraction. Reference: `context-mode/tests/store.test.ts` for test scenarios
- [ ] 4.8 Document the chunking strategies and MAX_CHUNK_BYTES constant in design docs or inline code comments

## Task 5: ContentStore — Three-tier search
- **Status:** pending
- **Depends on:** Task 4
- **Docs:** [implementation.md#contentstore--three-tier-search](./implementation.md#contentstore--three-tier-search)

### Subtasks
- [ ] 5.1 Create `internal/store/sanitize.go` — `sanitizeQuery(query string) string` that removes quotes/brackets/FTS5 special chars, splits on whitespace, filters stopwords, rejoins. Port from `context-mode/src/store.ts` `sanitizeQuery()`
- [ ] 5.2 Create `internal/store/levenshtein.go` — `levenshteinDistance(a, b string) int` standard DP implementation. Port from `context-mode/src/store.ts`
- [ ] 5.3 Create `internal/store/search.go` — implement `searchPorter(query string, limit int) ([]SearchResult, error)` using FTS5 MATCH + `bm25(chunks, 2.0, 1.0)` ranking
- [ ] 5.4 Implement `searchTrigram(query string, limit int) ([]SearchResult, error)` in `search.go` using the `chunks_trigram` table
- [ ] 5.5 Implement `searchFuzzy(query string, limit int) ([]SearchResult, error)` in `search.go` — query vocabulary for Levenshtein-close words (adaptive max distance: 1 for ≤4 chars, 2 for ≤12, 3 for >12), build corrected query, re-search via Porter
- [ ] 5.6 Implement `Search(queries []string, limit int) ([]SearchResult, error)` on ContentStore — sequential tier fallback, deduplication across tiers, freshness boost (join with sources table for `last_accessed_at` and `access_count`), sort by final score
- [ ] 5.7 Implement access tracking: when Search returns results, update `last_accessed_at` and increment `access_count` on matching sources (in background goroutine)
- [ ] 5.8 Write tests: Porter search with stemming matches, trigram search with partial matches, fuzzy search with typo correction, three-tier fallback (ensure tier 2 fires when tier 1 returns nothing), freshness boost ordering, access count incrementing, query sanitization, Levenshtein distance correctness. Reference: `context-mode/tests/store.test.ts` for search test scenarios
- [ ] 5.9 Document the search algorithm and freshness boost formula in design docs or inline comments

## Task 6: ContentStore — Tiered lifecycle and cleanup
- **Status:** pending
- **Depends on:** Task 5
- **Docs:** [implementation.md#contentstore--tiered-lifecycle-and-cleanup](./implementation.md#contentstore--tiered-lifecycle-and-cleanup)

### Subtasks
- [ ] 6.1 Implement `ClassifySources() ([]SourceInfo, error)` on ContentStore — query all sources, classify as hot/warm/cold based on `last_accessed_at` and `cold_threshold_days` config
- [ ] 6.2 Implement `Cleanup(maxAgeDays int, dryRun bool) ([]SourceInfo, error)` on ContentStore — find cold sources, delete chunks from both FTS5 tables + vocabulary + source row (in transaction) when not dry-run
- [ ] 6.3 Implement `Stats() (*StoreStats, error)` on ContentStore — source/chunk/vocab counts, DB file size, tier distribution, oldest/newest timestamps
- [ ] 6.4 Write tests: classify sources with different ages into correct tiers, cleanup dry-run returns list without deleting, cleanup force actually removes sources and chunks, stats reflect current DB state accurately
- [ ] 6.5 Document cleanup behavior and tier thresholds in `README.md` or config docs

## Task 7: PolyglotExecutor
- **Status:** pending
- **Depends on:** Task 1
- **Docs:** [implementation.md#polyglotexecutor](./implementation.md#polyglotexecutor)

### Subtasks
- [ ] 7.1 Create `internal/executor/types.go` — `ExecRequest`, `ExecResult`, `ExecutorConfig` structs as defined in implementation.md
- [ ] 7.2 Create `internal/executor/runtime.go` — `DetectRuntimes() map[string]string` using `exec.LookPath()` with preference order per language (bun > node for JS, python3 > python, etc.), cached via `sync.Once`. Port language configs from `context-mode/src/executor.ts` `LANGUAGE_CONFIGS`
- [ ] 7.3 Create `internal/executor/truncate.go` — `SmartTruncate(output string, maxBytes int) string` implementing 60/40 head/tail split with `[N lines / M KB truncated]` annotation. Port from `context-mode/src/truncate.ts`
- [ ] 7.4 Create `internal/executor/executor.go` — `Executor` struct and `NewExecutor(projectDir string, cfg ExecutorConfig)`. Implement `Execute(ctx context.Context, req ExecRequest) (*ExecResult, error)`: temp dir creation, script file writing with correct extension, `exec.CommandContext` with `SysProcAttr{Setpgid: true}`, stdout/stderr capture, timeout via context, process group kill on timeout, smart truncation, temp dir cleanup
- [ ] 7.5 Implement language-specific command construction in executor — correct binary + args for each of the 11 languages (reference: `context-mode/src/executor.ts` `LANGUAGE_CONFIGS`). Working directory: project dir for shell languages, temp dir for others
- [ ] 7.6 Implement background mode in executor — when `req.Background` is true, start process detached, record PID, return immediately without waiting
- [ ] 7.7 Write tests: runtime detection (mock `exec.LookPath` or test with actually available runtimes), smart truncation (under threshold, over threshold, exact boundary, UTF-8 safety, 60/40 split verification), execution of at least bash and python (available on CI), timeout behavior, background mode returns PID without blocking. Reference: `context-mode/tests/executor.test.ts`
- [ ] 7.8 Document supported languages and runtime detection behavior in `README.md`

## Task 8: Security
- **Status:** pending
- **Depends on:** Task 1
- **Docs:** [implementation.md#security](./implementation.md#security)

### Subtasks
- [ ] 8.1 Create `internal/security/types.go` — `Rules` struct (`Deny []string`, `Allow []string`), `Decision` type (enum: `Allow`, `Deny`, `Suspicious`)
- [ ] 8.2 Create `internal/security/settings.go` — `LoadRules(projectDir string) (*Rules, error)` that reads `.claude/settings.json` (project) and `~/.claude/settings.json` (global), parses `permissions.deny` and `permissions.allow` arrays, merges with project-level precedence
- [ ] 8.3 Create `internal/security/pattern.go` — `matchPattern(pattern, input string) bool` supporting glob (`*`, `**`, `?`), colon syntax (`git:*` → `git *`), and `Tool(pattern)` wrapper extraction. Implement `splitChainedCommands(command string) []string` splitting on `&&`, `||`, `;`, `|` while respecting quoted strings
- [ ] 8.4 Create `internal/security/eval.go` — `Check(rules *Rules, tool string, input string) Decision` implementing the evaluation order: deny check (any match → Deny), allow check (any match → Allow), default Allow. For Bash tool: split chained commands, check each part independently
- [ ] 8.5 Implement `checkNearMiss(rules *Rules, tool, input string) bool` in `eval.go` — Levenshtein distance ≤ 2 against deny patterns flags as Suspicious
- [ ] 8.6 Write tests: exact deny match, glob patterns (`*`, `**`, `?`), colon syntax, `Tool(pattern)` format, chained command splitting (including quoted strings), deny-wins-over-allow, project-overrides-global, near-miss detection, empty rules = allow all. Reference: `context-mode/tests/security.test.ts` for comprehensive test cases
- [ ] 8.7 Document security rule format and examples in `README.md` (mirror context-mode's README security section)

## Task 9: MCP Server — Core setup and tool registration
- **Status:** pending
- **Depends on:** Task 2, Task 3, Task 7, Task 8
- **Docs:** [implementation.md#mcp-server--core-setup-and-tool-registration](./implementation.md#mcp-server--core-setup-and-tool-registration)

### Subtasks
- [ ] 9.1 Create `internal/server/server.go` — `Server` struct with fields for store (nil until lazy-init), `storeOnce sync.Once`, executor, security rules, config, stats, projectDir. Constructor `NewServer(cfg *config.Config, projectDir string) (*Server, error)` initializes executor and security, leaves store nil
- [ ] 9.2 Implement `getStore() (*store.ContentStore, error)` on Server — `sync.Once`-guarded lazy initialization using config to resolve DB path
- [ ] 9.3 Create `internal/server/stats.go` — `SessionStats` struct with mutex-protected `BytesReturned`, `BytesSandboxed`, `CallCounts` map, and increment methods
- [ ] 9.4 Create `internal/server/tools.go` — register all 9 tools with `mcp-go` including JSON Schema input definitions for each tool. Reference: `context-mode/src/server.ts` tool registration for schema definitions
- [ ] 9.5 Implement `Serve(ctx context.Context) error` on Server — create `mcp-go` server with stdio transport, register tools, start serving (blocks). Wire into `cmd/capy/main.go` `serve` subcommand
- [ ] 9.6 Write tests: server construction, lazy store initialization (verify store is nil before first tool call, initialized after), session stats thread-safety (concurrent increments), tool registration (all 9 tools registered with correct schemas). For integration: start server subprocess, send a simple tool call via JSON-RPC, verify response format
- [ ] 9.7 Document MCP server usage in `README.md` (how to add to `.mcp.json`, how to test manually)

## Task 10: MCP Tools — Execution tools
- **Status:** pending
- **Depends on:** Task 9
- **Docs:** [implementation.md#mcp-tools--execution-tools](./implementation.md#mcp-tools--execution-tools)

### Subtasks
- [ ] 10.1 Create `internal/server/tool_execute.go` — `capy_execute` handler: parse inputs, security check, call executor, auto-index if output exceeds threshold and intent provided, format MCP response, update stats
- [ ] 10.2 Create `internal/server/tool_execute_file.go` — `capy_execute_file` handler: parse inputs, security check on file path, inject file path into execution environment, call executor, auto-index logic, format response, update stats
- [ ] 10.3 Create `internal/server/tool_batch.go` — `capy_batch_execute` handler: parse inputs (commands array + optional queries), security check each command, execute sequentially, auto-index ALL outputs, search if queries provided, return combined results, update stats
- [ ] 10.4 Write tests for each handler: successful execution, security denial, auto-indexing trigger (output > threshold + intent), auto-indexing skip (small output), batch with multiple commands + search queries, stats increment verification. Reference: `context-mode/src/server.ts` execute/batch handlers for edge cases
- [ ] 10.5 Document execution tools in `README.md` — input/output format, auto-indexing behavior, security enforcement

## Task 11: MCP Tools — Knowledge tools
- **Status:** pending
- **Depends on:** Task 9
- **Docs:** [implementation.md#mcp-tools--knowledge-tools](./implementation.md#mcp-tools--knowledge-tools)

### Subtasks
- [ ] 11.1 Create `internal/server/tool_index.go` — `capy_index` handler: parse inputs (content, label, optional content_type), call `store.Index()`, return source ID and chunk count
- [ ] 11.2 Create `internal/server/tool_search.go` — `capy_search` handler: parse inputs (queries array, optional limit), call `store.Search()`, format results with title/content/source/rank/matchTier
- [ ] 11.3 Create `internal/server/tool_fetch.go` — `capy_fetch_and_index` handler: parse inputs (url, optional label), fetch URL via `net/http`, detect content type, convert HTML to markdown (evaluate and integrate `JohannesKaufmann/html-to-markdown` or similar), extract page title for default label, call `store.Index()`, return confirmation
- [ ] 11.4 Write tests: index with explicit content type, index with auto-detection, search returning multi-tier results, fetch_and_index with mocked HTTP server (test HTML→markdown conversion, JSON passthrough, plaintext). Reference: `context-mode/src/server.ts` for handler behavior
- [ ] 11.5 Document knowledge tools in `README.md` — usage examples, supported content types, fetch behavior

## Task 12: MCP Tools — Utility tools
- **Status:** pending
- **Depends on:** Task 9
- **Docs:** [implementation.md#mcp-tools--utility-tools](./implementation.md#mcp-tools--utility-tools)

### Subtasks
- [ ] 12.1 Create `internal/server/tool_stats.go` — `capy_stats` handler: collect session stats + knowledge base stats (if store initialized), format human-readable report with tier distribution
- [ ] 12.2 Create `internal/server/tool_doctor.go` — `capy_doctor` handler: check version, available runtimes, FTS5 availability, config resolution, knowledge base status, hook registration, MCP registration. Format as pass/warn/fail report
- [ ] 12.3 Create `internal/server/tool_cleanup.go` — `capy_cleanup` handler: parse inputs (optional max_age_days, dry_run defaults true), call `store.Cleanup()`, return list of pruned/would-be-pruned sources
- [ ] 12.4 Write tests: stats with empty store, stats after indexing content, doctor with mock filesystem (missing hooks, present hooks), cleanup dry-run vs force. Reference: `context-mode/src/server.ts` for stats/doctor output format
- [ ] 12.5 Document utility tools in `README.md`

## Task 13: Hook system — PreToolUse
- **Status:** pending
- **Depends on:** Task 8, Task 9
- **Docs:** [implementation.md#hook-system--pretooluse](./implementation.md#hook-system--pretooluse)

### Subtasks
- [ ] 13.1 Create `internal/hook/types.go` — `HookInput`, `HookOutput` structs matching Claude Code's hook JSON protocol (see implementation.md for format)
- [ ] 13.2 Create `internal/hook/adapter.go` — `Adapter` interface (`ParseInput`, `FormatOutput`, `PlatformName`) and `ClaudeCodeAdapter` implementation that handles Claude Code's JSON format
- [ ] 13.3 Create `internal/hook/handler.go` — `HandleHook(event string, stdin io.Reader, stdout io.Writer) error` that reads stdin, dispatches to event-specific handler, writes stdout. Stub handlers for `posttooluse`, `precompact`, `sessionstart` (pass-through)
- [ ] 13.4 Create `internal/hook/pretooluse.go` — `handlePreToolUse(input *HookInput, rules *security.Rules) *HookOutput` with full interception logic: Bash → suggest capy_execute/capy_batch_execute, Read (analysis) → suggest capy_execute_file, WebFetch → suggest capy_fetch_and_index, Grep (broad) → suggest capy_batch_execute, capy tools → security check. Port heuristics from `context-mode/hooks/pretooluse.mjs`
- [ ] 13.5 Wire `capy hook` subcommand in `cmd/capy/main.go` — load config, load security rules, call `HandleHook()` with stdin/stdout
- [ ] 13.6 Write tests: pretooluse blocks Bash with large output suggestion, pretooluse allows small Bash commands, pretooluse blocks WebFetch with fetch_and_index suggestion, pretooluse runs security check on capy tools, posttooluse/precompact/sessionstart pass through, full stdin→stdout round-trip with JSON serialization. Reference: `context-mode/hooks/pretooluse.mjs` for interception patterns
- [ ] 13.7 Document hook system in `README.md` — how hooks work, which events are handled, how to verify hooks are working

## Task 14: CLI — Setup command
- **Status:** pending
- **Depends on:** Task 2, Task 13
- **Docs:** [implementation.md#cli--setup-command](./implementation.md#cli--setup-command)

### Subtasks
- [ ] 14.1 Create `internal/platform/setup.go` — `SetupClaudeCode(binaryPath, projectDir string, global bool) error` implementing: binary detection via `exec.LookPath`, MCP config writing (`.mcp.json`), hook config writing (`.claude/settings.json`), `.capy/` directory creation with `.gitignore`
- [ ] 14.2 Implement idempotent JSON merging in `setup.go` — read existing JSON into `map[string]interface{}`, deep-merge new entries, write back with `json.MarshalIndent`. Must preserve existing hooks, MCP servers, and permissions
- [ ] 14.3 Create `internal/platform/routing.go` — routing instructions template (port and adapt from `context-mode/configs/claude-code/CLAUDE.md` with `capy_` tool names). `GenerateRoutingInstructions() string` returns the text block
- [ ] 14.4 Create `internal/platform/doctor.go` — non-MCP version of diagnostic checks, reusable by both `capy doctor` CLI and `capy_doctor` MCP tool
- [ ] 14.5 Wire `setup` subcommand in `cmd/capy/main.go` with flags: `--platform` (default "claude-code"), `--binary` (optional), `--global` (bool). Wire `doctor` and `cleanup` subcommands with their respective flags
- [ ] 14.6 Write tests: setup creates correct `.mcp.json` entry, setup merges with existing settings without data loss, setup is idempotent (run twice = same result), routing instructions contain all capy tool names, doctor detects present/missing runtimes. Use temp directories for all file operations
- [ ] 14.7 Document `capy setup` usage and what it configures in `README.md`

## Task 15: Final verification
- **Status:** pending
- **Depends on:** Task 1–14
- **Docs:** —

### Subtasks
- [ ] 15.1 Run `testing-process` skill — full test suite (`make test`), verify all tests pass, check coverage, identify any gaps
- [ ] 15.2 Run `documentation-process` skill — ensure `README.md` covers: installation, build from source, configuration, all CLI commands, all MCP tools, security rules, hook system, comparison with context-mode. Ensure documentation parity with `context-mode/README.md`
- [ ] 15.3 Run `solid-code-review` skill with Go input — review all code for SOLID violations, security issues, idiomatic Go patterns, error handling, resource leaks (unclosed DBs, temp dirs)
