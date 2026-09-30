# Project database key resolution implementation plan

> Status: In progress — Tasks 1–6 and 8 complete; Tasks 7, 9–11 pending
> Design: [design.md](design.md)
> Tasks: [tasks.md](tasks.md)
> Baseline: `d9e09c1`
> Review corrections: [Finding dispositions](.reviews/design-review-reconciliation-2026-09-30.md)

## Implementation boundaries

Implement the agreed [credential contract](design.md#credential-selection) and [command behavior](design.md#command-and-diagnostic-behavior). Keep database path selection, executor/security project scope, vault configuration, encryption format, and retrieval behavior intact. No new dependency is required. If implementation proposes a library, apply `kk:dependency-handling` before adding or recommending it.

Use native file edits and synthetic credentials. Do not read the repository's real `.env`, copy real keys into fixtures, or use the live knowledge/vault database for validation. Isolate `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, and project directories in tests; clear inherited project selectors. Disable the vault except in explicit migration tests, which must isolate `CAPY_VAULT_PATH` and the child process's Claude/Codex discovery roots as well as its synthetic vault key.

The steps below are vertical slices. Each makes an observable path work and names its verification. The store lifetime slice comes first in the credential sequence because fresh maintenance connections are the highest-risk integration point. Temporary differences between paths are explicit in the task dependencies; the feature is not complete until all paths are integrated.

## 1 Independent FTS5 diagnostics

Primary files: `internal/server/tool_doctor.go`, `internal/server/tool_utility_test.go`, `cmd/capy/doctor.go`, `cmd/capy/cleanup_doctor_test.go`.

- Replace MCP doctor's stats-based FTS5 block with `platform.CheckFTS5()`. Move explicit `getStore()` initialization into the knowledge section and call stats there once. Remove imports made unused by this change only. → verify: a pre-existing encrypted fixture opened with the wrong key reports available FTS5 and the actual knowledge error.
- Add missing-key and filesystem/open-error variants through direct server construction, since normal serve rejects a missing key before MCP starts. Preserve current initialized-empty-store behavior and legacy-session hints. → verify: doctor tests exercise these cases without treating a successful initialize handshake as database access.
- In CLI doctor, treat only `os.IsNotExist` as an absent database; preserve other stat errors as diagnostic failures. → verify: the existing no-creation test still passes, and a deterministic invalid path component produces an access error rather than “not initialized.”

This slice is independent of credential resolution and may land first.

**Completed 2026-09-30.** Both diagnostic paths now distinguish FTS5 capability from knowledge access failures. Regression tests cover wrong/missing keys, a database path pointing to a directory, invalid CLI path components, preserved encrypted content, empty-store initialization, and missing-DB/marker non-creation. `make test` and focused doctor tests with and without `-race` passed. Both isolated reviewers reported no findings. See the [review and validation record](.reviews/task-1-code-review-2026-09-30.md), including unrelated test-environment follow-ups. Credential selection remains environment-only until the dependent slices land.

## 2 A captured key through the environment-only dbsize path

Primary files: `internal/store/store.go`, `internal/store/encryption.go`, `internal/store/encryption_test.go`, new `cmd/capy/knowledge.go`, `cmd/capy/dbsize.go`.

- Add private key/source fields and a `WithEncryptionKey` constructor option carrying the secret and safe source hint. The default constructor captures the environment once; explicit empty input overrides the default and fails on use. Route `openDB`, recovery retries, and `openSingleConn` through the captured value. → verify: two stores with different explicit keys operate concurrently under one unrelated environment key.
- Check for an empty captured key at the top of `getDB`, under its mutex and before mkdir/marker/open/recovery, and separately before `sql.Open` in `openSingleConn`. → verify: direct store use and standalone checkpoint with an explicit empty key leave a previously absent DB directory, `.project` marker, database, and sidecars absent, even when the environment contains a valid key. Add the corresponding direct-server doctor test in slice 6, when its explicit-credential option exists.
- Add an explicit-key preflight alongside the existing environment-based helper. Share validation of empty/short keys without changing the short-key warning policy. Use the captured safe source hint in wrong-passphrase errors while retaining `sqliteutil.IsWrongPassphrase` classification. Avoid changing vault callers or the shared encryption algorithm. → verify: explicit-key preflight works with an empty process environment and still rejects plaintext databases.
- Exercise reopen-after-close, normal access, rebuild, vacuum, public checkpoint, and close/checkpoint after changing the environment. Keep the connection pool closed before the final checkpoint. → verify: encrypted content survives, sidecars satisfy existing checkpoint expectations, and a new store using the changed environment cannot authenticate the old database.
- Introduce shared command helpers in `cmd/capy/knowledge.go`: `commandProjectDir` for existing project selection, `loadKnowledgeTarget` for strict config plus resolved owner/path, and `newKnowledgeStore` for lazy construction from a target and credential. Initially use the captured environment key and route dbsize through these helpers. Keep selection separate from opening so later doctor/checkpoint callers can avoid creating a missing database. → verify: existing dbsize behavior works through the refactored path; invalid config stops before selecting a default database.

Keep helpers small: resolve the target separately, resolve its credential separately, then construct the lazy store. Do not put credential loading in a root persistent pre-run hook; help, setup, vault, and Claude hook commands must remain independent.

**Completed 2026-09-30.** `NewContentStore` captures the environment once, with `WithEncryptionKey` overriding both key and safe source hint. All store connections use that snapshot; empty values fail before filesystem side effects. `ValidateEncryptionReadyWithKey` shares the existing missing/short-key validation and plaintext preflight. `dbsize` now uses the strict target helpers in `knowledge.go` and constructs a lazy store with its selected environment credential. Tests cover concurrent explicit keys, environment changes before lazy open and during maintenance, close/reopen, corruption recovery, wrong-key content preservation, source-safe errors, absent directory/marker/sidecars on empty input, and config parse/type/validation/read failures. Full `make test` and focused race checks passed; isolated review found only a formatting issue, now fixed. See the [review and validation record](.reviews/task-2-code-review-2026-09-30.md). File/dotenv selection and command/server integration remain assigned to Tasks 3–10.

## 3 Explicit project key files through dbsize

Primary files: `internal/config/config.go` (mechanical field declaration), `internal/config/loader.go`, new `internal/config/keys.go`, `cmd/capy/knowledge.go`, new `internal/config/keys_test.go` and `cmd/capy/key_resolution_cli_test.go`.

- Add `StoreConfig.KeyFile` and presence-aware overlay handling. Explicit empty clears a lower-layer key-file setting; omission inherits. Keep other fields' merging unchanged. → verify: table cases cover all three layers, omitted values, clearing, malformed types, and unchanged `store.path` behavior.
- Add `Config.ResolveStoreKey(projectDir)` returning the key separately from safe source metadata. Implement explicit-file selection, relative-owner anchoring, absolute paths, the raw-file format, and environment fallback when no file is configured. Require a regular target before open, recheck the opened descriptor, and bound reads to limit plus one byte; raw key files allow 4,096 bytes including a terminal newline. Reuse the file-admission helper with a 1 MiB limit for dotenv in the next slice. → verify: an explicit B key file overrides inherited A; missing/empty/invalid, FIFO/device/directory, and over-limit inputs fail without fallback or blocking on a pre-existing FIFO. Cover a regular symlink, a symlink to a FIFO, the exact limit, and limit plus one.
- Wire the resolver into the shared command helper used by dbsize. Errors include safe source/target context without secret values or DSNs. → verify: a real encrypted B database is readable through `capy dbsize --project-dir B` with A inherited, from an unrelated cwd.
- Cover normal checkout, linked worktree, submodule, absolute/XDG paths, relative `../` paths, and a symlinked DB/credential file. Do not load config from the main worktree or canonicalize project aliases. → verify: resolved DB paths match existing path rules and only the intended database is opened.

Use existing `MainWorktreeDir`, `DBProjectDir`, and `ResolveDBPath`; add no second Git-layout implementation. Recognize `key_file = ""` through the existing pointer-overlay pattern. File validation errors must not echo file contents.

**Completed 2026-09-30.** `store.key_file` now uses presence-aware merging across all three configuration layers. `Config.ResolveStoreKey` returns the selected secret separately from a safe `KeySource`; explicit files use the existing database owner, preserve literal bytes except one terminal LF/CRLF, reject invalid/nonregular/oversized input, and never fall back after an error. `dbsize` resolves before constructing its lazy store. Regression tests cover config clearing/types, raw byte limits, regular and FIFO symlinks, directory/device/socket rejection, permission errors, linked worktrees, submodules, relative/absolute/XDG paths, project aliases, and DB symlinks. Actual encrypted database reads succeed with a conflicting inherited key; wrong file keys preserve retained content, resolution failures leave database directories absent, and file changes affect newly resolved stores only. Full `make test`, focused normal/race checks, and targeted vet passed. Both isolated reviewers reported no findings; see the [review and validation record](.reviews/task-3-code-review-2026-09-30.md). No design deviation or new dependency was needed. Dotenv and other command integrations remain in their pending slices.

## 4 Literal dotenv compatibility through dbsize

Primary files: new `internal/config/dotenv.go` and tests, `internal/config/keys.go` and its tests, the CLI credential test file from slice 3.

Size is M: five bounded target files, one fixed single-line grammar, and an existing resolver/file-admission seam from slice 3. This task does not implement general dotenv or shell evaluation, add a dependency, or build an MCP harness. Keep those boundaries rather than expanding the parser until the task becomes L.

- Implement the precise [literal grammar](design.md#literal-dotenv-compatibility) as a pure parser. Detect target declaration candidates before requiring whole-file assignment syntax, so unrelated readable dotenv files without a target remain compatible with environment-only usage. When a candidate exists, parse enough structure to reject candidates embedded in unsupported multiline/shell input. → verify: malformed structure cannot yield a selected key.
- For unquoted target values, reject whitespace within the value, quote/backslash syntax, expansion markers, and shell operators; require quoting for those literal characters. Quoted targets follow the design's escape/comment rules. Reject duplicate/empty targets and unsupported syntax explicitly. → verify: table tests distinguish literal bytes, comments, quotes, malformed declarations, CRLF, false-positive variable names, and duplicate keys.
- Insert owner dotenv, distinct main-worktree fallback, then inherited environment into the resolver. Absent files/absent declarations continue; access or declaration errors stop. Explicit key files bypass dotenv completely. → verify: ordered fixtures cover every precedence edge, including an invalid lower-priority file that must never be read.
- Make whole-file rejection carry the safe source/line and `store.key_file` migration hint, retaining checks after the first declaration. → verify: an inherited valid key does not bypass unsupported syntax before or after a target; a later duplicate fails; selecting a valid explicit key file bypasses those same files. A relative-path linked worktree ignores its own conflicting dotenv; absolute/XDG modes check its own first.
- Include a dotenv file containing a synthetic vault assignment and attempted command substitution that would create a sentinel if executed. → verify: resolution rejects unsupported target syntax, creates no sentinel, and changes neither the vault key nor any process environment variable.
- Exercise actual dbsize reads with B's dotenv and A inherited, plus environment-only operation with no declaration. → verify: content remains readable with B after wrong-key attempts and no alternate default DB is created.

Do not run a shell, use regex as a substitute for quote/assignment parsing, or accept a partial parser result after an error. No source lines or values belong in parser diagnostics.

**Completed 2026-09-30.** The pure literal parser detects exact declarations and validates the entire file before returning a key. The resolver now selects owner dotenv, distinct main-worktree fallback, then environment, while explicit key files bypass dotenv. The existing file-admission helper enforces the 1 MiB raw limit. Regression coverage includes literal bytes and unsupported syntax, false-positive names, duplicates, embedded declarations, precedence and file-access boundaries, sentinel execution, environment/vault isolation, and real encrypted dbsize reads under conflicting inherited keys. Wrong selected keys preserve content; resolution errors leave DB directories/markers absent and retain safe source/line context with the key-file migration hint. Full `make test`, focused normal/race checks, and targeted vet passed; both isolated reviewers reported no actionable findings. See the [review and validation record](.reviews/task-4-code-review-2026-09-30.md). No design deviation or external dependency was needed. Command/server integration and rollout remain in Tasks 5–11.

## 5 Baseline stdio MCP transport verification

Primary files: new `cmd/capy/mcp_stdio_helpers_test.go` and `cmd/capy/mcp_stdio_test.go`.

This independent Risk-First slice adds passing coverage of the existing environment-only MCP path before credential integration. `capy(t, ...)` in `main_test.go` runs `go run` with no stdin; it is a CLI command helper, not a reusable MCP transport harness. Existing `internal/server` helpers call handlers in process and do not cover stdio either.

- Build one FTS5-enabled candidate binary per test fixture and add narrowly scoped helpers for child environment/cwd, piped stdin/stdout, JSON-RPC initialization and request IDs, deadlines, and cleanup/Wait. Capture stderr separately and terminate/reap children on timeout. Do not build a general protocol framework. → verify: an environment-only initialize → index → search → doctor → clean shutdown/reopen round trip passes against current behavior.
- Exercise two subprocesses with separate temporary projects and their respective correct environment keys, then reopen their databases. → verify: each returns only its own marker and retains content; this establishes the transport oracle without depending on the new resolver.
- Keep transport helpers reusable by the credential and wrapper cases in slices 6 and 7. → verify: request handling does not depend on sleeps, EOF-only startup, or exposing key values in diagnostics.

**Completed 2026-09-30.** The new stdio fixture builds one FTS5 binary per parent test and reuses it across isolated child processes and reopen checks. It sends initialization and the initialized notification, matches numeric request IDs, accepts intervening notifications, and bounds both pipe reads and writes. Separate bounded stderr capture redacts supplied synthetic credentials. Explicit EOF shutdown verifies a clean exit; registered cleanup terminates and reaps abandoned or failed children. Regression cases exercise early exit, read/write deadlines, mismatched IDs, protocol errors, stalled shutdown, and automatic cleanup without sleeps. Real index/search/doctor calls cover one project and two simultaneously running projects with distinct environment keys; queries omit the expected markers, reopen does not re-index, and shutdown leaves encrypted databases with no pending WAL content. Full `make test`, focused normal/race tests, and targeted vet passed. The race run instruments the harness; the launched candidate is a normal FTS5 build. Independent review found no correctness issues; two external low-priority cleanup suggestions were addressed. See the [review and validation record](.reviews/task-5-code-review-2026-09-30.md). No production code or dependency changed. Credential integration and wrapper cases remain in Tasks 6 and 7.

## 6 Direct MCP startup and store operations

Primary files: `cmd/capy/serve.go`, `internal/server/server.go`, `internal/server/server_test.go`, new `cmd/capy/key_resolution_mcp_test.go`.

- Route serve and bare invocation through strict config/credential selection and explicit-key preflight. Add `WithKnowledgeCredentials` to server construction; capture environment defaults once for existing internal callers. Pass the stored key/source to `getStore` without changing working-project security/executor scope. → verify: wrong inherited keys cannot override project files, and changing the environment after construction cannot change the server's store key.
- Reuse slice 5's binary/stdio fixture to drive actual index, search, and doctor calls. Run A/B subprocesses with crossed inherited keys and their local credentials, then close and reopen them. → verify: each returns its own indexed marker, reports correct diagnostics, preserves data, and checkpoints its database.
- Cover bare invocation, `--project-dir` before/after serve, missing explicit credentials, wrong selected credentials, and plaintext DB rejection. Also construct a server directly with an explicit empty credential and invoke doctor, bypassing serve preflight. Disable/isolate vault and global config in the fixture. → verify: missing credentials produce clear stderr before MCP starts; the direct-server case leaves DB directory/marker/files absent despite a nonempty inherited key; wrong nonempty keys remain visible on actual tool calls under the existing lazy-open contract.

The direct binary path is complete at this point. Generated MCP launches require the next slice because old wrappers can exit before Go runs.

Size is M: serve wiring, the server option, and credential-specific tests span four primary files. Transport/process-harness construction is already complete in slice 5.

**Completed 2026-09-30.** Bare invocation and `serve` now use strict target loading, shared credential resolution, and explicit-key preflight. `WithKnowledgeCredentials` supplies the captured key and safe source metadata; default server construction snapshots the environment once, and lazy store construction preserves that selection. Real stdio tests cover simultaneous key-file/dotenv projects under crossed inherited keys, index/search/doctor, clean shutdown/reopen, argument placement, missing/invalid credentials/configuration, plaintext rejection, and wrong-key content preservation. Direct server tests cover environment/file changes before lazy initialization and explicit-empty doctor without filesystem side effects. Full `make test`, focused normal/race checks, and targeted vet passed. Isolated review identified a startup-test scheduling race, now fixed and re-reviewed; no production defect was reported. See the [review and validation record](.reviews/task-6-code-review-2026-09-30.md). No design deviation or new dependency was needed. Generated wrappers, other CLI paths, and expanded diagnostic formatting remain in their assigned pending tasks.

## 7 Generated wrapper rollout and migration

Primary files: `internal/platform/setup.go` and wrapper tests, `internal/platform/doctor.go` and disabled-vault tests, and README rollout guidance. Update both generated wrapper copies mechanically; place real-binary cases in the existing CLI stdio fixture. Depends on slices 6 and 8: the full generated path includes Git checkpointing, while direct cleanup/checkpoint can be implemented independently in slice 8.

- Remove shell dotenv sourcing and the serve-only key guard from the generator and both committed wrappers. Preserve binary discovery, hook behavior, non-hook exit propagation, and Codex environment forwarding. → verify: existing wrapper exit-code and artifact drift/idempotency tests pass.
- Exercise generated wrappers with the candidate binary selected through a temporary PATH. Cover key-file-only MCP startup with no inherited key, dotenv with a wrong inherited key, bare invocation, leading global flags, wrapped dbsize, and hook execution without knowledge credentials. → verify: actual tool/database reads match the direct binary cases and hook exits remain zero.
- Update shared `CheckVaultDisabled` wording and its tests to explain that wrappers no longer source `.env`, and that the actual MCP launch environment must supply `CAPY_VAULT_KEY`. Do not introduce a vault file resolver. → verify: CLI and MCP doctors display the migration hint when the key is absent.
- Test a linked-worktree launch with both keys only in the main dotenv and no inherited keys, using an isolated vault fixture. Then relaunch with only the vault key supplied in the child environment. → verify: knowledge works in both runs; the first reports disabled vault with migration guidance, the second reads the isolated vault with the supplied key, and no project assignment overwrites it.
- Exercise the generated project pre-commit checkpoint path after slice 8, using local credentials and a deliberately failing checkpoint; keep the DB-repo guard keyless. → verify: correct local keys work despite wrong inheritance, while genuine checkpoint/busy failures abort the commit.
- Document dotenv and vault breaking changes, upgrade/setup/restart, and [rollback](design.md#rollback). Add a frozen v0.16.4-shaped config decoding fixture that ignores unknown `key_file` while retaining `store.path`; record a legacy-binary smoke check when available rather than requiring downloads in routine tests. → verify: the legacy fixture passes; upgraded wrappers and an environment-only launch work with explicit old-style credentials; fresh/repeated setup is idempotent. The reconciliation already records a v0.16.4 black-box path-resolution probe, not a completed downgrade MCP test.

Do not change generated routing, hook registration, or MCP configuration unless required by an actual implementation change. If changed, update their committed counterparts and existing drift coverage together.

## 8 Direct maintenance commands

Primary files: `cmd/capy/cleanup.go`, `cmd/capy/checkpoint.go`, `cmd/capy/main_test.go`, `cmd/capy/cleanup_doctor_test.go`.

- Route cleanup and checkpoint through strict target/credential helpers. Preserve cleanup selectors and dry-run/reclamation semantics. Preserve checkpoint's missing-DB no-op and treat other stat failures as errors. → verify: synthetic B credentials override inherited A for cleanup dry-run, explicit checkpoint, and cleanup reclamation.
- Replace `TestCheckpointSubcommand_BadConfig`'s fallback expectation with explicit failure and assert no default DB/marker is created. Keep the test's original triggering malformed config. → verify: failure occurs before credential lookup/open and clearly names configuration failure.
- Exercise direct cleanup/checkpoint with local credentials and deliberately failing checkpoint fixtures, using an isolated CLI environment. → verify: genuine checkpoint/busy failures retain nonzero status before wrapper integration.

This slice depends only on credential selection (slice 4), so it can run alongside the direct MCP work. Generated pre-commit integration belongs to slice 7, which depends on this slice; no partial task completion is needed to relax the old serialization. Reuse existing checkpoint tests. Do not broaden this slice into a redesign of close-error reporting.

**Completed 2026-09-30.** Cleanup and checkpoint now use the shared strict target loader and credential resolver. Cleanup retains its selectors, dry-run behavior, and reclamation flow; checkpoint checks for an absent database before requiring credentials and returns other stat errors explicitly. Regression tests exercise key-file/dotenv credentials under conflicting inheritance, actual cleanup and retained content, invalid configuration without fallback creation, missing/invalid/wrong local credentials, and a live read transaction that makes checkpoint fail with busy pages. The original malformed-config fixture now requires failure. Full `make test`, focused normal/race tests, and targeted vet passed; both isolated reviewers reported no findings. See the [review and validation record](.reviews/task-8-code-review-2026-09-30.md). No design deviation or dependency change was needed. Task 7 can now integrate generated wrappers and the pre-commit path.

## 9 Credential diagnostics on both surfaces

Primary files: `cmd/capy/doctor.go`, `internal/server/tool_doctor.go`, `internal/platform/doctor.go`, `cmd/capy/cleanup_doctor_test.go`, `internal/server/tool_utility_test.go`.

- Add shared formatting for a safe knowledge-credential source/result, with no secret-bearing object passed to formatting/logging. CLI doctor uses target/resolver helpers while retaining its no-create behavior and diagnostic exit convention. MCP doctor reports the source captured by its server and keeps the independent FTS5 check from slice 1. → verify: both surfaces distinguish selection success from authentication failure.
- With invalid config, report Config failure and skip target-dependent knowledge checks while continuing independent checks. With credential resolution failure, report that failure and avoid opening the DB. → verify: no fallback XDG DB or `.project` marker appears, FTS5 remains testable, and vault/runtime checks still run independently.
- Preserve actual opening errors and show correct guidance for key-file, dotenv, and environment sources. → verify: diagnostics contain expected source/path context but none of the synthetic key strings, assignment lines, or encryption DSNs.

Keep stats-error propagation and general shutdown reporting follow-ups outside this slice as recorded in the verification report.

## 10 Path inspection and rotation compatibility

Primary files: `cmd/capy/which.go`, `cmd/capy/encrypt.go`, `cmd/capy/main_test.go`, `cmd/capy/encrypt_test.go`.

- Make `which` use strict configuration without reading/requiring credentials. → verify: it prints the configured target with missing keys/key files, but fails on malformed config without printing a fallback target.
- Make encrypt fail on invalid config before prompting or opening a DB. Keep normal-access credential resolution out of its new-key path; an existing key file/dotenv must not replace an explicitly supplied new environment key. State the stop-attached-processes precondition in help and add a success reminder to update project credentials before restarting. → verify: guidance is present, malformed config does not prompt, and rotation with an old project credential and a new environment key produces a DB readable with the new key, not the old one. Do not claim a test can enforce operator behavior.
- Test the command's passphrase selection without relying on a developer's controlling terminal. If needed, introduce a narrow unexported prompt-function seam in `encrypt.go` that defaults to the existing terminal helpers; tests supply old/new answers through that seam. Do not change the terminal package or add a PTY dependency. → verify: the new-key prompt is used only when the environment key is absent, and old-key prompting remains intact.
- Verify project credential files are not rewritten and vault credentials are unchanged. → verify: byte-for-byte file comparison and vault-key assertions accompany the rotation test.

## 11 Final verification and durable documentation

Dependencies: all previous slices. This is a verification/documentation task, not a place to defer implementation or first integrate missing command paths.

- Run the full [matrix](#verification-matrix), then the FTS5-tagged suite and race checks appropriate to shared store lifetime. Use `kk:test`; required commands include `make test` and `make test-race` with both synthetic key variables set. → verify: all pass without weakened assertions, real-data access, or skipped new credential cases.
- Run `kk:document` to update README setup/configuration/troubleshooting/rotation, architecture credential flow, and the root AGENTS encryption invariant. Add the next available numbered ADR for this policy, cross-link ADR-019's superseded environment-only rule, and prominently record the whole-file dotenv break, environment-only vault migration, file limits, and rollback procedure. Preserve encryption/WAL invariants. → verify: docs contain the concrete migration/recovery steps and do not claim either that the knowledge key must exclusively come from the environment or that forwarding loads absent vault keys.
- Run `kk:review-code` with Go input and `kk:review-spec` over this feature. → verify: findings are fixed or durably recorded with specific reasons and next steps; docs match actual command/format behavior.
- Complete all task statuses only after passing verification. → verify: the repository diff contains the intended source/docs/generated copies and no synthetic key files, databases, or test artifacts.

## Verification matrix

| Dimension | Required cases | Observable assertion |
| --- | --- | --- |
| Two projects | A/B keys, crossed inherited values, simultaneous MCP processes | Index/search return only each project's marker; reopen retains content |
| Explicit key file | Relative/absolute/regular symlink; omitted, cleared, missing, unreadable, empty, invalid; FIFO/device/directory; 4,096/4,097 raw bytes | Correct source or explicit bounded failure; no blocking on a pre-existing FIFO or lower-source fallback |
| Dotenv | Owner/main fallback, conflicting linked dotenv, no declaration, literal grammar, duplicate/invalid/empty declaration, unsupported lines before/after target, nonregular/over-1-MiB file | Exact selected bytes or safe errors with key-file migration hint; inherited key never bypasses invalid declaration; explicit key file bypasses dotenv |
| Execution isolation | Target shell substitution; unrelated vault assignment | No sentinel creation, environment mutation, or vault-key change |
| Environment compatibility | No project declaration, no dotenv, unrelated dotenv, missing inherited key | Existing environment-only operation or clear missing-credential error |
| Target identity | Git subdir cwd; explicit override from another repo; linked worktree; submodule; relative `../`, absolute, XDG, DB symlink | Existing resolved DB/owner contract preserved |
| Invalid config | Parse/read/validation errors, previously successful checkpoint fallback | No default target/open/marker; doctor reports and continues independent checks |
| Key lifetime | Explicit-empty direct store/server; environment/key-file changes after construction; recovery; reopen; rebuild/vacuum/checkpoint/close | Empty key leaves DB directory/marker/files absent; otherwise one captured key per store, retained content and proper sidecars |
| Diagnostics | Wrong/missing key, DB access error, missing DB | FTS5 independent; source and real error preserved; no secret/DSN disclosure |
| Rotation | Old project credential plus new environment key; prompt fallback | New key succeeds, old fails, source files unchanged, vault unaffected |
| Wrappers/hooks | Both platforms, bare/leading-flag serve, CLI, no-key Claude hooks, Git checkpoint failures | New binary reached, real reads succeed, correct exit statuses |
| Vault migration | Worktree with both keys only in main dotenv and no inherited keys; repeat with vault key in actual launch environment | Knowledge works; first vault is disabled with migration hint, second vault is readable; no vault override from dotenv |
| Rollback compatibility | Frozen old config shape with unknown key_file; explicit environment credentials with regenerated wrappers; optional actual legacy-binary smoke check | Old-shaped loader retains store.path and ignores key_file; environment-only access works without credential-file interpretation |
| Generated artifacts | Whole-file equality, merged idempotency, completeness guard | Generator and committed artifacts agree |

All rows require implemented automated coverage except the optional real-host smoke check below. Place cases in the owning slice; reuse fixtures/helpers instead of mirroring production selection logic in expected-value calculations. For inaccessible-file behavior, use a deterministic reader seam or skip a permission-mode assertion only when the test process demonstrably bypasses it; another deterministic failure test must still exercise the error path.

After automated coverage, an optional smoke check in daemon-backed Codex may use two disposable projects and synthetic credentials. Record host version and observed reads if performed. Do not claim the original daemon's inherited key was identified or that a host smoke check ran unless it did.

## Review provenance and deferred findings

The isolated verification report covers current-code facts, not this candidate's runtime correctness. Its [follow-ups](.reviews/cove-2026-09-30.md#additional-findings-and-follow-ups) explicitly defer suppressed stats-query errors and ignored close errors to separate changes with concrete next steps. No implementation slice may hide a newly discovered blocking credential-resolution issue in those follow-ups.
