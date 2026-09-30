# Tasks for project database key resolution

> Design: [design.md](design.md)
> Implementation: [implementation.md](implementation.md)
> Investigation: [investigation.md](investigation.md)
> Status: pending
> Created: 2026-09-30
> Not Doing: Codex daemon changes, vault key management, shell dotenv execution, secret-manager integration, key provisioning, database-format changes, storage-identity redesign, general doctor integrity audit, shutdown-error overhaul
> Design review: pending; run `kk:review-design project-db-key-resolution` before implementation

## Task 1: Correct FTS5 diagnostics under database failures

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Tasks 2, 3, 4, 5, 6, 9, subject to their dependencies
- **Docs:** [Independent diagnostics](implementation.md#1-independent-fts5-diagnostics)

### Subtasks

- [ ] 1.1 Replace MCP doctor's stats-based FTS5 check in `internal/server/tool_doctor.go` with `platform.CheckFTS5`; explicitly initialize the knowledge store and call stats once → verify: wrong-key fixture reports available FTS5 and the real knowledge error.
- [ ] 1.2 Add missing-key and open-error cases to `internal/server/tool_utility_test.go`; preserve initialized-empty-store and legacy-session reporting → verify: focused doctor tests pass without using startup success as the oracle.
- [ ] 1.3 Distinguish `os.IsNotExist` from other stat failures in `cmd/capy/doctor.go`, with coverage in `cleanup_doctor_test.go` → verify: missing DB is not created, invalid path access is reported accurately.

## Task 2: Pin the store key through dbsize and maintenance connections

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 1
- **Slicing strategy:** Risk-First — fresh maintenance connections must use the same credential as the pool
- **Docs:** [Captured key lifetime](implementation.md#2-a-captured-key-through-the-environment-only-dbsize-path)

### Subtasks

- [ ] 2.1 Add private captured key/source fields and `WithEncryptionKey` in `internal/store`; snapshot the environment by default and preserve explicit-empty failure → verify: two stores with different explicit keys work under an unrelated process key.
- [ ] 2.2 Route normal/recovery opens and `openSingleConn` through captured credentials; add explicit-key preflight in `encryption.go` → verify: preflight rejects plaintext and works without inherited credentials.
- [ ] 2.3 Extend `encryption_test.go` for environment changes, reopen-after-close, rebuild, vacuum, checkpoint, and close → verify: content survives and checkpoint sidecar assertions pass using the original captured key.
- [ ] 2.4 Create `cmd/capy/knowledge.go` helpers for project/strict-config selection and lazy store construction; route `dbsize.go` through them → verify: environment-only dbsize works and malformed config cannot select a fallback DB.

## Task 3: Read an explicitly configured key file through dbsize

- **Status:** pending
- **Depends on:** Task 2
- **Size:** M
- **Can run in parallel with:** Task 1
- **Docs:** [Explicit key files](implementation.md#3-explicit-project-key-files-through-dbsize)

### Subtasks

- [ ] 3.1 Add `store.key_file` and presence-aware merging in `internal/config/config.go` and `loader.go` → verify: omission inherits, empty clears, all three config layers and invalid types are covered.
- [ ] 3.2 Implement `Config.ResolveStoreKey` and safe source metadata in `internal/config/keys.go`, initially selecting explicit files or environment → verify: raw format, relative owner anchoring, absolute/symlink paths, and no fallback on explicit-file failure.
- [ ] 3.3 Wire resolution into `cmd/capy/knowledge.go` → verify: actual B database access through dbsize succeeds with A inherited and B configured.
- [ ] 3.4 Add owner/path coverage in `internal/config/keys_test.go` and `cmd/capy/key_resolution_cli_test.go` using existing worktree fixtures and isolated CLI projects → verify: linked, submodule, `../`, absolute, XDG, and DB-symlink targets preserve existing ownership without reading real credentials.

## Task 4: Support literal project dotenv credentials through dbsize

- **Status:** pending
- **Depends on:** Task 3
- **Size:** M
- **Can run in parallel with:** Tasks 1, 9
- **Docs:** [Dotenv compatibility](implementation.md#4-literal-dotenv-compatibility-through-dbsize)

### Subtasks

- [ ] 4.1 Create the isolated literal parser in `internal/config/dotenv.go` with declaration detection and structural validation → verify: accepted bytes match the grammar and embedded/malformed/duplicate/empty declarations fail without partial results.
- [ ] 4.2 Add owner dotenv, distinct main-worktree fallback, and inherited environment ordering to `keys.go` → verify: every precedence boundary, absent declarations, access errors, and explicit-file bypass are covered.
- [ ] 4.3 Test quotes, escapes, comments, LF/CRLF, false-positive names, and unsupported shell/multiline forms → verify: diagnostics contain no source lines, values, or DSNs.
- [ ] 4.4 Add sentinel-execution and vault/environment-isolation tests plus actual dbsize reads → verify: no command executes, no environment changes, B content remains readable, and no fallback database appears.

## Task 5: Use project credentials in direct MCP sessions

- **Status:** pending
- **Depends on:** Tasks 2, 4
- **Size:** M
- **Can run in parallel with:** Tasks 1, 9
- **Docs:** [Direct MCP integration](implementation.md#5-direct-mcp-startup-and-store-operations)

### Subtasks

- [ ] 5.1 Wire `serveRunE` to strict config, shared resolution, and explicit-key preflight → verify: bare invocation and flags before/after serve obey the same project contract.
- [ ] 5.2 Add `WithKnowledgeCredentials` and default environment capture to `internal/server/server.go`; pass the snapshot/source into `getStore` → verify: environment changes after server construction do not change the selected store key.
- [ ] 5.3 Add isolated binary/MCP tests in `cmd/capy/key_resolution_mcp_test.go` using two projects and crossed inherited keys → verify: actual index/search/doctor calls return project-specific content, then close/reopen preserves it.
- [ ] 5.4 Cover missing explicit credentials, wrong selected keys, and plaintext databases → verify: correct failure phase/message and no replacement after a wrong key.

## Task 6: Ship wrappers that reach the Go resolver

- **Status:** pending
- **Depends on:** Task 5
- **Size:** M
- **Can run in parallel with:** Tasks 1, 9
- **Docs:** [Wrapper rollout](implementation.md#6-generated-wrapper-rollout)

### Subtasks

- [ ] 6.1 Remove dotenv execution and serve-only key guarding from `capyWrapperScript`; regenerate both committed wrappers → verify: `TestGeneratedWholeFileArtifacts` passes.
- [ ] 6.2 Preserve binary discovery, hook zero-exit behavior, non-hook statuses, and MCP environment forwarding → verify: existing wrapper exit, merged-idempotency, and artifact-completeness tests pass.
- [ ] 6.3 Test generated wrappers against the candidate binary for key-file-only MCP, wrong inherited keys, bare/leading-flag invocation, dbsize, and no-key hook events → verify: actual database operations and exit contracts match the design.
- [ ] 6.4 Document direct binary upgrade/setup/restart steps and the old-wrapper limitation → verify: fresh and repeated setup for both platforms produce the correct artifacts.

## Task 7: Apply project credentials to cleanup and checkpoint

- **Status:** pending
- **Depends on:** Tasks 4, 6
- **Size:** M
- **Can run in parallel with:** Task 9
- **Docs:** [Maintenance integration](implementation.md#7-maintenance-commands-and-git-checkpointing)

### Subtasks

- [ ] 7.1 Integrate shared target/key helpers into `cleanup.go` and `checkpoint.go` → verify: B credentials override inherited A for dry-run, reclamation, and checkpoint without changing selector behavior.
- [ ] 7.2 Preserve missing-DB checkpoint no-op, surface other stat failures, and update `TestCheckpointSubcommand_BadConfig` to explicit failure → verify: malformed config creates no fallback DB/marker.
- [ ] 7.3 Exercise the project Git pre-commit path with local credentials and failing checkpoint fixtures → verify: genuine failures abort commits and the separate DB-repo guard remains keyless.

## Task 8: Explain credential selection in CLI and MCP doctor

- **Status:** pending
- **Depends on:** Tasks 1, 4, 5, 7
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Credential diagnostics](implementation.md#8-credential-diagnostics-on-both-surfaces)

### Subtasks

- [ ] 8.1 Add shared safe credential-result formatting in `internal/platform/doctor.go`; use it in CLI/MCP doctor → verify: file, dotenv, and environment sources are identifiable without exposing values.
- [ ] 8.2 Handle invalid config and resolver failures without selecting/opening a fallback target; continue independent checks → verify: FTS5/vault/runtime diagnostics remain available and no DB/marker is created.
- [ ] 8.3 Add parity tests in CLI and server doctor tests → verify: source selection and actual authentication results remain distinct, real open errors survive, and all synthetic secrets/DSNs are absent from output.

## Task 9: Preserve path inspection and key rotation contracts

- **Status:** pending
- **Depends on:** Task 3
- **Size:** M
- **Can run in parallel with:** Tasks 1, 4, 5, 6, 7, subject to their dependencies
- **Docs:** [Path and rotation compatibility](implementation.md#9-path-inspection-and-rotation-compatibility)

### Subtasks

- [ ] 9.1 Make `which.go` use strict config without credential lookup → verify: valid paths print with missing credentials and malformed config produces no fallback path.
- [ ] 9.2 Make `encrypt.go` reject invalid config before prompting; preserve separate old/new-key selection and add the project-credential update reminder → verify: old project credentials cannot override a new environment passphrase.
- [ ] 9.3 Add deterministic command rotation tests in `encrypt_test.go`, using a narrow injected prompt seam if required → verify: new key succeeds, old key fails, prompt fallback remains correct, and project credential files/vault environment remain unchanged.
- [ ] 9.4 Keep shared `main_test.go` edits isolated from Task 7's checkpoint test changes → verify: both sets of command expectations remain intact when integrated.

## Task 10: Verify the complete feature and update durable documentation

- **Status:** pending
- **Depends on:** Tasks 1, 2, 3, 4, 5, 6, 7, 8, 9
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Final verification](implementation.md#10-final-verification-and-durable-documentation), [Matrix](implementation.md#verification-matrix)

### Subtasks

- [ ] 10.1 Use `kk:test` to run the full regression matrix, `make test`, and `make test-race` with FTS5 and isolated synthetic keys → verify: required cases pass without weakening assertions or using real data.
- [ ] 10.2 Use `kk:document` to update README, architecture, root AGENTS, and the next available credential-policy ADR; cross-link the superseded environment-only part of ADR-019 → verify: setup, precedence, literal formats, rotation, and old-wrapper migration agree with implementation.
- [ ] 10.3 Run `kk:review-code` with Go input → verify: findings are fixed or recorded durably with reasons and concrete next steps.
- [ ] 10.4 Run `kk:review-spec` over the implementation and this feature directory → verify: design, implementation plan, and runtime behavior agree.
- [ ] 10.5 Check generated-artifact guards and final repository status; update task/feature status only after verification → verify: no leaked credentials, synthetic DBs, or temporary fixtures are committed.

## Dependency Graph

```text
Task 1 -----------------------------------------------> Task 8 --+
Task 2 -> Task 3 -> Task 4 -> Task 5 -> Task 6 -> Task 7 -> Task 8 |
            |                                                   |
            +--------------------> Task 9 -----------------------+
                                                                v
                                                            Task 10
```

Task 8 additionally depends directly on Tasks 4 and 5; Task 10 depends on every earlier task. Parallel markers denote independent behavior/dependencies, not permission to overwrite another contributor's edits. Coordinate the small shared test-file edits called out in Task 9.
