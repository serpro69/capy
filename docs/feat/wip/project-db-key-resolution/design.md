# Project database key resolution

> Status: Design agreed; review findings reconciled; implementation pending
> Created: 2026-09-30
> Issue: [122](https://github.com/serpro69/capy/issues/122)
> Companions: [Investigation](investigation.md), [Implementation](implementation.md), [Tasks](tasks.md)
> Verification: [Isolated code verification](.reviews/cove-2026-09-30.md)
> Review: [Original findings](.reviews/design-review-2026-09-30.md), [Corroboration and fixes](.reviews/design-review-reconciliation-2026-09-30.md)
> Code baseline: `d9e09c1`; runtime reproduction: v0.16.4

## Problem and user

A developer runs concurrent Codex sessions for projects with different encrypted knowledge databases. An inherited, nonempty `CAPY_DB_KEY` can belong to another project. The generated wrapper preserves it instead of loading project credentials, so a database that works in the project terminal fails through MCP. MCP doctor then incorrectly attributes the database failure to unavailable FTS5.

How might we provide reliable project-key selection and diagnostics for these concurrent sessions while preserving environment-only usage, worktree behavior, vault isolation, and key rotation?

The user confirmed this framing, the Go design profile, evaluation of three approaches, approach C below, isolated technical verification, and both the credential-selection and integration contracts. This document specifies proposed behavior. The candidate implementation has not been built.

## Acceptance criteria

1. Projects A and B with distinct encrypted databases and declared local credentials both support MCP indexing, search, and doctor when each process inherits the other project's synthetic key. Assertions inspect retained content, not merely successful server startup.
2. Direct CLI and generated-wrapper paths select the same knowledge credential for the same project/configuration. Cover serve, dbsize, cleanup, checkpoint, and doctor, including bare serve invocation and global flags before the subcommand.
3. An explicitly selected missing, unreadable, empty, or invalid credential fails without using a lower-priority source. Resolution failure, including an empty captured key supplied directly to a store/server, does not create a DB directory or `.project` marker and does not open, create, replace, or recover the knowledge database.
4. When optional project credential files are absent or satisfy file-admission rules and contain no knowledge-key declaration, inherited `CAPY_DB_KEY` continues to work. Mandatory encryption and the existing warning for short passphrases remain.
5. Relative, absolute, XDG, linked-worktree, and symlinked database locations preserve existing database ownership. `--project-dir` is considered before credential selection.
6. Every connection belonging to a store uses its captured key, including recovery, maintenance, checkpoint, close, and reopening the same store object after close. Later environment/file changes affect new store instances only.
7. Knowledge resolution neither changes `CAPY_VAULT_KEY` nor exports any resolved credential into the process environment. Rotation retains its separate old/new-key contract.
8. FTS5 capability remains correctly reported under wrong/missing knowledge credentials and database-access errors. Knowledge diagnostics retain the actual opening error and identify its credential source without revealing the key or DSN.
9. Claude hook events preserve their wrapper exit contract; Git pre-commit checkpoint failures continue to abort commits. Generated wrapper copies match their generator.
10. Users whose vault key was loaded only by the old wrapper receive explicit migration guidance in documentation and both doctors. A worktree launch with the vault key only in `.env` reports the vault disabled after upgrade; supplying that key to the actual MCP launch environment restores vault access without changing knowledge-key selection.

## Existing system and constraints

| Component | Verified current behavior |
| --- | --- |
| [config/paths.go](../../../../internal/config/paths.go) | Project selection and database ownership are separate. Only nonempty relative store paths redirect linked worktrees to the main checkout. |
| [config/loader.go](../../../../internal/config/loader.go) | Merges XDG, selected-project `.capy/config.toml`, then `.capy.toml`. Errors discard the merged config; callers currently fall back to defaults. |
| [store/store.go](../../../../internal/store/store.go) | Store construction is lazy. Normal and dedicated maintenance connections independently read the environment. |
| [store/encryption.go](../../../../internal/store/encryption.go) | Startup preflight checks key presence and plaintext headers, not successful decryption. |
| [server/tool_doctor.go](../../../../internal/server/tool_doctor.go) | Its stats-based FTS5 check also initializes the store. |
| [platform/setup.go](../../../../internal/platform/setup.go) | One generator owns both wrappers. Existing wrappers can source `.env` or exit before invoking Go. |
| [cmd/capy/encrypt.go](../../../../cmd/capy/encrypt.go) | Prompts for the old key; the environment or a confirmed prompt supplies the new key. |

Preserve [ADR-016](../../../adr/016-wal-mode-and-checkpoint-strategy.md)'s pool-close-before-checkpoint ordering, [ADR-020](../../../adr/020-wal-mode-incompatible-with-pragma-rekey.md)'s encryption mechanisms, and [ADR-026](../../../adr/026-worktree-shared-knowledge-db.md)'s database ownership. This feature revises the environment-only credential policy in [ADR-019](../../../adr/019-encrypted-knowledge-db.md), while retaining mandatory encryption. It changes no schema, cipher, reader version, or search behavior.

Go profile guidance applies to connection ownership, explicit errors, and safe diagnostics. Retain the existing SQLite driver, `database/sql`, and logging infrastructure. Database-schema, gRPC, and external telemetry guidance does not apply to this bounded credential-resolution change. No dependency addition or upgrade is planned.

## Credential selection

### Configuration and ownership

Add optional `store.key_file`. It contains a path, never a passphrase. It follows the existing three configuration layers. Omission inherits the lower layer; an explicit empty string clears a lower-layer key-file setting and restores automatic selection. Use presence-aware merging for this field; do not change `store.path` merging.

Load configuration from the selected project exactly as today; do not reload the main worktree's configuration. Compute the credential owner using `cfg.DBProjectDir(projectDir)`. Relative key-file paths resolve against that owner, including relative paths supplied by global configuration. Absolute paths remain absolute. Do not expand environment variables or `~` in the setting.

Retain current project selection and worktree fallback behavior. In particular, an explicitly supplied subdirectory remains an explicit project override; this feature does not reinterpret it as its Git root. Ordinary invocation from a Git subdirectory still benefits from existing root detection. Do not canonicalize project symlinks or derive ownership from a database symlink's target or parent directory.

### Precedence and failure

Resolve the first applicable source:

1. Nonempty configured `store.key_file`.
2. `CAPY_DB_KEY` declared in `<DBProjectDir(projectDir)>/.env`.
3. The main worktree's `.env`, only if its directory differs from the owner above and the previous file did not declare a key.
4. Inherited `CAPY_DB_KEY`.

Do not read lower-priority files after choosing a source. In particular, a valid explicit key file bypasses `.env` compatibility parsing completely.

With a relative `store.path` in a linked worktree, the owner is the main checkout: the linked worktree's own `.env` is deliberately not read, even if it declares a different key. Absolute and XDG database modes retain the selected worktree as owner, so its `.env` is tried before a distinct main-worktree fallback.

An absent optional `.env` or a readable file without a recognized knowledge-key declaration permits fallback. Other file-access failures are errors: inability to inspect a candidate is not evidence that it contains no declaration. A recognized empty, malformed, duplicate, or unsupported knowledge-key declaration is an error. An explicit key file never falls back after any error. An empty inherited key produces a missing-credential error with setup guidance.

Resolve and validate credentials before operations that would open/create the database or write its `.project` marker. A valid but wrong selected key produces the existing wrong-passphrase/database error; never try other keys after failed decryption or recreate a valid encrypted database as a credential fallback.

### Key file format

The file contains the passphrase itself. Remove at most one terminal LF, or one terminal CRLF, for ordinary text-file compatibility. Preserve other bytes, including leading/trailing spaces; do not trim or unquote. Reject an empty result, NUL, and remaining CR/LF characters. Follow filesystem symlinks to regular files, supporting externally provisioned credential files. Do not write, chmod, rotate, or provision this file automatically.

Require a regular-file target before opening; directories, FIFOs, sockets, and devices are errors. Recheck the opened descriptor's type before reading. Cap raw key-file input at **4,096 bytes, including any terminal newline**, and read at most 4,097 bytes to detect overflow; neither reported stat size nor an EOF-only read is sufficient. Reject oversized input without truncation or credential fallback. Apply the same regular-file and bounded-read policy to optional dotenv inputs with a **1 MiB** limit; oversized or nonregular optional files are access errors, not absent declarations. These are admission limits on files, not a new passphrase-length limit on environment-only keys.

The documented project-local location is `.capy/db.key`, already covered by the generated `.capy/**` ignore rule. Document restrictive file permissions and keeping credentials outside version control. Users choosing other locations manage their own ignore rules. `capy setup` must not create a secret file or insert a machine-specific secret path into tracked MCP configuration.

### Literal dotenv compatibility

This is a limited compatibility reader, not a shell or a general dotenv implementation. Use the Go standard library and keep the parser isolated from filesystem selection.

- Recognize a line beginning, after spaces/tabs and optional `export` plus whitespace, with the exact identifier `CAPY_DB_KEY`. A misspelled/prefixed variable is not that identifier. Comments do not declare a key.
- A readable file with no such declaration candidate contributes no key. If a candidate exists, require the file to be a sequence of blank lines, comments, and single-line variable assignments. This prevents accepting a candidate embedded inside unsupported multiline strings or shell constructs.
- Accept `NAME=value` and `export NAME=value`, with whitespace around the assignment. Validate assignment structure throughout such a file, but retain only the target value. Duplicate target declarations fail even when equal.
- Target values may be unquoted, single-quoted, or double-quoted. Unquoted values contain no whitespace, quotes, backslashes, or shell operators; a `#` starts a comment only after separating whitespace. Single quotes preserve their enclosed bytes. Double quotes support escaped backslash and double quote only. Quoted values may contain spaces. After a closing quote, allow only whitespace and an optional comment.
- Never expand variables, execute commands, or interpret escapes such as `\n`. Reject unquoted or unescaped double-quoted expansion markers (`$` and backticks) in the target value; single-quoted versions are literal. Reject unsupported target escapes, multiline values, and executable statements. Non-target values are discarded and never expanded; their presence must not change the environment.
- Support LF and CRLF files. Parser/scanner failures return a source path, line number where available, a fixed reason, and guidance to configure `store.key_file` with a literal passphrase file; never include source text or the parsed value in an error.

**Breaking compatibility change:** users who already export a valid key via their shell/direnv can still be affected if `.env` contains a recognized knowledge-key declaration plus unsupported syntax anywhere else, before or after it. Whole-file validation then fails without inherited-key fallback. Configure `store.key_file` to bypass that application dotenv file entirely. Existing environment-only/direnv usage remains supported when no recognized project knowledge-key declaration exists and optional candidate files satisfy the access limits above; `.envrc` is not read or executed by capy.

Keep validation after the first declaration: later duplicate declarations and malformed trailing input must still fail. Checking only the prefix through the first candidate would change the agreed duplicate/error contract. Document this migration boundary prominently in the release/README/ADR work rather than describing it only as compatibility support.

## Architecture and key lifetime

Add the resolver to `internal/config`, next to path/config ownership. It returns the secret separately from a safe `KeySource` descriptor containing only kind and resolved source path. It does not import store, mutate environment variables, inspect the database, or authenticate a passphrase.

Add a store constructor option `WithEncryptionKey` accepting the selected key and a safe source hint. The constructor's default is a snapshot of `CAPY_DB_KEY`, preserving environment-only construction for existing callers. An explicit option replaces that snapshot even if empty; an empty explicit value must fail on use, never consult the environment again. Store secret fields stay private.

Enforce that failure at the beginning of `getDB`, under its mutex and **before** directory creation, `.project` writes, or opening/recovery. `openSingleConn` must independently check the captured key before `sql.Open`, since standalone checkpoint need not call `getDB`. Validation only inside `openDB` is too late. Direct-store/server tests must assert that a missing parent DB directory and marker remain absent after an empty-key failure.

Normal opening, corruption recovery, `openSingleConn`, vacuum, FTS rebuild, public checkpoint, and close/checkpoint all consume the same stored value. Add an explicit-key encryption preflight for serve. Preserve existing short-passphrase policy and SQLite error classification. Supply the actual safe source hint to the canary/error path so a file-backed credential failure does not misleadingly direct the user only to `CAPY_DB_KEY`; vault callers keep their existing hint.

Add a corresponding server option `WithKnowledgeCredentials`. Serve resolves once and injects the key/source before constructing the server. `getStore` passes them to the store inside its existing lazy initialization. Default server construction also captures the environment once so tests and internal callers retain a usable default without process-global mutation.

Keep executor/security/session project scope at the working project. Only knowledge database ownership and credential selection use the database owner. Resolved keys are not passed to executor subprocesses through environment mutation. Changing files or environment does not live-rekey a running server: construct a new store/server after deliberate credential rotation.

## Command and diagnostic behavior

| Path | Required behavior |
| --- | --- |
| Bare capy and `serve` | Strict config load, credential resolution, explicit-key preflight, then lazy MCP startup. Startup alone still does not prove database readability. |
| `dbsize`, `cleanup` | Strict config load and shared credential resolution before creating a store. Preserve cleanup selectors, dry-run, and reclamation behavior. |
| `checkpoint` | Strict config load. Missing database remains a successful no-op without requiring credentials. Other stat errors fail. For an existing database, resolve credentials and preserve checkpoint/busy failures. |
| `which` | Strict config load and existing path resolution; do not require or read credentials. |
| CLI doctor | Report config/credential failures and continue independent diagnostics. Never select a default database after invalid config. Do not create a missing database; only `os.IsNotExist` means absent. |
| MCP doctor | Probe FTS5 in memory independently. Explicitly call `getStore()` for knowledge stats and preserve opening errors. Existing lazy initialization may create an empty database, as today. |
| `encrypt` | Strict config load for the target path. Do not resolve or substitute the normal-access key. Keep the old-key prompt and new-key environment/confirmed-prompt contract. Tell users to update any project credential after success. |
| Claude hook events | Do not introduce a knowledge-key requirement. Preserve generated wrapper zero-exit behavior. |
| Git pre-commit | Its wrapped checkpoint uses the shared resolver. Preserve failed-checkpoint propagation and commit abortion. |
| Setup, help, version, vault commands | No knowledge-credential requirement introduced. Vault key selection remains independent. |

Diagnostics identify the database path and safe credential source; the value, source line, and DSN must never appear. CLI doctor retains its existing diagnostic exit convention rather than converting every failed check into a command exit failure. Other database commands fail on configuration/credential errors. Update `TestCheckpointSubcommand_BadConfig` to reflect the deliberate removal of fallback targeting.

For invalid configuration, doctor reports a failed Config check and explains that knowledge checks were not run; it may use ordinary defaults for independent runtime discovery but must not fabricate a fallback knowledge path. For resolution failures, report a failed knowledge-credential check and skip opening the database. Successful resolution is distinct from successful authentication. Preserve the real canary error when a selected key is wrong.

When the vault environment key is absent, both doctors use the shared `platform.CheckVaultDisabled` guidance: the vault is disabled because `CAPY_VAULT_KEY` is not in the process environment, wrappers no longer source `.env`, and enabling it requires configuring the actual launch environment and restarting the host/MCP process. Report this without reading a vault assignment from project files. This changes diagnostics, not vault key selection.

### Rotation operating preconditions

Before `capy encrypt`, stop all servers/processes using the target database. After successful rotation, update any project credential material before constructing new stores or restarting MCP. Document these operator steps in command help and README; do not imply that the CLI enforces process quiescence. Automatic process stopping, credential-file rewriting, and live key reload are outside scope. Tests verify the command guidance and key-lifetime/file-preservation behavior, not whether a human followed the procedure.

## Wrapper rollout

Remove credential sourcing and the serve-only missing-key guard from `capyWrapperScript`. Retain binary discovery, hook dispatch, and non-hook exit propagation. Update `.claude/scripts/capy.sh` and `.codex/scripts/capy.sh` together with the generator. Keep current MCP environment forwarding for credentials already present in the launch environment; forwarding does not load missing credentials from files.

**Breaking vault migration:** the old fallback sourced the whole main-worktree `.env` when `serve` inherited no knowledge key. It could therefore supply `CAPY_VAULT_KEY` too. After wrapper regeneration, a vault key present only in that file no longer enables the vault. Retain the agreed environment-only vault contract: users must provision the vault key in the environment of the process that actually launches MCP. An export in a new terminal does not update an already-running daemon's environment. Restart/reconfigure that launcher as appropriate and confirm vault access through MCP doctor, not only terminal doctor. The knowledge resolver must not compensate by reading or exporting vault credentials.

Before upgrading mixed shell/data dotenv users, migrate their knowledge passphrase to an explicit literal key file; otherwise whole-file rejection can block startup even with a correct inherited key. README/release guidance must name both this change and the vault migration.

Users upgrade the binary, run setup for each installed platform using the binary directly, and restart MCP. Old generated wrappers can stop the process before the new resolver runs or source the whole `.env`; upgrading only the binary cannot repair that behavior. Regeneration is part of supported rollout. Do not promise a verified `--no-daemon` workaround.

### Rollback

Stop MCP cleanly and checkpoint the target with the current binary and its valid credential before downgrading. Supply the same existing knowledge passphrase as `CAPY_DB_KEY` in the actual launch environment, and supply `CAPY_VAULT_KEY` separately if vault access is required. Downgrade the binary, run that binary's setup for each installed platform to regenerate matching wrappers, restart the launcher/MCP, and verify actual database reads and vault status. Do not rotate or rewrite the database just to roll back this feature.

The v0.16.4 loader ignores the unknown `store.key_file` setting, but cannot read the file as a credential source. Leaving the setting in config is compatible with that version; leaving the passphrase only in the file is not. Retaining new wrappers with an old binary also provides no dotenv loading. The [reconciliation evidence](.reviews/design-review-reconciliation-2026-09-30.md#r4-rollback-path) verifies unknown-field behavior; the implementation must preserve a legacy-loader regression fixture and exercise a downgrade-style launch with explicit environment credentials. There is no schema migration to reverse.

## Alternatives and decision

| Approach | User value | Feasibility | Distinct benefit | Cost |
| --- | --- | --- | --- | --- |
| A Wrapper repair | Fixes generated launch paths | High initially | Small patch | Duplicate project-selection policy and differing direct-CLI behavior |
| B Go resolver using dotenv | Consistent MCP and CLI selection | High | Little new configuration | General application dotenv remains the only local credential interface |
| C Go resolver with key file and dotenv compatibility | Consistent selection and explicit credential declaration | High | Works independently of daemon environment and complex application dotenv files | New setting and precedence/format contracts |

Approach C was selected by the user. Its main failure risks are fallback after malformed credentials, inconsistent keys across connection types, changing rotation's new-key input, and stale wrappers. The explicit failure, lifetime, command, and rollout contracts address each risk.

### Rejected alternatives

- A: duplicates root/argument/config interpretation in shell and leaves direct CLI behavior inconsistent.
- B: lacks an explicit alternative when the project's application dotenv file is not suitable for literal credential extraction.

## Assumptions

1. The host supplies the intended project directory, as in the reported same-database-path evidence. Verify explicit override and cwd/root cases; correcting an unrelated inherited project-directory variable is not a credential-selection mechanism.
2. Each machine has access to the declared local credential file. Missing-file tests must demonstrate failure without environment fallback.
3. Literal assignments cover the supported dotenv compatibility path. Grammar tests must include rejected executable/multiline inputs and migration guidance must make the boundary explicit.

## Not Doing

- Codex daemon changes or disabling daemon use: project credential selection belongs to capy.
- Vault credential selection changes: the vault keeps its environment-only key contract. Migration guidance and disabled-vault diagnostics are explicitly in scope.
- Shell execution, general dotenv evaluation, secret-manager/keychain integration, or key provisioning: unnecessary for the selected file/env contract.
- Database migrations, cipher changes, worktree/XDG ownership redesign, or project-symlink normalization: existing storage identity is preserved.
- A general doctor integrity audit or shutdown-error overhaul: the separate findings and concrete next steps are recorded in the [verification report](.reviews/cove-2026-09-30.md#additional-findings-and-follow-ups).

## Validation and review

The [implementation plan](implementation.md#verification-matrix) defines the regression matrix and observable checks. Run FTS5-tagged tests with synthetic keys and isolated config/data directories. Keep real key values out of test output and captured MCP transcripts. Scope benchmarks to actual algorithm changes; this design changes credential plumbing, not search/indexing algorithms.

The [design review](.reviews/design-review-2026-09-30.md) has been corroborated finding by finding and the valid issues corrected in these documents; see the [reconciliation](.reviews/design-review-reconciliation-2026-09-30.md). Implementation remains pending. Review reconciliation is not a claim that the proposed runtime behavior has been implemented or tested.
