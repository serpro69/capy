# ADR-033: Execution working directories and project identity

**Status:** Proposed — implemented candidate, pending decision acceptance
**Date:** 2026-10-12
**Preserves:** [ADR-026](026-worktree-shared-knowledge-db.md), [ADR-032](032-project-db-key-resolution.md)
**Provenance:** [upstream-sync design §4.2](../feat/wip/upstream-sync-v1.0.169/design.md#42-working-directories--e2), Tasks 7/8; this record describes that implementation, not a new independent approval.

## Context

Non-shell runtimes used their temporary script workspace as process cwd while
shell code used the project. Relative reads, module discovery and output writes
therefore differed by runtime. Multi-package projects also need per-call cwd
selection without moving their knowledge database, policies or vault scope.

## Decision

Every runtime defaults to the selected project. Execute, execute-file and batch
accept an optional existing directory; relative values resolve from that project.
`executor.ResolveCwd` validates it and resolves symlinks before parent components.
The absolute value travels in `ExecRequest.Cwd`; no process-wide `os.Chdir`,
environment change or mutable server/executor project field is used. A batch
resolves one directory for all workers; direct executor calls validate it too.

Temporary script paths and `TMPDIR` remain absolute and isolated per invocation.
Rust compiles in the temporary workspace, then runs its binary in the effective
cwd. Elixir's Mix inspection uses that same effective cwd. Go retains its
absolute temporary-script invocation and discovers modules/workspaces, local
imports, replace directives, automatic vendor mode and toolchain requirements
from the execution cwd. Inherited `GOFLAGS` stays removed by the safe environment.

Database selection, captured credentials, policy origin and vault scope stay
with the selected capy project. Relative execute-file `path` inputs resolve there
independently of cwd; the server forwards the admitted physical path. Absolute
external execution directories are allowed because submitted code already has
host-user access. This is not filesystem sandboxing.

## Alternatives and consequences

- Keeping runtime-specific cwd retained surprising relative I/O and module
  behavior. Explicitly selecting temporary paths now expresses disposable writes.
- Transcript-based cwd guessing depends on unrelated session ordering and does
  not establish project/key identity; it was excluded.
- Mutating global cwd or rebinding the project to each request would race and
  could select another database or policy. Request-local values avoid that.

Relative non-shell outputs now persist in the effective execution directory.
Callers depending on old temporary writes must use explicit temporary paths.
Per-call selection has no schema/config migration and can be removed independently;
binary rollback restores previous cwd/module behavior, requiring script review.

## Evidence and limits

Owners are `internal/executor/cwd.go`, `executor.go`, `wrap.go`, and server
`cwd.go` plus the three execution handlers. Runtime, concurrent-request, batch
and actual stdio fixtures cover directory isolation and unchanged credentials,
policies and vault ownership. See [Tasks 7/8 and final verification](../feat/wip/upstream-sync-v1.0.169/verification.md).
Unavailable runtimes are explicitly recorded as skipped; an implemented wrapper
is not evidence of an executed runtime test. Atomic checked-file handoff is
separate deferred work (D2).
