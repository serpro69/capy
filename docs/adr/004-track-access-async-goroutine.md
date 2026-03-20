# ADR-004: trackAccessAsync goroutine may outlive store Close()

**Status:** Accepted (risk acknowledged)
**Date:** 2026-03-20

## Context

`trackAccessAsync` in `search.go` spawns a background goroutine that calls `stmtTrackAccess.Exec()` to update `access_count` and `last_accessed_at` on sources that appeared in search results. If `Close()` is called on the `ContentStore` while this goroutine is still running, the prepared statement will already be finalized and the goroutine will get an error.

## Decision

Accept the risk without adding synchronization (e.g., `sync.WaitGroup`).

## Rationale

- `Close()` only runs on MCP server shutdown (process exit). The goroutine executes a few fast SQLite UPDATEs — typically completes in <1ms.
- The worst case is a failed `Exec` that logs at `Debug` level. No data loss, no panic, no goroutine leak.
- Adding a `WaitGroup` to track in-flight goroutines would add complexity to every search path for a race window that is practically zero.
- The goroutine is fire-and-forget by design — access tracking is a best-effort enhancement, not a correctness requirement.

## Consequences

On shutdown, 0-1 access tracking updates may be lost. This is acceptable since the data is advisory (freshness tracking for cleanup), not critical.
