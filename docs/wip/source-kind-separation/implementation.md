# Implementation: Source-Kind Separation

**Design:** [./design.md](./design.md)
**ADR:** [../../adr/017-source-kind-separation.md](../../adr/017-source-kind-separation.md)
**Tasks:** [./tasks.md](./tasks.md)

This plan is ordered so each step leaves the build green and tests passing. Commits should map 1:1 to tasks where practical. Each step names *how to verify it*.

## Prerequisites

- Read `internal/store/` thoroughly before touching code — `Index`, `SearchWithFallback`, `Cleanup`, and prepared-statement initialization in `store.go` are tightly coupled.
- Read ADR-006, ADR-007, ADR-011, ADR-012, and ADR-017 for the history.
- Skim `internal/server/tool_batch.go`, `tool_execute.go`, `tool_execute_file.go`, `tool_fetch.go`, `tool_index.go`, `tool_search.go`, `tool_cleanup.go`, `tool_stats.go`, and `intent_search.go` for current behavior.
- The project uses Go 1.22+, Ginkgo-style testify tests, `mcp-go` for the MCP server, and `github.com/mattn/go-sqlite3`.

## Implementation order

### <a id="step-1-schema-and-types"></a>Step 1 — Schema change and type plumbing

**Files:** `internal/store/schema.go`, `internal/store/types.go`.

- Add the `kind` column to the `sources` CREATE TABLE DDL with the default and CHECK constraint described in design.md §Data model.
- Add a `SourceKind` string-typed alias in `types.go` with two exported constants: `KindDurable`, `KindEphemeral`. Include validation helper `(k SourceKind) Valid() bool` and use it at boundaries.
- Add a `Kind SourceKind` field to `SourceInfo`, `IndexResult`, and `SourceMeta`.

**Step → verify:** `go build ./...` passes. No test changes yet — tests compile because nothing consumes `Kind` yet.

### <a id="step-2-migration"></a>Step 2 — One-shot migration on store open

**Files:** `internal/store/store.go`, new file `internal/store/migrate.go` (or inline into `store.go` if under ~40 LOC).

- Implement `applyMigrations(db *sql.DB) error` covering only the one migration needed right now (`017_add_source_kind`). No migrations-tracking table yet — `PRAGMA table_info(sources)` gives idempotency for free, and the retroactive `UPDATE` is itself idempotent (tagging already-`ephemeral` rows as `ephemeral` is a no-op). Add a tracking table when the second migration lands.
- Wrap the migration in a single transaction using `BEGIN IMMEDIATE` so SQLite acquires the reserved-write lock up-front. Two concurrent capy processes opening a pre-migration DB both attempt migration; the second acquires the lock after the first commits, re-checks `PRAGMA table_info`, sees the column exists, and returns without writing.
- Inside the transaction:
  1. `PRAGMA table_info(sources)` — scan for the `kind` column. If present, `COMMIT` and return (no-op).
  2. `ALTER TABLE sources ADD COLUMN kind TEXT NOT NULL DEFAULT 'durable'`.
  3. Retroactive `UPDATE sources SET kind = 'ephemeral' WHERE label LIKE 'execute:%' OR label LIKE 'file:%' OR label LIKE 'batch:%'`.
  4. `COMMIT`.
- **Do not add a `kind` index.** See design.md §Data model > Index strategy. Revisit only if query plans show a scan after the feature ships.
- **Store-open ordering is load-bearing:** the init path must be `exec(schemaSQL) → applyMigrations(db) → prepareStatements(db)`. Prepared statements reference the new `kind` column (per Step 3), so preparing before migration fails on existing pre-migration DBs.

**Step → verify:**
- `go test ./internal/store/...` — add a test that opens an in-memory DB populated by raw SQL matching the pre-migration DDL (sources table without `kind`, plus a handful of rows with `execute:`/`file:`/`batch:`/non-prefixed labels), runs migration, and asserts (a) column exists, (b) prefixed rows are `ephemeral`, (c) non-prefixed rows stay `durable`, (d) running the migration again is a no-op.
- Add a concurrent-migration test: run `applyMigrations` from two goroutines against one in-memory DB, assert no errors, column present, retroactive `UPDATE` applied exactly once in effect (count of `ephemeral` rows matches pre-seeded prefixed rows).
- `capy_doctor` (via MCP) on an existing dev DB shows no errors.

