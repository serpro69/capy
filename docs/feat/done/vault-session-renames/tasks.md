# Tasks: Vault Session Renames

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> Status: done
> Created: 2026-09-05
> Completed: 2026-09-12
> Not Doing: Claude propagation, archived transcript mutation, Claude custom-title ingestion, name matching in FTS, rename history, interactive merge conflicts, team/shared-vault administration

## Task 1: Persist and resolve vault-owned session names

- **Status:** done
- **Depends on:** —
- **Size:** L — spans impl §1–§3 (migration, domain reads, rename op) as one deliberate contract-first slice; splitting would ship a schema without its owning operations, and Tasks 2/3/4 block on the full contract either way
- **Slicing:** Contract-First — establishes the new persisted boundary used by independent CLI, merge, and TUI slices
- **Can run in parallel with:** —
- **Docs:** [implementation.md#1-persistence-contract-and-migration](./implementation.md#1-persistence-contract-and-migration), [implementation.md#2-domain-types-and-effective-title-reads](./implementation.md#2-domain-types-and-effective-title-reads), [implementation.md#3-store-rename-operation](./implementation.md#3-store-rename-operation)

### Subtasks

- [x] 1.1 Add shared `vault_session_names` DDL and idempotent `0005_session_names` migration in `internal/vault/store.go` and `internal/vault/migrations.go`; cover fresh/legacy schemas, reruns, and cascade deletion in `internal/vault/migrations_test.go` and `internal/vault/store_test.go`
- [x] 1.2 Add name state, shared normalization/validation, and one effective-title resolver in `internal/vault`; preserve imported `vault_sessions.title` ownership
- [x] 1.3 Update `GetSession`, `ListSessions`, and ambiguous-prefix candidates to resolve effective titles while retaining imported title provenance
- [x] 1.4 Implement transactional rename/clear by UUID prefix with monotonic local timestamps and immutable transcript/index assertions
- [x] 1.5 Add focused race coverage for rename/delete and repeated concurrent rename behavior (rename/merge race is Task 3.5)
- [x] 1.6 Add maintenance-retention tests: rename, then re-import/transcript replacement, reindex, compact, and backup-API rekey each preserve name and tombstone state

## Task 2: Expose rename, clear, and name lookup through the CLI

- **Status:** done
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** Task 3
- **Docs:** [implementation.md#2-domain-types-and-effective-title-reads](./implementation.md#2-domain-types-and-effective-title-reads), [implementation.md#4-cli-rename-and-name-lookup](./implementation.md#4-cli-rename-and-name-lookup)

### Subtasks

- [x] 2.1 Add and register `capy vault rename <session-id> <name>` plus mutually exclusive `--clear` handling in `cmd/capy/vault.go`, reusing existing lookup errors and rejecting `--tui`
- [x] 2.2 Add literal `ListOptions` name filtering (Go-side Unicode case folding per impl §2.3) and expose it as `vault list --name`; combine it with project filtering, cover a non-ASCII fixture, and return effective titles in table/JSON output
- [x] 2.3 Update `Search` and `SearchChunks` to resolve effective result titles, then render those titles in ordinary CLI transcript-search rows without making name-only text an FTS match
- [x] 2.4 Extend `cmd/capy/vault_test.go` and `cmd/capy/vault_unit_test.go` for rename, replace, clear, validation, lookup errors, name filtering, and byte/hash preservation

## Task 3: Merge name state independently from transcript content

- **Status:** done
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** Task 2
- **Docs:** [implementation.md#5-cross-vault-name-reconciliation](./implementation.md#5-cross-vault-name-reconciliation)

### Subtasks

- [x] 3.1 Feature-detect and read `vault_session_names` from an unmigrated source vault in `internal/vault/merge.go`, preserving legacy-source support
- [x] 3.2 Add latest-`(renamed_at_ns, machine_id)` conditional reconciliation with null clear tombstones, the equal-tuple value tie-break (total order), verbatim source-tuple writes (no local re-stamp), absent-destination wins, and empty-source-title normalization to tombstone
- [x] 3.3 Integrate atomic new-session+name writes (extend `SessionWrite`/`WriteBatch`) and name-only updates for the same-hash, smaller-transcript, and zero-message-excluded branches whenever the destination session exists; make dry-run/status output reflect effective results
- [x] 3.4 Extend `internal/vault/merge_test.go` with newer/older/tie/equal-tuple/tombstone, transcript-branch (including zero-message source → populated destination), legacy, failure, dry-run, convergence, and verbatim-tuple idempotence cases
- [x] 3.5 Add concurrent rename-vs-merge race coverage under `-race` with deterministic winner assertions

## Task 4: Rename sessions from every TUI browsing mode

- **Status:** done
- **Depends on:** Task 1, Task 2
- **Size:** M
- **Can run in parallel with:** Task 3
- **Docs:** [implementation.md#6-tui-rename-flow](./implementation.md#6-tui-rename-flow)

### Subtasks

- [x] 4.1 Extend `internal/vault/tui/app.go`'s consumer-owned store interface and root model with an asynchronous rename editor and result messages
- [x] 4.2 Bind `e` in list (navigation state) and viewer plus `ctrl+e` in search (bare `e` must keep editing the query/filter inputs), prefill the effective title, treat empty submit as clear, retain input on errors, and suppress duplicate pending writes
- [x] 4.3 Refresh authoritative list/search/view state after success while preserving selection and active filters where possible
- [x] 4.4 Broaden `f` filtering to effective title/project/UUID with the shared Unicode-folding matcher, render titles in search rows, and update help without changing `r`/`R` or viewer marker navigation
- [x] 4.5 Add TUI model/keybinding tests for all entry modes, typing `e` into the search query and list filter, success, clear, cancel, error, pending, filtered disappearance, refresh, and existing navigation regressions

### Notes (2026-09-12)

- Isolated review (code-reviewer + pal/gemini-3.1-pro-preview) applied: single-line inputs are width-bounded via the shared `boundInputWidth` helper (`app.go`), called from `layoutSubmodels` for the rename editor and from `listModel.setSize` / `searchModel.setSize` for the pre-existing filter and query inputs, which had the same cursor-off-screen gap. The helper also re-bounds the scroll window on resize via `CursorEnd`+`SetCursor`, since bubbles `handleOverflow` only recomputes when the cursor leaves the window. Also applied: nil-session guard on the success path; viewer metadata refresh now runs before the list re-read so a failing re-read cannot leave a committed rename stale in the header. Declined: lowering the input `CharLimit` to 120 (validation ownership stays in the store — length is measured after trim and secret redaction) and pre-allocating the filter slice.
- Test-fidelity fix: `keyMsg(" ")` in `viewer_test.go` now carries the `' '` rune, matching bubbletea's real `KeySpace` message; without it, typing a space into any textinput was a silent no-op in tests.

## Task 5: Final verification and documentation

- **Status:** done
- **Depends on:** Task 2, Task 3, Task 4
- **Size:** S
- **Can run in parallel with:** —
- **Docs:** [implementation.md#7-documentation-and-full-verification](./implementation.md#7-documentation-and-full-verification)

### Subtasks

- [x] 5.1 Run `$kk:test` to execute focused vault/CLI tests, formatting, vet, full tests, and race tests with `CAPY_DB_KEY`, `CAPY_VAULT_KEY`, and `-tags fts5` — 2026-09-12: `gofmt -l` clean over all tracked Go files, `make vet` clean, `make test` green (16 packages), `make test-race` green
- [x] 5.2 Run `$kk:document` to update `README.md`, `docs/architecture.md`, and command/TUI help for rename, clear, `list --name`, title precedence, and merge behavior — README: `rename` row, `list --name`, lookup sentence, TUI keys, merge paragraph, new "Naming sessions" section (secret-stripping surprise, duplicates, clear semantics, no Claude propagation); architecture.md: schema entry, "Session names & the effective title" section, merge reconciliation, CLI table, TUI keys; new [ADR-030](../../../adr/030-vault-session-names-and-latest-wins-merge.md). Cobra help for `vault rename` and `vault list --name` verified from the built binary
- [x] 5.3 Run `$kk:review-code` with Go input and address or durably record all findings — 2026-09-12, isolated mode over `master...HEAD` (code-reviewer: APPROVE, no P0–P2; pal/gemini-3.1-pro-preview). Applied: (corroborated) the TUI `f` filter now filters an in-memory snapshot refreshed only when the filter opens and after a rename (`Model.reloadSessions`, `listModel.all`) instead of re-querying the store per keystroke; static-message errors in this feature's code switched from `fmt.Errorf` to `errors.New`; a comment on `renameSessionAt`'s explicit `rows.Close()` explains why it is not deferred (the cursor must be released before the upsert on the same transaction — pal flagged it as non-idiomatic; it is deliberate). Declined: renaming `ContainsFold` (its doc comment already states the simple-lowercasing semantics; churn across store/CLI/TUI for a naming nuance). Observed, not touched: five pre-existing static `fmt.Errorf` calls elsewhere in `cmd/capy/vault.go` (rekey/merge/resume paths) predate this feature. No P0/P1 systemic findings to index
- [x] 5.4 Run `$kk:review-spec` against `docs/feat/wip/vault-session-renames/` and resolve implementation/documentation drift — 2026-09-12, isolated mode: CONFORMANT across Tasks 1–4. Applied: the viewer's detail sub-view help now advertises `e rename` (the binding already worked there); `design.md` status → Implemented; this file's header status updated; feature directory moved to `docs/feat/done/` at completion so ADR-030's design link resolves. No intentional deviations to index
- [x] 5.5 Run `make bench-quality` and `make bench-compare BASE=master TARGET=<branch>` (repository policy: `Search`/`SearchChunks` SQL changed) and record the comparison result — 2026-09-12: master baseline produced in a throwaway worktree (`bench-results/master.json`), branch run `bench-results/feat-vault_session_rename.json`; qualstat reports **no change on any metric** (retrieval quality across plaintext/transcript/vault_session/overall and every context-reduction metric identical, all deltas `~`). benchstat (perf) not installed and not required — ranking code is untouched; only the metadata LEFT JOIN changed

## Dependency Graph

```text
Task 1 ─┬─> Task 2 ──> Task 4 ─┐
        └─> Task 3 ────────────┴─> Task 5
```

## Completion notes (2026-09-12)

Where the implementation diverged from the plan: the TUI `f` filter was first built in the pre-existing shape (a store re-query per keystroke, now over an unfiltered read) and changed during the Task 5 review to an in-memory snapshot refreshed when the filter opens and after a rename — the plan only said "broaden `f` with the shared matcher". Width-bounding of every single-line input and ADR-030 were not in the plan; both came out of review and the documentation pass. Nothing turned out harder than expected in the store or merge layers; the one real cost was TUI test fidelity — a fake space keypress without its rune silently made every typed space a no-op, which looked like a matcher bug. Surprises worth knowing for future TUI work: bubbles `textinput` never re-bounds its horizontal scroll window on a `Width` change (only when the cursor leaves the window), bubbletea's standard renderer truncates rows wider than the terminal (so an over-wide input hides the cursor rather than breaking the layout, contrary to one external review claim), and `git check-ignore` on a directory pattern like `bench-results/` reports "not ignored" until the directory exists. The benchmark comparison against master was, as the design predicted, a zero delta on every metric.
