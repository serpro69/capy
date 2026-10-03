# Corroboration and resolution of the supplied design review

> Date: 2026-10-03
> Reviewed baseline: `d4f01854286e59e2ebf7354ca610aba34db9cbeb`
> Scope: design corrections only; feature implementation remains pending
> Design: [design.md](../design.md)
> Plan: [implementation.md](../implementation.md)
> Tasks: [tasks.md](../tasks.md)

All nine findings identify something worth addressing. Two numerical claims need
qualification against the wider/current corpus, and the first suggested remedy
alone would retain the source-line tie problem. The verdicts below distinguish
those qualifications from the validity of each requested correction.

## F1 — Assistant fragmentation and search landing (P1)

**Verdict: valid; addressed, with a correction to the proposed remedy.**

`transcriptMessages` in `internal/vault/transcript.go` keeps one assistant body
and appends launch markers. `rowForLine` in `internal/vault/tui/render.go` chooses
the last message at or before the source line. The proposed fragmentation would
duplicate assistant headings and permit a tall body's earlier text to scroll out
of view. Appending input markers alone still leaves the last marker winning the
same tie; the launch precedent does not solve that part.

The revised design uses ordered input placeholders in one body, followed by
input/launch markers. An explicit `SourceAnchor` on that owning body wins within
its source-line group; unmarked groups retain current behavior. Task 5 pins a tall
body/early-phrase viewport regression and preserves ordinal-based resize/return.
No fragment machinery or new input role remains in the plan.

## F2 — No Codex real-corpus output digest (P2)

**Verdict: valid; addressed as a pre-implementation prerequisite.**

`codex_canary_test.go` checks message counts, user rows, headings, and marker
shapes. `parity_canary_test.go` hashes complete reader output, but discovers
Claude sessions and includes viewer output that this feature intentionally changes.

New Task 1 specifies a Codex input/output SHA-256 baseline over complete scanner,
text, and Markdown outputs before production edits, with same-input comparisons,
live-file accounting, required category coverage, and aggregate verification
evidence. It reuses baseline I/O rather than the Claude reader set. This pass
specifies the gate; it does not claim that an unimplemented feature passed it.

## F3 — Direct-patch success boilerplate (P2)

**Verdict: valid; addressed with lossless collapse.**

`viewerToolMessage` currently lets `Diff` replace the result body. Removing it
would expose the small success body inline. The fresh corpus scan corroborates
21/21 legacy events matching direct calls in call→event→output order, with all
21 outputs structured and successful under the existing decoder's rules.

The decoder will carry that positive outcome fact as `ReportedSuccess`. A
completed associated event plus that fact forces a compact `apply_patch · output`
marker even for a small result. Full output remains available to find/copy and
expansion. No success-string equality heuristic or diagnostic suppression is
introduced; unsuccessful/uncertain results keep the existing result policy.

## F4 — Unspecified visible labels (P2)

**Verdict: valid; addressed.**

The prior plan required assertions for undefined failure labels and inherited
"Tool result" as the change-card heading. The design now specifies summaries for
every completion/data state, operation/path/count section syntax, detail headings,
and the absence of an exec-input excerpt. An optional display-only `Heading`
override supplies those headings through existing `RoleTool` details in normal
and find rendering. Empty overrides preserve existing messages and goldens.

## F5 — Null move destinations and missing status (P3)

**Verdict: compatibility rule valid; all-null numerical premise not corroborated
for the wider/current corpus. Addressed.**

The fresh scan found 887 update records: 881 null destinations and 6 nonempty
destinations, not an all-null set. All 842 paginated items did contain status
across the observed ten CLI versions. The absent-status rule remains a defensive
compatibility rule, not a description of an observed failure.

The model/adapter contract now explicitly maps absent/null destinations to no
move, preserves real destinations, and diagnoses empty/wrong-type destinations.
Task 3 requires all cases and read-only checks against observed moves.

## F6 — Broader adverse and legacy examples (P3)

**Verdict: valid coverage improvement; the original bounded sample statement was
accurate for that sample. Addressed.**

The wider/current scan found 9 failed items and 1 declined item, plus 13 files
with 21 legacy patch events from CLI 0.124–0.130. This differs from the supplied
five-failure count but confirms the missing categories are locally available.
Research now separates the dated narrow sample from the wider scan. Tasks 1, 3,
4, and 6 require real category coverage in addition to portable synthetic tests.

## F7 — Normalized-event equality (P3)

**Verdict: valid ambiguity; addressed.**

No duplicate IDs or mixed-family files occurred in the fresh scan, so current
data cannot settle this policy. The design explicitly compares normalized status,
sorted per-file original data, stdout/stderr, and location-independent diagnostic
reasons. It excludes timestamp/location/family/object order and normalizes only
specified optional-null cases. Task 3 tests each included/excluded dimension,
including output-only and diagnostic-only conflicts. Rendered-diff equality is
insufficient and is explicitly excluded as the implementation shortcut.

## F8 — Stale prior design and fragile performance link (P3)

**Verdict: valid, with the link a future-move risk rather than a currently broken
target. Addressed.**

The completed Codex-session design claimed byte-identical `FileChange` duplicates.
A superseding correction now explains the observed nested-call exception and
links this proposed replacement without claiming it is implemented.

The old performance link resolved under `wip` today. The new plan instead links
the source-owned find/latency harnesses and states the opt-in command, sample
count, workload distinction, and gate. Moving the earlier feature docs cannot
break those links.

## F9 — Dependency and task sizes (P3)

**Verdict: valid dependency error and justified scope concern; addressed.**

The old input task used response IDs and did not require legacy event handling;
file sharing was not a semantic dependency. The broad original paginated task
also combined the full operation grammar, states, identities, and first viewer
integration. Calling all that mechanical understated its scope.

The new plan has six bounded tasks: baseline capture; completed paginated updates;
remaining paginated operations/resilience; legacy/direct reconciliation; input
disclosure; final verification. Input disclosure depends only on the baseline.
Reusing `RoleTool`, one body, and existing frames/corpora removes the original new
role/fragment work. The plan lists actual shared-file coordination and defines
which task first introduces the common heading helper. All implementation tasks
remain pending.

## Validation of these corrections

Evidence was collected by direct source inspection and a read-only scan of 476
local rollouts. The [research](../research.md#wider-corpus-corroboration--2026-10-03)
contains aggregate counts and limitations. No raw private session was copied into
the repository. The six nonempty move destinations were also checked separately:
all six are strings. No production source, test, or dependency file was changed.

### Independent correction review

- An isolated `code-reviewer` reviewed all nine corrections against the current
  source and approved with no P0–P3 findings. It did not rerun the private corpus
  scan or execute pending feature tests.
- PAL (`gemini-3.1-pro-preview`) found no high/medium contradictions and supplied
  two LOW implementation notes. Both were checked against the source and made
  explicit in the plan; they require no further design decision:
  - **Event completion status lookup:** Task 4 now requires a single viewer-local
    canonical-event-ID→state map for result collapse, avoiding per-result scans.
  - **Preserving call-part order for mixed markers:** Task 5 now names the
    launch-only helper's replacement with an ordered detail-message list and
    retains sidecar-index registration only for subagent elements.
- No new systemic P0/P1 finding remained to index. The original findings and
  their resolutions are retained here rather than duplicated in knowledge notes.

### Checks

Local-link/anchor checks, pending-task metadata, acyclic dependency and parallel
claims, and `git diff --check` passed. Runtime behavior, real-corpus output parity,
and performance remain unverified until the pending implementation tasks run.
