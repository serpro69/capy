# ADR-035: Fetch validation time and bounded stale checks

**Status:** Proposed — implemented candidate, pending decision acceptance
**Date:** 2026-10-12
**Amends:** [ADR-013](013-configurable-fetch-ttl.md), [ADR-022](022-source-size-guard-and-db-bloat-prevention.md)
**Preserves:** [ADR-016](016-wal-mode-and-checkpoint-strategy.md), [ADR-017](017-source-kind-separation.md), [ADR-027](027-vault-is-sole-session-store.md)
**Provenance:** [upstream-sync design §5.5/7.1](../feat/wip/upstream-sync-v1.0.169/design.md), Tasks 12a/16a/16; this records their tested implementation, not a new acceptance decision.

## Context

Same-hash deduplication preserves `indexed_at`, so using indexing age as fetch
freshness repeatedly refetched unchanged content after expiry. Search access
cannot stand in for validation either. Meanwhile directory ingestion makes an
all-file stale scan on each eligible search unbounded, and a process-local cursor
would restart at the same files on every CLI invocation.

## Decision

Keep three independent concepts:

| Metadata | Meaning and owner |
| --- | --- |
| `indexed_at` and retention/access fields | Existing source lifecycle; unchanged fetch validation does not restart indexing age. |
| Nullable `fetch_validated_at_ms` (migration 021) | Time a successful HTTP body finished admission, before conversion/queueing; committed atomically with its actual content and kind by `IndexFetched`. |
| `file_check_seq` and partial `(file_check_seq, id)` index (migration 020) | Persisted order of attempted backing-file checks; advances even on denied/missing/unchanged reads. |

Fetch hits require matching URL-derived label/kind and
`0 <= now_ms - validated_at_ms < ttl_ms`. Missing, invalid or future markers miss.
Omission selects configured hours, `ttl` supplies request-local milliseconds,
and zero/force bypass. Validate all supplied values before work. Unchanged success
renews only validation and returns retained chunks; failed work, hits and searches
never renew it. Generic replacements clear validation. Out-of-order commits keep
their own content/time pair, not the maximum timestamp from another response.
Freshness cannot prevent an old ephemeral source's retention eviction.

Refresh selects at most 32 oldest-checked file rows and closes metadata cursors
before I/O. Each store allows one active pass with a five-second cooldown. Raw
read admission is bounded by `max(8 MiB, max_source_bytes + 1)` and a 100 ms clock
starting after metadata selection. The first candidate is admitted unless
canceled; an operation already started may finish. This is not a total query
latency or OS I/O deadline. Unattempted files keep their place.

Short SQLite write transactions allocate sequence values above the indexed
maximum and guard the checked source incarnation. Refresh cannot overwrite or
resurrect a concurrently replaced/removed source. Same-path replacement carries
the schedule; changing backing path resets it. Scheduling failure logs and stops
the pass. Cached content remains searchable on denied/failed/deferred reads.
Separate processes may duplicate bounded checks; no distributed lock is promised.

## Alternatives, cost and reversal

Renewing `indexed_at` would extend retention; using access time would turn searches
into remote validation. A per-process refresh cursor starves later files across
CLI restarts. Unbounded scans grow with corpus size. The chosen scheduling index
adds one write per attempted check and permits temporarily stale results in
exchange for bounded admission and persistent progress.

Both migrations are additive and transactional on encrypted knowledge stores;
create the scheduling index only after adding its column. Old rows start unchecked
and fetched rows conservatively miss until revalidated. Older binaries can ignore
the new columns; their replacements reset metadata. Downgrade preserves content
but loses freshness/refresh guarantees. Ciphertext, old timestamp formats,
source kinds, close/checkpoint ordering and the separate vault are unchanged.

Owners are store `stale.go`, `index.go`, `migrate.go`, and server `tool_fetch.go`.
Migration rollback/reopen, actual old-binary coexistence, concurrent replacement,
500 ms boundaries and 10,000-file fairness/workload fixtures supply evidence.
The [final verification ledger](../feat/wip/upstream-sync-v1.0.169/verification.md#task-20-final-verification-and-compatibility)
records tested revisions and measured read/row-change/allocation costs; SQLite
row-change counts must not be presented as physical disk-write measurements.