### <a id="step-3-plumb-kind-into-index"></a>Step 3 — Plumb `kind` through the index methods

**Files:** `internal/store/index.go`, `internal/store/store.go`, `internal/store/cleanup.go`, all callers in `internal/server/`.

- Change `Index(content, label, contentType string)` signature to `Index(content, label, contentType string, kind SourceKind)`. Do the same for `IndexPlainText` and `IndexJSON`.
- Update the prepared statement `stmtInsertSource` to include `kind` in the column list and bind parameter. If `kind` is the zero value, this is a bug — validate at the entrypoint and return an error.
- **Promotion without re-chunking:** extend the existing hash-match short-circuit in `Index` (`index.go:66-78`). When `existingHash == hash` AND `existingKind != kind`, run `UPDATE sources SET kind = ?, last_accessed_at = datetime('now') WHERE id = ?` (add a new prepared statement `stmtUpdateSourceKind`). Do NOT delete + re-insert chunks — content is identical by hash. `IndexResult.AlreadyIndexed` stays `true` in this path (caller-visible: "same content, just promoted/demoted"). Only when the hash genuinely differs do we fall through to the delete-and-reinsert path.
- Extend `stmtFindSourceByLabel` to also return `kind`: `SELECT id, content_hash, kind FROM sources WHERE label = ?`.
- Thread the new parameter through every caller:
  - `tool_execute.go` / `tool_execute_file.go` / `tool_batch.go` → pass `store.KindEphemeral`.
  - `intent_search.go` → pass `store.KindEphemeral`.
  - `tool_fetch.go` → pass `store.KindDurable`.
  - `tool_index.go` → pass `store.KindDurable`.
- Update `GetSourceMeta`, `ListSources`, `ClassifySources` to return `Kind` in their row scans.

**Step → verify:**
- `go build ./...` passes (compilation catches missed callers).
- Existing unit tests in `internal/store/store_test.go` still pass (after updating their calls to pass a kind).
- Add a new test `TestIndex_RespectsKind` covering both kinds.

### <a id="step-4-search-filter"></a>Step 4 — Default-exclude ephemeral from search

**Files:** `internal/store/search.go`, `internal/store/types.go`, `internal/server/tool_search.go`.

