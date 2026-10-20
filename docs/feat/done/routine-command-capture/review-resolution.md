# Design review finding verification and resolution

> Date: 2026-10-16
> Scope: the two user-supplied reviews of routine-command-capture
> Verified code revision: `55298c51ca6cdc6aa3fdf2382e9665a15892608f`
> Artifacts: [design](design.md), [implementation](implementation.md), [tasks](tasks.md)
> Status: valid findings incorporated; implementation remains pending

This is the historical disposition of the first two supplied reviews. Its section
and task numbers refer to that revision. A subsequent full reread and authorized
fixes are recorded in [readiness-review.md](readiness-review.md); current public
contracts live in [contracts.md](contracts.md), and the implementation plan now
has 17 resliced tasks. In particular, v1 rotation now explicitly supports reset
rather than promising an unimplemented log-preserving rekey path.

This is an author verification/disposition pass, not a new independent design
review. Code paths, current documents and filesystem/Git metadata were inspected;
the sandbox claim was checked against current official documentation. Proposed
runtime tests below have not been run because the feature is not implemented.

There were 17 reported findings. Sixteen are valid design gaps, including one
duplicate concurrency report: 15 distinct corrections. The predecessor-move
finding is a future maintenance risk, not a current broken link, and was not used
to relocate or rewrite documents.

## Per-finding dispositions

| ID | Supplied finding | Verdict and verified evidence | Resolution |
| --- | --- | --- | --- |
| R1.1 | P1 HTTP routing bypasses routine capture | **Valid; fix required.** `routeBash` returns from root/child HTTP handling before subsequent classification. Both absence-of-evidence allow and evidence-backed native-fallback text contradict whole-request capture. | Design §9: whole-request security → mandatory routine gate → HTTP-only routing. Mixed routine denial neither consumes HTTP evidence nor recommends native fallback. Implementation §13 / Task 11 add root/child and evidence-state fixtures. |
| R1.2 | P1 redaction across boundaries undefined | **Valid; fix required.** `StripSecrets` takes one independent string; token regexes and private blocks can span arbitrarily many events/pages. A bounded overlap alone cannot cover unbounded patterns. | Design §6 defines bounded per-stream recognizers, conservative overflow/EOF handling, encrypted projection/checkpoints and arbitrary-cursor behavior. Tasks 3/4/7 cover events, long values, FTS, previews, pages and lost state. |
| R1.3 | P2 batch versus owner capacity | **Valid; fix required.** Existing `maxBatchConcurrency=8`; the draft's owner default is 4, with no queue/slot contract. | Design §7 defines shared slots, bounded atomic pending admission, effective concurrency, fairness, dispatch cutoff and pending cancellation; Tasks 5/9 verify an eight-child batch with two standalone jobs. |
| R2.1 | P1 CLI store outside sandbox writable roots | **Valid; fix required, with scoped premise.** Default workspace-write does not grant home-cache writes, and native spawned commands inherit the sandbox. A claim that every MCP server is unsandboxed is unnecessary and is not adopted. | Design §8 selects owner-local `.capy/runs/`, with no implicit alternate store. Tasks 2/4/13 require actual sandboxed admission, excluded-owner diagnostics and generated ignore coverage. Setup does not modify permissions. |
| R2.2 | P2 Claude enforcement scope unclear | **Valid; fix required.** The original success statement was unqualified while routing/tasks/acceptance were Codex-only. | Design §§1/9/13/14 and task metadata explicitly make Claude routine capture guidance-only in v1; shared capture still applies to its MCP tools. Task 11 verifies accurate platform guidance and unchanged Claude routing. |
| R2.3 | P2 max_active versus batch cap | **Valid duplicate of R1.3.** Same missing scheduling interaction. | One shared scheduler contract; no independent contradictory clamping rule. Requested, effective and current concurrency are reported separately. |
| R2.4 | P2 long deadlines and slot exhaustion lockout | **Valid recovery/default gap; fix required.** Four unfinished runs can saturate an MCP owner, and the draft lacked actionable capacity errors. A separate CLI owner is not necessarily saturated, so this is not proof that every interface is locked out. | Five-minute proposed default deadline, 25-second response wait with rationale, explicit longer deadlines, bounded active-ID/age/deadline/cancel diagnostics, and recovery actions outside execution slots. No silent oldest-job cancellation. Tasks 5/9/13 exercise exhaustion and recovery. |
| R2.5 | P2 key rotation strands the disposable cache | **Valid operator-recovery gap; fix required.** `capy encrypt` rotates only the selected knowledge DB and intentionally has its own prompted-old/new-key contract. A changed credential cannot authenticate old run files. | Explicit `capy runs reset --discard`, keyless but quiescence-locked and narrowly destructive, named in wrong-key and rotation guidance. Task 12 verifies reset after rotation and active-owner refusal. Multi-cache automatic rekey is not added. |
| R2.6 | P2 eager dual-FTS cost unexamined | **Valid design ambiguity/cost gap; fix required.** The draft did not choose when indexing happens or clearly account for posting overhead. No measured regression or universal index-size multiplier is claimed. | Design §8 specifies lazy indexing on search/batch queries/intent, resumable coverage, transactional index quota rollback, dbstat/conservative accounting modes and cost benchmarks. Unsearched no-intent runs produce no FTS rows. Tasks 6/7/13 cover this. |
| R2.7 | P2 selected project defined only in plan | **Valid contract placement/test gap; fix required.** CLI `commandProjectDir` normally uses project discovery, while Codex launch validates an installation owner; nested owners can differ from Git root. | Design §8 now owns the complete selection rule. Invalid chains cannot fall back; cwd/knowledge path do not select storage. Acceptance and Tasks 2/4/13 explicitly cover nested owners, subdirectories and sibling worktrees. |
| R2.8 | P3 executor.timeout is currently unwired | **Valid; fix required.** Non-test references show config default/merge but no runtime consumer; execute/file hardcode 30,000 ms, batch 60,000 ms. | Design §10 explains first-time wiring and exact precedence/units: request ms → configured seconds × 1,000 → 300-second default, with presence/overflow validation. Tasks 5/12 test/document the compatibility change. |
| R2.9 | P3 not_started missing from state enum | **Valid; fix required.** The draft called it an outcome without defining that field or including it in `state`. | Design §5 makes it a state with null run ID/child exit. It distinguishes admitted `start_failed`; queued/skipped/cancelled-before-spawn children also have explicit semantics. Task 3 verifies the distinction. |
| R2.10 | P3 execute-file wait_ms absent from tasks | **Valid; fix required.** Design §10 promises it, while the execute-file implementation slice did not. | Implementation §10 and Task 8 explicitly add schema/help/shared lifetime support and `TestExecuteFileWait` for late output, deadline, cancellation and admission. |
| R2.11 | P3 local checkout described as submodule | **Valid; fix required.** `git ls-files --stage .gitmodules context-mode` returns no entries; `git check-ignore context-mode` matches and `context-mode/.git` is a directory. | Design §3 calls it a gitignored local checkout and identifies pinned upstream links as sufficient when that checkout is absent. |
| R2.12 | P3 random owner-lock files accumulate | **Valid lifecycle gap; fix required.** The draft assigned random owner locks without reclamation. The repository's NEVER-unlink rule explicitly applies to observation locks, not necessarily every imaginable lock; nevertheless unsafe unlink/recreate must not be introduced. | Design §7 uses 64 fixed permanent slot files plus lifecycle.lock, generation IDs in metadata, bounded slot admission and protected reuse. Task 5 validates churn, crash, reuse and exhaustion; reset preserves these files. |
| R2.13 | P3 predecessor move will break links | **Not a current defect; no link relocation required.** `docs/feat/wip/codex-hooks/{design,tasks}.md` exist and the links in scope resolve. No move was requested or scheduled in this change. | Preserve current relative links. Any later move to done must update inbound links in that same move and run link checks. Moving a completed feature or pinning all local links now would be unrelated scope. |
| R2.14 | P3 healthy running response and HTTP observations | **Valid missing policy decision; fix required.** `successfulPostToolUse` currently treats a non-error tool response as successful, so new running results need an explicit intended contract. | Design §9 says healthy running execute proves tool availability and counts; unstarted/queued/failed/incomplete states do not. Late failure does not rewrite past availability; status/read never refreshes TTL. Task 11 adds root/child observation fixtures. |

