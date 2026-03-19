# Tasks: Port MCP Core to Go

**Feature:** Port context-mode MCP core to Go
**Design:** [design.md](./design.md)
**Implementation:** [implementation.md](./implementation.md)

---

## Task 1: Project Bootstrap and Build System

**Status:** Not started
**Dependencies:** None
**Estimated complexity:** Low

Set up the Go module, directory structure, dependencies, and build system.

- [ ] Initialize Go module (`github.com/serpro69/capy`)
- [ ] Create full directory skeleton (`cmd/capy/`, `internal/server/`, `internal/store/`, `internal/executor/`, `internal/security/`, `internal/hook/`, `internal/adapter/`, `internal/config/`, `internal/session/`)
- [ ] Add core dependencies: `mcp-go`, `go-sqlite3`, `go-toml/v2`, `cobra`, `testify`
- [ ] Create `Makefile` with targets: `build` (with `CGO_ENABLED=1 -tags fts5`), `test`, `lint`
- [ ] Create `cmd/capy/main.go` with cobra root command and subcommand stubs (`serve`, `hook`, `setup`, `doctor`, `cleanup`)
- [ ] Verify that `capy` (no args) defaults to `serve` behavior (for MCP config `"command": "capy"`)
- [ ] Verify `go build` succeeds with FTS5 tag and produces a single binary
- [ ] Add `.github/workflows/` CI config for build + test (if applicable)

**Reference:** `implementation.md` Section 1

- [ ] **Documentation:** Add `go.mod` info and build instructions to project README. Document CGO requirement and FTS5 build tag.
- [ ] **Testing:** Verify binary builds on CI. Add a smoke test that runs `capy --help` and `capy doctor` (should exit 0 even with no config).

---

## Task 2: Configuration System

**Status:** Not started
**Dependencies:** Task 1
**Estimated complexity:** Medium

Implement the TOML configuration system with three-level precedence and DB path resolution.

- [ ] Define `Config`, `StoreConfig`, `CleanupConfig`, `ExecutorConfig` structs in `internal/config/config.go`
- [ ] Implement `defaults()` function returning sensible defaults (timeout 30s, max output 100KB, cold threshold 30 days, no auto-prune)
- [ ] Implement `Load(projectDir string)` with three-level precedence: `.capy.toml` > `.capy/config.toml` > `$XDG_CONFIG_HOME/capy/config.toml`
- [ ] Implement `ResolveDBPath(projectDir string)` — handles relative paths, absolute paths, and XDG default
- [ ] Implement `projectHash(dir string)` — SHA-256 first 12 hex chars of absolute path
- [ ] Implement project root detection: `CLAUDE_PROJECT_DIR` env → `git rev-parse --show-toplevel` → cwd
- [ ] Handle edge cases: missing files (skip silently), malformed TOML (return error), missing XDG env (default to `~/.local/share/`)

**Reference:** `implementation.md` Section 8, `design.md` Section 8

- [ ] **Documentation:** Document configuration schema in a `docs/configuration.md` file. Include all config keys, defaults, and examples for both `.capy.toml` and `.capy/config.toml`.
- [ ] **Testing:** Unit tests in `internal/config/config_test.go` covering: default loading, single-file loading, three-level precedence merge, DB path resolution (relative, absolute, XDG), project hash determinism, missing files, malformed TOML error.

---

## Task 3: ContentStore — Schema, Indexing, and Vocabulary

**Status:** Not started
**Dependencies:** Task 1, Task 2
**Estimated complexity:** High

Implement the SQLite FTS5 knowledge base: schema creation, all three chunking strategies, vocabulary extraction, and freshness metadata.

- [ ] Implement `ContentStore` struct with lazy initialization (`getDB()` pattern)
- [ ] Implement schema creation (sources, chunks, chunks_trigram, vocabulary tables) with WAL mode and `_busy_timeout=5000`
- [ ] Implement markdown chunking (`chunkMarkdown`):
  - Split on H1-H4 headings, maintain heading stack for breadcrumb titles
  - Preserve code blocks as atomic units (track fence state)
  - Flush on heading, horizontal rule, or EOF
  - Split oversized chunks (>4096 bytes) at paragraph boundaries with numbered suffixes
