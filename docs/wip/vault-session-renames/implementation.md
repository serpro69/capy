# Vault Session Renames — Implementation Plan

This plan implements the decisions in [design.md](./design.md). It assumes an experienced Go contributor who has not worked in capy's vault package before.

## Existing Code Map

| Area | Existing seam |
|---|---|
| Schema and migrations | `internal/vault/store.go` (`schemaSQL`) and `internal/vault/migrations.go` (name-keyed immediate migrations) |
| Session reads/writes | `internal/vault/store.go`: `Session`, `sessionMetaColumns`, `scanSessionMeta`, `GetSession`, `ListSessions`, `Search` |
| Chunk result metadata | `internal/vault/chunk_search.go` |
| Import ownership | `internal/vault/import.go`: `buildRecord`, `SessionWrite`, larger-content replacement |
| Cross-vault merge | `internal/vault/merge.go`: feature-detected source reads and batched destination writes |
| CLI | `cmd/capy/vault.go`; end-to-end harness in `cmd/capy/vault_test.go` |
| TUI orchestration | `internal/vault/tui/app.go`; list/search/view models and `dataStore` interface |
| User docs | `README.md` and `docs/architecture.md` |

The store uses `database/sql`, prepared or parameterized statements, context-aware methods, and `sqliteutil.BeginImmediateContext` for writes. Keep those conventions; do not introduce an ORM, a second migration system, or a new dependency.

## 1. Persistence Contract and Migration

