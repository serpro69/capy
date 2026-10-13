# Codex hooks v2 verification

## Scope and environment

Task 1 covers direct owner-validated launch, final invocation grammar, PATH
prerequisites, failure/cancellation behavior, generated artifacts and diagnostics.
Task 2 adds bounded hook stdin; Task 3 adds atomic shared guidance claims;
Task 4 adds observed-success HTTP routing for Codex main sessions. Their
evidence and limits are recorded separately below. Final live host acceptance
remains Task 5. Historical v1 results remain in
[verification.md](verification.md).

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
  observation behavior. Tasks 2–4's evidence follows below.

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

## Task 3 acceptance evidence

Shared guidance now uses exclusive, zero-byte 0600 markers for the three fixed
types (`bash`, `read`, `grep`). Serialization precedes claiming. With healthy
local state, one concurrent caller wins each session/type pair; a valid existing
marker suppresses the rest. Existing `sessionIDComponent` behavior and Codex's
namespaced identity are unchanged.

Legacy boolean-map JSON is recognized read-only within 4 KiB. Known-key presence
still suppresses guidance when the value is false. Invalid/unavailable state
produces the ordinary advisory and a fixed warning without creating a new claim;
warnings omit paths, session IDs and payload content. Missing identity persists
nothing. Reset removes exactly the session's old JSON and three known markers,
preserving other sessions and observation files/locks. SessionEnd remains
unchanged and leaves guidance state alone.

| Contract | Evidence |
|---|---|
| Atomic publication | `TestGuidanceAtomicClaim`: 32 simultaneous callers, one winner, independent types, empty regular 0600 markers, directory mode preserved, arbitrary types cannot create state |
| Legacy recognition | `TestGuidanceLegacyState`: all three keys, false values, unrelated keys, empty object, exact 4-KiB boundary, corrupt/empty/trailing/oversized/non-object/non-boolean/null data; legacy bytes stay identical |
| Invalid state remains advisory | `TestGuidanceStateFailureFallback`: directory/legacy/marker symlinks, dangling links, directories, FIFOs, nonempty markers, invalid types, unwritable directory and unreadable legacy file; repeated advisory with bounded payload-free warnings and no outside writes |
| No premature or anonymous claims | `TestGuidanceFormatBeforeClaim`, `TestGuidanceEmptySessionDoesNotPersist`, `TestGuidanceMissingProject`: formatting failures and missing identity cannot create suppression state |
| Exact reset and identity safety | `TestGuidanceResetIsolation` plus existing filename/session tests: exact legacy/three-marker removal, similar-looking IDs, malicious/long/Unicode IDs, empty-ID sentinels, unrelated types, symlink directory and stable observation lock inode |
| Real concurrent hooks | `TestCodexGuidanceConcurrentCLI`: synchronized 32-process waves through final setup-generated Codex commands from a subdirectory. One advisory for a shared session; mixed Codex/Claude sessions and all shared guidance types each have one winner. Second waves produce none; all nine claims survive |
| Claim without delivery | `TestGuidanceKilledWinner`: a real subprocess claims, signals readiness without writing its advisory, is killed and reaped; the next caller stays suppressed until explicit test reset |
| Inherited behavior | Existing Claude guidance, owner/cwd, Codex namespace, security, input, child observation, launch and generated-artifact tests pass with updated exact marker-path assertions |

### Task 3 verification results

Checked 2026-10-13 on Linux/amd64, Go 1.26.4, starting commit `a4130a5`.
Builds/tests used FTS5, the existing `/tmp/capy-v2-go-cache` and empty config home,
and synthetic knowledge/vault keys. Hook subprocesses used isolated fixture
environments with both keys unset. Permission-failure fixtures ran unprivileged.
The process barrier sits outside the hook to avoid consuming Task 2's stdin
budget while starting contenders; children enter the actual hook with complete
EOF-framed payloads. All processes are joined and all pipe endpoints closed.

