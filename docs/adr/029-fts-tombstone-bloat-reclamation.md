# ADR-029: FTS5 tombstone-bloat reclamation (`cleanup --optimize`)

## Status

Accepted

## Context

ADR-022 bounded per-source index growth and added an auto-`VACUUM` pass after
cleanup (§5). In practice a knowledge DB could still balloon to 50+ MB while
holding under 1 MB of live content, and neither `capy cleanup --force` nor
`capy cleanup --vacuum` reclaimed it. `capy dbsize` reported **0 freelist
pages** — so `VACUUM`, which only compacts freelist pages, was a no-op.

The bloat lives in the FTS5 index, not the freelist. capy indexes content into
two FTS5 tables (`chunks`, `chunks_trigram`). When a source is evicted, FTS5
does not free the underlying index pages — it appends *delete-tombstone
segments*. Those pages remain **live** (owned by the FTS5 shadow data tables),
so they never enter the freelist and `VACUUM` cannot touch them. Over months of
index/evict churn (and, historically, session-transcript writes since dropped
per ADR-027), the tombstone segments accumulate without bound.

The incremental FTS5 `'optimize'` command that `optimizeFTS` already runs every
50 inserts performs only a bounded segment merge; it does not fully discard the
tombstones. Only a full FTS5 `'rebuild'` reconstructs the index from the content
tables, releasing every stale segment page to the freelist — after which a
`VACUUM` compacts the file.

Measured on a real bloated DB: **50.9 MB → 8.3 MB**, all 83 sources intact.

## Decision

Add a `RebuildFTS()` store method and expose it as `capy cleanup --optimize`.

1. **`RebuildFTS()`** runs `INSERT INTO chunks(chunks) VALUES('rebuild')` and the
   same for `chunks_trigram` on a dedicated single connection (the same pattern
   `Vacuum` and `Checkpoint` use), then checkpoints the WAL. Rebuild alone does
   not shrink the file — it only moves the freed pages to the freelist.

2. **`--optimize`** chains `RebuildFTS()` then `Vacuum()` (the sequence that
   actually reclaims disk). It works standalone (`capy cleanup --optimize`) and
   after a forced cleanup (`capy cleanup --force --optimize`), where it evicts
   stale sources first and then reclaims. `--optimize` supersedes `--vacuum`
   since it already vacuums.

3. **Busy checkpoint is tolerated, not fatal.** The post-rebuild
   `wal_checkpoint(TRUNCATE)` runs while the connection pool may still be open
   (the `--force --optimize` path), so it can legitimately downgrade to PASSIVE
   (`busy > 0`). This costs no reclamation — the subsequent `VACUUM` opens its
   own connection and rewrites the full logical DB, WAL frames included — so
   `RebuildFTS` logs a warning rather than erroring. This is the opposite of the
   `Checkpoint()` method, which is only ever called with the pool closed and so
   errors on `busy > 0`.

4. **`--optimize` intentionally ignores the `--dry-run` default.** `cleanup`
   defaults to dry-run, but reclamation is a maintenance operation, not data
   eviction — mirroring the existing `--vacuum` flag. Gating it on dry-run would
   make `capy cleanup --optimize` a silent no-op, the same trap that
   `--kind session` cleanup produces without `--force`.

## Consequences

**Positive**

- Users have a first-class, verified path to reclaim FTS bloat that `VACUUM`
  alone cannot: `capy cleanup --optimize`.
- The single-connection setup shared by `Vacuum`, `RebuildFTS`, `Checkpoint`,
  and the private `checkpoint` is extracted into one `openSingleConn` helper.

**Negative / trade-offs**

- A full FTS5 rebuild is O(index size) and, under SQLCipher, re-encrypts the
  rewritten pages — heavier than `--vacuum`. It is a manual/explicit operation,
  not part of the auto-cleanup path.
- On-disk truncation still depends on the deferred `Close()` checkpoint
  (ADR-016): the CLI relies on its normal command teardown to flush the shrunk
  file to disk.

**Cross-reference**

- ADR-011 / ADR-022 — cleanup and DB-bloat prevention; this ADR adds the FTS
  reclamation path VACUUM-based bloat control did not cover.
- ADR-016 — WAL + checkpoint; the dedicated single-connection pattern and
  checkpoint-on-close invariant are reused here.
- ADR-020 — the WAL/PRAGMA-rekey incompatibility applies only to rekey;
  `rebuild` and `VACUUM` run safely in WAL mode.

## CLI changes

```
capy cleanup --optimize           # rebuild FTS indexes + VACUUM to reclaim FTS bloat
capy cleanup --force --optimize   # evict stale sources first, then reclaim
```
