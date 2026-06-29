# ADR-027: The vault is the sole session store (drop `session` kind from knowledge.db)

**Status:** Proposed
**Date:** 2026-06-29
**Supersedes:** Supersedes the `KindSession`-in-knowledge.db decision from
[sessionflow-rag](../done/sessionflow-rag/design.md); amends [ADR-017](017-source-kind-separation.md)
(which introduced the session kind alongside durable/ephemeral) and relates to
[ADR-007](007-tiered-freshness-and-content-dedup.md) (retention) and
[ADR-022](022-source-size-guard-and-db-bloat-prevention.md) (DB bloat).
**Design:** [docs/wip/vault-session-search/design.md](../wip/vault-session-search/design.md)
**Pairs with:** [ADR-028](028-corpus-agnostic-retrieval-and-rrf-federation.md) (the
search architecture that makes this storage change non-regressive).

## Context

Two subsystems independently ingest the *same* Claude Code session transcripts at
every MCP server start (`internal/server/server.go`):

1. **`session.Sweep`** (`server.go:148`, introduced by sessionflow-rag) chunks each
   transcript and writes it into the **per-project** `knowledge.db` as
   `kind='session'` — uncompressed text **plus** a full FTS5 index — on a 60-day TTL.
2. **`vaultSweep`** (`server.go:164`) archives the same transcripts verbatim into the
   **global** `vault.db` — zstd-compressed, retained forever, versioned, cross-machine
   mergeable, already FTS5-indexed.

Sessions are therefore stored twice, and the *more expensive* copy — uncompressed
content plus its index, churned by TTL eviction — is the one inflating
`knowledge.db`. This is the bloat the user observed.

The architectural intent already separates these stores (captured arch-decision
2026-05-28): the vault "uses its own DB — archives forever, global cross-project,"
while the knowledge store is "per-project + tiered cleanup." Sessions are
conversational history with a forever/global lifecycle — that is the vault's job, not
the per-project knowledge DB's.

The only reason `session` kind was put in `knowledge.db` originally was searchability:
when sessionflow-rag shipped, the vault did not exist. The vault now archives the
same data and (per ADR-028) gains benchmark-validated search. The duplication is now
pure cost.

## Decision

### D1: `vaultSweep` becomes the sole session ingestion path — *after* federation

Delete the `session.Sweep` goroutine call (`server.go`) and the `KindSession` write
in `session/sweep.go:indexSession` (`cs.IndexChunked(..., store.KindSession, ...)`).
Only the knowledge.db write and the helpers that deletion orphans (`buildIndexedAtMap`,
`shouldSkip`) are removed.

**Sequencing (load-bearing).** This removal must land **only after** ADR-028's
federation makes `capy_search` query the vault. Removing the sweep first would silently
strip session hits from default `capy_search` (the hits are gone from knowledge.db and
not yet served from the vault). Federation and removal therefore ship together; there
is no intermediate "sweep removed, federation pending" state.

