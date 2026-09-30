# ADR-032: Project database credentials and captured key lifetime

**Status:** Accepted
**Date:** 2026-09-30
**Partially supersedes:** [ADR-019](019-encrypted-knowledge-db.md) — environment-only knowledge credential selection
**Preserves:** [ADR-016](016-wal-mode-and-checkpoint-strategy.md), [ADR-020](020-wal-mode-incompatible-with-pragma-rekey.md), [ADR-026](026-worktree-shared-knowledge-db.md)

## Context

Concurrent MCP sessions may inherit a nonempty `CAPY_DB_KEY` for another project. The old wrapper kept that key, so a database readable in the project terminal could fail through MCP. Fixing the wrapper alone would leave direct CLI commands with a different selection policy. Independently rereading the environment for maintenance connections could also change a store's key during its lifetime.

The project selected a Go resolver with explicit key files and limited dotenv compatibility. This records that agreed decision, implemented and checked against the [feature design](../feat/wip/project-db-key-resolution/design.md), rather than inferring an undocumented intent from the code. Mandatory encryption, storage identity, schema, cipher, and retrieval behavior are unchanged.

## Decision

### Select the target before the credential

The command layer strictly loads configuration for the selected project, then computes `ResolveDBPath` and `DBProjectDir`. Invalid configuration fails without a default database fallback. CLI doctor reports the failure and continues independent checks.

`store.key_file` stores a path, never a passphrase. It follows the existing global → project `.capy/config.toml` → project `.capy.toml` layers: omission inherits, explicit `""` clears. Relative key-file paths, even from global config, are relative to the database owner; absolute paths remain absolute. There is no `~` or environment expansion.

`Config.ResolveStoreKey` selects the first applicable source:

1. A nonempty configured `store.key_file`.
2. `CAPY_DB_KEY` in the database owner's `.env`.
3. The main worktree's `.env`, only when distinct from that owner and no key was declared in the previous file.
4. Inherited `CAPY_DB_KEY`.

Relative `store.path` makes the main checkout the owner for linked worktrees, deliberately ignoring the linked checkout's dotenv. Absolute/XDG modes keep the selected worktree as owner and can fall back to the main checkout's dotenv. Configuration is never reloaded from the owner. Submodules, explicit project subdirectories, and symlinked databases retain existing identity rules; a database symlink's target does not become the credential owner.

Missing optional dotenv and admitted files without a recognized declaration permit fallback. Invalid declarations or file-access errors stop resolution; explicit key files never fall back on error. A valid but incorrect key produces an authentication error without trying another source or replacing the encrypted database.

### Bound and interpret credential files as data

Both file readers follow symlinks to regular files, check type before open and on the descriptor, and bound the actual read to the limit plus one byte. Pre-existing FIFOs, devices, sockets, and directories are rejected.

| Source | Raw limit | Format |
| --- | --- | --- |
| Key file | 4,096 bytes, including a terminal newline | Literal passphrase; remove at most one terminal LF/CRLF; preserve spaces; reject empty, NUL, or remaining CR/LF |
| Optional dotenv | 1 MiB | Literal single-line compatibility grammar; retain only the knowledge key |

These are file admission limits, not environment passphrase-length limits. Capy does not create, chmod, or rewrite credentials. The documented `.capy/db.key` location is covered by setup's `.capy/**` ignore rule; users must provision it privately and manage ignores elsewhere.

