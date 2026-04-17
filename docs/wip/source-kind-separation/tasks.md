# Tasks: Source-Kind Separation

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> ADR: [../../adr/017-source-kind-separation.md](../../adr/017-source-kind-separation.md)
> Status: pending
> Created: 2026-04-15

## Task 1: Schema column and type plumbing
- **Status:** done
- **Depends on:** —
- **Docs:** [implementation.md#step-1-schema-and-types](./implementation.md#step-1-schema-and-types)

### Subtasks
- [x] 1.1 Add `kind TEXT NOT NULL DEFAULT 'durable' CHECK (kind IN ('ephemeral', 'durable'))` to the `sources` CREATE TABLE in `internal/store/schema.go`
- [x] 1.2 Add `SourceKind` string alias and `KindDurable`, `KindEphemeral` constants to `internal/store/types.go`, plus a `(k SourceKind) Valid() bool` helper
- [x] 1.3 Add `Kind SourceKind` field to `SourceInfo`, `IndexResult`, and `SourceMeta` structs
- [x] 1.4 Verify `go build ./...` passes with no behavioral change yet

## Task 2: Idempotent migration on store open
- **Status:** done
- **Depends on:** Task 1
- **Docs:** [implementation.md#step-2-migration](./implementation.md#step-2-migration)

### Subtasks
- [x] 2.1 Implement `applyMigrations(db *sql.DB) error` in `internal/store/migrate.go` covering only `017_add_source_kind`. No migrations-tracking table yet — idempotency comes from `PRAGMA table_info(sources)` + the naturally idempotent retroactive `UPDATE`.
- [x] 2.2 Wrap the migration body in `BEGIN IMMEDIATE`. Inside the transaction: (a) `PRAGMA table_info(sources)` — if `kind` column present, `COMMIT` and return; (b) `ALTER TABLE sources ADD COLUMN kind TEXT NOT NULL DEFAULT 'durable'`; (c) retroactive `UPDATE sources SET kind = 'ephemeral' WHERE label LIKE 'execute:%' OR label LIKE 'file:%' OR label LIKE 'batch:%'`; (d) `COMMIT`.
- [x] 2.3 Wire `applyMigrations` into the store-open path with the exact ordering `exec(schemaSQL) → applyMigrations(db) → prepareStatements(db)`. This ordering is load-bearing (Task 3 updates `stmtInsertSource` to reference `kind`).
- [x] 2.4 Test: seed an in-memory DB using raw SQL for the pre-migration DDL (sources table without `kind`) + rows with `execute:`/`file:`/`batch:` and non-prefixed labels; run migration; assert column exists, prefixed rows tagged `ephemeral`, non-prefixed rows stay `durable`; run migration a second time — assert no-op (no errors, no row changes).
- [x] 2.5 Concurrent-migration test: run `applyMigrations` from two goroutines against one in-memory DB; assert no errors and the final row distribution matches the single-run case exactly.

## Task 3: Plumb kind through Index methods and call sites
- **Status:** done
- **Depends on:** Task 2
- **Docs:** [implementation.md#step-3-plumb-kind-into-index](./implementation.md#step-3-plumb-kind-into-index)

### Subtasks
- [x] 3.1 Change `Index`/`IndexPlainText`/`IndexJSON` signatures in `internal/store/index.go` to accept a `SourceKind` parameter; reject `""` with an explicit error
- [x] 3.2 Update `stmtInsertSource` in `internal/store/store.go` to include `kind` in INSERT column list and bind parameter
- [x] 3.3 Extend `stmtFindSourceByLabel` to return `kind`: `SELECT id, content_hash, kind FROM sources WHERE label = ?`. Add a new prepared statement `stmtUpdateSourceKind`: `UPDATE sources SET kind = ?, last_accessed_at = datetime('now') WHERE id = ?`.
- [x] 3.4 In `Index`'s hash-match short-circuit (`index.go:66-78`): if `existingHash == hash` AND `existingKind != newKind`, invoke `stmtUpdateSourceKind` in-place — do NOT delete chunks or re-insert. Return `IndexResult{AlreadyIndexed: true}`. Add a test `TestIndex_PromotesKindInPlace` asserting `chunk_count` and chunk rows are unchanged after promotion.
- [x] 3.5 Update `GetSourceMeta`, `ListSources`, `ClassifySources` to scan the `kind` column
- [x] 3.6 Update `internal/server/tool_execute.go` — pass `store.KindEphemeral` via the intent path
- [x] 3.7 Update `internal/server/tool_execute_file.go` — pass `store.KindEphemeral`
- [x] 3.8 Update `internal/server/tool_batch.go:92` — pass `store.KindEphemeral`
- [x] 3.9 Update `internal/server/intent_search.go:17` — pass `store.KindEphemeral`
- [x] 3.10 Update `internal/server/tool_fetch.go` — pass `store.KindDurable` for all three indexing paths (JSON / HTML-to-markdown / plaintext fallback)
- [x] 3.11 Update `internal/server/tool_index.go:61` — pass `store.KindDurable`
- [x] 3.12 Update existing `internal/store/store_test.go` call sites to pass a kind; add a new `TestIndex_RespectsKind` covering both values

## Task 4: Search default-excludes ephemeral
- **Status:** done
- **Depends on:** Task 3
- **Docs:** [implementation.md#step-4-search-filter](./implementation.md#step-4-search-filter)

### Subtasks
- [x] 4.1 Add `IncludeKinds []SourceKind` to `SearchOptions` in `internal/store/types.go` (array chosen over `bool` to leave room for a future third kind without a breaking change)
- [x] 4.2 In `internal/store/search.go`, derive effective kind filter: if `opts.Source != ""` → no filter (explicit-source override); else if `opts.IncludeKinds` empty → `{KindDurable}`; else → `opts.IncludeKinds`. Thread through `rrfSearch` and `execDynamicSearch`
- [x] 4.3 In `execDynamicSearch` (at `search.go:505`, already joins `sources s`), append `AND s.kind IN (?, …)` bound from the effective filter. Porter and trigram layers share this path; fuzzy correction re-enters via `rrfSearch` and inherits automatically
- [x] 4.4 Add `include_kinds: string[]` argument to the `capy_search` MCP tool schema in `internal/server/tool_search.go`. Validate elements against `{"durable", "ephemeral"}`; reject unknowns with an error that lists the accepted set. Map strings to `SourceKind` constants and wire into `SearchOptions.IncludeKinds`
- [x] 4.5 Strengthen the zero-results guidance in `tool_search.go`: when 0 results AND ephemeral was excluded, run `SELECT COUNT(*) FROM sources WHERE kind = 'ephemeral'`. If non-zero, surface both recovery paths explicitly: `include_kinds: ["durable","ephemeral"]` and `source: "execute:<lang>"` / `source: "file:<path>"` / `source: "batch:…"` with a concrete example
- [x] 4.6 Store-level test: mixed DB (1 durable + 1 ephemeral row both matching the query) — `SearchOptions{}` returns only durable; `IncludeKinds: []SourceKind{KindDurable, KindEphemeral}` returns both; explicit `Source: "execute:shell"` returns ephemeral regardless of `IncludeKinds`
- [x] 4.7 Store-level test: fuzzy-correction query over a misspelling returns kind-filtered results — assert no ephemeral leakage when `IncludeKinds` is empty
- [x] 4.8 Integration test in `internal/server/tool_knowledge_test.go`: invoke `handleSearch` against a mixed-corpus store. Default call → ephemeral excluded. `include_kinds: ["ephemeral"]` → only ephemeral. Unknown value → error naming accepted set
- [x] 4.9 Integration test (session-recovery journey): `capy_execute(code=…, intent=…)` to index ephemeral content. Then `capy_search(query=<matching phrase>)` with no source filter → assert zero results AND the message names both recovery paths. Then same query with `include_kinds: ["ephemeral"]` → assert the ephemeral row appears

## Task 5: Split cleanup into durable and ephemeral paths
- **Status:** done
- **Depends on:** Task 3
- **Docs:** [implementation.md#step-5-cleanup-split](./implementation.md#step-5-cleanup-split)

### Subtasks
- [x] 5.1 Add `EphemeralTTLHours int` (default 24) to `config.CleanupConfig` in `internal/config/config.go` (TOML key `ephemeral_ttl_hours` under `[store.cleanup]`, alongside `cold_threshold_days`). In the loader, reject values `< 1` with error `"[store.cleanup] ephemeral_ttl_hours must be >= 1 (use capy_cleanup purge_ephemeral=true for one-shot aggressive purging)"`
- [x] 5.2 Add `EvictionReason string` field to `SourceInfo` (values `"retention"`, `"ttl"`)
- [x] 5.3 Extract current retention-based logic from `Cleanup` into a helper `cleanupDurable`
- [x] 5.4 Implement `cleanupEphemeral(ttl time.Duration, dryRun bool)` — pure TTL eviction via `indexed_at < datetime('now', '-N hours')`, ignores `access_count`, same transactional deletion shape
- [x] 5.5 Rewrite `Cleanup(dryRun bool)` to call both helpers, merge results, tag each `SourceInfo` with its `EvictionReason`
- [x] 5.6 Update `internal/server/tool_cleanup.go` to render an `EvictionReason` column in the output table
- [x] 5.7 Test: seeded mixed DB — old ephemeral rows evicted with reason `ttl`; young ephemeral rows preserved; ephemeral row with `access_count = 5` but age > TTL is still evicted (this is the immortality-gate fix test); durable retention logic unchanged from current behavior
- [x] 5.8 Config-loader test: `ephemeral_ttl_hours = 0` and `ephemeral_ttl_hours = -1` both fail with the documented error; `ephemeral_ttl_hours = 1` loads successfully

## Task 6: Per-kind statistics
- **Status:** pending
- **Depends on:** Task 5
- **Docs:** [implementation.md#step-6-stats](./implementation.md#step-6-stats)

### Subtasks
- [ ] 6.1 In `StoreStats` (`internal/store/types.go`), rename `HotCount`/`WarmCount`/`ColdCount`/`EvictableCount` → `DurableHotCount`/`DurableWarmCount`/`DurableColdCount`/`DurableEvictableCount`. Do NOT keep the old names as duplicates
- [ ] 6.2 Add `DurableSourceCount`, `EphemeralSourceCount`, `EphemeralFreshCount`, `EphemeralStaleCount` fields
- [ ] 6.3 Update `Stats()`/`ClassifySources()` in `internal/store/cleanup.go` — `classifyTier` applies only to `kind = 'durable'` rows (populating the `Durable*` tier counts); for `kind = 'ephemeral'` bucket as `fresh` (within TTL) or `stale` (past TTL)
- [ ] 6.4 Grep the codebase for `HotCount`, `WarmCount`, `ColdCount`, `EvictableCount` (unprefixed) and update every reader in lockstep. `internal/server/tool_stats.go` is the primary one; tests may also reference them
- [ ] 6.5 Update `internal/server/tool_stats.go` to render two sections: durable tiers (renamed fields) + ephemeral fresh/stale
- [ ] 6.6 Test: `Stats()` on a mixed DB returns correct per-kind counts; `Durable*` tiers reflect only durable rows; `Ephemeral*` counts reflect only ephemeral rows

## Task 7: `purge_ephemeral` convenience flag on capy_cleanup
- **Status:** pending
- **Depends on:** Task 5
- **Docs:** [implementation.md#step-7-tool-cleanup-purge-flag](./implementation.md#step-7-tool-cleanup-purge-flag)

### Subtasks
- [ ] 7.1 Add `purge_ephemeral bool` to the `capy_cleanup` MCP tool schema
- [ ] 7.2 When set, `Cleanup` runs only `cleanupEphemeral` and skips `cleanupDurable` — implement via a new method `PurgeEphemeral(dryRun bool)` or via an options struct
- [ ] 7.3 Test: `purge_ephemeral: true` leaves durable rows with low retention scores untouched

## Task 8: Configuration and documentation
- **Status:** pending
- **Depends on:** Task 5, Task 7
- **Docs:** [implementation.md#step-8-config-docs](./implementation.md#step-8-config-docs)

### Subtasks
- [ ] 8.1 Document `[store.cleanup] ephemeral_ttl_hours` in the example `.capy.toml` and in `internal/config/config.go` Go doc — default 24, minimum 1, meaning, and the trade-off (longer = more intra-session recall, shorter = less DB churn)
- [ ] 8.2 Document `include_kinds` in the `capy_search` tool description — accepted values (`"durable"`, `"ephemeral"`), default behavior (durable only), and the session-recovery use case
- [ ] 8.3 Document `purge_ephemeral` in the `capy_cleanup` tool description
- [ ] 8.4 Release note in the repo's changelog/release process: explicit behavior-change bullet ("`capy_search` now excludes ephemeral sources by default") + explicit recovery-path bullet naming BOTH `include_kinds: ["durable","ephemeral"]` AND `source: "execute:<lang>"` / `"file:<path>"` / `"batch:…"`

## Task 9: Final verification
- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3, Task 4, Task 5, Task 6, Task 7, Task 8

### Subtasks
- [ ] 9.1 Run `kk:test` skill — verify full unit + integration test suite, covering migration, search-default, cleanup split, stats, tool-schema
- [ ] 9.2 Run `kk:document` skill — ensure README, ADR cross-references, and tool-schema descriptions are consistent
- [ ] 9.3 Run `kk:review-code` skill with Go as language input to audit the implementation
- [ ] 9.4 Run `kk:review-spec` skill to verify the implementation matches design.md, implementation.md, and ADR-017
