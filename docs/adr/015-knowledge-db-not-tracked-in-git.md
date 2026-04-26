# ADR-015: Knowledge DB must not be tracked in git

**Status:** Superseded by [ADR-019](019-encrypted-knowledge-db.md)
**Date:** 2026-03-31

## Context

The original design (ADR-006) noted that the DB location is configurable and "can be committed to VCS for team sharing." An exception was added to `.gitignore` to track `.capy/knowledge.db`.

During normal development, switching git branches replaced the main DB file while leaving SQLite's WAL and SHM sidecar files (untracked) in place. The result was a WAL from one branch paired with a DB from another — immediate corruption (`database disk image is malformed`), total data loss.

## Decision

Never track a live SQLite database in git. The default `.capy/knowledge.db` and any WAL/SHM sidecar files are gitignored.

The cross-machine / team-sharing use case (working on two laptops, sharing a KB across a team) remains valid but requires a different mechanism than raw git tracking. This is deferred to the session continuity feature. Candidate approaches:

- **Manifest-based reindex**: a `[reindex]` section in `.capy.toml` listing file paths and URLs; `capy reindex` rebuilds the KB deterministically. Commits the manifest, not the DB. Doesn't preserve session-derived content (batch_execute output, ad-hoc index calls).
- **Export/import**: `capy export` dumps to a git-friendly format; `capy import` rebuilds. Manual workflow.
- **External sync**: Syncthing/iCloud/Dropbox on `.capy/` directory. Works but adds external dependency.

None of these are implemented yet. For now, each machine maintains its own KB. Content hash deduplication (ADR-007) makes re-indexing the same files cheap.

As a stopgap for the two-machine single-user case, `capy checkpoint` flushes the WAL into the main DB file and removes sidecar files, making the DB safe to commit. The workflow: stop capy → `capy checkpoint` → commit → push. On the other machine: pull → capy recreates WAL/SHM on first access. This works but requires discipline and doesn't handle session-derived content reconstruction.

## Rationale

- SQLite WAL mode uses sidecar files (`.db-wal`, `.db-shm`) that must stay in sync with the main DB file. Git only tracks the main file.
- Branch switching, rebasing, stashing, or any git operation that replaces the DB file breaks this invariant.
- The corruption is silent until the next write — no warning, just `SQLITE_CORRUPT`.
- This is a fundamental SQLite + git incompatibility, not a capy-specific bug.

## Consequences

- `.capy/knowledge.db` stays gitignored (default)
- Cross-machine KB portability is deferred to session continuity
- Each checkout maintains its own independent knowledge base
- Users who previously tracked the DB should `git rm --cached .capy/knowledge.db` and delete the corrupted file
