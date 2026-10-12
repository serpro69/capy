# Task 20 isolated code review

**Scope:** complete upstream-sync runtime since `c0bfff2`, plus Task 20 tests/docs.
**Reviewer:** independent code-reviewer with bounded independent security/executor
and ingestion/store/CLI reviews; Go profile. No authorship context inherited.
**Final assessment:** APPROVE after corrections; no remaining P0–P3 findings.

The initial report inspected 149 files / 17,501 changed lines in the supplied
feature and supplement patches. It requested changes for three P2 findings:

| Finding | Evidence and correction |
| --- | --- |
| Direct argv was parsed as shell syntax | Python `subprocess.run(["printf", "%s", "foo("])` was blocked by an unterminated-group error. `EmbeddedCommand` retains argv provenance; bounded scanning quotes literal data while policy matching retains raw spelling. Both hook and MCP evaluators use it. |
| Array values became executable elements | `values=(blocked)` matched a `Bash(blocked:*)` deny. The array-data context now preserves literals and scans actual nested command/process substitutions, with existing scanner bounds. |
| Cancellation after final ingestion write was hidden | A committed final file bypassed the early cancellation branch and directory completion returned success. Shared ingestion now preserves committed counters and reports the cancellation as a partial result, including final metadata/end-of-walk checks. |

Author context: these corrections enforce the existing design's distinction
between data and executable elements and its explicit partial-cancellation
contract. They are fixes within scope, not newly accepted exceptions.

Re-review caught actual-shell classification gaps in early argv fixes: nested
keyword calls before `shell=True`, valued/long shell options before `-c`, Python
continuations, keyword spreads and dynamic shell values. The final bounded
call-tail classifier accounts for quotes/comments/brackets and continued keyword
tokens, conservatively adds shell inspection for ambiguous kwargs, and always
retains raw argv policy matching. Regression cases preserve the old deny/ask
spellings and inspect actual shell-source arguments.

The final re-review inspected 12 files / 593 changed lines and approved all
corrections without remaining findings. It also approved the deterministic mtime
fixture: positive progress, the 32-file bound and high-ID eventual coverage are
unchanged. The earlier documentation/test supplement had no additional findings.

A later four-file harness/doc supplement was independently approved: configurable
`BENCH_COUNT`/`BENCH_TIME` preserve the six-sample/one-second defaults and leave
quality checks intact. Documentation distinguishes smoke measurements from
repeated comparisons and states unconditional scanner/containment checks.
The later search-fixture optimization was also approved: the negative counter
suppresses only repeated compaction during serial setup; normal admission, FTS
writes and vocabulary extraction remain, both final optimize statements are
checked, and the counter is restored. The baseline received the identical change;
timed indexing and quality fixture paths were not changed.

Reviewers inspected policy admission, limits, hook observations, subprocess
lifetime, ingestion bounds, stale scheduling, migrations, fetch freshness and
accounting, batch budgets, shared throttle, compatibility notes and ADR provenance.
They ran no tests/benchmarks under read-only review restrictions; runtime evidence
is separately recorded in [verification](../verification.md#task-20-final-verification-and-compatibility).
The embedded-command extractor remains heuristic, and D1–D5 remain explicit.

PAL's `gemini-3.1-pro-preview` external continuation failed with `API_KEY_INVALID`.
No external-model approval or corroborated finding is claimed. No systemic
P0/P1 findings require knowledge indexing; these findings and decisions are
already durable here. Fixes proceeded under the existing implementation request.
