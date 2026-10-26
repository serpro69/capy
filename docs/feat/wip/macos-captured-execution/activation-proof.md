# Task 10 public activation evidence

> Baseline: `43a8686001e69adddc9e688acaf2596a70b144c9`
> Scope: [Task 10](tasks.md#task-10-enable-native-cli-and-ordinary-mcp-execution)
> Contract: [contracts.md](contracts.md)
> Date: 2026-10-26

Shared admission now permits the existing Darwin backend only on macOS 26,
arm64, with CGO. Both single and batch admission use the backend's fixed cleanup
scope. Linux admission still avoids process-wide subreaper preparation; only the
helper performs it. No schema, protocol, credential or policy change is included.

## Host and verification route

Native host: macOS 26.6.2 (25G83), Darwin 25.6.0 arm64, Go 1.27.2, Command Line
Tools SDK 27.0, deployment target 26.0, CGO enabled. Full Xcode is not selected.
Tests use synthetic `CAPY_DB_KEY=test-key-for-development` and
`CAPY_VAULT_KEY=test-key`, temporary fixture roots and a temporary build cache.

Installed MCP capture rejected before admission. The operator authorized the
single temporary build in [bootstrap.md](bootstrap.md#task-10-public-activation--authorized-bootstrap).
That build passed; the exception was removed immediately, original configuration
bytes matched, and an installed-hook probe verified restored routine denial.
The temporary binary then captured and recovered stdout/stderr successfully
(`c1ee91a1-cef1-4d6d-91ea-18638cb4c423`). Every subsequent verification command
uses its captured execution with an isolated owner/run store.

Evidence is retained in `bench-results/macos-captured-task10-validation/`;
`commands.json` records the exact test/vet/build programs and `environment.json`
records the temporary binary, owner and host. Original encrypted run records live
under `/private/tmp/capy-task10-validation.M1BbL6/owner/.capy/runs/`, subject to
their normal retention policy. Exported logs preserve failed attempts too.

## Native checks and fixture corrections

| Capture ID | Check | Result |
| --- | --- | --- |
| `6930aae9-d693-42e7-a718-52a0d59e072e` | Focused FTS5/integration capability, public admission and compiled CLI/MCP fixtures | Executor and execution passed. New public outcomes/owner/cwd/credential tests and recovery passed. Existing worktree fixture failed at its synthetic Git commit. |
| `c110c245-4728-4292-b3b5-99ce85b17893` | Selected FTS5/integration race checks across executor, execution, server and CLI | Executor **14.841s**, execution **22.783s**, server **34.409s** passed. CLI failed only the credential fixture's doctor path assertion. |
| `483dbd48-ba65-4613-b37e-fcf25ce4e4bc` | Corrected CLI/MCP race checks, affected-package integration vet, full FTS5 build | Passed; CLI **70.296s**. Build emitted a nonfatal denied module-stat-cache write warning and exited zero. |

The first fixture inherited host Git configuration (`commit.gpgsign=true` was
observed); its original error omitted stderr. Its synthetic commit now uses the
existing isolated fixture environment and retains command diagnostics. The next
race run passed that worktree fixture. The doctor helper expected `/var/...`,
while production owner resolution correctly reports `/private/var/...`. Resolving
the expected physical path fixes the fixture without changing production cwd or
weakening the owner assertion. Final revalidation includes the helper's other
stdio callers. No production correction was required after the initial patch.

Coverage includes compiled CLI/MCP once-only execution; byte-exact stdout/stderr
and encrypted restart/cross-interface recovery; scoped v2 proof; nonzero exits,
signals, deadlines, missing/bad runtimes and capture loss; 2-KiB response budgets;
external cwd with owner-local storage; nested/worktree owners; selected-key
failure without inherited-key fallback; blocked storage without cwd fallback;
and unchanged CLI matched-ask versus MCP deny-only behavior. The native private
failure/owner-loss suites also passed under race.

The matrix unit test rejects older/future majors and Intel/unknown architectures.
The end-to-end unsupported-host test is conditional and does not execute on this
supported host; its before-storage/before-spawn ordering is additionally inspected
in source. Existing Linux-only byte-path, mount-sandbox and retention fixtures
remain skipped here, with their original assertions intact. These native results
do not certify Linux runtime behavior.

## Benchmarks and review

Capture `5635488b-ea46-4aa5-8104-74319cc2ef13` completed the serial baseline/candidate
comparison successfully. The archived baseline reproduces the credential fixture's
physical-path failure and the four disabled-public-capture quality failures (make
exit 2). Candidate `make bench-quality` passes all store/server/vault cases,
including output sizes 4999, 5001, 10000 and 50000 bytes. Their responses are
3628, 3628, 3634 and 3640 bytes, with no knowledge indexing. All other quality
metrics, fixture hashes and diagnostic results match; differences are Git/time
metadata and the newly populated threshold results. Archive metadata reports
Git identity as unknown; the archive's actual revision is the baseline above.

The same six-sample `BenchmarkExecutorOverhead` command ran with `-benchmem` and
`-benchtime=1s` on both sources. Median latency changes:

| Runtime | Serial executor | Direct control | Parallel executor |
| --- | --- | --- | --- |
| Shell | +6.83% | +5.57% | −2.28% |
| JavaScript | +2.50% | −3.69% | +1.19% |
| Python | −11.15% | −1.03% | +0.42% |

These are descriptive samples without a statistical-significance claim; direct
controls also vary. `comparison.json` retains medians, allocations and actual
quality differences, with both raw benchmark text reports and quality JSON files
beside it. The baseline's public capture path is intentionally unsupported;
native capture latency is new evidence, not a comparable before/after claim.

Initial isolated internal source review found no defects. It also approved both
fixture corrections and the updated run reference. Final internal review
**APPROVED** Task 10 with no findings after reconciling all 18 changed files,
inspecting execution evidence, independently comparing quality results and
benchmark medians, and checking restored configuration. Tests were executed by
the implementation session, not rerun by the reviewer. Local checks passed
43 documentation links/anchors and `git diff --check`.

The external backend selected Claude/Anthropic (assumed
provider mapping); `agy` was absent, `codex` excluded as the authoring provider,
and `gemini` retired. Automatic approval review initially rejected the Claude
source export. The operator subsequently replied “I approve the review,”
authorizing that export. A refreshed 54-file bundle at
`/private/tmp/capy-task10-claude-w9469rso/` supplied all 12 criteria, the final
14 Go files, affected source, specifications and verification evidence. Prior
review conclusions were excluded from the external context.

The read-only PAL `clink` request to `claude` timed out awaiting `tools/call`
after 300 seconds. It returned no findings, observed model identity, continuation
ID or source-read receipt. Outcome: **attempted, incomplete**, not a completed
external review. All 14 candidate Go files still matched their submitted hashes
after timeout. No retry or alternate-provider request was made. Internal review
remains the completed independent review for this task; no findings need indexing.
Owner: feature implementer; preserve this limitation. A separately requested
external attempt should first resolve the recurring transport timeout and must
return actual findings and coverage before being counted as completed.

Tasks 11–13 retain all-mode acceptance, Linux/CI execution, diagnostics and release
artifact gates. Owner: feature implementer; run those tasks' specified checks and
retain their evidence before release. Darwin `observed` continues to permit the
explicitly accepted residual-process uncertainty; it never establishes host
quiescence.
