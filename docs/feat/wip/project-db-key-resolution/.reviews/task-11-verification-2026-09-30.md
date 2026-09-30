# Task 11 final verification and documentation

> Scope: complete project database key resolution feature, Tasks 1–11
> Baseline: `d9e09c1`; implementation through `c732764`, plus Task 11 documentation
> Status: verification passed; independent code and specification reviews complete

## Documentation

README now documents strict target selection, all credential consumers and keyless exceptions, exact file/quoting rules, worktree ownership, captured key lifetime, diagnostics, rotation, and existing upgrade/rollback steps. The architecture guide explains the resolver → server/store connection flow. Root AGENTS states the new invariant while retaining mandatory encryption and WAL requirements. CONTRIBUTING includes `store.key_file`, both synthetic test keys, test isolation, real stdio coverage, and the existing CI/race contract.

[ADR-032](../../../../adr/032-project-db-key-resolution.md) records the agreed policy, alternatives, file bounds, whole-file dotenv break, environment-only vault migration, lifetime guarantees, and rollback. ADR-019 links its superseded environment-only rule to ADR-032; its cipher/encryption/WAL decision remains accepted. No generated artifact or production implementation changes were needed. Review identified a Git-root test-fixture gap, corrected as described below.

Profile detection considered the complete feature change: Go activated by `.go` files, with no other profiles. Implement guidance covered design, interfaces, errors, security, concurrency, context, database access, and injection. Testing used the Go testing checklist; benchmarks were inapplicable because credential plumbing changes no retrieval/indexing/chunking/executor algorithm. Documentation applied CLI guidance, retained the custom TOML system (ADR-008), and cited the existing CI workflow. New CI/deployment/security-scanner configuration is N/A: no pipeline changes are part of this feature. No dependency was added or upgraded.

## Regression matrix evidence

Every required row is exercised by the automated suite. The table names the owning tests rather than treating successful startup as proof of database access.

