# Task 17 isolated specification review

An independent `spec-reviewer` read the design, authoritative contracts,
implementation plan and tasks before tracing the implementation. Scope includes
Tasks 1–16 and current Task 17. Generic feature Markdown activates no profile;
the Go profile has no installed review-spec checklist.

## Findings and corrections

1. **P2 SPEC_DEV — head-only previews.** The ordinary execution path returned
   only the first merged projection page, while design §7 requires safe head/tail
   previews. It now combines a bounded head with per-stream sanitized tails.
   `ReadProjectionTail` reverse-scans finalized projection rows and delegates
   expiry, version and pending-coverage checks to the ordinary reader. It never
   reads raw text or creates indexes. Store/server regressions exercise late
   failure diagnostics, protected spans, lazy indexing and expiry.
2. **P2 SPEC_DEV — cancellation during dispatch.** Cancellation after the first
   context check could make `Dispatch` fail and be classified as `spawn_failed`.
   The error branch now rechecks cancellation before finalizing an unstarted
   child, preserving `cancelled` with null child status. Existing cancellation
   integration/race coverage applies; a deterministic writer-lock reproduction
   of this particular interleaving was not run.

## Re-review and limits

The independent follow-up returned **CONFORMANT**, with no remaining P0–P3
finding, documentation inconsistency or ambiguity in the reviewed changes.
It also traced nested-program policy composition, byte-preserving persistence
and handoff, conservative quota accounting and live cleanup-evidence retention.
The reviewer executed no tests or benchmarks.

Actual Codex/Claude host trust, hook activation, current loaded MCP schemas,
client wait behavior and inherited host sandbox behavior were subsequently
verified through real host sessions. Hook subprocess and stdio fixtures alone
cannot establish those properties. See [Task 17 verification](task17-verification.md)
and [selected live evidence](task17-live-acceptance.json).

## Final live-evidence review

The independent closeout review initially identified a P2 acceptance gap:
short commands specifying `wait_ms=30000` did not exercise the default 25-second
attached wait. Both trusted hosts were resumed for a single 27-second command
with `wait_ms=25000` and `timeout=40000`. Each returned a healthy running response
before completion, followed by complete same-run recovery of both markers and
exit zero (Codex 27,011 ms/44 bytes; Claude 27,012 ms/46 bytes). No smaller wait
was needed. Exact inputs and timestamps are retained; approval delay is excluded
from execution latency.

The reviewer also requested traceable Codex call inputs and corrected tracking.
The evidence now retains the relevant code-mode call inputs, including worktree
cwd and wait parameters, and records the user-approved fixture-only trust change.

Final verdict: **CONFORMANT — ready to mark Task 17 done**, with no remaining
P0–P3 finding, documentation issue or ambiguity. The review executed no additional
tests or host calls. Scope remains the recorded Linux/host versions, supplemented
by the broader compiled test matrix. No intentional spec deviation was accepted
or indexed.
