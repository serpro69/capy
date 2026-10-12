# ADR-034: Prepared Read rules and execute-file admission

**Status:** Proposed — implemented candidate, pending decision acceptance
**Date:** 2026-10-12
**Provenance:** [upstream-sync design §3.2–3.3](../feat/wip/upstream-sync-v1.0.169/design.md#32-prepared-read-rules--prerequisite-for-s2i1c1), Tasks 2a/2/5/12–15; documents the selected implementation, not independent security approval.

## Context

Origin-free glob strings lose the meaning of settings-relative rules. Relative,
absolute and symlink spellings can otherwise produce different decisions across
MCP, hooks, directory ingestion and automatic refresh. Execute-file also needs
an explicit contract for reading files outside the selected project.

## Decision

`security.LoadReadPolicy` prepares an immutable snapshot retaining action,
settings source/anchor, selected project, rule cwd and home. It parses anchors
before cleaning: `//` filesystem root, `~/` home, `/` settings-source anchor, and
unprefixed/`./` cwd. Project/local sources anchor to the selected project; user
sources anchor to the settings parent. MCP/CLI rule cwd is the selected project;
hooks use validated payload cwd after selecting project/settings identity.

Support `*`, `**`, `?`, escaped literals and bare `Read`. Basename-only unprefixed
rules match at any depth under cwd. Invalid settings and unsupported syntax
produce diagnostics and fail admission. Missing settings remain normal. Legacy
single-slash denies use the union of source-relative and old absolute meanings,
warning that `//` removes ambiguity. New allows use only documented anchors.

Deny checks retain requested, lexical and physical candidates, including
symlink-before-`..` handling. Execute-file admits in-project candidates unless
denied; external reads require one normalized allow covering requested and
physical targets. Separate partial grants do not combine; denies always win.
Resolution failures block before execution. The admitted physical absolute path
passes into the runtime through its child environment, avoiding filename/source
interpolation. An execution cwd override cannot change this boundary.

Shared ingestion uses the same prepared deny policy. Direct absolute file
indexing keeps its prior deny-only contract; directory traversal has canonical
root containment and alias/target filters. Refresh skips denied reads while
keeping cached content. Server policy changes require restart; hooks load each
invocation. Existing local/shared/user settings are used; no new managed or
session policy discovery is claimed.

## Alternatives and consequences

Keeping raw glob arrays would leave callers with inconsistent anchor semantics.
Treating old absolute denies only as source-relative could silently weaken them;
granting the same legacy alias to allows could broaden access. Conservative
deny union plus warnings makes migration explicit. Unsupported syntax fails
instead of approximating a grant.

These checks cover explicit tool inputs, not every filesystem access by submitted
code. Descriptor-bound stat/read and bounded reads do not make policy evaluation
and open atomic. [D2](../feat/wip/upstream-sync-v1.0.169/upstream-audit.md#d2-filesystem-replacement-between-policy-checks-and-runtime-reads)
requires a checked-handle handoff to close concurrent replacement. Shell scanning
separately checks executable elements with explicit complexity bounds; it is
static policy matching, not a complete shell interpreter.

No database migration is required. Rollback restores earlier path/anchor behavior
and loses these admission guarantees; review migrated rules before downgrading.
Tests cover settings origins, deny/grant precedence, lexical/physical aliases,
CLI/MCP/stale paths and no-spawn failures. Owners are `internal/security/file_policy.go`,
server `security_check.go`, hook `context.go`, and `internal/knowledge`; see the
[verification ledger](../feat/wip/upstream-sync-v1.0.169/verification.md).
