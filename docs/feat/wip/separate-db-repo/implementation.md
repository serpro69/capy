# Implementation: First-class support for a separate knowledge-DB repo

> Design: [./design.md](./design.md)
> Tasks: [./tasks.md](./tasks.md)
> Issue: [#90](https://github.com/serpro69/capy/issues/90)

Audience: an experienced Go developer with no capy context. Read
[design.md](./design.md) first; this file tells you *where* and *how*.

## Orientation — files you will touch

| Area | File | What lives there today |
|---|---|---|
| Generator | `internal/platform/setup.go` | `capyWrapperScript` (const, whole wrapper), `SetupClaudeCode`, `SetupCodex`, `preCommitHookBlock(dbPattern)`, `preCommitHookScript`, `installPreCommitHook(projectDir)`, `resolveDBPattern`, `ensureGitignoreEntry`, `shellEscapePath`, markers `preCommitMarkerStart/End`. |
| Generator tests | `internal/platform/precommit_test.go`, `setup_test.go`, `setup_artifacts_drift_test.go` | Hook text assertions; setup idempotency; **drift guards**. |
| CLI | `cmd/capy/setup.go` | `newSetupCmd`: `--platform`, `--binary`, `--local`, `--project`. |
| CLI | `cmd/capy/encrypt.go` | `runEncrypt` → `encryptPlain` / `rekeyEncrypted`; both end in a rename-onto-`dbPath` swap. |
| Committed artifacts | `.claude/scripts/capy.sh`, `.codex/scripts/capy.sh` | Byte-identical copies of `capyWrapperScript`. |
| Docs | `README.md`, `docs/adr/016-wal-mode-and-checkpoint-strategy.md` | |

### The drift-guard contract (read before touching `setup.go`)

`setup_artifacts_drift_test.go` enforces AGENTS.md's rule that every generated
artifact has a byte-identical committed copy in this repo:

- `wholeFileArtifacts` — generator output must equal the committed file
  (`capyWrapperScript` ↔ both `capy.sh` copies).
- `TestDriftGuardCoversEverySetupArtifact` runs `SetupClaudeCode` + `SetupCodex` in a
  temp dir and fails if any written file is not registered or exempted
  (`exemptSetupPaths`; `.git/hooks/pre-commit` is already exempt).

For this feature: (a) the wrapper edit lands in the generator **and both committed
copies** in one commit; (b) `SetupDBRepo` is added to the completeness test in its own
temp repo. It writes only `.gitignore` (already in the base `covered` map) and the hook
(exempt), so no new registrations are expected — the test proves that.

### Existing primitives to reuse

- `ensureGitignoreEntry(path, entry)` — idempotent append.
- `installPreCommitHook` replace-or-append logic keyed on the markers.
- `shellEscapePath` for anything interpolated into single quotes.
- Test style: `testify`, `t.TempDir()`, fake `.git/hooks` dir; skip shell tests when
  `sh`/`bash`/`git` are absent (`exec.LookPath`).

---

## Task 1 — Wrapper exit-code propagation + project-hook `|| exit 1`

**Files:** `internal/platform/setup.go`, `.claude/scripts/capy.sh`,
`.codex/scripts/capy.sh`, `internal/platform/setup_test.go`, `precommit_test.go`.

1. In `capyWrapperScript`, restructure the binary-search loop body:
   `if [ "${1:-}" = "hook" ]; then "$p" "$@" || true; exit 0; fi` then `exec "$p" "$@"`.
   After the loop (binary not found): `hook` → existing `jq` deny JSON + `exit 0`;
   otherwise a one-line stderr error listing the searched locations and `exit 127`.
   Fix the header comment ("must always exit 0 **for hook events**").
   → verify: `TestGeneratedWholeFileArtifacts` fails until step 2.
2. Copy the generator output byte-for-byte into both committed `capy.sh` files.
   → verify: `go test -tags fts5 ./internal/platform/ -run 'Drift|Generated|Idempotent'`.
3. Shell-level wrapper test: write the generator output to a temp file; fake `capy` on
   `PATH` exiting `$FAKE_EXIT`. Assert: `bash w checkpoint` exits `FAKE_EXIT`;
   `bash w hook x` exits 0; with `PATH` emptied, `bash w checkpoint` exits 127 and
   `bash w hook x` exits 0 printing the deny JSON.
   → verify: removing `exec` makes the first assertion fail.
4. `preCommitHookBlock`: magic check inside
   `printf '%s\n' "$staged_dbs" | while IFS= read -r f; do …; done || exit 1`;
   `bash "<wrapper>" checkpoint || exit 1`; delete the dead `if [ $? -ne 0 ]` line.
   → verify: existing assertions hold; add `Contains(block, "checkpoint || exit 1")`
     and `NotContains(block, "$? -ne 0")`.

## Task 2 — `capy encrypt` symlink-safe

**Files:** `cmd/capy/encrypt.go`, `cmd/capy/encrypt_test.go` (new or extend).

1. In `runEncrypt`, after the `os.Stat(dbPath)` existence check, set
   `dbPath = filepath.EvalSymlinks(dbPath)` (on error keep the original — the file was
   just stat'ed, so an error here is unexpected; log a warning). All downstream
   messages then print the real path, which is the useful one for a symlinked DB.
2. Test: create an encrypted DB at `tmp/real/knowledge.db` (any `store` helper that
   opens with `CAPY_DB_KEY`), symlink `tmp/proj/.capy/knowledge.db` → it, run the
   resolution step and `sqliteutil.SwapAndVerify` onto the resolved path with a copy;
   assert `os.Lstat(symlink)` is still `ModeSymlink` and points at the real file.
   (Do not drive the interactive `runEncrypt`; extract the resolve step into a tiny
   helper if needed and test the composition.)
   → verify: the test fails when the `EvalSymlinks` line is removed.

## Task 3 — `capy setup --db-repo`

**Files:** `internal/platform/setup.go` (or `setup_dbrepo.go`, same package),
`cmd/capy/setup.go`, `precommit_test.go`, `setup_test.go`,
`setup_artifacts_drift_test.go`.

1. Generalize the installer: move the body of `installPreCommitHook(projectDir)` into
   `installPreCommitHookBlock(repoDir, block string) error`; keep the old name as a
   thin wrapper computing `preCommitHookBlock(resolveDBPattern(projectDir))`.
   `preCommitHookScript` becomes `"#!/bin/sh\n" + block`.
   → verify: all `TestInstallPreCommitHook_*` pass unchanged.
2. Add `preCommitHookBlockDBRepo() string` per design §1: markers; staged filter
   `git diff --cached --name-only --diff-filter=AM | grep '\.db$'`; per file: magic
   check → message naming `capy encrypt` in the owning project; `[ -s "$f-wal" ]` →
   block; `[ -e "$f-shm" ]` → block; both with the "stop active sessions for
   `$(dirname "$f")` or run `capy checkpoint --project-dir <project>`" message;
   `done || exit 1`. No capy invocation anywhere in the block.
   → verify: text tests — contains `-wal`, `-shm`, `\.db$`, `done || exit 1`; does
     **not** contain `capy.sh`, `checkpoint --db`, or a binary path.
3. Add `SetupDBRepo(repoDir string) error`: `ensureGitignoreEntry` × 2
   (`*.db-wal`, `*.db-shm`), `installPreCommitHookBlock(repoDir, preCommitHookBlockDBRepo())`
   (non-fatal stderr warning when `.git/hooks` is missing, as `SetupClaudeCode`).
   → verify: `TestSetupDBRepo` asserts exactly `.gitignore` and `.git/hooks/pre-commit`
     are written and that `.claude/`, `.mcp.json`, `.capy/`, `CLAUDE.md` are **absent**;
     run twice → identical tree.
4. CLI: `--db-repo` bool on `capy setup`; `MarkFlagsMutuallyExclusive("db-repo", "platform")`,
   likewise with `local` and `project`. In that mode skip binary resolution (not
   needed), call `SetupDBRepo(projectDir)`, print a two-line summary and the hint
   "commit as usual — the hook refuses unencrypted or un-checkpointed databases".
   → verify: `capy setup --db-repo` in a scratch `git init` dir writes the two files;
     `capy setup --db-repo --platform codex` errors.
5. Drift guard: in `TestDriftGuardCoversEverySetupArtifact` add a second temp dir with
   `.git/hooks`, run `SetupDBRepo`, walk it with the same coverage map.
   → verify: passes; making `SetupDBRepo` write an extra file makes it fail.
6. Shell-level hook test (skip if `sh`/`git` missing): temp `git init` repo, run
   `SetupDBRepo`, add `p/knowledge.db` with non-SQLite bytes, `git add`, commit. Cases:
   - clean → commit succeeds.
   - `p/knowledge.db-shm` present → blocked; stderr mentions `p` and `checkpoint --project-dir`.
   - non-empty `p/knowledge.db-wal` → blocked.
   - zero-byte `p/knowledge.db-wal` only → commit succeeds.
   - file starts with `SQLite format 3` → blocked; stderr mentions `capy encrypt`.
   → verify: all five pass; deleting the `-shm` line in the generator fails case 2.

## Task 4 — Documentation

**Files:** `README.md`, `docs/adr/016-wal-mode-and-checkpoint-strategy.md`.

1. README §"Keeping the DB out of your project repo (recommended)": add step 4
   `cd ~/capy-db && capy setup --db-repo`; replace the "Commit and sync from the private
   repo" block with "commit as usual — the hook blocks …" and list the two blocking
   messages with remedies; add the **pull rule** (no sessions running / no sidecars
   before `git pull`, citing ADR-015); mention `capy checkpoint --project-dir <project>`
   works from anywhere. Commands table: `setup --db-repo`. Note the wrapper behaviour
   change (non-hook subcommands propagate exit codes; a busy checkpoint now blocks
   project-repo commits).
   → verify: every command shown exists in `capy --help`.
2. ADR-016: add a third layer to the Decision ("**DB-repo guard hook** — pure sh,
   refuses staged DBs with sidecars or plaintext; repair happens in the owning
   project") and a Consequences bullet on the wrapper exit-code change. Add a short
   "Rejected: checkpoint from the DB repo" paragraph pointing at this design doc for the
   full rationale (including the keyless spike).
   → verify: ADR still reads as one decision record; no dangling references.

## Task 5 — Final verification

`/kk:test`, `/kk:document`, `/kk:review-code` (Go), `/kk:review-spec` on this
directory. Manual acceptance in the real private DB repo: `capy setup --db-repo`,
delete the hand-copied `.claude/scripts/capy.sh` there, stage one DB and commit with no
sessions running (succeeds); start a session on that project and commit again
(blocked with the sessions message).

## Testing approach

- Unit tests per task, `-tags fts5`; `CAPY_DB_KEY` only needed in Task 2.
- Shell-level tests gated on tool presence; they are the only way to prove the
  `|| exit 1` pipeline semantics and the sidecar checks.
- Drift tests are the acceptance gate for anything touching `setup.go`.

## Deferred / known gaps (explicit)

- `BackupCorruptDB` renames a symlinked path's *link*, not the file — pre-existing,
  out of scope. Add a `TODO(#90)` comment at the call site in `getDB` during Task 2 so
  the next reader sees it.
