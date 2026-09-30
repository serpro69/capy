# Project database key resolution investigation

Investigation of [issue 122](https://github.com/serpro69/capy/issues/122), performed on 2026-09-30 against checkout `d9e09c1` and the installed v0.16.4 binary. Recommendation: resolve knowledge credentials in Go using the selected project and database configuration, and pass the selected key to the store. Implementation remains pending because this task requested exploration.

Follow-up: the user selected the shared Go resolver with explicit key files and literal dotenv compatibility. The agreed contracts are now in [design.md](design.md), with an [implementation plan](implementation.md), [tasks](tasks.md), and [isolated verification](.reviews/cove-2026-09-30.md). This investigation retains the original reproduction evidence and proposal history.

## Confirmed behavior

The [wrapper generator](../../../../internal/platform/setup.go) sources the main worktree's entire `.env` only when the first argument is `serve` and inherited `CAPY_DB_KEY` is empty. A wrong nonempty inherited key wins. Other wrapped commands never perform this recovery.

Two disposable Git projects used distinct synthetic keys, project-local `.env` files, and relative `.capy/knowledge.db` paths. Both encrypted databases were initialized through `dbsize`. Tests used the committed Codex wrapper and isolated XDG directories; the vault was disabled. MCP checks sent initialization and `tools/call` requests over stdio, exercising actual database reads.

| Operation in project B | Inherited key | Observed result |
| --- | --- | --- |
| Wrapped `dbsize` | B | Success |
| Wrapped `dbsize` | A | Wrong-passphrase error despite B's `.env` |
| Wrapped `dbsize` | Unset | Missing-key error despite B's `.env` |
| Wrapped CLI doctor | A | FTS5 available; knowledge decryption fails |
| MCP doctor through wrapped `serve` | A | FTS5 incorrectly unavailable; knowledge decryption fails |
| MCP doctor through wrapped `serve` | Unset | B's `.env` loaded; FTS5 and knowledge checks pass |

This confirms the failure mechanism. It does not establish which key the originally reported Codex daemon inherited; no real credentials were inspected. Disabling the daemon remains unverified.

## Proposed resolution

Introduce a shared resolver after project selection and config loading. Proposed precedence:

1. An optional explicit `store.key_file`, containing the passphrase; configuration stores only its path. Resolve relative paths against `cfg.DBProjectDir(projectDir)` and permit absolute paths.
2. `CAPY_DB_KEY` from `.env` at that database project directory. For a linked worktree without a declaration there, retain the existing main-worktree fallback.
3. Inherited `CAPY_DB_KEY`, preserving environment-only and direnv usage when no project credential is declared.

These are proposed configuration semantics, not existing options. An explicitly selected missing, unreadable, malformed, or empty credential must fail without falling back to an inherited key. An unrelated `.env` with no key declaration does not disable environment-only usage. Without any project credential declaration, an inherited key's project provenance cannot be determined.

Read only the knowledge credential; do not execute `.env` or import `CAPY_VAULT_KEY`. Define supported literal assignment syntax and migration guidance for shell-computed values before implementation. Errors should identify the credential source and database path without printing values or DSNs.

Reuse [DBProjectDir and ResolveDBPath](../../../../internal/config/paths.go): relative database paths redirect to the main worktree; absolute paths and XDG defaults retain current behavior. Do not infer credential ownership from a database's physical parent or symlink target. Honor `--project-dir` before lookup. Credential/config errors must survive CLI paths that currently fall back to defaults.

Pass the resolved key to `ContentStore` and reuse it for normal opens and maintenance connections. Both currently reread the environment, including checkpointing on close. Wire serve, doctor, dbsize, cleanup, and checkpoint through the same resolution. Current Claude hook handlers do not open the knowledge store; preserve their zero-exit wrapper contract and avoid imposing a key requirement on unrelated commands.

Keep [encrypt](../../../../cmd/capy/encrypt.go)'s rotation input separate: inherited `CAPY_DB_KEY` currently means the **new** passphrase. Automatically substituting the existing project key would break that contract. Document updating the project credential after rotation.

## Diagnostic and wrapper changes

Replace [MCP doctor's](../../../../internal/server/tool_doctor.go) stats-based FTS5 inference with the existing `platform.CheckFTS5()` in-memory probe. Explicitly call `getStore()` in the knowledge check and preserve its actual stats error; the removed FTS5 block currently initializes that handle.

Move credential loading out of the wrapper once Go handles it. Update the generator and both committed wrappers together. Retain binary discovery, hook exit behavior, and environment forwarding for compatibility. Unconditionally sourcing `.env` alone leaves project selection, vault isolation, and rotation unresolved.

## Remaining validation

Add regression coverage for two projects with crossed inherited keys; missing/empty/invalid explicit credentials; environment-only operation; linked worktrees; subdirectories and `--project-dir`; relative, absolute, XDG, and symlinked database locations; vault-key isolation; encrypt rotation; and close/checkpoint using the selected key. Assert database readability, retained content, and no database replacement after a wrong key. Verify independent FTS5 reporting and generator drift tests. Build and test with `-tags fts5` and synthetic keys.

Only the reproduction matrix above was executed during this investigation. Worktree integration, a candidate implementation, and its regression suite remain to be completed.
