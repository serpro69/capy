# ADR-016: WAL mode and checkpoint strategy for git-tracked databases

**Status:** Accepted (with open question on WAL necessity)
**Date:** 2026-03-31

## Context

capy uses SQLite in WAL (Write-Ahead Logging) mode. WAL mode creates two sidecar files (`.db-wal`, `.db-shm`) alongside the main database. Git only tracks the main DB file — branch switching replaces it while leaving stale sidecar files behind, corrupting the database (see ADR-015).

To support users who commit the knowledge DB to git (e.g., syncing across machines), we need a checkpoint strategy that flushes the WAL into the main DB file before commits.

## Decision

Use a two-layer checkpoint strategy:

1. **MCP server shutdown** — `ContentStore.Close()` runs `PRAGMA wal_checkpoint(TRUNCATE)` when the server exits (triggered by lifecycle guard on parent death). This is the primary checkpoint mechanism because the server *owns* the DB connection and can get exclusive WAL access.

2. **Git pre-commit hook** — installed by `capy setup`. Runs `capy checkpoint` when the knowledge DB is staged. Re-stages the DB after checkpoint so git commits the flushed state. Covers both Claude-initiated and manual terminal commits. Works because the MCP server is typically not running during manual commits.

3. **DB-repo guard hook** — installed by `capy setup --db-repo` for a repository that *holds* knowledge databases (one subdirectory per project) rather than a project. It is pure `sh` with no capy binary or key dependency: it never checkpoints or repairs, it only *refuses* an unsafe staged `*.db` — one that is plaintext, or has a non-empty `-wal`/present `-shm` sidecar. The "no sidecars ⇒ main file complete" invariant (layer 1 removes them on clean shutdown) is what lets a keyless hook decide safety. Repair happens in the owning project (`capy encrypt`, or `capy checkpoint --project-dir <project>` from anywhere) — the DB repo never sees per-project keys. See [issue #90](https://github.com/serpro69/capy/issues/90) / `docs/feat/done/separate-db-repo/design.md`.

### Rejected approaches

**SessionEnd hook** was implemented and then removed. The hook fires while the MCP server still holds the DB connection open. A second connection from the hook cannot get exclusive WAL access, so `wal_checkpoint(TRUNCATE)` degrades to a passive checkpoint — leaving data in WAL/SHM files. The MCP server's own shutdown checkpoint is the correct mechanism.

**PreToolUse(Bash)** intercepting `git commit` was evaluated and rejected:
- Command parsing is unreliable (aliases, pipelines, chained commands)
- Git commits from the index, not the working tree — checkpointing the DB on disk doesn't help unless it's also re-staged, making the hook intrusive
- The git pre-commit hook already covers this at the correct layer

**Checkpoint from the DB repo** (a `--db <file>` flag, a `.project` reverse-mapping symlink, and the hook sourcing the owning project's env to obtain its key) was rejected for the DB-repo guard hook above. It bought only the ability to repair a stale WAL from inside the DB repo — where a live session's checkpoint is usually *busy* anyway — at the cost of per-project key handling inside a repo that is meant to hold none. A keyless checkpoint was also proven impossible (the sqlite3mc driver cannot open an encrypted DB without the key). "Refuse, don't repair" is the right contract; repair belongs in the owning project. Full rationale in `docs/feat/done/separate-db-repo/design.md`.

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

- `capy checkpoint` CLI command exists for manual use (when server is not running); `--project-dir <project>` targets a specific project from anywhere (the remedy the DB-repo guard hook names)
- MCP server checkpoints on shutdown via `ContentStore.Close()`
- Git pre-commit hook installed by `capy setup` (non-fatal if hooks dir is inaccessible)
- DB-repo guard hook installed by `capy setup --db-repo` — pure `sh`, refuses staged DBs with sidecars or plaintext; keeps a dedicated DB repo safe to commit from without a key
- The `capy setup` wrapper (`capy.sh`) propagates real exit codes for every subcommand **except** `hook` events (which still always exit 0). Consequence: a busy or failed `capy checkpoint` now **fails the project pre-commit hook** instead of silently committing a stale main file — a deliberate, user-visible change
- SessionEnd hook is a no-op placeholder for future cleanup tasks (not checkpoint)
- Future: if WAL mode is dropped, all checkpoint mechanisms can be removed
