# Tasks: Vault Session Renames

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> Status: pending
> Created: 2026-09-05
> Not Doing: Claude propagation, archived transcript mutation, Claude custom-title ingestion, name matching in FTS, rename history, interactive merge conflicts, team/shared-vault administration

## Task 1: Persist and resolve vault-owned session names

- **Status:** pending
- **Depends on:** —
- **Size:** L — spans impl §1–§3 (migration, domain reads, rename op) as one deliberate contract-first slice; splitting would ship a schema without its owning operations, and Tasks 2/3/4 block on the full contract either way
- **Slicing:** Contract-First — establishes the new persisted boundary used by independent CLI, merge, and TUI slices
- **Can run in parallel with:** —
- **Docs:** [implementation.md#1-persistence-contract-and-migration](./implementation.md#1-persistence-contract-and-migration), [implementation.md#2-domain-types-and-effective-title-reads](./implementation.md#2-domain-types-and-effective-title-reads), [implementation.md#3-store-rename-operation](./implementation.md#3-store-rename-operation)

### Subtasks

- [ ] 1.1 Add shared `vault_session_names` DDL and idempotent `0005_session_names` migration in `internal/vault/store.go` and `internal/vault/migrations.go`; cover fresh/legacy schemas, reruns, and cascade deletion in `internal/vault/migrations_test.go` and `internal/vault/store_test.go`
- [ ] 1.2 Add name state, shared normalization/validation, and one effective-title resolver in `internal/vault`; preserve imported `vault_sessions.title` ownership
- [ ] 1.3 Update `GetSession`, `ListSessions`, and ambiguous-prefix candidates to resolve effective titles while retaining imported title provenance
- [ ] 1.4 Implement transactional rename/clear by UUID prefix with monotonic local timestamps and immutable transcript/index assertions
- [ ] 1.5 Add focused race coverage for rename/delete and repeated concurrent rename behavior (rename/merge race is Task 3.5)
- [ ] 1.6 Add maintenance-retention tests: rename, then re-import/transcript replacement, reindex, compact, and backup-API rekey each preserve name and tombstone state

## Task 2: Expose rename, clear, and name lookup through the CLI

- **Status:** pending
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** Task 3
- **Docs:** [implementation.md#2-domain-types-and-effective-title-reads](./implementation.md#2-domain-types-and-effective-title-reads), [implementation.md#4-cli-rename-and-name-lookup](./implementation.md#4-cli-rename-and-name-lookup)

### Subtasks

- [ ] 2.1 Add and register `capy vault rename <session-id> <name>` plus mutually exclusive `--clear` handling in `cmd/capy/vault.go`, reusing existing lookup errors and rejecting `--tui`
- [ ] 2.2 Add literal `ListOptions` name filtering (Go-side Unicode case folding per impl §2.3) and expose it as `vault list --name`; combine it with project filtering, cover a non-ASCII fixture, and return effective titles in table/JSON output
- [ ] 2.3 Update `Search` and `SearchChunks` to resolve effective result titles, then render those titles in ordinary CLI transcript-search rows without making name-only text an FTS match
- [ ] 2.4 Extend `cmd/capy/vault_test.go` and `cmd/capy/vault_unit_test.go` for rename, replace, clear, validation, lookup errors, name filtering, and byte/hash preservation

## Task 3: Merge name state independently from transcript content

- **Status:** pending
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** Task 2
- **Docs:** [implementation.md#5-cross-vault-name-reconciliation](./implementation.md#5-cross-vault-name-reconciliation)

### Subtasks

- [ ] 3.1 Feature-detect and read `vault_session_names` from an unmigrated source vault in `internal/vault/merge.go`, preserving legacy-source support
- [ ] 3.2 Add latest-`(renamed_at_ns, machine_id)` conditional reconciliation with null clear tombstones, the equal-tuple value tie-break (total order), verbatim source-tuple writes (no local re-stamp), absent-destination wins, and empty-source-title normalization to tombstone
- [ ] 3.3 Integrate atomic new-session+name writes (extend `SessionWrite`/`WriteBatch`) and name-only updates for the same-hash, smaller-transcript, and zero-message-excluded branches whenever the destination session exists; make dry-run/status output reflect effective results
- [ ] 3.4 Extend `internal/vault/merge_test.go` with newer/older/tie/equal-tuple/tombstone, transcript-branch (including zero-message source → populated destination), legacy, failure, dry-run, convergence, and verbatim-tuple idempotence cases
- [ ] 3.5 Add concurrent rename-vs-merge race coverage under `-race` with deterministic winner assertions

## Task 4: Rename sessions from every TUI browsing mode

- **Status:** pending
- **Depends on:** Task 1, Task 2
- **Size:** M
- **Can run in parallel with:** Task 3
- **Docs:** [implementation.md#6-tui-rename-flow](./implementation.md#6-tui-rename-flow)

### Subtasks

- [ ] 4.1 Extend `internal/vault/tui/app.go`'s consumer-owned store interface and root model with an asynchronous rename editor and result messages
- [ ] 4.2 Bind `e` in list (navigation state) and viewer plus `ctrl+e` in search (bare `e` must keep editing the query/filter inputs), prefill the effective title, treat empty submit as clear, retain input on errors, and suppress duplicate pending writes
- [ ] 4.3 Refresh authoritative list/search/view state after success while preserving selection and active filters where possible
- [ ] 4.4 Broaden `f` filtering to effective title/project/UUID with the shared Unicode-folding matcher, render titles in search rows, and update help without changing `r`/`R` or viewer marker navigation
- [ ] 4.5 Add TUI model/keybinding tests for all entry modes, typing `e` into the search query and list filter, success, clear, cancel, error, pending, filtered disappearance, refresh, and existing navigation regressions

## Task 5: Final verification and documentation

- **Status:** pending
- **Depends on:** Task 2, Task 3, Task 4
- **Size:** S
- **Can run in parallel with:** —
- **Docs:** [implementation.md#7-documentation-and-full-verification](./implementation.md#7-documentation-and-full-verification)

### Subtasks

- [ ] 5.1 Run `$kk:test` to execute focused vault/CLI tests, formatting, vet, full tests, and race tests with `CAPY_DB_KEY`, `CAPY_VAULT_KEY`, and `-tags fts5`
- [ ] 5.2 Run `$kk:document` to update `README.md`, `docs/architecture.md`, and command/TUI help for rename, clear, `list --name`, title precedence, and merge behavior
- [ ] 5.3 Run `$kk:review-code` with Go input and address or durably record all findings
- [ ] 5.4 Run `$kk:review-spec` against `docs/wip/vault-session-renames/` and resolve implementation/documentation drift
- [ ] 5.5 Run `make bench-quality` and `make bench-compare BASE=master TARGET=<branch>` (repository policy: `Search`/`SearchChunks` SQL changed) and record the comparison result

## Dependency Graph

```text
Task 1 ─┬─> Task 2 ──> Task 4 ─┐
        └─> Task 3 ────────────┴─> Task 5
```
