# Task 20 isolated specification review

**Reviewer:** independent spec-reviewer, no authorship history; generic methodology
after Go profile detection (`review-spec/index.md` absent in the installed profile).
**Scope:** completed Tasks 1–19 including all lettered slices, plus current Task 20.
**Assessment:** CONFORMANT; no remaining P0–P3 findings, doc issues or ambiguities.

Reviewed design, implementation, tasks, audit and verification; README,
architecture, ADR-005/008/012/013/022 and proposed ADR-033/034/035. Traced the
following against code and representative regression/stdio/migration fixtures:

- Shell policies, prepared Read origins/anchors, legacy deny compatibility,
  external grants and admitted file handoff.
- Hook project/cwd selection, safe filenames, per-tool child-observation expiry,
  atomic consumption and session-only cleanup.
- Request-local runtime cwd, Rust compile/run separation and detached stream
  draining/discard with process-lifetime ownership.
- Literal source selectors, plaintext caps, bounded ingestion, Git/symlink
  selection, partial cancellation, persisted refresh scheduling and incarnation
  guards.
- CLI project/key ownership, filtering/error semantics, fetch validation/TTL and
  outcome accounting, batch provenance/serialized budget, shared search notices.
- Compatibility/rollback documentation, explicit proposed ADR status and retained
  source-kind/vault boundaries.

The reviewer found one documentation contradiction during the pass: README said
that omitting permission rules changed nothing, although execute-file containment
and scanner limits apply unconditionally. That sentence was corrected and
re-read; the final report has no unresolved finding. This correction aligns docs
with existing behavior and does not introduce a new design exception.

The reviewer executed no tests, benchmarks or repository scripts. Final Task 20
race/performance measurements and ledger completion were explicitly still running;
their results are recorded in [verification](../verification.md#task-20-final-verification-and-compatibility),
not established by this static review. Live host behavior and unavailable runtimes
were not independently exercised. D1–D5 remain intentional follow-ups.

No intentional SPEC_DEV/EXTRA_IMPL was found, so no deviations require knowledge
indexing. Corrections proceeded under the existing implementation request.