| Matrix row | Automated evidence |
| --- | --- |
| Two projects | `cmd/capy`: `TestMCPStdio`, `TestMCPProjectCredentials` — simultaneous processes, crossed inheritance, index/search markers and reopen without reindexing |
| Explicit key files | `internal/config`: `TestLoadKeyFilePrecedence`, `TestLoadKeyFileInvalidTypes`, `TestResolveStoreKeyFormat`, `TestResolveStoreKeyFileAccess`, `TestResolveStoreKeyUnreadable`, `TestResolveStoreKeyPathRules`; `cmd/capy`: `TestDBSizeSubcommand_KeyFileOwnership`, `TestDBSizeSubcommand_KeyFileFailureBeforeCreation`, `TestDBSizeSubcommand_WrongFileKeyPreservesData` |
| Dotenv | `internal/config`: `TestParseDotenvKeyLiteral`, `TestParseDotenvKeyNoDeclaration`, `TestParseDotenvKeyRejectsWholeFile`, `TestResolveStoreKeyDotenvPrecedence`, `TestResolveStoreKeyDotenvMainAccess`, `TestResolveStoreKeyDotenvFileAdmission`; `cmd/capy`: `TestDBSizeSubcommand_DotenvOwnership`, `TestDBSizeSubcommand_DotenvErrorsAndBypass` |
| Execution isolation | `TestResolveStoreKeyDotenvBypassAndIsolation`, `TestDBSizeSubcommand_DotenvNoExecutionOrCreation` and wrapper credential cases — sentinel absent; knowledge/vault environment unchanged |
| Environment compatibility | `TestResolveStoreKeyEnvironment`, `TestParseDotenvKeyNoDeclaration`, `TestDBSizeSubcommand_EnvironmentKey`, `TestDBSizeSubcommand_ClearedKeyFileUsesEnvironment`, `TestMCPStdio`, generated-wrapper environment-only cases |
| Target identity | `TestResolveStoreKeyOwnership`, `TestResolveStoreKeyPathRules`, CLI key-file/dotenv ownership cases, direct MCP cwd-subdirectory and project-override cases, real linked-worktree wrapper migration |
| Invalid config | `TestDBSizeSubcommand_InvalidConfig`, `TestCleanupSubcommand_InvalidConfig`, `TestCheckpointSubcommand_BadConfig`, `TestDoctorSubcommand_InvalidConfig`, `TestPathSubcommands_InvalidConfig`, `TestRunEncrypt_InvalidConfigBeforePrompts`, MCP startup failures — no fallback target/marker |
| Key lifetime | `internal/store`: `TestStoreEncryption_ExplicitKeysConcurrent`, `TestStoreEncryption_EmptyKeyHasNoSideEffects`, `TestStoreEncryption_KeyLifetime`, `TestStoreEncryption_RecoveryUsesCapturedKey`; `internal/server`: `TestServer_KnowledgeCredentialSnapshot`, `TestServer_EmptyKnowledgeCredentialDoctor`; `cmd/capy`: `TestKnowledgeStore_KeyFileSnapshot` |
| Diagnostics | `TestDoctorSubcommand_CredentialSources`, `TestDoctorSubcommand_CredentialResolutionFailure`, missing/stat-error/invalid-config cases; `TestDoctor_CapturedCredentialSources`, `TestDoctor_FTS5AvailableWithKnowledgeBaseError`, `TestDoctor_InitializesEmptyKnowledgeBase` — selection separate from authentication, safe errors, independent FTS5 |
| Rotation | `TestRunEncrypt_ProjectCredentialsDoNotSelectRotationKeys`, `TestRunEncrypt_PromptFailuresPreserveDatabase`, `TestEncryptSubcommand_Help`, `TestResolveDBSymlink_SwapPreservesSymlink`, `TestEncryptPlain_WALMode` — old/new access, retained data, untouched files/vault, operator guidance |
| Wrappers/hooks | `TestGeneratedWrapperProjectCredentials`, `TestGeneratedPreCommitProjectCredentials`, `TestWrapperExitCodes` — both platforms, bare/leading flags, actual reads, keyless hooks, real busy-checkpoint commit abortion and keyless DB-repo guard |
| Vault migration | `TestGeneratedWrapperVaultMigration` — knowledge works with both keys only in main dotenv; vault disabled with migration hint until its key is supplied to the child environment, then archived content is read |
| Rollback boundary | `TestLegacyV0164ConfigIgnoresKeyFile` plus generated-wrapper environment-only round trips; frozen old config shape retains `store.path`. Not a full legacy binary downgrade test |
| Generated artifacts | `TestGeneratedWholeFileArtifacts`, `TestMergedArtifactsAreIdempotent`, `TestDriftGuardCoversEverySetupArtifact`, repeated setup in wrapper fixtures |

Maintenance also exercises actual cleanup/reclamation with local credentials (`TestCleanupSubcommand_ProjectCredentials`), wrong-key content preservation, no creation on missing credentials, and busy/stat-error checkpoint failures. Parser/admission tests include exact and excess raw byte limits, symlinks, devices/directories/sockets, and child-process deadlines for pre-existing FIFOs.

## Executed checks

- `make test`: passed, FTS5 enabled, cache disabled with `GOFLAGS=-count=1`. Wall time 245.58 s; CLI 208.357 s, server 105.199 s, store 61.628 s, vault 182.767 s.
- `make test-race`: passed, FTS5 enabled and cache disabled. Wall time 257.85 s; CLI 212.814 s, server 110.128 s, store 67.926 s, vault 189.339 s.
- `go vet -tags fts5 ./cmd/capy ./internal/config ./internal/platform ./internal/server ./internal/store ./internal/sqliteutil`: passed.
- A JSON-output run of credential config/parser/admission tests and the three generated-artifact guards passed: 176 test/subtest pass events, zero skips and zero failures. This explicitly verifies permission-mode cases execute on this host.
- After the test-only Git fixture correction, the complete `TestMCPProjectCredentials` group passed again normally (10.561 s) and with `-race` (11.105 s), without skips. The full suites and targeted vet above preceded that correction; production code did not change.
- `git diff --check` passed. Relative documentation link targets were checked; stale README license and architecture ADR/contributor links were corrected. Final status contains only intended documentation and the strengthened MCP test.

