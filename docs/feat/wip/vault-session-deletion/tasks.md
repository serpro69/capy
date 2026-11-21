# Vault session deletion

> Design: [design.md](./design.md)
>
> Implementation: [implementation.md](./implementation.md)
>
> Status: pending
>
> Scope exclusions: bulk deletion, physical erasure, synchronized undelete

## Task 1. Keep a deleted UUID out of disk imports

**Status:** pending · **Depends on:** — · **Size:** M · **Can run in parallel with:** —

**Docs:** [Local deletion and archive writes](./implementation.md#local-deletion-and-archive-writes)

- [ ] Add persistent deletion records for fresh and migrated vaults and stamp the reader version on first use.
- [ ] Capture the effective project in the deletion transaction and remove the archive using the existing cleanup path.
- [ ] Enforce suppression at the store write boundary and return accurate import, retry and startup sweep outcomes.
- [ ] Verify reopen, mixed batches, a racing import, rollback, and preservation of child sessions.

## Task 2. Expose the local delete opt-out

**Status:** pending · **Depends on:** Task 1 · **Size:** M · **Can run in parallel with:** Task 3

**Docs:** [Local configuration](./implementation.md#local-configuration)

- [ ] Resolve the config at delete time, show the selected behavior in the preview, and implement a one-time local delete when disabled.
- [ ] Verify default and overridden configuration, re-import after an opted-out delete, and continued suppression of earlier recorded deletions.

## Task 3. Propagate deletions through a vault merge

**Status:** pending · **Depends on:** Task 1 · **Size:** M · **Can run in parallel with:** Task 2

**Docs:** [Cross-machine merge and reporting](./implementation.md#cross-machine-merge-and-reporting)

- [ ] Read source markers and apply them to present and absent destination sessions. Respect destination markers for incoming live rows.
- [ ] Preserve effective-project selection, union independently recorded scopes, and distinguish removals from remembered or repeated markers in preview and actual output.
- [ ] Verify both merge directions, retries, forwarding through a third vault, filtered merges and legacy source behavior.

## Task 4. Verify the operator workflow and document rollout

**Status:** pending · **Depends on:** Tasks 2 and 3 · **Size:** S · **Can run in parallel with:** —

**Docs:** [Operator documentation and verification](./implementation.md#operator-documentation-and-verification)

- [ ] Use `kk:test` for the encrypted workflow with CLI import, startup sweep and merge, including receipt of a deletion when local recording is disabled.
- [ ] Use `kk:document` to update operator-facing behavior, help and version requirements.
- [ ] Run the Go `kk:review-code` and `kk:review-spec` checks against the agreed behavior and resolve findings.

## Dependency graph

```text
Task 1 ─┬─ Task 2 ─┬─ Task 4
        └─ Task 3 ─┘
```
