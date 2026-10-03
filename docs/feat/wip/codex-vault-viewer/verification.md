# Codex vault viewer verification

## Task 1 — compatibility baseline (2026-10-03)

Task 1 adds test fixtures and compatibility gates only. Production code remains
at `0be03aaeaeb8429c480fd631de371007b56b7d38`; no decoder, scanner, renderer,
index, or viewer implementation changed during baseline capture. Tasks 2–6
remain pending, so this is not a claim that the viewer feature is implemented.

### Synthetic compatibility

Three independently authored fixtures cover a wrapped four-file update
(+23 / −14), two independent nested edits in one exec, and a legacy direct
call → event → result. Calls after edit events also pin the existing assistant
grouping. Nine checked-in goldens under `internal/vault/testdata/codex-edits/`
freeze the complete `ScanOutput` (metadata, rows, source lines, and `ToolNames`),
plain text, and Markdown. Viewer output is intentionally outside this gate.

`TestCodexParity_*` exercises deterministic serialization, changes to each reader,
identical-input output mismatches, true append versus rewritten input, additions,
removals, files vanishing between discovery/read, lost category coverage, rejection
of zero comparisons, baseline/manifest integrity, both roots, compression, and
revert variants. The initial synthetic capture and harness tests passed before
any production edit.

### Local corpus baseline

The machine-local baseline is `bench-results/codex-vault-viewer-parity.tsv`.
Its companion `.tsv.json` records original byte lengths and category membership,
bound to the baseline SHA-256. Both files are ignored by Git. Neither raw
recordings nor per-session paths are included in this document.

- Production revision: `0be03aaeaeb8429c480fd631de371007b56b7d38`.
- Baseline SHA-256: `c68edd3a7734c202c364d7e87a02298050fb2e64be7a50ca5337b050c1a08055`.
- Companion SHA-256: `d7178b74f3810e02f6d48c98626c0361b3feec2721f3404acf440bf9674abc4f`.
- Captured recordings: 482, including eligible main and child rollouts discovered
  under both Codex roots. Compressed: 0; revert variants: 0; vanished: 0.
- Capture duration: 67.26 seconds. Environment: Go 1.26.4, Linux/amd64.

Category counts are recordings, not events; one recording can cover several
categories.

| Category | Captured recordings | Unchanged recordings compared |
| --- | ---: | ---: |
| Completed paginated edits | 113 | 111 |
| Failed paginated edits | 8 | 8 |
| Declined paginated edits | 1 | 1 |
| Completed moves | 3 | 2 |
| Legacy direct edits | 13 | 13 |

The TSV reuses the existing parity baseline reader/writer and records uncompressed
input SHA-256 plus a digest of three independently named output SHA-256 values.
The companion permits exact prefix comparison for appends and enforces original
category coverage on unchanged inputs. Existing baselines are never refreshed
on mismatch. Missing/invalid companions, permission/decompression errors, zero
comparisons, and lost representative coverage fail visibly.

Capture and subsequent comparison use the same command, from the repository root:

```sh
GOCACHE=/tmp/capy-codex-vault-viewer-go-cache \
CAPY_DB_KEY=test-key-for-development CAPY_VAULT_KEY=test-key \
CAPY_CODEX_PARITY_BASELINE="$PWD/bench-results/codex-vault-viewer-parity.tsv" \
  go test -tags fts5 -count=1 -run '^TestCodexParityCanary$' -v ./internal/vault
```

The comparison passed in 67.69 seconds: **480 unchanged recordings compared,
zero output mismatches**, 2 appended, 0 otherwise changed, 3 new, 0 removed, and
0 vanished. All required categories retain unchanged representatives. The local
corpus grew to 485 recordings between capture and comparison; the excluded live
inputs do not count toward byte parity or category coverage. Preserve these same
baseline files for Tasks 2–6; do not regenerate them after production changes.

### Validation environment

The initial focused tests and golden capture passed. Subsequent default-cache
commands encountered a read-only Go cache; runs now use the task-specific `/tmp`
cache above. The sandbox also prevents existing Unix/TCP socket tests from
running. An unrestricted `go test -tags fts5 -count=1 ./...` passed every package
except `cmd/capy`, including the full vault and TUI suites (268.54 seconds and
23.12 seconds). These setup failures are not successful test results.

The CLI failures came from personal import policy: fixtures of 291/741 bytes were
excluded by a 196608-byte minimum. Rerunning the entire CLI package with an empty
`XDG_CONFIG_HOME` passed in 214.26 seconds. Together with the unrestricted full
run, every package passed; the original full invocation itself remained a failure.

