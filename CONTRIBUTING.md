# Contributing to capy

## Local Setup

### Prerequisites

- **Go 1.23+** ([install](https://go.dev/dl/))
- **C compiler** — required for CGO (SQLite FTS5):
  - macOS: `xcode-select --install`
  - Debian/Ubuntu: `sudo apt install build-essential`
  - Fedora: `sudo dnf install gcc`
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

```bash
make test       # run all tests
make vet        # static analysis
make test-race  # tests with race detector
```

## Project Structure

```
cmd/capy/           CLI entry points (serve, hook, setup, doctor, cleanup)
internal/
  adapter/          Platform adapter interface + Claude Code implementation
  config/           TOML config loading, project root detection, path resolution
  executor/         Polyglot code executor (11 languages, process isolation)
  hook/             Hook event routing (PreToolUse, PostToolUse, etc.)
  platform/         Setup command, doctor diagnostics, routing instructions
  security/         Settings parsing, glob matching, command splitting, shell-escape detection
  server/           MCP server, 9 tool handlers, stats, lifecycle, snippets
  store/            SQLite FTS5 knowledge base (schema, indexing, chunking, search, cleanup)
  version/          Version variable (set at build via ldflags)
```

## Testing

### Running Tests

All tests require the `fts5` build tag. The Makefile handles this:

```bash
make test                                         # all tests
make test-race                                    # with race detector
go test -tags fts5 -count=1 ./internal/store/...  # single package
go test -tags fts5 -count=1 -run TestSearch ./internal/store/...  # single test
go test -tags fts5 -count=1 -v ./internal/hook/...  # verbose
```

Without `-tags fts5` you'll get cryptic `no such module: fts5` errors from SQLite. This is the #1 gotcha.

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
| `server/integration_test.go` | MCP tool handler -> executor -> store -> search round-trips |
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

This shows diagnostics without modifying anything. Useful to verify runtimes, FTS5, and config loading.

### Testing With Claude Code (Full E2E)

For a full end-to-end test, point `capy setup` at a scratch project using the local binary:

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

The MCP server uses stdio (JSON-RPC over stdin/stdout). You can test it by sending raw JSON-RPC messages, but this is complex. The integration tests in `internal/server/integration_test.go` cover this path — prefer running those:

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
2. Runs security checks
3. Does the work (execute, index, search, etc.)
4. Tracks stats via `s.trackToolResponse()`
5. Returns `*mcp.CallToolResult`

**Store operations**: All database operations use prepared statements cached on the `ContentStore` struct. New queries need a prepared statement added to `store.go:prepareStatements()` and closed in `store.go:Close()`.

**SQLite WAL checkpoint** (see ADR-015, ADR-016): The knowledge DB uses WAL mode. On `Close()`, the WAL must be flushed into the main `.db` file — otherwise git operations corrupt the database (WAL/SHM sidecar files aren't tracked). The checkpoint requires exclusive WAL access, which means the `database/sql` connection pool must be closed *before* checkpointing. `Close()` handles this by: (1) closing statements, (2) closing the pool, (3) opening a fresh single connection for `PRAGMA wal_checkpoint(TRUNCATE)`. Do not attempt to checkpoint while pool connections are open — it silently degrades to passive (incomplete). The `Checkpoint()` method does the same thing standalone for the `capy checkpoint` CLI command.

**Important nuance**: WAL/SHM files only exist when the DB has been written to during a session. `Close()` checkpoints whatever WAL frames the current session created. If the server starts but no writes happen (e.g., empty `capy serve` → Ctrl+C), there's nothing to checkpoint and any pre-existing WAL files from a *previous* session remain untouched. This means stale WAL files from an older binary version or unclean shutdown require a manual `capy checkpoint` to flush.

**Security checks**: Bash deny patterns are loaded once at server startup. The `matchesAnyBashPattern` function uses cached regexes (`sync.Map`). Shell-escape patterns for non-shell languages are compiled once in `init()`.

**Hook routing** (`hook/pretooluse.go`): The main routing function dispatches on canonical tool name. New tool interceptions go here. Guidance uses file-based persistence (`.capy/guidance-<sessionID>.json`) since hooks run as separate short-lived processes.

### Adding a New MCP Tool

1. Define the tool schema in `server/tools.go` (follow existing patterns)
2. Create the handler in a new `server/tool_<name>.go` file
3. Register it in `server/tools.go:registerTools()`
4. Add the tool name to `platform/routing.go:CapyToolNames`
5. Write unit tests in `server/tool_<name>_test.go`
6. Add an integration test in `server/integration_test.go` if the tool interacts with the store

### Adding a New Hook Interception

1. Add the routing logic to `hook/pretooluse.go:handlePreToolUse()`
2. If it's a new canonical tool, add it to `hook/helpers.go:toolAliases` for platform variants
3. Add the tool name to `platform/setup.go:PreToolUseMatcherPattern` so the hook is registered
4. Write tests in `hook/hook_test.go` (unit, with test adapter) and `hook/integration_test.go` (with Claude Code adapter)

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
