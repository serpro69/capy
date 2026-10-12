# Task 17 isolated code review — 2026-10-12

Review mode: mid-implementation. Task 17 is in scope; completed Tasks 1–16,
including lettered prerequisites, provide regression context. Tasks 18–20 and
deferred D1–D5 are out of scope. The independent `code-reviewer` agent received
no implementation-session history. This report preserves its returned findings;
the parent saved it because the reviewer role permits read-only inspection.

## Independent review

**Files reviewed:** 17 files, 617 lines changed (558 additions, 59 deletions).
**Active profile:** Go, activated by `.go` extensions.
**Checklists:** SOLID, removal plan, security, code style, error handling,
injection security, performance and concurrency from kk 0.23.0.
**Assessment:** APPROVE — no unresolved findings after correction.

### P0 / P1

None.

### P2 — malformed authorities inflated attempted-miss counters (resolved)

- Location: `internal/server/ssrf.go:38`.
- Profile/checklist: Go / `security.md`; user-supplied fetch URLs require input
  validation. Confidence: 100%.
- The initial implementation accepted `https://example.invalid:80:90/`: Go's
  parser validates the final numeric port but leaves `example.invalid:80` as
  the hostname. The subsequent transport/DNS failure incremented attempted
  misses, violating Task 17's malformed-URL exclusion. The parent reproduced
  one miss instead of zero in both single and batch modes.
- Suggested correction: reject unbracketed colon-containing hostnames before
  cache/network work and validate bracketed IPv6 literals while preserving
  zone syntax.
- Final disposition: implemented and independently re-reviewed. Regression
  cases cover malformed authorities, unbracketed IPv6 and invalid literals;
  positive cases retain bracketed IPv6, zones and valid port boundaries.
  No remaining issue found.

Author context: a newly added positive IPv4-mapped bracket-literal fixture was
incorrect: the installed Go URL parser already rejects that form. The fixture
was moved to rejection coverage, preserving the existing parser behavior.

### P3

None.

### Removal and indexing

No removal candidates or outstanding code corrections. No findings to index;
the resolved P2 is recorded here and establishes no new undocumented convention.

### Evidence limits

The reviewer traced typed outcomes across single/batch fetches, failed
network/body/conversion/index attempts, bypasses and exclusions, concurrent
accounting, snapshot/reset behavior, uptime preservation, zero-safe hit rates,
configured freshness and both savings displays. Existing savings arithmetic and
knowledge-base statistics remain unchanged.

The reviewer performed read-only inspection and did not execute tests.
Parent-reported post-fix evidence: focused race suite with fetch integration
coverage passed in 21.787s; final syntax/rejection tests under `-race` passed in
1.374s. Full-suite verification was pending at review return; its final outcome
and pre-existing platform failures are recorded in
[verification.md](../verification.md#task-17-cache-outcome-statistics).
External DNS ordering and blocked redirects were traced rather than independently
exercised by the reviewer.

## External reviewer limitation

PAL model discovery succeeded and the two-step review was submitted to
`gemini-3.1-pro-preview`. The expert step failed with `API_KEY_INVALID` (HTTP 400);
it supplied no independent findings or approval. The isolated workflow's
single-reviewer fallback completed through the code-reviewer above.

The user authorized implementation of Task 17, so its reproduced review finding
was corrected within that scope without a separate approval pause.

## Follow-up: portable Git-ignore batch fixture

The full suite exposed an unrelated Unicode filename collision on macOS,
reproduced on untouched `888ed2a` and confirmed with `xĀ.md`/`xā.md` resolving to
the same file. The user requested a separate fix commit. `ea96dc0` adds a unique
ASCII numeric prefix to each Unicode-bearing fixture filename in
`internal/knowledge/gitignore_test.go`, preserving all 384 files, three batches,
and original assertions.

The same independent reviewer inspected this incremental test-only correction
and returned **APPROVE — no findings**. Production behavior is unchanged.
Parent-run focused race verification passed in 1.380s.
