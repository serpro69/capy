# ADR-018: Corrupt DB detection and recovery on open

**Status:** Accepted
**Date:** 2026-04-23
**Related:** ADR-015 (knowledge DB not tracked in git), ADR-016 (WAL mode and checkpoint)

## Context

Despite ADR-015's mitigation (gitignoring the DB), SQLite corruption can still occur from other causes: unclean shutdown during a WAL checkpoint, filesystem-level errors, or a user manually restoring a stale `.db` file without its matching WAL/SHM sidecars.

When capy opens a corrupt database, `getDB()` fails and the error propagates to every MCP tool call — the server is stuck until manual intervention. Since the knowledge base is reconstructible (re-indexing the same files is cheap per ADR-007's content hash deduplication), silent recovery is preferable to a hard stop.

## Decision

Detect SQLite corruption errors during `getDB()` and recover by **renaming** (not deleting) the corrupt files, then retrying the open sequence once.

Recovery steps:
1. Detect corruption via typed `sqlite3.Error` code check (`ErrCorrupt`, `ErrNotADB`), with a string-based fallback for wrapped errors (`"malformed"`, `"not a database"`, `"corrupt"`)
2. Close the failed DB handle
3. Rename `.db`, `.db-wal`, `.db-shm` to `<original>.corrupt.<timestamp>` (e.g., `knowledge.db.corrupt.20260423T143000`)
4. Retry the full open sequence (pragma → schema → migrations → prepared statements) once
5. If retry fails, propagate the error — avoid infinite loops on persistent filesystem errors

## Alternatives considered

- **Delete and recreate**: simpler but loses the corrupt file for forensic inspection or `sqlite3 .recover`. Rename costs nothing extra.
- **Application-level integrity check on open** (`PRAGMA integrity_check`): expensive (full table scan), and the corruption is already surfaced by the schema/migration step failing. Not worth the startup cost.
- **Prompt the user before recovery**: capy runs as an MCP server with no interactive UI. Automatic recovery with a warning log is the only practical option.

## Consequences

- Corrupt files are preserved with timestamped suffixes for forensic inspection
- Recovery is automatic and transparent — the server self-heals on next tool call
- Previously indexed content must be re-fetched/re-indexed after recovery (the DB is recreated empty)
- Only one retry per open — persistent filesystem errors propagate immediately
- Error classification helpers (`isSQLiteCorruption`, `isBusy`) are co-located in `internal/store/retry.go`