**Note on the session parse helpers.** The vault does **not** reuse
`session.ParseSession`/`ChunkSession` to build its retrieval chunks — ADR-028 D2/D3
chunk from the vault's own scanner, preserving the deliberate `vault → internal/session`
decoupling (`scanner_types.go:5`). So this removal need not preserve those helpers for
the vault; they remain in `internal/session` only for `capy sweep`'s dry-run/diagnostic
paths (retired or kept per the design's Open Question 1).

### D2: The schema `CHECK` keeps `'session'`; we stop *writing*, not *allowing*

`CHECK (kind IN ('ephemeral','durable','session'))` is left intact. Existing
knowledge.db files still hold `kind='session'` rows; removing the value would break
their open path. New session rows simply stop being produced. The `include_kinds`
`session` value also survives — ADR-028 re-points it at the vault corpus.

### D3: Legacy rows are reclaimed explicitly via the existing `purge_session`

`capy_cleanup` already exposes a `purge_session` flag
(`internal/server/tool_cleanup.go:28`). No new purge command is built. Reclaim is
**explicit and observable** — not a silent destructive migration on an encrypted DB,
and not a 60-day TTL drain. `capy_doctor` gains a check that reports **both** (a)
surviving `kind='session'` rows in knowledge.db → `capy_cleanup purge_session`, **and**
(b) vault sessions below `currentIndexVersion` whose chunk index is not yet backfilled
→ `capy vault reindex` (see D5). `capy_stats` surfaces the same two counts and marks the
knowledge session tier draining/deprecated rather than growing.

### D4: The vault stays opt-in; the gap degrades loudly — disabled *and* unbackfilled

The vault remains opt-in via `CAPY_VAULT_KEY` (unlike the mandatory `CAPY_DB_KEY`).
We do **not** make it mandatory. Two no-result states degrade loudly, never silently:

- **Vault disabled** (`CAPY_VAULT_KEY` unset): `capy_search`, `capy_vault_search`,
  `capy_stats`, and `capy_doctor` say session search is off and name `CAPY_VAULT_KEY`.
- **Vault enabled but un-backfilled** (D5): after the chunk-index version bump, archived
  sessions have empty chunk tables until `capy vault reindex`. A session search that
  returns nothing while a backlog exists names `capy vault reindex` — the
  vault-enabled-but-unsearchable window is surfaced, not silent.

### D5: The post-upgrade chunk backfill is manual but loudly surfaced

ADR-028 adds a chunk index and bumps `currentIndexVersion`, so every already-archived
session is flagged below-current with **empty chunk tables** until reindexed. Backfill
is **not** automatic (a background/open-time sweep was rejected in ADR-025 for the same
fail-loud/startup-stall reasons). Instead `capy vault reindex` — which already re-scans
below-version sessions from DB blobs — backfills chunks, and the pending backlog is
reported by `capy_doctor`/`capy_stats` (existing `VaultStats.OutdatedSessions`) and the
degrade-loudly messaging (D4). Historical sessions are therefore never *silently*
missing from search.

## Consequences

**Positive**
- `knowledge.db` size stops scaling with conversation volume — sessions contribute
  ~0 bytes. The single remaining session copy is the vault's compressed blob.
- One ingestion path, one source of truth for sessions; no more dual-sweep on
  startup.
- Sessions gain forever-retention + cross-machine merge (vault properties) instead of
  a 60-day TTL that silently evicted history.

**Negative / trade-offs**
- Users without `CAPY_VAULT_KEY` lose session search after upgrade. Mitigated by D4
  (loud, actionable messaging), not by forcing a key.
- A migration-era window exists where old `kind='session'` rows linger until the user
  runs `purge_session`. Accepted: explicit reclaim over silent mutation (D3).
- Session search now depends on the vault being enabled and reindexed — a coupling
  that did not exist when the knowledge sweep was unconditional.

## Alternatives considered

### Make the vault mandatory (rejected)
Promote `CAPY_VAULT_KEY` to required, matching `CAPY_DB_KEY`, so every user keeps
session search. Rejected: a breaking change forcing key setup on all existing users
for a feature that was opt-in-adjacent. Degrade-loudly (D4) delivers the safety
without the breakage.

### Auto-purge legacy rows on first post-upgrade open (rejected)
Silently `DELETE` `kind='session'` rows on startup. Rejected: a silent destructive
mutation on an encrypted DB contradicts capy's fail-loud ethos. `purge_session` is
explicit and observable.

### Let the 60-day TTL drain legacy rows (rejected)
Stop writing and let existing rows expire naturally. Rejected: bloat persists for up
to 60 days and the dead code path lingers with no forcing function to remove it.

### Keep a thin knowledge.db fallback when the vault is off (rejected)
Retain the old sweep only when `CAPY_VAULT_KEY` is unset. Rejected: two divergent
ingestion + search paths to maintain indefinitely, for the shrinking population of
non-vault users. Degrade-loudly is simpler and honest.