| Check | Result |
|---|---|
| `go test -tags fts5 -count=1 ./internal/hook` | Passed, 0.633s |
| `go test -tags fts5 -count=1 ./cmd/capy -run 'TestCodexGuidanceConcurrentCLI\|TestHookContextCLI'` | Passed, 2.650s |
| `go test -tags fts5 -race -count=1 ./internal/adapter ./internal/hook ./internal/platform` | Passed, 1.012s / 9.024s / 3.798s, including all generated-artifact guards |
| `GOFLAGS=-race GORACE=atexit_sleep_ms=0 go test -tags fts5 -race -count=1 ./cmd/capy -run 'TestCodexGuidanceConcurrentCLI\|TestHookContextCLI'` | Passed, 6.255s; actual CLI fixture builds inherit race instrumentation |
| Final `go test -tags fts5 -race -count=1 ./internal/hook` after additional identity/type/symlink fixtures | Passed, 7.815s |
| Full `go test -tags fts5 -count=1 -timeout 15m ./...` with local socket access | All packages passed; CLI 323.594s, server 163.625s, vault 310.168s |
| `go vet -tags fts5 ./...`, gofmt and Git whitespace checks | Passed |

[Isolated code review](.reviews/task-3-v2-code-review-2026-10-13.md): **APPROVE**,
with no findings. PAL's external review failed because its configured API key
was rejected. Architecture and root AGENTS guidance invariants now describe the
marker/legacy layout. No generated artifact, dependency, database or retention
policy changed; quality benchmarks do not apply to this scope.

### Task 3 limits and reflection

- Claiming is at-most-once with healthy local state. A killed winner can leave
  an advisory undelivered and suppressed; no acknowledgment protocol is added.
- Invalid or inaccessible state can repeat advisories. It cannot grant or deny
  permissions. Diagnostics are intentionally fixed rather than exposing state
  paths or JSON decoder details.
- Older shared-guidance JSON writers do not participate in atomic marker claims.
  Restart affected launchers across binary changes. No eager migration or
  automatic marker cleanup is introduced.
- Linux process/filesystem behavior is verified. macOS, network filesystem
  coordination and hostile concurrent directory replacement are unverified or
  outside the guarantee. Controlled subprocess tests do not establish live host
  delivery; that remains Task 5.

Implementation followed the plan. The notable test detail was placing the
process barrier before hook startup so synchronization cannot exhaust the
already-enforced stdin idle budget. Existing traversal and owner fixtures now
assert exact per-type marker names instead of weakening their isolation checks.

Optional editorial follow-up: `kk:clarify-docs` on this verification record and
the architecture guidance section.

## Task 4 acceptance evidence

Codex main-session HTTP routing now uses recent, one-use execute evidence.
Without a matching successful root `capy_execute` observation, the hook returns
advisory discovery/native-tool context with no permission grant. A success
permits one HTTP redirect within 60 seconds, after atomic consumption commits.
A failed retry cannot renew consumed evidence; a later success can renew it.
Security denies, matched asks and scanner failures precede this decision and
leave evidence untouched. Safe downloads, ordinary commands and direct MCP
calls do not consume it.

The main scope uses `.capy/codex-main-observations.json` and its permanent
`.lock`, with empty `agent` and execute-only tools. It shares the child store's
transaction algorithm while retaining independent 128-entry/64-KiB bounds,
60-second TTL and 20-ms lock wait. Explicit platform identity selects the scope;
the raw `codex:` check validates an already-selected main caller rather than
inferring platform. Child schema and Claude main behavior remain unchanged.

