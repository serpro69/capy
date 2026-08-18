# Design: Vault-backed session search (drop `session` kind from knowledge.db)

> Status: draft (revised after 4-way design review — findings addressed inline, tagged `review #N`)
> Created: 2026-06-29
> Profiles: `go` (applies `profiles/go/design/database.md`)
> ADRs: [ADR-027](../../adr/027-vault-is-sole-session-store.md) (vault is the sole
> session store — supersedes the `KindSession`-in-knowledge.db decision from
> [sessionflow-rag](../../done/sessionflow-rag/design.md), amends [ADR-017](../../adr/017-source-kind-separation.md))
> and [ADR-028](../../adr/028-corpus-agnostic-retrieval-and-rrf-federation.md)
> (corpus-agnostic retrieval core + RRF federation — overturns ADR-017 §Variants #2).
> Rides [ADR-025](../../adr/025-vault-index-version-and-reindex.md) (vault
> `index_version`/reindex). Both ADRs are **Proposed** pending `/kk:review-design`
> and implementation.

## Problem

Two subsystems independently ingest the *same* Claude Code session transcripts at
every MCP server start (`internal/server/server.go`):

1. `session.Sweep` (`server.go:148`) chunks each transcript and writes it into the
   per-project `knowledge.db` as `kind='session'` — **uncompressed text plus a full
   FTS5 index**, on a 60-day TTL.
2. `vaultSweep` (`server.go:164`) archives the same transcripts verbatim into the
   global `vault.db` — **zstd-compressed, retained forever, versioned, cross-machine
   mergeable**, already FTS5-indexed (per-line, via the scanner).

Sessions are therefore stored twice, and the *more expensive* copy (uncompressed +
index, churned by TTL eviction) is the one bloating `knowledge.db`. The vault is the
architecturally-correct home (captured arch-decision 2026-05-28: "vault uses its own
DB — archives forever, global cross-project; the knowledge store is per-project +
tiered cleanup").

The blocker to consolidating today: the vault's search (`VaultStore.Search`,
`store.go:928`) is a single plain FTS5 `MATCH` over **per-line** rows, whereas
`knowledge.db` search is a two-layer (porter + trigram) **RRF** engine with synonym
expansion, fuzzy correction, proximity rerank, and **semantic chunking**. Naively
dropping session kind would regress session-recall quality, and vault search is not
exposed to the assistant via MCP at all.

## Goals (success metrics)

- **G1 — Bloat elimination (primary):** `knowledge.db` size stops scaling with
  conversation volume; sessions contribute ~0 bytes to it. The vault becomes the sole
  session store.
- **G2 — Search quality, benchmark-validated (primary):** past-session retrieval
  quality, after moving to the vault corpus, stays within benchmark tolerance of
  today's sessionflow-rag retrieval/NIAH numbers. **Note (review #8):** Phase 3
  federation changes ranking *topology* (today's single unified RRF over one `chunks`
  table → nested RRF-of-RRF across two corpora). This is a deliberate behavioral
  change, not bit-parity; the benchmark gate (A1) is what holds quality, not a
  by-construction guarantee.
- **G3 — Seamless assistant UX (nice-to-have):** the assistant keeps receiving session
  hits through `capy_search` with no new decision-making.

## Non-goals (Not Doing)

- **Make the vault mandatory.** Opt-in via `CAPY_VAULT_KEY`; the gap is handled by
  degrade-loudly (D5/D8), not by forcing a key.
- **Port `intent_search` to the vault** (ephemeral command output, not sessions).
- **Change verbatim archival / compression / snapshot model.** Untouched.
- **Build a new purge command.** `capy_cleanup purge_session` already exists
  (`tool_cleanup.go:28`).
- **Remove `'session'` from the schema `CHECK`.** Kept for back-compat; we only stop
  *writing* it.
- **Alter the per-line `vault_fts` table.** TUI viewer + `capy vault search --role`
  depend on its `line_index`/`role` anchors; left as-is.
- **`role` filter on `capy_vault_search`.** Dropped (review #C/role): a semantic chunk
  spans mixed user/assistant/tool lines, so role is ill-defined at chunk granularity.
  Per-line role filtering remains on `capy vault search --role` and the TUI.
- **Vault vocabulary / fuzzy-correction pass (initially).** Deferred and benchmark-
  gated (D1, A2); the trigram layer provides interim typo/substring tolerance.
- **Re-introduce a `vault → internal/session` dependency.** The chunk source is the
  vault's own scanner (D3), preserving the deliberate decoupling (`scanner_types.go:5`).

## Decisions

### D1 — Extract a corpus-agnostic retrieval core (`internal/retrieval`)

Lift the corpus-independent search pipeline out of `store/search.go` into a new
`internal/retrieval` package: the **`SearchWithFallback` orchestration** (synonym-AND
pass → flat-OR fallback → optional fuzzy pass), `rrfSearch` fusion, `mergeRRFResults`,
`rerank`, `diversifyBySource`, entity boosting, and the porter/trigram/synonym query
sanitizers. The engine is parameterized by a `Corpus` abstraction supplying: a
`*sql.DB`, the porter + trigram FTS table names (fixed allowlist, never user input), a
`rowMapper`, and an **optional `FuzzyCorrector`**.

- **Fuzzy/vocabulary (review #3):** `fuzzyCorrectQuery` is backed by the per-corpus
  `vocabulary` table (`search.go:995`), which the vault does not have. Rather than
  drop fuzzy silently, the corrector is a **pluggable, optional** corpus capability:
  `ContentStore` supplies its vocabulary-backed corrector; the vault supplies `nil`,
  so its fuzzy pass is a no-op and it relies on the **trigram layer** for
  typo/substring reach. Adding a vault vocabulary is deferred until A2 shows the delta
  is unacceptable.
- `ContentStore` implements `Corpus` over `chunks`/`chunks_trigram`; its knowledge-only
  concerns — stale-source refresh, `trackAccess`, source-kind filtering — stay on the
  store side and wrap the shared core. `SearchOptions.IncludeKinds` is store-only and
  does **not** move into `retrieval` (review #R4).
- **Why now:** Phase 3 federation needs one engine over both corpora; extracting once
  avoids divergent implementations. **Behavior-preservation gate:** pure refactor for
  knowledge.db — existing `internal/store` search tests + `bench-quality` are the
  regression gate (A4); zero delta required.

### D2 — Vault chunk-FTS schema: two-layer, title-indexed, with provenance

Two FTS5 virtual tables, mirroring knowledge.db's two-layer design, **both indexing
`title` and `content`** (review #2 — knowledge.db indexes + BM25-weights `title`,
`schema.go:23`, `execDynamicSearch` `bm25(…, titleWeight, 1.0)`; the vault must too for
parity):

```
vault_chunks         USING fts5(title, content_text, session_uuid UNINDEXED,
                                subagent_id UNINDEXED, chunk_index UNINDEXED,
                                first_line_index UNINDEXED, tokenize='porter unicode61')
vault_chunks_trigram USING fts5(title, content_text, … same UNINDEXED cols …,
                                tokenize='trigram')
```

- **Column-order invariant (Task 2):** the shared retrieval skeleton hardcodes
  `highlight(<table>, 1, …)` / `bm25(<table>, titleWeight, 1.0)`, so `title` and
  `content_text` **must stay the first two declared columns** of both tables, in that
  order (see ADR-028 D1 "as implemented" and the `retrieval.CorpusConfig` doc).
- `first_line_index` (review #2 line anchor): the raw-JSONL `LineIndex` of the chunk's
  first constituent `ScanResult`. Because chunks are built from scanner output (D3),
  this is a **real, recorded anchor** — not a fragile mapping back from parsed-turn
  space. It lets a chunk hit open at the right line in the TUI / `vault show`.
- No `role` column / no role filter on the tool (see Not Doing).
- The existing per-line `vault_fts` is **untouched** (display/navigation).

### D3 — Chunk source: the vault scanner (resolves the parse-path findings)

Chunks are produced by **grouping the vault scanner's per-message `ScanResult`s into
windows** — *not* by `session.ParseSession`/`ChunkSession`. This resolves reviews
#1/#9/#11 at the root:

- **No disk / no reader-parser (#1):** `ScanSession(io.Reader)` already runs from DB
  bytes (`scanSessionAndSubagents(uuid, RawJSONL, files)`), and subagents already come
  from `vault_files` sidecars. No `ParseSessionReader` and no `vault → session` import
  is introduced — the deliberate decoupling (`scanner_types.go:5`) is preserved.
- **No dual-index divergence (#9):** `vault_chunks` and `vault_fts` derive from the
  **same scanned text** (tool-results included, broad). A chunk search and a line search
  over a session can no longer disagree about what exists.
- **No category error (#11):** there is one parser; A3 below validates chunk-window
  quality, not "ScanSession vs ParseSession equivalence."

A vault chunker groups consecutive `ScanResult`s into overlapping windows (reusing the
session chunker's window/overlap sizing for consistency), applies
`store.SplitOversized`/`store.MaxChunkBytes`/`store.ChunkHasCode`, records
`first_line_index` from the window's first result, and builds a BM25-friendly title
(session datetime + turn range + tool names, adapted from `buildChunkTitle`). Text is
already sanitized by the scanner.

### D4 — Migration `0004`, reindex, and backfill observability (resolves #6)

A vault migration `0004_add_chunk_fts` creates the two tables; `currentIndexVersion`
bumps. **Migration-numbering note (review #12):** registered migrations are `0001` and
`0003` — `0002` (`vault_snapshots`) was intentionally dropped (`migrations.go:20`).
`0004` is the correct next slot; the gap is deliberate.

The bump flags every already-archived session as below-current, so its chunk tables are
**empty until reindexed**. `RebuildFTSBatch` extends to (re)populate the chunk tables
alongside `vault_fts` in the same batched, WAL-checkpointed pass. `Reindex` already
re-scans every below-version session **from DB blobs** (ADR-025) — so it backfills
chunks too.

**The backfill is not silent (review #6).** `Reindex` stays a manual command
(consistent with ADR-025's explicit-command philosophy — no open-time/background
backfill). But its pendency is surfaced loudly:

- `capy_doctor` reports **both** (a) legacy `kind='session'` rows in knowledge.db
  (reclaim via `capy_cleanup purge_session`) **and** (b) `N` vault sessions below
  `currentIndexVersion` (run `capy vault reindex`) — using the existing
  `OutdatedSessions` count (`VaultStats`).
- `capy_stats` surfaces the same backlog.
- Degrade-loudly (D5/D7) covers the **vault-enabled-but-unbackfilled** state, not just
  vault-disabled: a session search that finds nothing while a reindex backlog exists
  says so and names `capy vault reindex`.

### D5 — `capy_vault_search` MCP tool + degrade-loudly

New tool registered in `tools.go`; handler `handleVaultSearch` in
`internal/server/tool_vault_search.go`. Inputs:

- `queries: []string` (batched), `limit` (default 3)
- `project` — defaults to the **current** project (`s.projectDir` → `project_path`
  filter); `"*"` or `all_projects: true` widens globally
- `before`, `after` — reuse `vault.SearchOptions`. **No `role`** (Not Doing).

Runs the **chunk** corpus through the shared retrieval core; returns chunk-level
snippets (`snippet()` over `content_text`) with `session_uuid`, `title`,
`project_path`, `end_time`, and the `first_line_index` anchor. Uses the long-lived
vault handle (D6). Degrade-loudly: `CAPY_VAULT_KEY` unset → explicit
"vault disabled — set `CAPY_VAULT_KEY`" message; vault enabled but reindex backlog > 0
and zero results → message names `capy vault reindex`.

### D6 — Long-lived vault read handle on the Server (resolves #7)

Today the server holds **no** persistent vault handle: `vaultSweep` opens its own
`VaultStore` and `Close()`s it (WAL checkpoint) per call. Opening the **encrypted**
vault (key derivation + PRAGMA + WAL) plus checkpoint-on-close **per search** would put
real latency on the hottest path — so ADR-028's "a second DB query" framing was wrong.

Add a lazily-initialized, long-lived `s.vault` handle (mirroring `getStore()`'s
`sync.Once`), opened when `CAPY_VAULT_KEY` is present, reused by `capy_vault_search`,
federation (D7), **and** the startup sweep, and `Close()`d (checkpoint) in `shutdown()`.
`shutdown()` currently closes only the knowledge store and the sweep owns the vault
handle — this decision moves vault ownership to the server lifecycle. WAL allows
concurrent readers, so federated reads are safe alongside the startup sweep writer.

### D7 — Federate into `capy_search` (corpus-aware; resolves #4, #8)

`handleSearch` gains a vault pass when the vault is enabled:

- **Corpus-aware empty-KB preflight (#4):** the current early return on
  `kbStats.SourceCount == 0` (`tool_search.go:66`) must **not** fire when the vault has
  matching sessions. The preflight becomes "knowledge **and** vault both empty for this
  scope," or is moved after the federated search.
- Run the query through the vault chunk corpus (project-scoped by default;
  `all_projects`/`project:"*"` widens — new `capy_search` schema fields), then RRF-merge
  the knowledge and vault ranked lists. This is the **nested-RRF topology change** G2
  flags; A1 is the gate.
- `include_kinds` `session` re-points to the vault corpus; default `["durable","session"]`
  behavior is preserved (session hits still appear by default). `["durable"]` excludes
  the vault pass. Vault hits carry a `session:<uuid>` label.
- Reworked zero-/few-results messaging names `CAPY_VAULT_KEY` (disabled) or
  `capy vault reindex` (backlog), replacing the stale knowledge.db session-exclusion
  copy at `tool_search.go:134-148`.

### D8 — Remove the knowledge.db session sweep — *after* federation (resolves #5)

**Sequencing (review #5):** removing `session.Sweep` while federation is not yet in
place would silently strip session hits from default `capy_search`. Therefore sweep
removal (this decision) **depends on** D7 federation landing first. "Phase B is
shippable on its own" was wrong and is removed — the shippable boundary is *after*
federation (see implementation.md phases).

Then: delete the `session.Sweep` call (`server.go`) and the `KindSession` write in
`sweep.go:indexSession`; remove only the helpers the deletion orphans
(`buildIndexedAtMap`, `shouldSkip`). `vaultSweep` becomes the sole ingestion path.
`cmd/capy/sweep.go` is repurposed as a deprecating alias to `capy vault reindex` (Open
Q1). `capy_doctor`/`capy_stats` reclaim + backlog reporting per D4. Schema `CHECK` keeps
`'session'`.

> Note: the chunk/session parse helpers in `internal/session` (`ParseSession`,
> `ChunkSession`, …) are **not** reused by the vault under D3, so D8's deletion does not
> need to preserve them for the vault — only the knowledge.db sweep stops calling them.
> They remain in the package for `capy sweep`'s dry-run/diagnostic paths unless those
> are also retired (Open Q1).

## Architecture summary

```
                       ┌────────────────────────────┐
   capy_search ───────▶│ internal/retrieval (shared) │◀─────── capy_vault_search
   (federated, P-3)    │  SearchWithFallback orch.    │         (chunk corpus, P-2)
                       │  RRF · rerank · diversify    │
                       │  synonym/trigram sanitizers  │
                       │  + optional FuzzyCorrector   │
                       └──────┬───────────────┬──────┘
                  Corpus:knowledge        Corpus:vault
              chunks / chunks_trigram   vault_chunks / vault_chunks_trigram
              (+ stale-refresh,          (title+content indexed; first_line_index;
               trackAccess, kind filt,   chunks built from the SAME scanner that
               vocabulary→fuzzy)         feeds per-line vault_fts; no fuzzy initially)
                                         long-lived s.vault handle (D6)
```

## Go database conventions applied (profile `go` / database.md)

Adopted: parameterized placeholders only (table names from a fixed allowlist, never
user input); `*Context` variants; `defer rows.Close()` + `rows.Err()`;
`errors.Is(err, sql.ErrNoRows)`; `sql.Null*`/pointers for nullable columns; batched
writes with WAL checkpoint between batches.

**Documented deviations:** *No sqlx/pgx/ORM* — raw `database/sql` + `mattn/go-sqlite3`
(`fts5` tag), matching existing code. *Hand-rolled migrations* — the vault's idempotent
name-keyed runner (ADR-025); the profile's "external migration tooling / never
hand-roll schema" rule targets production multi-tenant DBs, not a local single-user
encrypted SQLite, so schema creation is in scope here with human review.

## Assumptions (testable)

- **A1:** RRF *rank* fusion across the two corpora yields sensible merged ordering.
  *Validate:* federated benchmark vs. today's unified ranking on shared fixtures.
- **A2:** Scanner-derived chunks + a vault porter+trigram FTS (no vocabulary/fuzzy)
  reach retrieval quality within tolerance of today's session search. *Validate:*
  re-run sessionflow-rag NIAH/retrieval against the vault corpus; if the delta is
  unacceptable, add a vault vocabulary + fuzzy corrector (D1).
- **A3 (reworded, #11):** The scanner-derived chunker produces good BM25 windows and a
  correct `first_line_index` anchor. *Validate:* on fixtures, assert chunk-window
  sizing/overlap, sub-agent append order, and that `first_line_index` resolves to the
  expected raw-JSONL line. (No "ScanSession vs ParseSession" comparison — there is one
  parser.)
- **A4:** Extracting the shared core regresses neither quality nor perf of knowledge.db
  search. *Validate:* existing `internal/store` search tests + benches, zero delta.
- **A5:** Per-project scope is resolvable for federation — `vault_sessions.project_path`
  + the server's `projectDir`. *Confirmed:* `VaultStore.Search` already filters by
  `project_path`.
- **A6 (new, #7):** A long-lived vault read handle is safe alongside the startup sweep
  writer under WAL. *Validate:* concurrent federated-read + sweep test; shutdown
  checkpoint test.

## Rejected Alternatives

- **C — Minimal wire-up** of per-line vault `Search`: fails G2 (single-table per-line
  BM25 ≪ two-layer RRF).
- **B-only / A-only:** lose G3 / federate a weak corpus respectively.
- **Reuse `session.ChunkSession` via a reader-parser (the other chunk-source fork):**
  parity-by-construction with old knowledge.db chunks, but reverses the deliberate
  decoupling, leaves the line anchor unsolved (turn-pairs carry no line numbers), keeps
  the lossy-vs-broad dual-index divergence, and needs a new reader API + sidecar subagent
  reconstruction. Rejected in favor of D3 (scanner-derived).
- **Make vault mandatory / auto-purge / TTL-drain / single-layer FTS / raw-BM25 union:**
  as before (breaking / silent / lingering / quality gap / cross-corpus incomparable).
- **Per-call vault open on the search path:** rejected for the long-lived handle (D6).
- **Background/open-time chunk backfill:** rejected per ADR-025 (stalls startup / fights
  fail-loud); replaced by loud reporting + manual `reindex` (D4).

## Open questions

1. **`cmd/capy/sweep.go` fate:** deprecating alias to `capy vault reindex`, or removed?
   (Affects `capy sweep` users and whether `internal/session` chunk helpers stay.)
   Default: alias + deprecation notice; keep helpers.
2. **Vault chunker window/overlap:** reuse session chunker's `windowSize=4`/`overlap=1`
   as-is over scan turns, or tune for per-message granularity? Default: reuse, let A2
   tune.
3. **`all_projects` default for `capy_search` federation:** confirmed project-default;
   the new schema field name (`all_projects` bool vs `project:"*"`) is an implementation
   detail to settle in D5/D7.