## Evidence anchors

- [HTTP early returns](../../../../internal/hook/pretooluse.go), functions
  `routeBash` and `routeCodexMainHTTP`.
- [Whole-string redaction](../../../../internal/sanitize/sanitize.go),
  `privateTagRe`, `secretPatterns` and `StripSecrets`. A synthetic private block
  whose opening tag is in one page and contents in another demonstrates why
  sanitizing the second page independently lacks required state. Unbounded
  token and whitespace regexes also defeat an arbitrary fixed overlap.
- [Batch scheduling](../../../../internal/server/tool_batch.go),
  `maxBatchConcurrency`, serial shared time budget and parallel worker pool.
- [Timeout configuration](../../../../internal/config/config.go),
  [merge](../../../../internal/config/loader.go),
  [execute](../../../../internal/server/tool_execute.go) and
  [execute-file](../../../../internal/server/tool_execute_file.go).
- [Knowledge rotation](../../../../cmd/capy/encrypt.go), `runEncryptWithPrompts`;
  [owner selection](../../../../internal/platform/codex_owner.go) versus
  [ordinary CLI project selection](../../../../cmd/capy/knowledge.go).
- [Observation acceptance](../../../../internal/hook/posttooluse.go),
  `successfulPostToolUse`; [permanent observation lock](../../../../internal/hook/observations.go).
- [Existing SQLite usage inspection](../../../../internal/store/diskusage.go)
  explicitly distinguishes dbstat coverage from fallback accounting. The new
  index accounting is a proposed run-store adaptation, not an existing guarantee.
- Official [sandbox behavior](https://learn.chatgpt.com/docs/sandboxing) and
  [workspace-write protected paths](https://learn.chatgpt.com/docs/agent-approvals-security#protected-paths-in-writable-roots),
  retrieved 2026-10-16: spawned shell commands inherit confinement; ordinary
  workspace access does not authorize arbitrary home-cache writes. Project-local
  storage still needs actual admission testing under the effective workspace.

## Verification of this document change

All implementation tasks remain unchecked. This pass changes only design
documents and the associated dispositions. Local Markdown targets/anchors,
13 task dependency/parallel records, all 17 disposition IDs, stale-default scans
and `git diff --check` passed.
Runtime, race, sandbox, redaction and performance tests are named in the plan for
the implementation phase; no new test result or independent re-review is claimed.
