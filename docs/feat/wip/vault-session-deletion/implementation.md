# Implementation plan for vault session deletion

The storage and command behavior is specified in the [design](./design.md). Implement against temporary encrypted vaults using synthetic keys.

## Local deletion and archive writes

1. Extend the fresh schema and next named migration in `internal/vault/store.go` and `migrations.go`. Make the marker independent of `vault_sessions` and index membership by UUID through the composite key. Extend the reader-version constants in `codec.go`. Verify that migrating an existing vault preserves its sessions and that the first durable marker raises the version in the same commit.
2. In `DeleteSession`, resolve the actual UUID and effective project in the write transaction, reuse the session and index removal logic, and preserve the current missing-session and independent-child behavior. Reuse the removal operation for incoming markers even if the destination has no row. Verify both rollback and removal of blobs, sidecars, metadata, and search entries.
3. In `internal/vault/import.go` and the shared store write path, apply the UUID check to manual imports and server sweeps. A check during discovery can avoid loading an excluded transcript, but the check at commit decides the outcome. Carry that result back through batch and per-session retry reporting. Verify a batch containing both a marked UUID and a live neighbor, and a deterministic interleaving where deletion commits between import planning and writing.

## Local configuration

Add the presence-aware boolean `vault.remember_deletions` to `internal/config` with a default of true. Resolve it in the invocation project's configuration for `cmd/capy/vault.go`'s delete command. The confirmation preview should explain whether this delete will propagate or the session can be re-imported. Preserve the existing confirmation and `--yes` behavior. Import, sweep, and incoming merge always honor stored markers. Verify explicit false, ordinary precedence, restoration by import after an opted-out deletion, and preservation of a prior marker after a config change.

## Cross-machine merge and reporting

In `internal/vault/merge.go`, detect the marker table on the source and enumerate selected markers independently of live session rows. Use a coherent source view of the rows and markers for each merge so a concurrent deletion cannot leave the receiver with mismatched observations. Apply incoming pairs to the destination atomically with removal of any current session. Honor local markers before reading or writing live content or reconciling metadata, including the existing same-content and smaller-copy paths. Reuse the source-project matcher. A durable marker for a third machine and a deleted destination transcript are different observable outcomes.

Extend the result counters and CLI output in `internal/vault/import.go` and `cmd/capy/vault.go` so the operator can see what a merge removed or would remove. Compare the preview with a real merge against an unchanged fixture. Exercise both directions, an empty destination that later forwards the deletion, differing project labels, filtered repeats, and legacy sources. Verify that unaffected sessions still follow existing larger-copy and metadata rules.

## Operator documentation and verification

Update the merge and delete help, the vault reference, README and the affected vault invariants in the architecture documentation. Describe the retained identifiers, configuration, project filters, dry-run, and mixed-version rollout. Base verification on focused encrypted store, CLI and startup-sweep tests with `-tags fts5` and both synthetic keys. Run affected suites with the race detector. Include the repository quality benchmark if implementation changes indexing or search code.