- [ ] Implement plain text chunking (`chunkPlainText`):
  - Phase 1: blank-line splitting (3-200 sections, each <5000 bytes)
  - Phase 2 fallback: 20-line groups with 2-line overlap
  - Single chunk if <20 lines
- [ ] Implement JSON chunking (`walkJSON`):
  - Recursive object tree walk with key-path titles
  - Small flat objects as single chunks
  - Array batching by byte size with identity field detection (`id`, `name`, `title`, `slug`, `key`, `label`)
  - Fallback to plaintext on parse error
- [ ] Implement `IndexMarkdown(content, source string)`, `IndexPlainText(content, source string)`, `IndexJSON(content, source string)` methods
  - Each returns `IndexResult{SourceID, Label, TotalChunks, CodeChunks}`
  - Insert chunks into both `chunks` and `chunks_trigram` tables
  - Extract and insert vocabulary words
- [ ] Implement freshness metadata:
  - `content_hash` (SHA-256) computed on index
  - Re-index detection: compare hash, skip if unchanged, delete+reindex if changed
  - `indexed_at` set on insert, `last_accessed_at` and `access_count` initialized
- [ ] Implement `Close()` for clean DB shutdown

**Reference:** `context-mode/src/store.ts` — `#chunkMarkdown()`, `#chunkPlainText()`, `#walkJSON()`, `index()`, `indexPlainText()`, `indexJSON()`. `implementation.md` Section 2.

- [ ] **Documentation:** Document the knowledge base schema and chunking behavior in `docs/knowledge-base.md`. Include chunk size limits, heading behavior, and JSON identity field detection.
- [ ] **Testing:** Comprehensive tests in `internal/store/store_test.go` and `internal/store/chunk_test.go`:
  - Markdown chunking: headings, code blocks, oversized chunks, no headings, empty input, horizontal rules
  - Plain text chunking: blank-line split, fixed-line fallback, single chunk
  - JSON chunking: flat objects, nested objects, arrays with identity fields, parse failure fallback
  - Indexing: insert and retrieve, re-index with same content (no-op), re-index with changed content
  - Schema creation: idempotent (run twice without error)
  - Port relevant fixtures from `context-mode/tests/fixtures/`

---

## Task 4: ContentStore — Three-Tier Search

**Status:** Not started
**Dependencies:** Task 3
**Estimated complexity:** High

Implement the full search pipeline: Porter stemming, trigram, fuzzy Levenshtein, smart snippets, distinctive terms, and progressive throttling.

- [ ] Implement `searchPorter(query, limit, source)` — FTS5 MATCH on `chunks` table with BM25 ranking (`bm25(chunks, 2.0, 1.0)`)
- [ ] Implement `searchTrigram(query, limit, source)` — FTS5 MATCH on `chunks_trigram` with trigram sanitization (keep only `[a-zA-Z0-9 _-]`)
- [ ] Implement `SearchWithFallback(query, limit, source)` — three-tier cascade: Porter → trigram → fuzzy correct + re-search
- [ ] Implement `levenshteinDistance(a, b string) int` — standard DP, lowercase comparison
- [ ] Implement `maxEditDistance(wordLen int) int` — 1-4→1, 5-12→2, 13+→3
- [ ] Implement `fuzzyCorrect(query string) string` — correct each word against vocabulary table within edit distance threshold
- [ ] Implement smart snippet extraction:
  - Parse FTS5 highlight markers (char(2)/char(3))
  - Create 300-char windows around each match position
  - Merge overlapping windows
  - Collect up to 1500 bytes
  - Fallback to `strings.Index` when no highlight markers
- [ ] Implement `GetDistinctiveTerms(sourceID, maxTerms)`:
  - IDF scoring: `log(totalChunks / count)`
  - Length bonus for longer words
  - Identifier bonus for underscores/camelCase
  - Filter: 3+ chars, not in stopword list
