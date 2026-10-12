# Task 18 isolated code review — 2026-10-12

Scope: optional batch queries, exact/global knowledge scope, persisted command
previews, bounded section metadata/response rendering and byte accounting.
Tasks 19–20 and D1–D5 remain outside this review.

## Reviewers and methodology

- Independent `code-reviewer` agent, with no implementation history, using the
  Go profile's SOLID/removal/security/style/error/injection/naming/performance/
  concurrency criteria. Reviewed 12 files and 992 changed lines in the supplied
  patch, then re-reviewed final corrections in three live files. Its role is
  read-only; test executions below are the implementation session's evidence.
- PAL `gemini-3.8-flash`: initial submission succeeded, but expert analysis failed
  with `API_KEY_INVALID`. It supplied no review findings or approval.

## Findings and disposition

The independent reviewer reported **APPROVE**, with no P0–P3 findings or removal
candidates. It checked validation before execution, exact batch scope,
durable+ephemeral global filtering without vault access, safe persisted previews,
stored totals for unchanged batches, parameterized bounded inventory queries,
reserved JSON budgets, byte separation and serial/parallel execution behavior.

Final corrections were independently re-reviewed and approved:

1. Full-suite integration assertions required preserving existing `## <query>`
   headings. The formatter again uses that wording.
2. Existing invalid-cwd fixtures required preflight rejection to leave call
   statistics untouched. Every result remains bounded, but tracking now begins
   only after argument/cwd/security admission. Accepted responses are counted
   after bounding.
3. Author-sourced: Go `%q` can emit `\x` escapes for control-bearing source labels.
   JSON quoting now keeps the displayed selector reusable as a tool argument.
   A regression decodes the selector and retrieves the exact source, including
   NUL, quotes, backslash, Unicode and HTML-significant characters.

The reviewer inspected the additional accounting, selector, distinctive-term and
large-policy-error regressions. Focused/race/stdio/quality checks were reported by
the implementation session. The final full-suite rerun was still pending when
the reviewer issued its assessment; its result is recorded in
[verification.md](../verification.md#task-18-batch-query-scope-and-bounded-provenance).

No systemic P0/P1 findings to index. No new convention requires separate indexing;
the implementation notes and verification record preserve the relevant details.
