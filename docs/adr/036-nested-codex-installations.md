# ADR-036: Independent nested Codex installations

**Status:** Accepted — implemented
**Date:** 2026-10-16
**Supersedes:** The blanket overlap prohibition in the [Codex hooks design](../feat/wip/codex-hooks/design.md#61-files-and-invocation) and its [v2 amendment](../feat/wip/codex-hooks/design-v2.md#4-simpler-owner-bound-launch).
**Preserves:** [ADR-032](032-project-db-key-resolution.md), [ADR-033](033-execution-cwd-and-project-identity.md).

## Context and decision

Parent and child projects need independent capy settings and knowledge, with
only the child's capy rules applied to a session opened in the child. Codex
[loads matching hooks from multiple sources](https://learn.chatgpt.com/docs/hooks#where-codex-looks-for-hooks);
config precedence alone does not suppress a parent's hook.

Setup creates or preserves an owner ID in the requested project, allowing
distinct ancestor installations. Existing Git-root discovery remains unchanged;
use `--project-dir <child>` to select a child inside the same repository.
Runtime still scans every physical ancestor,
validates every binding against its adjacent marker, and rejects copied IDs
on the same chain. It validates the invoked ID and mode/event before selecting
the nearest installation; a missing nearest binding is an error.

- A nearest-owner hook runs normally. A validated ancestor hook consumes input
  with the existing bounded-EOF reader, then exits 0 without parsing the event,
  applying policy, emitting guidance or touching state. This lets the host finish
  writing large inputs; transport failures retain the normal exit contract.
- MCP must invoke the nearest owner's own registration. An ancestor command
  fails with a reload diagnostic before credentials or databases are accessed;
  it is never rebound to a different owner.
- Doctor reports the nearest installation after the same full-chain validation.
  It cannot establish host trust or prove that the child's hooks actually run.

Global settings and unrelated hooks retain their existing behavior. Database
paths and credentials remain project-owned under ADR-032; explicit shared paths
and linked-worktree sharing retain their existing semantics. No database moves,
schema changes, new dependencies or launch-grammar changes are required.

## Alternatives and limits

Keeping the blanket rejection prevents ordinary nested projects. Running both
owners' hooks applies parent rules to the child and duplicates routing state.
Rebinding inherited commands to the child loses command provenance and can
duplicate child handling. Explicitly suppressing validated ancestor hooks and
rejecting ancestor MCP commands avoids those outcomes.

Install the child with setup rather than copying the parent's `.codex` directory.
Restart affected sessions after installation changes and trust the child's
definitions; there is deliberately no parent fallback if the child is inactive.
Malformed ancestor configuration still blocks launch. Entirely deleted origin
artifacts cannot authenticate cached commands; stop sessions before removal.
Older binaries reject nested installations, so rollback requires a single owner
or non-overlapping checkouts. Generated bindings and owner IDs remain unchanged.

## Verification

`TestSetupCodexNested` covers either installation order and unchanged reruns;
`TestCodexNestedOwnerResolution` and `TestCodexShadowedHookRequiresBothBindings`
cover selection and failed provenance. Existing copied-ID/missing-marker tests
retain their rejection contract. `TestCodexNestedLaunches` drives generated
commands in one repository and across nested Git roots, checks all ancestor
hook no-ops, separate policies/keys/databases, and wrong-owner MCP rejection.
These are command/MCP fixtures; live Codex host activation is not established
by them. CI/release changes are N/A; existing FTS5-tagged test conventions apply.

The focused owner/launch/input tests pass normally and with race instrumentation
(including the compiled CLI fixtures). The original combined `setup --local`
rerun also passes. `go vet -tags fts5 ./...`, `make build` and an independent
code review completed successfully. The second, external review was unavailable
because the configured provider rejected its API key.

The full suite is not green. The following unrelated failures were reproduced
at the unchanged `7d443c8` baseline and left outside this ownership change:

| Failure | Follow-up |
| --- | --- |
| `TestCodexMixedStartupContext` expects the external toolbox hook to inject the full routing file | Update the fixture for the existing slim toolbox payload while retaining the separate capy-hook assertions. |
| `TestMergedArtifactsAreIdempotent/.codex/config.toml` | Reconcile the committed MCP registration (currently nested under `[tui]`) with `mergeCodexMCPServer`, preserving the owner ID and unrelated settings. |
| Vault CLI import/restore/merge fixtures inherit a 196,608-byte minimum and exclude their tiny transcripts | Isolate invocation project/configuration in those fixtures so personal import policy cannot affect them. |

The initial full run also hit sandbox restrictions on Unix/TCP test sockets;
`internal/config` and `internal/server` both passed when rerun with local sockets
permitted.
