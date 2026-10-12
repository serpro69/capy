# Codex hooks v2: bounded input and evidence-based routing

> Status: proposed — design drafted; implementation pending
> Created: 2026-10-12
> Baseline: capy `ac60b81` (same source tree as comparison commit `a58f90d`); Codex CLI 0.162.1
> Prior feature: [design.md](design.md) · [completed tasks](tasks.md) · [v1 verification](verification.md)
> Next: [implementation-v2.md](implementation-v2.md) · [tasks-v2.md](tasks-v2.md)

## 1. Purpose and scope

This amendment improves three mechanisms inside capy's existing hooks:

1. Bound Codex hook stdin reads so a writer holding the pipe open cannot leave
   enforcement waiting indefinitely.
2. Make the existing once-per-session guidance claim atomic across processes.
3. Apply recent-success, one-use HTTP routing evidence to the Codex main agent,
   extending the approach already used for children.

The intended users are developers running the installed capy Codex hooks,
including sessions that issue concurrent tool calls or lack a callable capy MCP
tool. The five-event catalog remains unchanged. This is an amendment to v1,
not a replacement for its portable owner, policy, trust or child contracts.
The unsuffixed design/implementation/tasks and their completed evidence remain
historical v1 artifacts. V2 implementation follows the `-v2` documents explicitly.
Where the documents differ, v2 supersedes v1 only for Codex input admission,
shared guidance persistence and Codex main-session HTTP routing/evidence. All
other v1 requirements remain inherited acceptance criteria.

The user selected the main-agent behavior: **allow with guidance while tool
availability is unverified; after a suitable capy tool succeeds, permit one
redirect using recent evidence**. Security denies and matched approval rules
still block first. This deliberately changes v1's unconditional main-agent
HTTP-routing denial; it does not change command-policy enforcement.

Atomic guidance is shared with Claude because the existing helper is shared;
its advisory semantics stay the same. New stdin limits and new main-agent
availability behavior apply to Codex only. Claude's input handling, main-agent
HTTP enforcement, approval response, and existing child behavior are retained.

## 2. Starting point and evidence

| Current mechanism | Evidence at the baseline | V2 change |
|---|---|---|
| `internal/hook/hook.go:Run` | Reads stdin with `io.ReadAll` before parsing, policy loading or state access | A bounded Codex reader before `handleEvent` |
| `internal/hook/guidance.go` | Reads a session JSON map, changes it, then writes without a cross-process claim | Exclusive per-type markers, with legacy JSON recognition |
| `internal/hook/pretooluse.go:routeBash` | Checks security first; main HTTP routes deny unconditionally; children consume suitable observations | Codex main HTTP routes use their own observations |
| `internal/hook/posttooluse.go` | Records execute/fetch success only when a stable child identity exists | Record successful main Codex execute calls separately |
| `internal/hook/observations.go` | Bounded, locked, expiring, atomically consumed child evidence | Reuse the algorithm with a distinct main scope/file pair |
| `internal/hook/sessionend.go` | Clears only the ending stable session's child observations | Also clear that Codex main session's evidence |

