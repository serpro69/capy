# AGENTS.md

This file provides guidance to Codex (OpenAI's coding agent) when working with code in this repository.

## Project

`capy` is an MCP server and Claude Code plugin written in Go. It solves context window flooding by keeping raw tool outputs in sandboxed subprocesses and indexing them into SQLite FTS5 with BM25 ranking. It also provides persistent, queryable knowledge across sessions via an encrypted SQLite database.

## Architecture

See [docs/architecture.md](docs/architecture.md) for the full architecture document.

## Key Packages

```
cmd/capy/           CLI entry points (cobra commands)
internal/
  adapter/          Platform adapter interface (HookAdapter) + Claude Code implementation
  config/           TOML config loading with 3-level precedence, project root detection
  executor/         Polyglot code executor (11 languages), process group isolation
  giturl/           Git platform URL detection (GitHub/GitLab/Bitbucket/Gitea)
  hook/             Hook event dispatch: PreToolUse routing, guidance, security
  platform/         Setup command, doctor diagnostics, routing instructions
  sanitize/         Secret stripping (regex-based redaction)
  security/         Settings parsing, glob matching, command splitting, shell-escape detection
  server/           MCP server, 9 tool handlers, stats tracking, lifecycle guard
  session/          Claude Code JSONL parsing, transcript building, sweep indexing
  store/            SQLite FTS5 knowledge base: schema, indexing, search, cleanup, encryption
  version/          Build-time version injection via ldflags
```

## Critical Invariants

- **Encryption is mandatory.** `CAPY_DB_KEY` must be set. The store refuses to open without it.
- **FTS5 build tag required.** All builds and tests must use `-tags fts5`. The Makefile handles this.
- **WAL checkpoint on close.** The connection pool must be closed before checkpointing (see `store.go:Close()`).
- **Source kinds are schema-enforced.** `CHECK (kind IN ('ephemeral', 'durable', 'session'))`.
- **Hooks are short-lived processes.** Each hook invocation is a separate `capy hook <event>` process.

## Build & Test

```bash
export CAPY_DB_KEY=test-key-for-development
make build      # CGO_ENABLED=1, -tags fts5
make test       # all tests
make test-race  # with race detector
```

## ADRs

Architecture Decision Records are in [docs/adr/](docs/adr/).

## Design Docs

Completed features: [docs/done/](docs/done/). In-progress features: [docs/wip/](docs/wip/).
