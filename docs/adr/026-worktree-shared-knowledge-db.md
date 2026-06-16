# ADR-026: Git worktrees share the main worktree's knowledge DB

**Status:** Accepted
**Date:** 2026-06-16

## Context

[ADR-019](019-encrypted-knowledge-db.md) re-enabled committing the (now
encrypted) knowledge DB to git via an opt-in project-scoped `store.path`
(e.g. `store.path = ".capy/knowledge.db"`), superseding [ADR-015](015-knowledge-db-not-tracked-in-git.md)'s
blanket prohibition. The documented cross-machine workflow (README §Cross-machine
sync) is: configure a project-local `store.path`, `capy encrypt` → `capy
checkpoint` → commit `.capy/knowledge.db`.

That committed-DB setup breaks under [git worktrees](https://git-scm.com/docs/git-worktree).
A linked worktree is a separate working directory checked out from the same
repository. With a project-scoped `store.path`, capy resolved the DB relative to
the worktree's own root, so each worktree got its **own copy** of
`.capy/knowledge.db`:

- Knowledge indexed during a worktree session lands in the worktree's copy and is
  **stranded** — it never reaches the main project's DB.
- Because `knowledge.db` is a committed **binary** file, two worktrees writing
  their own copies produce a merge conflict with **no resolvable diff**. The
  practical outcome is that worktree DB changes are discarded.

Net effect: there is no way to persist knowledge from sessions that happen in a
worktree. We want a worktree session to read and write the *same* DB as the main
checkout, transparently.

The XDG default path (`~/.local/share/capy/<project-hash>/knowledge.db`) has the
same per-worktree *separation* (the hash is keyed off the worktree path), but
**not** the same pain: it is outside git, so nothing is committed and nothing
conflicts. The acute, data-losing problem is specific to a project-scoped
(relative) `store.path`.

## Decision

### D1: Redirect a project-scoped DB to the repository's main worktree

`config.DBProjectDir(projectDir)` is the single point that decides which project
directory owns the knowledge DB:

- **Relative `store.path`** (project-scoped) **in a linked worktree** → the
  repository's **main worktree**. All worktrees of a repo therefore share one DB,
  which lives in (and is committed from) the main checkout.
- **Absolute `store.path`** → unchanged. It is already a fixed, shared location.
- **XDG default** (empty `store.path`) → unchanged. Keyed per project, not
  committed, so per-worktree separation causes no conflict (see Consequences for
  the deliberate scope limit).

`ResolveDBPath` resolves a relative `store.path` against `DBProjectDir(projectDir)`
instead of the raw `projectDir`.

### D2: Detect the main worktree by parsing `.git`, not by shelling out to git

`config.MainWorktreeDir(projectDir)` identifies a linked worktree **without
invoking the `git` binary** and **without relying on any committed marker file**:

1. `<projectDir>/.git` is a regular **file** (a directory means a normal repo or
   the main worktree itself → no redirect).
2. The file contains `gitdir: <worktree git dir>`; that git dir must sit under
   `…/worktrees/<name>`. This segment guard is what distinguishes a worktree from
   a **submodule** (whose `.git` file also points via `gitdir:`, but into
   `…/.git/modules/<name>`) — a submodule must keep its own DB.
3. The common git dir is read from the worktree git dir's `commondir` file
   (fallback: strip the trailing `worktrees/<name>` segments); the main worktree
   is its parent.

Reading the `.git` file directly is faster (no subprocess on every startup),
works even when `git` is not on `PATH`, and is more robust than a committed
`.capy/.project` marker — which would record a machine-specific absolute path and
is gitignored and overwritten per worktree. Any malformed or unexpected layout
**falls back to `projectDir`** (fail-safe): a detection failure forgoes the
redirect but never breaks DB resolution.

### D3: The `.capy/.project` marker records the DB owner, not the working dir

`internal/store/store.go` writes a diagnostic `.capy/.project` marker next to the
DB recording the store's `projectDir`. That marker has **no code readers** — it is
write-only. Because the DB now lives under the main worktree, all five production
`store.NewContentStore` call sites (`server.go`, and `cmd/capy/{cleanup,sweep,checkpoint,dbsize}.go`)
pass `DBProjectDir(projectDir)` so the marker records the **main** worktree (the
DB's true owner) rather than clobbering it with the current worktree's path.

This is deliberately decoupled from the *working* directory. The MCP server's own
`s.projectDir` — used for security globs, the executor working directory, and
session sweep — stays the **current** worktree, because file operations are
worktree-local. Only DB ownership follows the main worktree.

## Consequences

- A worktree session with a project-scoped `store.path` transparently reads and
  writes the main checkout's `.capy/knowledge.db`. Worktree knowledge persists and
  the binary-merge-conflict failure mode is eliminated.
- `capy which`, `capy cleanup`, `capy sweep`, `capy checkpoint`, and `capy dbsize`
  run from a worktree all operate on the main worktree's DB automatically.
- **Scope limit (deliberate):** the XDG default is *not* redirected — worktrees
  keep separate XDG-hashed DBs. They do not conflict (outside git), but worktree
  knowledge is also not shared back to the main project in that mode. Sharing the
  XDG default across worktrees was considered out of scope for the data-losing
  problem this ADR targets; it can be revisited if users want it.
- Submodules are explicitly excluded by the `worktrees/<name>` segment guard, so a
  submodule checkout keeps its own DB.
- Cross-worktree concurrency uses the same single-DB story as any two capy
  processes sharing a DB — SQLite WAL handles concurrent access; the
  WAL-checkpoint-on-close invariant ([ADR-016](016-wal-mode-and-checkpoint-strategy.md))
  is unchanged.

## Alternatives considered

### Shell out to `git rev-parse --git-common-dir` (rejected)
Correct, but spawns a `git` subprocess on every server/CLI startup and requires
`git` on `PATH`. Parsing the `.git` file yields the same answer with neither cost.

### Read a committed `.capy/.project` marker to find the main worktree (rejected)
The marker stores a machine-specific **absolute** path, is gitignored (so a
worktree checkout would not even receive it), and is overwritten per worktree —
circular and non-portable. Git's own worktree metadata is the authoritative,
machine-correct source.

### Redirect the XDG default too (deferred)
Would make every worktree share the main project's hashed DB. Out of scope: the
XDG default does not lose data (no commit, no conflict), and redirecting it would
change behavior for all worktree users — including any who rely on per-worktree
isolation. Left for a follow-up if demand appears.
