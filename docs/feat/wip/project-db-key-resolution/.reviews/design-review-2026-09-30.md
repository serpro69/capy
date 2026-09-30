# Design Review: Project database key resolution

**Scope:** design.md + implementation.md + tasks.md (investigation.md and `.reviews/cove-2026-09-30.md` read for context)
**Overall assessment:** CONCERNS_FOUND
**Documents:**

- Design: docs/feat/wip/project-db-key-resolution/design.md
- Implementation: docs/feat/wip/project-db-key-resolution/implementation.md
- Tasks: docs/feat/wip/project-db-key-resolution/tasks.md

**Summary:** 9 findings: 0 critical, 1 high, 3 medium, 5 low

Reviewed against baseline `d9e09c1`. Every codebase reference in the docs was verified (`DBProjectDir`/`MainWorktreeDir`/`ResolveDBPath` in `internal/config/paths.go`; `openDB`/`openSingleConn`/`Close`/`Checkpoint` in `internal/store/store.go`; `platform.CheckFTS5`; `capyWrapperScript` and `preCommitHookBlock` in `internal/platform/setup.go`; `TestCheckpointSubcommand_BadConfig` at `cmd/capy/main_test.go:200`; the `detectionOverlay` pointer pattern in `internal/config/loader.go`; Codex `env_vars` forwarding at `setup.go:550`).

---

## Findings

### P0 - Critical

(none)

### P1 - High

- **[MISSING]** Removing wrapper `.env` sourcing silently disables the vault in the motivating scenario
  - **Section:** design.md:Wrapper rollout; design.md:Acceptance criteria #7; design.md:Not Doing ("Vault credential selection changes")
  - **Confidence:** 7/10 — verified in `setup.go:42-55`: the old wrapper sources the *whole* main-worktree `.env` under `set -a`, which is how `CAPY_VAULT_KEY` reaches an MCP process started from a shell with no env (the linked-worktree case the wrapper comment documents). The design moves only the knowledge key into Go and deletes the sourcing. Vault is opt-in: unset key = "disabled", no error, session hits just vanish from `capy_search`.
  - **Description:** The design claims to preserve "vault access" and "vault isolation" and says knowledge resolution does not touch `CAPY_VAULT_KEY`. Both are literally true, but the *rollout* removes the only mechanism that loaded the vault key for users who relied on wrapper sourcing. After regenerating wrappers those users lose vault search silently.
  - **Evidence:** design.md "Keep current MCP environment forwarding for environment-only compatibility and vault access" — Codex `env_vars` forwarding only forwards what is already in the daemon's env; Claude's MCP config has no env block (cove report Q4). Neither replaces sourcing.
  - **Recommendation:** Decide explicitly and write it down: (a) accept the regression, name it a behavior change in the rollout section and README, and make CLI/MCP doctor say "vault disabled — `CAPY_VAULT_KEY` not in environment; the wrapper no longer sources `.env`"; or (b) extend the resolver contract to also read a literal `CAPY_VAULT_KEY` declaration (same parser, same precedence, injected via a vault constructor option) — this contradicts the current Not Doing item and must be a user decision. Either way, add a verification-matrix row for "worktree shell with no env, vault key only in `.env`".

### P2 - Medium

- **[TECH_RISK]** "Fail on use" for an explicit-empty key still creates the DB directory and `.project` marker
  - **Section:** design.md:Architecture and key lifetime ("an empty explicit value must fail on use"); design.md:Acceptance #3; implementation.md slice 8 ("no fallback XDG DB or `.project` marker appears")
  - **Confidence:** 8/10 — `store.go:112-119` runs `MkdirAll` and writes `.project` *before* `openDB()` is called.
  - **Description:** If the captured-key check lives in `openDB`/`openSingleConn` (where `RequireEncryptionKey` is today), a store constructed with an empty explicit key will still create the directory and marker on first use. That breaks the "does not create" acceptance criterion for the direct-server-construction doctor cases slice 1 and 8 add.
  - **Recommendation:** State in the design/implementation that the empty-captured-key check happens at the top of `getDB` (before mkdir/marker) and in `openSingleConn`, and add a test asserting no directory/marker after a fail-on-use.

- **[TECH_RISK]** Whole-file dotenv strictness is an upgrade-breaking change for some existing environment-only users
  - **Section:** design.md:Literal dotenv compatibility (lines 82-83); design.md:Acceptance #4
  - **Confidence:** 6/10 — deliberate trade-off, but its blast radius is under-stated.
  - **Description:** Today a user with `CAPY_DB_KEY=...` in the project `.env` plus *any* unsupported line elsewhere (multiline `PRIVATE_KEY="-----BEGIN…"`, `source`, `$(…)` on an unrelated var) works fine via their shell/direnv. After upgrade the resolver finds the candidate, requires the whole file to be literal assignments, and fails hard with no fallback to the inherited key. Acceptance #4 only promises compatibility when there is *no* declaration; the doc files this under "compatibility" rather than "breaking change".
  - **Recommendation:** (1) Name it a breaking change in Wrapper rollout and in the slice-10 ADR/README work. (2) Require the parser error to carry the migration hint (`store.key_file`) since that is the escape hatch. (3) Consider restricting structural validation to lines *before* the candidate: a candidate can only be embedded in a construct that opened earlier, so lines after it cannot retroactively swallow it. That keeps the safety argument and shrinks breakage.

