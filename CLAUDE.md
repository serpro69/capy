# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

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
  retrieval/        Corpus-agnostic FTS5 retrieval core: RRF two-layer fusion, rerank, entity boosting, query sanitization — shared by store (knowledge) and vault (session chunks) via the Corpus interface (ADR-028)
  sanitize/         Secret stripping (regex-based redaction of API keys, tokens, credentials)
  security/         Settings parsing, glob matching, command splitting, shell-escape detection
  server/           MCP server, 10 tool handlers, stats tracking, lifecycle guard, intent search, cross-corpus vault federation
  sqliteutil/       Shared SQLite open/recovery: canary query, corruption classification, backup
  store/            SQLite FTS5 knowledge base: schema, indexing, search, cleanup, encryption, migration
  vault/            Session vault: verbatim archival, FTS5 search (per-line + chunk), discovery, import, cross-machine merge; sole session store (ADR-027)
  vault/tui/        Interactive TUI for vault browsing, search, and session viewing (bubbletea)
  version/          Build-time version injection via ldflags
```

### Critical Invariants

- **Encryption is mandatory.** `CAPY_DB_KEY` must be set for the knowledge store; `CAPY_VAULT_KEY` must be set for the vault. Tests require both.
- **FTS5 build tag required.** All builds and tests must use `-tags fts5`. The Makefile handles this.
- **WAL checkpoint on close.** The connection pool must be closed before checkpointing (see `store.go:Close()` and ADR-016).
- **WAL + PRAGMA rekey incompatible.** Encryption path must switch to DELETE journal mode before rekeying (ADR-020).
- **Source kinds are schema-enforced.** `CHECK (kind IN ('ephemeral', 'durable', 'session'))` — no other values accepted.
- **Vault blob `encoding` column is authoritative.** Compressed (`'zstd'`) vs raw (`'raw'`/`NULL`-legacy) blobs are distinguished by the per-row `encoding` column, never magic-byte detection (sidecars hold arbitrary bytes). The first compressed write stamps `vault_meta.min_reader_version`; `openDB` refuses a vault whose marker exceeds `supportedReaderVersion` (2). `content_hash`/`size_bytes`/FTS are always computed on **uncompressed** bytes.
- **Vault rekey uses the backup-API, not PRAGMA rekey.** `sqliteutil.Rekey` writes a fresh new-key file (open old → checkpoint → backup-copy → swap+verify), sidestepping the WAL/PRAGMA-rekey incompatibility above. Shared by `capy vault rekey` and `capy encrypt`.
- **Hooks are short-lived processes.** Each hook invocation is a separate `capy hook <event>` process. State persists via `.capy/guidance-<sessionID>.json` files.

### Build & Test

```bash
export CAPY_DB_KEY=test-key-for-development   # required for knowledge store tests
export CAPY_VAULT_KEY=test-key                # required for vault tests
make build                                    # CGO_ENABLED=1, -tags fts5
make build-glamour                            # + opt-in glamour TUI markdown (-tags fts5,glamour)
make test                                     # all tests
make test-race                                # with race detector
go test -tags fts5 -count=1 ./internal/<pkg>/... # single package
go test -tags fts5,glamour ./internal/vault/tui/... # glamour-tagged TUI subset
```

### Benchmarks

After changing search, indexing, chunking, or executor code, run benchmarks to check for regressions:

```bash
make bench-quality   # quality benchmarks → bench-results/{branch}.json
```

Compare against a baseline with `make bench-compare BASE=main TARGET={branch}` or view a single report with `go run -tags fts5 ./cmd/qualstat bench-results/{branch}.json`. Quality benchmarks are skipped during `go test ./...` (gated by `CAPY_BENCH_RESULTS`).

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
- ADR-025: Vault `index_version` and DB-driven reindex
- ADR-026: Git worktrees share the main worktree's knowledge DB
- ADR-027: The vault is the sole session store (drop `session` writes from knowledge.db)
- ADR-028: Corpus-agnostic retrieval core + cross-corpus RRF federation

## Completed Features

Design docs for completed features are in [docs/done/](docs/done/). Each has design.md, implementation.md, and tasks.md.

# Extra Instructions

@.claude/CLAUDE.extra.md

# capy — context-window routing

@.capy/AGENTS.md
