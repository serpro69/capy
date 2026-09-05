# Vault Session Renames — Design

**Status:** Draft

**Issue:** [#81 — vault: support session renames](https://github.com/serpro69/capy/issues/81)

**Active profile:** Go (`database/sql` / SQLite)

## Problem

The vault derives `vault_sessions.title` during import from the last Claude Code `ai-title` record, falling back to the first significant user prompt. That title is useful when it is accurate, but users cannot correct, replace, or clear it after a session has been archived. The same imported title is consequently repeated across `vault list`, `show`, search results, JSON/MCP responses, and the TUI.

The obvious-looking implementation—updating `vault_sessions.title`—is wrong. That column is imported archival metadata and can be replaced when a transcript grows or arrives from another vault. A user-managed name has a different owner and lifecycle: imports and reindexing must not erase it, clears must survive later merges, and transcript preservation must remain byte-for-byte unchanged.

### How Might We

How might we let users assign and change meaningful names for archived vault sessions from either CLI or TUI, while preserving the archived transcript and keeping names stable across later imports and merges?

## Target User

The primary user is an individual developer organizing a local vault, including vaults merged across that developer's own machines. Team-managed or concurrently shared vault administration is not part of capy's current local-only model.

## Success Criteria

1. A successful rename is immediately reflected in `list`, `show`, CLI/TUI search-result display, JSON/MCP result metadata, and every ambiguous-UUID candidate.
2. A custom name survives transcript growth, re-import, reindex, compact, rekey, and repeated cross-machine merges.
3. `capy vault list --name <substring>` finds sessions by effective title without mixing name matching into transcript/chunk FTS semantics.
4. Clearing a name restores the latest imported title and cannot be undone accidentally by merging an older rename.
5. Rename and clear do not change archived `raw_jsonl`, sidecars, `content_hash`, `size_bytes`, FTS rows, or chunk rows.
6. Merge convergence is deterministic: all vaults presented with the same rename states choose the same effective name.

## Architecture Overview

```text
CLI rename/clear ─┐
                  ├─> VaultStore name operation ─> vault_session_names
TUI edit name ────┘                                  │
                                                     ▼
vault_sessions.title ───────────────────────> effective-title resolver
                                                     │
                 list / show / search / MCP / TUI <──┘

source vault_session_names ─> latest-wins reconciliation ─> destination
```

The vault DB is authoritative for capy-owned names. Imported title extraction remains unchanged. Presentation reads resolve a single effective title inside the `vault` package, while import writes continue to own only the imported title.

## Ownership and Title Precedence

`vault_sessions.title` remains the imported title. It is updated only by the existing insert/replace logic and is never directly changed by the rename feature.

The effective title is:

1. A non-empty `vault_session_names.custom_title`, when present.
2. Otherwise, `vault_sessions.title`.

A clear is an explicit state, not the absence of history. Once a session has been renamed or cleared, a metadata row remains so merge can distinguish “this vault has never named the session” from “this vault deliberately cleared an older name.”

The Go domain model must expose imported and active-custom title state without making CLI, server, or TUI callers duplicate precedence logic. Existing `Session.Title` writes remain imported metadata; read-facing helpers or fields provide the effective title. `SearchResult.Title` continues to be the effective display title.

## Storage Model

Migration `0005_session_names` creates `vault_session_names` for both fresh and existing vaults. The table contains:

| Column | Contract |
|---|---|
| `session_uuid` | Primary key; foreign key to `vault_sessions(uuid)` with `ON DELETE CASCADE` |
| `custom_title` | Nullable text; non-null is the active override, null is a clear tombstone |
| `renamed_at_ns` | UTC Unix-nanosecond timestamp used for latest-wins reconciliation |
| `machine_id` | Stable writer identity and equal-timestamp tie-breaker |

No additional index is needed. There is at most one metadata row per session, and name lookup is a substring over the effective value, which cannot use an ordinary B-tree prefix index. The single-user vault already performs bounded table scans for project filters.

The table definition is shared between `schemaSQL` and the migration, following the existing chunk-table pattern so fresh and migrated schemas cannot drift. The migration uses capy's established name-keyed migration runner and immediate transaction. This project convention takes precedence over introducing a second migration framework; the SQL and query plan require human review before implementation merges.

## Rename and Clear Contract

Add `capy vault rename <session-id> <name>` and `capy vault rename <session-id> --clear`. A name and `--clear` are mutually exclusive. Quoting is required when a name contains shell-separated whitespace.

The session ID follows existing lookup behavior: at least eight UUID characters, `ErrSessionNotFound` when absent, and `AmbiguousUUIDError` with effective-title candidates when non-unique.

Name normalization and validation are shared by CLI and TUI:

- Trim surrounding whitespace.
- Apply `sanitize.StripSecrets` before persistence because names are returned through CLI and MCP surfaces.
- Reject an empty post-normalization value for rename; clear is the explicit empty-state operation.
- Reject terminal control characters.
- Reject names longer than 120 Unicode code points rather than truncating silently.
- Permit duplicate names; UUID remains the mutation identity.

A local operation writes `(custom_title, renamed_at_ns, machine_id)` atomically. Its timestamp is `max(current UTC UnixNano, stored renamed_at_ns + 1)`, failing loudly on impossible integer overflow. This makes an explicit local action newer than the state it edits even if the wall clock moved backward. The store owns resolution and mutation in one immediate transaction so concurrent delete/rename cannot leave orphaned state.

## Read Surfaces and Name Lookup

Every session-metadata read left-joins `vault_session_names` and resolves effective title in the `vault` package. This includes:

- `GetSession` and ambiguous-prefix candidates.
- `ListSessions` and `list --json`.
- Per-line `Search` and chunk-level `SearchChunks` result metadata.
- CLI `show` headers, ordinary search output, server/MCP result mapping, and all TUI models.

CLI and TUI search rows currently carry `SearchResult.Title` but do not consistently render it. They must render a width-bounded effective title as part of this feature; matching remains based on transcript/chunk FTS only.

`ListOptions` gains a name filter, exposed as `capy vault list --name <substring>`. It performs a case-insensitive literal substring comparison over the effective title. `%`, `_`, quotes, and FTS operators are ordinary characters, not query syntax. Project and name predicates combine with AND, newest-first ordering is unchanged, and limit is applied after both predicates.

The TUI's `f` filter is broadened from project-only filtering to session filtering across effective title, project path, and UUID, preserving `/` for transcript FTS search. This makes custom names discoverable in the interactive browser without conflating them with message matches.

## TUI Interaction

Use `e` for “edit name.” `r` and `R` remain the established restore/resume pair, while `n` is already next-marker navigation in the viewer.

`e` is available for the selected list session, the session owning the selected search result, and the open viewer session. It opens a focused single-line input prefilled with the effective title and labeled `name (empty clears)`. `Enter` submits; `Esc` cancels. Submitting an empty input writes a clear tombstone.

The write runs as a Bubble Tea command. While pending, duplicate submissions are ignored. On success, the current mode reloads authoritative state:

- List mode reapplies the active session filter and retains a sensible neighboring selection.
- Search mode reruns the current FTS query so all hits for the session display the new title.
- Viewer mode reloads session metadata without rewriting or reparsing the archived transcript.

A successful operation closes the editor and shows a transient confirmation. Validation or DB failures leave the editor open with its text intact and show an error status. Help text in all three modes advertises `e rename`.

## Import, Reindex, and Maintenance

Import never creates, changes, or removes a name row. A transcript replacement updates imported metadata and content exactly as today; the foreign-keyed name row remains. Clearing therefore reveals the newest imported title, not a snapshot captured at rename time.

Reindex touches only FTS/chunk rows and `index_version`. Compact rewrites only encoded blobs. Rekey copies the complete database. None interpret rename metadata, and `currentIndexVersion` is not bumped because extraction and indexed content do not change.

Deleting a session cascades its name row. Restoring or resuming remains byte-faithful and does not append a name to Claude Code files.

## Cross-Machine Merge

`MergeFrom` feature-detects the source table because source vaults are deliberately opened without migration. A legacy source contributes no name state.

For a current source, compare `(renamed_at_ns, machine_id)` lexicographically. The source state wins only when its tuple is greater. A winning null title clears the destination. The comparison is independent of content hash and size: a newer name can merge when transcripts match or when the source transcript loses the existing larger-wins comparison.

A new source session and its optional name are inserted in one transaction to satisfy the foreign key. Existing-session name reconciliation can proceed without rewriting the transcript. Name-only changes report `updated`; identical/older states report `skipped`. Dry-run performs the same comparison and reports the prospective effective title. Repeating the merge is idempotent.

If timestamps match, machine ID produces a direction-independent winner for normal cross-machine divergence. “Latest” remains latest recorded wall-clock time, not a causal guarantee.

## Failure, Concurrency, and Security

All reads and writes use context-aware, parameterized `database/sql` calls. Multi-statement operations use the existing `sqliteutil.BeginImmediateContext` discipline. `sql.ErrNoRows`, ambiguous prefixes, context cancellation, and write errors remain distinct and actionable.

Rename never logs or emits the archived transcript. Control-character rejection prevents a custom title from injecting terminal escapes. Secret stripping maintains the existing invariant that session titles surfaced through search/list responses do not expose recognized credentials.

A merge write failure follows current per-session failure handling: record an error, continue other sessions, and do not claim success. TUI failures do not update cached presentation state. WAL lifecycle and checkpoint behavior are unchanged.

## Verification Strategy

Verification covers schema migration and cascade behavior; store rename/replace/clear and effective-title resolution; name filtering; CLI end-to-end behavior; TUI interaction in list/search/view; and a merge matrix spanning newer, older, tie, tombstone, matching/smaller transcript, new session, legacy source, dry-run, and repeated merge cases.

Tests must assert archived transcript bytes and content hashes are unchanged after rename/clear. Race coverage exercises rename/delete and rename/merge. The complete suite runs with `CAPY_DB_KEY` and `CAPY_VAULT_KEY` plus `-tags fts5`. Retrieval-quality benchmarks are not required because the feature deliberately does not change indexed content or ranking.

## Assumptions

1. Session UUID remains the stable identity across copied and merged vaults.
2. A developer's machine clocks are sufficiently aligned for wall-clock latest-wins semantics; machine ID resolves equal timestamps across machines.
3. A same-machine, same-nanosecond divergent rename is not produced by normal operation.
4. Duplicate human-readable names are acceptable because UUID remains the mutation key.
5. The automatic schema migration is acceptable for existing encrypted vaults and will receive normal human review.
6. A table scan over one effective-title value per session is acceptable at expected single-user vault sizes.

## Not Doing

- **Claude Code propagation:** capy does not modify local Claude session files or call an unsupported programmatic rename path.
- **Archived transcript mutation:** names are not appended to `raw_jsonl`, restore output, sidecars, or content hashes.
- **Claude `/rename` ingestion:** existing `custom-title` records are not promoted into capy-owned overrides in this feature.
- **Name matching in FTS:** transcript and chunk search semantics remain unchanged; use `list --name` or TUI filtering.
- **Rename history/undo:** only current state plus a clear tombstone is retained.
- **Interactive merge conflicts:** latest timestamp with deterministic tie-break resolves conflicts automatically.
- **Team/shared-vault administration:** no user identity, permissions, or multi-writer coordination is introduced.

## Rejected Alternatives

### Inline override columns on `vault_sessions`

Rejected because imported archival metadata and user-owned state would share the replacement row. Although it minimizes query changes, every import and merge update would need extra safeguards against clobbering the override.

### Immutable rename-event log

Rejected because audit history and event compaction add complexity without current user value. An event log still needs winner-selection semantics; it does not remove the cross-machine policy decision.

## References

- [Vault architecture](../../architecture.md#vault)
- [Original vault design](../../done/vault/design.md)
- [ADR-025: vault index version and reindex](../../adr/025-vault-index-version-and-reindex.md)
- [Issue #81](https://github.com/serpro69/capy/issues/81)
- [Anthropic request for a programmatic Claude rename interface](https://github.com/anthropics/claude-code/issues/37282)
