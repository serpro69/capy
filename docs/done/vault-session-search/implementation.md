# Implementation Plan: Vault-backed session search

> Design: [./design.md](./design.md)
> Tasks: [./tasks.md](./tasks.md)

Audience: an experienced Go engineer new to this codebase. All builds/tests use the
`fts5` build tag (the `Makefile` handles it); `CAPY_DB_KEY` and `CAPY_VAULT_KEY` must
be set (see CLAUDE.md §Build & Test). Each step pairs with an explicit verification.

**Sequencing rule (review #5):** the knowledge.db session sweep is removed **only after**
`capy_search` federates the vault, so default session search never silently breaks.
There is **no** standalone "ship Phase 2 with the sweep removed" boundary. The phases:

- **Phase 1 (Tasks 1–2):** shared retrieval core. Pure refactor, behavior-gated.
- **Phase 2 (Tasks 3–7):** the vault becomes a searchable chunk corpus exposed via
  `capy_vault_search`. The knowledge.db sweep still runs — no regression yet.
- **Phase 3 (Tasks 8–9):** federate the vault into `capy_search`, then (and only then)
  remove the knowledge.db sweep. Federation + removal ship together.
- **Task 10:** verification.

---

## Phase 1 — Shared retrieval core

### Task 1 — Extract `internal/retrieval`
Move the corpus-independent pipeline out of `store/search.go`: the
`SearchWithFallback` orchestration (synonym-AND → flat-OR → optional fuzzy),
`rrfSearch`, `mergeRRFResults`, `rerank`, `diversifyBySource`, entity boosting, and the
porter/trigram/synonym sanitizers + FTS5 escaping. Define `Corpus` (db handle, porter
+ trigram table names from a fixed allowlist, `rowMapper`, optional `FuzzyCorrector`).
Place `SearchResult` where both packages import it without a cycle; `retrieval` must not
import `store`, and `SearchOptions.IncludeKinds` (store-only, review #R4) stays in
`store`. The fuzzy pass is gated on a corpus-supplied `FuzzyCorrector` — `ContentStore`
supplies its vocabulary-backed one, the vault supplies `nil` (review #3).

**Step → verify:** `go test -tags fts5 ./internal/retrieval/...` (moved unit tests) and
`./internal/store/...` both pass.

### Task 2 — `ContentStore` as a `Corpus` + behavior gate
`ContentStore` implements `Corpus` over `chunks`/`chunks_trigram`; generalize
`execDynamicSearch` so the table name + row mapping are corpus-supplied. Knowledge-only
steps (`refreshStaleSources`, `trackAccess`, kind filtering) stay in `store` and wrap
the shared core.

**Step → verify (A4):** `go test -tags fts5 -count=1 ./internal/store/...` unchanged;
`make bench-quality` + `make bench-compare BASE=master TARGET=<branch>` show **zero**
retrieval-quality delta.

---

## Phase 2 — Vault as a searchable chunk corpus

### Task 3 — Vault chunk-FTS schema + migration `0004`
Add `vault_chunks` and `vault_chunks_trigram` to `schemaSQL` (`vault/store.go`) —
**both indexing `title` and `content_text`** (review #2) plus `UNINDEXED`
`session_uuid`, `subagent_id`, `chunk_index`, `first_line_index`. Add
`migrate0004_add_chunk_fts` to the existing runner; bump `currentIndexVersion`. Note the
intentional `0002` gap (review #12) in a code comment.

**Step → verify:** fresh + migrated fixture vaults expose both tables (`table_info`)
and a bumped `index_version`; migration idempotent on re-run.

### Task 4 — Scanner-derived chunker + chunk indexing + reindex + backfill observability
The vault chunker groups the scanner's per-message `ScanResult`s into overlapping
windows (reuse the session chunker's window/overlap sizing; `store.SplitOversized`/
`store.MaxChunkBytes` — `store.ChunkHasCode` is intentionally unused, the chunk schema
has no `has_code` column, see tasks.md Task 4.1), recording `first_line_index` from each
window's first result and a `buildChunkTitle`-style title. **No `internal/session`
import** (review #1/#9). Wire chunk production into the import path
(`scanSessionAndSubagents` already yields `ScanResult`s from DB bytes) and extend
`RebuildFTSBatch`/`UpdateSessionFTS` to (re)populate the chunk tables in the same
batched, WAL-checkpointed pass (`WHERE session_uuid IN (...)` delete). Backfill
observability (review #6): `capy_doctor`/`capy_stats` report the count of vault sessions
below `currentIndexVersion` (existing `OutdatedSessions`) with a "run `capy vault
reindex`" hint.

**Step → verify (A3):** import a fixture → `vault_chunks` count > 0; assert window
sizing/overlap, sub-agent append order, and `first_line_index` resolves to the expected
raw-JSONL line; `capy vault reindex` backfills chunks for a below-version session from
`raw_jsonl`; doctor/stats show the backlog before reindex and zero after.

### Task 5 — Vault chunk search via the shared core
Add `VaultStore.SearchChunks(ctx, SearchOptions) ([]SearchResult, error)` constructing
the vault `Corpus` (chunk tables, vault row mapper, `nil` fuzzy) and running the
`retrieval` engine. Honor `project` (default current), `before`/`after`. **No role
filter** (Not Doing). `defer rows.Close()` + `rows.Err()` + `errors.Is(sql.ErrNoRows)`.

**Step → verify:** porter-only and trigram-only terms each return hits (two-layer
proven); title-match ranks a session whose title (tools/turns) matches; results carry
`first_line_index`.

### Task 6 — Long-lived vault read handle on the Server (review #7)
Add a lazily-initialized `s.vault` (`sync.Once`, opened only when `CAPY_VAULT_KEY`
present), reused by the sweep, the tool (Task 7), and federation (Task 8), and
`Close()`d (checkpoint) in `shutdown()`. Move vault ownership from `vaultSweep` to the
server lifecycle.

**Step → verify (A6):** concurrent federated-read + startup-sweep test; shutdown
flushes `vault.db-wal` (checkpoint runs once); no per-call open in the search path.

### Task 7 — `capy_vault_search` MCP tool + degrade-loudly
`toolVaultSearch()` + `handleVaultSearch` in `tool_vault_search.go`, registered in
`tools.go`. Inputs `queries`, `limit`, `project`/`all_projects`, `before`/`after` (no
`role`). Uses `s.vault`. Returns chunk snippets with `session_uuid`/`title`/
`project_path`/`end_time`/`first_line_index`. Degrade-loudly: key unset → explicit
disabled message; enabled but reindex backlog > 0 and zero hits → name `capy vault
reindex`.

**Step → verify:** integration test (mirror `vault_sweep_test.go`): archived sessions
return ranked hits; key unset → disabled message + no vault file; backlog present + zero
hits → reindex hint.

---

## Phase 3 — Federate, then remove the sweep

### Task 8 — Federate the vault into `capy_search`
`handleSearch`: (a) make the empty-KB preflight (`tool_search.go:66`) **corpus-aware**
— do not early-return when the vault has matching sessions in scope (review #4);
(b) when the vault is enabled and `session` is in scope, run `SearchChunks`
(project-scoped by default; `all_projects`/`project:"*"` widens — new `capy_search`
schema fields) and RRF-merge with the knowledge list (the **nested-RRF topology change**,
review #8); (c) re-point `include_kinds:"session"` to the vault corpus, preserving
default `["durable","session"]`; (d) replace the stale session-exclusion copy
(`tool_search.go:134-148`) with messaging that names `CAPY_VAULT_KEY` (disabled) or
`capy vault reindex` (backlog). Update the `capy_search` tool description +
`.capy/AGENTS.md` source-kinds table.

**Step → verify (A1):** session hit interleaves with durable by default; `["durable"]`
omits it; vault-only project no longer hits "knowledge base is empty"; disabled/backlog
messaging fires; existing `tool_search`/`tool_knowledge` tests green; federated bench vs
unified baseline within tolerance.

### Task 9 — Remove the knowledge.db session sweep + reclaim wiring
**Depends on Task 8.** Delete the `session.Sweep` goroutine (`server.go`) and the
`KindSession` write (`sweep.go:indexSession`); remove only the helpers the deletion
orphans (`buildIndexedAtMap`, `shouldSkip`). `cmd/capy/sweep.go`: deprecating alias to
`capy vault reindex` or remove (Open Q1). `tool_doctor.go` reports **both** legacy
knowledge.db session rows (→ `capy_cleanup purge_session`) and below-version vault
sessions (→ `capy vault reindex`); `tool_stats.go`/`stats.go` mark the knowledge session
tier draining/deprecated. Schema `CHECK` keeps `'session'`.

**Step → verify:** fresh server start creates no `kind='session'` knowledge rows;
default `capy_search` still returns session hits (now via the vault) — proving no
regression vs pre-removal; doctor shows both reclaim + reindex hints; `go build`/`go vet`
clean.

---

## Documentation & ADR

- ADRs authored ahead of implementation: [ADR-027](../../adr/027-vault-is-sole-session-store.md)
  and [ADR-028](../../adr/028-corpus-agnostic-retrieval-and-rrf-federation.md) — both
  **Proposed**; flip to **Accepted** once `/kk:review-design` passes and code lands.
- Update `CLAUDE.md` package map (`internal/retrieval`), `.capy/AGENTS.md` source-kinds
  table, `docs/architecture.md`; `capy_index` the non-obvious rationale to
  `kk:arch-decisions`.

## Verification matrix (assumptions → checks)

| Assumption | Check | Task |
|---|---|---|
| A1 RRF rank-fusion sound | federated bench vs unified baseline | 8 |
| A2 scanner-chunk parity (no vocab/fuzzy) | sessionflow-rag NIAH/retrieval on vault corpus | 5 |
| A3 chunk windows + line anchor | window/overlap + first_line_index + subagent order | 4 |
| A4 no knowledge.db regression | store search tests + bench-compare zero delta | 2 |
| A5 project scope resolvable | project_path filter test | 5/8 |
| A6 long-lived vault handle safe | concurrent read+sweep; shutdown checkpoint | 6 |