- **[MISSING]** No rollback path
  - **Section:** design.md:Wrapper rollout
  - **Confidence:** 6/10
  - **Description:** The design has rollout steps but nothing on going back: an older binary that reads a config with `store.key_file` (does go-toml v2 ignore unknown keys in non-strict mode? — verify), regenerated wrappers that no longer source `.env` when paired with an old binary, and a project whose only key now lives in `.capy/db.key` (old binary: "CAPY_DB_KEY is required").
  - **Recommendation:** Add a short rollback paragraph: downgrade binary → re-run old `capy setup` → export `CAPY_DB_KEY` (or restore `.env` sourcing). Add a test that an unknown `key_file` key does not break the previous loader if go-toml is non-strict.

### P3 - Low

- **[INCOMPLETE]** Key-file reader should require a regular file and cap size
  - **Section:** design.md:Key file format
  - **Confidence:** 7/10
  - **Description:** "Follow symlinks normally" plus unbounded `ReadFile` means a FIFO blocks forever and a multi-GB file is read into memory. Add: stat must report a regular file after symlink resolution; read at most N bytes (e.g. 4 KiB) and reject larger.

- **[AMBIGUOUS]** A linked worktree's *own* `.env` is never consulted
  - **Section:** design.md:Precedence and failure
  - **Confidence:** 7/10
  - **Description:** With relative `store.path`, owner = main worktree, so step 2 reads main's `.env` and step 3 is skipped (same dir). The worktree's own `.env` is never read. This matches the old wrapper (git-common-dir) and is probably intended, but it is the first question a user will ask. State it in one sentence.

- **[STRUCTURE]** Task 7 over-serialized behind Task 6; graph ASCII ambiguous for Task 9
  - **Section:** tasks.md:Task 7 "Depends on: Tasks 4, 6"; Dependency Graph
  - **Confidence:** 6/10
  - **Description:** Only subtask 7.3 (pre-commit through the wrapper) needs Task 6. 7.1/7.2 could run alongside 6. The ASCII graph's Task 9 arrow visually joins the Task 7→8 edge; the prose fixes it but the picture misleads.

- **[STRUCTURE]** Tasks 4 and 5 read as L
  - **Section:** tasks.md:Task 4, Task 5
  - **Confidence:** 5/10
  - **Description:** Task 4 is a full grammar parser + precedence engine + isolation tests; Task 5 is serve wiring + server option + a subprocess stdio MCP harness with A/B projects. Either split (parser vs. precedence wiring; server option vs. harness) or justify M by pointing at the existing `capy(t, …)` binary helper in `main_test.go` as the harness.

- **[STRUCTURE]** Assumption 4 is operational, not testable
  - **Section:** design.md:Assumptions #4
  - **Confidence:** 6/10
  - **Description:** "Users stop attached servers before rotation" cannot be verified by a test. Reword as a documented precondition of `capy encrypt` (already true today) rather than an assumption, or drop it.

---

## Clean Areas

- **Precedence contract** is unambiguous and consistent across design, implementation slice 4, and tasks 4.2; the "never read lower-priority files after choosing" rule closes the fallback-after-malformed risk.
- **Key lifetime**: pinning one captured key across `openDB`, recovery, `openSingleConn`, `Close` checkpoint is correctly identified as the highest-risk seam (cove Q1 confirms both paths reread env today); ADR-016 pool-close-before-checkpoint ordering is preserved.
- **Layering**: resolver in `internal/config`, no store import, secret separated from `KeySource`, no env mutation — sound. Bonus: executor subprocesses no longer see the resolved key.
- **Encrypt rotation contract** kept separate; the cove report and design both catch the "inherited key means NEW passphrase" trap.
- **Generator/committed-artifact sync** rule from AGENTS.md is respected (slice 6 names both wrappers + drift tests).
- **Not Doing** items are genuine scope exclusions with rationale, except as noted in the P1 (vault) finding; deferred cove follow-ups are durably recorded with next steps.
- **Vertical slicing**: every slice lands an observable path through `dbsize`; not horizontal layers.
- **Task conventions**: Not Doing header, size tags, parallel markers, dependency graph all present.
- **Diagnostics**: no key/DSN disclosure rule stated on both surfaces; FTS5 decoupled from store init with the initialization side-effect (cove Q3) handled.
- **Verification matrix** is concrete and maps rows to owning slices.
