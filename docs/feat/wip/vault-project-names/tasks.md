# Tasks: Vault Project Names

> Design: [design.md](design.md)
>
> Implementation: [implementation.md](implementation.md)
>
> Issue: [#92](https://github.com/serpro69/capy/issues/92)
>
> Status: in-progress — Tasks 1–6 done; Task 7 next
>
> Created: 2026-09-19
>
> Not Doing: Automatic worktree association, aliases/project registry, bulk edits, parent/child inheritance, history/undo, MCP mutation, transcript/FTS mutation, restore/resume relocation, team administration, new dependencies
>
> Accepted limits: Older binaries omit project metadata during merge; MCP reserves the exact star selector for all projects. See [implementation limits](implementation.md#deferred-work-and-accepted-limits).

Each task is a complete path with regression checks. M denotes a bounded behavior using existing mechanisms; repeated registration, projection and migration-template plumbing do not represent new architectural work. Parallel markers are conservative because several slices touch shared files. They describe scheduling possibilities, not authorization to spawn agents.

## Task 1: Set and clear a project through the CLI

- **Status:** done
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Slice 1](implementation.md#slice-1-set-and-clear-a-project-through-the-cli), [storage](design.md#ownership-and-storage)

### Subtasks

- [x] 1.1 Add internal/vault/session_project.go with SessionProject, ProjectOptions, EffectiveProject, normalization and transactional SetSessionProject. Verify local set/replace/clear, literal prefix errors, clock rollback/overflow and independent title state in session_project_test.go.
- [x] 1.2 Extend store.go's fresh schema and shared metadata join/scan; add the next name-keyed project migration in migrations.go. Verify fresh/legacy parity, idempotent opens, tombstones and delete cascade in migrations_test.go.
- [x] 1.3 Add cmd/capy/vault_project.go and register project <id> <name>/--clear. Verify CLI responses, invalid combinations and --tui rejection using the binary harness.
- [x] 1.4 Snapshot archived bytes, files, hash, size, FTS, chunks and index version around set/clear. Verify only project metadata changes and raw show JSON remains identical.
- [x] 1.5 Run Slice 1's focused checks with both required keys and FTS5; record actual results below this task during implementation.

### Task 1 verification and review (2026-09-20)

Implementation and documentation are complete for this slice. Task 2 is next;
Tasks 2–10 remain pending. See the [implementation record](implementation.md#task-1-implementation-record-2026-09-20).

All Go checks used FTS5 and both required test keys (`CAPY_DB_KEY=test-key-for-development`,
`CAPY_VAULT_KEY=test-key`).

- PASS: `go test -tags fts5 -count=1 ./internal/vault ./cmd/capy -run 'Project|Migration|SessionName|Rename'`
  (vault 24.457s, CLI 15.255s).
- PASS: `go test -race -tags fts5 -count=1 ./internal/vault -run 'SessionProject|NormalizeSessionName|Rename|SessionName'`
  (22.483s; no races reported).
- PASS: the migration `/legacy` subtest run independently, after moving its
  fresh-schema reference outside the sibling subtests.
- PASS: `make vet` and `git diff --check`.
- PASS: `go test -tags fts5 ./... -skip '^TestCodexCanary$'` with an empty temporary
  `XDG_CONFIG_HOME` (all packages; vault 104.439s, CLI 57.624s, server 94.266s).
- Initial `make test` did **not** pass: several pre-existing CLI fixtures inherited
  personal `vault.min_session_bytes=430080`, and the live-corpus `TestCodexCanary`
  failed decoding an existing local rollout. The shared Claude fixture now isolates
  XDG configuration; the full rerun also isolated it for other CLI fixtures.
  No assertion was relaxed and no production import policy was changed.
- `kk:review-code:isolated`: independent PAL review with Gemini 3.1 Pro found one
  LOW issue, redundant normalization in `projectOptions`; removed it because
  `SetSessionProject` validates before opening the database. No unresolved review
  findings and no P0/P1 findings to index. The code-reviewer sub-agent could not
  access files with its permitted tools, so the skill's external-reviewer fallback
  was used. Task scope excluded the pending slices.
- `kk:test` and `kk:document` completed. No new project conventions were indexed:
  the metadata ownership/clock/normalization patterns are already in the design
  and ADR-030. Quality benchmarks are reserved for the retrieval-changing slices
  and Task 10; this slice changes no indexing, ranking or executor logic.

**Known verification limits / follow-up:**

- The excluded live-corpus canary fails in `tallyCodexRaw` before project metadata
  is involved: `unexpected end of JSON input` at zero-based line 182 of
  `~/.codex/sessions/2026/09/17/rollout-2026-09-17T08-31-24-01a0ae10-15df-7ba1-909c-aaa8b95ac150.jsonl`.
  This local input predates the task and was left untouched. Before Task 10's
  unrestricted full-suite gate, inspect/recover the malformed source transcript
  or address malformed-input handling in the independent canary; then rerun
  `TestCodexCanary` and `make test`. Do not claim the unrestricted suite passed.
- CLI fixtures outside `setupVaultEnv` still inherit personal global configuration.
  Standardizing all CLI fixture setup is outside Task 1. Until then, run the suite
  with a temporary empty `XDG_CONFIG_HOME`; a follow-up should isolate the remaining
  Codex/restore/merge fixtures while retaining explicit config-precedence tests.

## Task 2: Browse reassigned sessions in the CLI

- **Status:** done
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Slice 2](implementation.md#slice-2-browse-reassigned-sessions-in-the-cli), [selection](design.md#project-selection)

### Subtasks

- [x] 2.1 Add the shared effective-project SQL expression and literal predicate; use them in ListSessions before limits. Verify older eligible sessions survive limits and title/platform/child filters compose correctly.
- [x] 2.2 Update cmd/capy/vault.go list/show, ambiguity, delete and child displays to use effective projects with original-path detail. Verify labels are literal, including a custom value equal to the raw path.
- [x] 2.3 Add list JSON project while retaining project_path and every existing field; keep show --format json verbatim. Verify both projections in CLI fixtures.
- [x] 2.4 Add SQL/Go resolver parity and literal-query tests covering unset/set/clear, percent, underscore, backslash, quotes and case behavior. Run Slice 2's focused checks.

### Task 2 verification and review (2026-09-20)

Task 2 is complete; Task 3 is next. Tasks 3–10 remain pending. See the
[implementation record](implementation.md#task-2-implementation-record-2026-09-20).

All Go tests used FTS5 and both required test keys (`CAPY_DB_KEY=test-key-for-development`,
`CAPY_VAULT_KEY=test-key`); binary fixtures use their existing isolated vault keys.

- PASS: `go test -tags fts5 -count=1 ./internal/vault ./cmd/capy -run 'Project|ListSessions|WriteShowHeader|PrintDeletePreview|WriteLookupCandidates'`
  (vault 13.366s, CLI 20.434s). The first run exposed a missing child in the new
  test fixture; fixed by creating the existing child rollout fixture and using
  default platform discovery, then reran successfully.
- PASS: `go test -tags fts5 -count=1 ./... -skip '^TestCodexCanary$'` with an empty
  temporary `XDG_CONFIG_HOME` (all packages; CLI 69.675s, server 94.578s,
  vault 107.004s). The unrestricted suite was not claimed: the malformed
  live-transcript canary and fixture-isolation follow-up documented under Task 1
  remain unchanged.
- PASS: `make vet` and `git diff --check`.
- PASS: `go test -race -tags fts5 -count=1 ./internal/vault -run 'SessionProject_(ListFiltering|SQLResolverAndLiteralQueries)|ListSessionsNameFilter'`
  (3.039s; no races).
- `kk:review-code:isolated`: the code-reviewer sub-agent could not read files
  because its permitted reader tools were unavailable. The skill's independent
  external-reviewer fallback (PAL, Gemini 3.1 Pro) completed the review and found
  one LOW presentation issue: the Markdown original-path row followed dates.
  Moved it directly below Project. No unresolved findings or P0/P1 findings to
  index.
- PASS after the review fix: `go test -tags fts5 -count=1 ./cmd/capy -run 'VaultProject|WriteShowHeader|PrintDeletePreview|WriteLookupCandidates'`
  (20.299s). The list display assertion also respects the existing column width
  so a long home directory does not make the fixture fail.
- `kk:test` and `kk:document` completed. README, architecture, CLI help and feature
  records describe the implemented scope. No new conventions were indexed:
  resolver precedence, literal labels and filter ordering are already captured
  in the design. No retrieval/indexing/executor code changed; quality benchmarks
  remain with the retrieval-changing slices and Task 10.

No Task 2 requirement is deferred.

## Task 3: Search reassigned transcripts through the CLI

- **Status:** done
- **Depends on:** Task 2
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Slice 3](implementation.md#slice-3-search-reassigned-transcripts-through-the-cli)

### Subtasks

- [x] 3.1 Add SearchOptions.ProjectPath, reject simultaneous effective/raw scope, and update store.go Search to select project metadata and filter before rank/limit. Verify both scopes and limit regression cases.
- [x] 3.2 Populate SearchResult.Project and CustomProject without changing ProjectPath; update CLI search display and JSON. Verify resolved labels and raw-path compatibility.
- [x] 3.3 Prove project-only words do not become transcript matches, metadata joins do not duplicate hits, and ranking/anchors remain stable for equivalent candidate sets. Run Slice 3's focused checks.

### Task 3 verification and review (2026-09-20)

Task 3 is complete; Task 4 is next. Tasks 4–10 remain pending. See the
[implementation record](implementation.md#task-3-implementation-record-2026-09-20).

All Go tests used FTS5 and both required keys (`CAPY_DB_KEY=test-key-for-development`,
`CAPY_VAULT_KEY=test-key`); binary fixtures use their existing isolated vault keys.

- PASS: `go test -tags fts5 -count=1 ./internal/vault ./cmd/capy -run 'Project|SearchResolves'`
  (vault 14.171s, CLI 27.303s); rerun after the review fix passed
  (vault 14.060s, CLI 26.936s).
- PASS: `go test -race -tags fts5 -count=1 ./internal/vault -run 'SessionProject_(Search|SQLResolverAndLiteralQueries)|SearchResolves'`
  (3.345s); rerun after the review fix passed (3.369s; no races).
- PASS: `make vet`, `git diff --check`, and `gofmt -l` over changed Go files
  (no formatting changes reported).
- PASS: `go test -tags fts5 -count=1 ./... -skip '^TestCodexCanary$'` with an empty
  temporary `XDG_CONFIG_HOME` (all packages; CLI 76.659s, server 95.402s,
  vault 107.519s). This run preceded the small shared-resolver review fix;
  the focused and race suites above verified the final code afterward.
  The known malformed live transcript and remaining CLI fixture-isolation
  follow-up under Task 1 remain; the unrestricted full suite is not claimed.
- PASS: `make bench-quality` before and after the search changes, then
  `make bench-compare BASE=vault-project-task3-before TARGET=feat-vault_project_name`.
  Reports are `bench-results/vault-project-task3-before.json` and
  `bench-results/feat-vault_project_name.json`; identical dataset fingerprint
  `7d45338724b05181ebc92bd0b74eb7708bdd4fba27a8d6830b54a127f2b6ba2d`.
  All retrieval/context-reduction metrics were unchanged, including vault
  R@1=0.933, R@5=0.967, NDCG@10=0.954, MRR=0.950, and negative FP=0/10.
  Existing FTS fallback warnings and the miss for `tr_005_q3` occurred in both
  runs. `benchstat` is unavailable, so no performance comparison is claimed.
  These quality benchmarks cover existing chunk/knowledge retrieval; the new
  per-line ordering/limit fixtures directly cover this task's changed query.
  Task 10 retains the full-feature master comparison and scale measurements.
- `kk:review-code:isolated`: the code-reviewer agent could not access its required
  reader tools. The skill's independent external-review fallback (PAL,
  Gemini 3.1 Pro) completed the review. Its MEDIUM finding identified a temporary
  `SessionProject` allocation in the search loop; compiler escape analysis
  confirmed it. Extracted `effectiveProject` for both session and search
  projections, preserving one precedence rule; compiler analysis confirmed
  removal of that allocation. The LOW alignment suggestion required no change:
  `gofmt` was already clean. Independent follow-up closed both findings and
  reported no remaining actionable issues. No P0/P1 findings to index.
- `kk:test` and `kk:document` completed. No new conventions were indexed: the
  scope, projection and ranking contracts are already documented in the design.

No Task 3 requirement is deferred. The implementation follows Slice 3; the only
review-driven adjustment was sharing the resolver without temporary session state.

## Task 4: Query custom projects through capy_vault_search

- **Status:** done
- **Depends on:** Task 3
- **Size:** M
- **Can run in parallel with:** Task 5
- **Docs:** [Slice 4](implementation.md#slice-4-query-custom-projects-through-capy_vault_search)

### Subtasks

- [x] 4.1 Update chunk_search.go's project joins, row mapping and shared predicate wiring in both retrieval layers. Verify effective/raw scope and candidate filtering before retrieval limits.
- [x] 4.2 Add the shared MCP request-scope resolver in tool_vault_search.go and use it in handleVaultSearch. Verify omitted/empty project, explicit custom/raw value, star and all_projects precedence.
- [x] 4.3 Render effective project metadata in formatVaultHit and update tools.go help. Verify a renamed current-directory session stays visible by default, while a name alone does not associate a different checkout.
- [x] 4.4 Run Slice 4's focused tests, including existing disabled-vault, date and backlog behavior.

### Task 4 verification and review (2026-09-20)

Task 4 is complete; Task 5 is next. Tasks 5–10 remain pending. See the
[implementation record](implementation.md#task-4-implementation-record-2026-09-20).

All Go tests used FTS5 and both required test keys (`CAPY_DB_KEY=test-key-for-development`,
`CAPY_VAULT_KEY=test-key`); fixtures retain their existing isolated vault keys.

- PASS: `go test -tags fts5 -count=1 ./internal/vault ./internal/server -run 'Project|VaultSearch|SearchChunks|FormatVaultHit'`
  (vault 19.399s, server 8.689s), including disabled-vault, date, backlog and
  platform regressions. New fixtures exercise both retrieval layers beyond the
  candidate limit and explicit/default/widened MCP scope.
- PASS: `go test -race -tags fts5 -count=1 ./internal/vault ./internal/server -run 'SessionProject_Chunk|VaultSearch_Project|FormatVaultHit|Search_ProjectAssignment'`
  (vault 2.314s, server 3.245s; no races).
- PASS: `go test -tags fts5 -count=1 ./... -skip '^TestCodexCanary$'` with an empty
  temporary `XDG_CONFIG_HOME` (all packages; CLI 78.999s, server 98.944s,
  vault 111.128s). The known malformed live-transcript canary and remaining
  fixture-isolation follow-up documented under Task 1 remain unchanged. The
  unrestricted full suite is not claimed.
- PASS: `make vet`, `git diff --check`, and `gofmt -l` over changed Go files.
- PASS: `make bench-quality BENCH_BRANCH=vault-project-task4-before` before code
  changes and `make bench-quality` afterward, then
  `make bench-compare BASE=vault-project-task4-before TARGET=feat-vault_project_name`.
  Reports are `bench-results/vault-project-task4-before.json` and
  `bench-results/feat-vault_project_name.json`, with identical dataset fingerprint
  `7d45338724b05181ebc92bd0b74eb7708bdd4fba27a8d6830b54a127f2b6ba2d`.
  All retrieval/context-reduction metrics were unchanged; vault R@1=0.933,
  R@5=0.967, NDCG@10=0.954, MRR=0.950 and negative FP=0/10. Existing FTS fallback
  warnings and the `tr_005_q3` miss occurred in both runs. `benchstat` is
  unavailable, so no performance comparison is claimed. The full-feature master
  comparison and 10,000-session measurements remain Task 10.
- `kk:review-code:isolated`: the code-reviewer sub-agent could not read files
  with its permitted tools. The skill's independent external-reviewer fallback
  (PAL, Gemini 3.1 Pro) completed the review and found one LOW test clarity issue:
  the `capy_search` compatibility regression used `vaultSearchReq`, whose request
  name is `capy_vault_search`. Replaced it with an explicit `capy_search` request.
  No unresolved findings or P0/P1 findings to index.
- PASS after that test-only review fix:
  `go test -tags fts5 -count=1 ./internal/server -run '^TestSearch_ProjectAssignmentKeepsDefaultScope$'`
  (0.903s). Production code is unchanged since the full, race and benchmark runs.
- `kk:test` and `kk:document` completed. No new project conventions were indexed:
  scope separation, literal labels and pre-limit filtering are already captured
  by the feature design. No new dependencies or generated artifacts.

No Task 4 requirement is deferred. The only plan adjustment is the compatibility
change to the shared `capy_search` caller: it explicitly passes raw `ProjectPath`
until Task 6 updates effective-project selection and availability together.

## Task 5: Inspect effective project statistics

- **Status:** done
- **Depends on:** Task 2
- **Size:** M
- **Can run in parallel with:** Task 4
- **Docs:** [Slice 5](implementation.md#slice-5-inspect-effective-project-statistics), [JSON](design.md#read-surfaces-and-json-compatibility)

### Subtasks

- [x] 5.1 Preserve raw VaultStats.ByProject and add ByEffectiveProject with exact-string grouping. Verify grouping across different raw paths, clear fallback and unchanged child/platform totals.
- [x] 5.2 Update ordinary stats output to effective groups; retain stats JSON projects and add project_groups. Verify both raw and effective JSON contracts, empty vault and case-distinct names.
- [x] 5.3 Run Slice 5's checks and confirm every session contributes once to each breakdown.

### Task 5 verification and review (2026-09-21)

Task 5 is complete; Task 6 is next. Tasks 6–10 remain pending. See the
[implementation record](implementation.md#task-5-implementation-record-2026-09-21).

All Go checks used FTS5 and both required keys (`CAPY_DB_KEY=test-key-for-development`,
`CAPY_VAULT_KEY=test-key`); binary fixtures retain their isolated vault keys.

- PASS: `go test -tags fts5 -count=1 ./internal/vault ./cmd/capy -run 'Project|Stats'`
  (vault 9.942s, CLI 31.386s).
- PASS: `make vet`, `git diff --check`, and `gofmt -l` over changed Go files
  (no formatting changes reported).
- PASS: `go test -tags fts5 -count=1 ./...`, with an empty temporary
  `XDG_CONFIG_HOME` and `TMPDIR=/private/tmp` (all packages; CLI 76.154s,
  server 62.069s, vault 71.346s). No test exclusions were used; the earlier
  live-transcript canary failure did not recur in this checkout.
- PASS: `go test -race -tags fts5 -count=1 ./internal/vault ./cmd/capy -run 'SessionProject_Stats|VaultProject_StatsJSON|StatsToJSON|StatsByPlatform'`
  with the same environment (vault 2.165s, CLI 5.995s; no races reported).
- The initial full run failed three existing restore fixtures because macOS
  resolves `/var` to `/private/var`, while their expected paths use the unresolved
  temporary root. The complete rerun above used a canonical temporary path;
  no assertions or production restore behavior were changed.
- `kk:review-code:isolated`: the code-reviewer sub-agent could not access files
  with its permitted reader tools. The skill's independent external-reviewer
  fallback (PAL, Gemini 3.1 Pro) completed the review and reported zero issues
  across all severities. No unresolved Task 5 findings or P0/P1 findings to index.
- `kk:test` and `kk:document` completed. README, architecture, command help and
  feature records describe the completed stats contract. No new conventions were
  indexed: metadata ownership, grouping and JSON compatibility are already in
  the design. No retrieval/indexing/executor changes require quality benchmarks
  for this slice; Task 10 retains the scale measurement and full-feature comparison.

**Known verification follow-up:** `TestRestoreSession_OverwritePolicy`,
`TestRestoreSession_SymlinkRootResolved` and
`TestVaultCodex_RestoreDefaultRootIsCodexHome` still assume temporary paths have no
symlinked ancestors. Fixing these unrelated restore fixtures is outside Task 5.
Normalize their expected temporary roots with `filepath.EvalSymlinks`, keeping
the existing containment and content assertions, then rerun them under macOS's
default `TMPDIR`. Until then, use the canonical `TMPDIR` above for full-suite
verification. The separate XDG fixture-isolation follow-up under Task 1 also remains.

No Task 5 implementation requirement is deferred.

## Task 6: Preserve federated search availability

- **Status:** done
- **Depends on:** Task 4
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Slice 6](implementation.md#slice-6-preserve-federated-search-availability), [availability](design.md#federation-availability-and-errors)

### Subtasks

- [x] 6.1 Implement metadata-only HasSessionsInProject using the validated shared predicate. Verify existence agrees with effective/raw membership and mixed scope fails.
- [x] 6.2 Reuse the MCP scope resolver in tool_search.go; replace vaultStatsHaveSessions with the existence query and pass the same scope to SearchChunks. Verify empty-KB searches return eligible named sessions and retain physical default scoping.
- [x] 6.3 Surface preflight errors in-band while allowing the actual vault pass and successful knowledge results. Verify errors cannot masquerade as a definitive empty corpus.
- [x] 6.4 Remove only the helper made unused by this change, update capy_search help, and run existing federation/kind/source regressions plus Slice 6's checks.

### Task 6 verification and review (2026-09-22)

Task 6 is complete; Task 7 is next. Tasks 7–10 remain pending. See the
[implementation record](implementation.md#task-6-implementation-record-2026-09-22).

All Go checks used FTS5 and both required keys (`CAPY_DB_KEY=test-key-for-development`,
`CAPY_VAULT_KEY=test-key`), with `TMPDIR=/private/tmp`. Fixtures retain their
existing isolated vault keys.

- PASS: `go test -tags fts5 -count=1 ./internal/vault ./internal/server -run 'SessionProject_(HasSessions|SQLResolverAndLiteralQueries)|Search_.*(Project|Vault|Empty|Session|Federat|Source|Kind)|Search_Backlog'`
  (vault 2.199s, server 11.679s). Covers availability/retrieval scope parity,
  literal matching, cancellation/database failures, empty/nonempty knowledge
  stores, widening, physical defaults, clear fallback and source/kind opt-outs.
- PASS: `go test -race -tags fts5 -count=1 ./internal/vault ./internal/server -run 'SessionProject_(HasSessions|SQLResolverAndLiteralQueries)|Search_(Project|VaultAvailability)'`
  (vault 7.044s, server 8.558s; no races reported).
- PASS: `go test -tags fts5 -count=1 ./...` with an empty temporary
  `XDG_CONFIG_HOME` (all packages; CLI 81.916s, server 65.110s, vault 75.404s).
  No test exclusions were used. The temporary-path and XDG fixture-isolation
  follow-ups recorded under Tasks 1 and 5 remain outside this slice.
- PASS: `make vet`, `git diff --check`, and formatting of all changed Go files.
- PASS: `make bench-quality BENCH_BRANCH=vault-project-task6-before` before code
  changes and `make bench-quality` afterward, then
  `make bench-compare BASE=vault-project-task6-before TARGET=feat-vault_project_name`.
  Reports are `bench-results/vault-project-task6-before.json` and
  `bench-results/feat-vault_project_name.json`, with identical dataset fingerprint
  `7d45338724b05181ebc92bd0b74eb7708bdd4fba27a8d6830b54a127f2b6ba2d`.
  All retrieval/context-reduction metrics were unchanged; vault R@1=0.933,
  R@5=0.967, NDCG@10=0.954, MRR=0.950 and negative FP=0/10. Existing FTS fallback
  warnings and the `tr_005_q3` miss remain. `benchstat` is unavailable, so no
  performance comparison is claimed. Task 10 retains the full-feature master
  comparison and 10,000-session measurements.
- `kk:review-code:isolated`: the code-reviewer sub-agent could not access its
  permitted reader tools and performed no review. The skill's independent
  external-reviewer fallback (PAL, Gemini 3.1 Pro) completed the review and
  reported zero issues across all severities. No unresolved findings or P0/P1
  findings to index.
- `kk:test` and `kk:document` completed. README, architecture and tool help agree
  with the implemented scope. No new conventions were indexed: the scope,
  metadata-only availability and failure contracts are already in the design.

No Task 6 requirement is deferred. The implementation follows Slice 6; the
failure fixture uses the existing lazy vault handle to trigger opening errors
without adding a production test seam or changing stored data.

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
- 2026-09-20: kk:review-design (standard) reviewed all three documents: SOUND, no findings. Ownership, explicit/default scope, migration, merge ordering, error handling, task dependencies and verification contracts are consistent; existing metadata/migration/CLI seams were verified. Task 1 implementation authorized by the user.
- No agreed implementation work is deferred. Accepted compatibility limits and any later deferred findings belong in [the implementation record](implementation.md#deferred-work-and-accepted-limits).
