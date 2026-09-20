# Vault Project Names — Implementation Plan

**Status:** Design reviewed; Tasks 1–2 done; Tasks 3–10 pending

**Contract:** [design.md](design.md)

**Execution:** [tasks.md](tasks.md)

**Issue:** [#92](https://github.com/serpro69/capy/issues/92)

This plan is for a Go contributor new to the vault. Read [ADR-030](../../../adr/030-vault-session-names-and-latest-wins-merge.md), the [vault architecture](../../../architecture.md), and the repository [invariants](../../../../AGENTS.md) first. Existing title code is a pattern, not a reason to share title/project conflict timestamps. Do not change decoders, transcript contents, knowledge-store scoping or physical path resolution.

All code/API names introduced below are proposed names. Existing seams were inspected on 2026-09-19. Keep this document aligned if implementation chooses different names. The slices are vertical paths; mechanical registrations and projection plumbing are included with the path they enable.

## Code map

| Concern | Existing files and seams |
| --- | --- |
| Schema, metadata, reads, writes | internal/vault/store.go: schemaSQL, sessionMetaColumns/Join, scanSessionMeta, SessionWrite, writeRecord |
| Migration runner | internal/vault/migrations.go; current last migration 0006_platform |
| Local metadata and merge precedent | internal/vault/session_name.go; tests in session_name_test.go and merge_test.go |
| Source selection and merge | internal/vault/merge.go: sourceSessionUUIDs, readSourceName, reconcileMergeName, MergeFrom |
| Retrieval | internal/vault/store.go Search; internal/vault/chunk_search.go SearchChunks and scanChunkSearchRow |
| CLI and JSON | cmd/capy/vault.go; binary harness in cmd/capy/vault_test.go |
| MCP | internal/server/tool_vault_search.go, tool_search.go, tools.go; tool_search_federation_test.go |
| TUI | internal/vault/tui/app.go, list.go, search.go, viewer.go; rename_test.go and helpers_test.go |
| User/reference docs | README.md, docs/architecture.md; platform/routing.go generates .capy/AGENTS.md |

## Shared contracts

- Keep Session.ProjectPath and SearchResult.ProjectPath physical. Add Session.ProjectOverride, Session.EffectiveProject(), SearchResult.Project and SearchResult.CustomProject. Do not make a custom label pass through filesystem normalization or home-prefix shortening.
- Local writes use ProjectOptions{Name, Clear} and SetSessionProject. Nil state means never edited; a state with null CustomProject means clear. New and migrated tables share the same definition.
- A vault-owned effective-project SQL expression plus a shared predicate builder handles escaped LIKE. SearchOptions.Project targets the effective value; ProjectPath targets the raw value. Reject both together. Local SQL grouping and filtering agree with the Go resolver.
- HasSessionsInProject uses the same predicate. It is not an FTS query and does not depend on grouped statistics.
- MCP argument resolution is shared by both handlers: widening flags first, then non-empty explicit project, otherwise raw current-directory scope. Do not change coercion or widening precedence.
- Preserve the current raw-path stats field; introduce ByEffectiveProject separately. JSON compatibility is additive.
- No new dependency, schema framework, FTS index version or minimum-reader version is part of this change. Migration naming must be rechecked immediately before implementation.

## Slice 1: Set and clear a project through the CLI

Primary behavior: archive a fixture, set its custom project by UUID prefix, see the effective value in the command response, clear it and see the latest imported path.

1. Create internal/vault/session_project.go with the state/options types, resolver, normalization and local write operation. Follow RenameSession's literal prefix, immediate transaction, cursor closure, monotonic clock, overflow and error contracts. Reuse or extract only the concrete normalization logic shared with titles; retain title-specific errors and behavior. → verify: TestSessionProject_LocalEdits covers Unicode boundaries, secret stripping before length validation, controls, invalid UTF-8, duplicate names, ambiguity, clock rollback and overflow.
2. In store.go add the shared project-table definition to schemaSQL and extend Session plus the shared metadata join/scan. In migrations.go add the next migration using the established applied check/immediate recheck. → verify: fresh, pre-project schema, repeated-open and delete-cascade fixtures expose identical constraints and correctly distinguish absent state from a clear tombstone.
3. Add cmd/capy/vault_project.go for the new subcommand and register it from vault.go. Validate arguments before opening the vault; reject --tui and print effective post-write state. Keep ProjectPath untouched. → verify: a binary-harness TestVaultProject_Command round trip covers set, replacement, clear, invalid combinations, missing/ambiguous IDs and project/title independence.
4. Extend a before/after snapshot fixture to compare raw JSONL, sidecars, content hash, size, index_version, per-line FTS and chunks. → verify: local set/clear changes only the project table; show --format json remains byte-identical.

Focused check: with both required test keys, run go test -tags fts5 -count=1 ./internal/vault ./cmd/capy -run 'Project|Migration|SessionName'.

## Slice 2: Browse reassigned sessions in the CLI

Primary behavior: project reassignment is visible in list/show/details and changes list --project selection, with compatible JSON.

1. Add the effective-project expression/predicate helper in session_project.go; use it in ListSessions before its SQL limit. Retain the existing Go-side title predicate and limit ordering when --name is also supplied. → verify: TestSessionProject_ListFiltering places a matching older session below newer nonmatching sessions and proves --limit does not hide it; cover combined title/platform/children filters.
2. Update list/show headers (text and Markdown), ambiguity candidates, delete confirmations and child rows in cmd/capy/vault.go. Distinguish literal custom labels from shortened fallback paths; show original path separately when different. → verify: CLI fixtures cover custom, no override, clear and custom value equal to the imported path.
3. Add project to list JSON while retaining project_path; do not touch raw show JSON. → verify: JSON assertions pin both values and the existing fields; list --project no longer matches the replaced path unless another active field/filter independently warrants a match.
4. Test SQL resolver parity across supported states and literal percent/underscore/backslash/quotes, including ASCII and non-ASCII cases. → verify: exact effective values agree in Go and SQL; existing SQLite substring behavior remains unchanged.

Focused check: go test -tags fts5 -count=1 ./internal/vault ./cmd/capy -run 'Project|ListSessions'.

## Slice 3: Search reassigned transcripts through the CLI

Primary behavior: a transcript-content query can be restricted by effective project without changing FTS matching or JSON provenance.

1. Extend SearchOptions with raw ProjectPath scope and validation. Join project metadata in Search and populate SearchResult.Project/CustomProject alongside unchanged ProjectPath. Apply the shared scope predicate before ORDER BY rank/LIMIT. → verify: TestSessionProject_SearchFiltering proves override-only explicit filtering, raw implicit filtering, mutually exclusive option errors and limit-before-selection regression prevention.
2. Update CLI search rows and JSON mapping. Custom names are display/filter metadata only. → verify: a transcript term returns the resolved project; a term existing only in the custom label is not an FTS match; project_path stays raw.
3. Add result-order comparison fixtures with no project filter and with an equivalent eligible subset. → verify: metadata joins neither duplicate hits nor change titles, anchors or ranking within the same candidate set.

Focused check: go test -tags fts5 -count=1 ./internal/vault ./cmd/capy -run 'Project|SearchResolves'.

## Slice 4: Query custom projects through capy_vault_search

Primary behavior: explicit MCP project searches use labels, while default searches still reach sessions from their actual current directory.

1. Extend SearchChunks' joins/select/row mapper with project projection and pass the shared predicate into CorpusConfig for both porter and trigram layers. → verify: TestSessionProject_ChunkFiltering tests both scope modes, clear fallback, pre-limit candidate selection and unchanged ranking/navigation metadata.
2. Add a request-to-project-scope helper in tool_vault_search.go and use it in handleVaultSearch. Update formatVaultHit to display the resolved project, handling literal labels correctly. → verify: TestVaultSearch_ProjectOverrideScopes exercises explicit name, explicit replaced path, omitted/empty project, star, all_projects and widening precedence.
3. Update the capy_vault_search argument description in tools.go to explain explicit effective-project matching versus default imported-path scope. → verify: schema/help assertions match the accepted modes; no new MCP argument or mutation tool exists.

Use fixtures where name and raw path differ completely. Include a renamed session whose raw path equals the server directory: default search must still find it, while explicit project equal to that raw path must not. A label-only association with another checkout must not create a default hit.

Focused check: go test -tags fts5 -count=1 ./internal/vault ./internal/server -run 'Project|VaultSearch|SearchChunks'.

## Slice 5: Inspect effective project statistics

Primary behavior: the human-readable statistics group renamed sessions together while existing JSON consumers keep raw-path totals.

1. Preserve VaultStats.ByProject and its raw aggregation; add ByEffectiveProject using the shared effective expression, exact GROUP BY and deterministic count-descending/value ordering. → verify: TestSessionProject_Stats groups two different raw paths under one label, then separates them after clear; every session counts once and children/platform totals stay unchanged.
2. Use effective groups in ordinary stats output. Keep stats JSON projects byte-shape-compatible and add project_groups entries with project/count. → verify: TestVaultProject_StatsJSON asserts raw and effective breakdowns separately, including empty vault and case-distinct labels.
3. Keep grouped stats out of the new availability API; do not repurpose the old ProjectStat.ProjectPath field for a label. → verify: existing raw-stat assertions remain valid.

Focused check: go test -tags fts5 -count=1 ./internal/vault ./cmd/capy -run 'Project|Stats'.

## Slice 6: Preserve federated search availability

Primary behavior: capy_search serves custom-project sessions even when the knowledge store is empty, without broadening default scope.

1. Implement HasSessionsInProject in session_project.go using the same validated scope builder and a metadata-only existence query. → verify: TestSessionProject_HasSessions compares existence with actual session membership for effective, raw, empty and invalid mixed scope.
2. In tool_search.go reuse Slice 4's request-scope helper, pass the chosen scope to SearchChunks and replace vaultStatsHaveSessions with the new existence check. Keep source filters, kind selection, knowledge scoping and backlog stats intact. → verify: TestSearch_ProjectOverrideScopes covers empty/nonempty knowledge DBs, renamed current-directory sessions, renamed worktree sessions under explicit labels, unrelated projects and all-projects widening.
3. Surface availability failures in-band and still attempt the actual vault pass; do not convert a failed check to a definitive empty corpus. Preserve existing successful knowledge results. → verify: a targeted failure fixture produces an explicit diagnostic rather than only the empty-KB guide; empty false still gives the established guide.
4. Remove the old availability helper only when this change has made it unused; update capy_search argument help. → verify: existing federation/source/kind tests stay green.

Focused check: go test -tags fts5 -count=1 ./internal/server -run 'Search_.*(Project|Vault|Empty|Session|Federat)'.

## Slice 7: Merge project assignments across personal vaults

Primary behavior: copying/merging the same archived session preserves independent title and project choices, including clear tombstones.

1. Add the project-table probe and source reader in merge.go. For project-filtered enumeration, select metadata (UUID, imported path, nullable project state), normalize whitespace-only foreign overrides to clear as ADR-030 does for titles, apply a literal substring matcher with ASCII-only folding, then release rows before any transcript read/write. With no project filter, retain the existing UUID-only enumeration. → verify: current and pre-project source fixtures select by effective project before blob decoding; matcher parity tests cover the SQLite literal cases.
2. Implement project ordering/reconciliation alongside the local project operation, with a deterministic test seam. Extend SessionWrite/writeRecord so source project state joins title state in the session write transaction. → verify: TestSessionProject_MergeOrder pins newer/older timestamps, duplicate machine IDs, value ties, nulls, absence and verbatim winning tuples.
3. Replace name-only skip handling with a narrowly scoped metadata reconciliation path that independently decides title and project inside one transaction. Cover equal hash, smaller source, zero-message existing destination and new/replaced sessions. → verify: TestMergeFrom_ProjectStateMatrix proves edits to different fields survive, no orphan rows appear, and updates count once.
4. Carry raw and effective project values separately in merge reports, including dry-run and a losing source override; extend ImportedSession mechanically without changing disk-import discovery or physical diagnostics. → verify: dry-run matches the prospective destination project without writes, repeat merge is skipped, reversed merge converges and larger transcript replacement retains the winning project state.
5. Replace the intentional old location-hint selector expectation in TestMergeFrom_ProjectFilter, retaining escaped-wildcard and legacy-source cases. → verify: a location-hint-only match no longer selects; a legacy imported path still does; source vault files/schema are not migrated or rewritten.
6. Add deterministic project-edit/merge and title-edit/project-edit concurrency coverage. → verify: focused race checks preserve both metadata tracks and the total-order winner, with errors visible rather than swallowed.

Focused check: go test -race -tags fts5 -count=1 ./internal/vault -run 'Project|MergeFrom_.*Name|SessionNameSupersedes'.

## Slice 8: Browse and search custom projects in the TUI

Primary behavior: project display, the local finder and a CLI-supplied project scope remain correct while navigating between modes.

1. Add Options.Project, carry it from list/search --project --tui launches, and retain it in list options and the search model. Preserve existing platform scope. → verify: TestApp_ProjectScopePersistence asserts initial load, filter refresh, child toggle, opening live search, returning to list and repeated searches retain the project.
2. Replace project display operands in list.go, search.go and viewer.go with resolved values; show imported path separately in session details when different. Replace only the project operand of the local f finder, preserving its established Unicode folding across title/project/UUID. → verify: TUI fixtures find a custom label and do not find a replaced raw path through the project operand; unrelated title/UUID matches remain supported.
3. Keep labels literal, fallback paths shortened, and headers bounded in narrow terminals. → verify: existing render/navigation tests plus custom/path-equal and long-path cases pass without wrapping the app beyond its allocated rows.

Focused check: go test -tags fts5 -count=1 ./internal/vault/tui ./cmd/capy -run 'Project|Filter|Platform|Children'.

## Slice 9: Edit a project's assignment in the TUI

Primary behavior: ctrl+g sets or clears the selected session's project from every relevant screen, without losing edit text or active scope.

1. Extend dataStore and stubs with SetSessionProject. Add an explicit title/project editor target to the existing root editor lifecycle; keep e/ctrl+e unchanged. Intercept ctrl+g after an active editor's routing and before list/search input routing, so it works even while the local finder is open. → verify: key tests cover list, active filter, search, viewer, empty selection and preserved title/text/navigation bindings.
2. Load authoritative selected metadata on project-editor entry, show original path separately and prefill only the override. Preserve input on validation failures; do not silently truncate before the store's normalization. → verify: no-override/clear starts blank, a path-like override remains literal, boundary validation and narrow-window rendering behave as specified.
3. Dispatch the target-specific write asynchronously and route its result through the shared refresh lifecycle. On success update viewer metadata, reload scoped list and rerun search with a newer sequence. On write failure retain the editor; on subsequent refresh failure report that the write succeeded. → verify: pending double-submit, cancel, delayed stale search results, committed-save/refresh failure and disappearing-from-filter scenarios.
4. Add ctrl+g project to help for each screen and the active-filter affordance. Preserve parent/child independence and viewer navigation state. → verify: a child edit changes only that child; existing rename tests remain green.

Focused check: go test -tags fts5 -count=1 ./internal/vault/tui -run 'Project|Rename|Keybindings|Filter'.

## Slice 10: Maintenance, documentation and final verification

1. Extend the existing encrypted maintenance fixtures for project override and clear state through re-import replacement, reindex, compact and backup-API rekey. Assert title state and imported-path behavior independently. → verify: TestSessionProject_MaintenanceSurvival plus local edit/delete race tests pass with both required keys.
2. Update README command examples, TUI keys, explicit/default MCP semantics, stats JSON and merge selector compatibility. Update architecture ownership/merge/filtering sections and link this design; add a focused ADR only if maintainers want a separate permanent record. Update tool help and generated routing prose where it explains project scope. If routing changes, edit internal/platform/routing.go and regenerate .capy/AGENTS.md together. → verify: TestGeneratedWholeFileArtifacts and TestMergedArtifactsAreIdempotent pass; examples agree with the implemented commands/JSON.
3. Add a 10,000-session metadata measurement using synthetic records, measuring list, grouped stats, existence and project-filtered retrieval with/without overrides. Record fixture size, machine, before/after timings and any justified regression in tasks.md; do not claim a latency SLA from unmeasured assumptions. → verify: no per-hit metadata query or blob decoding appears in query paths; investigate material regressions before completion.
4. Invoke kk:test with CAPY_DB_KEY=test-key-for-development and CAPY_VAULT_KEY=test-key; run the relevant focused suites, make vet, make test, make test-race and go test -tags fts5,glamour ./internal/vault/tui/... according to actual changes. → verify: required suites pass with FTS5 and no race reports; failures are fixed or recorded explicitly as blocking.
5. Run make bench-quality and compare the implementation branch with a measured master baseline using make bench-compare BASE=master TARGET=<branch>. Follow AGENTS.md's branch-name normalization and rename a detached baseline's HEAD.json to master.json before comparing; do not mistake a missing baseline for success. → verify: quality report and comparison are recorded, with unexplained regressions resolved.
6. Invoke kk:document, kk:review-code with Go input and kk:review-spec against this directory. Record unresolved findings durably, with reason and next action; do not mark the feature done with blocking gaps. → verify: docs match final code and no blocking review finding remains.

## Assumptions and deviations from generic guidance

The [design assumptions](design.md#assumptions) are testable bets, particularly interactive behavior at 10,000 sessions. Existing encrypted SQLite/database/sql and name-keyed migrations take precedence over the profile's generic sqlx/pgx/external-migration suggestions. This plan specifies a conceptual schema contract; it does not execute schema changes. Review the resulting migration and query plans with the implementation.

The conditional Go database checklist applies. gRPC and observability checklists do not apply: no transport, protobuf, metrics or tracing design is introduced. No new dependency recommendation is made.

## Not Doing and rejected alternatives

Follow the complete [scope exclusions](design.md#not-doing) and [rejected alternatives](design.md#rejected-alternatives). In particular, default MCP scope must not be "fixed" by canonicalizing worktrees or matching both custom and raw values in the same explicit filter.

## Deferred work and accepted limits

No agreed success criterion is deferred. Automatic association, bulk editing and a project registry are deliberate exclusions, not promised follow-ups.

Accepted compatibility limitation: old binaries omit project metadata during cross-vault merge. The recovery is to upgrade and rerun merge; no reader-version gate is planned. Star-only labels cannot narrow MCP searches because the pre-existing star sentinel means all projects; use CLI filtering or choose a different label.

If implementation exposes a new gap, add a concrete entry here and in tasks.md with what was skipped, why and the next step. Chat-only deferral does not count.

## Task 1 implementation record (2026-09-20)

- Added migration 0007_session_projects, SessionProject/ProjectOptions,
  Session.ProjectOverride, EffectiveProject, NormalizeSessionProject and the
  transactional SetSessionProject API. The existing title normalizer delegates
  to a shared concrete helper while retaining its error text.
- Added `vault project <id> <name>` / `<id> --clear`; validates before database
  access, rejects `--tui`, and reports the effective post-write value. Existing
  key/path flags, Cobra error handling and command-context propagation are inherited.
- Shared metadata reads include override provenance for lookup, list, children
  and ambiguity candidates. UI/JSON projection and selection changes remain in
  Tasks 2–9; this slice does not claim those later behaviors.
- Regression snapshots now compare complete FTS/chunk rows, encoded sidecars
  and index_version as well as raw JSONL, hash and size. CLI fixtures isolate
  XDG_CONFIG_HOME because a personal minimum-import-size setting otherwise
  excludes the small test sessions. Initial error-text fixture expectations were
  corrected to match the specified field-specific messages.
- No new dependencies, query/ranking/indexing changes or generated setup artifacts.
  Full-feature quality measurements and cross-surface documentation remain Task 10.

Verification and isolated review results, including the pre-existing live-corpus
canary failure and remaining CLI fixture isolation work, are recorded in
[tasks.md](tasks.md#task-1-verification-and-review-2026-09-20). No Task 1
implementation requirement is deferred.

## Task 2 implementation record (2026-09-20)

- Added `effectiveProjectSQL` and `effectiveProjectPredicate` in
  `session_project.go`. `ListSessions` uses the bound, escaped predicate before
  its limit; the Go title filter retains its existing limit ordering. Tests pin
  resolver parity and literal matching, including SQLite ASCII-only folding.
- CLI list/show, delete previews, ambiguity candidates and child list rows use
  `displaySessionProject`. Override provenance preserves path-looking labels
  literally, even when equal to the imported path; show/delete expose the
  original path separately when different.
- List JSON adds `project`, retains imported `project_path` and all other fields,
  and leaves raw show JSON byte-identical. README, architecture and list help
  document the completed slice and identify the pending surfaces.
- No new dependencies, schema changes, generated artifacts, transcript search,
  ranking or indexing changes. Retrieval quality benchmarks remain with the
  retrieval-changing slices and Task 10.
- The initial new child-display test omitted its child rollout. It now uses the
  existing `writeCodexChild` fixture and default platform discovery; no production
  import behavior or assertion was relaxed.

Verification and independent review results are recorded in
[tasks.md](tasks.md#task-2-verification-and-review-2026-09-20).