```sh
capy_test_config_dir=$(mktemp -d /tmp/capy-vault-viewer-config.XXXXXX)
XDG_CONFIG_HOME="$capy_test_config_dir" \
GOCACHE=/tmp/capy-codex-vault-viewer-go-cache \
CAPY_DB_KEY=test-key-for-development CAPY_VAULT_KEY=test-key \
  go test -tags fts5 -count=1 ./cmd/capy
```

The focused synthetic suite also passed with the race detector, including after
the review fixes to failure diagnostics:

```sh
GOCACHE=/tmp/capy-codex-vault-viewer-go-cache \
CAPY_DB_KEY=test-key-for-development CAPY_VAULT_KEY=test-key \
  go test -race -tags fts5 -count=1 \
    -run '^(TestCodexParity_|TestCodexConsumers_EditCompatibility)' ./internal/vault
```

Performance, retrieval quality, both viewer builds, and feature/spec verification
remain Task 6 work. Task 1 changes no search, indexing, chunking, executor, or
runtime code, so it does not require a retrieval benchmark rerun.

### Review and scope

[`kk:review-code:isolated`](.reviews/task-1-code-review-2026-10-03.md) completed
with an independent code reviewer and Gemini 3.1 Pro. All findings were fixed:
rollout I/O errors now omit private paths while retaining the underlying error
identity, vanished-file assertions print only counts, and baseline I/O/JSON
errors include operation context. The independent reviewer approved the privacy
fix; focused race tests passed after the final edits. No findings remain deferred
within Task 1, and no P0/P1 findings required knowledge-base indexing.

The companion manifest is the only implementation detail added to the plan: the
shared two-digest TSV alone cannot distinguish true appends or retain original
category coverage. The captured TSV and its companion remain unchanged after
review. No new project convention needs indexing; this procedure is recorded here
and in the implementation plan.

### Pre-existing test-isolation follow-up

Some CLI vault tests inherit personal import settings because they do not all
isolate `XDG_CONFIG_HOME` (for example, `setupCodexVaultEnv` and standalone restore
fixtures). This is outside the test-only compatibility task and is not changed
here. Run them with an empty `XDG_CONFIG_HOME` for reproducible validation; a
separate follow-up should isolate config consistently in those fixture helpers
and pin a regression with a non-default personal minimum-size policy.

## Task 2 — grouped completed paginated updates (2026-10-03)

Task 2 adds the viewer path for recorded paginated updates. The neutral four-file
fixture opens one `4 files changed (+23 −14)` marker, with sorted file sections,
archived-CWD-relative paths and the `File changes · completed` heading. Normal
and find rendering share the optional heading override. The existing tool detail
retains complete bodies, copy selection, marker return, resize and local search
ownership. No new role, frame type, dependency, schema or index version is added.

`TestCodexFileChangePaginated_*` covers status evidence, unavailable/partial data,
metadata, physical anchors across malformed/oversized neighbors, preservation of
the open assistant slot, and bounded body-free warnings. `TestCodexFileChangeDiff`
covers several hunks, exact ranges/counts, file headers, CRLF, no-final-newline
annotations, empty ranges and malformed content. `TestViewerCodexFileChanges`
opens the four-file group and finds all 23 additions while reusing one corpus;
`TestTranscriptHeadingOverride` checks normal/find headings, single-row headers,
Markdown bypass and omitted empty JSON fields in both builds.

### Compatibility evidence

The working tree is based on `8afa3de4d7e1f9f2869989b7a1a021cc764510cd`.
The original Task 1 TSV and companion remain unchanged, with the same SHA-256
values recorded above. Frozen synthetic scanner/text/Markdown goldens passed.
The same opt-in corpus command used by Task 1 passed in 53.14 seconds:

- 497 recordings discovered; 478 unchanged inputs compared, **zero mismatches**.
- 4 appended, 0 otherwise changed, 15 new, 0 removed and 0 vanished.
- Unchanged category coverage: 111 completed paginated, 8 failed, 1 declined,
  2 real-move and 13 legacy-direct recordings.
- No compressed recordings or revert variants were present.

This is scanner/export byte parity, not a claim that pending operation and legacy
viewer behavior is implemented. Raw recordings and per-session paths remain local.

### Checks and scope

