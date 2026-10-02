# In-session search verification — 2026-10-02

This record maps the completed behavior to repeatable tests and the final
[performance protocol](performance.md#task-6-complete-protocol--2026-10-02).
Tasks 1–5's early gates remain historical evidence with their original scope;
Task 6 measures the completed hidden-target and suspension paths.

## Acceptance evidence

| Criterion | Implementation and verification |
| --- | --- |
| Every supported literal occurrence is reachable | `find_text.go` scans original field bytes without caps, preserving overlaps and duplicate provenance. `TestFindText`, `TestFindTextFields`, `TestFindTextCollapsed` and `TestViewerFindCollapsed` cover spaces, case, punctuation, full hidden bodies, summaries and repeated visible/hidden cycles. |
| Fuzzy selection opens the chosen content line | `find_fuzzy.go` and `find_picker.go`; `TestFindFuzzy`, `TestFindFuzzyOracle`, `TestFindPicker`, `TestFindPickerSnippet`, `TestAppFindPicker` and `TestViewerFindFuzzyCompletion` cover full-line alignment, all query lengths 1–256, NUL, Unicode, paging, original byte spans and precise ordinary/hidden acceptance. |
| Resize preserves the selected occurrence | `find_render.go` source spans and frame restoration; `TestFindRenderCoverage`, `TestFindRenderBrackets`, viewer resize/cancel regressions, `TestAppFindRawReturnPendingResizeKeepsVisibleSelection`, and new `TestFindLifecycle` verify selected identity and mapped location. `TestAppFindStatusAtOneRow` also checks the root row budget and counter/selection retention when copying or shrinking to one row. |
| Every reference update meets 100 ms | `TestFindLatency`: 86 classes × 100 samples in each build; maximum 29.135 ms default / 28.601 ms glamour. Includes Update, scheduling, matching/sorting/projection, application and View. Full tables and raw logs are linked from `performance.md`. |
| Claude, Codex and both render builds agree | Existing platform/decoder fixtures, `TestAppFindPlatforms`, `TestViewerFindCollapsed`, `TestAppFindPicker` and `TestFindLifecycle` cover both platforms. The TUI suites run under `fts5` and `fts5,glamour`; linkage checks retain the optional renderer boundary. |
| Editing cannot trigger actions; cancellation restores state | `TestAppFindRouting`, `TestAppFindRoutingPicker`, `TestViewerFindAsyncLatestPending`, viewer snapshot regressions and `TestAppFindActions` exercise printable shortcuts, Ctrl+G, Ctrl+C, pending submit, stale jobs, and exact/fuzzy cancellation. |
| Local search is read-only and in-process | Viewer/corpus helpers own no store. App tests assert zero search and metadata mutation counters, original archive bytes and copied Body identity. The input's external clipboard binding is disabled; terminal paste is covered. No decoder, schema, CLI, MCP, reindex or setup change was introduced. |

The lifecycle harness adds the full reference corpus to local and root return
scenarios. Its ordinary test runs the scenario assertions on both platforms;
the opt-in harness runs 100 measurements per operation under both build tags.
`TestFindStress` records 100,000 lines, a single MiB line, long graphemes and
within-line cancellation. `TestFindLifetimeStress` records twenty nested
child/tool/fuzzy/raw/back cycles and release of the viewer's search caches.

## Verification environment and commands

All runs use synthetic `CAPY_DB_KEY=test-key-for-development`,
`CAPY_VAULT_KEY=test-key`, `CGO_ENABLED=1`, `GOCACHE=/tmp/capy-go-build` and
canonical `TMPDIR=/private/tmp`. Repository-wide suites use an empty temporary
`XDG_CONFIG_HOME` and local TCP/Unix socket access. `GOFLAGS=-count=1` disables
test caching for the Makefile suites. No new test accesses the user's vault.

`make test` passed across the repository (CLI 207.172 s, vault 132.455 s).
`make test-race` passed (CLI 210.694 s, vault 446.995 s). Both included the new
ordinary lifecycle scenarios. These repository-wide runs preceded the later
test-helper retirement correction and one-row status fix; final focused reruns
cover the complete affected TUI package after both changes. Outcomes are recorded
in [Task 6 evidence](tasks.md#task-6-evidence--2026-10-02).

After both corrections, complete focused reruns passed:

| Command | Result |
| --- | --- |
| `go test -tags fts5 -count=1 ./internal/vault/tui/` | PASS, 20.754 s |
| `go test -tags fts5,glamour -count=1 ./internal/vault/tui/` | PASS, 22.215 s |
| `go test -race -tags fts5 -count=1 ./internal/vault/tui/` | PASS, 36.067 s |
| `go test -race -tags fts5,glamour -count=1 ./internal/vault/tui/` | PASS, 49.575 s |
| `make bench-quality BENCH_BRANCH=vault-find-task6` | PASS |
| `make bench-compare BASE=master TARGET=vault-find-task6` | Verified fixture digest; every quality/context metric unchanged |

The baseline is the existing matching `master.json` measured at detached HEAD
`397ecfd`; target is dirty `feat/session_search` at `9f89f15`. Optional benchstat
comparison was skipped because the tool is unavailable. Both six-repetition
conventional benchmark sets passed; full raw results and scope are in the
performance report.

Final `make vet` and `go vet -tags fts5,glamour ./internal/vault/tui/...` passed.
Both `go build -tags fts5` and `go build -tags fts5,glamour` binaries built. This
Go installation has no `go tool nm`; native `nm` confirmed that the default
binary excludes glamour symbols and the tagged binary includes them.

Build/test/CI requirements are inherited from [AGENTS.md](../../../../AGENTS.md)
and [.github/workflows/ci.yml](../../../../.github/workflows/ci.yml).
CLI/configuration additions: N/A — existing vault entry points launch this viewer.
Schema/data migrations: N/A — search state exists only in memory. Dependencies
and CI pipeline edits: N/A — existing versions and checks suffice.

## Terminal walkthrough

Built binaries were exercised through a 100×30 PTY against a temporary encrypted
vault imported from generated fixtures: one Claude main session with a Read body
and sidecar, and one Codex parent with an exec_command body and archived child.
No real session was used. The default Claude walkthrough verified `/needle`,
the 1/4 → 2/4 → 3/4 counter, hidden-body landing, raw inspection/return, fuzzy
selection, clear and draft cancellation. The glamour Codex walkthrough verified
the same literal/hidden path, fuzzy `hidden` acceptance, normal Markdown return
after clear/back, marker opening of the child, its independent 1/1 search and
return to the parent. ASCII brackets and the picker `>` remained visible.

The PTY does not automatically answer terminal background-color queries; the
glamour walkthrough supplied an OSC 11 response before sending action keys.
Initial terminal setup/normal rendering is outside the search timing protocol.
The observed controls, scopes and clear/back behavior agree with the app tests
and the updated README. Resize fidelity is asserted by the automated model
tests, including width and height changes during raw and child suspension.

## Reviews and reflection

[Isolated code review](.reviews/review-code-task6-2026-10-02.md) approved the final
harness and public docs after explicit command retirement was added to memory
checkpoints, and approved the later status fix. PAL returned no actionable
findings. The [full-feature spec review](.reviews/review-spec-task6-2026-10-02.md)
is CONFORMANT, with no outstanding findings or intentional deviations to index.

Spec review found one production layout defect: copying during accepted search
at a terminal height of one appended a second status row. All four new regression
cases failed before the fix (exact/fuzzy, copying at one row/shrinking after copy).
`Model.View` now gives the sole row to its submodel, retaining status in state so
it reappears after growth. The query/counter and selected source position survive.
This closes the agreed terminal-budget contract without changing search semantics.

The main measurement correction was distinguishing an idle search controller
from fully retired cosmetic timer commands. Joining Batch children outside
latency timing made retained-heap snapshots meaningful without adding timer waits
to the user path. This testing pattern was indexed as `kk:test-patterns`; there
is no new architecture decision or deferred feature requirement.

Optional editorial follow-up: `$kk:clarify-docs README.md docs/architecture.md`.
