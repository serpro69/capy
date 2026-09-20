# Vault Project Names — Design

**Status:** Agreed design; design reviewed; Tasks 1–2 done; Tasks 3–10 pending

**Issue:** [#92 — vault: support custom session project name](https://github.com/serpro69/capy/issues/92)

**Date:** 2026-09-19

**Profile:** Go; database guidance applied using the repository's existing SQLite conventions

**Companions:** [Implementation](implementation.md), [Tasks](tasks.md)

## Problem and user

An individual developer archives sessions from their normal checkout and temporary worktrees, sometimes merging vaults across their own machines. Imported project paths preserve where sessions actually ran, but a path such as /tmp/tmp.ffjhS8XqPu does not let a project filter for capy find that session after the worktree disappears.

How might we let developers organize and retrieve archived sessions under a chosen project name while preserving original paths and keeping overrides stable through imports and merges?

This is an extension of [session renames](../../done/vault-session-renames/design.md) and [ADR-030](../../../adr/030-vault-session-names-and-latest-wins-merge.md). This document describes the delta. The new WIP directory follows the repository's task-tracking convention; completed feature documents remain historical.

## Confirmed direction and success criteria

The user selected explicit naming, both CLI and TUI editing, and **override semantics with separate implicit filesystem scope**. The optional isolated Chain-of-Verification pass was declined; the proposal is grounded in direct code inspection and the existing ADR.

1. Assigning capy to a temporary-worktree session makes explicit project filters find it in CLI listing/search, TUI project scope, and both MCP search tools. An override replaces the imported path for these explicit filters.
2. Default MCP searches still scope by the imported path. Assigning a name neither removes a session from its original directory's implicit scope nor automatically associates it with a different checkout.
3. Display and effective-project statistics agree. Original paths remain available in details and existing JSON fields.
4. Set/clear works from CLI and TUI, targets one session UUID, and leaves titles and other sessions unchanged.
5. Overrides and clear tombstones survive transcript growth, re-import, reindex, compact, rekey, and deterministic cross-machine merges.
6. Set/clear leaves archived bytes, sidecars, imported metadata, content hashes, sizes, FTS rows, chunk rows, and restore/resume locations unchanged.
7. Project predicates run before query limits and retrieval candidate selection. No reindex is required.

## Existing mechanisms and architecture

The relevant existing mechanisms are:

- [store.go](../../../../internal/vault/store.go): Session, metadata SELECT/scan, list, per-line search, stats, SessionWrite and transactional writes.
- [session_name.go](../../../../internal/vault/session_name.go): the established local edit, tombstone, clock and reconciliation pattern.
- [chunk_search.go](../../../../internal/vault/chunk_search.go): project predicates enter both retrieval layers through CorpusConfig.
- [merge.go](../../../../internal/vault/merge.go): source schema probes, source selection and content-independent title reconciliation.
- [tool_search.go](../../../../internal/server/tool_search.go) and [tool_vault_search.go](../../../../internal/server/tool_vault_search.go): explicit/default project scoping; federation currently infers session availability from raw-path stats.
- [TUI app.go](../../../../internal/vault/tui/app.go): asynchronous title editor, authoritative refresh, and consumer-owned dataStore interface.

The proposed flow is CLI/TUI project edit → vault-owned metadata transaction → effective-project projection → browsing/search/stats. Cross-vault merge reconciles project state independently of transcript and title state. Imported paths retain ownership of filesystem operations and implicit MCP scope.

## Ownership and storage

Add vault_session_projects via migration 0007_session_projects. At the inspected revision 0006_platform is the latest migration; if another migration lands first, take the next unused number and update these documents. Do not reuse the deliberate 0002 gap.

| Field | Contract |
| --- | --- |
| session_uuid | Primary key and foreign key to vault_sessions.uuid, cascading on session deletion |
| custom_project | Nullable text; non-null is a non-empty override, null is an explicit clear tombstone |
| updated_at_ns | Required Unix-nanosecond edit timestamp |
| machine_id | Required writer identity, independent of the imported session's machine ID |

Share the table definition between fresh schema and the name-keyed migration. No existing session needs an override row. No extra substring index, FTS schema change, currentIndexVersion bump, or reader-version bump is needed.

Add SessionProject state and Session.ProjectOverride alongside the existing Session.Name. Preserve Session.ProjectPath and ClaudeProjectDir verbatim. Session.EffectiveProject() resolves non-null override, otherwise current imported path. No row and a tombstone have the same effective value but different merge history. Never overwrite ProjectPath to carry a label.

Extend the shared session metadata join/scan, including lookup, ambiguous-prefix candidates and Children. SearchResult gains Project for the resolved value and nullable CustomProject for display provenance, while retaining ProjectPath as the imported path. Presentation must not duplicate precedence rules.

### SQL and Go precedence

Unlike titles, projects constrain retrieval candidates and GROUP BY. A single vault-owned SQL expression resolves the effective project from the project join, alongside the Go resolver. Its supported-state semantics are custom_project when non-null, otherwise project_path. Use this expression in the shared local predicate builder and effective grouping; test parity with the Go resolver for unset, set, equal-to-path and clear states. This deliberately extends ADR-030's Go-only title precedence; post-filtering top-ranked hits in Go would lose valid results.

Source-vault queries feature-detect the table and use imported path alone when absent; source selection also observes the foreign-state normalization described under Cross-machine merge. SQL alias names/expressions are trusted internal constants; user values remain bound parameters.

## Local edit contract

Expose SetSessionProject(ctx, prefix, ProjectOptions) returning post-write Session metadata. ProjectOptions carries Name or Clear, mutually exclusive. CLI and TUI share this entry point.

Reuse title normalization semantics: trim, redact recognized secrets, reject empty, invalid UTF-8 or control characters, then reject more than 120 Unicode code points after normalization. Do not truncate or require a directory to exist. Duplicate names intentionally group sessions. A path-looking name is still a label; it is never expanded, resolved or used as a launch directory.

Resolve UUID prefixes using existing literal prefix rules. Resolve, read prior project state and write within one immediate transaction. Use the ADR-030 monotonic local timestamp and overflow behavior. Project timestamps are independent of title timestamps. Cancellation, lookup ambiguity and database failures surface as errors.

Set/clear affects only this session. Parent and child sessions neither inherit nor cascade project changes. An explicit clear keeps a tombstone even if no override was previously present.

## Project selection

| Surface | Selection source |
| --- | --- |
| vault list --project, vault search --project | Effective project |
| TUI launched with --project | Effective project, retained across modes/reloads |
| Explicit MCP project other than the existing star sentinel | Effective project |
| MCP with no explicit project | Imported project_path under the existing current-directory substring rule |
| MCP all_projects or project star sentinel | Unrestricted; existing widening precedence retained |
| vault merge --project | Source vault's effective project |
| vault import --project and startup discovery | Existing physical discovery/location hints |

ListOptions.Project remains an effective-project substring. SearchOptions.Project becomes the explicit effective-project substring; add SearchOptions.ProjectPath for implicit raw-path scope. Both non-empty is invalid and fails loudly. Neither means unrestricted. No new MCP argument is introduced. A shared server helper resolves the existing request arguments into exactly one of these states; it must not rewrite Server.projectDir.

Retain SQLite's existing escaped LIKE substring semantics, including its ASCII case-insensitivity. Percent, underscore and backslash are literal query characters. Do not add path normalization, basename inference, full Unicode folding or wildcard project syntax. An exact star remains MCP's existing all-projects sentinel; CLI project filters treat it literally. A star-only custom label therefore has no narrowing MCP selector, an existing reserved-query limitation to state in help rather than silently changing the sentinel.

Apply predicates inside SQL before LIMIT, FTS rank selection and both chunk retrieval layers. Name, role, date, platform and child filters otherwise retain their existing behavior.

The TUI's local f filter remains the existing Unicode-folded search across title, project and UUID; replace its project operand with EffectiveProject(). It is a multi-field local finder, distinct from the SQL-backed --project scope. It must not also search the imported path as a project alias. Keep that established local-folding behavior rather than changing title filtering.

### Federation availability and errors

Add HasSessionsInProject(ctx, project, projectPath), using the same project predicate and SELECT EXISTS without decoding blobs or matching transcript terms. Federation uses it instead of vaultStatsHaveSessions for the empty-knowledge-base preflight. Statistics remain useful for backlog warnings but are not an authority on the query's scope.

A failed availability query must not turn into a false "knowledge base is empty" response. Surface the failure in-band and attempt the normal vault search; successful knowledge results remain usable. A definitive false result retains the existing empty-KB guidance. This is an availability check, not a promise of a transcript match.

### Physical boundaries and merge compatibility

Import filters continue selecting on-disk files; archived labels cannot classify an unarchived file. Startup sweep, config path resolution, security globs, executor directories, restoration paths and resume directories remain physical.

Merge's old location-hint OR raw-path project selector is intentionally replaced with effective-project selection, including for legacy sources where effective means imported path. A mangled Claude directory or Codex rollout-location substring alone no longer selects a session. Document this change and replace the old expectation in TestMergeFrom_ProjectFilter; keep wildcard-escaping coverage.

## Read surfaces and JSON compatibility

List/show/search, ambiguity messages, delete confirmation, child listings, TUI list/search/viewer and MCP result metadata display the effective project. Custom labels are rendered as literal labels, without home-directory shortening; fallback imported paths may retain displayPath formatting. Details show original path separately when different. Project-only metadata is never added to transcript/chunk MATCH content.

| Output | Compatibility contract |
| --- | --- |
| list --json and search --json | Keep project_path as imported path; add project as the effective value |
| show --format json | Remains verbatim archived JSONL; no injected metadata |
| stats --json projects | Preserve current raw-path rows and project_path/count fields |
| stats --json project_groups | Add effective project/count rows |
| Ordinary stats output | Show effective project groups |

Keep VaultStats.ByProject as the legacy raw-path aggregation and add ByEffectiveProject with an explicitly named effective-project field. Group by exact stored effective strings; labels that differ in case remain distinct groups even though a substring query can match both, as with existing raw-path grouping. Each session contributes once to each aggregation; child and platform counting conventions are unchanged.

Merge reporting adds effective Project and nullable CustomProject to ImportedSession without repurposing raw ProjectPath. Report the prospective/resulting destination value, including destination overrides that beat source state. Ordinary disk-import diagnostics remain physical source-path reports, consistent with the import selection boundary; no override lookup is added to discovery. Omitted project information on existing skipped/error report rows need not be invented.

## Cross-machine merge

Apply ADR-030's ordering independently to project state: timestamp, machine ID, then value (non-null beats null at an equal tuple; two non-null values compare bytewise). An absent destination loses to any source state. Store a winning source tuple verbatim; no local timestamp bump during merge.

Read source state through a schema-detected path. Unsupported empty/whitespace-only source overrides are treated as clear tombstones, matching the title precedent. To keep source project filtering consistent with this normalization, project-filtered source enumeration resolves source project metadata before applying the literal matcher; it must not filter a foreign empty override with an inconsistent SQL expression. The source enumeration holds no cursor during destination writes and reads metadata only. Use a small literal-substring matcher with ASCII-only folding for this enumeration, tested against the SQLite project predicate; ContainsFold would wrongly add Unicode folding here. Apply selection before transcript/blob loading. Non-empty foreign values otherwise follow ADR-030's verbatim metadata policy.

Every existing destination session may receive newer metadata even when the source transcript is same-hash, smaller, or an excluded zero-message shell. No destination parent row means no orphan project state is written for an excluded source. Reconcile title and project state in one transaction on metadata-only branches; neither field's winner gates the other's reconciliation. Extend SessionWrite to carry project state so new/replaced session and both metadata tracks commit atomically.

A session counts as updated if transcript, title or project changed, and only once. Dry-run computes the same prospective outcome without writes. Repeated/reversed merges converge. Database errors stay visible per session.

Legacy sources contribute no project state and cannot clear destination overrides. Older binaries merging from a newer vault omit this table; this is the same accepted, recoverable mixed-version limitation as session names. Recommend upgrading all merging machines and rerunning merge. Reader gating is not expanded for an additive metadata table.

## CLI and TUI interaction

Add vault project <session-id> <name> and vault project <session-id> --clear. Reject missing name/clear, both together, ambiguous IDs and invalid names. Reject --tui on this mutating subcommand as rename does. Report the effective value after success.

Bind ctrl+g to project editing in list (including an active local filter), search and viewer. No selection means no action. Preserve e/ctrl+e title editing, navigation, text entry and restore/resume bindings.

Reuse the existing editor lifecycle with an explicit edit target distinguishing title from project; do not build a general metadata framework. For project editing, prefill only the current override (blank if absent/cleared) and display original path separately. Search-result entry loads authoritative session metadata before opening because the result's effective value does not identify override state.

Enter saves; whitespace-only input clears; Esc cancels. Pending writes consume duplicate submissions; cancellation through ctrl+c remains available. Validation/write errors keep the editor and text intact. Do not silently truncate project input before normalization; retain bounded rendering for long inputs.

After success, update visible metadata and refresh list and search under their current scopes. Retain selection where possible; a reassigned session can disappear from the active project filter. Increment search sequence IDs so older async results cannot restore stale project data. Report refresh failures as "saved, refresh failed", not as a failed write. Preserve viewer content, navigation and parent frames; there is no parent/child project inheritance.

## Database practices and operational bounds

Continue database/sql, existing pool configuration and sqliteutil.BeginImmediateContext; no new dependencies or migration framework. Use context-aware parameterized operations, close cursors before same-connection writes and distinguish not-found from SQL errors. Use the repository's migration fast-path/recheck pattern, not PostgreSQL row-lock syntax. WAL close/checkpoint and encryption invariants remain untouched.

Overrides are at most one row per session; joins are one-to-one. Substring filters and small metadata aggregations already scan the single-user vault. Avoid per-hit DB reads and transcript decoding for list, stats, source selection or availability checks. The TUI may fetch one selected session when opening its editor, following existing metadata access patterns.

## Assumptions

- Must hold: UUID remains the stable mutation and merge identity; source and destination are trusted personal vaults.
- Must hold: a chosen name changes explicit project selection, not the imported directory used for filesystem operations.
- Should hold: the ADR-030 120-code-point normalization contract is sufficient for project labels. Imported paths themselves are not length-limited.
- Should hold: scans over thousands of metadata rows remain interactive. Validate a 10,000-session fixture and compare before/after latency; this is an unmeasured scale assumption, not a claimed SLA.
- Should hold: users accept explicit-name searches and physical default MCP scope as different modes; help and regression tests must make the distinction concrete.
- Accepted inherited constraint: wall-clock timestamps can be skewed and machine IDs can collide; ADR-030's total ordering defines convergence rather than real-time chronology.

## Not Doing

- Automatic worktree/main-checkout association or filesystem-to-label inference: explicit naming is the selected outcome.
- Bulk edits, parent/child inheritance or cascading reassignment: mutation remains one UUID.
- Project registry, alias lists, rename history or undo: current state plus tombstone is sufficient.
- MCP mutation tools: editing is through CLI/TUI; MCP remains a read surface.
- Transcript rewriting, label terms in FTS, restore/resume relocation: archival and physical ownership remain unchanged.
- Team-shared vault administration or interactive conflicts: this is a personal-vault feature.
- New dependencies, migration systems or generic metadata framework: extend established mechanisms.

## Rejected alternatives

| Alternative | Reason |
| --- | --- |
| Alias matching against override OR original path | Explicit reassignment would continue matching the old project and disagree with the visible grouping |
| Applying effective-project selection to implicit MCP scope | Renaming would remove sessions from their original directory's default searches |
| Overwriting imported project_path | Imports can replace it; it also controls physical behavior |
| One timestamp for title and project | Independent edits across machines could erase each other |
| Filtering retrieved top results in Go | Results discarded after LIMIT cannot be replaced with eligible lower-ranked hits |
| Reusing grouped statistics as the scope preflight | Effective groups cannot answer imported-path membership reliably |
| Automatic worktree canonicalization | Does not solve arbitrary labels or already-deleted worktrees and exceeds selected scope |

## Verification and review

[Implementation](implementation.md) specifies checks per slice; [tasks](tasks.md) makes them actionable. Cover fresh/legacy migrations, normalization, set/clear, filter-before-limit, raw/effective scope separation, JSON compatibility, both MCP paths with an empty knowledge DB, TUI editing and scopes, title/project merge independence, all transcript branches, clocks/ties/concurrency, maintenance survival and archived-byte invariance.

Run the repository's FTS5-tagged tests, race checks and quality comparison during implementation. No new production code or database migration is executed by this design task. The post-design gate is kk:review-design over all three documents. Required implementation reviews and checks are in the final task.
