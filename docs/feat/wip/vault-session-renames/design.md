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
| `custom_title` | Nullable text; non-null is the active override, null is a clear tombstone. Never empty by construction: validation rejects an empty rename and merge normalizes an empty source value to a clear tombstone |
| `renamed_at_ns` | UTC Unix-nanosecond timestamp used for latest-wins reconciliation |
| `machine_id` | Stable writer identity and equal-timestamp tie-breaker |

No additional index is needed. There is at most one metadata row per session, and name lookup is a substring over the effective value, which cannot use an ordinary B-tree prefix index. The single-user vault already performs bounded table scans for project filters.

The table definition is shared between `schemaSQL` and the migration, following the existing chunk-table pattern so fresh and migrated schemas cannot drift. The migration uses capy's established name-keyed migration runner and immediate transaction. This project convention takes precedence over introducing a second migration framework; the SQL and query plan require human review before implementation merges.

## Rename and Clear Contract

Add `capy vault rename <session-id> <name>` and `capy vault rename <session-id> --clear`. A name and `--clear` are mutually exclusive. Quoting is required when a name contains shell-separated whitespace.

The session ID follows existing lookup behavior: at least eight UUID characters, `ErrSessionNotFound` when absent, and `AmbiguousUUIDError` with effective-title candidates when non-unique. Prefix bytes are matched literally; SQLite `LIKE` metacharacters (`%`, `_`, and the escape character) never broaden lookup or mutation scope.

Name normalization and validation are shared by CLI and TUI and apply strictly in this order:

1. Trim surrounding whitespace.
2. Apply `sanitize.StripSecrets` because names are returned through CLI and MCP surfaces. A name that itself matches a recognized credential pattern is therefore stored redacted, not verbatim — deliberate; README must note this UX surprise.
3. Reject an empty post-normalization value for rename; clear is the explicit empty-state operation.
4. Reject malformed UTF-8 so storage, JSON rendering, and Unicode-aware filtering observe the same title.
5. Reject terminal control characters.
6. Reject names longer than 120 Unicode code points — measured after trimming and secret-stripping, since redaction changes length — rather than truncating silently.

Duplicate names are permitted; UUID remains the mutation identity.

A local operation writes `(custom_title, renamed_at_ns, machine_id)` atomically. Its timestamp is `max(current UTC UnixNano, stored renamed_at_ns + 1)`, failing loudly on impossible integer overflow. This makes an explicit local action newer than the state it edits even if the wall clock moved backward. The store owns resolution and mutation in one immediate transaction so concurrent delete/rename cannot leave orphaned state.

## Read Surfaces and Name Lookup

Every session-metadata read left-joins `vault_session_names` and resolves effective title in the `vault` package. This includes:

- `GetSession` and ambiguous-prefix candidates.
- `ListSessions` and `list --json`.
- Per-line `Search` and chunk-level `SearchChunks` result metadata.
- CLI `show` headers, ordinary search output, server/MCP result mapping, and all TUI models.

CLI and TUI search rows currently carry `SearchResult.Title` but do not consistently render it. They must render a width-bounded effective title as part of this feature; matching remains based on transcript/chunk FTS only.

`ListOptions` gains a name filter, exposed as `capy vault list --name <substring>`. It performs a case-insensitive literal substring comparison over the effective title. Case folding is Unicode-aware and happens in Go (`strings.ToLower` on both needle and title) over the resolved effective title — SQLite's built-in `lower()`/`NOCASE` folds ASCII only, which would silently mismatch the non-ASCII names the contract allows. The project predicate stays in SQL; the name predicate and the limit are applied in Go after title resolution, covered by the bounded table scan already accepted in Assumption 6. `%`, `_`, quotes, and FTS operators are ordinary characters, not query syntax. Project and name predicates combine with AND and newest-first ordering is unchanged.

The TUI's `f` filter is broadened from project-only filtering to session filtering across effective title, project path, and UUID, using the same Unicode-folding matcher as `--name` and preserving `/` for transcript FTS search. This makes custom names discoverable in the interactive browser without conflating them with message matches.

## TUI Interaction

Use `e` for “edit name” where ordinary characters are not routed to a text input: the list in navigation state and the open viewer session. `r` and `R` remain the established restore/resume pair, while `n` is already next-marker navigation in the viewer.

Search mode is different: its query input is always focused and every ordinary character edits the query, so binding bare `e` would make that letter untypeable. Search mode therefore binds `ctrl+e` for the session owning the selected result. Likewise, while the list's `f` filter input is active, `e` types into the filter; the rename binding applies only in navigation state.

The editor opens a focused single-line input prefilled with the effective title and labeled `name (empty clears)`. `Enter` submits; `Esc` cancels. Submitting an empty input writes a clear tombstone.

The write runs as a Bubble Tea command. While pending, duplicate submissions are ignored. On success, the current mode reloads authoritative state:

- List mode reapplies the active session filter and retains a sensible neighboring selection.
- Search mode reruns the current FTS query so all hits for the session display the new title.
- Viewer mode reloads session metadata without rewriting or reparsing the archived transcript.

A successful operation closes the editor and shows a transient confirmation. Validation or DB failures leave the editor open with its text intact and show an error status. Help text in all three modes advertises the rename binding (`e rename` in list/viewer, `ctrl+e rename` in search).

