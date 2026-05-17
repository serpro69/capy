# Contributing to capy

## Local Setup

### Prerequisites

- **Go 1.25+** ([install](https://go.dev/dl/))
- **C compiler** — required for CGO (SQLite FTS5 + sqlite3mc encryption):
  - macOS: `xcode-select --install`
  - Debian/Ubuntu: `sudo apt install build-essential`
  - Fedora: `sudo dnf install gcc`
  - No external SQLite library needed — the build uses a `go.mod` replace directive to bundle sqlite3mc (SQLite3MultipleCiphers) via the jgiannuzzi/go-sqlite3 fork
- **Language runtimes** (optional) — for testing the executor against specific languages:
  - At minimum: `bash`, `python3`
  - Full set: `node`/`bun`, `tsx`/`ts-node`, `ruby`, `go`, `rustc`, `php`, `perl`, `Rscript`, `elixir`

### Clone and Build

```bash
git clone https://github.com/serpro69/capy.git
cd capy
make build
```

This produces a `./capy` binary. The `-tags fts5` build tag is handled by the Makefile.

### Verify

Tests require the `CAPY_DB_KEY` environment variable (any non-empty string works for tests):

```bash
export CAPY_DB_KEY=test-key-for-development

make test       # run all tests
make vet        # static analysis
make test-race  # tests with race detector
```

## Project Structure

```
cmd/capy/           CLI entry points (serve, hook, setup, doctor, cleanup, checkpoint, encrypt, sweep, which, dbsize)
internal/
  adapter/          Platform adapter interface + Claude Code implementation
  config/           TOML config loading, project root detection, path resolution
  executor/         Polyglot code executor (11 languages, process isolation)
  giturl/           Git platform URL detection (shared by hook and server)
  hook/             Hook event routing (PreToolUse, PostToolUse, SessionStart, etc.)
  platform/         Setup command, doctor diagnostics, routing instructions
  sanitize/         Secret stripping (regex-based redaction for indexed content)
  security/         Settings parsing, glob matching, command splitting, shell-escape detection
  server/           MCP server, 9 tool handlers, stats, lifecycle, snippets, intent search
  session/          Claude Code session JSONL parsing, transcript building, chunking, sweep indexing
  store/            SQLite FTS5 knowledge base (schema, indexing, chunking, search, cleanup, encryption, migration)
  version/          Version variable (set at build via ldflags)
docs/
  adr/              Architecture Decision Records (numbered, append-only)
  done/             Design docs for completed features (design.md, implementation.md, tasks.md)
  wip/              Design docs for in-progress features
  architecture.md   System architecture overview
```

For the full architecture, see [docs/architecture.md](docs/architecture.md).

## Testing

### Running Tests

All tests require:

1. The `fts5` build tag (handled by the Makefile)
2. The `CAPY_DB_KEY` environment variable set to any non-empty string

```bash
export CAPY_DB_KEY=test-key-for-development       # set once per shell session

make test                                         # all tests
make test-race                                    # with race detector
go test -tags fts5 -count=1 ./internal/store/...  # single package
go test -tags fts5 -count=1 -run TestSearch ./internal/store/...  # single test
go test -tags fts5 -count=1 -v ./internal/hook/...  # verbose
```

Top gotchas:
- Without `-tags fts5` you'll get cryptic `no such module: fts5` errors from SQLite.
- Without `CAPY_DB_KEY` you'll get `CAPY_DB_KEY environment variable is required` errors from store tests.

### Coverage

```bash
CGO_ENABLED=1 go test -tags fts5 -coverprofile=cover.out ./...
go tool cover -func=cover.out | tail -1           # total percentage
go tool cover -html=cover.out -o cover.html       # visual report
```

### Test Organization

Each package has two levels of tests:

- **Unit tests** (`*_test.go`) — test individual functions in isolation. Use test stubs/mocks. Fast.
- **Integration tests** (`integration_test.go`) — test the full pipeline across packages. Use real adapters, real SQLite, real executor. Found in `internal/server/` and `internal/hook/`.

Key integration test files:

| File | What it covers |
|------|----------------|
| `server/integration_test.go` | MCP tool handler → executor → store → search round-trips |
| `hook/integration_test.go` | Full hook JSON through Claude Code adapter with real routing |

## Local Verification Without Installation

You don't need to install capy globally to test it. Build and run from the repo root.

### Testing Hooks Manually

Build the binary and pipe JSON to stdin:

```bash
make build

# Test: curl gets intercepted
echo '{"tool_name":"Bash","tool_input":{"command":"curl https://example.com"},"session_id":"test"}' \
  | ./capy hook pretooluse | python3 -m json.tool

# Expected: permissionDecision "allow" with updatedInput replacing curl with echo message

# Test: WebFetch gets denied
echo '{"tool_name":"WebFetch","tool_input":{"url":"https://example.com"},"session_id":"test"}' \
  | ./capy hook pretooluse | python3 -m json.tool

# Expected: permissionDecision "deny"

# Test: normal bash gets guidance
echo '{"tool_name":"Bash","tool_input":{"command":"ls -la"},"session_id":"test"}' \
  | ./capy hook pretooluse | python3 -m json.tool

# Expected: additionalContext with capy guidance (or empty if guidance was already shown this session)

# Test: security deny
echo '{"tool_name":"Bash","tool_input":{"command":"sudo rm -rf /"},"session_id":"test"}' \
  | ./capy hook pretooluse --project-dir . | python3 -m json.tool

# Expected: permissionDecision "deny" (if you have a Bash(sudo *) deny rule in .claude/settings.json)
```

### Testing Doctor

```bash
./capy doctor
./capy doctor --project-dir /path/to/some/project
```

### Testing With Claude Code (Full E2E)

```bash
# Create a scratch project
mkdir /tmp/capy-test && cd /tmp/capy-test
git init

# Setup using your local binary
/path/to/capy/capy setup --binary /path/to/capy/capy

# Verify
/path/to/capy/capy doctor

# Now open this project in Claude Code — hooks and MCP tools should be active
```

To iterate: rebuild with `make build`, then restart Claude Code (the MCP server and hooks pick up the new binary automatically since the config points to the absolute path).

### Testing the MCP Server Directly

The MCP server uses stdio (JSON-RPC over stdin/stdout). The integration tests cover this path:

```bash
go test -tags fts5 -count=1 -v -run TestIntegration ./internal/server/...
```

## Development Workflow

### Making Changes

1. Find the relevant package in `internal/`
2. Read existing tests to understand the patterns
3. Write or modify code
4. Run the package tests: `go test -tags fts5 -count=1 ./internal/<package>/...`
5. Run `make vet` for static analysis
6. Run `make test` for the full suite
7. If you changed hook behavior, test manually with piped JSON (see above)

### Common Patterns

**Tool handlers** (`server/tool_*.go`): Each MCP tool has a handler function that:
1. Parses arguments from the request (with input coercion for double-serialized JSON)
2. Runs security checks (deny policies, file path checks, shell-escape detection)
3. Does the work (execute, index, search, etc.)
4. Tracks stats via `s.trackToolResponse()`
5. Returns `*mcp.CallToolResult`

**Store operations**: All database operations use prepared statements cached on the `ContentStore` struct. New queries need a prepared statement added to `store.go:prepareStatements()` and closed in `store.go:Close()`.

**Write transactions**: Use `beginImmediate()` (no-op DELETE after BEGIN) to acquire SQLite's RESERVED write lock immediately, preventing interleaving between the dedup SELECT and subsequent INSERT/UPDATE.

**SQLite WAL checkpoint** (see ADR-015, ADR-016, ADR-019): The knowledge DB uses WAL mode with mandatory encryption (sqlite3mc, SQLCipher v4 compat). On `Close()`, the WAL must be flushed into the main `.db` file — otherwise git operations corrupt the database. The checkpoint requires exclusive WAL access, which means the `database/sql` connection pool must be closed *before* checkpointing. `Close()` handles this by: (1) closing statements, (2) closing the pool, (3) opening a fresh single connection for `PRAGMA wal_checkpoint(TRUNCATE)`.

**WAL/rekey incompatibility** (see ADR-020): sqlite3mc does not support `PRAGMA rekey` in WAL journal mode. `capy encrypt`'s `encryptPlain` path must switch to DELETE journal mode before rekeying.

**Security checks**: Bash deny patterns are loaded once at server startup. The `matchesAnyBashPattern` function uses cached regexes (`sync.Map`). Shell-escape patterns for non-shell languages are compiled once in `init()`.

**Hook routing** (`hook/pretooluse.go`): The main routing function dispatches on canonical tool name. New tool interceptions go here. Guidance uses file-based persistence since hooks run as separate short-lived processes.

**Content indexing**: All content passes through `sanitize.StripSecrets()` before hashing and storage. SHA-256 content hashing enables dedup — same content with same label skips re-indexing.

**Search pipeline**: RRF (Reciprocal Rank Fusion) across Porter + trigram layers, with fuzzy Levenshtein correction on sparse results. Post-processing: per-source diversification, title-match boost, proximity reranking, entity-aware boosting.

### Adding a New MCP Tool

1. Define the tool schema in `server/tools.go` (follow existing patterns for annotations)
2. Create the handler in a new `server/tool_<name>.go` file
3. Register it in `server/tools.go:registerTools()`
4. Add the tool name to `platform/routing.go:CapyToolNames`
5. Write unit tests in `server/tool_<name>_test.go`
6. Add an integration test in `server/integration_test.go` if the tool interacts with the store

### Adding a New Tool Extractor (Session Indexing)

The session parser uses a table-driven `ExtractorRegistry` (`internal/session/tools.go`) to decide how each tool's `tool_use` input appears in indexed transcripts. Three actions:

- **`ActionPromote`** — tool input becomes part of `AssistantText` (survives even on tool-only turns). Used for conversational tools like PAL.
- **`ActionEnrich`** — tool input becomes a metadata line (e.g., `[Read: path/to/file.go]`). Only appears on turns that already have text.
- **`ActionSkip`** — tool is omitted from transcript metadata. Default for unregistered tools.

To add a new extractor:

1. Write an extract function: `func(input json.RawMessage) string` — parse the JSON input, return a human-readable string (empty string = graceful skip)
2. Register it in `NewDefaultRegistry()` in `tools.go` with the exact tool name and appropriate action
3. Add a test in `tools_test.go` covering valid input, malformed input, and empty fields

### Adding a New Hook Interception

1. Add the routing logic to `hook/pretooluse.go:handlePreToolUse()`
2. If it's a new canonical tool, add it to `hook/helpers.go:toolAliases` for platform variants
3. Add the tool name to `platform/setup.go:PreToolUseMatcherPattern` so the hook is registered
4. Write tests in `hook/hook_test.go` (unit, with test adapter) and `hook/integration_test.go` (with Claude Code adapter)

### Adding Platform Support

1. Implement the `adapter.HookAdapter` interface for the new platform
2. Add tool name aliases to `hook/helpers.go:toolAliases`
3. Add a setup path in `platform/setup.go` (e.g., `SetupCodex` for Codex CLI)
4. Add platform-specific routing instructions if needed

## Configuration Reference

capy uses TOML configuration with three-level precedence (lowest to highest):
1. `~/.config/capy/config.toml` (global)
2. `.capy/config.toml` (project)
3. `.capy.toml` (project root)

```toml
[store]
# path = ".capy/knowledge.db"       # optional override; default: ~/.local/share/capy/<project-hash>/knowledge.db
# title_weight = 2.0                # BM25 title column weight
# max_source_bytes = 2097152        # 2 MB hard cap on total content per source

[store.cleanup]
# cold_threshold_days = 30          # durable sources older than this may be evicted
# ephemeral_ttl_hours = 24          # lifetime for ephemeral sources (minimum 1)
# session_ttl_days = 60             # lifetime for session sources (minimum 1)
# auto_prune = false                # automatic pruning (not yet implemented)

[store.cache]
# fetch_ttl_hours = 24              # skip re-fetch within this window

[executor]
# timeout = 30                      # seconds per execution
# max_output_bytes = 102400         # 100 KB output cap

[server]
# log_level = "info"                # "debug", "info", "warn", "error"
```

## Releases

Releases are fully automated. Push a semver tag to trigger the pipeline:

```bash
git tag v1.0.0 && git push origin v1.0.0
```

What happens:
1. **CI** runs vet + tests (same as every push to master)
2. **Build** produces binaries on native runners for 3 platforms (darwin/arm64, linux/amd64, linux/arm64)
3. **Release** creates a GitHub Release with tarballs and SHA256SUMS
4. **Homebrew** updates the `serpro69/homebrew-tap` formula (stable releases only)

Pre-release tags (e.g., `v1.0.0-rc.1`) create pre-release GitHub Releases and skip the Homebrew update.

## Code Conventions

- **Error handling**: Wrap errors with context via `fmt.Errorf("doing X: %w", err)`. Don't swallow errors silently — at minimum log with `slog.Debug` or `slog.Warn`.
- **Concurrency**: Use `sync.Mutex` for mutable state, `sync.Once` for lazy init, `sync.Map` for concurrent caches. No naked goroutines that use shared resources without synchronization.
- **No `init()` side effects**: The only `init()` in the codebase compiles regex patterns (`security/shell_escape.go`). Keep it that way.
- **Tests**: Use `testify/assert` and `testify/require`. `require` for preconditions (test stops on failure), `assert` for the actual check.
- **Build tags**: Always pass `-tags fts5` for anything involving the store or server packages.
- **Logging**: Use `slog` exclusively. `slog.Info` for operational events, `slog.Debug` for internal details, `slog.Warn` for recoverable issues.
