# Task 7 implementation review

> Scope: generated wrappers, vault migration guidance, rollout and rollback
> Review mode: `kk:review-code:isolated`, mid-implementation
> Reviewers: independent `code-reviewer` agent and PAL `gemini-3.1-pro-preview`

## Result

The independent agent approved the changes with no P0–P3 findings. PAL completed its two-step external review with no required fixes and one optional low-priority suggestion: diagnose missing Bash before attempting wrapper launches. The fixture now explicitly checks for Bash and Git and reports a clear prerequisite failure, preserving execution of the required credential cases. The independent reviewer approved that follow-up delta. No systemic P0/P1 findings to index.

The review covered the generator, both committed wrappers, shared doctor guidance and its CLI/MCP tests, real-binary integration tests, the frozen legacy decoding fixture, README migration instructions, and task bookkeeping. Go SOLID, removal, security, style, error-handling, injection, database, and naming checklists applied. Tasks 1–6 and 8 supplied completed context. Pending Tasks 9–11 were excluded from missing-implementation assessment. The independent agent inspected tests without running them; validation below comes from the implementing session.

## Implementation and coverage

The wrapper delegates knowledge credential selection to Go. It no longer executes dotenv or rejects `serve` before the resolver runs. Binary discovery, hook zero exits, other command exit propagation, and Codex environment forwarding are preserved. Both committed scripts were mechanically regenerated from `capyWrapperScript`; artifact equality, merge idempotency, and completeness guards pass.

The shared `CheckVaultDisabled` explains that wrappers no longer source `.env`, the actual launch environment must supply `CAPY_VAULT_KEY`, and the host/MCP process must restart. Platform, CLI, and MCP assertions cover the guidance. README documents literal key files, whole-file dotenv rejection even under correct inheritance, vault environment migration, direct setup for both platforms, restart and MCP verification, rotation operating steps, and rollback without database-format changes.

The existing stdio fixture builds the candidate and launches real setup-generated scripts through a temporary PATH. Both platform paths exercise key-file-only startup, wrong inherited keys with dotenv, bare invocation, a leading project override from another directory, environment-only launches, index/search/doctor, clean shutdown/reopen, wrapped dbsize, and keyless hooks. An executable dotenv sentinel remains absent when an explicit key file is selected. Setup runs twice and wrapper bytes remain identical.

Each platform's migration test creates an independent real linked worktree and synthetic vault. With both keys only in the main dotenv and neither inherited, knowledge reads succeed and MCP doctor reports the vault disabled with migration guidance. Supplying only the vault key in the child environment restores archive stats and a real vault search, even when dotenv declares a conflicting vault key. The imported source transcript is removed before startup so archive reads cannot be satisfied by a later discovery sweep. Knowledge remains owned by the main checkout.

Generated Git hooks exercise both key-file and dotenv selection under wrong inheritance. A read transaction pins a snapshot before a new WAL write; an actual commit fails with busy-checkpoint output and leaves HEAD unchanged. Releasing the reader lets checkpoint and commit succeed, and the committed database blob matches the checkpointed working file. Both indexed sources survive reopen. The separate DB-repo guard commits an encrypted, checkpointed fixture without a credential or wrapper.

The frozen v0.16.4 path-bearing config subset uses the existing TOML decoder with no `KeyFile` field and confirms unknown `key_file` leaves relative, parent-relative, and absolute `store.path` values intact. Its shape was checked against the local v0.16.4 tag; the decoder API was checked in the [pinned v2.2.4 documentation](https://pkg.go.dev/github.com/pelletier/go-toml/v2@v2.2.4#Unmarshal) after Context7 had no matching library. No module dependency changed. This is a decoding regression fixture, not a complete legacy-binary downgrade test. Actual legacy-binary and daemon-backed smoke checks were not run.

## Verification

- Focused FTS5 checks for generated wrappers/pre-commit, artifact guards, exit codes, disabled-vault diagnostics, and legacy decoding passed in the CLI, platform, config, and server packages, normally and with `-race`.
- The Codex-only vault migration subtest passed independently with `-race`. Author review had identified shared setup between platform subtests; each platform now constructs its own worktree/vault fixtures.
- Final wrapper/pre-commit race rerun passed after the fixture-isolation and prerequisite-diagnostic changes.
- Full `make test` passed with FTS5 enabled and both synthetic key variables set, using the corrected environment below.
- Targeted `go vet -tags fts5` for all four affected Go packages and `git diff --check` passed.

All credential fixtures are synthetic; child home/config/data/discovery/vault paths are temporary. No real project credentials or databases were used for validation. The race flag instruments the test process, including the SQLite contention fixture; the launched candidate remains an ordinary FTS5 build. Search/indexing algorithms did not change, so quality benchmarks were not required.

The first full-suite invocation incorrectly supplied one shared `CLAUDE_CONFIG_DIR`, overriding existing tests' per-test HOME isolation and causing unrelated config and vault-sweep assertions to fail. The corrected invocation uses a clean environment with temporary HOME/XDG roots and both synthetic keys, leaving session-discovery overrides unset. This is the existing test-environment constraint recorded in the [Task 1 review](task-1-code-review-2026-09-30.md#environment-notes-and-deferred-test-portability); no assertions were weakened.

No design deviation or new project convention was needed. Expanded knowledge credential diagnostics, remaining CLI integration, and final architecture/ADR/AGENTS updates remain in Tasks 9–11.
