# Design Review: vault-in-session-search

**Date:** 2026-09-20
**Scope:** [design.md](../design.md), [implementation.md](../implementation.md), [tasks.md](../tasks.md)
**Overall assessment:** CONCERNS_FOUND
**Summary:** 2 findings: 0 critical, 2 high, 0 medium, 0 low.

## Findings

### P0 - Critical

(none)

### P1 - High

#### RD-1 — TECH_RISK: NUL-bearing content can panic the proposed fuzzy worker

- **Section:** design.md:90,108,155; implementation.md:98; tasks.md:73.
- **Confidence:** 10/10 — reproduced against the pinned dependency.
- **Description:** The corpus includes original message bodies; control escaping affects presentation only. Task 4 passes complete content lines to sahilm/fuzzy v0.1.1 without defining protection against its NUL sentinel.
- **Evidence:** `FindNoSort("a", []string{"a\x00b"})` panics with `index out of range [1] with length 1`. The matcher treats a NUL lookahead as end-of-input, advances the pattern index, then indexes beyond the pattern on the next iteration. Valid JSON can encode this body using `\u0000`; the query itself need not contain controls. An unhandled worker panic can terminate the application. See the [pinned source](https://github.com/sahilm/fuzzy/blob/v0.1.1/fuzzy.go#L134-L188).
- **Recommendation:** Define a NUL-safe matching path preserving whole-line subsequences and original byte positions. Add cases before, after, and across embedded NULs. Skipping the line or matching generated escape spellings would change the specified corpus.

#### RD-2 — TECH_RISK: The pinned scorer corrupts long-query positions

- **Section:** design.md:68,74,155; implementation.md:98,152; tasks.md:73.
- **Confidence:** 10/10 — reproduced on linux/amd64 with Go 1.26.4.
- **Description:** The 256-code-point limit exceeds the matcher's safe scoring range. A strict result comparator cannot repair overflowed scores or invalid character positions.
- **Evidence:** Identical query/candidate strings of 40 repeated `a` characters produce score `-8315356191162110941`. At 43 characters, returned positions repeat byte 41 and omit byte 42; 64- and 256-character cases also violate strict position ordering. Exponential adjacency bonuses overflow machine integers. Highlighting these positions misses characters in a fully matching line. See the [scoring implementation](https://github.com/sahilm/fuzzy/blob/v0.1.1/fuzzy.go#L147-L158).
- **Recommendation:** Specify bounded scoring with valid increasing source positions, using a corrected adapter/local matcher or a verified dependency change. Add correctness cases across 1–256 characters; the planned long-query latency checks alone do not assert these invariants.

### P2 - Medium

(none)

### P3 - Low

(none)

## Clean Areas

Source identities, plain rendering, nested scope ownership, shortcut precedence, cancellation/epochs, rollback, and task structure are coherent. Assumptions, exclusions, sequential dependencies, both build variants, and end-to-end performance measurement are explicit. Referenced integration seams and issue #101 were checked.

## Verification and remaining work

Two temporary Go probes used `go run -mod=readonly -tags fts5` against the existing module; repository code and dependencies were unchanged. No feature tests or latency measurements were claimed.

RD-1/RD-2 remain open because this invocation reviews the plan. Before Task 4, revise the matcher choice and correctness tests in all three documents. The existing pin cannot be accepted solely from API inspection.

## Next steps

Choose: update both findings; discuss specific findings; accept the risks and implement; or finish with this report. Design-document changes await confirmation.
