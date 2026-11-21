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

## Task 1. Keep a deleted UUID out of disk imports

**Status:** pending · **Depends on:** — · **Size:** M · **Can run in parallel with:** —

**Docs:** [Local deletion and archive writes](./implementation.md#local-deletion-and-archive-writes)

- [ ] Add `0008_session_deletions` and the matching fresh schema. Set the reader maximum to 4, preserve the existing platform/encoding stamps and update the pairing and future-version fixtures in the same change.
- [ ] Capture the effective project on explicit remembered delete. Atomically remove the live archive and its indexes, record its pair, and stamp 4. Verify rollback, reopen, and independent child sessions.
- [ ] In `Import`, check every selected UUID before reading transcript or sidecars. Compare preview and apply for an unopenable marked candidate beside a readable new session. Assert `excluded` / `deleted`, neighbor admission, and corresponding sweep counts.
- [ ] Guard full writes in their transaction and check replacement row counts before child writes. Preserve per-input outcomes through commit, batch rollback and individual retries.
- [ ] Cover a mixed stale-index batch with one remembered deletion and one survivor, including the retry path. Check moved Codex rollouts with deletion before the hint update and, for a stale moved rollout, between hint update and rebuild. Assert one final outcome per candidate and preserve reindex's survivor count.

## Task 2. Expose the local delete opt-out

**Status:** pending · **Depends on:** Task 1 · **Size:** M · **Can run in parallel with:** Task 3

**Docs:** [Local configuration](./implementation.md#local-configuration)

- [ ] Resolve the config at delete time. Verify the permanence and propagation notice for remembered deletes, the one-time-delete preview, full UUID, cancellation, and `--yes`.
- [ ] Verify configuration precedence and suppression of earlier recorded deletions after a config change.
- [ ] Interleave an opted-out delete between replacement planning and flush in import and merge. With no sidecars or overrides, assert `skipped` / `session disappeared`, zero session and all three FTS row counts, and successful admission of a fresh complete copy on a later import.

## Task 3. Propagate deletions through a vault merge

**Status:** pending · **Depends on:** Task 1 · **Size:** M · **Can run in parallel with:** Task 2

**Docs:** [Cross-machine merge and reporting](./implementation.md#cross-machine-merge-and-reporting)

- [ ] Pin source version/schema, marker pairs, UUIDs, payloads and metadata to one read transaction. Commit a source delete between enumerations in a two-connection fixture and verify this merge and the next.
- [ ] Apply selected source pairs verbatim and honor destination markers for live records and metadata-only writes. Verify A→B→C with differing checkout paths/labels, both filtered and full merges, and an unrelated UUID. Also cover independently originated pairs.
- [ ] Batch index cleanup for distinct live destination UUIDs. Count the cleanup statements on a populated destination during replay and require zero FTS deletes when all selected UUIDs are absent.
- [ ] Compare preview, apply, and repeat with two pairs for one live UUID and a second UUID sharing its prefix. Check distinct archive and pair counts, full IDs, and marker-only output.
- [ ] Verify the first marker merged into an empty vault stamps 4 and forwards onward, protected destinations exclude incoming legacy copies, and refused future-version sources contribute nothing.

## Task 4. Verify the operator workflow and document rollout

**Status:** pending · **Depends on:** Tasks 2 and 3 · **Size:** S · **Can run in parallel with:** —

**Docs:** [Operator documentation and verification](./implementation.md#operator-documentation-and-verification)

- [ ] Use `kk:test` for the encrypted workflow with CLI import, startup sweep and merge, including receipt of a deletion when local recording is disabled.
- [ ] Use `kk:document` to update the CLI help, README, vault reference, `AGENTS.md` and `docs/architecture.md` with the implemented behavior and reader milestone. Run the quality benchmark for the shared index-path change.
- [ ] Run the Go `kk:review-code` and `kk:review-spec` checks against the agreed behavior and resolve findings.

## Dependency graph

```text
Task 1 ─┬─ Task 2 ─┬─ Task 4
        └─ Task 3 ─┘
```
