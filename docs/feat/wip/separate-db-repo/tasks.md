# Tasks: First-class support for a separate knowledge-DB repo

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> Status: pending
> Created: 2026-09-13
> Issue: [#90](https://github.com/serpro69/capy/issues/90)
> Not Doing: checkpointing from inside the DB repo (`--db`, key lookup, env sourcing, reverse mapping), wrapper in the DB repo, auto-discovery, capy-driven git commits, team/multi-machine DB repos, doctor check, pre-pull guard, WAL-mode revisit, symlink-aware BackupCorruptDB

## Task 1: Wrapper propagates exit codes; project hook blocks on failed checkpoint
- **Status:** done
- **Depends on:** —
- **Size:** S
- **Can run in parallel with:** Task 2, Task 3
- **Docs:** [implementation.md#task-1](./implementation.md#task-1--wrapper-exit-code-propagation--project-hook--exit-1)

### Subtasks
- [x] 1.1 Edit `capyWrapperScript` in `internal/platform/setup.go`: `hook` keeps `|| true; exit 0` + deny-JSON fallback; other subcommands `exec` the binary; not-found → stderr + `exit 127`; fix header comment
- [x] 1.2 Update `.claude/scripts/capy.sh` and `.codex/scripts/capy.sh` byte-for-byte in the same commit
- [x] 1.3 Shell-level wrapper test with fake `capy` on PATH: passthrough, hook always 0, not-found 127 vs 0
- [x] 1.4 `preCommitHookBlock`: `while … done || exit 1` for the magic check, `checkpoint || exit 1`, remove dead `$? -ne 0`; extend `precommit_test.go`

## Task 2: `capy encrypt` resolves symlinks before the swap
- **Status:** done
- **Depends on:** —
- **Size:** S
- **Can run in parallel with:** Task 1, Task 3
- **Docs:** [implementation.md#task-2](./implementation.md#task-2--capy-encrypt-symlink-safe)

### Subtasks
- [x] 2.1 `runEncrypt`: `filepath.EvalSymlinks(dbPath)` after the existence check (warn and keep original on error)
- [x] 2.2 Test: symlinked encrypted DB → resolved path → `SwapAndVerify` leaves the symlink intact
- [x] 2.3 Add `TODO(#90)` at the `BackupCorruptDB` call in `store.getDB` noting the symlink-rename gap

## Task 3: `capy setup --db-repo` end-to-end (generator, CLI, drift guard, hook behaviour)
- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 1, Task 2
- **Docs:** [implementation.md#task-3](./implementation.md#task-3--capy-setup---db-repo)

### Subtasks
- [ ] 3.1 Generalize to `installPreCommitHookBlock(repoDir, block)`; keep `installPreCommitHook(projectDir)` as thin wrapper; existing tests unchanged
- [ ] 3.2 Add `preCommitHookBlockDBRepo()` — staged `\.db$`, magic check, `-s "$f-wal"`, `-e "$f-shm"`, remedy message naming the dir and `capy checkpoint --project-dir`, `done || exit 1`, no capy invocation; text tests
- [ ] 3.3 Add `SetupDBRepo(repoDir)`: `.gitignore` `*.db-wal`, `*.db-shm`; install hook; nothing else. Tests: exact file set, negative assertions, idempotency
- [ ] 3.4 CLI: `--db-repo` on `capy setup`, mutually exclusive with `--platform/--local/--project`, summary output
- [ ] 3.5 Extend `TestDriftGuardCoversEverySetupArtifact` with a `SetupDBRepo` temp repo
- [ ] 3.6 Shell-level hook test in a temp git repo: clean passes; `-shm` blocks; non-empty `-wal` blocks; zero-byte `-wal` passes; plaintext blocks

## Task 4: Documentation (README, ADR-016 amendment)
- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3
- **Size:** S
- **Can run in parallel with:** —
- **Docs:** [implementation.md#task-4](./implementation.md#task-4--documentation)

### Subtasks
- [ ] 4.1 README §"Keeping the DB out of your project repo": add `capy setup --db-repo` step, hook behaviour + remedies, the pull rule (no sidecars before `git pull`), `checkpoint --project-dir` from anywhere, commands table, wrapper behaviour change note
- [ ] 4.2 ADR-016: third checkpoint layer (DB-repo guard hook), wrapper exit-code consequence, "Rejected: checkpoint from the DB repo" pointer to design.md

## Task 5: Final verification
- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3, Task 4
- **Size:** S
- **Can run in parallel with:** —

### Subtasks
- [ ] 5.1 Run `/kk:test` — full suite with `-tags fts5`, race detector, shell-level tests not skipped locally
- [ ] 5.2 Run `/kk:document` — AGENTS.md generator/committed-copy table mentions `SetupDBRepo` outputs (hook exempt, `.gitignore` entries)
- [ ] 5.3 Run `/kk:review-code` with Go profile
- [ ] 5.4 Run `/kk:review-spec` against `docs/feat/wip/separate-db-repo/`
- [ ] 5.5 Manual acceptance in the real private DB repo: `capy setup --db-repo`, remove the hand-copied wrapper, commit with no sessions (passes), commit with a live session (blocked)

## Dependency Graph

```
Task 1 ─┐
Task 2 ─┼─→ Task 4 ─→ Task 5
Task 3 ─┘
```