All commands use `GOCACHE=/tmp/capy-codex-vault-viewer-go-cache`,
`CAPY_DB_KEY=test-key-for-development` and `CAPY_VAULT_KEY=test-key`.

- Focused decoder/consumer/model tests passed.
- `go test -tags fts5 -count=1 ./...` passed every package, including the full
  vault (229.23 seconds), default TUI (22.78 seconds), and CLI (227.11 seconds)
  suites. This run used an empty temporary `XDG_CONFIG_HOME` and unrestricted
  execution for existing socket tests, as described in Task 1.
- Focused race tests passed: `go test -race -tags fts5 -count=1 -run
  '^(TestCodexFileChange|TestCodexConsumers|TestCodexDecoder|TestViewerCodexFileChanges|TestTranscriptHeadingOverride|TestViewerFind)'
  ./internal/vault ./internal/vault/tui`.
- The full glamour TUI suite passed: `go test -tags fts5,glamour -count=1
  ./internal/vault/tui/...` (24.34 seconds).
- `make bench-quality BENCH_BRANCH=codex-vault-viewer-task2` passed. The explicit
  report name preserves earlier results. `make bench-compare BASE=vault-find-task2
  TARGET=codex-vault-viewer-task2` found identical quality/context-reduction metrics
  against the existing `a415bda` baseline, using the same dataset SHA-256
  `7d45338724b05181ebc92bd0b74eb7708bdd4fba27a8d6830b54a127f2b6ba2d`.
  Its performance comparison was skipped because `benchstat` is not installed;
  this does not establish parse/render latency. Task 6 retains that measurement.

Task 3 still owns add/delete/move conversion and duplicate/conflicting identities.
Those operations currently retain labeled unavailable details; ordinary failed,
declined and unconfirmed groups show diagnostics without candidate hunks. Task 4
owns legacy/direct reconciliation; Task 5 owns long executable input disclosure.
Task 5 should reuse the heading override introduced here. Public feature guidance
and final performance/spec validation remain Task 6. Issue #121 is not complete.

### Review completion

The [isolated code review](.reviews/task-2-code-review-2026-10-03.md) is complete.
The independent reviewer approved without findings. The external review's blank
context-line concern was checked against the design, the other parser's distinct
input grammar and a read-only aggregate corpus scan. Strict prefix validation is
retained; the added prefixed/unprefixed regression cases passed under `-race`.
Two unmeasured allocation suggestions are recorded for Task 6's performance work.
There are no outstanding required Task 2 fixes or new conventions to index.

## Task 3 — paginated operations, states and identity (2026-10-03)

Task 3 completes paginated add/delete/move conversion and exact event identity.
Adds and deletes use archived content, including empty files, CRLF and missing
final newlines. Absent/null destinations mean no move; nonempty strings retain
both paths, while empty/wrong-type destinations remain unavailable. Completed
groups keep valid sibling diffs without presenting partial totals as complete.
Failed, declined and unconfirmed groups show reported paths and diagnostics,
with no applied hunks or aggregate line counts.

Nonempty IDs reconcile once per transcript at the first physical event anchor.
Equality compares state, availability, sorted original paths, operations,
destinations, verbatim content/diff bytes, stdout/stderr and diagnostic
code/field/value. Rendered text/counts, timestamps and diagnostic locations are
excluded. Diagnostic JSON is normalized for member order without rounding
numbers. Conflicts become one unconfirmed detail with source-line references;
empty IDs remain independent. Raw records are untouched.

### Synthetic coverage and corpus discoveries

`TestCodexFileChangeDiff_*` covers archived adds/deletes, empty content versus
missing/null content, destination shapes, exact counts and newline annotations.
`TestCodexFileChangePaginated_AllOperationsAndStates` checks all operation headers
and conservative state presentation. `TestCodexFileChangeIdentity_*` checks each
equality field, output-only/diagnostic-only conflicts, map order, optional nulls,
distinct/empty IDs, repeated conflicts, location-independent diagnostics and
physical anchors across malformed/oversized neighbors. Warnings are bounded and
do not contain patch bodies, IDs or paths; ordinary failure/decline emits none.

Read-only inspection exposed six pure moves with explicitly empty diff strings.
A later live recording also contained an explicit empty no-op update. Both now
produce valid zero-count diffs; missing/null and whitespace-only diffs remain
unavailable. This refines the plan's empty-versus-unavailable distinction without
relaxing numbered-hunk validation. The initial synthetic run also caught the
shared JSON string helper's acceptance of `null` as an empty string; the new
converter uses a local presence-aware guard, preserving older consumer behavior.

