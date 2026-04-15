# Tasks: Source-Kind Separation

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> ADR: [../../adr/017-source-kind-separation.md](../../adr/017-source-kind-separation.md)
> Status: pending
> Created: 2026-04-15

## Task 1: Schema column and type plumbing
- **Status:** pending
- **Depends on:** —
- **Docs:** [implementation.md#step-1-schema-and-types](./implementation.md#step-1-schema-and-types)

### Subtasks
- [ ] 1.1 Add `kind TEXT NOT NULL DEFAULT 'durable' CHECK (kind IN ('ephemeral', 'durable'))` to the `sources` CREATE TABLE in `internal/store/schema.go`
- [ ] 1.2 Add `SourceKind` string alias and `KindDurable`, `KindEphemeral` constants to `internal/store/types.go`, plus a `(k SourceKind) Valid() bool` helper
- [ ] 1.3 Add `Kind SourceKind` field to `SourceInfo`, `IndexResult`, and `SourceMeta` structs
- [ ] 1.4 Verify `go build ./...` passes with no behavioral change yet

## Task 2: Idempotent migration on store open
- **Status:** pending
- **Depends on:** Task 1
- **Docs:** [implementation.md#step-2-migration](./implementation.md#step-2-migration)

### Subtasks
- [ ] 2.1 Create `_capy_migrations` table (name PRIMARY KEY, applied_at TEXT) in schema or migration file
- [ ] 2.2 Implement `applyMigrations(db *sql.DB) error` in `internal/store/migrate.go` — reads applied migrations, runs pending ones in order, records completion
- [ ] 2.3 Implement migration `017_add_source_kind`: check column existence via `PRAGMA table_info(sources)`; if absent, run `ALTER TABLE sources ADD COLUMN kind TEXT NOT NULL DEFAULT 'durable'` + retroactive `UPDATE` (tagging `execute:%`/`file:%`/`batch:%` as ephemeral) + `CREATE INDEX IF NOT EXISTS idx_sources_kind` — all in one transaction
- [ ] 2.4 Call `applyMigrations` from the store-open path after `schemaSQL` exec
- [ ] 2.5 Test: seed an in-memory DB with pre-ALTER schema + rows that match prefix patterns; assert post-migration column exists, prefixed rows are tagged `ephemeral`, unprefixed rows stay `durable`, running the migration a second time is a no-op

## Task 3: Plumb kind through Index methods and call sites
- **Status:** pending
- **Depends on:** Task 2
- **Docs:** [implementation.md#step-3-plumb-kind-into-index](./implementation.md#step-3-plumb-kind-into-index)

### Subtasks
- [ ] 3.1 Change `Index`/`IndexPlainText`/`IndexJSON` signatures in `internal/store/index.go` to accept a `SourceKind` parameter; reject `""` with an explicit error
- [ ] 3.2 Update `stmtInsertSource` in `internal/store/store.go` to include `kind` in INSERT column list and bind parameter
- [ ] 3.3 Update dedup path in `Index` to treat a differing stored `kind` as a content change (force re-index with new kind)
- [ ] 3.4 Update `GetSourceMeta`, `ListSources`, `ClassifySources` to scan the `kind` column
- [ ] 3.5 Update `internal/server/tool_execute.go` — pass `store.KindEphemeral` via the intent path
- [ ] 3.6 Update `internal/server/tool_execute_file.go` — pass `store.KindEphemeral`
- [ ] 3.7 Update `internal/server/tool_batch.go:92` — pass `store.KindEphemeral`
- [ ] 3.8 Update `internal/server/intent_search.go:17` — pass `store.KindEphemeral`
- [ ] 3.9 Update `internal/server/tool_fetch.go` — pass `store.KindDurable` for all three indexing paths (JSON / HTML-to-markdown / plaintext fallback)
- [ ] 3.10 Update `internal/server/tool_index.go:61` — pass `store.KindDurable`
- [ ] 3.11 Update existing `internal/store/store_test.go` call sites to pass a kind; add a new `TestIndex_RespectsKind` covering both values

## Task 4: Search default-excludes ephemeral
- **Status:** pending
- **Depends on:** Task 3
- **Docs:** [implementation.md#step-4-search-filter](./implementation.md#step-4-search-filter)

### Subtasks
- [ ] 4.1 Add `IncludeEphemeral bool` to `SearchOptions` in `internal/store/types.go`
- [ ] 4.2 In `internal/store/search.go`, compute `applyKindFilter := !opts.IncludeEphemeral && opts.Source == ""`; thread through `rrfSearch` and `execDynamicSearch`
- [ ] 4.3 Append `AND s.kind = 'durable'` to Porter, trigram, and fuzzy-Levenshtein query paths conditional on `applyKindFilter`; add the `sources` join where missing
- [ ] 4.4 Add `include_ephemeral` argument to the `capy_search` MCP tool schema and wire into `SearchOptions` in `internal/server/tool_search.go`
- [ ] 4.5 Update the empty-result guidance text in `tool_search.go` to mention `include_ephemeral: true` and explicit `source:` as alternatives
- [ ] 4.6 Test: mixed DB (1 durable + 1 ephemeral row both matching the query) — default search returns only durable; `IncludeEphemeral: true` returns both; explicit `Source: "execute:shell"` returns ephemeral even with `IncludeEphemeral: false`

## Task 5: Split cleanup into durable and ephemeral paths
- **Status:** pending
- **Depends on:** Task 3
- **Docs:** [implementation.md#step-5-cleanup-split](./implementation.md#step-5-cleanup-split)

### Subtasks
- [ ] 5.1 Add `EphemeralTTLHours int` (default 24) to `config.Config.Store` in `internal/config/config.go`; reject negative values at load time
- [ ] 5.2 Add `EvictionReason string` field to `SourceInfo` (values `"retention"`, `"ttl"`)
- [ ] 5.3 Extract current retention-based logic from `Cleanup` into a helper `cleanupDurable`
- [ ] 5.4 Implement `cleanupEphemeral(ttl time.Duration, dryRun bool)` — pure TTL eviction via `indexed_at < datetime('now', '-N hours')`, ignores `access_count`, same transactional deletion shape
- [ ] 5.5 Rewrite `Cleanup(dryRun bool)` to call both helpers, merge results, tag each `SourceInfo` with its `EvictionReason`
- [ ] 5.6 Update `internal/server/tool_cleanup.go` to render an `EvictionReason` column in the output table
- [ ] 5.7 Test: seeded mixed DB — old ephemeral rows evicted with reason `ttl`; young ephemeral rows preserved; ephemeral row with `access_count = 5` but age > TTL is still evicted (this is the immortality-gate fix test); durable retention logic unchanged from current behavior
- [ ] 5.8 Test: `ephemeral_ttl_hours = 0` purges all ephemeral rows regardless of age

## Task 6: Per-kind statistics
- **Status:** pending
- **Depends on:** Task 5
- **Docs:** [implementation.md#step-6-stats](./implementation.md#step-6-stats)

### Subtasks
- [ ] 6.1 Extend `StoreStats` in `internal/store/types.go` with `DurableSourceCount`, `EphemeralSourceCount`, per-kind tier/age counts per design doc
- [ ] 6.2 Update `Stats()`/`ClassifySources()` in `internal/store/cleanup.go` — `classifyTier` applies only to `durable`; for `ephemeral`, bucket as `fresh` or `stale` (past TTL)
- [ ] 6.3 Update `internal/server/tool_stats.go` to render a durable-vs-ephemeral section alongside the existing tier table
- [ ] 6.4 Test: `Stats()` on a mixed DB returns correct per-kind counts and tier distribution is durable-only

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
- [ ] 8.1 Document `ephemeral_ttl_hours` in the example `.capy.toml` and in `internal/config/config.go` Go doc
- [ ] 8.2 Document `include_ephemeral` in the `capy_search` tool description
- [ ] 8.3 Document `purge_ephemeral` in the `capy_cleanup` tool description
- [ ] 8.4 Add a release note entry describing the default behavior change for `capy_search`

## Task 9: Final verification
- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3, Task 4, Task 5, Task 6, Task 7, Task 8

### Subtasks
- [ ] 9.1 Run `kk:test` skill — verify full unit + integration test suite, covering migration, search-default, cleanup split, stats, tool-schema
- [ ] 9.2 Run `kk:document` skill — ensure README, ADR cross-references, and tool-schema descriptions are consistent
- [ ] 9.3 Run `kk:review-code` skill with Go as language input to audit the implementation
- [ ] 9.4 Run `kk:review-spec` skill to verify the implementation matches design.md, implementation.md, and ADR-017
