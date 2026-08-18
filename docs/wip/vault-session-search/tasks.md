# Tasks: Vault-backed session search

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> Status: pending
> Created: 2026-06-29
> Not Doing: make vault mandatory; port intent_search; change verbatim/compression/snapshot model; build a new purge command (reuse purge_session); drop 'session' from schema CHECK; alter per-line vault_fts; role filter on capy_vault_search (chunk granularity); vault vocabulary/fuzzy initially (benchmark-gated); re-couple vault→internal/session

**Sequencing (review #5):** the knowledge.db sweep (Task 9) is removed **only after**
federation (Task 8) lands — there is no "ship with the sweep removed" boundary before
federation. Phase 1 = Tasks 1–2; Phase 2 = Tasks 3–7; Phase 3 = Tasks 8–9.

## Task 1: Extract `internal/retrieval` shared core
- **Status:** done
- **Depends on:** —
- **Size:** M
- **Slicing:** Risk-first (hot search path; behavior-preserving)
- **Can run in parallel with:** Task 3, Task 6
- **Docs:** [implementation.md#task-1--extract-internalretrieval](./implementation.md#task-1--extract-internalretrieval)

### Subtasks
- [x] 1.1 Create `internal/retrieval/`; move `SearchWithFallback` orchestration, `rrfSearch`, `mergeRRFResults`, `rerank`, `diversifyBySource`, entity boosting, porter/trigram/synonym sanitizers + FTS5 escaping
- [x] 1.2 Define `Corpus` (db handle, porter+trigram table names from a fixed allowlist, `rowMapper`, optional `FuzzyCorrector`); place `SearchResult` to avoid an import cycle; `retrieval` must not import `store`; keep `SearchOptions.IncludeKinds` in `store` (review #R4)
  - Note: in this task the db handle, row mapping, and per-call knowledge filters live *inside* the corpus's `Exec` callback (closed over by `ContentStore.searchCorpus`); Task 2 generalizes `execDynamicSearch` so table name + row mapping become explicit corpus-supplied pieces. Table names are allowlist-validated in `retrieval.NewCorpus` (Task 3/5 must add the vault tables to the allowlist).
  - Review-driven additions (isolated review): `NewCorpus` rejects `porterTable == trigramTable`; `RRFSearch` panics loudly on a zero-value `Corpus`; `context.Context` threaded through `SearchFunc`/`SearchWithFallback`/`RRFSearch` (store passes `s.ctx()`, `execDynamicSearch` now uses `QueryContext`) so Task 5's `SearchChunks(ctx, …)` needs no API churn.
- [x] 1.3 Make the fuzzy pass gated on a corpus-supplied `FuzzyCorrector` — `ContentStore` supplies the vocabulary-backed one, vault supplies `nil` (review #3)
- [x] 1.4 Move unit tests; `go test -tags fts5 ./internal/retrieval/... ./internal/store/...` green

## Task 2: `ContentStore` as a `Corpus` + behavior gate
- **Status:** done
- **Depends on:** Task 1
- **Size:** M
- **Slicing:** Risk-first
- **Can run in parallel with:** Task 3, Task 6
- **Docs:** [implementation.md#task-2--contentstore-as-a-corpus--behavior-gate](./implementation.md#task-2--contentstore-as-a-corpus--behavior-gate)

### Subtasks
- [x] 2.1 Generalize `execDynamicSearch` so table name + row mapping are corpus-supplied; `ContentStore` implements `Corpus` over `chunks`/`chunks_trigram`
  - Note: the old `execDynamicSearch` moved into `retrieval` as the shared layer-query skeleton (`CorpusConfig.execSearch`); `NewCorpus` now takes a `CorpusConfig` (DB handle, tables, SELECT/JOIN shape, title weight, pre-bound filter clauses, `RowMapper`, optional fuzzy). The internal `exec` seam between RRF orchestration and SQL execution is preserved (engine tests stub it). Skeleton invariant: every corpus FTS table declares `title`/`content` as columns 0/1 — Task 3's vault tables must follow it.
- [x] 2.2 Keep knowledge-only steps (`refreshStaleSources`, `trackAccess`, kind filtering) in `store`, wrapping the shared core
  - Note: filter building lives in `store.knowledgeFilterClauses` (source label, content type, kind — review #R4), passed pre-bound to the corpus.
- [x] 2.3 **Gate (A4):** `go test -tags fts5 -count=1 ./internal/store/...` unchanged; `make bench-compare BASE=master TARGET=<branch>` zero delta

## Task 3: Vault chunk-FTS schema + migration `0004`
- **Status:** pending
- **Depends on:** —
- **Size:** S
- **Can run in parallel with:** Task 1, Task 2, Task 6
- **Docs:** [implementation.md#task-3--vault-chunk-fts-schema--migration-0004](./implementation.md#task-3--vault-chunk-fts-schema--migration-0004)

### Subtasks
- [ ] 3.1 Add `vault_chunks` + `vault_chunks_trigram` to `schemaSQL` (`vault/store.go`) — **`title` and `content_text` both indexed** (review #2); `session_uuid`/`subagent_id`/`chunk_index`/`first_line_index` UNINDEXED
- [ ] 3.2 Add `migrate0004_add_chunk_fts` to the existing runner; bump `currentIndexVersion`; comment the intentional `0002` gap (review #12)
- [ ] 3.3 Tests: fresh + migrated fixture vaults expose both tables and a bumped version; idempotent on re-run

## Task 4: Scanner-derived chunker + indexing + reindex + backfill observability
- **Status:** pending
- **Depends on:** Task 3
- **Size:** M
- **Can run in parallel with:** Task 1, Task 2, Task 6
- **Docs:** [implementation.md#task-4--scanner-derived-chunker--chunk-indexing--reindex--backfill-observability](./implementation.md#task-4--scanner-derived-chunker--chunk-indexing--reindex--backfill-observability)

### Subtasks
- [ ] 4.1 Add a vault chunker that groups the scanner's per-message `ScanResult`s into overlapping windows (reuse window/overlap sizing; `store.SplitOversized`/`MaxChunkBytes`/`ChunkHasCode`), recording `first_line_index` + a `buildChunkTitle`-style title — **no `internal/session` import** (review #1/#9/#11)
- [ ] 4.2 Wire chunk production into the import path (off `scanSessionAndSubagents`'s DB-bytes `ScanResult`s); extend `RebuildFTSBatch`/`UpdateSessionFTS` to (re)populate chunk tables in the same batched, WAL-checkpointed pass
- [ ] 4.3 Backfill observability (review #6): `capy_doctor`/`capy_stats` report vault sessions below `currentIndexVersion` (existing `OutdatedSessions`) with a "run `capy vault reindex`" hint
- [ ] 4.4 Tests (A3): import fixture → chunks > 0; assert window sizing/overlap, sub-agent append order, `first_line_index` → expected raw-JSONL line; `capy vault reindex` backfills a below-version session from `raw_jsonl`; backlog shown before, zero after

## Task 5: Vault chunk search via the shared core
- **Status:** pending
- **Depends on:** Task 2, Task 4
- **Size:** M
- **Can run in parallel with:** Task 6
- **Docs:** [implementation.md#task-5--vault-chunk-search-via-the-shared-core](./implementation.md#task-5--vault-chunk-search-via-the-shared-core)

### Subtasks
- [ ] 5.1 Add `VaultStore.SearchChunks(ctx, SearchOptions) ([]SearchResult, error)` building the vault `Corpus` (chunk tables, vault row mapper, `nil` fuzzy) and running the `retrieval` engine; honor `project` (default current), `before`/`after`; **no role filter** (Not Doing); `defer rows.Close()` + `rows.Err()` + `errors.Is(sql.ErrNoRows)`
- [ ] 5.2 Return `first_line_index` anchor + chunk `snippet()` per hit
- [ ] 5.3 Tests: porter-only and trigram-only terms each return hits; title-match ranks the matching session; parity gate (A2) — sessionflow-rag NIAH/retrieval against the vault corpus vs the knowledge.db session baseline

## Task 6: Long-lived vault read handle on the Server
- **Status:** pending
- **Depends on:** —
- **Size:** S
- **Slicing:** Risk-first (concurrency/lifecycle; review #7)
- **Can run in parallel with:** Task 1, Task 2, Task 3, Task 4, Task 5
- **Docs:** [implementation.md#task-6--long-lived-vault-read-handle-on-the-server-review-7](./implementation.md#task-6--long-lived-vault-read-handle-on-the-server-review-7)

### Subtasks
- [ ] 6.1 Add a lazily-initialized `s.vault` (`sync.Once`, opened only when `CAPY_VAULT_KEY` present); move vault ownership from `vaultSweep` to the server lifecycle; sweep reuses the handle
- [ ] 6.2 `Close()` (checkpoint) the handle in `shutdown()` alongside the knowledge store
- [ ] 6.3 Tests (A6): concurrent federated-read + startup-sweep; shutdown flushes `vault.db-wal`; assert no per-call open in the search path

## Task 7: `capy_vault_search` MCP tool + degrade-loudly
- **Status:** pending
- **Depends on:** Task 5, Task 6
- **Size:** M
- **Slicing:** Contract-first (new MCP boundary)
- **Can run in parallel with:** Task 8
- **Docs:** [implementation.md#task-7--capy_vault_search-mcp-tool--degrade-loudly](./implementation.md#task-7--capy_vault_search-mcp-tool--degrade-loudly)

### Subtasks
- [ ] 7.1 `toolVaultSearch()` schema + register in `tools.go`; inputs `queries`, `limit`, `project`/`all_projects`, `before`/`after` (no `role`); reuse `coerce.go`
- [ ] 7.2 `handleVaultSearch` uses `s.vault` + `SearchChunks`; format snippets with `session_uuid`/`title`/`project_path`/`end_time`/`first_line_index`
- [ ] 7.3 Degrade-loudly: key unset → explicit disabled message; enabled but reindex backlog > 0 and zero hits → name `capy vault reindex`
- [ ] 7.4 Integration test: archived sessions return ranked hits; key unset → disabled message + no vault file; backlog + zero hits → reindex hint

## Task 8: Federate the vault into `capy_search`
- **Status:** pending
- **Depends on:** Task 5, Task 6
- **Size:** M
- **Can run in parallel with:** Task 7
- **Docs:** [implementation.md#task-8--federate-the-vault-into-capy_search](./implementation.md#task-8--federate-the-vault-into-capy_search)

### Subtasks
- [ ] 8.1 Make the empty-KB preflight (`tool_search.go:66`) corpus-aware — do not early-return when the vault has matching sessions in scope (review #4)
- [ ] 8.2 When vault enabled and `session` in scope, run `SearchChunks` (project-default; `all_projects`/`project:"*"` widens — new `capy_search` schema fields) and RRF-merge with the knowledge list (nested-RRF topology change, review #8)
- [ ] 8.3 Re-point `include_kinds:"session"` to the vault corpus; preserve default `["durable","session"]`; tag vault hits `session:<uuid>`
- [ ] 8.4 Replace the stale session-exclusion copy (`tool_search.go:134-148`) with `CAPY_VAULT_KEY` (disabled) / `capy vault reindex` (backlog) messaging; update the `capy_search` description + `.capy/AGENTS.md`
- [ ] 8.5 Integration test (A1): session hit interleaves by default; `["durable"]` omits it; vault-only project no longer hits "knowledge base is empty"; messaging fires; federated bench within tolerance; existing tests green

## Task 9: Remove the knowledge.db session sweep + reclaim wiring
- **Status:** pending
- **Depends on:** Task 8
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [implementation.md#task-9--remove-the-knowledgedb-session-sweep--reclaim-wiring](./implementation.md#task-9--remove-the-knowledgedb-session-sweep--reclaim-wiring)

### Subtasks
- [ ] 9.1 Delete the `session.Sweep` goroutine (`server.go`) and the `KindSession` write (`sweep.go:indexSession`); remove only orphaned helpers (`buildIndexedAtMap`, `shouldSkip`)
- [ ] 9.2 `cmd/capy/sweep.go`: deprecating alias to `capy vault reindex` or remove (Open Q1) — confirm with maintainer
- [ ] 9.3 `tool_doctor.go` reports **both** legacy knowledge.db session rows (→ `capy_cleanup purge_session`) and below-version vault sessions (→ `capy vault reindex`); `tool_stats.go`/`stats.go` mark the knowledge session tier draining/deprecated
- [ ] 9.4 Tests: fresh server start creates no `kind='session'` knowledge rows; **default `capy_search` still returns session hits via the vault** (no regression vs pre-removal); doctor shows both hints; `go build`/`go vet` clean

## Task 10: Final verification
- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3, Task 4, Task 5, Task 6, Task 7, Task 8, Task 9
- **Size:** S
- **Can run in parallel with:** —

### Subtasks
- [ ] 10.1 Run `/kk:test` — full suite (`make test` / `make test-race`), vault + store + retrieval + server packages
- [ ] 10.2 Run `/kk:document` — flip ADR-027 + ADR-028 to Accepted; update `CLAUDE.md` ADR list + package map (`internal/retrieval`), `.capy/AGENTS.md`, `docs/architecture.md`; `capy_index` rationale to `kk:arch-decisions`
- [ ] 10.3 Run `/kk:review-code` with Go as the language input
- [ ] 10.4 Run `/kk:review-spec` to verify implementation matches design + implementation docs
- [ ] 10.5 `make bench-compare BASE=master TARGET=<branch>` — confirm no knowledge.db regression; document the vault-corpus parity (A2) and federated-ranking (A1) results

## Dependency Graph

```
Task 1 ─→ Task 2 ─┐
                  ├─→ Task 5 ─┐
Task 3 ─→ Task 4 ─┘           ├─→ Task 7 ──────────┐
Task 6 ───────────────────────┤                    ├─→ Task 10
                              └─→ Task 8 ─→ Task 9 ─┘
```

Edges (authoritative — the diagram is approximate): `{2,4}→5`; `{5,6}→7`;
`{5,6}→8`; `8→9`; all of `1..9 → 10`. Task 5 does **not** depend on Task 6; Task 9
depends on Task 8 only (not Task 7).
Parallel: Tasks 1∥3∥6, then 2∥4∥6; Task 7 ∥ Task 8.