## Import, Reindex, and Maintenance

Import never creates, changes, or removes a name row. A transcript replacement updates imported metadata and content exactly as today; the foreign-keyed name row remains. Clearing therefore reveals the newest imported title, not a snapshot captured at rename time.

Reindex touches only FTS/chunk rows and `index_version`. Compact rewrites only encoded blobs. Rekey copies the complete database. None interpret rename metadata, and `currentIndexVersion` is not bumped because extraction and indexed content do not change.

Deleting a session cascades its name row. Restoring or resuming remains byte-faithful and does not append a name to Claude Code files.

## Cross-Machine Merge

`MergeFrom` feature-detects the source table because source vaults are deliberately opened without migration. A legacy source contributes no name state. The reverse direction is an accepted, non-destructive limitation: an older capy binary merging from a current vault does not read `vault_session_names` and silently carries no name state — destination names are untouched, and re-running the merge with an upgraded binary carries them.

For a current source, compare `(renamed_at_ns, machine_id)` lexicographically. The source state wins only when its tuple is greater; a destination without a name row compares lower than any source tuple, so a named or cleared source always wins there. A winning null title clears the destination. When two distinct states carry an equal tuple — possible because machine identity can be duplicated via `CAPY_MACHINE_ID` or a dotfile-synced `~/.config/capy/machine-id` — a deterministic value tie-break completes the total order: a non-null title beats a null tombstone, and two non-null titles compare bytewise with the greater winning. Convergence therefore never depends on machine IDs being unique.

A winning source state is written **verbatim** — `(custom_title, renamed_at_ns, machine_id)` exactly as stored in the source. The monotonic `max(now, stored + 1)` bump applies to local rename/clear operations only; re-stamping during merge would break idempotence and cross-vault convergence. A source `custom_title` that is empty or whitespace-only (which no supported writer produces) is normalized to a clear tombstone, preserving the NULL-or-non-empty invariant.

The comparison is independent of content hash and size, and name reconciliation runs for every source session whose UUID exists in the destination — including source sessions the zero-message exclusion guard drops and transcripts skipped as same-hash or smaller. An excluded or empty source session with no destination row contributes no name: there is no parent row for the foreign key.

A new source session and its optional name are inserted in one transaction to satisfy the foreign key. Existing-session name reconciliation can proceed without rewriting the transcript. Name-only changes report `updated`; identical/older states report `skipped`. Dry-run performs the same comparison and reports the prospective effective title. Repeating the merge is idempotent.

“Latest” remains latest recorded wall-clock time, not a causal guarantee.

## Failure, Concurrency, and Security

All reads and writes use context-aware, parameterized `database/sql` calls. Multi-statement operations use the existing `sqliteutil.BeginImmediateContext` discipline. `sql.ErrNoRows`, ambiguous prefixes, context cancellation, and write errors remain distinct and actionable.

Rename never logs or emits the archived transcript. Control-character rejection prevents a custom title from injecting terminal escapes. Secret stripping maintains the existing invariant that session titles surfaced through search/list responses do not expose recognized credentials.

A merge write failure follows current per-session failure handling: record an error, continue other sessions, and do not claim success. TUI failures do not update cached presentation state. WAL lifecycle and checkpoint behavior are unchanged.

## Verification Strategy

Verification covers schema migration and cascade behavior; store rename/replace/clear and effective-title resolution; name filtering (including non-ASCII case folding); CLI end-to-end behavior; TUI interaction in list/search/view (including that `e` remains typeable in the search query and list filter); name retention through transcript replacement, re-import, reindex, compact, and backup-API rekey; and a merge matrix spanning newer, older, tie, equal-tuple value tie-break, tombstone, matching/smaller transcript, zero-message source with populated destination, new session, legacy source, dry-run, and repeated merge cases. Idempotence assertions must check the stored tuple equals the source tuple after a winning merge, so accidental re-stamping cannot pass.

Tests must assert archived transcript bytes and content hashes are unchanged after rename/clear. Race coverage exercises rename/delete and rename/merge with deterministic winner assertions. The complete suite runs with `CAPY_DB_KEY` and `CAPY_VAULT_KEY` plus `-tags fts5`. Ranking and indexed content are expected unchanged, but `Search`/`SearchChunks` SQL changes, so repository benchmark policy applies: run `make bench-quality` and compare against the base branch with `make bench-compare` to prove no regression.

## Assumptions

1. Session UUID remains the stable identity across copied and merged vaults.
2. “Latest wins” means the latest recorded wall-clock timestamp: a machine with a forward-skewed clock wins conflicts until other machines' clocks pass its timestamps. This is the accepted semantic, not a clock-synchronization requirement.
3. Machine IDs may collide (`CAPY_MACHINE_ID` override, dotfile-synced config); equal-tuple divergence is resolved by the deterministic value tie-break rather than assumed away.
4. Duplicate human-readable names are acceptable because UUID remains the mutation key.
5. The automatic schema migration is acceptable for existing encrypted vaults and will receive normal human review.
6. A table scan resolving one effective-title value per candidate session is acceptable at expected single-user vault sizes: on the order of thousands of sessions, with interactive latency required to hold at 10k sessions. If vaults grow past that, name filtering becomes a benchmark-backed follow-up.

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
