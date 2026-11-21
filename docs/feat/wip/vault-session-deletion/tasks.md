# Vault session deletion

> Design: [design.md](./design.md)
>
> Implementation: [implementation.md](./implementation.md)
>
> Status: done
>
> Not Doing: bulk deletion, physical erasure, local or synchronized undelete
>
> Design review: [consolidated findings and evidence](./review-resolution.md)

## Task 1. Enforce the deletion record at archive write boundaries

**Status:** done · **Depends on:** — · **Size:** M · **Can run in parallel with:** —

**Docs:** [Local deletion and archive writes](./implementation.md#local-deletion-and-archive-writes)

- [x] Add `0008_session_deletions` and the matching fresh schema. Define milestone 4 and preserve existing platform/encoding stamps. Keep the public reader maximum at 3 and the CLI delete behavior as shipped during this preparatory task.
- [x] Implement the internal remembered-delete operation. Capture the effective project and atomically remove the archive and its indexes, record its pair, and stamp 4. In an isolated fixture verify rollback and independent child sessions.
- [x] In `Import`, check every selected UUID before reading transcript or sidecars. On the open fixture handle, compare preview and apply for an unopenable marked candidate beside a readable new session. Assert `excluded` / `deleted` and neighbor admission.
- [x] Guard full writes in their transaction and check replacement row counts before child writes. Preserve per-input outcomes through commit, batch rollback and individual retries.
- [x] Cover a mixed stale-index batch with one remembered deletion and one survivor, including the retry path. Check moved Codex rollouts with deletion before the hint update and, for a stale moved rollout, between hint update and rebuild. Assert one final outcome per candidate and preserve reindex's survivor count.

**Execution context**

- 2026-11-21, observed at `a6dd4f1`. Import queues full and index-only writes separately. Both flush paths need committed per-input outcomes. The existing reader maximum and CLI deletion mode stay in effect for this task.
- 2026-11-21, verified. Focused race tests passed for deletion, digest planning, moved Codex files, and batch retries. Independent review approved after adding the post-read marker/digest snapshot and its regression test. `make bench-quality` passed. The stored master comparison flagged differences also present at `a6dd4f1`. A fresh encrypted benchmark at that revision passed comparison with the candidate.

## Task 2. Propagate deletions through a vault merge

**Status:** done · **Depends on:** Task 1 · **Size:** M · **Can run in parallel with:** —

**Docs:** [Cross-machine merge and reporting](./implementation.md#cross-machine-merge-and-reporting)

- [x] Pin source version/schema, marker pairs, UUIDs, payloads and metadata to one read transaction. Commit a source delete between enumerations in a two-connection fixture and verify this merge and the next.
- [x] Apply selected source pairs verbatim and honor destination markers for live records and metadata-only writes. Verify A→B→C with differing checkout paths/labels, both filtered and full merges, and an unrelated UUID. Also cover independently originated pairs.
- [x] Use bounded index cleanup for distinct live or first-remembered UUIDs. Seed legacy FTS rows for a missing parent and prove its first incoming marker removes them while preserving a neighboring UUID. Require zero FTS deletes on repeat and when an already-marked absent UUID gains a different project pair.
- [x] Compare preview, apply, and repeat with two pairs for one live UUID and a second UUID sharing its prefix. Check distinct archive and pair counts, full IDs, and marker-only output.
- [x] Verify planning, metadata-only reconciliation and post-commit reporting when a destination row disappears. Force an error or cancellation after one committed batch, verify partial CLI output and nonzero exit, then retry and verify the totals.
- [x] Raise the reader maximum to 4 with complete merge handling and updated help. Verify reopen/forward after the first incoming marker, version 3 for ordinary platform writes, and 5 for future-version refusal. Verify that an inconsistent version-4 source missing its marker table fails before changes, and a supported legacy source cannot revive a deleted UUID.

**Execution context**

- 2026-11-21, observed at `4172ad1`. Merge reads the source and destination metadata in separate pool queries. Bind source reads to one transaction, plan from a consistent destination view, and publish each batch's results after commit.
- 2026-11-21, verified. Race tests covered filtered forwarding, snapshot interleavings, orphan cleanup, cancellation after commit, legacy refusal/suppression and CLI partial failure/retry. The independent review identified platform attribution and CLI signal handling, both corrected and retested. An earlier full vault race invocation timed out in the optional real-corpus Codex canary. Synthetic race suites cover this feature.

## Task 3. Enable local recording and its opt-out

**Status:** done · **Depends on:** Task 2 · **Size:** M · **Can run in parallel with:** —

**Docs:** [Local configuration](./implementation.md#local-configuration)

- [x] Enable remembered deletion by default with the config opt-out and permanence notice in the same change. Check the preview, full UUID, declined confirmation and `--yes` for each mode.
- [x] Verify configuration precedence, the invocation project's `--project-dir` override and suppression of earlier recorded deletions after a config change.
- [x] Interleave an opted-out delete between replacement planning and flush in import and merge. With no sidecars or overrides, assert `skipped` / `session disappeared`, zero session and all three FTS row counts, and successful admission of a fresh complete copy on a later import.

**Execution context**

- 2026-11-21, observed at `539bcc8`. The delete command already resolves a full UUID before confirmation. Load the invocation project's deletion setting once, display the corresponding notice, and pass that same value to the store.
- 2026-11-21, verified. Encrypted race tests passed for configuration precedence, both CLI previews and confirmation paths, and stale sidecar-free import and merge replacements. The vault and TUI race suites passed with the optional local-corpus canaries excluded. All packages compiled with FTS5. The quality benchmark passed comparison against a fresh run at `a6dd4f1`. Independent Task 3 source review approved. Task 4 owns startup verification and rollout documentation.

## Task 4. Verify the operator workflow and document rollout

**Status:** done · **Depends on:** Task 3 · **Size:** S · **Can run in parallel with:** —

**Docs:** [Operator documentation and verification](./implementation.md#operator-documentation-and-verification)

- [x] Use `kk:test` for the encrypted workflow with CLI import and merge. Reopen a marked vault through the normal server startup sweep and verify its excluded and admitted counts. Include receipt of a deletion when local recording is disabled.
- [x] Use `kk:document` to update the CLI help, README, vault reference, `AGENTS.md` and `docs/architecture.md` with the implemented behavior and reader milestone. Run the quality benchmark for the shared index-path change.
- [x] Run the Go `kk:review-code` and `kk:review-spec` checks against the agreed behavior and resolve findings.

**Execution context**

- 2026-11-21, observed at `7443005`. The CLI can originate and receive deletion records. The server opens the vault in its background startup sweep and logs import outcomes per platform. Verify that handoff through the CLI and a fresh server process using temporary encrypted vaults.
- 2026-11-21, verified. The CLI imported a session, propagated its deletion to an opted-out peer, and previewed a subsequent import. A fresh `capy serve` process excluded that session and admitted its neighbor. The focused FTS5 race run passed with a symlinked temporary directory. `make bench-quality` and `qualstat` passed against the saved Task 3 report.
- 2026-11-21, verified after review. Identical duplicate transcripts now check the post-read deletion state. A failed or interrupted merge identifies queued work and preserves earlier committed counts. Regression tests cover cancellation after the first 50 deletions, rollback of a two-UUID batch, retry, and preservation of reader version 4 after a later Codex write. Independent code and spec reviewers approved the corrections.
- 2026-11-21, final verification. The vault and config race suites passed with the optional local-corpus canaries excluded. The final CLI and vault regression run passed with FTS5, race instrumentation in the CLI subprocess, and a symlinked temporary directory. The uncached quality benchmark passed comparison with Task 3. Deployment remains an operator action: upgrade and restart all shared-vault writers before the first local or incoming deletion.

## Dependency graph

```text
Task 1 → Task 2 → Task 3 → Task 4
```
