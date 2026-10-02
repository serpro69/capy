# Tasks: In-session vault search

> Design: [design.md](design.md)
> Implementation: [implementation.md](implementation.md)
> Issue: [#101](https://github.com/serpro69/capy/issues/101)
> Status: in-progress
> Created: 2026-09-20
> Design review: [Re-review passed](.reviews/review-design-2026-10-01.md); Task 1 feasibility gate passed in both builds
> Reconciliation: [Consolidated findings](.reviews/consolidated-findings-2026-09-20.md)
> Not Doing: raw JSONL search, recursive child/subagent search, regex/multiline queries, full fzf syntax, persistent history, global FTS/MCP/CLI changes, rich Markdown during search, decoder-policy changes, general lazy rendering

## Task 1: Exact find with mandatory early feasibility evidence

- **Status:** done
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** —
- **Strategy:** Risk-First — prove source positions and the measured exact path before dependent work
- **Docs:** [Implementation: Task 1](implementation.md#task-1--exact-find-in-the-main-transcript), [Performance protocol](implementation.md#performance-verification)

### Subtasks

- [x] 1.1 Add find_text.go full corpus identities and overlapping literal matching, including hidden Body computational coverage; temporarily expose only supported visible fields in the viewer → verify: TestFindText covers duplicates, case, spaces, punctuation, overlaps, and full hidden text without FTS bounds.
- [x] 1.2 Add find_render.go tracked plain wrapping, escaping and source spans; reserve two cells for generated selection brackets and promote pinned x/ansi only if imported → verify: TestFindRender maps Unicode/wrapped hits, and stripped-ANSI output brackets the chosen occurrence on same-row and wrapped cases.
- [x] 1.3 Add viewer_find.go slash preview/commit/cancel, counter, n/N and Esc clear; wire viewer.go → verify: TestViewerFind and TestViewerFindResize pass under both tags.
- [x] 1.4 Add latest-pending cancellable commands and root epochs → verify: TestViewerFindAsync covers stale results, cancellation, pending submit and same-session reopen.
- [x] 1.5 Route local input before app.go actions and bound layout → verify: TestAppFindRouting prevents unintended actions and store searches.
- [x] 1.6 Create find_bench_test.go's deterministic reference corpus and opt-in harness; record full-corpus computational measurements and implemented exact UI operations in performance.md under both tags → verify: all location checks and every recorded implemented operation pass the 100 ms gate before Task 2 starts.
- [x] 1.7 Run both focused suites and label the temporary hidden-navigation gap as Task 3 work → verify: normal/global search regressions pass; early evidence explicitly identifies unimplemented operation classes.

### Task 1 evidence — 2026-10-01

- [Performance report](performance.md): 100 samples per operation in both builds,
  full hidden-field computational scans, Unicode/long-line stress and retained
  heap measurements. Final maximum 27.052 ms against the 100 ms gate.
- [Isolated code review](.reviews/review-code-task1-2026-10-01.md): approved after
  six reproduced restoration/mapping findings were fixed. PAL's external reviewer
  returned 503; the independent code-reviewer completed the review and follow-ups.
- Both complete TUI suites passed with -count=1 under fts5 and fts5,glamour;
  final race runs passed under both tags (2.475 s / 2.752 s). They include existing
  global-search, viewer, collapse, marker, raw, child and metadata regressions.
- make test passed with synthetic keys and isolated XDG_CONFIG_HOME; make vet
  and glamour TUI vet passed. Both binaries built and native nm confirmed
  default excludes glamour while the tagged build includes it. This Go
  installation lacks go tool nm, so the system symbol tool was used.
- BenchmarkFind ran six repetitions under both tags. make bench-quality and
  qualstat comparison against an independently archived f99d758 baseline showed
  no retrieval-quality or context-reduction change.
- No schema, decoder, persisted state, CLI flags, setup artifacts or CI pipeline
  changes. x/ansi v0.11.6 was promoted to a direct requirement without a version
  or go.sum change. All new behavior is confined to the TUI.

The query editor also consumes Ctrl+G before the root project editor and disables
the widget's external clipboard utility binding. Terminal bracketed paste remains
supported and is covered by the Unicode 256-code-point limit regression.

Tasks 2–3 add manual detail scopes, parent-query suspension and hidden-result
navigation. Slash searches
ordinary bodies, launch labels and full summaries in each opened transcript,
including full collapsed tool bodies and manually opened details.
Root/raw suspension remains
Task 4; fuzzy and final public documentation remain Tasks 5/6.

## Task 2: Local frames and manual nested return

- **Status:** done
- **Depends on:** Task 1, including its performance gate
- **Size:** M
- **Can run in parallel with:** —
- **Strategy:** Risk-First — settle local state ownership before search-selected details
- **Scope:** Local target migration and required consumer compatibility; root suspension integration is Task 4
- **Docs:** [Implementation: Task 2](implementation.md#task-2--local-frames-and-manual-nested-return)

### Subtasks

- [x] 2.1 Add the complete frame contract in viewer_targets.go, including provenance, position, presentation and committed search state → verify: TestViewerTargetsClone proves mutable child state does not alias a parent.
- [x] 2.2 Migrate viewer.go target readers/writers and viewer_find.go scope ownership; remove independently mutable legacy flags/return state → verify: TestViewerTargetsNested and existing viewer/collapse/marker tests cover manual main/sidecar/tool return, resize and global-search entry.
- [x] 2.3 Adapt app.go/raw.go consumers to derived frame accessors in the same commit → verify: TestViewerTargetsConsumers confirms correct platform, copy source, owning session and sidecar raw bytes, with no shadow-state writes.
- [x] 2.4 Start each manual detail's own corpus and restore its parent's committed state; migrate private-field test assertions without weakening behavior → verify: TestViewerFindScopes checks isolated counts and selected-occurrence restoration.

### Task 2 evidence — 2026-10-01

- [Isolated code review](.reviews/review-code-task2-2026-10-01.md): APPROVE,
  no P0–P3 findings. The independent code-reviewer performed static review;
  PAL returned no issues and no additional actionable signal.
- Complete TUI suites passed with `-count=1` under `fts5` and `fts5,glamour`.
  Complete TUI race runs passed under both tags (2.738 s / 3.034 s), including
  `TestViewerTargetsClone`, `TestViewerTargetsNested`,
  `TestViewerTargetsConsumers`, `TestViewerFindScopes`, stale-result retirement
  and pending-clear restoration. Existing viewer, collapse, marker, raw, child,
  global-search and metadata tests retain their behavior assertions.
- `make vet` and `go vet -tags fts5,glamour ./internal/vault/tui/...` passed.
  Tests use synthetic encryption keys, `GOCACHE=/tmp/capy-go-build` and, for
  repository-wide checks, an empty `XDG_CONFIG_HOME=/tmp/capy-task2-config`.
- `make test` passed across the repository with local socket access (CLI
  218.261 s, server 108.253 s, vault 219.961 s). The restricted run was stopped
  after it made no package-output progress; it is not counted as a passing run.
- `make bench-quality BENCH_BRANCH=vault-find-task2` passed. Qualstat verified
  the fixture digest and found every retrieval/context-reduction metric
  unchanged against `vault-find-task1.json` (f99d758). The current measurement
  is the dirty `master` worktree at a415bda, saved as `vault-find-task2.json`;
  no existing report was overwritten. This is quality evidence, not a new
  claim about unimplemented UI latency paths.
- No dependency, schema, decoder, setup-artifact, CLI flag or CI changes. No
  additional conventions to index. Public documentation and full performance
  verification remain Task 6.

## Task 3: Exact find opens collapsed results at the match

- **Status:** done
- **Depends on:** Task 2
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation: Task 3](implementation.md#task-3--exact-matches-inside-collapsed-tool-details)

### Subtasks

- [x] 3.1 Enable full collapsed-body result navigation using Task 1's corpus; remove the temporary eligibility predicate → verify: TestFindTextCollapsed reaches excluded/large outputs, summaries and diffs without duplicate indexing.
- [x] 3.2 Use Task 2's complete frames to open mapped summary/body passages, including within an opened sidecar → verify: TestViewerFindCollapsed and TestViewerFindDiff land on the precise span.
- [x] 3.3 Preserve the originating query/corpus while replacing temporary search targets → verify: repeated visible/hidden/hidden/visible navigation wraps without growing the stack.
- [x] 3.4 Implement cancel/clear/back distinctions and replace invalidated comments → verify: TestViewerFindCollapsedCancelClear never revives a cleared query and both rendering suites pass.

### Task 3 evidence — 2026-10-02

- [Isolated code review](.reviews/review-code-task3-2026-10-02.md): APPROVE after
  two reproduced async navigation findings were fixed and re-reviewed. Pending
  selection now applies with its target, and survives repeated keys and resize.
- Complete TUI suites passed under `fts5` and `fts5,glamour` with `-count=1`.
  Final race suites passed under both tags (2.653 s / 3.176 s). Regressions cover
  decoded Read/Bash/exec_command outputs beyond FTS bounds, reconstructed diffs,
  long summaries, exact locations, bounded repeated cycles, sidecar isolation,
  copy-source identity, clear/back and cancellation before navigation completes.
- `make vet` and the final TUI vet checks under both tags passed. Synthetic
  encryption keys and `GOCACHE=/tmp/capy-go-build` were used throughout.
- `make bench-quality BENCH_BRANCH=vault-find-task3` passed. Qualstat verified
  fixture digest `7d45338724b05181ebc92bd0b74eb7708bdd4fba27a8d6830b54a127f2b6ba2d`
  and every retrieval/context-reduction metric matched the existing
  `master.json` baseline (detached HEAD at `397ecfd`). Current measurement:
  dirty `feat/session_search` at `81a4fcd`, saved as `vault-find-task3.json`.
- `make test` passed across the repository with local socket access,
  `XDG_CONFIG_HOME=/tmp/capy-task3-config` and `TMPDIR=/private/tmp` (CLI
  209.647 s, server 70.307 s, vault 131.206 s). The initial noncanonical-TMPDIR
  run failed unrelated path assertions; the follow-up below records the cause.
- [Exact latency rerun](performance.md#task-3-exact-path-rerun--2026-10-02):
  100 samples per operation in both builds; final maximum 11.569 ms. Previous-hit
  wrap now opens a collapsed tool and is asserted by the harness. This does not
  certify every hidden-target/suspension/fuzzy operation scheduled for Task 6.
- No dependency, schema, parser, CLI/config, setup artifact or CI changes.
  Public documentation remains Task 6.

## Task 4: Preserve search through root suspension and actions

- **Status:** pending
- **Depends on:** Task 3
- **Size:** M
- **Can run in parallel with:** —
- **Scope:** Existing frame contract plus root lifecycle/actions; no further frame-model migration
- **Docs:** [Implementation: Task 4](implementation.md#task-4--preserve-search-through-root-suspension-and-actions)

### Subtasks

- [ ] 4.1 Integrate child-session push/pop, cancellation, fresh epochs and stale completion retirement in app.go/viewer_find.go → verify: TestAppFindChildScopes covers nested, missing/failed children and late results.
- [ ] 4.2 Preserve search around raw inspection using Task 2's provenance accessor → verify: TestAppFindRawReturn covers sidecar-owned tools, children, resize and pending work.
- [ ] 4.3 Preserve source position through copy/status and rename; retain q/Esc precedence → verify: TestAppFindActions checks original Body copying, metadata-only updates and action keys outside input.
- [ ] 4.4 Recheck frame lifetime and cross-mode isolation → verify: both TUI suites/race tests pass without query resurrection or stack growth.

## Task 5: Fuzzy line picker with safe matching and a latency gate

- **Status:** pending
- **Depends on:** Task 4
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation: Task 5](implementation.md#task-5--fuzzy-line-selection), [Matcher contract](design.md#fuzzy-matcher-contract)

### Subtasks

- [ ] 5.1 Add find_fuzzy.go's local whole-line subsequence matcher with ordinary NUL handling, original byte spans, bounded additive scoring and in-line cancellation; do not import sahilm/fuzzy → verify: TestFindFuzzy covers NUL before/after/across hits, every length 1–256, 39–43/64/128/256 boundary cases, Unicode, oracle agreement, strictly increasing spans, correspondence/completeness, score bounds and ties.
- [ ] 5.2 Add find_picker.go scrolling/paging, role/location metadata, full-line snippets and ASCII > selection prefix → verify: TestFindPicker keeps selected results reachable and has a concrete ANSI-free indicator.
- [ ] 5.3 Wire Ctrl+F accept/cancel into viewer_find.go and shared frame navigation → verify: TestAppFindPicker opens ordinary/hidden/nested hits and restores exact state on Esc.
- [ ] 5.4 Add key fixtures and dynamic help, including accepted fuzzy span brackets and n/N marker behavior → verify: routing/no-color tests pass; go.mod/go.sum have no new fuzzy import/version change.
- [ ] 5.5 Test NUL/long-query worker completion, replacement and mid-line cancellation, then extend the reference harness to integrated fuzzy operations → verify: TestViewerFindFuzzyCompletion retires every job and both builds meet 100 ms before final verification.

## Task 6: Complete verification, performance evidence, and documentation

- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3, Task 4, Task 5, including both early gates
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation: Task 6](implementation.md#task-6--final-verification-and-documentation), [Performance protocol](implementation.md#performance-verification)

### Subtasks

- [ ] 6.1 Extend the existing latency harness to all hidden-target and suspension paths; record both builds and stress cases in performance.md → verify: all recorded reference operations meet 100 ms, all correctness invariants pass, and comparisons with Tasks 1/5 are explicit.
- [ ] 6.2 Run $kk:test for full suite, race tests, both TUI tags and relevant vet/linkage checks → verify: record commands/outcomes with synthetic fixtures.
- [ ] 6.3 Run make bench-quality and compare with an explicit baseline → verify: no unexplained retrieval regression and correct detached report naming.
- [ ] 6.4 Run $kk:document; update README.md and docs/architecture.md controls, scope, plain presentation and return semantics → verify: manual Claude/Codex walkthrough agrees with app tests.
- [ ] 6.5 Run $kk:review-code with Go language input → verify: findings resolved or durably recorded with concrete next steps.
- [ ] 6.6 Run $kk:review-spec across all feature documents → verify: all acceptance criteria have implementation evidence; no early probe is mislabeled as feature verification.

## Dependency Graph

~~~text
Task 1 (exact feasibility gate)
  → Task 2 (complete local frames)
  → Task 3 (hidden-body search)
  → Task 4 (root suspension/actions)
  → Task 5 (safe fuzzy + feasibility gate)
  → Task 6 (complete verification; depends on all preceding tasks)
~~~

## Evidence and remaining work

The three supplied review rounds consolidate to six unique claims. Each was checked against the current documents/code; both upstream matcher failures were independently reproduced. [The reconciliation](.reviews/consolidated-findings-2026-09-20.md) records qualified verdicts, fixes and standalone prototype evidence.

Tasks 1–3 and the exact feasibility gate are complete. The next task is Task 4.
Tasks 4–6 remain pending; the measured exact path does not certify their
interactions. The original review
files remain historical evidence; their repeated P1 findings are tracked once in
the reconciliation.

Pre-existing test-environment follow-up: some CLI import/merge fixtures inherit
the user's vault.min_session_bytes policy (192 KiB on this machine), excluding
their tiny archives. The full suite passed with XDG_CONFIG_HOME pointed at an
empty temporary directory. This TUI task leaves unrelated CLI fixtures unchanged;
isolate XDG_CONFIG_HOME in setupCodexVaultEnv and the remaining custom
import/restore/merge fixtures in a separate test-hermeticity fix. Local TCP/Unix
listener tests also require socket access beyond the restricted sandbox; the
successful full run used that access. No production failure was hidden by
changing assertions or skipping tests.

Task 3 also exposed pre-existing macOS temporary-path assumptions in
`cmd/capy/key_resolution_mcp_test.go` (`TestMCPProjectCredentials`) and
`internal/vault/restore_test.go` (`TestRestoreSession_OverwritePolicy` and
`TestRestoreSession_SymlinkRootResolved`). They compare unresolved `/var/...`
fixture paths with canonical `/private/var/...` production paths. The initial
full run failed those assertions; rerun uses `TMPDIR=/private/tmp`. This task
leaves those unrelated fixtures unchanged. Follow-up: canonicalize expected
temporary paths with `filepath.EvalSymlinks` and verify both a symlinked and a
canonical TMPDIR, alongside the XDG_CONFIG_HOME isolation fix above.