1. Define one shared `sessionNamesTableSQL` constant in `internal/vault/store.go` and include it in `schemaSQL` for fresh vaults. The table columns and null-tombstone semantics must match [Storage Model](./design.md#storage-model). → verify: a fresh `VaultStore` exposes the table and its `ON DELETE CASCADE` foreign key through SQLite metadata.
2. Add `migrate0005AddSessionNames` to `internal/vault/migrations.go`, using the existing read fast path, `BeginImmediateContext`, in-transaction applied recheck, shared DDL, and migration record. Do not reuse the deliberately vacant `0002` slot. → verify: a legacy schema gains the table exactly once and records `0005_session_names` across repeated opens.
3. Extend `internal/vault/migrations_test.go` and the fresh-schema assertions in `internal/vault/store_test.go`. Cover fresh, legacy, idempotent rerun, and cascade deletion. → verify: `go test -tags fts5 -count=1 ./internal/vault/...` passes the migration-focused tests.

## 2. Domain Types and Effective-Title Reads

1. Add a private/public-as-needed name-state type carrying nullable custom title, timestamp, and machine ID. Extend `Session` so imported title and active custom title remain distinguishable, and provide one vault-package effective-title resolver. Do not let presentation packages repeat precedence rules. Precedence lives only in this Go resolver: reads scan both title columns and resolve in Go — no SQL-side `COALESCE` encodes precedence, so the two layers cannot drift. The resolver may treat NULL as the only no-override state because `custom_title` is never empty by construction (validation rejects empty renames; merge normalizes an empty source value to a tombstone). → verify: table-driven unit tests cover override, clear tombstone, no row, and empty imported title, plus an assertion that no write path persists an empty-string `custom_title`.
2. Replace the unqualified `sessionMetaColumns` assumption with a reusable, table-qualified select fragment that left-joins `vault_session_names`. Update `scanSessionMeta`, `GetSession`, and `ListSessions` to populate imported/custom state while preserving raw-BLOB decoding behavior. → verify: existing store tests still pass and renamed sessions appear consistently in list, lookup, and ambiguous-prefix candidates.
3. Add `Name string` (or equivalently explicit name-filter state) to `ListOptions`. Keep the project predicate in SQL; apply the name predicate in Go over the resolved effective title using Unicode-aware case folding (`strings.ToLower` on both needle and title — SQLite's `lower()`/`NOCASE` folds ASCII only), then apply the limit after both predicates. The needle is a literal: never interpolate it into query text or interpret FTS/wildcard syntax. → verify: tests cover mixed case, a non-ASCII fixture (e.g. `Café` matched by `café`), literal `%`/`_`/quotes, combined project/name filtering, no match, and limit-after-filter.
4. Update per-line search in `store.go` and chunk search in `chunk_search.go` to return the effective title via a left join. Keep `MATCH`, BM25/RRF, snippets, and indexed columns untouched. → verify: existing search tests retain ranking/results while a renamed fixture changes only `SearchResult.Title`.

## 3. Store Rename Operation

1. Add a shared name normalizer in `internal/vault/` applying steps strictly in this order: trim, strip recognized secrets, reject post-normalization empty values, reject Unicode control characters, and reject values over 120 runes — length measured after trim and strip, since redaction changes length. Keep clear represented separately from an empty string. A name matching a credential pattern persists redacted; this is deliberate and belongs in the README notes. → verify: table tests cover Unicode length boundaries (including a value whose length crosses 120 runes only after redaction), whitespace, controls, secret redaction, and duplicate-name acceptance.
2. Add a store operation accepting a UUID prefix and either a normalized name or clear request. Resolve the prefix and upsert inside one immediate transaction. Reuse `ErrSessionNotFound` and `AmbiguousUUIDError`; candidate titles must be effective. → verify: store tests cover exact/partial success, missing, ambiguous, rename replacement, and clear fallback.
3. Give the production entry point current UTC time and `MachineID()`, with an internal deterministic seam for tests. Persist `max(now.UnixNano(), existing renamed_at_ns + 1)` and fail on overflow. Update only `vault_session_names`. → verify: a backward-moving test clock still produces a greater timestamp, while before/after reads prove `raw_jsonl`, content hash, size, FTS rows, and chunks are unchanged.
4. Exercise concurrent rename/delete and repeated rename behavior under the race detector (the rename/merge race lives in §5.5). The transaction must yield either a completed rename on an existing session or an actionable error, never orphan metadata. → verify: focused `go test -race -tags fts5` cases pass repeatedly.
5. Add the maintenance-retention tests promised by Success Criterion 2: rename a fixture session, then exercise transcript replacement via re-import, `reindex`, `compact`, and backup-API rekey, asserting the custom name and a clear tombstone survive each operation. → verify: each maintenance path leaves `vault_session_names` rows unchanged.

## 4. CLI Rename and Name Lookup

1. Register `newVaultRenameCmd` in `cmd/capy/vault.go` with `rename <session-id> <name>` and `rename <session-id> --clear`. Enforce mutually exclusive argument forms, reject `--tui`, call the store operation, and print the resulting effective title/clear confirmation. → verify: command tests cover valid rename, clear, missing/ambiguous IDs, invalid names, and invalid argument combinations.
2. Add `--name` to `vault list` and pass it through `ListOptions`; JSON and table output must use the effective title. → verify: CLI end-to-end tests import fixtures, rename them, filter by mixed-case substring, combine `--project`/`--name`, clear, and observe imported-title fallback.
3. Render the already-carried effective title in ordinary CLI search rows without changing FTS matching. Update width/truncation intentionally so snippets remain readable. → verify: a content match displays the custom name, while a query that exists only in the name returns no FTS match.
4. Extend command round-trip coverage to snapshot archived bytes and digest before rename/clear and compare them afterward. → verify: the end-to-end test reports byte-identical JSON output plus identical hash/size.

## 5. Cross-Vault Name Reconciliation

1. Add a parameterized source-table existence probe and source name reader in `internal/vault/merge.go`. A source without `vault_session_names` is a supported legacy vault and contributes no state. → verify: existing legacy-source merge tests pass without migration or “no such table” errors.
2. Implement a conditional name upsert comparing `(renamed_at_ns, machine_id)`, completed to a total order by the value tie-break (non-null title beats null tombstone; two non-null titles compare bytewise, greater wins). An absent destination row loses to any source tuple. A winning source state is written **verbatim** — never re-stamp with the local `max(now, stored+1)` bump, which applies to local operations only and would break idempotence and convergence. Normalize an empty/whitespace-only source title to a clear tombstone. Null titles must remain tombstones. Keep the comparison independent of transcript content decisions. → verify: a table-driven matrix covers newer/older timestamp, equal-timestamp machine tie-break, equal-tuple value tie-break, absent-destination insert, named/cleared combinations, identical state, and that a winning merge stores the source tuple unchanged.
3. Integrate reconciliation into `MergeFrom`. Mechanically this means restructuring the early-`continue` branches in `merge.go`: the zero-message exclusion and the equal-hash/smaller-size skips must fall through to a name-only reconciliation whenever the destination session row exists, instead of skipping the session entirely; an excluded or empty source with no destination row contributes no name (no FK parent). Extend the new-session write path (`SessionWrite`/`WriteBatch` carry no name state today) so a new session and its name commit in one transaction. Report name-only change as updated; preserve per-session error continuation. → verify: merge tests cover all three transcript branches plus the zero-message-source → populated-destination case, source-only sessions, write errors, and destination transcript immutability.
4. Apply the same decision in dry-run without writes and surface the prospective effective title. Repeat each merge fixture to prove idempotence and run source/destination order checks for timestamp ties. → verify: second runs report skipped, stored tuples equal the winning source tuples, and both merge directions converge on the same effective title.
5. Add the rename/merge race promised by the design's verification strategy: a concurrent local rename against a running `MergeFrom` on the same destination, under the race detector, with a deterministic winner asserted from the reconciliation order. → verify: focused `go test -race -tags fts5` cases pass repeatedly.

## 6. TUI Rename Flow

1. Extend the TUI's consumer-owned `dataStore` interface with the rename operation and update stubs. Add root-model rename-editor state using the existing Bubble Tea/textinput dependencies; do not create a new package dependency. → verify: model construction tests compile against both `VaultStore` and the stub.
2. Route `e` from the list (navigation state only — while the `f` filter input is active, `e` must keep editing the filter) and the viewer, and `ctrl+e` from search mode (its query input is always focused, so a bare `e` must keep editing the query — see `searchModel.Update`'s fall-through to the text input) to a prefilled `name (empty clears)` input. While editing, consume keys before normal mode routing; `Enter` submits asynchronously, `Esc` cancels, and a pending request ignores duplicate submit. → verify: model tests cover all entry modes, that typing `e` into the search query and the list filter still edits text, prefill, cancel, empty clear, and pending-state behavior.
3. On success, close the editor, show confirmation, and reload the current mode from the store. On error, retain editor text and show an error. Preserve selection where possible and allow a renamed item to disappear from a nonmatching active filter. → verify: tests assert authoritative refreshed titles in list/search/view and no local mutation on failure.
4. Broaden list `f` filtering to effective title, project path, and UUID using the shared Unicode-folding matcher from §2.3, while leaving `/` as transcript FTS search. Render effective titles in search results and add the rename binding to mode help (`e rename` in list/viewer, `ctrl+e rename` in search). Preserve `r`/`R` restore/resume and viewer `n` marker navigation. → verify: keybinding/filter tests cover all searchable fields and existing navigation suites remain green.

## 7. Documentation and Full Verification

1. Update `README.md` Session Vault command table, examples, and TUI keys. Document duplicate names, clear semantics, `list --name`, and lack of Claude propagation. → verify: every new command/flag/key is discoverable from README and Cobra help.
2. Update `docs/architecture.md` with the new table, effective-title ownership, and independent latest-wins merge path. Keep completed historical docs under `docs/done/vault/` unchanged; this document records the delta. → verify: architecture links and table names match implementation.
3. Run formatting, focused package/CLI tests, `make vet`, `make test`, and `make test-race` with required keys/tags. Because `Search`/`SearchChunks` SQL changes (left join for effective titles), repository benchmark policy applies even though ranking is expected unchanged: run `make bench-quality` and `make bench-compare BASE=master TARGET=<branch>` to prove it. → verify: all commands exit successfully with no race reports and no benchmark regression.
4. Run `$kk:review-code` with Go input and `$kk:review-spec` against this feature directory; address findings or record any explicitly deferred work in `tasks.md`. → verify: no unresolved blocking review finding remains.

## Assumptions

- Session UUID is the stable key across copied/merged vaults.
- "Latest wins" means latest recorded wall-clock timestamp; forward clock skew wins until overtaken. Accepted semantic, not a synchronization requirement.
- Machine IDs may collide (`CAPY_MACHINE_ID`, synced config); equal-tuple divergence resolves via the deterministic value tie-break.
- Duplicate names are acceptable; UUID prefixes remain the mutation handle.
- Effective-title substring scans are acceptable at expected vault sizes (thousands of sessions; interactive latency must hold at 10k).
- Existing name-keyed migration infrastructure remains the approved project mechanism.

## Not Doing

- Claude Code rename propagation or local transcript mutation.
- Archived BLOB/content-hash mutation.
- Claude `custom-title` ingestion as a vault override.
- Name terms in transcript/chunk FTS.
- Rename event history or general undo.
- Interactive merge conflicts or shared-team identity/permissions.

## Rejected Alternatives

- **Inline override columns:** rejected because import replacement and user metadata ownership remain coupled.
- **Immutable event log:** rejected because audit history and event lifecycle are unnecessary for the current user job.
