# Design: First-class support for a separate knowledge-DB repo

> Issue: [#90](https://github.com/serpro69/capy/issues/90)
> Status: design complete, implementation pending
> Created: 2026-09-13 (revised same day — scope reduced, see Rejected Alternatives)
> Implementation: [./implementation.md](./implementation.md)
> Tasks: [./tasks.md](./tasks.md)

## Problem

The README recommends keeping `knowledge.db` **outside** the project repo: move it into
a dedicated private git repo (one subdirectory per project, e.g.
`~/capy-knowledge-db/<project>/knowledge.db`) and symlink it back to the project's
`.capy/knowledge.db`. The store follows the symlink transparently, so sessions work.
Committing from that private repo is unsupported by the tooling:

- The pre-commit hook `capy setup` installs greps staged paths for
  `\.capy/knowledge\.db$`. In the DB repo the paths look like `<project>/knowledge.db`,
  so the block never fires — neither the checkpoint nor the unencrypted-DB guard.
- `capy checkpoint` inside the DB repo resolves the *current project's* config, finds
  none, falls back to the XDG default and reports "no knowledge base". One config could
  not describe fourteen databases anyway, and every project has its **own**
  `CAPY_DB_KEY`, so nothing run from the DB repo can open those files.
- Users end up hand-copying `.claude/scripts/capy.sh` into the DB repo and hand-editing
  the hook — exactly the generator/committed-artifact drift AGENTS.md warns about.

Two adjacent defects surfaced while tracing the code. They are independent of the DB
repo but are hit by this setup and are fixed here:

- **The wrapper swallows failures.** `.claude/scripts/capy.sh` runs the binary with
  `|| true` and always exits 0, and prints a PreToolUse *deny* JSON when the binary is
  missing regardless of subcommand. In the project pre-commit hook this means a busy
  checkpoint (MCP server still holding the DB) never blocks the commit — a stale main
  file is committed silently. Only the `hook` subcommand needs the always-exit-0
  contract. The hook also has a dead `if [ $? -ne 0 ]` after a piped `while` loop that
  can never fire.
- **`capy encrypt` through a symlink destroys the symlink.** The final swap renames the
  new file onto the *symlink path*, replacing the link with a regular file and stranding
  the DB-repo copy.

## Goal

**Committing in the DB repo is safe by construction.** Staging any `*.db` there and
committing either succeeds (the file is complete and encrypted) or is blocked with a
message naming the file and the remedy. No key material, no capy binary, no knowledge of
project locations inside the DB repo.

How-Might-We: *let a capy user who keeps `knowledge.db` files for many projects in one
dedicated private git repo commit those databases safely from that repo, without knowing
each project's location up front, and without a single shared encryption key.*

**Persona:** solo developer, many projects, one private DB repo, one key per project.

## Key insight

The DB repo does not need to *fix* a database — it only needs to *refuse* an unsafe one.
SQLite removes the `-wal` and `-shm` sidecars when the last connection closes cleanly,
and the MCP server checkpoints on shutdown (ADR-016). Therefore:

> **No sidecars beside the file ⇒ the main file is complete and self-consistent.**

A sidecar present means either a live session (checkpoint from the DB repo would report
*busy* anyway) or an unclean shutdown (the owning project can repair it with
`capy checkpoint`). Both are the user's call to resolve, and both have a one-line remedy.
The hook therefore needs no key and no binary — it is pure `sh`.

## Constraints

1. One `CAPY_DB_KEY` per project; the DB repo never sees keys.
2. Every artifact `capy setup` writes must stay reproducible from
   `internal/platform/setup.go` and pass the drift tests.
3. Hook events invoked via `capy.sh hook …` must keep exiting 0.
4. No machine-specific paths committed anywhere.

## Chosen approach

Three independent pieces, each shippable alone.

### 1. `capy setup --db-repo` — a pure-sh guard hook plus `.gitignore` entries

A setup mode for a repository that holds databases, not a project. Mutually exclusive
with `--platform`, `--local`, `--project`. `platform.SetupDBRepo(repoDir)` does two
things and nothing else:

1. `.gitignore`: add `*.db-wal`, `*.db-shm`. Committed sidecars are the ADR-015
   corruption vector, and they *do* appear in the DB repo while a session runs.
2. Install the **DB-repo pre-commit block** (same `# capy:` start/end markers as the
   project hook, so re-runs replace in place and a stale hand-edited block is migrated).

No wrapper script, no MCP config, no routing file, no `.capy/`. The hook needs no capy
binary, so an outdated or missing binary can never weaken the guard.

**DB-repo hook block** (generator `preCommitHookBlockDBRepo()`), for each staged path
matching `\.db$` (added or modified):

| Check | Blocks when | Message names |
|---|---|---|
| Plaintext | first 15 bytes are `SQLite format 3` | `capy encrypt` **in the owning project** |
| Pending WAL | `<f>-wal` exists **and is non-empty** | "stop active capy sessions for `<dir>`, or run `capy checkpoint` from that project (`capy checkpoint --project-dir <path>` works from anywhere)" |
| Open connection | `<f>-shm` exists (any size) | same message |

A zero-byte `-wal` is tolerated: `PRAGMA wal_checkpoint(TRUNCATE)` can leave one
behind (the existing `capy checkpoint` already treats it as fine). Iteration uses
`printf '%s\n' "$staged" | while IFS= read -r f; do …; done || exit 1`; the trailing
`|| exit 1` is what makes an inner `exit 1` stop the commit — a pipeline's status is the
loop's, not the inner command's.

`installPreCommitHook` is generalized to accept a block so both modes share the
marker-delimited replace-or-append logic.

### 2. Wrapper exit codes + project-hook fix

**Wrapper** (`capyWrapperScript`; committed copies `.claude/scripts/capy.sh`,
`.codex/scripts/capy.sh` — change all three in one commit or
`TestGeneratedWholeFileArtifacts` fails): branch on `$1`.

- `hook` → exactly today's behaviour (`|| true; exit 0`; deny-JSON when no binary).
- anything else → `exec "$p" "$@"` so the real exit code propagates; when no binary is
  found, print a plain error to stderr and `exit 127`.
- The `serve` key-recovery block is untouched.

**Project hook block** (`preCommitHookBlock`): the checkpoint call gains `|| exit 1`
(a busy checkpoint now blocks the commit — a deliberate, user-visible change); the
unencrypted check moves into the same `while … done || exit 1` pipeline shape,
replacing the dead `if [ $? -ne 0 ]` line.

### 3. `capy encrypt` is symlink-safe

`runEncrypt` resolves `dbPath` with `filepath.EvalSymlinks` (the file must exist at
that point already) before calling `encryptPlain`/`rekeyEncrypted`. The swap then
renames onto the real file and the symlink survives. `sqliteutil` itself is not touched.

## Documentation contract (README)

The "Keeping the DB out of your project repo" section adds one step
(`cd ~/capy-db && capy setup --db-repo`) and two rules the hook cannot enforce:

- **Pull only when no sessions are running.** `git pull` replacing the main file while a
  session holds the old inode is exactly the ADR-015 corruption vector. The hook's
  "no sidecars" condition is the same precondition — check for sidecars before pulling.
- **`capy checkpoint --project-dir <project>`** works from anywhere and is the remedy
  the hook names.

## Assumptions

1. SQLite deletes `-wal`/`-shm` on clean last-connection close, and capy's server
   shutdown checkpoint (`ContentStore.Close`) runs in normal operation — so in the
   common case (sessions closed) the hook passes with no user action.
2. Git runs pre-commit hooks with cwd at the worktree root, and
   `git diff --cached --name-only` yields root-relative paths (same assumption as the
   existing project hook).
3. A zero-byte `-wal` never carries data; any non-empty `-wal` may.
4. The DB repo is a normal checkout with `.git/hooks` (setup warns and skips otherwise,
   as today).

## Not Doing

- **Checkpointing from inside the DB repo** (any `--db` flag, key lookup, env sourcing,
  reverse project mapping). See Rejected Alternatives — the guard makes it unnecessary.
- **A wrapper script in the DB repo** — the hook has no binary dependency.
- **Auto-discovery (`capy checkpoint --all`)**, **capy-driven `git commit`**,
  **team/multi-machine DB repos**, **a `doctor` check for the DB repo** — out of scope.
- **Guarding `git pull`** — git has no pre-pull hook; documented instead.
- **Revisiting WAL mode (ADR-016 open question)** — would obsolete all checkpoint
  machinery but is benchmark-gated and separate.
- **Making `BackupCorruptDB` symlink-aware** — observed while tracing: renaming a
  symlinked DB path on corruption renames the *link*, not the file. Unrelated; noted.

## Rejected Alternatives

- **Full "commit just works" design (first revision of this doc):** `--db <file>` on
  `checkpoint`/`encrypt`/`dbsize`; the store writing a `.project` symlink beside the
  real DB as a reverse mapping; the DB-repo hook following it to the project and
  sourcing that project's env file in a subshell to obtain the key; a wrapper copy in the
  DB repo. It bought exactly one capability — repairing a stale WAL or committing while a
  session is open, from inside the DB repo — at the cost of three CLI flags, store-side
  bookkeeping, an indirection chain that `source`s a shell file from a git hook, and a
  setup mode with six tasks. With a live session the checkpoint is usually *busy* anyway.
  Rejected as over-engineering; "close your sessions or run checkpoint from the project"
  is the right contract for the persona.
- **Keyless checkpoint.** Spike (2026-09-13): sqlite3mc recomputes WAL frame checksums
  over ciphertext and the checkpoint runs at the VFS layer, so SQLite's *close-time*
  checkpoint did flush an encrypted WAL keylessly with integrity intact. But
  `PRAGMA wal_checkpoint` needs the pager's WAL handle, opened only by a read
  transaction that reads page 1 → `file is not a database`; and the go-sqlite3 driver
  unconditionally runs `PRAGMA synchronous` (flag `PragFlg_NeedSchema`) during `Open`,
  so a keyless `database/sql` connection cannot even be established. Recorded in
  `kk:debug-context` so nobody re-runs it.
- **Mapping table in a DB-repo `.capy.toml`** / **`CAPY_DB_KEY_<DIR>` convention**:
  both exist only to obtain keys in the DB repo, which the guard design does not need.
- **README snippet instead of `capy setup --db-repo`:** defensible, but a generated
  hook picks up fixes on re-run and answers the issue's "tooling does not support it"
  complaint; the installer is two calls into existing primitives.
