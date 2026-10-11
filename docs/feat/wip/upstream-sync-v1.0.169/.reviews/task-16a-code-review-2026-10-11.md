# Task 16a isolated code review — 2026-10-11

Reviewers: independent `code-reviewer` agent without implementation-session
history; PAL `gemini-3.8-flash` was attempted but unavailable. Scope: Task 16a
persisted fetch validation and regressions in completed tasks. Tasks 16–20 and
deferred D1–D5 remain outside this slice.

**Code-reviewer assessment: APPROVE. No P0–P3 findings.**

The independent reviewer examined 20 files and 878 changed lines, including
untracked tests and the later actual HTML-conversion failure fixture. Go
activated via `.go` extensions; checklists were SOLID, removal, security, code
style, error handling, injection, naming, performance, database and concurrency.

The review verified atomic content/timestamp pairing, both dedup branches,
retained counts/type, generic replacement clearing, migration locking/rollback,
conservative freshness checks, retention independence and single/batch failure
handling. No removal candidates or findings to index. The reviewer did not run
tests; author-run commands and environmental qualifications are recorded in
[verification.md](../verification.md#task-16a-atomic-fetch-validation-renewal).

Coverage limits noted by the reviewer: timing tests exercise cleanup and batch
queue delays, while the out-of-order store fixture commits sequentially with
reversed response timestamps. Actual competing fetch writes and held-lock delays
are not directly exercised. Inspection confirmed timestamps are supplied before
lock acquisition and committed with their corresponding content.

The full suite subsequently identified an existing boolean stdio fixture that
seeded cache freshness with generic indexing. The fixture now uses `IndexFetched`
with the same synthetic key, project database, kind and composite URL label;
force-string/cache-hit/SSRF assertions remain intact. Its focused race test passes.
This is a test adaptation to the intentionally changed freshness contract.
The reviewer separately examined that one-file, ten-line follow-up and approved
it without P0–P3 findings; the original approval stands.

## External review availability

PAL listed `gemini-3.8-flash` as its most capable configured model. Step 1 created
a continuation; step 2 failed with `API_KEY_INVALID` (HTTP 400), so no expert
assessment was produced. The independent code reviewer supplies the completed
review under the skill's single-reviewer fallback. The failed external call is
not counted as approval.

## Indexing

No findings to index. The implementation patterns and compatibility details are
already recorded in ADR-013 and the feature documents; no duplicate project
convention note was added.