- Add `IncludeKinds []SourceKind` to `SearchOptions`. Array (not bool) is chosen so a future third kind can be added without a breaking change. Empty/nil means "default: durable only."
- In `SearchWithFallback`, derive the effective kind filter: if `opts.Source != ""`, apply no kind filter (explicit-source override, preserves intra-batch `SourceMatchMode: "exact"`); else if `opts.IncludeKinds` is empty, filter to `{KindDurable}`; else filter to `opts.IncludeKinds`.
- In `execDynamicSearch` (at `search.go:505`, which already joins `sources s`), append `AND s.kind IN (?, ?, …)` when the effective kind filter is set. Both Porter and trigram layers pick this up automatically since they share `execDynamicSearch`.
- Fuzzy correction (`fuzzyCorrectQuery`) operates on the `vocabulary` table and re-enters `rrfSearch`, which calls `execDynamicSearch` — the kind filter propagates automatically. No bespoke work needed in the fuzzy path; verify by tracing one corrected-query test.
- In `tool_search.go`, add `include_kinds` (JSON array of strings) to the tool argument schema. Validate elements against the allowed set (`"durable"`, `"ephemeral"`); reject unknown values with an error that lists the accepted set. Wire into `SearchOptions.IncludeKinds` by mapping strings to `SourceKind` constants.
- **Strengthen the "no results" fallback** in `tool_search.go`: when zero results AND the filter excluded ephemeral (i.e., `opts.Source == ""` and `IncludeKinds` didn't include `KindEphemeral`), run a secondary count — `SELECT COUNT(*) FROM sources WHERE kind = 'ephemeral'`. If non-zero, surface both recovery paths explicitly: *"N ephemeral sources present but excluded by default. Retry with `include_kinds: ["durable","ephemeral"]` or an explicit `source:` filter (e.g., `source: "execute:shell"`) to include command output."*

**Step → verify:**
- Unit test: seed a DB with 1 durable and 1 ephemeral row; `SearchWithFallback(…, SearchOptions{})` returns only the durable row; `SearchWithFallback(…, SearchOptions{IncludeKinds: []SourceKind{KindDurable, KindEphemeral}})` returns both.
- Unit test: `SearchWithFallback(…, SearchOptions{Source: "execute:shell"})` returns the ephemeral row regardless of `IncludeKinds`.
- Unit test: fuzzy correction over a mis-spelled query returns kind-filtered results (i.e., correction doesn't leak ephemeral into durable-only search).
- Integration test in `tool_knowledge_test.go`: `capy_search(query=…)` returns durable-only by default; with `include_kinds: ["ephemeral"]` returns ephemeral only.
- Integration test (session-recovery journey): `capy_execute(intent=…)` → `capy_search(query=<matching phrase>)` returns zero results AND the error message names both `include_kinds` and explicit `source:` recovery paths.

### <a id="step-5-cleanup-split"></a>Step 5 — Split cleanup into durable + ephemeral paths

**Files:** `internal/store/cleanup.go`, `internal/store/types.go`, `internal/config/config.go`, `internal/server/tool_cleanup.go`.

- Add `EphemeralTTLHours int` to `config.CleanupConfig` (under `[store.cleanup]` — it's an eviction knob, alongside the existing `ColdThresholdDays`). Default `24`. In the config loader, reject `< 1` with a clear error: `"ephemeral_ttl_hours must be >= 1 (use capy_cleanup purge_ephemeral=true for one-shot aggressive purging)"`. Zero does not mean "disabled" and it does not mean "purge everything" — both interpretations are traps.
- In `cleanup.go`:
  - Add `cleanupEphemeral(db *sql.DB, ttl time.Duration, dryRun bool) ([]SourceInfo, error)` — pure TTL eviction, ignores `access_count`.
  - Keep the existing retention-score path as `cleanupDurable` (extract from current `Cleanup`).
  - Rewrite `Cleanup(dryRun bool)` to call both and merge results. The merged slice tags each `SourceInfo` with its eviction reason (new field `EvictionReason string` — values `"retention"` or `"ttl"`).
- Update `tool_cleanup.go` to render the `EvictionReason` column in its markdown table.

**Step → verify:**
- Unit test: seed mixed DB (durable + ephemeral, various ages and access counts). Run `Cleanup(dryRun=true)`. Assert (a) old ephemeral rows appear with reason `ttl`, (b) young ephemeral rows do not, even with `access_count = 0`, (c) durable-retention logic matches prior behavior.
- Unit test: ephemeral row with `access_count = 5` and age > TTL is still evicted. This is the test that proves the immortality-gate fix.
- Config-loader test: `ephemeral_ttl_hours = 0` fails to load with the documented error. Negative values likewise.
- Integration test: `capy_cleanup dry_run=false` on a seeded DB removes the expected rows.

### <a id="step-6-stats"></a>Step 6 — Per-kind stats

**Files:** `internal/store/cleanup.go` (where `Stats` lives), `internal/store/types.go`, `internal/server/tool_stats.go`, `internal/server/stats.go`.

- In `StoreStats`, **rename** the existing `HotCount`/`WarmCount`/`ColdCount`/`EvictableCount` fields to `DurableHotCount`/`DurableWarmCount`/`DurableColdCount`/`DurableEvictableCount` — do not duplicate. Retention scoring only runs on durable rows, so the original names would be silently misleading after this change.
- Add `DurableSourceCount`, `EphemeralSourceCount`, `EphemeralFreshCount`, `EphemeralStaleCount` (see design.md §Stats changes for the exact shape).
- Populate via `ClassifySources` (which already walks all sources). For `kind == KindDurable` run `classifyTier` as today. For `kind == KindEphemeral` bucket into `fresh` (`indexed_at >= now − TTL`) vs `stale` (past TTL, awaiting cleanup).
- Update every reader of the renamed fields in lockstep: `internal/server/tool_stats.go` and any stats test. Grep for `HotCount`, `WarmCount`, `ColdCount`, `EvictableCount` before the commit to ensure no stragglers.
- Update `tool_stats.go` to render a two-section table: durable tiers (using the renamed fields) and ephemeral fresh/stale counts.

**Step → verify:**
- Unit test: `Stats()` on a seeded mixed DB returns correct per-kind counts and correct `Durable*` tier classification.
- `go build ./...` catches any missed field renames.
- Run `capy_stats` via MCP on a dev DB and eyeball the output.

### <a id="step-7-tool-cleanup-purge-flag"></a>Step 7 — `capy_cleanup --purge-ephemeral` convenience flag

**Files:** `internal/server/tool_cleanup.go`, `internal/store/cleanup.go`.

- Add `purge_ephemeral bool` to the MCP tool schema. When true, skip the durable path entirely and only run `cleanupEphemeral`.
- This is a minor convenience — the default Cleanup path already handles ephemeral TTL. Ship it because users may want a fast "clear scratch" operation without touching durable retention logic.

**Step → verify:**
- Unit test: `Cleanup` with `purgeEphemeralOnly=true` leaves durable rows untouched regardless of retention score.

### <a id="step-8-config-docs"></a>Step 8 — Config documentation

**Files:** `internal/config/config.go` (Go doc comments), `.capy.toml.example` or equivalent, `README.md` if it covers config.

- Document `[store.cleanup] ephemeral_ttl_hours` with its default (24), meaning, trade-off (longer = more intra-session recall, shorter = less DB churn), and the `>= 1` constraint (values `< 1` rejected at load time with an error that points users to `purge_ephemeral`).
- Document `capy_search`'s new `include_kinds` argument — accepted values, default behavior, and the recovery path for command-output lookups.
- Document `capy_cleanup`'s new `purge_ephemeral` argument.
- Release note in the repo's changelog/release process: explicit behavior-change bullet + explicit recovery-path bullet (both paths named).

**Step → verify:**
- `go doc ./internal/config` shows the new field under `CleanupConfig`.
- README renders correctly locally.

### <a id="step-9-verification-and-review"></a>Step 9 — Full verification and review

Run the `test`, `document`, `review-code`, and `review-spec` skills per the task list. Catch any missed write sites, stale docs, or spec drift.

## Cross-cutting concerns

### Testing hygiene

- Table-driven tests for each new path. Do not mock `time.Now` at the store layer — TTL tests should seed rows with SQL `datetime('now', '-48 hours')` so they exercise real clock behavior.
- Any test that opens a store must exercise the migration path by default (i.e., `store.Open` always runs `applyMigrations`).

### Backward compatibility

- Existing `.capy.toml` files without `ephemeral_ttl_hours` fall back to the default.
- Existing `capy_search` callers that pass no new arguments see the new default behavior (ephemeral excluded). This is a **behavior change** — flag in the release note.
- Existing DB files are migrated on first open. No downtime; migration is sub-millisecond for realistic DB sizes.

### Failure modes to exercise

- Migration fails halfway through (power loss between `ALTER TABLE` and `UPDATE`) — both statements live inside one `BEGIN IMMEDIATE` transaction; either both apply or neither.
- Two capy processes open a pre-migration DB concurrently — `BEGIN IMMEDIATE` serializes; the second process's `PRAGMA table_info` re-check returns early. Covered by the concurrent-migration test in Step 2 verification.
- User sets `ephemeral_ttl_hours = 0` or a negative value — rejected at config load with a clear error message pointing to `purge_ephemeral`. Zero is not treated as "disabled" (there is no disabled mode) and not as "purge everything" (that's a tool invocation, not a config).
- Older capy binary opens a post-migration DB — safe. Older `stmtInsertSource` uses an explicit column list that doesn't include `kind`; INSERTs get the `DEFAULT 'durable'` value. Older `SELECT` statements don't read `kind`, so the extra column is invisible. No downgrade breakage.

### Code-review checklist (for the reviewer)

- [ ] Every call to `Index` / `IndexPlainText` / `IndexJSON` in `internal/server/` passes a kind explicitly. No defaults at the store API.
- [ ] `SearchOptions{}` (zero value) with empty `Source` and empty `IncludeKinds` results in a `kind = 'durable'` filter.
- [ ] `cleanup.go` has two clearly separated paths — no shared `access_count` gate.
- [ ] Migration is idempotent, transactional, and uses `BEGIN IMMEDIATE` with an inside-transaction `PRAGMA table_info` re-check.
- [ ] Store-open path runs `schemaSQL` → `applyMigrations` → `prepareStatements` in that order.
- [ ] Hash-match short-circuit in `Index` promotes via `UPDATE`, does NOT re-chunk.
- [ ] No `HotCount`/`WarmCount`/`ColdCount`/`EvictableCount` stragglers — all renamed to `Durable*`.
- [ ] `include_kinds` array rejects unknown values at the tool boundary.
- [ ] `ephemeral_ttl_hours < 1` rejected at config load.
- [ ] New tool arguments are documented in the MCP tool schema (JSON schema `description` fields), not just in Go comments.
