# Final v2 isolated spec review — 2026-10-13

**Result: CONFORMANT — no unresolved findings.** All Tasks 1–5 were reviewed;
none were skipped. The in-progress task status represented this final review
gate, not missing implementation.

## Scope and method

The independent reviewer read `design-v2.md`, `implementation-v2.md` and
`tasks-v2.md` before implementation evidence, with unsuffixed v1 documents used
only for unchanged inherited contracts. The Go spec-profile slot was absent,
so generic spec methodology applied. Capy knowledge tools were unavailable.

Source/tests were checked for final launch grammar, PATH prerequisites before
setup writes, owner inspection, failure/status and MCP lifecycle, bounded EOF
admission, atomic guidance and legacy/reset behavior, isolated main execute
evidence, security precedence, cleanup, generated artifacts and documentation.

## Finding and resolution

- **OUTDATED_DOC, P3, confidence 10/10 — resolved.** README's owner-selection
  paragraph still said generated commands pass `--project-dir`. Final generated
  argv carries protocol/owner-ID/mode/event, and `cmd/capy/codex_launch.go` calls
  `hook.Run` directly with the resolved owner, as required by design §4.1.
  README now distinguishes direct generated dispatch from manual CLI selection.
  The reviewer checked the corrected wording against the generator and caller.

No P0–P2 issues, other outstanding findings, ambiguities or intentional spec
deviations were identified. No deviation needs indexing.

## Evidence and limits

The reviewer inspected recorded full-suite, race, vet, artifact and latency
results. Selected actual host records support the main advisory → execute →
redirect → advisory sequence, deny/ask enforcement, child lifecycle, both
isolation directions within 60 seconds, compact context, independent hooks and
normal SessionEnd cleanup with unchanged lock identities. The expired initial
isolation attempt was not counted as evidence preservation.

This was read-only review: tests/live acceptance were assessed from source and
retained evidence, not independently rerun. Live coverage is Linux Codex CLI
0.162.1 with the expressly permitted retained authentication/global-policy home.
Other platforms/versions, automatic non-Git discovery, uninterruptible regular-file
I/O and shorter host deadlines retain their documented limits. Guidance promises
claiming, not delivery after a killed winner. Historical toolbox duplication,
output-policy work and session continuity remain intentional exclusions.

See [final verification](../verification-v2.md#task-5-final-installed-acceptance)
and [selected host evidence](../live-acceptance-v2.json).
