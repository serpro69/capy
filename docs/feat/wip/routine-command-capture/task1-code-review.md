# Task 1 isolated code review

Review scope: Task 1 supervisor/process/lease proof only. Tasks 2–17 remain
pending. The independent `code-reviewer` reviewed the original 17-file patch
(2,013 changed lines). The external PAL review was attempted using
`gemini-3.1-pro-preview`; its configured provider rejected the API key, so it
produced no review. This is a single independent reviewer result.

## Findings and author corrections

1. **P1 — Startup failure waited indefinitely for a stopped helper.** Both
   failure paths closed pipes then synchronously called `cmd.Wait`, defeating
   the admission deadline. The reviewer recommended returning retained helper
   ownership alongside the error. **Author correction:** `Launch` now returns
   that handle after any post-spawn startup failure. The owner must retain and
   join it independently; cleanup protection stays with the stopped helper.
   `TestStoppedHelperDoesNotBlockAdmission` covers cancellation and timeout,
   lock retention, later joining and absence of command spawn.
2. **P2 — Shared lease validation could acquire rather than verify ownership.**
   An unrelated shared holder plus an unlocked incoming descriptor could pass
   contention probes. **Author correction:** validation now reads bounded Linux
   fdinfo for a descriptor-specific, whole-file FLOCK of the required mode. It
   never acquires/converts/unlocks the descriptor. The foreign-lifecycle fixture
   checks rejection and confirms validation did not acquire a lock.
3. **Coverage gap — Broken-notice test delayed Wait until completion.** The
   original Wait cancelled on notice read failure and could kill a live command.
   **Author correction:** notice failure now joins execution without cancelling;
   deadline, explicit control and owner liveness still apply. The fixture starts
   Wait concurrently and holds the command before allowing its final marker.

The reviewer found no further child-reaping, pipe-drain, private credential/FD,
or legacy preparation regression by static tracing. Tests were run by the
implementation session; the reviewer did not execute them under its read-only
role. Focused re-review of the current launcher, helper and fixtures returned
**APPROVE**, with no remaining actionable findings. The implementation session's
final race rerun passed, including the concurrent broken-notice hold.

All findings are recorded here; there is no deferred implementation fix from
this review. No duplicate knowledge indexing is needed because this record and
the supervisor proof preserve the applicable lessons.
