# ADR-016: WAL mode and checkpoint strategy for git-tracked databases

**Status:** Accepted (with open question on WAL necessity)
**Date:** 2026-03-31

## Context

capy uses SQLite in WAL (Write-Ahead Logging) mode. WAL mode creates two sidecar files (`.db-wal`, `.db-shm`) alongside the main database. Git only tracks the main DB file — branch switching replaces it while leaving stale sidecar files behind, corrupting the database (see ADR-015).

To support users who commit the knowledge DB to git (e.g., syncing across machines), we need a checkpoint strategy that flushes the WAL into the main DB file before commits.

## Decision

Use a two-layer checkpoint strategy:

1. **SessionEnd hook** — best-effort WAL checkpoint when a Claude Code session ends. Covers the common case (session closes, DB left clean). 1.5s timeout, errors logged to stderr, never blocks session exit.

2. **Git pre-commit hook** — installed by `capy setup`. Runs `capy checkpoint` when the knowledge DB is staged. Re-stages the DB after checkpoint so git commits the flushed state. Covers both Claude-initiated and manual terminal commits.

A `PreToolUse(Bash)` hook intercepting `git commit` commands was evaluated and rejected:
- Command parsing is unreliable (aliases, pipelines, chained commands)
- Git commits from the index, not the working tree — checkpointing the DB on disk doesn't help unless it's also re-staged, making the hook intrusive
- The git pre-commit hook already covers this at the correct layer

## Open Question: Is WAL mode necessary?

WAL mode is designed for high-concurrency environments (many concurrent readers while a writer is active). capy's MCP server is effectively single-actor — one server process per project, sequential tool calls.

Switching to `PRAGMA journal_mode = TRUNCATE` (or `DELETE`) would:
- Eliminate WAL and SHM sidecar files entirely
- Make the DB always self-contained and git-ready after every transaction commit
- Remove the need for SessionEnd checkpoint, pre-commit hook, and `capy checkpoint` CLI
- Simplify ADR-015's entire problem space to zero

The tradeoff: WAL provides better read performance during concurrent writes (readers don't block on writers) and is generally recommended for server workloads. For capy's workload (occasional indexing bursts + search queries), this concurrency benefit may not matter.

**Action required before changing:** Benchmark indexing + search performance under both modes. If TRUNCATE mode shows no measurable regression for typical workloads (indexing 100+ chunks while searching), it would be the simpler choice. If WAL provides meaningful benefit during `batch_execute` (which indexes and searches in the same call), keep WAL and the checkpoint infrastructure.

## Consequences

- `capy checkpoint` CLI command exists for manual use
- SessionEnd hook registered by `capy setup`
- Git pre-commit hook installed by `capy setup` (non-fatal if hooks dir is inaccessible)
- Future: if WAL mode is dropped, all three checkpoint mechanisms can be removed
