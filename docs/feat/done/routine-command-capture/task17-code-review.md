# Task 17 isolated code review

Review scope: the complete feature, `595018f..3dc03a2`, plus Task 17 changes.
The independent `code-reviewer` first reviewed the 202-file feature scope in
logical batches, concentrating on production execution/storage/security paths
and representative tests. Its follow-up reviewed the 20 changed source/test files
and related context. This is a source review, not independent test execution.

The Go profile covered SOLID/removal, security, style, error handling, injection,
naming, performance, database/sql and concurrency. gRPC did not apply. The
reviewer's knowledge searches found no relevant prior findings or idioms.
External PAL review was unavailable; this report has one independent reviewer.

## Initial findings and corrections

| Severity | Finding | Correction and regression |
| --- | --- | --- |
| P1 | Nested literal `capy execute --project-dir ...` could replace the outer caller's policy in MCP/CLI programs | `EvaluateProgram` recursively inspects captured programs before admission. MCP retains deny-only behavior; CLI/hooks retain matched-ask provenance. Security, hook, server and compiled CLI regressions cover nested owner overrides and embedded argv without treating quoted data as executable code. |
| P1 | JSON changed non-UTF-8 program/cwd bytes | Explicit byte-string representation now covers code, cwd, file path, stored owner and supervisor store path. Ordinary UTF-8 requests retain their representation; old string-only readers reject exceptional objects. Compiled CLI regression executes exact raw program bytes in a non-UTF-8 owner/cwd and recovers the expected bytes. |
| P1 | Conservative quota rollback erased the page debit needed to trigger eviction | Retention reserves tentative allocated-page growth as well as logical capture growth. A regression with exactly logical room proves an old completed run is evicted and the active append succeeds within quota. |
| P2 | A live quarantined owner's unretired result could disappear after TTL before reconciliation could claim its slot | Load/list recognize completed-but-unretired evidence independently of registry scans. A regression keeps the lease held, expires the historical result and verifies status/list/raw recovery remain available. |

The first two accounting/byte regressions failed on the initial implementation.
The initial shared-policy change also exposed an existing scanner-boundary
fixture: the recursive pass now budgets its extra spelling/suffix work while
retaining the existing shell scanner limits. No limit assertion was relaxed.

## Re-review

The reviewer assessed all initial findings as resolved and returned **APPROVE**,
with no remaining actionable P0–P3 finding. It also inspected the spec-review
corrections: sanitized tail previews and cancellation classification during
dispatch. Test execution and installed-host acceptance remain the parent
workflow's responsibility, recorded in [verification](task17-verification.md).

Repeated batch-search progress over an expensive ready-child prefix remains an
unverified performance limitation, not a reproduced defect. A follow-up should
measure repeated three-second batch searches with enough already-indexed children
to stress the prefix and assert later-child coverage advances. No claim of
exhaustive adversarial performance coverage is made here.

No separate knowledge note is needed: these findings and conventions are recorded
in repository documentation. Optional editorial follow-up:
`kk:clarify-docs docs/feat/wip/routine-command-capture/task17-code-review.md`.

The reviewer separately approved the final concurrent-search fixture correction
and private-protocol test additions. The former permits only the contract's
explicit transient index failure during active capture, then requires strict
recovery and exact bytes; the latter verifies byte-path framing, unknown-field
rejection and legacy-reader failure. No further production changes followed
source approval. Final test outcomes are in the verification report.
