# Tasks for project database key resolution

> Design: [design.md](design.md)
> Implementation: [implementation.md](implementation.md)
> Investigation: [investigation.md](investigation.md)
> Status: in-progress
> Created: 2026-09-30
> Not Doing: Codex daemon changes, vault key management, shell dotenv execution, secret-manager integration, key provisioning, database-format changes, storage-identity redesign, general doctor integrity audit, shutdown-error overhaul
> Design review: findings corroborated and corrected; see [reconciliation](.reviews/design-review-reconciliation-2026-09-30.md)
> Renumbering: original Task 5 is now Task 6; original Tasks 6–10 are now Tasks 7–11. New Task 5 isolates stdio transport verification.

## Task 1: Correct FTS5 diagnostics under database failures

- **Status:** done
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Tasks 2, 3, 4, 5, 6, 7, 10, subject to their dependencies
- **Docs:** [Independent diagnostics](implementation.md#1-independent-fts5-diagnostics)
- **Verification:** `make test` and focused FTS5 doctor/race tests passed; [isolated review and environment notes](.reviews/task-1-code-review-2026-09-30.md).

### Subtasks

- [x] 1.1 Replace MCP doctor's stats-based FTS5 check in `internal/server/tool_doctor.go` with `platform.CheckFTS5`; explicitly initialize the knowledge store and call stats once → verify: wrong-key fixture reports available FTS5 and the real knowledge error.
- [x] 1.2 Add missing-key and open-error cases to `internal/server/tool_utility_test.go`; preserve initialized-empty-store and legacy-session reporting → verify: focused doctor tests pass without using startup success as the oracle.
- [x] 1.3 Distinguish `os.IsNotExist` from other stat failures in `cmd/capy/doctor.go`, with coverage in `cleanup_doctor_test.go` → verify: missing DB is not created, invalid path access is reported accurately.

## Task 2: Pin the store key through dbsize and maintenance connections

- **Status:** done
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Tasks 1, 5
- **Slicing strategy:** Risk-First — fresh maintenance connections must use the same credential as the pool
- **Docs:** [Captured key lifetime](implementation.md#2-a-captured-key-through-the-environment-only-dbsize-path)
- **Verification:** `make test` and focused FTS5 encryption/dbsize/doctor/race tests passed; [isolated review and validation](.reviews/task-2-code-review-2026-09-30.md).

### Subtasks

- [x] 2.1 Add private captured key/source fields and `WithEncryptionKey` in `internal/store`; snapshot the environment by default and preserve explicit-empty failure → verify: two stores with different explicit keys work under an unrelated process key.
- [x] 2.2 Reject an empty captured key at the top of `getDB`, under the mutex before mkdir/marker/open/recovery, and before `sql.Open` in `openSingleConn` → verify: direct store use and checkpoint leave the DB directory, marker, DB, and sidecars absent despite a nonempty inherited key. Task 6 adds the server-specific case once its constructor option exists.
- [x] 2.3 Route normal/recovery/maintenance opens through captured credentials; add explicit-key preflight in `encryption.go` and extend `encryption_test.go` → verify: plaintext rejection, environment changes, reopen-after-close, rebuild, vacuum, checkpoint, and close preserve the selected key and content.
- [x] 2.4 Create `cmd/capy/knowledge.go` helpers for project/strict-config selection and lazy store construction; route `dbsize.go` through them → verify: environment-only dbsize works and malformed config cannot select a fallback DB.

## Task 3: Read an explicitly configured key file through dbsize

- **Status:** done
- **Depends on:** Task 2
- **Size:** M
- **Can run in parallel with:** Tasks 1, 5
- **Docs:** [Explicit key files](implementation.md#3-explicit-project-key-files-through-dbsize)
- **Verification:** `make test`, focused FTS5 credential/dbsize tests with and without `-race`, and targeted `go vet` passed; [isolated review and validation](.reviews/task-3-code-review-2026-09-30.md).

### Subtasks

- [x] 3.1 Add `store.key_file` and presence-aware merging in `internal/config/config.go` and `loader.go` → verify: omission inherits, empty clears, all three config layers and invalid types are covered.
- [x] 3.2 Implement `Config.ResolveStoreKey`, safe source metadata, and regular-file/bounded-read admission in `internal/config/keys.go` → verify: the 4,096-byte raw limit includes the newline, overflow is rejected without truncation/fallback, regular symlinks work, and nonregular targets including a pre-existing FIFO fail promptly.
- [x] 3.3 Wire explicit-file/environment resolution into `cmd/capy/knowledge.go` → verify: actual B database access through dbsize succeeds with A inherited and B configured.
- [x] 3.4 Add format and owner/path coverage in `internal/config/keys_test.go` and `cmd/capy/key_resolution_cli_test.go` → verify: empty/NUL/newline errors, exact/over-limit files, linked worktrees, submodules, `../`, absolute, XDG, and DB/credential symlinks obey the contract without reading real credentials.

## Task 4: Support literal project dotenv credentials through dbsize

- **Status:** done
- **Depends on:** Task 3
- **Size:** M
- **Can run in parallel with:** Tasks 1, 5, 10
- **Docs:** [Dotenv compatibility and size boundary](implementation.md#4-literal-dotenv-compatibility-through-dbsize)
- **Size rationale:** Five bounded target files; fixed single-line grammar plus the existing resolver/file-admission helper. General dotenv/shell evaluation and transport harnesses are excluded.
- **Verification:** `make test`, focused FTS5 dotenv/resolver/dbsize tests with and without `-race`, and targeted `go vet` passed; [isolated review and validation](.reviews/task-4-code-review-2026-09-30.md).

### Subtasks

- [x] 4.1 Create the isolated literal parser in `internal/config/dotenv.go` with declaration detection and whole-file structural validation → verify: accepted bytes match the grammar and embedded/malformed/duplicate/empty declarations fail without partial results.
- [x] 4.2 Add owner dotenv, distinct main-worktree fallback, then environment ordering to `keys.go`; reuse regular-file admission with the 1 MiB dotenv limit → verify: every precedence boundary, access/size errors, and explicit-file bypass are covered.
- [x] 4.3 Test unsupported syntax both before and after a valid declaration, including a later duplicate, with a correct inherited key → verify: failure retains the source/line and `store.key_file` migration hint without printing source text or values.
- [x] 4.4 Test conflicting linked-worktree dotenv, literal grammar, false-positive names, sentinel execution, and vault/environment isolation through parser/resolver and actual dbsize fixtures → verify: relative DB mode ignores the linked dotenv, absolute/XDG mode consults it, no commands execute or environment values change, and content remains intact.

## Task 5: Establish baseline stdio MCP round trips

- **Status:** done
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Tasks 1, 2, 3, 4, 8, 10, subject to their dependencies
- **Slicing strategy:** Risk-First — establish real process/transport evidence before integrating project credentials
- **Docs:** [Baseline transport verification](implementation.md#5-baseline-stdio-mcp-transport-verification)
- **Verification:** Full `make test`, focused FTS5 stdio tests with and without `-race`, and targeted `go vet` passed; [isolated review and validation](.reviews/task-5-code-review-2026-09-30.md).

### Subtasks

- [x] 5.1 Add `cmd/capy/mcp_stdio_helpers_test.go` with one FTS5 binary fixture, isolated child environment/cwd, JSON-RPC IDs, piped I/O, bounded deadlines, and child cleanup → verify: it handles real responses and reaps failed/timed-out processes without sleeps or key disclosure.
- [x] 5.2 Add `cmd/capy/mcp_stdio_test.go` for existing environment-only initialize/index/search/doctor/shutdown/reopen behavior → verify: actual tool calls and retained content pass against current behavior.
- [x] 5.3 Run two subprocesses with distinct projects and their respective correct environment keys → verify: each returns its own marker and the helpers can be reused by later credential/wrapper cases.

## Task 6: Use project credentials in direct MCP sessions

- **Status:** done
- **Depends on:** Tasks 4, 5
- **Size:** M
- **Can run in parallel with:** Tasks 1, 8, 10
- **Docs:** [Direct MCP integration](implementation.md#6-direct-mcp-startup-and-store-operations)
- **Size rationale:** Four primary files for serve/server wiring and credential cases; Task 5 already owns transport construction.
- **Verification:** Full `make test`, focused FTS5 server/MCP credential tests with and without `-race`, and targeted `go vet` passed; [isolated review and validation](.reviews/task-6-code-review-2026-09-30.md).

### Subtasks

- [x] 6.1 Wire `serveRunE` to strict config, shared resolution, and explicit-key preflight → verify: bare invocation and flags before/after serve obey the same project contract.
- [x] 6.2 Add `WithKnowledgeCredentials` and default environment capture to `internal/server/server.go`; pass the snapshot/source into `getStore` → verify: environment changes after server construction do not change the store key.
- [x] 6.3 Reuse Task 5's fixture in `cmd/capy/key_resolution_mcp_test.go` with crossed inherited keys and declared project credentials → verify: actual index/search/doctor calls return project-specific content and close/reopen preserves it.
- [x] 6.4 Cover missing explicit credentials, wrong selected keys, plaintext databases, and direct-server doctor with an explicit empty credential despite a nonempty inherited key → verify: correct failure phase/message, no directory/marker/files on missing keys, and no replacement after a wrong key.

## Task 7: Ship wrappers with explicit upgrade and rollback guidance

- **Status:** done
- **Depends on:** Tasks 6, 8
- **Size:** M
- **Can run in parallel with:** Tasks 1, 10
- **Docs:** [Wrapper rollout and migration](implementation.md#7-generated-wrapper-rollout-and-migration)
- **Verification:** Full `make test`, focused FTS5 wrapper/migration/checkpoint/diagnostic checks with and without `-race`, and targeted vet passed; [isolated review and validation](.reviews/task-7-code-review-2026-09-30.md).

### Subtasks

- [x] 7.1 Remove dotenv execution and the serve-only guard from `capyWrapperScript`; regenerate both committed wrappers → verify: whole-file equality, wrapper exit-code, merged-idempotency, and completeness guards pass.
- [x] 7.2 Reuse the CLI stdio fixture for generated launches, key-file-only startup, wrong inheritance, bare/leading-flag serve, wrapped dbsize, and no-key hooks → verify: real database operations and exit contracts match direct use.
- [x] 7.3 Update shared `CheckVaultDisabled` guidance/tests and cover a worktree with both keys only in main dotenv, then with the vault key in the actual launch environment → verify: knowledge works in both cases; vault is first disabled with migration guidance, then readable; its key is never taken from or overwritten by project files.
- [x] 7.4 Integrate Task 8's checkpoint through the generated Git pre-commit path → verify: genuine checkpoint/busy failures abort commits and the separate DB-repo guard stays keyless.
- [x] 7.5 Document both breaking migrations and upgrade/rollback steps; add a frozen old-shaped config fixture with unknown `key_file` → verify: the legacy shape retains `store.path`, environment-only launch works, and fresh/repeated setup emits matching artifacts. Record any actual legacy-binary smoke test separately.

## Task 8: Apply project credentials to direct cleanup and checkpoint

- **Status:** done
- **Depends on:** Task 4
- **Size:** M
- **Can run in parallel with:** Tasks 5, 6
- **Docs:** [Direct maintenance integration](implementation.md#8-direct-maintenance-commands)
- **Verification:** Full `make test`, focused FTS5 maintenance tests with and without `-race`, and targeted `go vet` passed; [isolated review and validation](.reviews/task-8-code-review-2026-09-30.md).

### Subtasks

- [x] 8.1 Integrate shared target/key helpers into `cleanup.go` and `checkpoint.go` → verify: B credentials override inherited A for dry-run, reclamation, and checkpoint without changing selector behavior.
- [x] 8.2 Preserve missing-DB checkpoint no-op, surface other stat failures, and update `TestCheckpointSubcommand_BadConfig` to explicit failure → verify: malformed config creates no fallback DB/marker.
- [x] 8.3 Exercise direct checkpoint success/failure with isolated credentials → verify: busy/error cases remain nonzero; generated pre-commit integration is covered separately by dependent Task 7.

## Task 9: Explain credential selection in CLI and MCP doctor

- **Status:** pending
- **Depends on:** Tasks 1, 6, 7
- **Size:** M
- **Can run in parallel with:** Task 10
- **Docs:** [Credential diagnostics](implementation.md#9-credential-diagnostics-on-both-surfaces)

### Subtasks

- [ ] 9.1 Add shared safe credential-result formatting in `internal/platform/doctor.go`; use it in CLI/MCP doctor while retaining Task 7's disabled-vault migration hint → verify: key-file, dotenv, and environment sources are identifiable without exposing values.
- [ ] 9.2 Handle invalid config and resolver failures without selecting/opening a fallback target; continue independent checks → verify: FTS5/vault/runtime diagnostics remain available and no DB directory/marker is created.
- [ ] 9.3 Add parity tests in CLI and server doctor tests → verify: selection and authentication remain distinct, real open errors survive, and all synthetic secrets/DSNs are absent.

## Task 10: Preserve path inspection and key rotation contracts

- **Status:** pending
- **Depends on:** Task 3
- **Size:** M
- **Can run in parallel with:** Tasks 1, 4, 5, 6, 7, 9, subject to their dependencies
- **Docs:** [Path and rotation compatibility](implementation.md#10-path-inspection-and-rotation-compatibility)

### Subtasks

- [ ] 10.1 Make `which.go` use strict config without credential lookup → verify: valid paths print with missing credentials and malformed config produces no fallback path.
- [ ] 10.2 Make `encrypt.go` reject invalid config before prompting; preserve old/new-key selection, document stopping attached processes in help, and add the post-rotation credential-update reminder → verify: guidance is explicit and old project credentials cannot override a new environment passphrase.
- [ ] 10.3 Add deterministic rotation tests in `encrypt_test.go`, using the narrow prompt seam if needed → verify: new key succeeds, old key fails, prompt fallback is correct, and credential files/vault environment remain unchanged.
- [ ] 10.4 Coordinate shared `main_test.go` changes with Task 8 → verify: path/rotation and checkpoint expectations remain intact when integrated; no test purports to prove a human stopped all processes.

## Task 11: Verify the complete feature and update durable documentation

- **Status:** pending
- **Depends on:** Tasks 1, 2, 3, 4, 5, 6, 7, 8, 9, 10
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Final verification](implementation.md#11-final-verification-and-durable-documentation), [Matrix](implementation.md#verification-matrix)

### Subtasks

- [ ] 11.1 Use `kk:test` for the regression matrix, `make test`, and `make test-race` with FTS5 and isolated synthetic keys → verify: all cases, including migration, bounded input, and empty-key side effects, pass without weakened assertions or real data.
- [ ] 11.2 Use `kk:document` for README, architecture, root AGENTS, the next credential-policy ADR, and ADR-019's superseded policy → verify: whole-file dotenv break, vault launch-environment migration, file limits, linked-worktree precedence, rotation preconditions, and rollback steps are explicit and consistent.
- [ ] 11.3 Run `kk:review-code` with Go input → verify: findings are fixed or recorded durably with reasons and concrete next steps.
- [ ] 11.4 Run `kk:review-spec` over the implementation and feature directory → verify: design, plan, and runtime behavior agree.
- [ ] 11.5 Check generated-artifact guards and final repository status; update feature/task status only after verification → verify: no credentials, synthetic DBs, or temporary fixtures are committed.

## Dependency Graph

Each arrow below is a direct prerequisite edge; comma-separated destinations each depend on the source. This representation deliberately avoids crossing/joining arrows.

```text
Task 1  --> Task 9
Task 2  --> Task 3
Task 3  --> Task 4, Task 10
Task 4  --> Task 6, Task 8
Task 5  --> Task 6
Task 6  --> Task 7, Task 9
Task 8  --> Task 7
Task 7  --> Task 9
Tasks 1 through 10 --> Task 11 (all required)
```

Task 8's direct maintenance path no longer waits for wrappers. Task 7 owns the generated pre-commit integration and waits for both direct MCP and maintenance paths. Parallel markers describe dependency independence; coordinate shared test-file edits and never overwrite another contributor's changes.
