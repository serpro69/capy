# Feature spec conformance review — 2026-10-02

Result: **CONFORMANT**. All Tasks 1–6 were explicitly in scope, including the
current task awaiting review/evidence completion. No outstanding P0–P3 findings,
documentation issues, ambiguities or intentional deviations to index.

The isolated `spec-reviewer` read design, implementation and task documents,
performance and verification records, public README/architecture changes, and
the relevant TUI implementation/tests. Go activated by `.go` extension; the
installed profile had no `review-spec` slot, so generic spec methodology applied.

## Acceptance coverage

1. Literal matching preserves overlaps, full collapsed fields, corpus ordering
   and original byte coordinates without caps.
2. The fuzzy matcher follows the folding, alignment, bounded scoring,
   cancellation and span contracts; selection opens ordinary or hidden content.
3. Source mapping preserves occurrence identity through wrapping, resizing,
   escaping and grapheme highlighting.
4. The inspected raw logs contain 86 classes × 100 samples in each build.
   Measurements include Update, scheduling, computation, application and View.
   Retained-memory checkpoints follow command retirement.
5. Claude/Codex and default/glamour have implementation and regression coverage;
   clearing restores normal rendering.
6. Input routing, transaction cancellation, independent scopes, nested returns,
   fresh epochs and stale-result retirement match the design.
7. Local search owns no store and invokes no archive write, reindex, external
   process or query persistence.

## Resolved P2 — one-row root status overflow

Static tracing found that `Model.View` appended status after the search footer
even when the terminal had only one row. This violated the explicit row budget
and query/counter priority. The author reproduced it in four cases: exact/fuzzy
selection, with copy at height one or with shrink after copying at height ten.
Each failed with two rows before the fix.

`app.go` now renders status only when height exceeds one, preserving status text
and error state for later growth. `TestAppFindStatusAtOneRow` asserts one row,
the counter, selected source identity and status recovery after enlargement.
The spec reviewer re-read the fix/test and found no further code or doc issue.
The independent code reviewer also approved this follow-up.

## Evidence limits

The reviewer performed static inspection, not builds, tests, benchmarks or the
terminal walkthrough. Execution evidence comes from the checked logs and the
author's verification record. Final latency reruns after the narrow layout fix
were underway at review close; their refreshed figures/digests must be recorded
before marking Task 6 done. Earlier gates and prototypes remain historical
evidence with their original limits. Terminal painting, archive loading and
arbitrary-size performance are outside the defined 100 ms guarantee.

Author completion record: both final reruns passed after the layout correction,
with 86 × 100 samples each and maxima 29.135 ms default / 28.601 ms glamour.
Performance tables, retained-memory figures, raw logs and the full viewer bundle
digest were refreshed before Task 6 was marked done. No implementation changed
after the reviewed status fix.
