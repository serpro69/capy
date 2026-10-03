# Codex vault viewer verification

All six tasks are complete. [Task 6](#task-6--final-verification-and-documentation-2026-10-03)
records final acceptance; earlier sections retain their historical task boundaries.

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

## Task 4 — legacy events and direct results (2026-10-03)

Legacy `patch_apply_end` records now use the shared file-change converter and
canonical event identity. Presence-aware success/status normalization implements
all rows of the design table, including absent status, null, malformed and
contradictory evidence. Both event families work regardless of declared history
mode. Legacy events retain their physical anchor without closing the assistant
call-attachment window.

Only a unique, nonempty direct `apply_patch` call/result ID associates with its
canonical event. Structured events supersede input-derived fallback diffs even
when failed, unconfirmed or unavailable. Duplicate call IDs, and conservatively
repeated result IDs, cannot prove a successful fallback. Explicit result exit
codes that contradict the event make the event unconfirmed; absence of positive
result evidence alone does not. The existing event-less successful direct patch
fallback remains covered by the original decoder/consumer tests.

The decoder preserves `ReportedSuccess`, optional `ExitCode` and `FileChangeID`
as transient model facts. The viewer builds one event-state map and uses those
facts to collapse a nonempty associated successful output into
`apply_patch · output`, with the `Tool result` heading and complete result body.
There is one grouped edit card; the duplicate input-derived diff is removed.
Other result bodies retain their existing inline/size-based policy. Scanner and
exports ignore the new evidence, retaining their existing summaries and bodies.

### Synthetic and viewer coverage

`TestCodexFileChangeLegacy_*` covers the status matrix, mixed-family duplicate
and conflicting records in both orders, warning privacy, assistant grouping and
physical anchors across malformed/oversized neighbors.
`TestCodexFileChangeDirectResult_*` covers independent wrapper IDs, unique direct
association, duplicate calls/results, absent/unusable event identity, unavailable
changes, explicit exit contradictions, missing exit codes, unstructured
success-looking text, event-less fallback and results preceding their events.
`TestViewerCodexDirectPatchOutput` opens the grouped diff and compact result,
checks absence of inline boilerplate and fallback content, and exercises full-body
copy selection, parent return, hidden-body find, shared corpus ownership and
resize. It passes in both build variants.

### Corpus evidence

Working-tree base: `6877641057de68729e5d7d81604c7f7e2a40d939`.
The Task 1 baseline files remain unchanged:

- TSV SHA-256: `c68edd3a7734c202c364d7e87a02298050fb2e64be7a50ca5337b050c1a08055`.
- Companion SHA-256: `d7178b74f3810e02f6d48c98626c0361b3feec2721f3404acf440bf9674abc4f`.

The extended read-only `TestCodexFileChangeCanary` passed (10.23 seconds):
153 recordings with edits; 987 completed, 9 failed and 1 declined canonical
operations; 855 adds, 48 deletes, 1,042 updates and 12 moves; zero unavailable
completed-file diffs. **All 21 legacy events had exact direct associations and
call→event→output order**, with one grouped edit and compact, complete output.
Only aggregate evidence is retained; raw recordings and paths stay machine-local.

`TestCodexParityCanary` passed in 59.25 seconds: **478 unchanged recordings,
zero scanner/text/Markdown mismatches**. Of 555 discovered recordings, 4 were
appended, 73 new, 0 otherwise changed, 0 removed and 0 vanished; none were
compressed or revert variants. Unchanged category coverage: 111 completed
paginated, 8 failed, 1 declined, 2 real-move and all 13 legacy-direct recordings.
Frozen synthetic scanner/text/Markdown goldens also pass.

Both corpus tests used the original absolute `CAPY_CODEX_PARITY_BASELINE` path
and `CAPY_CODEX_CHANGES_CANARY=1`, with the Task 1 cache and synthetic keys:

```sh
go test -tags fts5 -count=1 \
  -run '^(TestCodexFileChangeCanary|TestCodexParityCanary)$' -v ./internal/vault
```

### Checks

All Go checks use `GOCACHE=/tmp/capy-codex-vault-viewer-go-cache`,
`CAPY_DB_KEY=test-key-for-development` and `CAPY_VAULT_KEY=test-key`.

- Focused Codex decoder, consumer, file-change and TUI checks passed.
- `go test -tags fts5 -count=1 ./...` passed every package, using an empty
  temporary `XDG_CONFIG_HOME` and unrestricted execution for existing socket
  tests. Vault: 243.22 seconds; default TUI: 22.50 seconds; CLI: 232.84 seconds.
- Focused race checks passed with `-tags fts5` over both vault and TUI packages,
  including existing find regressions (1.60 and 1.71 seconds).
- The full glamour TUI suite passed under the race detector:
  `go test -race -tags fts5,glamour -count=1 ./internal/vault/tui/...`
  (57.22 seconds).
- `make bench-quality BENCH_BRANCH=codex-vault-viewer-task4` passed.
  `make bench-compare BASE=codex-vault-viewer-task3 TARGET=codex-vault-viewer-task4`
  found identical retrieval quality and context reduction on dataset SHA-256
  `7d45338724b05181ebc92bd0b74eb7708bdd4fba27a8d6830b54a127f2b6ba2d`.
  Performance comparison was skipped because `benchstat` is absent; final
  parse/render and find-latency measurements remain Task 6.

Task 5 still owns long executable inputs; Task 6 owns public feature documentation
and final feature-wide verification. Issue #121 is not complete at this point.

### Review completion

The [isolated review](.reviews/task-4-code-review-2026-10-03.md) is complete.
The independent code reviewer approved without findings; Gemini 3.1 Pro returned
no actionable findings. Author review changed one corpus assertion to avoid
printing private diff content on failure. No required Task 4 fixes remain and no
new project convention or systemic P0/P1 finding requires indexing.
The canary passed again after that assertion change (10.32 seconds): 155 edit
recordings, 993 completed/9 failed/1 declined operations, 858 adds/48 deletes/1,054
updates, 12 moves, zero unavailable completed diffs, and the same 21 exact legacy
direct associations. Growth since the earlier run is reported separately from
the unchanged-input parity comparison.

## Task 5 — compact executable inputs and source landing (2026-10-03)

Codex custom `exec` calls now supply verbatim `ToolCall.CodeText`; their shared
`Input`, `Name`, and `Summary` retain the prior representation. The viewer uses
the existing strict thresholds (over 20 lines or over 2,000 bytes) to replace
long input summaries with `→ exec · input` placeholders. One assistant body
retains part order, followed by input and launch markers in call order. Input
details retain `RoleTool`, the `Tool input` heading, and the complete code body.
The existing normal/find heading helper introduced in Task 2 is reused.

A viewer-only map assigns `exec · output` to results by exact response call ID,
including results preceding their calls. Nested edit IDs do not participate.
Both inline and collapsed output retain their complete bodies. Only the owning
assistant body receives `SourceAnchor`; global source-line jumps prefer it
within the latest eligible line group. Existing unmarked ties still select the
last message, and local restoration continues to use message ordinals.

### Synthetic and viewer coverage

`TestTranscriptCodeInput_*` covers exact byte/line boundaries, multibyte byte
counts, empty/short calls, pragmas, quotes, escaped newlines, CRLF, optional JSON
omission, mixed input/launch ordering, sidecar mapping, compact inline/collapsed
results, unmatched IDs, unchanged shared summaries, and the wrapped four-file
fixture with its +23/−14 grouped edit. The existing group test now selects the
file-change heading explicitly because input details also carry headings.

`TestViewerCodeInput*` covers one heading per assistant entry, Markdown bypass,
full-body detail and clipboard data, exact/fuzzy hidden-text matches, repeated
visible/hidden navigation without corpus duplication or frame growth, pending
navigation resize/cancel, raw return, child return, and marker restoration after
rewrap. A tall assistant body pins global landing on its heading and early phrase
despite appended input and launch markers. These tests pass in both builds.

### Corpus evidence

Working-tree base: `9e99ed3acbe509e11645c9e9da150a92271cdc12`.
The original machine-local baseline and companion remain unchanged:

- TSV SHA-256: `c68edd3a7734c202c364d7e87a02298050fb2e64be7a50ca5337b050c1a08055`.
- Companion SHA-256: `d7178b74f3810e02f6d48c98626c0361b3feec2721f3404acf440bf9674abc4f`.

`TestCodexParityCanary` passed in 66.36 seconds: **478 unchanged recordings,
zero scanner/text/Markdown mismatches**. Of 565 discovered recordings, 4 were
appended, 83 new, 0 otherwise changed, 0 removed and 0 vanished; none were
compressed or revert variants. Unchanged category coverage: 111 completed
paginated, 8 failed, 1 declined, 2 real-move and all 13 legacy-direct recordings.
The original absolute `CAPY_CODEX_PARITY_BASELINE` path was used; no baseline
was recaptured. Raw recordings and per-session paths remain machine-local.

### Checks

All checks use `GOCACHE=/tmp/capy-codex-vault-viewer-go-cache`,
`CAPY_DB_KEY=test-key-for-development` and `CAPY_VAULT_KEY=test-key`.

- Focused input, file-change, decoder/consumer and navigation checks passed.
  Frozen scanner/text/Markdown goldens and existing Claude goldens pass without
  regeneration. Initial new test failures were incorrect bracket expectations
  for the existing plain result prefix; assertions now match its actual format.
- `go test -tags fts5 -count=1 ./...` passed every package with an empty temporary
  `XDG_CONFIG_HOME` and unrestricted execution for existing socket tests.
  CLI: 230.53 seconds; vault: 241.16 seconds; default TUI: 22.19 seconds.
- `go test -race -tags fts5 -count=1 -run
  'TestTranscriptCodeInput|TestViewerCodeInput|TestCodexDecoder|TestCodexConsumers|TestViewerFind|TestViewerTargets|TestAppFind'
  ./internal/vault/...` passed (vault 1.66 seconds, TUI 19.79 seconds).
- `go test -race -tags fts5,glamour -count=1 ./internal/vault/tui/...` passed
  the full glamour TUI suite (56.18 seconds).
- `go test -tags fts5 -count=1 -run '^TestCodexParityCanary$' -v
  ./internal/vault` passed with the original baseline, as reported above.
- `gofmt` and `git diff --check` are clean.

No new dependency, schema/index change, setup artifact, or search matcher was
introduced. Final same-corpus performance, find-latency and retrieval-quality
benchmarks, public documentation, and feature-wide spec review remain Task 6.
Task 5 required no design deviation or new convention beyond the recorded plan.

### Review completion

The [isolated review](.reviews/task-5-code-review-2026-10-03.md) is complete.
The independent reviewer approved the implementation and its follow-up without
findings. Gemini's single native HIGH finding concerned redundant newline scans
in the shared collapse predicate. A before/after helper benchmark confirmed the
cost; the predicate now checks byte length first. Detailed measurements and
their limits are in the review report. This changes neither threshold and does
not affect scanner/export output.

After that reorder, focused default input/tool/navigation race tests passed
(vault 1.20 seconds, TUI 19.76 seconds), as did the glamour input-navigation race
subset (1.18 seconds). The full suites above preceded this equivalent predicate
reorder; the post-fix runs target its affected behavior. No Task 5 follow-up
remains. Task 6 and issue #121 remain open.

## Task 6 — final verification and documentation (2026-10-03)

Working-tree base: `a0d2a9bafc78` (Tasks 1–5). Task 6 adds reproducible viewer
and decoder benchmarks, public documentation, and two measured allocation
improvements. It changes neither recorded outcomes nor displayed bytes.

### Final compatibility and real viewer evidence

The original pre-feature production revision is
`0be03aaeaeb8429c480fd631de371007b56b7d38`. The machine-local baseline files were
not regenerated:

- TSV SHA-256: `c68edd3a7734c202c364d7e87a02298050fb2e64be7a50ca5337b050c1a08055`.
- Companion SHA-256: `d7178b74f3810e02f6d48c98626c0361b3feec2721f3404acf440bf9674abc4f`.

After the allocation changes, `TestCodexParityCanary` passed in 73.54 seconds:
**478 unchanged recordings, zero scanner/text/Markdown mismatches**. Of 570
discovered recordings, 4 were appended, 88 new, 0 otherwise changed, 0 removed
and 0 vanished. There were no compressed recordings or revert variants in this
local corpus; synthetic cases cover them. Unchanged category coverage remains
111 completed paginated, 8 failed, 1 declined, 2 real-move and 13 legacy-direct
recordings. Changed/new recordings are excluded from parity claims.

The final read-only viewer canary passed on 158 edit recordings: 1,022 completed,
9 failed and 1 declined canonical operations; 872 adds, 48 deletes, 1,095 updates
and 12 moves; zero unavailable completed-file diffs. All 21 legacy events retained
exact direct associations and the expected grouped-edit/compact-result display.
These are decoder/viewer assertions over real bytes, not a human visual audit.
Only aggregate evidence is committed; raw recordings and per-session paths stay
machine-local. Synthetic frozen scanner/export outputs and Claude goldens pass
without regeneration.

```sh
CAPY_CODEX_PARITY_BASELINE="$PWD/bench-results/codex-vault-viewer-parity.tsv" \
CAPY_CODEX_CHANGES_CANARY=1 \
  go test -tags fts5 -count=1 \
  -run '^(TestCodexFileChangeCanary|TestCodexParityCanary|TestCodexFileChangeDiff.*)$' \
  -v ./internal/vault
```

### Tests and builds

All Go runs use `CGO_ENABLED=1`, `GOCACHE=/tmp/capy-codex-vault-viewer-go-cache`,
synthetic `CAPY_DB_KEY=test-key-for-development` and `CAPY_VAULT_KEY=test-key`,
and an empty `XDG_CONFIG_HOME=/tmp/capy-viewer-task6/config`. Full-suite commands
run with local-socket access for the existing socket tests.

- `go test -tags fts5 -count=1 ./...`: passed before and after the measured
  decoder changes. Final CLI 250.64 s, vault 261.08 s, default TUI 22.73 s;
  every package passed.
- `go test -race -tags fts5,glamour -count=1 ./internal/vault/tui/...`: passed
  the full glamour TUI suite (58.74 s). The affected viewer subset passed again
  after the decoder changes (1.22 s).
- `make build`, `make build-glamour`, and `go vet -tags fts5 ./...`: passed
  after the final decoder change. Builds emitted nonfatal read-only module-cache
  metadata warnings; both exited successfully.
- The initial `go test -race -tags fts5 -count=1 ./...` passed every package
  except vault, where the existing live-corpus `TestCodexCanary` exceeded Go's
  default 10-minute timeout while scanning/sanitizing recordings. No race was
  reported. `go test -race -tags fts5 -count=1 -timeout=30m ./internal/vault/...`
  then passed the complete vault package (817.57 s) and default TUI (36.46 s),
  including the live-corpus canary, after the final decoder change. Together
  with the other packages from the original run, every package passed under
  race detection. The timed-out invocation itself is not counted as a pass.

Ordinary suites skip opt-in latency, quality and parity tests unless their
environment gates are set; those gates were explicitly exercised separately.
No real corpus category was skipped. The absent `benchstat` binary means no
statistical-significance claim is made for timing comparisons.

### Find latency and ownership

The unchanged source-owned `TestFindLatency` ran with `CAPY_FIND_BENCH=1` and
`GOMAXPROCS=2`: **86 operation classes × 100 samples in each build, all below
100 ms**. Maximum: **53.611 ms default**, **38.547 ms glamour** (both fuzzy
one-character editing). Full per-operation median/p95/maximum/allocation logs:
[default](.reviews/task-6-latency-default.txt),
[glamour](.reviews/task-6-latency-glamour.txt).

Environment: Go 1.26.4, Linux 7.1.1/amd64, Intel Core i7-11800H. Reference seed
20260920; SHA-256 `d565dddaa76fbb50f300b5ab7d5c79e3f5b0add1c46fa09d1d8528175b9a0ef9`;
10,000 lines, 1,034,588 content bytes, 200 messages, 100 collapsed bodies;
100×30 terminal. Measurements include root Update, scheduling, matching,
projection/application and View; archive parsing is excluded by the established
protocol. Race tests were running separately, so these timings are environment
observations rather than a cross-machine comparison with earlier reports.

`TestFindLifetimeStress` also passed in both builds. Twenty nested
child/tool/fuzzy/raw open/back cycles left empty frame backing arrays, preserved
the parent search, and released the corpus/projection on viewer exit. The default
retained-heap delta after twenty cycles was 11,456 bytes; leaving the viewer
released about 6.66 MB relative to the prepared-search baseline. This is a bounded
retention observation, not a proof about arbitrary archive sizes.

```sh
CAPY_FIND_BENCH=1 GOMAXPROCS=2 go test -tags fts5 -count=1 \
  -run '^(TestFindLatency|TestFindLifetimeStress)$' -v ./internal/vault/tui
CAPY_FIND_BENCH=1 GOMAXPROCS=2 go test -tags fts5,glamour -count=1 \
  -run '^(TestFindLatency|TestFindLifetimeStress)$' -v ./internal/vault/tui
```

### Allocation profiling and chosen implementation

`BenchmarkCodexUpdateDiff` compares small/large valid hunks with malformed input
rejected at the first line. `BenchmarkCodexChangeOutput` measures 64-byte and
1-MiB stdout. Six samples use `-benchmem -benchtime=200ms`, Go 1.26.4 and
`GOMAXPROCS=2`. A separate one-second CPU/alloc-space profile attributed 61.5%
of sampled allocation space to builder growth in this deliberately narrow
microbenchmark workload. JSON validation/unquoting remains the major stdout cost.

| Case | Before median | After median | Before → after bytes/op | Allocations/op |
| --- | ---: | ---: | ---: | ---: |
| 32-line valid update | 2.142 µs | 0.839 µs | 4,584 → 192 | 11 → 3 |
| 16,384-line valid update | 918.086 µs | 269.953 µs | 3,241,971 → 192 | 35 → 3 |
| Large input, early malformed | 28.670 ns | 26.830 ns | 32 → 32 | 1 → 1 |
| 1-MiB stdout | 4.353 ms | 3.992 ms | 2,105,504 → 1,048,736 | 4 → 3 |

Raw [before](.reviews/task-6-hotspots-before.txt) and
[after](.reviews/task-6-hotspots-after.txt) results are retained. The after run
first used a Go overlay with the same final code to validate the optimization
before changing production. Timing differences are descriptive medians;
allocation reductions are the basis for adopting the changes.

Byte-based null checks remove temporary copies in stdout/stderr, content and
destination decoding. For updates, validation never rewrites hunk bytes, so
`Diff.Text` can retain the original suffix after optional file headers. This
removes the builder entirely rather than reserving a large capacity before
validating malformed input. `FileChange.Content` already retains the same source
string. Existing exact-byte tests cover multiple hunks, file headers, CRLF,
annotations, final-newline absence and malformed boundaries. Both independent
reviewers and the external reviewer approved the equivalence.

### Same-archive viewer costs and separate stress

`BenchmarkCodexViewerArchive` reads an opt-in frozen uncompressed recording
outside timing. The selected file is an unchanged completed-paginated recording
from the Task 1 manifest: 997,960 bytes, SHA-256
`45f3d04990e0dfa347862780c99704c7fc42ff5be2faaa3fa5b536033d8fd131`.
Its path/content remain machine-local. The identical harness was copied into
the `0be03aa` snapshot, and default/glamour test binaries were compiled there
and from the final working tree. Six sequential before/after pairs per build
used `GOMAXPROCS=2`, `-test.benchmem`, `-test.benchtime=200ms`, width 99.
The independent long-running race test remained active; these are local paired
observations, not statistically significant estimates. Earlier exploratory runs
overlapped full-suite/corpus work and are not used for the comparison below.

| Build/operation | Before median | Final median | Before → final bytes/op |
| --- | ---: | ---: | ---: |
| Default parse | 14.339 ms | 14.616 ms | 3,113,605 → 3,445,390 |
| Default collapsed render | 0.971 ms | 0.352 ms | 519,834 → 138,198 |
| Default find-corpus build | 0.047 ms | 0.049 ms | 210,737 → 210,992 |
| Glamour parse | 14.361 ms | 14.678 ms | 3,074,235 → 3,415,493 |
| Glamour collapsed render | 5.911 ms | 1.643 ms | 2,111,449 → 767,318 |
| Glamour find-corpus build | 0.058 ms | 0.061 ms | 210,736 → 210,993 |

Parsing now normalizes events that were previously skipped and materializes their
viewer bodies: approximately 0.3 ms and 0.33 MB more on this input. Rendering
benefits from compact input markers (about 64%/72% less elapsed time), while the
complete searchable content remains present. No unexplained cost change remains
on this measured archive; this is not a guarantee for every recording.

The separate synthetic `BenchmarkCodexViewerLargeEdit` includes a 16,384-line
executable input and a 16,384-line replacement hunk. Final default/glamour medians:
parse 33.205/27.695 ms, collapsed render 0.024/0.066 ms, find-corpus build
7.152/8.244 ms. Parse allocates about 12.84/12.69 MB and the complete find corpus
15.92 MB. These measurements overlapped other validation and have **no 100 ms
acceptance claim**. They measure parsing/collapsed rendering/corpus construction;
expanded-detail input-to-frame behavior is covered by navigation tests and the
separate established latency workload, not by these throughput benchmarks.

[Raw paired archive and stress results](.reviews/task-6-viewer-benchmarks.txt).
Reproduce after choosing the same frozen local input:

```sh
CAPY_CODEX_VIEWER_BENCH_INPUT=/absolute/path/to/frozen-rollout.jsonl \
GOMAXPROCS=2 go test -tags fts5 -run '^$' -v \
  -bench '^BenchmarkCodexViewer(Archive|LargeEdit)$' -benchmem -benchtime=200ms \
  -count=6 ./internal/vault/tui
# Repeat with -tags fts5,glamour; use the same harness and input on the baseline.
```

### Retrieval quality, documentation and review

`make bench-quality` passed before and after the decoder optimization, retaining
reports as `codex-vault-viewer-task6.json` and `codex-vault-viewer-task6-final.json`.
The baseline was newly generated from a clean `git archive 0be03aa` snapshot under
`/tmp`, named `codex-vault-viewer-pre-feature.json`; its embedded Git metadata is
`unknown` because the snapshot has no `.git`, so the source revision is recorded
here explicitly. Earlier benchmark files were preserved.

`make bench-compare BASE=codex-vault-viewer-pre-feature TARGET=codex-vault-viewer-task6-final`
found identical retrieval and context-reduction metrics on dataset SHA-256
`7d45338724b05181ebc92bd0b74eb7708bdd4fba27a8d6830b54a127f2b6ba2d`.
Overall R@1/R@10/NDCG@10/MRR remain 0.910/0.994/0.955/0.945. Performance comparison
via `benchstat` was skipped because it is not installed; the dedicated viewer
measurements are separate from quality and the corpus-output parity gate.

`kk:document` updated README viewer guidance, architecture mechanisms and model
comments. The existing CLI and CI configuration is inherited; no command, flag,
configuration setting, dependency version, deployment, CI or setup artifact is
changed. ADR-031 already governs the decoder/consumer boundary; no new ADR is
needed for these display policies. Optional prose clarification can be requested
with `kk:clarify-docs README.md docs/architecture.md`.

The [isolated code review](.reviews/task-6-code-review-2026-10-03.md) and
[isolated spec review](.reviews/task-6-spec-review-2026-10-03.md) approved the
feature and measured follow-up. All external LOW suggestions were addressed;
no required behavior or review fix is deferred. No new convention or systemic
finding needs indexing beyond the decisions and measurements recorded here.

### Completion and reflection

Task 6 and the feature are complete; all accepted behavior is implemented and
verified. No required work is deferred. The only implementation refinement was
measurement-driven: retaining validated hunk text removed the need to choose a
builder capacity and preserved cheap malformed-input rejection. The live-corpus
race canary needed a longer timeout, while paired normal rendering became much
cheaper once long code was disclosed through markers. These observations are
captured above rather than promoted to general timing guarantees.
