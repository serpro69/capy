# Codex hooks v2 verification

## Scope and environment

Task 1 covers direct owner-validated launch, final invocation grammar, PATH
prerequisites, failure/cancellation behavior, generated artifacts and diagnostics.
Task 2 adds bounded hook stdin; its evidence and limits are recorded separately
below. Atomic guidance, main-session evidence and final live host acceptance
remain Tasks 3–5. Historical v1 results remain in [verification.md](verification.md).

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

## Task 1 review and limits

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
- Task 1 did not establish input deadlines, guidance markers or main-session
  observation behavior. Task 2's input evidence follows below; Tasks 3–4 remain
  pending.

The main implementation surprise was mcp-go joining workers before returning
from `Listen`: cancellation must happen at either transport failure boundary,
and SIGPIPE must not skip cleanup. The two independent findings sharpened that
contract without changing the executor or adding another lifecycle supervisor.

Optional editorial follow-up: `kk:clarify-docs` on this record and the updated
README/architecture launch sections; it is not required for Task 1 acceptance.

## Task 2 acceptance evidence

Codex hook input now requires EOF within a time and serialized-byte budget
before reaching parsing, context resolution, policies or event handlers.
Ordinary events use 2-second idle / 10-second absolute / 8-MiB limits;
SessionEnd uses 100-ms idle / 250-ms absolute / 64-KiB limits. Only a positive
read resets idle time. Complete malformed PreToolUse input still returns an
exit-0 structured denial; transport failures return exit 2, diagnostic stderr
and empty stdout. Other events return diagnostic exit 1 on transport failure.
Claude retains its existing reader, and MCP never uses this admission path.

| Contract | Evidence |
|---|---|
| Owned, deadline-capable pipe input | `TestCodexHookInputDescriptors`: inherited-style blocking anonymous pipe, empty/partial/complete-looking input held open, closed writer, regular file, directory/device/closed descriptor rejection and unreadable file. After closing the borrowed original, a writer gets EPIPE, proving no duplicate reader remains |
| Framing, timing and bytes | `TestCodexHookInputBudgets`: exact cap/cap-plus-one across read chunks, raw UTF-8 and escaped JSON sizes, empty EOF, deterministic zero-read idle expiry, trickle absolute expiry, late positive reads, guard failure before read, and sanitized typed read failure |
| Admission before side effects | `TestCodexHookInputNoSideEffects`: every event rejects overflow before even calling the adapter parser and leaves an isolated owner empty |
| Both CLI paths | `TestCodexHookInputExitCodes`: direct `hook --platform codex` and final setup-generated commands, all five events, from a subdirectory with keys unset. Regular-file overflow and unsupported descriptors produce the required exit/stderr/empty-stdout result without usage text; completed valid/malformed/empty payloads keep their handler contracts |
| Actual production timing | `TestCodexHookInputProductionDeadlines`: ordinary idle, continuously trickling input, short SessionEnd idle/absolute profiles and prompt EOF through real inherited pipes. Writers remain open until the hook exits itself; test watchdogs do not terminate successful probes; EPIPE checks rule out leftover stdin readers |

The reader duplicates stdin, marks the duplicate close-on-exec, sets pipe
nonblocking mode before `os.NewFile`, and verifies `SetReadDeadline` support.
It closes its owned duplicate on return and creates no read goroutine. The
duplicate shares descriptor flags with stdin, so this is the CLI's sole read
path; neither file's `Fd()` may be called during reading. The Go module floor
remains 1.25.2. Context7 returned master-linked API excerpts, so the descriptor
contract was checked against the pinned
[file API](https://github.com/golang/go/blob/go1.25.2/src/os/file.go) and
[Unix implementation](https://github.com/golang/go/blob/go1.25.2/src/os/file_unix.go).

### Task 2 controlled timing results

Linux/amd64, Go 1.26.4, 2026-10-13, starting commit `b0c6aab`; same isolated
cache/config and synthetic test keys as Task 1. The installed hook subprocesses
clear both keys. Representative
wall times include process startup and owner validation:

| Probe | Observed time |
|---|---:|
| Direct ordinary empty/partial idle | 2.005 / 2.012 seconds |
| Generated ordinary empty/complete-looking JSON idle | 2.014 / 2.011 seconds |
| Generated ordinary trickle, writer open | 10.008 seconds |
| Direct/generated SessionEnd idle | 105 / 107 ms |
| Generated SessionEnd trickle, writer open | 258 ms |
| Generated normal EOF | 5 ms |

These are controlled input-producer tests, not evidence of naturally stalled
live host delivery. No launcher timeout override or generated-artifact change
was needed. The existing final supervisor, owner ID and trust state are retained.

### Task 2 verification and limits

| Check | Result |
|---|---|
| `go test -tags fts5 ./internal/hook -run TestCodexHookInput -count=1` | Passed, 0.142s |
| `go test -tags fts5 ./cmd/capy -run '^TestCodexHookInput' -count=1 -v` | Passed, 12.713s |
| `go test -tags fts5 -race -count=1 ./internal/adapter ./internal/hook ./internal/platform` | Passed, 1.042s / 8.323s / 3.825s, including generated-artifact guards |
| `GOFLAGS=-race GORACE=atexit_sleep_ms=0 go test -tags fts5 -race -count=1 ./cmd/capy -run '^TestCodexHookInput'` | Passed, 18.540s; fixture builds inherit race instrumentation, and hook fixtures disable the race runtime's exit sleep for timing assertions |
| `go vet -tags fts5 ./...` | Passed |
| Full `go test -tags fts5 -count=1 -timeout 15m ./...` | All packages except CLI/config/server passed. CLI had one fixture supplying unsupported device stdin; config/server required local sockets denied by the sandbox |
| `go test -tags fts5 -count=1 ./cmd/capy -run '^TestGeneratedLaunch(ProjectCredentials\|VaultMigration)$'` | Passed, 16.506s, after supplying empty-pipe EOF in the credential fixture |
| `go test -tags fts5 -count=1 ./cmd/capy -run '^TestGeneratedPreCommitProjectCredentials$'` | Passed, 14.726s; completes verification of every test using the changed helper |
| `go test -tags fts5 -count=1 -timeout 15m ./internal/config ./internal/server` with local socket access | Passed, 0.073s / 154.625s |

Together with the unchanged-package and other CLI results from the full run,
these targeted reruns complete passing coverage of every package and affected
fixture. No assertion was weakened for sandbox restrictions. Final vet, gofmt
and Git whitespace checks pass. No generator or committed generated artifact
changed in this task.

The independent [Task 2 code review](.reviews/task-2-v2-code-review-2026-10-13.md)
approved the implementation and fixture correction with no findings. PAL's
external review could not run because its configured API key was rejected.

- Supported anonymous-pipe behavior is verified on Linux. macOS runtime behavior
  remains unverified; no broader platform claim is made.
- Regular redirected files have size limits and time checks between reads.
  Deadline checks cannot interrupt blocked filesystem/kernel I/O.
- Budgets cover input reading after descriptor setup and owner preflight, not
  the entire hook process. An earlier host timeout or stalled startup can still
  prevent delivery of capy's diagnostic.
- Terminal, directory and device stdin are rejected. Empty EOF is supported
  through a pipe or regular file; `/dev/null` is an unsupported device.
- Search, indexing, chunking and executor code did not change, so quality
  benchmarks do not apply. Final live v2 host acceptance remains Task 5.