| Contract | Evidence |
|---|---|
| Separate bounded storage | `TestCodexMainObservationStore`: absent-state no-op, identity/tool admission, timestamp replacement, exact TTL boundary, independent main/child caps and locks, oldest eviction, stable lock inode, malformed/trailing/oversized/unknown/future/duplicate state, invalid agent/tools and nonregular/symlink rejection |
| Root execute-only producer | `TestCodexMainObservationPayloads`: root/type-only identities, aliases, serialized/array results, transcript independence, actual children, missing session, Claude namespace collision, failure/cancellation/ambiguous flags, fetch/batch/execute-file/other tools; ineligible calls neither create nor renew main state |
| One-use routing | `TestCodexMainRoutingEvidence`: no state → advice → success → one redirect → advice → failed retry → advice → new success → redirect; expiry, missing identity, download/ordinary/MCP controls and inline HTTP |
| Policy precedence | `TestCodexMainRoutingSecurityPrecedence`: deny, ask, later compound deny and scanner limits under absent/fresh/expired/corrupt/locked evidence; exact rule/source retained and state bytes unchanged |
| Consumption failures | `TestCodexMainRoutingStateFailures`: held lock, failed staging write and corrupt state yield advice with unchanged evidence, never an uncommitted denial |
| Concurrent consumption | `TestCodexMainConcurrentRedirect`: 16 synchronized callers produce exactly one redirect and advisory responses for the rest |
| Isolation | `TestCodexMainChildIsolation`: root, child, child sibling, session sibling, another owner and a Claude raw-ID collision cannot consume each other's evidence |
| Cleanup | `TestCodexMainSessionEnd`: root-only cleanup, both scopes, missing identity, child-labelled end, sibling retention, independent failures, unchanged permanent lock inodes, absent-state no-op, Claude/main isolation and no database files |
| Installed operation | `TestCodexMainObservationLifecycle`: temporary Git installation, final setup-generated commands from a subdirectory, compact startup/child guidance, full root lifecycle, deny/ask controls before and after evidence, child isolation and root/child-labelled SessionEnd |
| Inherited contracts | Existing child store/lifecycle, launch/location/owner/credential/input/guidance tests and generated-artifact guards remain passing; root/type-only v1 fixtures now explicitly assert main state and absent child state |

### Task 4 verification results

Checked 2026-10-13 on Linux/amd64, Go 1.26.4, starting commit `805ec1d`, using
FTS5, synthetic knowledge/vault keys and the same isolated config/cache paths
as earlier tasks. Installed-hook fixtures clear both keys and never issue an
HTTP request; the payload commands are inspected by hooks, not executed.

| Check | Result |
|---|---|
| `go test -tags fts5 -count=1 ./internal/hook ./internal/platform` | Passed, 0.693s / 2.288s, including context limits and artifact guards |
| `go test -tags fts5 -count=1 ./cmd/capy -run 'TestCodex(MainObservationLifecycle\|ChildObservationLifecycle\|LaunchLocations\|DirectDispatch)$'` | Passed, 5.038s |
| `go test -tags fts5 -race -count=1 ./internal/adapter ./internal/hook ./internal/platform` | Passed, 1.015s / 9.798s / 3.820s |
| `GOFLAGS=-race GORACE=atexit_sleep_ms=0 go test -tags fts5 -race -count=1 ./cmd/capy -run '^TestCodexMainObservationLifecycle$'` | Passed, 4.828s; actual CLI fixture builds inherit race instrumentation |
| `go vet -tags fts5 ./...` | Passed |
| Full `go test -tags fts5 -count=1 -timeout 15m ./...` with local socket access | All packages passed; CLI 331.930s, server 165.512s, vault 318.510s |
| gofmt and Git whitespace checks | Passed |

[Isolated code review](.reviews/task-4-v2-code-review-2026-10-13.md): **APPROVE**,
with no findings. PAL's external expert step failed because its configured API
key was rejected. Runtime guidance, the routing generator and committed
`.capy/AGENTS.md` changed together; README, architecture and root AGENTS describe
the resulting behavior. No dependency, database, matcher/event catalog, owner
marker, host trust state or executor behavior changed. Quality benchmarks do
not apply to this scope.

### Task 4 limits and reflection

- Evidence proves a suitable tool succeeded recently for one caller in one
  owner context, not permanent availability. Unknown/unobserved calls can run
  natively after the existing security checks; no host permission is granted.
- Tests replay synthetic complete host payloads through installed commands.
  Actual live Codex observation delivery and the full main-session round trip
  under normal host trust remain Task 5.
- Linux storage/process behavior is verified. macOS and other host runtimes
  remain unverified; no universal runtime guarantee is added.
- Root SessionEnd retains separate permanent locks and never accesses either
  database. A failed scope cleanup does not prevent the other; TTL and caps
  bound evidence when cleanup is unavailable.

Implementation followed the plan with one internal scope selector and shared
transactions. The raw session namespace check is an additional admission guard;
platform selection still comes from the adapter. Existing child fixtures retain
their behavioral assertions, and the updated root fixtures assert separation
instead of simply dropping their former no-state checks.

Optional editorial follow-up: `kk:clarify-docs` on this record and the updated
README/architecture HTTP routing sections.
