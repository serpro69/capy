# AGENTS.md

This file provides guidance to Codex (OpenAI's coding agent) when working with code in this repository.

## Project

`capy` is an MCP server and Claude Code plugin written in Go. It solves context window flooding by keeping raw tool outputs in sandboxed subprocesses and indexing them into SQLite FTS5 with BM25 ranking. It also provides persistent, queryable knowledge across sessions via an encrypted SQLite database.

Originally a Go port of [context-mode](https://github.com/mksglu/context-mode) (TypeScript), capy has evolved independently with its own feature set: mandatory encryption at rest, three source kinds with distinct lifecycle policies, session transcript indexing, multi-platform hook routing, and a tiered retention system.

## Architecture

See [docs/architecture.md](docs/architecture.md) for the full architecture document.

### Key Packages

```
cmd/capy/           CLI entry points (cobra commands)
internal/
  adapter/          Platform adapter interface (HookAdapter) + Claude Code implementation
  config/           TOML config loading with 3-level precedence, project root detection, DB path resolution
  executor/         Polyglot code executor (11 languages), process group isolation, output truncation
  giturl/           Git platform URL detection (GitHub/GitLab/Bitbucket/Gitea)
  hook/             Hook event dispatch: PreToolUse routing, guidance, security, subagent injection
  platform/         Setup command (writes hooks/MCP config), doctor diagnostics, routing instructions
  sanitize/         Secret stripping (regex-based redaction of API keys, tokens, credentials)
  security/         Settings parsing, glob matching, command splitting, shell-escape detection
  server/           MCP server, 9 tool handlers, stats tracking, lifecycle guard, intent search
  session/          Claude Code JSONL parsing, transcript building, chunking, sweep indexing
  store/            SQLite FTS5 knowledge base: schema, indexing, search, cleanup, encryption, migration
  version/          Build-time version injection via ldflags
```

### Critical Invariants

- **Encryption is mandatory.** `CAPY_DB_KEY` must be set. The store refuses to open without it. Tests require it too.
- **FTS5 build tag required.** All builds and tests must use `-tags fts5`. The Makefile handles this.
- **WAL checkpoint on close.** The connection pool must be closed before checkpointing (see `store.go:Close()` and ADR-016).
- **WAL + PRAGMA rekey incompatible.** Encryption path must switch to DELETE journal mode before rekeying (ADR-020).
- **Source kinds are schema-enforced.** `CHECK (kind IN ('ephemeral', 'durable', 'session'))` — no other values accepted.
- **Hooks are short-lived processes.** Each hook invocation is a separate `capy hook <event>` process. State persists via `.capy/guidance-<sessionID>.json` files.

### Build & Test

```bash
export CAPY_DB_KEY=test-key-for-development  # required for all tests
make build                                    # CGO_ENABLED=1, -tags fts5
make test                                     # all tests
make test-race                                # with race detector
go test -tags fts5 -count=1 ./internal/<pkg>/... # single package
```

### Benchmarks

After changing search, indexing, chunking, or executor code, run benchmarks to check for regressions:

```bash
make bench-quality   # quality benchmarks → bench-results/{branch}.json
```

Compare against a baseline with `make compare BASE=main TARGET={branch}` or view a single report with `go run -tags fts5 ./cmd/qualstat bench-results/{branch}.json`. Quality benchmarks are skipped during `go test ./...` (gated by `CAPY_BENCH_RESULTS`).

Key files: `internal/store/bench_test.go` (retrieval + NIAH), `internal/store/bench_perf_test.go` (performance), `internal/server/bench_integration_test.go` (5000-byte threshold), `internal/store/testdata/bench/*.jsonl` (fixtures). Fixture authoring guide: [benchmark/FIXTURES.md](benchmark/FIXTURES.md).

## ADRs

Architecture Decision Records are in [docs/adr/](docs/adr/). Key ones:
- ADR-006: Persistent knowledge base
- ADR-007: Tiered freshness and content dedup
- ADR-011: Conservative cleanup policy
- ADR-015/016: Knowledge DB not in git + WAL checkpoint strategy
- ADR-017: Source kind separation (durable/ephemeral/session)
- ADR-019/020: Encrypted knowledge DB + WAL/rekey incompatibility
- ADR-022: Source size guard and DB bloat prevention
- ADR-023: Fetch ephemeral default and routing rewrite
- ADR-024: Server-side git URL enforcement

## Completed Features

Design docs for completed features are in [docs/done/](docs/done/). Each has design.md, implementation.md, and tasks.md.
