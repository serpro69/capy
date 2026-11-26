# Whole-feature isolated spec review

Reviewer: independent `spec-reviewer`, no implementation conversation history.
Scope: all Tasks 1–6, including the current release-verification task. Reviewed
design, implementation, tasks, verification, review-resolution, readiness-review
and retained live-acceptance JSON. Go profile active; no Go review-spec phase
content exists in the installed plugin. Capy knowledge search was unavailable.

**Assessment: CONFORMANT after the explicit acceptance-plan revision. No
outstanding findings.** No P0, P1 or P3 findings, documentation contradictions
or unresolved ambiguities.

## Historical P2 SPEC_DEV: live policy-home isolation

Confidence: 10/10. The original implementation plan §5.2.1 required an isolated
live policy home. `internal/security/settings.go` still loads global Claude
settings, and the interactive run retained the host's authentication/trust home.
Verification disclosed this difference instead of claiming a hermetic live run.

The revised §5.2.1 permits that arrangement with isolated capy configuration,
synthetic project rules/credentials, disclosed global-policy inheritance, exact
denial provenance and a successful control. The retained host evidence meets
those conditions. Installed-command tests continue to isolate policy homes.
The reviewer confirmed that no production policy correction is required.

**Author context:** the execution environment prohibits repurposing HOME and
CODEX_HOME. The live check used the supported trust UI and existing host
authentication, with synthetic capy keys and fixture rules. The deviation was
resolved by an explicit, documented test-environment revision; it is retained
here for provenance. No user-confirmed deviation indexing is claimed.

## Coverage and limits

Traced platform dispatch, identity/project selection, policy provenance and
precedence, owner inspection, failure supervision, MCP portability, five-event
registration, external-hook preservation, child observations/cleanup, artifact
guards, doctor parity and release guidance. The final full FTS5 suite log shows
all packages passing. The reviewer inspected evidence/tests but did not rerun them.

Host evidence supports startup/child guidance, subdirectory MCP, deny/ask
behavior, one redirect followed by fallback, mixed hooks, resume and repair.
Live SessionEnd with outstanding observations and actual missing-file discovery
remain covered separately by subprocess tests. An older-binary downgrade was
documented but not run. Non-Git host discovery, other Codex versions/surfaces and
Windows remain unverified. Output budgets, toolbox PRs and issue closure are
outside scope. See [verification](../verification.md#task-6-installed-runtime-and-release-documentation).