The runner uses a fully specified child environment with synthetic knowledge/vault keys, temporary HOME/XDG/config/data/cache/temp/vault paths, and no inherited project or Claude/Codex discovery overrides. Go caches/module paths are explicit. Existing local socket tests ran with permitted loopback/Unix-socket access. No live credentials/databases or real transcript corpora were used for validation. Optional live-corpus canaries remain outside this synthetic verification.

Normal/race suite logs are temporary local artifacts under `/tmp/capytask116gsr2ynq/`; the admission/artifact JSON log is `/tmp/capytask11jsqq43xj/matrix.log`; revised MCP test logs are under `/tmp/capytask11rewdl0u2/`. They are not repository files. The race flag instruments the test processes and in-process store/server lifetime tests; existing CLI/stdio helpers build ordinary FTS5 subprocess binaries. No actual legacy-binary downgrade or daemon-backed host smoke test was performed.

## Reviews and reflection

`kk:review-code:isolated` used an independent `code-reviewer` agent and two-step external PAL `gemini-3.1-pro-preview` review. The cumulative review covered 44 implementation/test/wrapper/documentation files from the feature baseline, rather than only Task 11's documentation delta. The Go SOLID, removal, security, style, error, injection, naming, database, and concurrency checklists applied. `kk:review-spec:isolated` independently compared all eleven tasks and durable docs to the implementation.

### Corroborated coverage finding — fixed and re-reviewed

The code reviewer reported **P3**, and the independent spec reviewer reported **P2 MISSING_IMPL**: `TestMCPProjectCredentials` created ordinary temporary projects for the subdirectory/override cases, so those fixtures did not prove the matrix's Git-root discovery and cross-repository override behavior. PAL also reported this as **LOW** after receiving the code review finding; that repetition is not independent corroboration.

The subdirectory case now initializes a disposable Git repository and creates a conflicting nested config/credential, so falling back to a nearer marker would fail the selected-project assertions. Explicit override cases initialize both the target and launch-directory repositories. Existing real index/search/doctor checks remain; assertions now exclude an unintended `.project` marker as well as a database. The existing bounded Git helper and isolated child environment are reused, with no dependency change. Both independent reviewers re-read the delta and returned **APPROVE / CONFORMANT**, with no remaining findings. Focused normal/race execution passed as recorded above.

### External lifecycle finding — separate follow-up

PAL reported **LOW: “Data race on lazy store fields during shutdown”** at `internal/server/server.go`'s `shutdown`: its `s.store` / `s.vault` reads may overlap first initialization from an active request during signal shutdown.

**Author context:** the `Serve`/`shutdown` bodies and unsynchronized pointer reads are unchanged from baseline `d9e09c1`. The feature passes captured credentials inside the existing `sync.Once` initialization; it introduces no shutdown synchronization change. Full race tests passed, but they do not prove this signal-versus-first-open interleaving is safe, and the subprocess candidates are not race builds. This remains a potential pre-existing lifecycle issue, not a reproduced defect or a reason to change credential semantics.

The independent code reviewer also checked this scope: credential fields are set before server publication, vault initialization is unchanged, and doctor already initialized the store through its former FTS5 check. It found no new or materially worsened overlap from this feature and agreed the lifecycle concern should not block it.

**Next steps:** in a separate server-lifecycle change, add a deterministic first-`getStore`/`getVault` versus shutdown race test, exercise it under `-race`, and coordinate request draining, lazy publication, and idempotent close. Validate signal, EOF, and parent-death paths and retained database/checkpoint behavior. A pointer-only atomic replacement should not be assumed to solve closing during active use. This follows the feature's explicit shutdown-overhaul exclusion; it is recorded rather than silently dismissed.

No systemic P0/P1 findings to index and no intentional specification deviations to index. No new project conventions emerged beyond the design and this review record; prior stats-query and close-error follow-ups remain in the [baseline verification](cove-2026-09-30.md#additional-findings-and-follow-ups).

The final implementation follows the agreed design without a dependency or production-code change in Task 11. The notable verification lesson was that a successful cwd-subdirectory launch alone did not establish Git-root discovery: a real repository and conflicting nearer marker made the intended contract observable. Documentation also needed to separate source selection from authentication and preserve the narrower legacy-decoder and race-subprocess evidence limits.
