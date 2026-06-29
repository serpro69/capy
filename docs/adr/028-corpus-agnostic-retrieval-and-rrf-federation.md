# ADR-028: Corpus-agnostic retrieval core + cross-corpus RRF federation

**Status:** Proposed
**Date:** 2026-06-29
**Supersedes:** Overturns the reasoning of [ADR-017](017-source-kind-separation.md)
§"Variants Considered #2" (a second FTS5 table was rejected because cross-corpus BM25
scores are incomparable) — that objection does not apply to rank fusion; see D3.
**Design:** [docs/wip/vault-session-search/design.md](../wip/vault-session-search/design.md)
**Pairs with:** [ADR-027](027-vault-is-sole-session-store.md) (the storage change this
search architecture makes non-regressive); builds on [ADR-025](025-vault-index-version-and-reindex.md)
(vault `index_version`/reindex) and [ADR-010](010-rrf-two-layers-not-four.md) (RRF
two-layer rationale).

## Context

[ADR-027](027-vault-is-sole-session-store.md) moves sessions out of `knowledge.db`
into the vault. That is only safe if vault search is at least as good as the session
search it replaces, and if the assistant can still reach session hits.

Two gaps stand in the way:

1. **Quality.** `knowledge.db` search is a two-layer engine: a porter-stemmed FTS and a
   trigram FTS, fused by Reciprocal Rank Fusion (`store/search.go:rrfSearch`), with
   synonym expansion, **vocabulary-backed fuzzy correction**, and proximity rerank, over
   semantic chunks. The vault's search (`VaultStore.Search`) is a single plain FTS5
   `MATCH` over **per-line** rows. Dropping session kind without upgrading vault search
   would regress session recall.
2. **Reach.** The vault is not exposed to the assistant via MCP at all, and once
   sessions live only in the vault, a single `capy_search` call must still surface them
   alongside durable knowledge to preserve the assistant's UX.

The two corpora (knowledge chunks, vault session chunks) live in separate encrypted
SQLite databases with independent FTS5 statistics.

## Decision

### D1: Extract a corpus-agnostic retrieval core (`internal/retrieval`)

Lift the corpus-independent pipeline out of `store/search.go` into a new
`internal/retrieval` package: the **`SearchWithFallback` orchestration** (synonym-AND →
flat-OR fallback → optional fuzzy pass), RRF fusion, `mergeRRFResults`, `rerank`,
`diversifyBySource`, entity boosting, and the porter/trigram/synonym sanitizers. The
engine is parameterized by a `Corpus`: a `*sql.DB`, the porter + trigram table names
(fixed allowlist, never user input), a row→`SearchResult` mapper, and an **optional
`FuzzyCorrector`**.

- **Fuzzy correction is a pluggable, per-corpus capability (not dropped silently).**
  `fuzzyCorrectQuery` is backed by the per-corpus `vocabulary` table, which the vault
  lacks. `ContentStore` supplies its vocabulary-backed corrector; the vault supplies
  `nil`, so its fuzzy pass is a no-op and it relies on the **trigram layer** for
  typo/substring reach. A vault vocabulary is deferred until the benchmark (A2) shows an
  unacceptable delta — an explicit scope decision, not an oversight.
- `ContentStore` becomes one `Corpus` (`chunks`/`chunks_trigram`), keeping its
  knowledge-only concerns — stale-source refresh, `trackAccess`, source-kind filtering —
  on the store side, wrapping the shared core. `SearchOptions.IncludeKinds` is
  store-only and does **not** move into `retrieval`.
- Extracting now (not copying ranking logic into the vault) is justified because D3
  federation needs one engine over both corpora anyway; a copy would diverge. The
  extraction is a **behavior-preserving refactor** for `knowledge.db`: existing
  `internal/store` search tests + `bench-quality`/`bench-compare` are the gate — zero
  retrieval-quality delta before acceptance.

### D2: The vault gets a two-layer chunk FTS, built from the scanner, additive

