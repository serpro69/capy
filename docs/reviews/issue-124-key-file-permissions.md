# Issue #124: key-file permission enforcement

Implemented on 2026-10-01. [Issue](https://github.com/serpro69/capy/issues/124), [current credential contract](../adr/032-project-db-key-resolution.md).

## Accepted behavior

- Explicit key files require Unix mode `0400` or `0600`, without special bits. Normal resolution checks before opening and on the opened descriptor before reading; failures never fall back.
- Project setup repairs existing configured regular-file targets to `0600`. The user explicitly selected preserving already-safe `0400` files. Setup follows symlinks and database-owner/worktree path rules, skips missing targets, and never reads or rewrites secret contents. Inspection and repair failures stop setup before platform artifacts are generated.
- CLI doctor reports permission failures through credential selection and skips database access. MCP doctor reports current metadata separately while retaining its captured key and continuing database checks. Neither doctor repairs permissions.
- Dotenv, vault credentials, keyless commands, missing-database checkpoint, and DB-repository setup keep their existing contracts.

## Verification

- Focused permission, repair, setup, CLI, and MCP-doctor tests passed across all four affected packages.
- `go test -tags fts5 -race -count=1 -timeout=600s ./...` passed for the whole repository. CLI package: 198.176 s; platform: 8.354 s; config: 7.410 s; server: 71.221 s. The race flag instruments test processes; existing CLI helpers launch ordinary FTS5 subprocess binaries.
- `go vet -tags fts5 ./...` and `git diff --check` passed.
- After the review's test-isolation fix, setup, precommit, and generated-artifact tests passed again with `-race` in `internal/platform` and `cmd/capy`; the entire platform package also passed again with `-race` (4.118 s). Vet passed again for those packages. Production behavior did not change after the full suite.
- A separate synthetic inherited-global-config probe pointed at an absolute `0644` key file. Setup tests passed and left that file at `0644`, proving they do not repair a caller's inherited key fixture.

All executions used synthetic keys and isolated home, XDG, vault, and temporary directories. Local sockets were allowed for the existing full-suite tests. No live credential file or database was used. Generated wrapper/config contents did not change; their drift guards passed. Search/indexing/executor algorithms did not change, so quality benchmarks were not applicable. Existing CLI and CI conventions are inherited from the repository; pipeline changes are outside this feature.

The initial focused run exposed two fixture errors, both corrected explicitly: this host clears setuid/setgid on non-executable files, so their rejection is covered by filesystem-independent mode-policy tests; and a dangling symlink must be asserted as an existing link with an absent target. Real-file tests cover regular modes, sticky-bit rejection, repair, idempotence, symlink preservation, and unchanged secret bytes.

## Independent review

`kk:review-code:isolated` used a fresh `code-reviewer` agent and two-step PAL `gemini-3.1-pro-preview` review. Go was the sole active profile; SOLID, removal, security, code style, error handling, injection, and exported naming checklists applied.

The code reviewer found **P2: existing setup tests inherited global configuration and could chmod a developer's real absolute key file**. The platform package now isolates global configuration in `TestMain`; the existing CLI setup test also isolates it. Tests intentionally exercising global settings use synthetic configuration. The reviewer rechecked all setup callers and returned **APPROVE**, with no remaining findings.

PAL reported no issues. Its broad claims of TOCTOU protection are not adopted: opened-descriptor validation protects the read admission check, but pathname-based setup repair is not atomic. The independent reviewer classified this as a residual limitation under the agreed scope, not a blocking finding. Neither reviewer independently executed tests. No P0/P1 systemic findings to index; no new project conventions beyond the documented contract.

## Limits and follow-up boundary

This feature enforces Unix mode bits; it does not audit extended ACLs or parent-directory permissions. Setup assumes trusted directories and a stable credential path during repair. Concurrent replacement between `Stat`, `Chmod`, and the final `Stat` can redirect the mutation; the final check does not prove inode identity. Hardening against hostile concurrent replacement is deferred because the agreed feature is local-user mode repair with external symlink support. If that threat model becomes required, implement platform-appropriate descriptor-based metadata access and mutation, including unreadable mode-`0000` targets, and add deterministic file/symlink replacement tests before claiming atomic target safety. This assumption is also documented at the repair method.

Chmod-denied and read-only-filesystem branches were inspected but not forced in an automated fixture; setup propagates those errors. A future portability test can provision an immutable or unmodifiable synthetic file on each supported platform and assert that setup fails before artifact generation. Current tests cover other deterministic inspection failures and nonregular targets.
