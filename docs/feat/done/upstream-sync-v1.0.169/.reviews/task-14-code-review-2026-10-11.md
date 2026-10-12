# Task 14 isolated code review — 2026-10-11

Reviewers: independent `code-reviewer` agent with no implementation-session
history, and PAL `gemini-3.1-pro-preview`. Scope: the new CLI indexing entry
point and preservation of completed tasks. Terminal search and Tasks 15–20
remain pending; D2's atomic policy/open handoff remains deferred.

**Final code-reviewer assessment: APPROVE. No unresolved P0–P3 findings.**

The reviewer examined seven changed files (711 changed lines after the fix),
including untracked CLI implementation/tests and the relevant unchanged
target-selection, ingestion and store-close paths. Go profile: `.go` extension.
Checklists: SOLID, removal, security, code style, error handling, injection and
concurrency. The review was static; author-run verification is recorded in
[verification.md](../verification.md#task-14-terminal-knowledge-indexing).

## Resolved code-reviewer finding

**P2 — cancellation during the final write can return exit 0.**
Location: `cmd/capy/index.go`, `runIndex` after `IndexPath` and deferred cleanup.
Profile: Go; checklist: concurrency. Confidence: 99%.

The store's transaction/checkpoint path uses its own context. A signal arriving
during the last write could therefore leave the command context canceled while
ingestion and close returned success, violating the documented exit contract.

Author change: join `ctx.Err()` after ingestion before reporting directory
outcomes, and after deferred store close, without duplicating an existing
cancellation error. Keep committed content and finish checkpointing.
`TestIndexCommandLateCancellationRetainsCommits` checks both file and directory
commands after the final commit; the existing real SIGTERM fixture exercises
cancellation during a nested Git lookup. The reviewer re-read the fix/tests and
approved them. Final focused race tests pass.

## External review

PAL's native finding: **LOW — Windows test portability concern**, recommending
replacing `syscall.SIGTERM` with `os.Interrupt` in the signal regression.

Author context: Windows support is outside this sync's scope and the existing
filesystem implementation uses Unix-specific flags. Moreover, the installed Go
source at `/usr/local/go/src/os/exec.go:350` explicitly states that sending
`os.Interrupt` on Windows is not implemented. The proposed substitution would
not provide the claimed portability. The test keeps SIGTERM to exercise the
documented supported-platform behavior; no platform-expansion change is made.

PAL's positive summary also misstated limit exits as 1. The design, code, help
and tests consistently use exit 0 for limit-stopped incomplete scans and exit 1
for operational/validation failures. No implementation change was needed for
that summary statement. PAL reviewed the pre-cancellation-fix snapshot; the
independent code reviewer verified the final fix.

## Indexing

No P0/P1 findings or removal candidates. No findings to index: the P2 cause,
resolution and external-review context are recorded here and in the feature
implementation notes.
