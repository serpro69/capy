# Task 3 isolated code review

Reviewed 2026-10-10 using `kk:review-code:isolated`. The independent
`code-reviewer` reviewed the guidance filename implementation and regression
tests, with design §3.4 and Task 3 as the acceptance criteria. The Go profile
loaded SOLID, removal, security, code style, error handling and injection
checklists. PAL was unavailable in this session, so the workflow used its
single-reviewer fallback. Review was static; test results were supplied by the
implementing agent.

## Finding and resolution

- **P2 — test project inherited from the environment (100% confidence).**
  The original `guidanceHookCall` used the real Claude adapter, which reads
  `CLAUDE_PROJECT_DIR` into the event. Routing then overrides the temporary test
  project, risking writes outside the fixture and interference among parallel
  tests. The helper now uses the existing `testAdapter`, which parses the JSON
  without importing that environment variable. Each invocation still exercises
  production routing and filesystem persistence with a fresh adapter.
  Verification ran the new tests under `-race` with `CLAUDE_PROJECT_DIR` pointing
  at a separate temporary directory and confirmed it remained empty.

## Final independent assessment

**APPROVE.** The reviewer rechecked the fix and reported no remaining P0–P3
findings. The specified alphabet, byte boundary, digest mapping, bounded
filenames, creation/reset consistency, empty-ID behavior, traversal sentinels
and session isolation were covered. No production defects were found.

No findings to index: no systemic P0/P1 findings. The resolved test issue is
recorded here rather than duplicated in the knowledge base. Other feature tasks,
including observation-state persistence in Task 5a, remain outside this review.
