# Task 10 independent reviews — 2026-09-23

Reviewed through `kk:review-code:isolated` and `kk:review-spec:isolated`.
Independent reviewers had no implementation-session history. The Go profile
applied. PAL tools were unavailable, so code review used the skill's independent
sub-agent fallback. Reviewers inspected code and test/report artifacts; execution
was performed by the implementing session and is recorded in [tasks.md](tasks.md#task-10-verification-and-review-2026-09-23).

## Code review

Initial scope: 12 changed files, 613 lines changed (+582/-31), including new files.
The reviewer covered encrypted maintenance, edit/delete concurrency, physical
restore/resume paths, generated routing parity, benchmark comparability and
test-oracle correctness. No P0/P1/P3 findings.

One **P2** finding: the initial canary correction classified tagged response
items using event membership. A missing event could therefore also remove its
expected response from the tally, concealing incomplete events.

**Resolved:** the test-only `codexCanaryResponseText` classifier recognizes the
observed question-reply wrapper independently of the event stream. Both history
modes now reject an unpaired tagged response beside ordinary prompts and an
event-free tagged reply. Text/multiplicity comparison and injected-noise filtering
remain. Production decoder policy is unchanged.

The same independent reviewer inspected the correction and its negative fixtures:
**APPROVE**, no remaining findings. No systemic P0/P1 findings to index.

## Spec review

Scope: Tasks 1–10 and all seven design success criteria, including the current
Task 10 despite its in-progress execution status at review time.

**CONFORMANT**, zero P0/P1/P2/P3 findings, doc inconsistencies or ambiguities.
The reviewer traced schema/migration parity, normalization, independent clocks,
tombstones, pre-limit SQL predicates, CLI/JSON compatibility, both MCP scope
modes, federation errors, all merge branches/order/atomicity, TUI scope/edit/
refresh behavior, and maintenance/restore/resume preservation. The reviewer
also checked scale methodology, stored quality metrics, routing documentation
and the final canary correction.

No intentional spec deviations or unresolved findings to index. Interactive TTY
behavior was checked through code and fixtures; no manual terminal session was
performed. Existing XDG/macOS fixture-isolation limitations remain documented
under Tasks 1 and 5, with explicit environment controls used for full execution.

Temporary cleanup: automatic approval review rejected removal of
`/private/tmp/capy-project-task10-review.patch` because deleting files requires
explicit user approval. It remains outside the repository; it can be removed
manually when no longer needed. The temporary master worktree was removed after
preserving its benchmark report in the main checkout.
