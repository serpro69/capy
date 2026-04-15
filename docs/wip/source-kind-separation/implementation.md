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

- Introduce a `_capy_migrations` table (`CREATE TABLE IF NOT EXISTS _capy_migrations (name TEXT PRIMARY KEY, applied_at TEXT DEFAULT CURRENT_TIMESTAMP)`).
- Implement `applyMigrations(db *sql.DB)` that runs pending migrations idempotently. Only migration for now: `017_add_source_kind`.
  - Check `PRAGMA table_info(sources)` for the `kind` column. If absent, `ALTER TABLE sources ADD COLUMN kind TEXT NOT NULL DEFAULT 'durable'`.
  - Run the retroactive `UPDATE` tagging rows with `execute:%`, `file:%`, `batch:%` labels as `ephemeral`.
  - Insert a row in `_capy_migrations`.
- Call `applyMigrations` from `openDB`/`init` after `schemaSQL` exec.
- Add `CREATE INDEX IF NOT EXISTS idx_sources_kind ON sources(kind)` to the migration.

**Step → verify:**
- `go test ./internal/store/...` — add a test that opens an in-memory DB seeded with the pre-migration schema, runs migration, and asserts (a) column exists, (b) pre-seeded rows with `execute:` label are now `ephemeral`, (c) running again is a no-op.
- `capy_doctor` (via MCP) on an existing dev DB shows no errors.

### <a id="step-3-plumb-kind-into-index"></a>Step 3 — Plumb `kind` through the index methods

**Files:** `internal/store/index.go`, `internal/store/store.go`, `internal/store/cleanup.go`, all callers in `internal/server/`.

- Change `Index(content, label, contentType string)` signature to `Index(content, label, contentType string, kind SourceKind)`. Do the same for `IndexPlainText` and `IndexJSON`.
- Update the prepared statement `stmtInsertSource` to include `kind` in the column list and bind parameter. If `kind` is the zero value, this is a bug — validate at the entrypoint and return an error.
- Update dedup path: if `existingKind != kind`, treat as content change (force re-index). Put this behind a helper so the rule is obvious.
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

- Add `IncludeEphemeral bool` to `SearchOptions`.
- In `SearchWithFallback`, derive `applyKindFilter := !opts.IncludeEphemeral && opts.Source == ""`. Thread this through `rrfSearch` and `execDynamicSearch`.
- In every SQL query that selects from `chunks` or `chunks_trigram`, join the `sources` table if not already joined and append `AND s.kind = 'durable'` conditionally. The fuzzy Levenshtein path is the trickiest — read it carefully.
- In `tool_search.go`, add `include_ephemeral` to the tool argument schema and wire it into `SearchOptions`.
- Update `tool_search.go`'s "no results" fallback message to mention: "Ephemeral sources excluded by default. Pass `include_ephemeral: true` to include command output."

**Step → verify:**
- Unit test: seed a DB with 1 durable and 1 ephemeral row; `SearchWithFallback(…, SearchOptions{})` returns only the durable row; `SearchWithFallback(…, SearchOptions{IncludeEphemeral: true})` returns both.
- Unit test: `SearchWithFallback(…, SearchOptions{Source: "ephemeral-label"})` returns the ephemeral row even without `IncludeEphemeral`.
- Integration test: `capy_search` via the MCP server returns durable-only by default.

### <a id="step-5-cleanup-split"></a>Step 5 — Split cleanup into durable + ephemeral paths

**Files:** `internal/store/cleanup.go`, `internal/store/types.go`, `internal/config/config.go`, `internal/server/tool_cleanup.go`.

- Add `EphemeralTTLHours int` to `config.Config.Store` with default `24`. Document in the config file and loader.
- In `cleanup.go`:
  - Add `cleanupEphemeral(db *sql.DB, ttl time.Duration, dryRun bool) ([]SourceInfo, error)` — pure TTL eviction, ignores `access_count`.
  - Keep the existing retention-score path as `cleanupDurable` (extract from current `Cleanup`).
  - Rewrite `Cleanup(dryRun bool)` to call both and merge results. The merged slice tags each `SourceInfo` with its eviction reason (new field `EvictionReason string` — values `"retention"` or `"ttl"`).
- Update `tool_cleanup.go` to render the `EvictionReason` column in its markdown table.

**Step → verify:**
- Unit test: seed mixed DB (durable + ephemeral, various ages and access counts). Run `Cleanup(dryRun=true)`. Assert (a) old ephemeral rows appear with reason `ttl`, (b) young ephemeral rows do not, even with `access_count = 0`, (c) durable-retention logic matches prior behavior.
- Unit test: ephemeral row with `access_count = 5` and age > TTL is still evicted. This is the test that proves the immortality-gate fix.
- Integration test: `capy_cleanup dry_run=false` on a seeded DB removes the expected rows.

### <a id="step-6-stats"></a>Step 6 — Per-kind stats

**Files:** `internal/store/cleanup.go` (where `Stats` lives), `internal/store/types.go`, `internal/server/tool_stats.go`, `internal/server/stats.go`.

- Extend `StoreStats` with the fields described in design.md §Stats changes.
- Populate them by extending `ClassifySources` (which already walks all sources) — only run `classifyTier` for durable rows; for ephemeral, bucket into `fresh` vs `stale` (age > TTL).
- Update `tool_stats.go` to render a two-column table (durable tiers | ephemeral counts).

**Step → verify:**
- Unit test: `Stats()` on a seeded mixed DB returns correct per-kind counts.
- Run `capy_stats` via MCP on a dev DB and eyeball the output.

### <a id="step-7-tool-cleanup-purge-flag"></a>Step 7 — `capy_cleanup --purge-ephemeral` convenience flag

**Files:** `internal/server/tool_cleanup.go`, `internal/store/cleanup.go`.

- Add `purge_ephemeral bool` to the MCP tool schema. When true, skip the durable path entirely and only run `cleanupEphemeral`.
- This is a minor convenience — the default Cleanup path already handles ephemeral TTL. Ship it because users may want a fast "clear scratch" operation without touching durable retention logic.

**Step → verify:**
- Unit test: `Cleanup` with `purgeEphemeralOnly=true` leaves durable rows untouched regardless of retention score.

### <a id="step-8-config-docs"></a>Step 8 — Config documentation

**Files:** `internal/config/config.go` (Go doc comments), `.capy.toml.example` or equivalent, `README.md` if it covers config.

- Document `[store]ephemeral_ttl_hours` with its default, meaning, and the trade-off (longer = more intra-session recall, shorter = less DB churn).
- Document `capy_search`'s new `include_ephemeral` argument.
- Document `capy_cleanup`'s new `purge_ephemeral` argument.

**Step → verify:**
- `go doc ./internal/config` shows the new field.
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

- Migration fails halfway through (power loss between `ALTER TABLE` and `UPDATE`) — use a transaction around both statements; either both apply or neither.
- User sets `ephemeral_ttl_hours = 0` — treat as "purge on every `Cleanup` call". Document this.
- User sets a negative TTL — reject at config load with a clear error.

### Code-review checklist (for the reviewer)

- [ ] Every call to `Index` / `IndexPlainText` / `IndexJSON` in `internal/server/` passes a kind explicitly. No defaults at the store API.
- [ ] `SearchOptions{}` (zero value) with empty `Source` results in a `kind = 'durable'` filter.
- [ ] `cleanup.go` has two clearly separated paths — no shared `access_count` gate.
- [ ] Migration is idempotent and transactional.
- [ ] New tool arguments are documented in the MCP tool schema (JSON schema `description` fields), not just in Go comments.