The new opt-in `TestCodexFileChangeCanary` reads the local corpus and independently
checks wire states, canonical ID counts and first anchors against viewer details.
Its exact content/diagnostic equality coverage remains synthetic. Final default
run: **121 recordings with paginated edits; 926 completed, 9 failed, 1 declined;
782 added, 47 deleted and 998 updated file records, including 12 moves; zero
unavailable completed-file diffs** (8.85 seconds). Counts are observations of a
live corpus, separate from the parity run below. Raw paths/content stay local.

```sh
GOCACHE=/tmp/capy-codex-vault-viewer-go-cache \
CAPY_DB_KEY=test-key-for-development CAPY_VAULT_KEY=test-key \
CAPY_CODEX_CHANGES_CANARY=1 \
  go test -tags fts5 -count=1 -run '^TestCodexFileChangeCanary$' -v ./internal/vault
```

Early probe failures are not counted as passes. A faulty move assertion was
replaced with checks over normalized file records, and private-body assertions
were replaced with boolean checks. The isolated review then identified a canary
assumption incompatible with deduplication; the final probe groups nonempty IDs
and accepts unconfirmed conflicts. The final implementation plus corpus probe
also passed under `-race` (81.37 seconds, including the focused Codex suite).

### Scanner/export compatibility

Working-tree base: `91c966d203bfc3a5a62e4876aaff7e2426a90744`.
The original Task 1 baseline and companion retain their recorded SHA-256 values:

- TSV: `c68edd3a7734c202c364d7e87a02298050fb2e64be7a50ca5337b050c1a08055`.
- Companion: `d7178b74f3810e02f6d48c98626c0361b3feec2721f3404acf440bf9674abc4f`.

The same `CAPY_CODEX_PARITY_BASELINE` command documented under Task 1 passed
after the final empty-diff change (50.48 seconds): **478 unchanged recordings,
zero scanner/text/Markdown output mismatches**. It discovered 523 recordings:
4 appended, 0 otherwise changed, 41 new, 0 removed, 0 vanished, 0 compressed and
0 revert variants. Unchanged category coverage remains 111 completed paginated,
8 failed, 1 declined, 2 real-move and 13 legacy-direct recordings. Frozen
synthetic scanner/export goldens also pass. Legacy viewer changes remain Task 4.

### Checks and review

All Go commands use `-tags fts5`, the task-specific `/tmp` Go cache and synthetic
knowledge/vault keys; glamour commands add the `glamour` build tag.

- `go test -tags fts5 -count=1 ./...` passed every package, using an empty
  temporary `XDG_CONFIG_HOME` and unrestricted execution for existing socket tests.
  Vault: 239.40 seconds; default TUI: 21.89 seconds; CLI: 233.01 seconds.
- `go test -tags fts5,glamour -count=1 ./internal/vault/tui/...` passed
  (24.23 seconds).
- After the final empty-update and canary fixes, the entire affected subtree
  passed again: `go test -tags fts5 -count=1 ./internal/vault/...`
  (vault 218.09 seconds, TUI 21.77 seconds).
- Focused race checks passed in both builds. Final glamour invocation:
  `go test -race -tags fts5,glamour -count=1 -run
  '^(TestCodexFileChange|TestCodexConsumers|TestCodexDecoder|TestViewerCodexFileChanges|TestTranscriptHeadingOverride|TestViewerFind)'
  ./internal/vault ./internal/vault/tui` (1.63 and 1.82 seconds).
- `make bench-quality BENCH_BRANCH=codex-vault-viewer-task3` passed, preserving
  previous reports. `make bench-compare BASE=codex-vault-viewer-task2
  TARGET=codex-vault-viewer-task3` found identical retrieval quality and context
  reduction against Task 2's `8afa3de` report on dataset SHA-256
  `7d45338724b05181ebc92bd0b74eb7708bdd4fba27a8d6830b54a127f2b6ba2d`.
  Performance comparison was explicitly skipped because `benchstat` is absent;
  final parse/render and find-latency measurements remain Task 6.

The [isolated review](.reviews/task-3-code-review-2026-10-03.md) is complete:
the independent reviewer approved the final fixes; the external reviewer returned
no actionable findings. No required Task 3 fix or new convention remains to index.
Task 4 owns legacy/direct reconciliation and mixed-family wire tests, Task 5 owns
long executable inputs, and Task 6 owns final feature documentation/verification.
