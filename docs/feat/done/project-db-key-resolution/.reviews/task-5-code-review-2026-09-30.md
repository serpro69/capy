# Task 5 implementation review

> Scope: baseline environment-only stdio MCP transport and reusable process fixture
> Review mode: `kk:review-code:isolated`, mid-implementation
> Reviewers: independent `code-reviewer` agent and PAL `gemini-3.1-pro-preview`

## Result

The independent agent approved with no P0–P3 findings. PAL completed its two-step external review with two low-priority suggestions, both addressed:

- **LOW — dense initializer**, `mcp_stdio_helpers_test.go`, original line 94: expand the `stdioProcess` composite literal across semantic fields. The initializer now uses one field per line.
- **LOW — unhandled blocking-copy return**, `mcp_stdio_test.go`, original line 240: explicitly account for `io.Copy`'s result in the deliberately stalled helper child. The helper now exits with a failure code if the copy unexpectedly returns an error. Author context: successful completion also exits with a failure code; normal tests terminate this child externally while it remains blocked.

No correctness, security, or architecture defects were reported. No findings qualify for indexing. The review covered two new Go test files and task bookkeeping. Go profile checklists covered SOLID, removal, security, style, errors, command injection, naming, and concurrency. Tasks 1–4 supplied regression context; pending Tasks 6–11 were excluded from missing-implementation assessment.

## Implementation

`buildStdioCapy` builds one FTS5 candidate per parent fixture, reused for every session in its subtests. `stdioEnv` inherits only PATH and explicitly isolates home, config/data directories, session discovery roots, knowledge credentials, and the disabled vault's target. `startStdioProcess` accepts executable/arguments/cwd/environment separately so subsequent direct-credential and wrapper tests can reuse it.

The fixture owns pipe ends, sets read/write deadlines, matches increasing JSON-RPC IDs, accepts intervening notifications, and checks protocol/tool failures. It captures bounded stderr separately, checks supplied synthetic keys for disclosure, and redacts them in failure diagnostics. A single waiter reaps the child; normal EOF shutdown requires a successful exit, while request failure, timeout, or test cleanup forcibly terminates and waits. The protocol handshake and EOF shutdown follow the [MCP lifecycle contract](https://modelcontextprotocol.io/specification/2025-06-18/basic/lifecycle).

Real tool calls index a marker, search for independent query terms, and verify doctor reports available FTS5, one retained source/chunk, the selected project, and a disabled vault. Two processes stay alive together while each writes and reads its own project under distinct correct environment keys. Both databases reopen without re-indexing. Searches omit the expected marker and apply no source/project filter, avoiding query-echo or filter-based false positives. Clean shutdown checks persisted encrypted files and absent/empty WALs.

## Verification

Passed:

- `go test -tags fts5 -count=1 -run '^TestMCPStdio' ./cmd/capy`.
- Focused tests with `-race`, including the final cleanup changes.
- Full `make test`, including generated-artifact guards.
- `go vet -tags fts5 ./cmd/capy`, formatting, and whitespace checks.

Transport failure cases exercise exit code 7 with redacted stderr, blocked reads, blocked writes, mismatched response IDs, JSON-RPC errors, shutdown ignoring EOF, and registered cleanup when the caller leaves a child running. Each case asserts the child was reaped; no sleeps are used. The race run instruments the test harness and helper test executable. The candidate server built by the fixture is a normal FTS5 binary without race instrumentation; this run does not establish race coverage inside that subprocess.

The full suite ran with synthetic keys and an isolated home/config/data/vault environment. Local HTTP and Unix socket access was permitted for existing tests. The focused tests passed in the ordinary sandbox. No assertions were weakened, no live database or credential files were used, and no production code or dependencies changed. Search/indexing algorithms are unchanged, so quality benchmarks are not applicable. Full-feature race/matrix verification remains assigned to Task 11.

No design deviation or new convention requiring separate indexing was introduced. Credential selection in direct MCP, generated wrappers, and the remaining command integrations stay in their assigned pending tasks.