Add `vault_chunks` (porter/unicode61) and `vault_chunks_trigram` (trigram) virtual
tables, **both indexing `title` and `content_text`** (knowledge.db indexes and
BM25-weights `title`; the vault must too for parity). Each row also carries `UNINDEXED`
`session_uuid`, `subagent_id`, `chunk_index`, and `first_line_index`.

**Chunk source — the vault's own scanner, not `session.ChunkSession`.** Chunks are
produced by grouping the scanner's per-message `ScanResult`s into windows (reusing
`store.SplitOversized`/`MaxChunkBytes`/`ChunkHasCode`). This is deliberate:

- The scanner already runs from DB bytes (`ScanSession(io.Reader)`,
  `scanSessionAndSubagents`), so reindex stays disk-free (ADR-025) with **no** new
  reader-based parser and **no** `vault → internal/session` import — the deliberate
  decoupling (`scanner_types.go:5`) is preserved.
- `vault_chunks` and the per-line `vault_fts` therefore derive from the **same scanned
  text**, so the two indexes can never disagree about what a session contains (a real
  risk had we reused the lossy `session` parser for chunks while the scanner fed
  `vault_fts`).
- `first_line_index` (the window's first `ScanResult` line) is a **recorded** anchor for
  "open in TUI / `vault show`", not a fragile reverse-mapping from parsed-turn space.

The per-line `vault_fts` is **retained untouched** (TUI viewer, `capy vault search
--role`). **No `role` filter on the chunk corpus**: a chunk spans mixed user/assistant/
tool lines, so role is ill-defined at chunk granularity; per-line role filtering stays
on `vault_fts`. Two chunk tables (not one) buy the porter+trigram RRF quality (see
Alternatives). Backfill rides ADR-025: the version bump leaves existing sessions' chunk
tables empty until `capy vault reindex`, surfaced loudly per [ADR-027 D5](027-vault-is-sole-session-store.md)
(no silent/background backfill).

### D3: Federate by RRF *rank* merge — a deliberate ranking-topology change

`capy_search` runs both corpora and fuses their ranked result lists with a final RRF
pass (rank-based: `score = 1/(k + rank)`, `k=60`). The load-bearing insight: **ADR-017
#2 rejected a second FTS table because raw BM25 scores are incomparable across corpora
(IDF is per-table) — but RRF merges on rank position, not score, so the incomparability
never arises.** Federating two independently-ranked corpora by RRF is sound where a
raw-BM25 UNION was not.

**This is a ranking-topology change, not bit-parity.** Today sessions and durable
content share one `chunks` table and are ranked by a *single unified* RRF. Federation
replaces that with *nested* RRF: `RRF(knowledge) ⊕ RRF(vault) → final RRF`. For mixed
queries the ordering will differ. The benchmark (A1) is the quality gate; we accept the
topology change rather than claim equivalence. Vault hits are tagged `session:<uuid>` so
the assistant distinguishes conversation hits from durable knowledge.

### D4: `session` `include_kinds` re-points to the vault; default behavior preserved

The `session` value stays valid but routes to the vault corpus instead of a
knowledge.db kind filter. Default scope keeps `["durable","session"]` behavior — session
hits still appear by default — and `["durable"]` excludes the vault pass. Federated
vault search is **project-scoped by default** (`vault_sessions.project_path` + the
server's `projectDir`), with `all_projects` / `project:"*"` to widen. The empty-KB
preflight in `handleSearch` is made **corpus-aware** so a project with only vault
sessions is searched, not short-circuited with "knowledge base is empty."

A dedicated `capy_vault_search` MCP tool exposes the vault corpus directly. Sequencing
(ADR-027 D1): the tool lands in **Phase 2**; `capy_search` federation lands in
**Phase 3** and ships **together with** the knowledge.db sweep removal — never before.

### D5: A long-lived vault read handle on the Server

The server holds no persistent vault handle today: `vaultSweep` opens its own
`VaultStore` and `Close()`s it (WAL checkpoint) per call. Opening the **encrypted** vault
(key derivation + PRAGMA + WAL) plus checkpoint-on-close **per search** would put real
latency on the hottest path. So federation does **not** open the vault per call: add a
lazily-initialized, long-lived `s.vault` handle (mirroring `getStore()`), opened when
`CAPY_VAULT_KEY` is present, reused by `capy_vault_search`, federation, and the startup
sweep, and `Close()`d (checkpoint) in `shutdown()`. WAL permits concurrent readers, so
federated reads are safe alongside the sweep writer.

## Consequences

**Positive**
- One ranking engine, two corpora — no divergent search implementations.
- Session retrieval gets two-layer porter+trigram RRF in its new home; quality is held
  by the benchmark gate (A1/A2), satisfying ADR-027's precondition.
- `vault_chunks` and `vault_fts` share one source of truth (the scanner), so chunk and
  line search never disagree.
- The RRF-rank-merge principle generalizes: any future corpus federates by rank, never
  by raw cross-table BM25.

**Negative / trade-offs**
- The core extraction touches the hot search path; mitigated by the zero-delta gate (D1).
- Federation is a ranking-*topology* change for mixed queries (D3), not equivalence;
  the benchmark is the gate, not a proof.
- The vault maintains two FTS representations (per-line + chunks), increasing vault.db
  size and reindex cost. Accepted: the vault is compressed; the chunk index is the price
  of retrieval quality.
- Federation reads the vault on the `capy_search` hot path. This is a query against the
  **long-lived `s.vault` handle (D5)** — *not* a per-call encrypted open + checkpoint
  (an earlier draft mischaracterized it as "a second DB query"). Candidate fetch is
  bounded by the same `limit*5` per layer as today.
- Vault chunk content includes tool-result text (scanner-broad), so it is not
  byte-identical to the old knowledge.db session chunks (parser-lossy). Accepted —
  arguably better recall; A2 validates.

## Alternatives considered

### Reuse `session.ChunkSession` via a reader-based parser (rejected — the chunk-source fork)
Add `ParseSessionReader(io.Reader)` + reconstruct subagents from `vault_files` sidecars,
then reuse the exact knowledge.db chunker for parity-by-construction. Rejected: it
reverses the deliberate `vault → session` decoupling, leaves the `first_line_index`
anchor unsolved (turn-pairs carry no line numbers, and parse-turn ≠ scanner-turn space),
keeps a lossy-chunks-vs-broad-lines divergence between `vault_chunks` and `vault_fts`,
and needs a new reader API + sidecar reconstruction. D2's scanner-derived chunks solve
the anchor and divergence at the root.

### Single-layer vault chunk FTS (rejected)
One porter chunk table, no trigram. Lighter, but a measurable quality gap versus the
two-layer RRF; conflicts with the quality precondition.

### Title left UNINDEXED on the chunk tables (rejected)
Would drop the BM25 title weighting knowledge.db relies on (`titleWeight`); chunks index
`title` for parity.

### Replace the per-line `vault_fts` with chunk FTS (rejected)
The TUI viewer and `vault search --role` need per-line `line_index`/`role` anchors;
chunk granularity would break line-level navigation. The two indexes serve different
surfaces.

### Per-call vault open on the search path (rejected)
A full encrypted open + checkpoint-on-close per `capy_search`/`capy_vault_search` call.
Rejected for the long-lived `s.vault` handle (D5).

### Background / open-time chunk backfill (rejected)
Auto-reindexing on first post-upgrade open. Rejected per ADR-025: stalls server startup
on large vaults and fights the fail-loud ethos. Replaced by manual `capy vault reindex`
+ loud backlog reporting (ADR-027 D5).

### Raw BM25 UNION across corpora (rejected — historical, ADR-017 #2)
Merging `bm25()`/rank values from two FTS tables directly. Per-table IDF makes the scores
incomparable. RRF rank fusion (D3) is the correct mechanism and is what makes the
two-corpus design viable.
