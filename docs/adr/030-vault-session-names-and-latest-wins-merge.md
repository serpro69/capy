# ADR-030: Vault session names — capy-owned table, Go-side precedence, latest-wins merge

## Status

Accepted

## Context

The vault derives `vault_sessions.title` during import from the last Claude Code
`ai-title` record, falling back to the first significant user prompt. Users had no
way to correct, replace, or clear that title once a session was archived, and the
same imported title was repeated across `list`, `show`, search results, JSON/MCP
responses, and the TUI ([issue #81](https://github.com/serpro69/capy/issues/81)).

The obvious implementation — letting `capy vault rename` update
`vault_sessions.title` — is wrong. That column is imported archival metadata and is
rewritten by the existing insert/replace path whenever a larger transcript arrives
from disk or from another vault. A user-managed name has a different owner and a
different lifecycle: imports and reindexing must not erase it, a clear must survive a
later merge of an older rename, and archived transcripts must stay byte-for-byte
unchanged (the vault's core invariant).

Cross-machine merge (`capy vault merge`) adds a convergence requirement: all vaults
presented with the same rename history must end up displaying the same name, and
repeating a merge must be a no-op. Machine identities are not guaranteed unique
(`CAPY_MACHINE_ID`, a dotfile-synced `~/.config/capy/machine-id`), so a policy that
relies on unique writer IDs to break ties would not converge.

Full design: [docs/feat/done/vault-session-renames/design.md](../feat/done/vault-session-renames/design.md).

## Decision

1. **Names live in their own table, `vault_session_names`** (migration
   `0005_session_names`; DDL shared between `schemaSQL` and the migration): one row
   per session ever renamed or cleared, `session_uuid` PK/FK with `ON DELETE
   CASCADE`, nullable `custom_title`, `renamed_at_ns`, `machine_id`. Import,
   reindex, compact, and rekey never read or write it; rename never writes
   `vault_sessions.title`.

2. **A clear is an explicit tombstone, not the absence of a row.** `custom_title IS
   NULL` means "deliberately cleared"; no row means "never named here". The column is
   never the empty string by construction (validation rejects an empty rename; merge
   normalizes an empty source value to a tombstone), so NULL is the only no-override
   state the resolver has to consider.

3. **Title precedence is resolved in Go, in one place.** `Session.EffectiveTitle()`
   returns the custom title when present, else the imported title. Every read surface
   selects both columns through a shared `LEFT JOIN` fragment and calls the resolver;
   there is no SQL-side `COALESCE`, so the SQL and Go layers cannot encode different
   precedence rules. Ranking, snippets, indexed columns, and `currentIndexVersion` are
   unchanged.

4. **Local rename/clear timestamps are monotonic per row:**
   `max(now.UnixNano(), stored renamed_at_ns + 1)`, failing loudly on overflow. An
   explicit local action is therefore always newer than the state it edits, even if
   the wall clock moved backward.

5. **Merge reconciles names independently of transcript content, latest-wins with a
   total order.** For every source session whose UUID exists in the destination —
   including sessions the zero-message exclusion drops and transcripts skipped as
   same-hash or smaller — compare `(renamed_at_ns, machine_id)` lexicographically;
   an absent destination row loses to any source tuple; an equal tuple is broken by
   value (a non-NULL title beats a tombstone, two titles compare bytewise and the
   greater wins). A winning source state is written **verbatim** — the monotonic bump
   in (4) applies to local operations only — so repeated and reversed merges converge
   on identical stored tuples. A new session and its name commit in one transaction.

6. **Names are normalized through the shared secret stripper.** `NormalizeSessionName`
   applies, in order: trim, `sanitize.StripSecrets`, reject empty, reject invalid
   UTF-8, reject control characters, reject more than 120 code points (measured after
   redaction). Names are returned through CLI and MCP surfaces, so a name that itself
   matches a credential pattern is stored redacted — a deliberate UX surprise,
   documented in the README.

7. **Name lookup is a literal substring over the effective title, folded in Go.**
   `list --name` and the TUI `f` filter use `ContainsFold` (`strings.ToLower` on both
   sides) because SQLite's `lower()`/`NOCASE` fold ASCII only; the predicate and the
   limit run in Go after title resolution. Name terms are not added to transcript or
   chunk FTS.

## Consequences

- A rename is immediately reflected in every read surface (Success Criterion 1) and
  survives transcript growth, re-import, reindex, compact, backup-API rekey, and
  repeated merges (Criterion 2), with archived bytes, `content_hash`, `size_bytes`,
  FTS rows, and chunk rows provably unchanged (Criterion 5).
- Clearing reveals the *latest* imported title, not a snapshot captured at rename
  time — because the imported title keeps its own owner.
- Convergence never depends on unique machine IDs or synchronized clocks. "Latest"
  is latest recorded wall-clock time: a forward-skewed machine wins conflicts until
  other clocks pass its timestamps. This is the accepted semantic, not a bug.
- Name-only merge changes report `updated`; identical/older states `skipped`; dry-run
  reports the prospective effective title.
- A legacy source vault (pre-0005) contributes no names. The reverse is a known
  non-destructive gap: an older binary merging *from* a newer vault reads no
  `vault_session_names` and carries none; re-running with an upgraded binary does.
- Every session-metadata read now carries a `LEFT JOIN`; `list --name` is a bounded
  table scan resolving one effective title per candidate. Acceptable at expected
  single-user sizes (thousands of sessions, interactive at 10k); beyond that, name
  filtering becomes a benchmark-backed follow-up.
- Duplicate names are permitted; the UUID remains the only mutation identity.
- No propagation to Claude Code, and Claude Code's `/rename` (`custom-title`
  records) is not promoted into a vault name. Rename history/undo and interactive
  merge conflict resolution are explicitly out of scope.

## Alternatives considered

- **Inline override columns on `vault_sessions`** (`custom_title`, `renamed_at`).
  Fewest query changes, rejected because imported archival metadata and user-owned
  state would share the replacement row: every import and merge update would need
  extra safeguards against clobbering the override, and a clear would be
  indistinguishable from "never named" without yet another column.
- **Immutable rename-event log.** Gives audit history and undo for free, rejected
  because event compaction and log lifecycle add complexity without current user
  value — and an event log still needs a winner-selection policy; it does not remove
  the cross-machine decision.
- **SQL-side precedence (`COALESCE(custom_title, title)` in every query).** Rejected
  so precedence cannot drift between queries and Go callers; a single Go resolver is
  the only place the rule exists.
- **Interactive merge conflict resolution.** Rejected for the local, single-user
  vault model; the deterministic total order resolves every case automatically.