The preceding upstream comparison inspected context-mode **v1.0.169**, peeled
commit `589d8214d56740a28b5f7bf63167743d586b0b40`. Its
[stdin reader](https://github.com/mksglu/context-mode/blob/589d8214d56740a28b5f7bf63167743d586b0b40/hooks/core/stdin.mjs)
supplies the idle-timeout idea; its
[guidance claim](https://github.com/mksglu/context-mode/blob/589d8214d56740a28b5f7bf63167743d586b0b40/hooks/core/routing.mjs#L121)
uses exclusive creation. Its
[MCP readiness check](https://github.com/mksglu/context-mode/blob/589d8214d56740a28b5f7bf63167743d586b0b40/hooks/core/mcp-ready.mjs)
looks for a live/fresh server anywhere in a shared temporary directory. V2 takes
the availability concern but rejects that unscoped signal.

A comparison probe held stdin open after one byte: upstream's reader rejected
after a test-configured 100 ms idle timeout, while the baseline capy CLI still
waited after two seconds and was terminated by the probe. This is subprocess
evidence of the missing internal deadline, not a live host failure.

The pinned Codex
[command runner](https://github.com/openai/codex/blob/rust-v0.162.1/codex-rs/hooks/src/engine/command_runner.rs)
includes stdin writes in its outer deadline and tolerates a broken input pipe.
Its [timeout normalization](https://github.com/openai/codex/blob/rust-v0.162.1/codex-rs/hooks/src/engine/discovery.rs#L710)
defaults ordinary command hooks to 600 seconds, but SessionEnd to one second,
capped at three. Therefore SessionEnd has a separate short input budget below.
User-configured shorter host deadlines may still win; v2 cannot guarantee that
a host killing a hook will receive capy's diagnostic.

## 3. Success criteria

- For a supported pipe, idle input terminates within the configured internal
  idle budget; continuing trickles cannot extend the absolute budget. Normal
  EOF-complete input incurs no artificial wait.
- Oversized input is rejected before decoding. No incomplete/truncated input
  reaches policy loading, guidance, observations or database code.
- Direct and generated Codex PreToolUse transport failures exit **2**, emit
  nonempty diagnostic stderr, and emit no stdout. Complete malformed JSON
  retains v1's successful structured denial (exit 0).
- With writable valid state, 32 concurrent processes claiming one guidance
  type for one session produce exactly one advisory. Independent types and
  sessions cannot suppress each other.
- A main Codex session without suitable evidence receives HTTP routing advice,
  not a routing denial. A successful main execute call grants at most one
  redirect within 60 seconds; another attempt falls back unless a newer
  success has renewed evidence.
- Main, child, sibling-session, different-owner and Claude evidence cannot
  authorize one another. Existing child caps, TTL, permanent lock and
  consume-before-deny behavior remain intact.
- Security deny/ask/scanner and installation-failure tests pass in every
  availability state. All five installed events remain credential-free.

## 4. Bounded Codex stdin

### 4.1 Budgets and scope

The proposed v2 defaults are named internal constants, not new config keys,
environment variables or CLI flags. Tests inject smaller limits through an
internal options seam; generated commands use the production defaults.

| Event | Idle deadline | Absolute read deadline | Maximum serialized input |
|---|---:|---:|---:|
| PreToolUse, PostToolUse, SessionStart, SubagentStart | 2 seconds | 10 seconds | 8 MiB |
| SessionEnd | 100 ms | 250 ms | 64 KiB |

Idle time starts when the reader starts, and resets only on a positive byte
read. Absolute elapsed time never resets. These budgets start after the Go hook
process enters the reader, not before bootstrap/owner preflight. They are input
transport budgets, not whole-hook execution deadlines. SessionEnd reserves time
for startup and two bounded cleanup operations within the normal host deadline.

The byte cap is an explicit additional v2 decision, not something upstream's
idle timer provides. Count raw serialized bytes, including escaping and metadata;
accept exactly the cap when followed by EOF and reject the first byte beyond it.
Use bounded chunks and retain at most cap-plus-one serialized input bytes, with
bounded allocation overhead; never use unbounded `ReadAll` followed by a size
check. The 8 MiB allowance includes substantial
envelope/escaping overhead beyond the command scanner's separate limits. It
does not change those scanner limits. An oversized PostToolUse can lose an
availability observation; it cannot authorize a redirect. The smaller SessionEnd
budget is for lifecycle metadata, not a transcript body.

### 4.2 Framing and failure contract

EOF remains the framing boundary. A complete-looking JSON object on an open
pipe is not enough: trailing bytes may still arrive. Do not parse a prefix on
idle, repair malformed JSON, or turn an empty stalled pipe into a pass.

| Input outcome | PreToolUse | Other Codex events |
|---|---|---|
| EOF within time/size limits | Existing parser/router | Existing handler |
| Complete invalid JSON, including empty EOF | Existing denial JSON, exit 0 | Existing parse failure behavior |
| Zero-byte or partial idle stall | Exit 2, stderr, empty stdout | Nonzero diagnostic exit; no event handling |
| Absolute deadline, even while bytes arrive | Same transport failure | Same diagnostic failure |
| Size overflow or read error | Same transport failure | Same diagnostic failure |
| Unsupported input descriptor/guard setup failure | Same transport failure | Same diagnostic failure |

Reader failures carry a typed classification to `cmd/capy/hook.go`; the command
maps Codex pre-tool input failures to the existing CLI `exitError` mechanism.
Do not call `os.Exit` in the hook package. Existing wrapper/launcher supervision
continues to map failures to exit 2. Successful policy denials remain exit 0.
Suppress usage text for these operational input errors; diagnostics describe
the boundary and suggest inspecting the hook launcher/input producer. They
must not include input content, command bodies, keys or a permission-bypass tip.

### 4.3 Reader lifecycle

The production reader owns a single CLI stdin read path. For pipes, use
deadline-capable file I/O; never merely select a timer around an indefinitely
blocked goroutine. Inherited stdin is not necessarily pollable just because it
is an `os.File`: the implementation must establish/verify that capability or
return a guard-setup error. When wrapping a duplicated descriptor on Unix,
establish nonblocking mode before creating the pollable `os.File`, keep its
ownership explicit, and do not subsequently use `File.Fd()` in a way that
returns it to blocking mode. Close every owned descriptor on all paths.

Regular-file redirection remains supported with the same byte cap and deadline
checks between bounded reads; regular-file I/O is not generally cancellable via
`SetReadDeadline`. The hard stalled-writer guarantee is for supported pipes,
not uninterruptible filesystem/kernel I/O. Terminal/directory/device input need
not become a new supported interactive mode. No buffered reader or descriptor
may survive a completed CLI subprocess.

Go's module floor is 1.25.2. Context7 was queried for that version but returned
master-linked excerpts; the relevant contract was cross-checked against pinned
[Go 1.25.2 file APIs](https://github.com/golang/go/blob/go1.25.2/src/os/file.go)
and [Unix file construction](https://github.com/golang/go/blob/go1.25.2/src/os/file_unix.go).
Use existing Go/Unix facilities; no new module dependency is planned. Linux
anonymous-pipe probes are required; macOS behavior must be verified or recorded
as unverified before expanding compatibility claims.

## 5. Atomic guidance claims

### 5.1 Representation and claiming

Keep `sessionIDComponent` unchanged, including the Codex namespace before hashing.
Replace new JSON read–modify–writes with one empty regular marker per supported
guidance type:

`<owner>/.capy/guidance-<session-component>-<type>.once`

Only the internal types `bash`, `read` and `grep` are admitted; neither a tool
name nor arbitrary caller input is interpolated as a new type. Missing project
or stable session identity keeps the existing nonpersistent advisory path.

The atomic claim is exclusive creation of the final marker, with mode 0600.
There is no payload to publish partially. Serialize the advisory response before
claiming it. One successful creator returns that response; a pre-existing valid
empty regular marker suppresses it. Do not treat a symlink, directory, FIFO,
nonempty marker or arbitrary I/O error as proof that guidance was shown.
Create a missing `.capy` directory directly under the validated owner; preserve
an existing directory's mode. Validate the directory and marker type without
following state symlinks.
The project directory itself has already been normalized by hook context logic.

This promises at-most-once **claiming** with healthy state, not exactly-once
delivery to the model. A process can die after creating the marker but before
writing stdout. Losing one optional advisory is preferable to inventing a
delivery/acknowledgment protocol. Policy decisions never depend on this marker.

### 5.2 Upgrade, errors and reset

Before creating a new marker, recognize existing
`.capy/guidance-<component>.json` state read-only. Bound the read to 4 KiB and
require a regular non-symlink file containing one complete boolean-map JSON
object. Preserve the old membership semantics for known keys, including a
present false value, because v1 suppressed on key presence. Ignore unrelated
keys; malformed/oversized/unreadable legacy state is not a successful claim.
Do not rewrite, delete or eagerly migrate old files during normal hook calls.

Concurrent v2 invocations are safe because each final marker has one creator.
Concurrent old/new binaries are not a supported upgrade mode: an old writer
cannot participate in the new atomic protocol. After upgrading, restart affected
launchers. On downgrade, v1 may show an extra advisory because it does not read
the new markers; policy behavior is unaffected.

On invalid or unwritable state, return the normal advisory without persisting a
claim and report a bounded warning. This preserves best-effort guidance. The
once-only promise is qualified by successful valid state access; failure must
never deny a tool or change any security decision.

`ResetGuidanceFile` removes the one legacy file and only the three known markers
for that exact component, never a glob of neighboring session IDs. It is a
quiescent test helper, not a concurrent production reset protocol. SessionEnd
continues to leave guidance state alone. Storage remains one legacy file plus
at most three v2 markers per stable session; v2 does not add automatic retention
or a session-directory sweep. Existing `.capy/**` ignore rules already cover them.

## 6. Main-agent availability through observed success

### 6.1 What evidence means

No process probe, heartbeat, trust record or MCP registration flag is used.
A PostToolUse success proves only that a suitable tool completed for the
identified host session in the selected owner context recently. It does not
prove permanent availability or grant host permission to run a replacement.

For the new Codex main path, suitable evidence is a successful **`capy_execute`**
call. The existing Bash HTTP routes need arbitrary-code execution; a successful
fetch, stats, search, batch or execute-file call is not interchangeable evidence.
Keep the current matcher, which also observes fetch for existing child handling.
Reuse `successfulPostToolUse` and its explicit failure/cancellation checks.
Do not infer availability from prose such as an error string inside successful
content, and do not reinterpret an ordinary tool error as transport unavailability.

Three distinct concepts are retained:

| Knowledge state | Meaning | Routing behavior after security passes |
|---|---|---|
| Unverified | No observation, expired/consumed state, missing identity, or state access failure | Pass with bounded discovery/native-tool guidance |
| Recent suitable success | Matching owner/session execute success less than 60 seconds old | One denial redirect after successful atomic consumption |
| Confirmed unavailable | Would require an authoritative host signal, which this integration does not have | Not claimed or persisted; no need to distinguish it to provide fallback |

The user chose advisory fallback for unknown availability. Therefore v2 needs
no new producer of negative availability information. A failed/canceled call
does not mint or renew positive evidence; it also does not classify the server
as unavailable. An earlier unconsumed success expires normally. After a
redirect consumes it, a failed retry cannot recreate it.

### 6.2 State identity and isolation

Introduce a separate Codex-main observation scope using:

- `.capy/codex-main-observations.json`
- `.capy/codex-main-observations.lock` — permanent inode, never unlinked/replaced

Reuse the child store's read/validate/expire/lock/atomic-write/consume algorithm
through a narrow internal scope choice, rather than copying it or creating a
generic persistence framework. Keep the existing child file, lock, schema and
validation unchanged.

The main file uses the same envelope and entry fields as the child store:
`entries`, `session`, `agent`, `tools`. In this distinct file `agent` must be
empty, and `tools` admits only `capy_execute`. Child storage still requires a
nonempty agent and admits its existing two tools. State paths are selected by
trusted code from explicit adapter identity and the raw nonempty/empty child-ID
distinction, never by a synthetic agent name that could collide with a real ID.

Main sessions require the existing stable `codex:`-namespaced session identity.
The physical owner directory provides project scope; its portable marker ID
alone is not a global storage key. Different sessions, sibling worktrees,
separate copies, Claude sessions with the same raw ID, and child agents do not
share main evidence. A type-only payload remains main handling, as in v1.

Use 60-second TTL, 128 entries, 64 KiB serialized state and a 20 ms maximum
lock wait **per scope**. Main plus child files therefore have a combined ceiling
of 256 entries/128 KiB; this is an explicit extra budget, not a change to the
child cap. Invalid/future timestamps, malformed/oversized files and failed
reads/writes result in advisory fallback. Positive records create state;
unobserved fallback and cleanup of absent state create nothing. Store only safe
identity components and success timestamps, never inputs, outputs or credentials.

### 6.3 Routing order and consumption

The installed owner preflight remains first. After complete input admission,
parsing, identity/project validation and command scanning:

1. Evaluate security denies and matched asks with existing precedence/source
   provenance. Return those decisions without consuming routing evidence.
2. Identify existing unsafe HTTP candidates using the existing detection and
   silent-download exceptions. V2 does not broaden command detection.
3. For a Codex main candidate, consume its suitable evidence under the main
   lock. Commit removal before returning the routing denial. Without successful
   consumption, return advisory context and no `permissionDecision` field.
4. For a child, use its existing child store and algorithm. For a Claude main
   agent, retain v1 enforcement. Ordinary non-HTTP calls retain normal guidance.

Do not return a bare `allow` decision, modify command arguments, approve another
tool, or suggest bypassing an actual security rule. A routing denial should name
the observed execute tool and explain that native fallback is available if the
suggested call fails or disappears. Fallback must say availability is unverified,
suggest at most one tool-discovery attempt when supported, and retain bounded
extraction advice. It must not claim that MCP is down.

One success is a replaceable timestamp, not an accumulating redirect credit.
With no new success, concurrent HTTP attempts yield at most one redirect. A
new success arriving after consumption can legitimately enable another redirect.
No evidence is consumed for a safe download, ordinary Bash command, direct capy
MCP call, or policy denial.

### 6.4 Cleanup and presentation

On root Codex SessionEnd, independently remove that stable session from main
and child scopes. Acquire/release their locks separately; never nest locks.
Failure in one cleanup must not prevent the other. Ignore child-labelled Codex
SessionEnd for this root cleanup; ordinary host child completion is not a root
SessionEnd. Claude retains its existing cleanup. Do not create absent stores,
remove permanent lock files, or open either database. TTL/caps bound missed
SessionEnd evidence.

Update runtime Codex guidance and generated `.capy/AGENTS.md` to distinguish
main/child evidence from Claude main enforcement. Preserve the 4,096-byte
SessionStart/SubagentStart payload bound and unrelated toolbox text. Keep the
five-event catalog, anchored matchers, owner IDs, hook trust handling and
`additionalContextLimit` omission unchanged. No new server lifecycle code,
MCP tool, readiness daemon or credential-resolution path is needed.

## 7. Compatibility, rollout and rollback

- Install one v2 binary consistently for a project, rerun setup for generated
  routing text, and restart affected sessions/launchers. Do not rotate the
  owner marker. V1 guidance/evidence files remain readable under their old rules.
- Update the runtime routing generator and committed counterpart together.
  If implementation changes any generated wrapper or registration despite this
  plan, update both sides and revalidate exact-hook trust implications; do not
  silently change generated launch grammar.
- Downgrading restores unconditional Codex main HTTP routing and may repeat
  advisory messages. It does not turn a policy deny/ask into an allow. V1 ignores
  the separate main observation file, so no child-state migration is required.
- All v1 owner overlap, missing-marker, setup idempotency, startup context and
  Claude regression acceptance remains mandatory. Non-Git host discovery and
  universal runtime/platform support remain unclaimed.

## 8. Diagnostics, performance and verification

Use existing stderr/slog conventions. Input failures report a stable reason
(idle, absolute deadline, too large, read/guard failure) and bounded numeric
metadata; never raw payloads. Advisory-state errors log a bounded warning and
keep the tool path usable. Avoid duplicate error logging inside helper and CLI
layers. Normal evidence record/consume operations do not log tool content or
add a user-visible progress stream.

New metrics, tracing, dashboards, alerts, profiling endpoints and RUM are
**N/A**: these short-lived local hooks add no telemetry service. Operational
evidence is returned decisions, bounded stderr, process outcomes and test/live
records. Database/gRPC profile topics are **N/A**: hooks remain database-free
and no network API is added. Existing encryption and vault contracts are inherited
unchanged from [v1](design.md#5-adapter-policy-and-state-contracts).

The additional work is bounded file I/O: a guidance legacy lookup plus exclusive
claim, and one main observation transaction only for relevant success/HTTP paths.
No server process scan, DB access or subprocess version probe enters PreToolUse.
Measure warm installed-hook latency before/after and investigate material
regressions; do not invent a machine-independent latency target. Quality
benchmarks are unnecessary unless implementation expands into their required
search/indexing/chunking/executor areas.

Tests must include real pipes held open, trickle input, exact size boundaries,
direct and generated exit codes, cross-process guidance races, legacy state,
main/child cross-consumption negatives, owner/session separation, failed writes,
TTL/cap/lock contention and policy precedence. Live verification must exercise
main fallback → successful execute → one redirect → fallback under normal trust,
from a project subdirectory, and keep raw keys out of evidence. Synthetic stdin
tests are not proof of live host delivery. Detailed steps are in the
[implementation plan](implementation-v2.md).

## 9. Assumptions and readiness

| Assumption | Required verification / response if false |
|---|---|
| The pinned Codex runtime delivers complete hook JSON promptly over a pipe | Capture timing and run large/slow-chunk fixtures; revise the proposed constants before release if normal input exceeds them |
| Pollable inherited pipes can be guarded on supported Unix targets | Prove cancellation/deadline behavior with the actual compiled CLI; do not substitute a timer around an unbounded read |
| Main PostToolUse supplies stable session identity and explicit success metadata | Root live execute round trip must produce a root-only entry; otherwise do not claim root evidence support |
| Empty child ID denotes the main caller, as already used by v1 | Verify root/type-only/child payloads; never manufacture an agent ID or infer it from transcripts |
| Local exclusive creation and observation locking have their normal semantics | Run multiple processes on supported local filesystems; network filesystems and hostile concurrent directory replacement are outside this guarantee |
| Sessions are restarted across binary upgrades/downgrades | Document the mixed-version limitation; do not claim atomic coordination with old JSON writers |

These are proposed contracts backed by source inspection, v1 evidence and the
user's availability-policy choice. V2 is not implemented or independently
reviewed yet. The optional review recommendations do not themselves establish
readiness or test results.

## 10. Not doing

- New hook events or session continuity — the user limited this amendment to
  existing-hook behavior.
- Noisy-build routing, routine output budgets, external-MCP guidance or trivial
  command exemptions — separate choices from the three selected changes.
- Machine-wide readiness sentinels, heartbeat/PID discovery or host trust-state
  mutation — none proves this agent can call a tool.
- Confirmed-negative availability or per-request MCP identity protocols — the
  user selected fallback for unknown state, so no such new boundary is needed.
- Credential/store/vault changes, automatic archival or database access from
  hooks — preserve existing ownership and encryption invariants.
- New runtime config knobs, module upgrades, a general state framework or a
  broad cleanup/retention redesign — unnecessary for these bounded contracts.
- Hosted web interception, command rewriting, native permission emulation,
  universal platform support or overlapping installations — preserve v1 scope.

## 11. Rejected alternatives

| Alternative | Reason rejected |
|---|---|
| Depend only on host timeout | Can delay the decision and loses capy's own failure classification; host termination is not an exit-2 response |
| Parse buffered JSON on idle or accept a prefix | Cannot prove framing/trailing-data validity and may silently truncate input |
| Unbounded read goroutine plus a timer | Returns control without reliably terminating the blocked reader |
| Lock and rewrite the whole guidance JSON map | Can work, but adds lock/atomic-replacement machinery for three independent presence bits; exclusive markers are smaller |
| Delete legacy guidance state on upgrade | Repeats existing advisories and needlessly mutates historical state |
| Any live MCP sentinel means tools are available | Cross-project/process liveness is not session-scoped tool availability |
| Keep denying main HTTP with no evidence | Conflicts with the user's selected unknown-state fallback and can route into an unavailable tool |
| Invent a reserved child ID for the main agent in the existing file | Can collide with a host-supplied child identity and couples migration to the v1 schema |
| Persist a permanent ready flag or accumulate redirect credits | A past success does not guarantee later availability and can cause repeated dead ends |
| Treat every tool error as confirmed unavailability | Execution/domain errors do not prove the tool disappeared; transport failures may produce no PostToolUse at all |