**Breaking change: whole-file dotenv validation.** When an exact `CAPY_DB_KEY` declaration candidate exists, every line must be blank, a comment, or a supported single-line assignment. Optional `export` and whitespace around `=` are accepted. Duplicate or empty targets, multiline strings, executable statements, and unsupported syntax before or after the target fail even with a correct inherited key. Unquoted target values forbid whitespace, quotes, backslashes, shell operators, and expansion markers; comments require separating whitespace. Single quotes preserve bytes. Double quotes allow escaped backslash/double quote only and reject `$` and backticks. Other values are structurally checked and discarded. No commands, expansions, or environment mutations occur. Files without a recognized candidate contribute no key, subject to admission limits. See the [README](../../README.md#project-credentials) for usage.

### Capture one key for every store connection

The resolver returns the secret separately from `KeySource` (kind and safe source path). It does not depend on the store or authenticate the database. Serve injects the result through `WithKnowledgeCredentials`; lazy `getStore` passes it through `WithEncryptionKey`. Direct store/server construction snapshots the environment by default. Explicit empty options override the default and fail on use.

`ContentStore` checks emptiness before directory creation, `.project` writes, or open/recovery, and checks separately on dedicated connections. Pool, recovery, rebuild, vacuum, checkpoint, close, and reopen-after-close all use the captured key. Files and environment are not live key-reload channels. The pool still closes before the final checkpoint (ADR-016).

`serve`, `dbsize`, `cleanup`, `checkpoint`, and CLI doctor use the shared resolver. Checkpoint retains a keyless missing-database no-op. `which` requires valid target configuration but never reads credentials. Help, setup, version, and Claude hook events do not gain a knowledge-key requirement. Both doctors report safe source selection separately from authentication and check FTS5 independently in memory. CLI doctor does not create a missing database; MCP doctor may lazily initialize an empty store. Errors never include values, assignment text, or DSNs.

`capy encrypt` bypasses normal-access resolution. It strictly loads the target, prompts for the old key, and uses the new `CAPY_DB_KEY` or a confirmed prompt for the new key. Operators must stop all attached processes, rotate, update project credentials, then restart. The command cannot enforce that process precondition and does not rewrite credential files or change vault credentials. Plaintext encryption retains the DELETE-before-PRAGMA-rekey rule; encrypted rotation uses the backup API (ADR-020).

### Deploy wrappers and migrate the vault environment

The generator and both committed wrappers delegate selection to Go without sourcing `.env` or enforcing a serve-only environment guard. Hook zero exits, non-hook failure propagation, and Git checkpoint commit abortion remain intact. Codex environment forwarding passes only credentials already present in the launcher.

**Breaking change: dotenv-only vault keys stop enabling the vault.** The old wrapper sometimes sourced the entire main dotenv when the knowledge key was absent, incidentally loading `CAPY_VAULT_KEY`. The vault remains environment-only: supply that key to the process that actually launches MCP and restart the launcher and MCP. An export in a new terminal does not update an existing daemon. Both doctors explain this when the vault is disabled; knowledge resolution never reads or exports a vault key.

Upgrade procedure: first move credentials out of incompatible application dotenv into an explicit literal file; preserve the existing database passphrase. Upgrade the binary and invoke it directly to run setup for each installed platform. Restart the launcher/MCP and verify real knowledge reads and vault status through MCP. Updating only the binary leaves old wrapper behavior in place. See [commands and recovery steps](../../README.md#upgrade-and-credential-migration).

## Alternatives

- **Repair shell wrappers:** smaller initially, but duplicates project/configuration policy and leaves direct CLI inconsistent.
- **Go dotenv-only resolver:** centralizes selection but offers no explicit escape from complex application dotenv syntax.
- **Key files with literal dotenv compatibility (chosen):** one policy for MCP and CLI, independent of stale inherited knowledge keys, with an explicit bypass for incompatible dotenv files.

## Consequences and rollback

Local project credentials override inherited knowledge keys. Invalid declarations now fail closed; whole-file dotenv rejection and vault migration must be treated as upgrade changes. Secret-manager integration, vault file resolution, credential provisioning, daemon changes, and live key reload remain outside scope.

To roll back: stop MCP cleanly, checkpoint with the current binary and valid credential, provide the same passphrase as `CAPY_DB_KEY` in the actual launch environment, and provide `CAPY_VAULT_KEY` separately if needed. Downgrade, run the old binary's setup for each platform, restart, and verify actual database reads and vault status. No schema or cipher migration needs reversal; do not rotate just to downgrade.

The v0.16.4 loader ignores unknown `key_file` while retaining `store.path`, but cannot read credentials from that file. A frozen decoding fixture and new-binary environment-only wrapper round trips cover that boundary; they are not a full legacy-binary downgrade or daemon-host smoke test. Keeping new wrappers with an old binary does not restore shell dotenv loading.

Validation uses synthetic credentials and isolated projects across parser/admission limits, worktrees, crossed inherited keys, real concurrent stdio processes, wrong-key content preservation, empty-key side effects, maintenance lifetime, rotation, diagnostics, and generated-artifact guards. The [task record](../feat/wip/project-db-key-resolution/tasks.md#task-11-verify-the-complete-feature-and-update-durable-documentation) records final suite and review evidence.
