# Vault session deletion

> Design: [design.md](./design.md)
>
> Implementation: [implementation.md](./implementation.md)
>
> Status: pending
>
> Not Doing: bulk deletion, physical erasure, local or synchronized undelete
>
> Design review: [consolidated findings and evidence](./review-resolution.md)

## Task 1. Enforce the deletion record at archive write boundaries

**Status:** pending · **Depends on:** — · **Size:** M · **Can run in parallel with:** —

**Docs:** [Local deletion and archive writes](./implementation.md#local-deletion-and-archive-writes)

- [ ] Add `0008_session_deletions` and the matching fresh schema. Define milestone 4 and preserve existing platform/encoding stamps. Keep the public reader maximum at 3 and the CLI delete behavior as shipped during this preparatory task.
- [ ] Implement the internal remembered-delete operation. Capture the effective project and atomically remove the archive and its indexes, record its pair, and stamp 4. In an isolated fixture verify rollback and independent child sessions.
- [ ] In `Import`, check every selected UUID before reading transcript or sidecars. On the open fixture handle, compare preview and apply for an unopenable marked candidate beside a readable new session. Assert `excluded` / `deleted` and neighbor admission.
- [ ] Guard full writes in their transaction and check replacement row counts before child writes. Preserve per-input outcomes through commit, batch rollback and individual retries.
- [ ] Cover a mixed stale-index batch with one remembered deletion and one survivor, including the retry path. Check moved Codex rollouts with deletion before the hint update and, for a stale moved rollout, between hint update and rebuild. Assert one final outcome per candidate and preserve reindex's survivor count.

## Task 2. Propagate deletions through a vault merge

**Status:** pending · **Depends on:** Task 1 · **Size:** M · **Can run in parallel with:** —

**Docs:** [Cross-machine merge and reporting](./implementation.md#cross-machine-merge-and-reporting)

- [ ] Pin source version/schema, marker pairs, UUIDs, payloads and metadata to one read transaction. Commit a source delete between enumerations in a two-connection fixture and verify this merge and the next.
- [ ] Apply selected source pairs verbatim and honor destination markers for live records and metadata-only writes. Verify A→B→C with differing checkout paths/labels, both filtered and full merges, and an unrelated UUID. Also cover independently originated pairs.
- [ ] Use bounded index cleanup for distinct live or first-remembered UUIDs. Seed legacy FTS rows for a missing parent and prove its first incoming marker removes them while preserving a neighboring UUID. Require zero FTS deletes on repeat and when an already-marked absent UUID gains a different project pair.
- [ ] Compare preview, apply, and repeat with two pairs for one live UUID and a second UUID sharing its prefix. Check distinct archive and pair counts, full IDs, and marker-only output.
- [ ] Verify planning, metadata-only reconciliation and post-commit reporting when a destination row disappears. Force an error or cancellation after one committed batch, verify partial CLI output and nonzero exit, then retry and verify the totals.
- [ ] Raise the reader maximum to 4 with complete merge handling and updated help. Verify reopen/forward after the first incoming marker, version 3 for ordinary platform writes, and 5 for future-version refusal. Verify that an inconsistent version-4 source missing its marker table fails before changes, and a supported legacy source cannot revive a deleted UUID.

## Task 3. Enable local recording and its opt-out

**Status:** pending · **Depends on:** Task 2 · **Size:** M · **Can run in parallel with:** —

**Docs:** [Local configuration](./implementation.md#local-configuration)

- [ ] Enable remembered deletion by default with the config opt-out and permanence notice in the same change. Check the preview, full UUID, declined confirmation and `--yes` for each mode.
- [ ] Verify configuration precedence, the invocation project's `--project-dir` override and suppression of earlier recorded deletions after a config change.
- [ ] Interleave an opted-out delete between replacement planning and flush in import and merge. With no sidecars or overrides, assert `skipped` / `session disappeared`, zero session and all three FTS row counts, and successful admission of a fresh complete copy on a later import.

## Task 4. Verify the operator workflow and document rollout

**Status:** pending · **Depends on:** Task 3 · **Size:** S · **Can run in parallel with:** —

**Docs:** [Operator documentation and verification](./implementation.md#operator-documentation-and-verification)

- [ ] Use `kk:test` for the encrypted workflow with CLI import and merge. Reopen a marked vault through the normal server startup sweep and verify its excluded and admitted counts. Include receipt of a deletion when local recording is disabled.
- [ ] Use `kk:document` to update the CLI help, README, vault reference, `AGENTS.md` and `docs/architecture.md` with the implemented behavior and reader milestone. Run the quality benchmark for the shared index-path change.
- [ ] Run the Go `kk:review-code` and `kk:review-spec` checks against the agreed behavior and resolve findings.

## Dependency graph

```text
Task 1 → Task 2 → Task 3 → Task 4
```
