# Task 1 isolated code review — 2026-10-03

Scope: Task 1, freeze synthetic and real-corpus compatibility. Tasks 2–6 remain
pending and were explicitly excluded from missing-implementation findings.
Production code did not change. Evidence is in [verification.md](../verification.md).

Reviewers: independent `code-reviewer` sub-agent and PAL `codereview` using
`gemini-3.1-pro-preview` (two-step external review). Both received the complete
diff, including untracked test/golden additions, Task 1 scope, design context,
and the Go review checklists. Neither was given private corpus/baseline files.

Go activated on the three changed `.go` test files. Applied checklists: SOLID,
removal plan, security, code style, error handling, injection/input handling,
and naming. No other profile activated. No removal work was identified.

## Findings and resolutions

### Independent code reviewer

- **P2 — failure diagnostics expose private rollout paths.** Raw filesystem
  errors passed to `require.NoError` retain paths, and `require.Empty` on the
  vanished-file map prints its keys. Fix: strip `fs.PathError` path wrappers
  while preserving the underlying cause, and assert on the vanished count.
  **Resolved:** `codexParityIOError` handles read/discovery errors;
  `TestCodexParity_IOErrors` verifies omitted paths and `errors.Is` semantics.
  The reviewer checked the resolution separately and returned **APPROVE** with
  no remaining actionable findings.

### External reviewer

- **MEDIUM — missing context wrapping in `writeCodexParityBaseline`.** Returned
  file/JSON errors need operation context. **Resolved:** baseline creation,
  reading for the manifest hash, JSON encoding, and companion writing now wrap
  errors with `%w` and a specific operation.
- **MEDIUM — missing context wrapping in `readCodexParityBaseline`.** Returned
  TSV/JSON/read errors need operation context. **Resolved:** baseline reading,
  companion decoding, and hash-verification reads now wrap their errors.

These are the external review's native severity labels. Its quoted line numbers
referred to embedded review context; the named functions were verified against
the actual source before applying fixes.

## Final validation

Focused compatibility and harness tests pass with `-race -tags fts5` after all
fixes. Full-suite results and the CLI configuration-isolation rerun are recorded
in the verification document. The local corpus gate compared 480 unchanged
recordings with zero output mismatches and every required category represented.

No corroborated duplicate findings, P0/P1 findings, or deferred Task 1 issues.
No findings to index. The separately discovered pre-existing CLI test-isolation
issue has a concrete follow-up in verification.md; it is outside this task.