- [ ] Implement progressive throttling:
  - 60-second sliding window
  - 1-3 calls: max 2 results per query
  - 4-8 calls: max 1 result + warning
  - 9+ calls: blocked with "use batch_execute" error
- [ ] Implement output caps: 40 KB for search, 80 KB for batch_execute
- [ ] Update `last_accessed_at` and increment `access_count` on search hits
- [ ] Implement stopword list (88 words — common English + code/changelog terms, see `context-mode/docs/llms-full.txt` lines 360-364)

**Reference:** `context-mode/src/store.ts` — `searchWithFallback()`, `levenshteinDistance()`, `fuzzyCorrect()`, `getDistinctiveTerms()`. `implementation.md` Section 2.

- [ ] **Documentation:** Document search behavior, fuzzy thresholds, throttling rules, and snippet extraction in `docs/knowledge-base.md` (append to Task 3's doc).
- [ ] **Testing:** Tests in `internal/store/search_test.go`:
  - Porter stemming: "caching" matches "cached", "caches"
  - Trigram: "useEff" finds "useEffect", partial matches
  - Fuzzy: "kuberntes" corrects to "kubernetes", threshold boundaries
  - Snippet extraction: match highlighting, window merging, fallback
  - Distinctive terms: IDF scoring, identifier bonus, stopword filtering
  - Throttling: 3 calls normal, 4-8 reduced, 9+ blocked, window reset
  - Output caps: verify truncation at 40KB/80KB
  - Source filtering: LIKE match on source label
  - Empty queries: returns error
  - No results: returns list of indexed sources
  - Port relevant test cases from `context-mode/tests/core/search.test.ts` and `context-mode/tests/store.test.ts`

---

## Task 5: ContentStore — Cleanup and Lifecycle

**Status:** Not started
**Dependencies:** Task 3
**Estimated complexity:** Low

Implement the tiered freshness lifecycle and cold-source pruning.

- [ ] Implement `Cleanup(maxAgeDays int, dryRun bool) ([]PrunedSource, error)`:
  - Select cold sources: `last_accessed_at < cutoff AND access_count = 0`
  - In dry-run mode: return list without deleting
  - In delete mode: remove chunks from both FTS5 tables, remove source
- [ ] Implement `GetStoreStats() StoreStats` — total sources, total chunks, DB file size, per-tier counts (hot/warm/cold)
- [ ] Ensure cleanup deletes from both `chunks` and `chunks_trigram` tables
- [ ] Ensure vocabulary table is not affected by cleanup (words may be shared across sources)

**Reference:** `design.md` Section 3.8-3.9, `implementation.md` Section 2.9

- [ ] **Documentation:** Document cleanup behavior, tier definitions, and dry-run mode in `docs/knowledge-base.md`.
- [ ] **Testing:** Tests in `internal/store/store_test.go`:
  - Insert sources with varying `last_accessed_at`, verify cleanup selects correctly
  - Dry-run returns candidates without deleting
  - Delete mode removes chunks and sources
  - Recently accessed sources (even old) are preserved
  - Sources with access_count > 0 are preserved

---

## Task 6: PolyglotExecutor — Core Execution

**Status:** Not started
**Dependencies:** Task 1
**Estimated complexity:** High

Implement the polyglot code executor: runtime detection, process spawning, output capture, timeout handling, and process group management.

- [ ] Implement `PolyglotExecutor` struct with lazy runtime detection
- [ ] Implement `detectRuntimes()` using `exec.LookPath` for all 11 languages with fallback chains
- [ ] Implement `Execute(ctx, lang, code, opts)`:
  - Create temp directory, write script file with correct filename/extension
  - Apply auto-wrapping (Go: package main wrap, PHP: `<?php` prepend, Elixir: BEAM path, Rust: compile step)
  - Build command with correct runtime and args
  - Set working directory (project dir for shell, temp dir for others)
  - Set environment via `buildEnv()` with credential passthrough
  - Set process group (`Setpgid: true`)
  - Capture stdout/stderr with hard cap monitoring (100 MB)
  - Handle timeout via `context.WithTimeout` — kill process group on timeout
  - Return `ExecResult{Stdout, Stderr, ExitCode, TimedOut, Killed}`
  - Clean up temp directory
- [ ] Implement `FILE_CONTENT` injection for `execute_file`:
  - Language-specific prepend code for all 11 languages (see `design.md` Section 4.4)
  - Set `FILE_CONTENT_PATH` variable
- [ ] Handle Rust special case: two-step compile (`rustc`) + execute
- [ ] Handle null/empty output: return `"(no output)"`
- [ ] Handle non-zero exit code: combine stdout + stderr in result

**Reference:** `context-mode/src/executor.ts`, `context-mode/src/runtime.ts`. `implementation.md` Section 3.

- [ ] **Documentation:** Document supported languages, runtime fallback chains, auto-wrapping rules, and environment passthrough in `docs/executor.md`.
- [ ] **Testing:** Tests in `internal/executor/executor_test.go`:
  - Successful execution for each language that's available on CI (at minimum: shell, python, node/bun, go)
  - Timeout handling: long-running process killed within timeout
  - Process group cleanup: no orphaned child processes
  - Non-zero exit code: stderr captured
  - Empty output: returns "(no output)"
  - FILE_CONTENT injection: verify variable is accessible in subprocess
  - Auto-wrapping: Go package main, PHP `<?php`, Rust compile+run
  - Environment passthrough: verify key env vars are forwarded
  - Port relevant tests from `context-mode/tests/executor.test.ts`

---

## Task 7: Smart Truncation

**Status:** Not started
**Dependencies:** Task 1
**Estimated complexity:** Low

Implement the smart output truncation algorithm (60% head + 40% tail).

- [ ] Implement `SmartTruncate(output string, maxBytes int) string`:
  - If output ≤ maxBytes, return as-is
  - Split into lines
  - Collect head lines until 60% of byte budget consumed
  - Collect tail lines (from end) until 40% of byte budget consumed
  - Insert separator with truncation stats
  - Snap to line boundaries (never cut mid-line)
- [ ] Implement byte-accurate calculations (`len()` in Go is byte length for strings — this is correct for UTF-8)
- [ ] Handle edge cases: single very long line (no line break to snap to), output exactly at maxBytes

**Reference:** `context-mode/src/truncate.ts` — `smartTruncate()`, `context-mode/src/executor.ts`. `implementation.md` Section 3.6.

- [ ] **Documentation:** Include truncation behavior in `docs/executor.md` (thresholds, ratio, separator format).
- [ ] **Testing:** Tests in `internal/executor/truncate_test.go`:
  - Short output: returned unchanged
  - Long output: correct head/tail split
  - Line boundary snapping
  - Separator message format
  - UTF-8 safety (multi-byte characters not split)
  - Single long line edge case
  - Port relevant tests from `context-mode/tests/` (search for `smartTruncate` or `truncate` tests)

---

## Task 8: Security — Permission Enforcement

**Status:** Not started
**Dependencies:** Task 1
**Estimated complexity:** Medium

Implement the deny/allow permission system reading from Claude Code's settings.json.

- [ ] Implement `LoadSettings(projectDir string)` — load and merge rules from three `.claude/settings*.json` files
- [ ] Implement glob-to-regex conversion:
  - `*` → `[^\s]*` (no whitespace)
  - `**` → `.*` (anything)
  - `?` → `.`
  - Escape regex metacharacters
- [ ] Implement bash pattern parsing:
  - Colon format: `Bash(command:argsGlob)` → `/^command(\s+argsRegex)?$/`
  - Space format: `Bash(command argsGlob)` → `/^command\s+argsRegex$/`
  - Plain glob: `Bash(glob)` → entire pattern converted
- [ ] Implement tool pattern parsing: `ToolName(fileGlob)` → globstar file path matching
- [ ] Implement chained command splitting (`SplitChainedCommands`):
  - Split on `&&`, `||`, `;`, `|`
  - Quote-aware: respect single/double quotes and backticks
  - Return individual command segments
- [ ] Implement shell-escape detection (`ExtractShellCommands`):
  - Regex patterns for Python, JavaScript, TypeScript, Ruby, Go, PHP, Rust
  - Python subprocess list form: `subprocess.run(["rm", "-rf", "/"])` → `"rm -rf /"`
- [ ] Implement `Evaluate(rules, command) PermissionDecision`:
  - Split command, check each segment against deny rules
  - Deny always wins over allow
  - Return `Allow` or `Deny` with reason
- [ ] Implement `EvaluateFilePath(rules, toolName, filePath) PermissionDecision` for Read deny patterns

**Reference:** `context-mode/src/security.ts` (full implementation). `implementation.md` Section 4.

- [ ] **Documentation:** Document security model, pattern syntax, and deny-wins semantics in `docs/security.md`. Include examples matching the capy README Security section.
- [ ] **Testing:** Tests in `internal/security/security_test.go`, `glob_test.go`, `split_test.go`:
  - Glob patterns: `*`, `**`, `?`, escaped metacharacters
  - Bash colon format: `Bash(rm:*)` matches `rm -rf /`
  - Bash space format: `Bash(sudo *)` matches `sudo anything`
  - Deny wins: command matches both deny and allow → denied
  - Chained commands: `echo ok && sudo rm` → second part blocked
  - Quote-aware splitting: `echo "hello && world"` → single command
  - Shell-escape detection: Python `os.system()`, JS `execSync()`, etc.
  - File path patterns: `Read(.env)`, `Read(**/.env*)`
  - Settings merge: project-local overrides global
  - Port relevant tests from `context-mode/tests/security.test.ts`

---

## Task 9: MCP Server — Tool Registration and Core Handlers

**Status:** Not started
**Dependencies:** Task 3, Task 4, Task 6, Task 7, Task 8
**Estimated complexity:** High

Implement the MCP server with all 9 tool handlers, session statistics, and lazy store initialization.

- [ ] Implement `Server` struct with lazy store init, executor, security, config, stats, throttle
- [ ] Register all 9 tools with `mcp-go` tool registration API (JSON Schema for each tool's parameters)
- [ ] Implement `handleExecute` — execute code, apply smart truncation, auto-index with intent when output > 5KB
- [ ] Implement `handleExecuteFile` — FILE_CONTENT injection, security check on file path, execute, auto-index with intent
- [ ] Implement `handleBatchExecute` — sequential command execution, output indexing, multi-query search, section inventory, 80KB output cap
- [ ] Implement `handleIndex` — accept content or path, choose chunking strategy (markdown by default), return IndexResult
- [ ] Implement `handleSearch` — parse queries array, apply throttle, run SearchWithFallback for each query, apply 40KB output cap, include distinctive terms
- [ ] Implement `handleFetchAndIndex` — HTTP GET, content-type routing (HTML→markdown, JSON, plaintext), index, return preview (3072 byte limit)
- [ ] Implement `handleStats` — return per-tool byte/call breakdown, savings ratio, reduction percentage, estimated tokens, knowledge base size
- [ ] Implement `handleDoctor` — check runtimes, FTS5, hooks, config, knowledge base status
- [ ] Implement `handleCleanup` — delegate to store.Cleanup, return pruned sources list
- [ ] Implement `SessionStats` tracking — increment on every tool response via `trackResponse(toolName, responseBytes)`
- [ ] Implement stdio transport startup (JSON-RPC on stdin/stdout via mcp-go)
- [ ] Implement graceful shutdown: SIGTERM/SIGINT → close DB, kill children, exit

**Reference:** `context-mode/src/server.ts` (full implementation), `context-mode/docs/llms-full.txt` lines 26-230. `implementation.md` Section 5.

- [ ] **Documentation:** Document all 9 tools with parameter schemas, return formats, and usage examples in `docs/tools.md`. Model after `context-mode/docs/llms-full.txt` format.
- [ ] **Testing:** Integration tests in `internal/server/server_test.go`:
  - Each tool handler: valid input → expected output format
  - execute with intent: output >5KB triggers auto-index
  - batch_execute: multi-command + multi-query
  - search throttling: verify progressive limits
  - fetch_and_index: mock HTTP server for HTML/JSON/text content types
  - stats: verify counters increment correctly
  - doctor: verify runtime detection output
  - cleanup: verify delegation to store
  - Error cases: missing required params, invalid language, bad URL
  - Port relevant test patterns from `context-mode/tests/core/server.test.ts`

---

## Task 10: Hook System — PreToolUse and Claude Code Adapter

**Status:** Not started
**Dependencies:** Task 8
**Estimated complexity:** Medium

Implement the hook dispatcher, PreToolUse handler with full routing logic, and Claude Code adapter.

- [ ] Implement `HookAdapter` interface in `internal/adapter/adapter.go`:
  - `ParsePreToolUse`, `FormatBlock`, `FormatAllow`, `FormatSubagentRouting`
  - `ParsePostToolUse`, `FormatPostToolUse` (stub methods for future)
  - `ParseSessionStart`, `FormatSessionStart`
  - `Capabilities() PlatformCapabilities`
- [ ] Implement `ClaudeCodeAdapter` in `internal/adapter/claudecode.go`:
  - JSON parsing of Claude Code hook input format
  - `FormatBlock` → `{"permissionDecision": "deny", "reason": "..."}`
  - `FormatAllow` → `{"additionalContext": "..."}`
  - `FormatSubagentRouting` → `{"updatedInput": {"prompt": "...routing block..."}}`
  - `FormatSessionStart` → `{"additionalContext": "...routing block..."}`
- [ ] Implement hook dispatcher in `internal/hook/hook.go`:
  - Read JSON from stdin
  - Route to handler based on event name
  - Write JSON to stdout
- [ ] Implement `handlePreToolUse` in `internal/hook/pretooluse.go`:
  - Bash curl/wget detection → block with redirect to `capy_fetch_and_index`
  - Bash inline HTTP detection (fetch(), requests.get(), http.get()) → block with redirect to `capy_execute`
  - Bash other → security check against deny rules
  - WebFetch → block with redirect to `capy_fetch_and_index`
  - Read → pass through with guidance (use `capy_execute_file` for analysis)
  - Grep → pass through with guidance (use `capy_execute` for searches)
  - Agent/Task → inject routing block into subagent prompt
  - capy_execute/execute_file/batch_execute → security checks only
- [ ] Implement routing block XML string (see `design.md` Section 7.3)
- [ ] Implement stub handlers for posttooluse, precompact, sessionstart (pass-through or routing injection only), userpromptsubmit
- [ ] Wire `capy hook <event>` cobra subcommand to dispatcher

**Reference:** `context-mode/hooks/pretooluse.mjs`, `context-mode/hooks/core/routing.mjs`, `context-mode/src/adapters/claude-code/`. `implementation.md` Sections 6-7.

**Designed-for but deferred:** The adapter interface is designed to support Gemini CLI, VS Code Copilot, Cursor, OpenCode, and other platforms. Only the Claude Code adapter is implemented now. See `context-mode/src/adapters/` and `context-mode/docs/platform-support.md` for the full platform matrix.

- [ ] **Documentation:** Document hook system, routing decisions, and Claude Code integration in `docs/hooks.md`. Include the full routing table and JSON format examples.
- [ ] **Testing:** Tests in `internal/hook/pretooluse_test.go` and `internal/adapter/claudecode_test.go`:
  - Routing: curl command → blocked with redirect
  - Routing: wget command → blocked with redirect
  - Routing: inline HTTP in bash → blocked
  - Routing: normal bash command → pass through
  - Routing: WebFetch → blocked
  - Routing: Read → pass through with guidance
  - Routing: Grep → pass through with guidance
  - Routing: Agent/Task → routing block injected
  - Routing: capy_execute with denied command → blocked
  - Security: denied bash command → blocked with reason
  - Adapter: parse Claude Code JSON input correctly
  - Adapter: format block/allow/routing responses correctly
  - Parse error → pass through (don't block)
  - Port relevant tests from `context-mode/tests/hooks/core-routing.test.ts`

---

## Task 11: Setup Command

**Status:** Not started
**Dependencies:** Task 10
**Estimated complexity:** Medium

Implement `capy setup` for auto-configuring Claude Code integration.

- [ ] Implement `capy setup` cobra subcommand with `--binary` flag and future `--platform` flag
- [ ] Implement Claude Code setup:
  - Detect `capy` binary location (from `$PATH` or `--binary` flag)
  - Create `.claude/` directory if it doesn't exist
  - Merge hooks into `.claude/settings.json` (read existing, add capy hook entries, write back)
  - Merge MCP server into `.mcp.json` (or project-level MCP config)
  - Append routing instructions to `CLAUDE.md` (check for existing `<context_window_protection>` block, skip if present)
  - Add `.capy/` to `.gitignore` (create or append)
- [ ] Implement idempotent merge logic:
  - JSON settings: check if capy hook entry already exists before adding
  - MCP config: check if capy server already registered
  - CLAUDE.md: check if routing block already present
  - `.gitignore`: check if `.capy/` line already exists
- [ ] Print summary of changes made (files created/modified)

**Reference:** `context-mode/src/cli.ts` (setup command logic), `context-mode/configs/claude-code/` (template files). `implementation.md` Section 9.

**Designed-for but deferred:** `--platform` flag for Gemini CLI, VS Code Copilot, Cursor, OpenCode, Codex CLI, etc. Each platform would have its own setup logic (different config paths, hook formats, routing instruction files). See `context-mode/configs/` for per-platform template files.

- [ ] **Documentation:** Document `capy setup` usage and what it configures in `docs/getting-started.md`. Include before/after examples of each modified file.
- [ ] **Testing:** Tests in `internal/config/` or a dedicated setup test file:
  - Fresh setup: all files created from scratch
  - Idempotent: running twice produces identical results
  - Existing settings.json: capy entries merged without overwriting existing hooks
  - Existing CLAUDE.md: routing block appended without duplicating
  - Existing .gitignore: `.capy/` not duplicated
  - Binary detection: `--binary` flag overrides PATH lookup

---

## Task 12: Doctor Command

**Status:** Not started
**Dependencies:** Task 6, Task 3, Task 10
**Estimated complexity:** Low

Implement `capy doctor` CLI command and `capy_doctor` MCP tool for installation diagnostics.

- [ ] Implement diagnostic checks:
  - Runtime availability for all 11 languages (reuse executor's `detectRuntimes`)
  - FTS5 availability (try creating a virtual table in `:memory:` DB)
  - Hook registration in `.claude/settings.json` (check for capy entries)
  - Config file discovery (list which config files were found)
  - Knowledge base status: path, file size, source count, chunk count
  - Binary version and location
- [ ] Format output as a checklist (OK / WARN / FAIL for each check)
- [ ] Wire to both `capy doctor` CLI subcommand and `capy_doctor` MCP tool handler
- [ ] MCP tool returns structured text; CLI command uses colored terminal output (if tty)

**Reference:** `context-mode/src/cli.ts` (doctor command), `context-mode/skills/ctx-doctor/SKILL.md`.

- [ ] **Documentation:** Document diagnostic output format and what each check means in `docs/getting-started.md`.
- [ ] **Testing:** Tests that verify doctor runs without error in a minimal environment. Verify each check produces expected output format. Test with missing runtimes, missing config, missing DB.

---

## Task 13: HTML-to-Markdown Conversion

**Status:** Not started
**Dependencies:** Task 1
**Estimated complexity:** Medium

Implement or integrate HTML-to-Markdown conversion for `capy_fetch_and_index`.

- [ ] Evaluate Go HTML-to-Markdown libraries:
  - `github.com/JohannesKaufmann/html-to-markdown` (v2) — most popular, GFM support
  - Alternative: custom implementation using `golang.org/x/net/html` parser
- [ ] Implement `htmlToMarkdown(html string) string`:
  - Strip `<script>`, `<style>`, `<nav>`, `<header>`, `<footer>` elements before conversion
  - Support GFM tables and strikethrough
  - Preserve code blocks
- [ ] Handle edge cases: malformed HTML, empty response, binary content detection
- [ ] Integrate into `handleFetchAndIndex` in server

**Reference:** `context-mode/docs/llms-full.txt` lines 170-183 (Turndown + domino + GFM plugin behavior).

- [ ] **Documentation:** Document content-type routing and HTML conversion behavior in `docs/tools.md` (under `capy_fetch_and_index`).
- [ ] **Testing:** Test with sample HTML pages: basic article, GFM table, code blocks, script/style stripping, malformed HTML graceful handling. Use `httptest.NewServer` for integration tests.

---

## Task 14: End-to-End Integration Testing

**Status:** Not started
**Dependencies:** Task 9, Task 10, Task 11
**Estimated complexity:** Medium

Comprehensive integration tests that exercise the full pipeline: hook → MCP server → store → executor → search.

- [ ] MCP server integration tests:
  - Start server, send `capy_execute` request, verify response format
  - `capy_execute` with intent + large output → verify auto-indexing + search result
  - `capy_batch_execute` with multiple commands + queries → verify inventory + results
  - `capy_index` + `capy_search` round-trip → verify indexed content is searchable
  - `capy_fetch_and_index` + `capy_search` → verify URL content is indexed and searchable
  - `capy_stats` after several tool calls → verify counters
  - `capy_cleanup` → verify cold source pruning
- [ ] Hook integration tests:
  - Pipe real Claude Code hook JSON → verify correct routing decision
  - Bash curl command → blocked with capy_fetch_and_index suggestion
  - Normal bash command → passed through
  - Read tool → guidance appended
  - Subagent → routing block injected
- [ ] Full pipeline test:
  - Setup → hook intercepts → MCP tool handles → store indexes → search retrieves
- [ ] Performance test: index a large document (~100 KB), search, verify response time < 1 second

**Reference:** `context-mode/tests/hooks/integration.test.ts`, `context-mode/tests/mcp-integration.ts`, `context-mode/BENCHMARK.md`.

- [ ] **Documentation:** Add a `BENCHMARK.md` with performance numbers once tests pass. Model after `context-mode/BENCHMARK.md`.
- [ ] **Testing:** All tests in this task ARE the tests. Ensure they cover the critical paths identified in context-mode's benchmark scenarios: Playwright snapshot (large HTML), GitHub issues (JSON), access logs (plain text), git log (structured text).

---

## Task 15: User-Facing Documentation

**Status:** Not started
**Dependencies:** Task 9, Task 10, Task 11, Task 12
**Estimated complexity:** Medium

Create comprehensive user documentation achieving parity with context-mode's docs.

- [ ] Create `docs/llms.txt` — short overview document (model after `context-mode/docs/llms.txt`)
- [ ] Create `docs/llms-full.txt` — comprehensive technical reference (model after `context-mode/docs/llms-full.txt`):
  - Architecture overview
  - All 9 MCP tools with full parameter docs
  - Knowledge base schema, BM25, chunking, search, fuzzy details
  - Execution engine: languages, runtimes, auto-wrapping, truncation, environment
  - Security model: patterns, splitting, shell-escape detection
  - Hook system: routing table, JSON formats
  - Configuration: schema, precedence, path resolution
  - Session statistics
  - Edge cases and constraints
- [ ] Update project `README.md`:
  - Installation instructions (`go install`, binary download, `capy setup`)
  - Quick start with example prompts
  - Tool table with context savings
  - Security section
  - Configuration section
  - Benchmarks (link to `BENCHMARK.md`)
- [ ] Create `CONTRIBUTING.md` with development workflow, test instructions, project structure
- [ ] Verify documentation covers all features implemented in Tasks 1-14

**Reference:** `context-mode/docs/llms-full.txt`, `context-mode/docs/llms.txt`, `context-mode/README.md`, `context-mode/CONTRIBUTING.md`.

- [ ] **Documentation:** This task IS documentation.
- [ ] **Testing:** Review all docs for accuracy against implemented code. Verify all code examples compile/run. Verify all tool parameter docs match actual JSON schemas.
