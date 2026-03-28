# Tasks: Port MCP Core to Go

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> Status: pending
> Created: 2026-03-20

## Task 1: Project bootstrap and build system

- **Status:** done
- **Depends on:** —
- **Docs:** [implementation.md#1-project-bootstrap](./implementation.md#1-project-bootstrap)

### Subtasks
- [x] 1.1 Initialize Go module (`github.com/serpro69/capy`), Go 1.23+
- [x] 1.2 Create full directory skeleton: `cmd/capy/`, `internal/server/`, `internal/store/`, `internal/executor/`, `internal/security/`, `internal/hook/`, `internal/adapter/`, `internal/config/`, `internal/session/`
- [x] 1.3 Add dependencies: `mcp-go`, `go-sqlite3`, `go-toml/v2`, `cobra`, `testify`
- [x] 1.4 Create `Makefile` with targets: `build` (CGO_ENABLED=1, -tags fts5), `test`, `vet`, `clean`
- [x] 1.5 Create `cmd/capy/main.go` with cobra root command (default: serve) and subcommand stubs: `serve`, `hook` (positional arg for event), `setup`, `doctor`, `cleanup`
- [x] 1.6 Create `internal/version/version.go` with `Version` variable, wire `--version` flag
- [x] 1.7 Verify: `make build` produces binary, `./capy --version` works, all subcommands run without panic, `make vet` and `make test` pass
- [x] 1.8 Write tests: CLI flag parsing, subcommand routing, version output

## Task 2: Configuration system

- **Status:** done
- **Depends on:** Task 1
- **Docs:** [implementation.md#2-configuration-system](./implementation.md#2-configuration-system)

### Subtasks
- [x] 2.1 Create `internal/config/config.go` with `Config`, `StoreConfig`, `CleanupConfig`, `ExecutorConfig`, `ServerConfig` structs and `DefaultConfig()` function (timeout in seconds: 30, max_output_bytes: 102400, cold_threshold_days: 30, auto_prune: false, log_level: "info")
- [x] 2.2 Create `internal/config/loader.go` with `Load(projectDir string) (*Config, error)` — three-level precedence merge: XDG → `.capy/config.toml` → `.capy.toml`
- [x] 2.3 Create `internal/config/paths.go` with `DetectProjectRoot()` (CLAUDE_PROJECT_DIR → git root → cwd), `ProjectHash(dir string) string` (SHA-256 first 16 hex chars), `ResolveDBPath(projectDir string) string`
- [x] 2.4 Write tests: config defaults, single-file loading, three-level precedence merge, project root detection, project hash determinism, DB path resolution (relative, absolute, XDG default), missing files, malformed TOML error

## Task 3: ContentStore — schema, indexing, and vocabulary

- **Status:** done
- **Depends on:** Task 1, Task 2
- **Docs:** [implementation.md#3-contentstore-implementation](./implementation.md#3-contentstore-implementation)

### Subtasks
- [x] 3.1 Create `internal/store/store.go` — `ContentStore` struct with lazy initialization (`getDB()` pattern), `NewContentStore(dbPath, projectDir)`, `Close()` (finalize statements, WAL checkpoint, close DB)
- [x] 3.2 Create `internal/store/schema.go` — `initSchema()` with WAL/NORMAL/busy_timeout/foreign_keys pragmas and all CREATE TABLE/VIRTUAL TABLE statements (sources with content_type/last_accessed_at/access_count/content_hash, chunks FTS5 Porter, chunks_trigram FTS5 trigram, vocabulary)
- [x] 3.3 Create `internal/store/types.go` — `SearchResult`, `SourceInfo`, `StoreStats`, `IndexResult`, `Chunk` structs
- [x] 3.4 Create `internal/store/stopwords.go` — port the 88-word STOPWORDS set from `context-mode/src/store.ts`, expose as `IsStopword(word string) bool`
- [x] 3.5 Create `internal/store/detect.go` — `DetectContentType(content string) string` returning "markdown", "json", or "plaintext"
- [x] 3.6 Create `internal/store/chunk.go` — implement `chunkMarkdown(content string, maxBytes int) []Chunk`: split by H1-H4 headings, preserve code blocks (track fence state), heading hierarchy as breadcrumb titles, paragraph-boundary fallback for oversized sections, code block detection for content_type
- [x] 3.7 Implement `chunkPlainText(content string, linesPerChunk int) []Chunk` in `chunk.go` — two-phase: blank-line splitting (3-200 sections, each <5000 bytes) → fixed 20-line groups with 2-line overlap. Single chunk titled "Output" if small
- [x] 3.8 Implement `walkJSON` + `chunkJSONArray` + `findIdentityField` in `chunk.go` — recursive walk with key-path titles, small flat objects as single chunks, nested objects always recurse, array batching with identity field detection (`id`, `name`, `title`, `path`, `slug`, `key`, `label`). Fallback to plaintext on parse error
- [x] 3.9 Create `internal/store/index.go` — `Index(content, label, contentType string) (*IndexResult, error)` with dedup (content_hash comparison), auto-detect content type, chunk, insert into both FTS5 tables (in transaction), vocabulary extraction. Also `IndexPlainText()` and `IndexJSON()` entry points
- [x] 3.10 Create `internal/store/vocabulary.go` — `extractAndStoreVocabulary(content string)` splitting on `[^\p{L}\p{N}_-]+`, filter 3+ chars and stopwords, INSERT OR IGNORE
- [x] 3.11 Write tests: schema idempotency, markdown chunking (headings, code blocks, oversized, no headings, horizontal rules), plaintext chunking (blank-line split, fixed-line fallback, single chunk), JSON chunking (flat, nested, arrays with identity fields, parse failure), indexing (insert, dedup same hash, re-index changed hash), vocabulary extraction, content type detection

## Task 4: ContentStore — three-tier search

- **Status:** done
- **Depends on:** Task 3
- **Docs:** [implementation.md#34-search](./implementation.md#34-search)

### Subtasks
- [x] 4.1 Create `internal/store/search.go` — `search(query string, limit int, source string, mode string) []SearchResult` using FTS5 MATCH + `bm25(chunks, 2.0, 1.0)` + `highlight(chunks, 1, char(2), char(3))`. Support AND/OR modes. Separate statements for filtered vs unfiltered
- [x] 4.2 Implement `searchTrigram(query string, limit int, source string, mode string) []SearchResult` against `chunks_trigram` table. Sanitize trigram queries: keep only `[a-zA-Z0-9 _-]`, minimum 3-char words
- [x] 4.3 Implement `levenshteinDistance(a, b string) int` (standard DP, lowercase), `maxEditDistance(wordLen int) int` (1/2/3 thresholds), `fuzzyCorrect(query string) string` (query vocabulary, find closest within threshold)
- [x] 4.4 Implement `SearchWithFallback(query string, limit int, source string) []SearchResult` — 8-layer fallback: Porter+AND → Porter+OR → Trigram+AND → Trigram+OR → Fuzzy(Porter+AND → Porter+OR → Trigram+AND → Trigram+OR). Stop at first layer returning results. Tag each result with `MatchLayer`
- [x] 4.5 Implement `sanitizeQuery(query string, mode string) string` — remove quotes/brackets/FTS5 special chars, split, filter stopwords, quote each word, join with mode separator
- [x] 4.6 Implement access tracking: on search hit, update `last_accessed_at` and increment `access_count` on matching sources (background goroutine)
- [x] 4.7 Implement `GetDistinctiveTerms(sourceID int64, maxTerms int) []string` — stream chunks, count doc frequency, filter by appearance range, IDF + lengthBonus + identifierBonus scoring
- [x] 4.8 Implement `GetChunksBySource(sourceID int64) []SearchResult` and `ListSources() []SourceInfo` — direct queries bypassing FTS5 MATCH
- [x] 4.9 Write tests: Porter stemming matches, trigram partial matches, fuzzy correction with typos, 8-layer fallback (verify lower layers fire), source filtering, access count incrementing, query sanitization, Levenshtein correctness, distinctive terms IDF scoring, empty query error, no results returns source list

## Task 5: ContentStore — cleanup and lifecycle

- **Status:** done
- **Depends on:** Task 3
- **Docs:** [implementation.md#39-cleanup](./implementation.md#39-cleanup)

### Subtasks
- [x] 5.1 Create `internal/store/cleanup.go` — `ClassifySources() ([]SourceInfo, error)` classifying as hot/warm/cold based on `last_accessed_at` and config thresholds
- [x] 5.2 Implement `Cleanup(maxAgeDays int, dryRun bool) ([]SourceInfo, error)` — find cold sources with access_count = 0, delete chunks from both FTS5 tables + source row (vocabulary is shared, don't delete)
- [x] 5.3 Implement `Stats() (*StoreStats, error)` — source/chunk/vocab counts, DB file size, tier distribution
- [x] 5.4 Write tests: classify sources with different ages, cleanup dry-run vs force, stats accuracy, recently-accessed sources preserved, sources with access_count > 0 preserved

## Task 6: PolyglotExecutor

- **Status:** done
- **Depends on:** Task 1
- **Docs:** [implementation.md#4-polyglotexecutor-implementation](./implementation.md#4-polyglotexecutor-implementation)

### Subtasks
- [x] 6.1 Create `internal/executor/runtime.go` — `Language` type, `runtimeCandidates` map, `detectRuntimes()` using `exec.LookPath()` with preference order, `buildCommand()` for each language. Rust special case: `__rust_compile_run__`
- [x] 6.2 Create `internal/executor/types.go` — `ExecRequest`, `ExecResult` (Stdout, Stderr, ExitCode, TimedOut, Killed, Backgrounded, PID)
- [x] 6.3 Create `internal/executor/wrap.go` — `autoWrap(lang, code, projectDir)` for Go/PHP/Elixir wrapping, `injectFileContent(lang, code, absPath)` for all 11 languages
- [x] 6.4 Create `internal/executor/env.go` — `BuildSafeEnv(tmpDir string) []string` with denylist of ~50 dangerous env vars (port exact set from `context-mode/src/executor.ts` lines 325-394), sandbox overrides (TMPDIR, HOME, LANG, PYTHON*, NO_COLOR), SSL cert detection, PATH default
- [x] 6.5 Create `internal/executor/executor.go` — `PolyglotExecutor` struct, `NewExecutor()`, `Execute(ctx, req)`: temp dir, script writing (correct extension, 0o700 for shell), process group isolation (`Setpgid: true`), stdout/stderr capture with hard cap monitoring (100 MB → kill process group), timeout via context with process group kill, smart truncation, temp dir cleanup
- [x] 6.6 Implement `ExecuteFile(ctx, req)` — resolve absolute path, inject file content via `injectFileContent()`, delegate to `Execute()`
- [x] 6.7 Implement Rust compile+run: `rustc src.rs -o bin` then execute binary
- [x] 6.8 Create `internal/executor/truncate.go` — `SmartTruncate(output string, maxBytes int) string` with 60/40 head/tail split, line-boundary snapping, truncation stats in separator
- [x] 6.9 Create `internal/executor/exit_classify.go` — `ClassifyNonZeroExit(language, exitCode, stdout, stderr)` with shell soft-fail for exit 1 with stdout
- [x] 6.10 Implement background mode: detach process on timeout, record PID, return partial output. `CleanupBackgrounded()` kills all tracked PIDs
- [x] 6.11 Write tests: runtime detection, smart truncation (under/over threshold, UTF-8 safety, 60/40 split), safe env (dangerous vars stripped, overrides applied), exit classification (soft fail for grep, hard fail for others), execution of bash and python (at minimum), timeout behavior, hard cap, background mode, file content injection for 3+ languages, auto-wrapping for Go/PHP

## Task 7: Security

- **Status:** done
- **Depends on:** Task 1
- **Docs:** [implementation.md#5-security-implementation](./implementation.md#5-security-implementation)

### Subtasks
- [x] 7.1 Create `internal/security/settings.go` — `ReadBashPolicies(projectDir string) []SecurityPolicy` reading from 3 files: `.claude/settings.local.json` → `.claude/settings.json` → `~/.claude/settings.json`. `ReadToolDenyPatterns(toolName, projectDir string) [][]string`
- [x] 7.2 Create `internal/security/glob.go` — `globToRegex(glob string) *regexp.Regexp` (colon syntax: `git:*` → match `git` alone or with args; plain glob: `*` → `.*`), `fileGlobToRegex(glob string) *regexp.Regexp` (`**` matches path segments, `*` matches non-separator), `parseBashPattern()`, `parseToolPattern()`
- [x] 7.3 Create `internal/security/split.go` — `SplitChainedCommands(command string) []string` splitting on `&&`, `||`, `;`, `|` while respecting single/double quotes and backticks
- [x] 7.4 Create `internal/security/shell_escape.go` — `ExtractShellCommands(code, language string) []string` with regex patterns for Python, JS/TS, Ruby, Go, PHP, Rust. Include Python subprocess list-form extraction
- [x] 7.5 Create `internal/security/eval.go` — `EvaluateCommandDenyOnly()` (server-side, deny or allow), `EvaluateCommand()` (hook-side, deny > ask > allow), `EvaluateFilePath()` (normalize backslashes, file glob matching)
- [x] 7.6 Write tests: exact deny match, glob patterns (`*`, `**`, `?`), colon syntax, file path globs (`.env`, `**/.env*`), chained command splitting (with quoted strings and backticks), deny-wins-over-allow, three-tier settings precedence, shell-escape detection for Python/JS/Ruby/Go/PHP/Rust (including Python list form), file path evaluation with backslash normalization

## Task 8: MCP Server — core setup and tool registration

- **Status:** done
- **Depends on:** Task 2, Task 3, Task 6, Task 7
- **Docs:** [implementation.md#6-mcp-server-implementation](./implementation.md#6-mcp-server-implementation)

### Subtasks
- [x] 8.1 Create `internal/server/server.go` — `Server` struct with store (nil until lazy-init), `storeMu sync.Once`, executor, security policies, config, stats, throttle, projectDir. Constructor `NewServer()`. `getStore()` with `sync.Once`
- [x] 8.2 Create `internal/server/stats.go` — `SessionStats` struct with mutex-protected Calls, BytesReturned, BytesIndexed, BytesSandboxed, and increment methods. `TrackResponse()` computes byte size of response content
- [x] 8.3 Create `internal/server/tools.go` — register all 9 tools with `mcp-go` including JSON Schema input definitions for each tool
- [x] 8.4 Create `internal/server/snippet.go` — `ExtractSnippet(content, query string, maxLen int, highlighted string) string` with FTS5 highlight marker parsing (STX/ETX chars 2/3), 300-char window merging, fallback to `strings.Index`. `positionsFromHighlight()` function
- [x] 8.5 Create `internal/server/lifecycle.go` — `StartLifecycleGuard(onShutdown func()) func()` with ppid polling (30s), SIGTERM/SIGINT/SIGHUP handling (stdin close handled by mcp-go stdio transport)
- [x] 8.6 Implement `Serve(ctx context.Context) error` — create mcp-go server with stdio transport, register tools, start lifecycle guard, block. Wire into `cmd/capy/main.go` serve subcommand. Add unhandled panic recovery
- [x] 8.7 Write tests: server construction, lazy store initialization, session stats thread-safety, lifecycle guard (mock ppid change), snippet extraction (highlight markers, no markers, overlapping windows), tool registration count

## Task 9: MCP Tools — execution tools

- **Status:** done
- **Depends on:** Task 8
- **Docs:** [implementation.md#63-tool-handlers](./implementation.md#63-tool-handlers)

### Subtasks
- [x] 9.1 Create `internal/server/tool_execute.go` — `capy_execute` handler: parse inputs, security check (shell: EvaluateCommandDenyOnly, non-shell: ExtractShellCommands + check each), execute, classify non-zero exit codes, intent-driven search flow (if intent AND output > 5000 bytes: index as plaintext, search, return titles + previews + distinctive terms), stats tracking. Include input coercion for double-serialized arrays
- [x] 9.2 Create `internal/server/tool_execute_file.go` — `capy_execute_file` handler: check file path against Read deny patterns, check code against Bash/shell-escape patterns, call ExecuteFile(), same intent search and exit classification as execute
- [x] 9.3 Create `internal/server/tool_batch.go` — `capy_batch_execute` handler: coerce inputs (string→object, double-serialized arrays), security check each command, execute each **separately** as shell with `2>&1` (own truncation budget, remaining timeout), index combined output as markdown, build section inventory via GetChunksBySource(), three-tier search fallback (scoped → global with cross-source warning), 3000-byte snippets, 80 KB output cap, distinctive terms. Default timeout 60s
- [x] 9.4 Write tests: successful execution, security denial (bash deny, shell-escape in Python, file path deny), auto-indexing trigger (output > 5KB + intent), intent search returns titles not full content, exit code classification, batch with multiple commands + search, batch timeout (remaining commands skipped), input coercion, stats tracking

## Task 10: MCP Tools — knowledge tools

- **Status:** done
- **Depends on:** Task 8
- **Docs:** [implementation.md#63-tool-handlers](./implementation.md#63-tool-handlers)

### Subtasks
- [x] 10.1 Create `internal/server/tool_index.go` — `capy_index` handler: parse inputs (content, path, source), call store.Index(), return source ID and chunk count
- [x] 10.2 Create `internal/server/tool_search.go` — `capy_search` handler: accept queries array or query string (with input coercion), progressive throttling (1-3: max 2, 4-8: max 1 + warning, 9+: blocked), SearchWithFallback per query, smart snippets (1500 bytes), 40 KB total output cap, include source listing when no results
- [x] 10.3 Create `internal/server/tool_fetch.go` — `capy_fetch_and_index` handler: native Go `net/http` fetch with timeout/redirect limit/User-Agent, content-type routing (HTML → markdown via `JohannesKaufmann/html-to-markdown` stripping script/style/nav/header/footer, JSON → IndexJSON, text → IndexPlainText), 3072-byte preview
- [x] 10.4 Write tests: index with explicit/auto content type, search with multi-tier results and throttling, fetch_and_index with mock HTTP server (HTML→markdown, JSON, text)

## Task 11: MCP Tools — utility tools

- **Status:** done
- **Depends on:** Task 8
- **Docs:** [implementation.md#63-tool-handlers](./implementation.md#63-tool-handlers)

### Subtasks
- [x] 11.1 Create `internal/server/tool_stats.go` — `capy_stats` handler: session stats (per-tool bytes/calls, uptime), savings calculation (keptOut, ratio, reductionPct, estimatedTokens), knowledge base stats if store initialized (tier distribution). Format as markdown table
- [x] 11.2 Create `internal/server/tool_doctor.go` — `capy_doctor` handler: check version, available runtimes, FTS5, config, knowledge base status, security policies. Format as pass/warn/fail report. Note: hook/MCP registration checks deferred to Task 14 (requires setup system)
- [x] 11.3 Create `internal/server/tool_cleanup.go` — `capy_cleanup` handler: parse inputs (max_age_days, dry_run defaults true), call store.Cleanup(), return pruned/would-be-pruned sources
- [x] 11.4 Write tests: stats with empty/populated store, doctor output format, cleanup dry-run vs force

## Task 12: Hook system — PreToolUse

- **Status:** done
- **Depends on:** Task 7, Task 8
- **Docs:** [implementation.md#7-hook-implementation](./implementation.md#7-hook-implementation)

### Subtasks
- [x] 12.1 Create `internal/hook/hook.go` — `Run(event string, adapter HookAdapter, policies []SecurityPolicy, projectDir string) error` dispatcher: read stdin JSON, route to handler, write stdout JSON
- [x] 12.2 Create `internal/hook/routing.go` — `RoutingBlock()` returning XML routing instructions, `READ_GUIDANCE`, `GREP_GUIDANCE`, `BASH_GUIDANCE` constants
- [x] 12.3 Create `internal/hook/guidance.go` — `guidanceOnce(guidanceType string, content string, adapter HookAdapter)` throttle: in-memory set, show each advisory at most once per session
- [x] 12.4 Create `internal/hook/helpers.go` — `stripQuotedContent()` (heredocs + single/double quoted strings), `stripHeredocs()`, `isCurlOrWget()`, `hasInlineHTTP()`, `isBuildTool()` (gradle, maven), `isCapyTool()`
- [x] 12.5 Create `internal/hook/pretooluse.go` — full routing logic: Bash (security check first, then curl/wget → modify echo, inline HTTP → modify echo, build tools → modify echo, other → guidance once), Read (guidance once), Grep (guidance once), WebFetch (deny), Agent/Task (inject routing block, upgrade Bash subagent to general-purpose), capy tools (security checks)
- [x] 12.6 Create stub handlers: `posttooluse.go`, `precompact.go`, `sessionstart.go` (routing instructions only), `userpromptsubmit.go`
- [x] 12.7 Wire `capy hook <event>` subcommand in `cmd/capy/main.go` — load config, load security rules, create adapter, call `Run()`
- [x] 12.8 Write tests: curl/wget blocked, inline HTTP blocked, build tools blocked, WebFetch denied, Read/Grep guidance (once per session), Agent routing block injected, capy_execute security check, parse error → pass through, full stdin→stdout JSON round-trip

## Task 13: Claude Code adapter

- **Status:** done
- **Depends on:** Task 1
- **Docs:** [implementation.md#8-claude-code-adapter](./implementation.md#8-claude-code-adapter)

### Subtasks
- [x] 13.1 Create `internal/adapter/adapter.go` — `HookAdapter` interface (`ParsePreToolUse`, `FormatBlock`, `FormatAllow`, `FormatModify`, `FormatAsk`, `FormatSessionStart`, `Capabilities`), `PreToolUseEvent`, `PlatformCapabilities` types
- [x] 13.2 Create `internal/adapter/claudecode.go` — `ClaudeCodeAdapter` implementing all methods: parse `tool_name`/`tool_input`/`session_id`/`transcript_path` from JSON, format responses with `hookSpecificOutput` wrapper and correct field names (`permissionDecision`, `permissionDecisionReason`, `additionalContext`, `updatedInput`). Session ID extraction: transcript_path UUID > session_id > CLAUDE_SESSION_ID env > ppid fallback
- [x] 13.3 Write tests: parse Claude Code JSON input, format block/allow/modify/ask/sessionstart responses, session ID extraction from various sources

## Task 14: Setup command

- **Status:** pending
- **Depends on:** Task 12, Task 13
- **Docs:** [implementation.md#9-setup-command-implementation](./implementation.md#9-setup-command-implementation)

### Subtasks
- [ ] 14.1 Create `internal/platform/setup.go` — `SetupClaudeCode(binaryPath, projectDir string) error`: binary detection via `exec.LookPath`, MCP config merging (`.mcp.json`), hook config merging (`.claude/settings.json`), `.capy/` directory creation
- [ ] 14.2 Implement idempotent JSON merging — read existing, deep-merge, write back with `json.MarshalIndent`. Preserve existing hooks, MCP servers, permissions
- [ ] 14.3 Create `internal/platform/routing.go` — `GenerateRoutingInstructions() string` with capy tool names. Append to `CLAUDE.md` checking for existing `<context_window_protection>` block
- [ ] 14.4 Create `internal/platform/doctor.go` — diagnostic checks reusable by both CLI and MCP tool
- [ ] 14.5 Wire `setup`, `doctor`, `cleanup` subcommands in `cmd/capy/main.go` with flags: `--platform` (default "claude-code"), `--binary` (optional), `--max-age-days`, `--dry-run`/`--force`
- [ ] 14.6 Ensure `.capy/` added to `.gitignore` (create or append, check for existing entry)
- [ ] 14.7 Write tests: setup creates correct files, merges with existing settings without data loss, idempotent (run twice = same result), routing instructions contain capy tool names, doctor detects present/missing runtimes

## Task 15: HTML-to-Markdown conversion

- **Status:** pending
- **Depends on:** Task 1
- **Docs:** [implementation.md#63-tool-handlers](./implementation.md#63-tool-handlers)

### Subtasks
- [ ] 15.1 Evaluate and integrate `github.com/JohannesKaufmann/html-to-markdown` (v2) — must support GFM tables and element stripping
- [ ] 15.2 Implement `htmlToMarkdown(html string) string`: strip `<script>`, `<style>`, `<nav>`, `<header>`, `<footer>`, `<noscript>` elements before conversion, preserve code blocks, GFM tables
- [ ] 15.3 Handle edge cases: malformed HTML, empty response, binary content detection
- [ ] 15.4 Write tests: basic HTML article, GFM tables, code blocks, script/style stripping, malformed HTML graceful handling (use `httptest.NewServer`)

## Task 16: End-to-end integration testing

- **Status:** pending
- **Depends on:** Task 9, Task 10, Task 11, Task 12, Task 14
- **Docs:** —

### Subtasks
- [ ] 16.1 MCP server integration tests: start server, send capy_execute request, verify response format. Execute with intent + large output → verify auto-index + search. batch_execute with multiple commands + queries. index + search round-trip. fetch_and_index + search. stats counters. cleanup
- [ ] 16.2 Hook integration tests: pipe real Claude Code hook JSON through `capy hook pretooluse`, verify correct routing decisions. Bash curl → blocked. Normal bash → pass through. WebFetch → denied. Read → guidance. Agent → routing block injected
- [ ] 16.3 Full pipeline test: setup → hook intercepts → MCP tool handles → store indexes → search retrieves
- [ ] 16.4 Performance test: index a ~100 KB document, search, verify response time < 1 second

## Task 17: Final verification

- **Status:** pending
- **Depends on:** Task 1–16

### Subtasks
- [ ] 17.1 Run `testing-process` skill — full test suite (`make test`), verify all tests pass, check coverage, identify gaps
- [ ] 17.2 Run `documentation-process` skill — ensure README covers: installation, build from source, configuration, CLI commands, MCP tools, security rules, hook system
- [ ] 17.3 Run `solid-code-review` skill — review all code for SOLID violations, security issues, idiomatic Go patterns, error handling, resource leaks
