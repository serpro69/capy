# Codex hooks v2 verification

## Scope and environment

Task 1 only: direct owner-validated launch, final invocation grammar, PATH
prerequisite, failure/cancellation behavior, generated artifacts and diagnostics.
Tasks 2–5 remain pending, including bounded hook stdin, atomic guidance,
main-session evidence and final live host acceptance. Historical v1 results
remain in [verification.md](verification.md).

Checked 2026-10-13 on Linux/amd64, Go 1.26.4, against starting commit `0c3f921`.
Every build/test used `-tags fts5`. Tests used synthetic `CAPY_DB_KEY` and
`CAPY_VAULT_KEY`, `XDG_CONFIG_HOME=/tmp/capy-v2-empty-config`, and a writable
`GOCACHE=/tmp/capy-v2-go-cache`. Installed-hook fixtures cleared both keys;
MCP fixtures used isolated homes/configuration and synthetic credentials.
No module dependency/version or database format changed. Existing encryption,
credential selection and WAL close contracts are inherited from
[architecture](../../../architecture.md#knowledge-credential-flow) and
[ADR-016](../../../adr/016-wal-mode-and-checkpoint-strategy.md).

## Task 1 acceptance evidence

| Contract | Evidence |
|---|---|
| One final hook/direct-MCP grammar | `TestCodexFinalLaunchBindings`, `TestCodexOwnerBindings`, `TestCodexOwnerInlineBindings`: strict executable/protocol/ID/mode/event/arity, alternate MCP aliases, equivalent JSON/TOML serialization and opaque trust metadata |
| Owner protection | Existing location, overlap and copied-owner/missing-marker fixtures pass through final generated commands; all ancestor bindings retain their adjacent-marker checks |
| Direct dispatch | `TestCodexDirectDispatch`, `TestCodexLaunchLocations`, `TestCodexMCPLaunchLocations`: root/subdirectory, spaces, moved trees, sibling worktrees and non-Git command resolution; initialized MCP lists ten tools and executes a harmless call; configured external storage stays separate |
| No wrapper dependency | Absent, sentinel, unreadable and symlink Codex wrapper fixtures all launch directly; sentinels are never invoked; doctor ignores unused wrapper-only files |
| Credential separation | `TestGeneratedLaunchProjectCredentials`, `TestGeneratedLaunchVaultMigration`: key-file/dotenv/environment precedence, linked worktree storage ownership, environment-only vault opt-in; Claude retains its wrapper-specific CLI cases |
| Bounded setup prerequisite | `TestCodexSetupPathPrerequisite`: missing/nonexecutable/incompatible/timeout/overflow/wrong-output candidates, combined 4 KiB output limit, exact newline/stream response, matching symlink versus mismatched `--binary`, one probe per prepared invocation; no chmod/config/marker changes on failure |
| No partial default-both setup | `TestCodexSetupPathPrerequisiteCLI`: prerequisite failure leaves the target empty; Claude-only and DB-repo setup retain their behavior |
| Metadata probe has no project/input effects | `TestCodexProtocolProbe`: invalid project config/owner, unset credentials and an open stdin pipe do not affect the exact protocol response; unsupported identifiers fail |
| Failure/status/stdin | Supervisor failure fixtures cover missing/incompatible/crashed binaries, owner failure, non-pre-tool error status and successful exit-0 denial; direct/generated handler responses match for identical input; MCP initialization failure has empty stdout |
| Actual process cleanup | `TestCodexLaunchFailureAndCancellation`: one real Go child under the hook shell, forwarded INT/TERM/HUP and reaping; initialized direct MCP EOF/signals terminate real executor groups and sleeping descendants |
| Closed output cleanup | `TestCodexMCPClosedOutputCleanup`: detached executor plus active foreground call, closed stdout, EOF/signal/synchronous-response triggers; requires cleanup and diagnostic exit 1. `TestStdioWriteFailureCancelsWorkers` separately tests a non-SIGPIPE output failure against actual mcp-go worker joins with stdin open |
| Generated artifacts and coexistence | Whole-file equality, merge idempotency and completeness guards pass; owner ID retained, external hooks/configuration/trust metadata preserved, Codex wrapper removed from generation and inventory |

The starting checkout's committed wrappers, Codex registration and routing copy
were behind their existing v1 generators. Task 1 synchronizes the final launch
artifacts and restores `.capy/AGENTS.md` to its unchanged routing generator.
The two external toolbox handlers remain present. No host trust record or
feature override was changed, and no installed binary was replaced.

## Validation commands and results

All commands below used the isolated environment above. Full repository/server
checks required local socket access for existing HTTP fixtures.

| Check | Result |
|---|---|
| Focused final launch/owner/PATH/supervisor fixtures | Passed: platform 2.050s, CLI 7.850s |
| Direct dispatch/protocol/process cancellation fixtures | Passed: CLI 8.728s |
| Complete platform suite after artifact synchronization | Passed: 2.270s, including all three drift guards |
| `go test -tags fts5 -race -count=1 ./internal/adapter ./internal/hook ./internal/platform` | Passed: 1.013s / 8.090s / 3.792s |
| Focused CLI Codex/setup/generated-credential tests with `-race` | Passed: 44.570s |
| Actual subprocess race instrumentation: `GOFLAGS=-race GORACE=atexit_sleep_ms=0 go test -tags fts5 -race -count=1 ./cmd/capy -run 'TestCodex(DirectDispatch\|LaunchFailureAndCancellation\|MCPClosedOutputCleanup\|ProtocolProbe)$'` | Passed: 72.445s; `GOFLAGS` reaches each fixture's `go build` |
| `go test -tags fts5 -race ./internal/server -run 'Test(StdioWriteFailureCancelsWorkers\|StartLifecycleGuard_Cleanup)$' -count=1` | Passed: 1.069s |
| `go vet -tags fts5 ./...` | Passed after the final code changes |
| Full `go test -tags fts5 -count=1 -timeout 15m ./...` | Every package except CLI passed; CLI reported four obsolete setup/wrapper fixtures, subsequently rebased and passed in focused runs |
| Final `go test -tags fts5 -count=1 -timeout 15m ./cmd/capy ./internal/server` after fixture and lifecycle fixes | Passed: CLI 297.503s, server 150.425s. Together with the unchanged packages from the full run above, every package has a passing result |

The first sandboxed attempt could not write the default Go build cache; moving
the cache to `/tmp` resolved that. The sandbox also denied a server test's local
socket; the socket-enabled full run passed that package (171.336s). No failed
assertion was relaxed to accommodate either environment restriction.

Final artifact guard rerun passed (0.005s). `gofmt` and Git whitespace checking
also pass; the latter uses the ordinary blank/trailing/space-before-tab checks
because the user's global `indent-with-non-tab` setting flags the existing
two-space JSON format. No repository or global Git setting was changed.

Search/indexing/chunking/executor production code did not change, so the
repository's quality benchmarks do not apply. Existing CI/distribution
conventions remain unchanged; no new metrics, network service or schema work.

## Warm installed-hook timing

Built the starting commit and final candidate with the same Go toolchain, ran
their generated PreToolUse commands in isolated disposable owners, and alternated
five batches of 101 harmless `true` events per candidate. The first event claims
ordinary guidance; subsequent events use the warm state. No database keys were
present. Batch wall times, including shell dispatch and small fixture overhead:

| Candidate | Five batch times (seconds) | Median per invocation |
|---|---|---|
| v1 starting commit | 1.08, 1.17, 1.10, 1.05, 1.04 | 10.69 ms |
| v2 direct launch | 0.61, 0.60, 0.53, 0.54, 0.54 | 5.35 ms |

These local measurements indicate approximately 50% lower median launch cost.
Other verification ran on the same machine, so this is an indicative regression
check, not a portable latency target or controlled performance guarantee.

## Review and remaining limits

[Isolated code review](.reviews/task-1-v2-code-review-2026-10-13.md): **APPROVE**
on re-review. Both initial P1 output/cancellation findings reproduced and were
fixed with subprocess regressions. The external PAL reviewer could not run
because its configured API key was rejected. The independent reviewer inspected
tests; test execution/results above belong to the implementing agent. Findings
are recorded here and in the review, so no duplicate knowledge note was indexed.

- Linux process behavior is verified. macOS and other hosts are unverified.
- No live v2 Codex session was started or re-trusted in Task 1. Task 5 owns that
  acceptance work; static checks and generated-command tests do not establish it.
- Parent-death monitoring retains its 30-second polling interval; no immediate
  parent-death detection is claimed. No second server lifecycle guard was added.
- Runtime PATH belongs to the host environment. Install the final binary there,
  stop affected sessions before replacing loaded definitions, restart and review
  changed hooks through normal host controls. Setup does not edit PATH or trust.
- Input deadlines, guidance markers and main-session observation behavior are
  still pending Tasks 2–4, not partially claimed by this launch slice.

The main implementation surprise was mcp-go joining workers before returning
from `Listen`: cancellation must happen at either transport failure boundary,
and SIGPIPE must not skip cleanup. The two independent findings sharpened that
contract without changing the executor or adding another lifecycle supervisor.

Optional editorial follow-up: `kk:clarify-docs` on this record and the updated
README/architecture launch sections; it is not required for Task 1 acceptance.
