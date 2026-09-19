# Tasks: Vault Project Names

> Design: [design.md](design.md)
>
> Implementation: [implementation.md](implementation.md)
>
> Issue: [#92](https://github.com/serpro69/capy/issues/92)
>
> Status: pending — agreed design; post-design review and implementation pending
>
> Created: 2026-09-19
>
> Not Doing: Automatic worktree association, aliases/project registry, bulk edits, parent/child inheritance, history/undo, MCP mutation, transcript/FTS mutation, restore/resume relocation, team administration, new dependencies
>
> Accepted limits: Older binaries omit project metadata during merge; MCP reserves the exact star selector for all projects. See [implementation limits](implementation.md#deferred-work-and-accepted-limits).

Each task is a complete path with regression checks. M denotes a bounded behavior using existing mechanisms; repeated registration, projection and migration-template plumbing do not represent new architectural work. Parallel markers are conservative because several slices touch shared files. They describe scheduling possibilities, not authorization to spawn agents.

## Task 1: Set and clear a project through the CLI

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Slice 1](implementation.md#slice-1-set-and-clear-a-project-through-the-cli), [storage](design.md#ownership-and-storage)

### Subtasks

- [ ] 1.1 Add internal/vault/session_project.go with SessionProject, ProjectOptions, EffectiveProject, normalization and transactional SetSessionProject. Verify local set/replace/clear, literal prefix errors, clock rollback/overflow and independent title state in session_project_test.go.
- [ ] 1.2 Extend store.go's fresh schema and shared metadata join/scan; add the next name-keyed project migration in migrations.go. Verify fresh/legacy parity, idempotent opens, tombstones and delete cascade in migrations_test.go.
- [ ] 1.3 Add cmd/capy/vault_project.go and register project <id> <name>/--clear. Verify CLI responses, invalid combinations and --tui rejection using the binary harness.
- [ ] 1.4 Snapshot archived bytes, files, hash, size, FTS, chunks and index version around set/clear. Verify only project metadata changes and raw show JSON remains identical.
- [ ] 1.5 Run Slice 1's focused checks with both required keys and FTS5; record actual results below this task during implementation.

## Task 2: Browse reassigned sessions in the CLI

- **Status:** pending
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Slice 2](implementation.md#slice-2-browse-reassigned-sessions-in-the-cli), [selection](design.md#project-selection)

### Subtasks

- [ ] 2.1 Add the shared effective-project SQL expression and literal predicate; use them in ListSessions before limits. Verify older eligible sessions survive limits and title/platform/child filters compose correctly.
- [ ] 2.2 Update cmd/capy/vault.go list/show, ambiguity, delete and child displays to use effective projects with original-path detail. Verify labels are literal, including a custom value equal to the raw path.
- [ ] 2.3 Add list JSON project while retaining project_path and every existing field; keep show --format json verbatim. Verify both projections in CLI fixtures.
- [ ] 2.4 Add SQL/Go resolver parity and literal-query tests covering unset/set/clear, percent, underscore, backslash, quotes and case behavior. Run Slice 2's focused checks.

## Task 3: Search reassigned transcripts through the CLI

- **Status:** pending
- **Depends on:** Task 2
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Slice 3](implementation.md#slice-3-search-reassigned-transcripts-through-the-cli)

### Subtasks

- [ ] 3.1 Add SearchOptions.ProjectPath, reject simultaneous effective/raw scope, and update store.go Search to select project metadata and filter before rank/limit. Verify both scopes and limit regression cases.
- [ ] 3.2 Populate SearchResult.Project and CustomProject without changing ProjectPath; update CLI search display and JSON. Verify resolved labels and raw-path compatibility.
- [ ] 3.3 Prove project-only words do not become transcript matches, metadata joins do not duplicate hits, and ranking/anchors remain stable for equivalent candidate sets. Run Slice 3's focused checks.

## Task 4: Query custom projects through capy_vault_search

- **Status:** pending
- **Depends on:** Task 3
- **Size:** M
- **Can run in parallel with:** Task 5
- **Docs:** [Slice 4](implementation.md#slice-4-query-custom-projects-through-capy_vault_search)

### Subtasks

- [ ] 4.1 Update chunk_search.go's project joins, row mapping and shared predicate wiring in both retrieval layers. Verify effective/raw scope and candidate filtering before retrieval limits.
- [ ] 4.2 Add the shared MCP request-scope resolver in tool_vault_search.go and use it in handleVaultSearch. Verify omitted/empty project, explicit custom/raw value, star and all_projects precedence.
- [ ] 4.3 Render effective project metadata in formatVaultHit and update tools.go help. Verify a renamed current-directory session stays visible by default, while a name alone does not associate a different checkout.
- [ ] 4.4 Run Slice 4's focused tests, including existing disabled-vault, date and backlog behavior.

## Task 5: Inspect effective project statistics

- **Status:** pending
- **Depends on:** Task 2
- **Size:** M
- **Can run in parallel with:** Task 4
- **Docs:** [Slice 5](implementation.md#slice-5-inspect-effective-project-statistics), [JSON](design.md#read-surfaces-and-json-compatibility)

### Subtasks

- [ ] 5.1 Preserve raw VaultStats.ByProject and add ByEffectiveProject with exact-string grouping. Verify grouping across different raw paths, clear fallback and unchanged child/platform totals.
- [ ] 5.2 Update ordinary stats output to effective groups; retain stats JSON projects and add project_groups. Verify both raw and effective JSON contracts, empty vault and case-distinct names.
- [ ] 5.3 Run Slice 5's checks and confirm every session contributes once to each breakdown.

## Task 6: Preserve federated search availability

- **Status:** pending
- **Depends on:** Task 4
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Slice 6](implementation.md#slice-6-preserve-federated-search-availability), [availability](design.md#federation-availability-and-errors)

### Subtasks

- [ ] 6.1 Implement metadata-only HasSessionsInProject using the validated shared predicate. Verify existence agrees with effective/raw membership and mixed scope fails.
- [ ] 6.2 Reuse the MCP scope resolver in tool_search.go; replace vaultStatsHaveSessions with the existence query and pass the same scope to SearchChunks. Verify empty-KB searches return eligible named sessions and retain physical default scoping.
- [ ] 6.3 Surface preflight errors in-band while allowing the actual vault pass and successful knowledge results. Verify errors cannot masquerade as a definitive empty corpus.
- [ ] 6.4 Remove only the helper made unused by this change, update capy_search help, and run existing federation/kind/source regressions plus Slice 6's checks.

## Task 7: Merge project assignments across personal vaults

- **Status:** pending
- **Depends on:** Task 2
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Slice 7](implementation.md#slice-7-merge-project-assignments-across-personal-vaults), [merge](design.md#cross-machine-merge)

### Subtasks

- [ ] 7.1 Feature-detect/read project source state in merge.go; normalize foreign empty overrides consistently before metadata-only effective-project selection. Verify legacy sources, ASCII literal-matcher parity and selection before blob loading.
- [ ] 7.2 Add deterministic project ordering/reconciliation and carry state through SessionWrite/writeRecord. Verify source tuples remain verbatim and new session plus metadata commits atomically.
- [ ] 7.3 Reconcile title and project independently in one transaction on equal-hash, smaller and excluded-source branches. Verify changed fields survive independently, statuses count once, and no metadata orphan is created.
- [ ] 7.4 Add effective/raw merge-report fields without changing disk-import scope; implement matching dry-run projection. Verify destination-wins display, repeated/reversed convergence and transcript replacement behavior.
- [ ] 7.5 Update TestMergeFrom_ProjectFilter for the intentional removal of location-hint matching, preserving escaped-metacharacter coverage. Verify source vaults remain unmigrated and unchanged.
- [ ] 7.6 Add deterministic edit/merge and title/project concurrency cases; run Slice 7's focused race checks.

## Task 8: Browse and search custom projects in the TUI

- **Status:** pending
- **Depends on:** Task 3
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Slice 8](implementation.md#slice-8-browse-and-search-custom-projects-in-the-tui)

### Subtasks

- [ ] 8.1 Carry Options.Project from CLI list/search launches into app.go, list options and searchModel; retain platform scope. Verify initial load, reload, child toggle and mode transitions retain project scope.
- [ ] 8.2 Update list/search/viewer project projections and imported-path detail; change the local f filter's project operand to EffectiveProject. Verify replaced raw paths are not project aliases while title/UUID matching still works.
- [ ] 8.3 Preserve literal labels, fallback path shortening and bounded headers. Run Slice 8's focused tests plus existing render/navigation checks.

## Task 9: Edit project assignments in the TUI

- **Status:** pending
- **Depends on:** Task 8
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Slice 9](implementation.md#slice-9-edit-a-projects-assignment-in-the-tui), [interaction](design.md#cli-and-tui-interaction)

### Subtasks

- [ ] 9.1 Extend dataStore/stubs and the root editor with an explicit title/project target. Bind ctrl+g in list, active finder, search and viewer without intercepting an already open editor. Verify existing title and text-input keys.
- [ ] 9.2 Load authoritative metadata, show original path separately and prefill only the override. Verify blank fallback/clear, path-looking names, input validation without truncation and narrow-terminal layout.
- [ ] 9.3 Save asynchronously, refresh all affected views under active scope and guard against stale search results. Verify error text retention, duplicate-submit protection, cancellation, saved-but-refresh-failed status and filter disappearance.
- [ ] 9.4 Update screen/filter help and verify independent child edits preserve parent/viewer state. Run Slice 9's checks and the existing rename suite.

## Task 10: Maintenance, documentation and final verification

- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3, Task 4, Task 5, Task 6, Task 7, Task 8, Task 9
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Slice 10](implementation.md#slice-10-maintenance-documentation-and-final-verification), [verification](design.md#verification-and-review)

### Subtasks

- [ ] 10.1 Extend encrypted maintenance and edit/delete race fixtures for overrides and clears. Verify survival through re-import replacement, reindex, compact and rekey; prove archived bytes and restore/resume paths remain unchanged.
- [ ] 10.2 Use kk:document to update README, architecture, command/tool help and scope/JSON/merge compatibility notes. If routing prose changes, update routing.go and regenerate .capy/AGENTS.md together; verify both generated-artifact guard tests.
- [ ] 10.3 Measure 10,000-session metadata/query behavior with and without overrides; record baseline, environment and results here. Investigate material regressions and unexpected blob/per-hit reads.
- [ ] 10.4 Invoke kk:test with both required keys; complete appropriate FTS5, vet, full-suite, race and glamour-tagged TUI checks. Record results; do not relax assertions to hide failures.
- [ ] 10.5 Run quality benchmarks and compare measured master/implementation reports, observing detached-baseline filename rules. Record comparison and resolve unexplained regressions.
- [ ] 10.6 Invoke kk:review-code with Go input and kk:review-spec for this feature. Fix blockers; record any accepted unresolved finding with reason and next step in these documents.
- [ ] 10.7 Confirm all success criteria and documentation links against the final implementation before marking the feature done.

## Dependency Graph

```text
Task 1 --> Task 2 --> Task 3 --> Task 4 --> Task 6
Task 2 --> Task 5
Task 2 --> Task 7
Task 3 --> Task 8 --> Task 9

Tasks 1-9 -------------------------------------> Task 10
```

## Design-stage record

- Go profile, individual-developer scope, explicit naming, refined override semantics, CLI/TUI editing, persistence, filtering and interface/JSON contracts were confirmed in the design conversation.
- Existing code and ADR-030 were inspected; the optional isolated CoVe pass was declined.
- No implementation or runtime tests have been performed as part of authoring this plan.
- Next gate: kk:review-design over design.md, implementation.md and tasks.md.
- No agreed implementation work is deferred. Accepted compatibility limits and any later deferred findings belong in [the implementation record](implementation.md#deferred-work-and-accepted-limits).
