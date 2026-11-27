# ADR-038: A complete session ID wins over prefix matches; captured IDs resolve exactly

**Status:** Accepted
**Date:** 2026-11-27
**Amends:** Vault session lookup's pure literal-prefix rule (`sessionIDPrefixPattern`, eight-byte minimum) used by `GetSession`, rename, project assignment, delete planning and the session brief.
**Preserves:** [ADR-027](027-vault-is-sole-session-store.md) (the vault owns session identity), [ADR-031](031-transcript-model-seam-and-multi-platform-vault.md) (an independently archived child is selected by its own ID), the eight-byte prefix minimum and literal treatment of `%`, `_` and `\`.

## Context

Every vault command resolved its session argument as a literal `LIKE` prefix,
which SQLite folds for ASCII case. Claude session IDs come from transcript
filenames, so legacy archives can hold IDs that are not fixed-length UUIDs.
When one such ID begins another (`legacy-session`, `legacy-session-2`), or two
differ only in ASCII case, the shorter one was reported as ambiguous even when
typed in full. No input could select it: `vault show`, restore, resume,
rename, project assignment, delete and the session brief all failed with "use a
longer ID". The brief's search-hit follow-up and timeout hint pointed at the
same dead end. Fixed-length lowercase UUIDs cannot collide this way.

The vault-session-brief design kept "existing vault lookup semantics" by
reusing the shared helper; neither the design nor its review considered this
collision. A user decision on 2026-11-27 asked for an exact match to win in
every lookup path rather than only in the brief.

Review of that change found a second, related class of defect, present before
it: code that already holds a full ID — a TUI selection, a launch marker, a
deferred TUI action, a reindex enumeration — resolved it again through the
prefix lookup. If that session was deleted meanwhile, a longer ID it prefixes
could be shown, edited, restored, resumed or reindexed in its place.

## Decision

1. **A complete ID wins.** Each lookup selects the session whose ID equals the
   input byte for byte, if one exists in scope; otherwise it falls back to the
   literal prefix rule, where two matches remain ambiguous. `preferExactSession`
   (internal/vault/store.go) narrows `GetSession`, rename, project assignment
   and delete planning. The brief's resolver runs a scoped `uuid = ?` query
   before its `LIMIT 2` prefix query, because that query may not return the
   exact row; the two implementations cross-reference each other.
2. **Scope still applies first.** The brief and MCP selectors restrict the
   candidates before either rule is evaluated, and ambiguity still reveals no
   candidates or counts.
3. **Captured full IDs never fall back to a prefix.** Callers that obtained the
   ID from the vault earlier must treat a different resolved ID as "session
   gone": the TUI's `getSession` helper (viewer, raw view, project editor,
   child launch), `RenameOptions.ExactID` / `ProjectOptions.ExactID` for TUI
   writes, `DeleteOptions.ExactIDs` for TUI deletion, `lookupVaultSession(...,
   exact)` for TUI-deferred restore and resume, and the identity check in
   `rebuildSessionFTS` for reindex. `ExactID` also lets the TUI write IDs
   shorter than eight bytes.
4. **Operator and agent input keeps prefix lookup.** Command-line arguments,
   the TUI's startup `--tui <id>` argument and `capy_vault_brief`'s `session`
   accept a prefix, now with exact-match precedence.
5. **Selection precedes decoding.** `GetSession` decodes only the transcript of
   the session it selects, so a damaged candidate cannot fail another
   session's exact lookup or an ambiguity report.

## Consequences

- Every archived session with an ID of at least eight bytes is reachable by
  typing that complete ID.
- Accepted risk: someone who types exactly a complete shorter ID while meaning
  a longer ID it begins now acts on the shorter one. This needs the same rare
  legacy collision; previously the same input was refused. Delete still shows
  the resolved selection before confirmation.
- New code that stores a session ID and looks it up later must use an exact
  path from decision 3. The prefix lookup is for input a person or agent typed.
- Known limitation: a full ID copied from an older search hit and passed to
  `capy_vault_brief` or `capy vault show` is input, so if that legacy session
  was deleted it can resolve to a longer ID it prefixes. Both outputs name the
  resolved session. An exact-only selector for hit follow-ups is tracked in
  `docs/feat/wip/vault-session-brief/tasks.md`.
- Nothing is persisted differently; mixed binaries on a shared vault differ
  only in how arguments resolve.
- Rollback removes the exact-match step and the exact options; the
  captured-ID guards can stay, since they only refuse mismatched results.

## Alternatives considered

### Keep pure prefix semantics (rejected)

Leaves colliding legacy sessions unreachable by any input, including the
brief's own follow-up instructions.

### Exact match only for read-only lookups (rejected)

Show and the brief would select the session while rename, project assignment
and delete still refused it, and the brief's `vault show` hint could disagree
with the brief. The write-side risk is the same rare collision and is shown
before delete confirmation.

### Make every lookup exact-only (rejected)

Removes prefix convenience for operators, and Codex IDs are routinely shortened
to twelve characters in listings.
