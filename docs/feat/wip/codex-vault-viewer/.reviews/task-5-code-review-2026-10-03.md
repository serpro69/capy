# Task 5 isolated code review — 2026-10-03

Scope: long executable inputs, result aliases and source landing on base
`9e99ed3acbe509e11645c9e9da150a92271cdc12`. Tasks 1–4 supply completed
compatibility context. Task 6 final benchmarks, public documentation and
feature-wide spec review remain outside missing-implementation findings.

## Reviewers and methodology

- Independent `code-reviewer` sub-agent with no implementation conversation:
  **APPROVE**, no P0/P1/P2/P3 findings in the initial 10-file, 506-line change.
  It checked both rendering paths and navigation ownership statically; it did
  not independently execute tests or inspect the local corpus.
  A second review of the 2-file follow-up approved the predicate reorder,
  benchmark and comment clarifications with no findings.
- PAL `codereview`, Gemini 3.1 Pro, external two-step workflow with maximum
  reasoning: one native **HIGH** performance finding, described below.

Go activated on `.go` extensions. Both reviewers received the SOLID, removal,
security, code-style, error-handling, injection and performance checklists,
the patch including new files, surrounding source, and design/task scope.
No other profile activated. No dependency or setup artifact changed.

## External review finding

**[HIGH] `internal/vault/transcript.go`, `overCollapseThreshold`: O(N) string
traversal precedes O(1) length check for large payloads.**

Gemini observed that the new alias and body-composition passes both call the
existing helper on executable inputs. Its recommendation was to check
`len(body) > collapseToolResultBytes` before counting newlines, avoiding a full
scan for inputs that already exceed the byte limit.

**Author context:** the helper's predicate order predates this change; Task 5
adds two callers on potentially large code strings. The concern was validated
with a focused before/after benchmark and addressed by reordering the pure OR
operands. Collapse semantics are identical. This benchmark establishes the
local redundant scan, not an end-to-end viewer latency regression or severity.
The report retains the external reviewer's native HIGH classification.

`BenchmarkExecutableInputThreshold` ran three 100 ms samples per case on
Linux/amd64, Intel i7-11800H, Go 1.26.4. Every case allocated zero bytes:

| Body | Before | After |
| --- | --- | --- |
| Short code | 4.19–4.44 ns/op | 4.24–4.33 ns/op |
| 21 lines, below byte limit | 6.30–6.36 ns/op | 7.14–8.34 ns/op |
| 4 KiB, one line | 49.41–49.55 ns/op | 0.51–0.66 ns/op |
| 4 MiB, one line | 72.95–77.29 µs/op | 0.48–0.59 ns/op |

These short microbenchmarks demonstrate the bounded work of the reordered
predicate. Small-case differences are reported without a statistical or
whole-viewer performance claim. Task 6 retains the full parse/render and
find-latency protocol.

No corroborated or author-sourced correctness findings were raised. No findings
to index: the external item is a single utility's predicate ordering, with no
systemic P0/P1 pattern or new project convention beyond the documented design.

## Validation

See [Task 5 verification](../verification.md#task-5--compact-executable-inputs-and-source-landing-2026-10-03).
Full default tests, focused default race checks, the full glamour TUI race suite
and original corpus parity passed. After the predicate reorder, affected
input/tool/navigation tests were rerun under the race detector in both builds.
The reorder affects viewer collapse policy only; scanner and exports never
call this helper.
