# Codex hooks v2: simpler launch, bounded input and evidence-based routing

> Status: proposed — design drafted; implementation pending
> Created: 2026-10-12
> Baseline: capy `112f2c8` (includes the Codex `hooks.state` parser fix); Codex CLI 0.162.1
> Prior feature: [design.md](design.md) · [completed tasks](tasks.md) · [v1 verification](verification.md)
> Next: [implementation-v2.md](implementation-v2.md) · [tasks-v2.md](tasks-v2.md)

## 1. Purpose and scope

This amendment covers four changes inside capy's existing integration:

1. Dispatch from the validated Go launcher directly and require `capy` on PATH,
   eliminating the project-wrapper round trip and fallback-location search.
2. Bound Codex hook stdin reads so a writer holding the pipe open cannot leave
   enforcement waiting indefinitely.
3. Make the existing once-per-session guidance claim atomic across processes.
4. Apply recent-success, one-use HTTP routing evidence to the Codex main agent,
   extending the approach already used for children.

The intended users are developers running the installed capy Codex hooks,
including sessions that issue concurrent tool calls or lack a callable capy MCP
tool. The five-event catalog remains unchanged. This is an amendment to v1,
not a replacement for its portable owner, policy, trust or child contracts.
The unsuffixed design/implementation/tasks and their completed evidence remain
historical v1 artifacts. V2 implementation follows the `-v2` documents explicitly.
Where the documents differ, v2 supersedes v1 for Codex launch/configuration,
input admission, shared guidance persistence and main-session HTTP routing.
Owner validation, policy precedence and other unchanged v1 requirements remain
inherited acceptance criteria. Both iterations belong to the same PR: **there
is no backward-compatibility requirement for the interim v1 launch format**.
Implement only the final format; do not add a v1 parser or automatic migration.

The user selected the main-agent behavior: **allow with guidance while tool
availability is unverified; after a suitable capy tool succeeds, permit one
redirect using recent evidence**. Security denies and matched approval rules
still block first. This deliberately changes v1's unconditional main-agent
HTTP-routing denial; it does not change command-policy enforcement.

Atomic guidance is shared with Claude because the existing helper is shared;
its advisory semantics stay the same. New stdin limits and new main-agent
availability behavior apply to Codex only. Claude's input handling, main-agent
HTTP enforcement, approval response, and existing child behavior are retained.
Existing shared guidance JSON predates this Codex PR; its read-only recognition
below preserves that older functionality, not an interim Codex launch contract.

## 2. Starting point and evidence

| Current mechanism | Evidence at the baseline | V2 change |
|---|---|---|
| `setup_codex_launch.go` / `cmd/capy/codex_launch.go` | Inline discovery/supervision launches Go, which launches a project wrapper that discovers/launches Go again | One PATH-resolved Go process dispatches after owner validation; only hooks retain a small outer failure supervisor |
| `cmd/capy/setup.go` / `SetupCodex` | Binary fallback locations and `--binary` validation can mask the environment the host will use | Explicit PATH/protocol prerequisite, checked before setup writes |
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

- Generated Codex hooks start one capy process after the compact shell
  supervisor; MCP starts that process directly. Neither path executes a project
  wrapper or performs a second binary search.
- Setup fails before writes when the PATH candidate is absent or cannot support
  the final launch protocol. Owner IDs remain stable across valid repeat setup.
- Missing/incompatible binaries and pre-tool dispatch failures still block with
  exit 2; MCP keeps protocol stdout clean and retains its execution status.
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

## 4. Simpler owner-bound launch

### 4.1 Final execution paths

| Surface | Final path |
|---|---|
| Codex hook | host → compact Bash supervisor → PATH `capy codex-launch` → owner preflight → hook handler in the same Go process |
| Codex MCP | host → PATH `capy codex-launch` → owner preflight → existing serve entry in the same Go process |
| Claude hooks/MCP | Existing Claude project wrapper and its lookup/exit behavior |

The launcher invokes `hook.Run` with the explicit Codex adapter and validated
owner for hook mode. For serve mode, factor the existing serve entry so it can
accept that owner and a context without constructing another Cobra command or
duplicating config/credential/executor/server initialization. Preserve execution
cwd; validated owner and event cwd still serve different purposes. Owner
inspection must precede stdin consumption, policies, observation state and
credential/database access. Hooks remain credential-free.

The launcher does not spawn a wrapper, another copy of capy or a project-defined
executable. `.codex/scripts/capy.sh` is removed from this repo's generated
artifacts and no longer written or required by setup/doctor. An unused copy in
a development checkout is not an executable binding and must not be run or
used as a registration signal. Do not automatically delete unrelated scripts.
Remove Codex-only branches from the shared wrapper generator and regenerate
`.claude/scripts/capy.sh`, retaining Claude's established behavior.

### 4.2 One final invocation grammar

Use one literal protocol identifier, `capy-codex-launch-v2`, in the final
generated form. The hidden Go command accepts four positional data arguments:
protocol identifier, owner ID, mode, event. Hook mode has one existing event;
serve mode has an empty event. Reject the old unversioned argument shape.

Hook definitions launch `bash -c` with one fixed compact supervisor; its `$0`
is the protocol identifier and its remaining arguments carry owner/mode/event.
It invokes external `capy` through PATH, passing those four values as data.
There is no fallback-location list, inline policy/config parser or project
script lookup. Keep the minimum supervision needed to retain stdin, forward
INT/TERM/HUP, wait/reap the child and remap unsuccessful PreToolUse launches to
exit 2 with repair text. A bare `exec` cannot perform that remapping after capy
fails. Interrupted hooks retain the established signal outcome rather than
being presented as successful policy decisions.

MCP uses `command = "capy"` and structured argv beginning with `codex-launch`
and the protocol identifier. It needs no shell: a missing MCP executable is a
host launch failure, not a pre-tool permission decision. Preserve the existing
environment forwarding and explicit storage/execution-owner separation.

`codex_owner.go` recognizes only these two final generated forms. Replace the
old recognizer rather than retaining both. Keep strict ID/mode/event validation,
JSON/TOML decoding and equivalent serialization support; preserve opaque TOML
`hooks.state`. Continue inspecting all physical ancestors, adjacent markers,
aliases and inline hooks. Removing a wrapper requirement must not remove owner,
missing-marker or overlap validation. The marker's existing format and random
ID remain unchanged; no ID rotation is needed for a different command string.

### 4.3 PATH prerequisite and setup diagnostics

Codex installations require a compatible external `capy` executable on the
PATH inherited by Codex. No implicit search of `~/.local/bin`, Homebrew paths,
`~/go/bin` or the current directory remains in the Codex launch path. Those
locations work when explicitly included in PATH. Claude retains its fallback
search; `--local` still selects the Claude settings destination, not platforms.

Before any writes in a setup invocation that includes Codex, resolve `capy` on
the setup process's PATH and perform one bounded, side-effect-free protocol
probe: `capy codex-launch --check-protocol capy-codex-launch-v2`. The candidate
must exit 0 and print exactly that identifier plus a newline. Unsupported
identifiers exit nonzero. The probe must not read stdin, resolve an owner,
load project configuration, open a database or create state. Bound it to two
seconds and 4 KiB combined captured output; timeout, overflow or unexpected
output is a prerequisite failure. Do not perform this probe on every hook call.

Check this prerequisite before Claude setup as well when the default both-
platform mode is selected, so a known PATH failure cannot leave that invocation
half-configured. `SetupCodex` must also enforce it when called independently.
Keep its test seam explicit; do not make tests depend on a developer's installed
binary. For Codex, an explicit `--binary` must identify the same executable as
the PATH candidate; otherwise report that it cannot override the generated
PATH-based launch. Claude-only setup and DB-repo mode retain their contracts.

Running `./capy setup` is permitted, but the local executable alone does not
satisfy this prerequisite. Explain how to put the intended binary on PATH and
restart Codex so it inherits that environment. Do not edit shell profiles or
PATH automatically. Setup validates its own environment, not the environment
of every collaborator or an already-running desktop/CLI host.

### 4.4 Failure, cancellation and generated artifacts

Preserve successful structured denials as exit 0. Owner, input and hook-dispatch
errors reach the existing CLI exit handling; the small hook supervisor also
catches missing/older/crashed binaries before they can evaluate the event.
PreToolUse failures exit 2 with nonempty stderr and no fabricated success.
Other hook failures remain diagnostic nonzero exits. Serve mode preserves errors
and normal exit outcomes, with diagnostics confined to stderr.

Removing the wrapper supervisor removes an intermediate process group, not the
cleanup obligations. The hook shell must forward signals to and reap its one Go
child. Direct MCP dispatch reuses server lifecycle/parent monitoring and executor
cleanup, with context propagation into the shared serve entry. Do not add a
second competing lifecycle guard or lose cleanup on stdin EOF, cancellation,
signals or initialization failure. Cancel the shared serve context before
joining background work; the existing signal path currently calls shutdown
without first canceling the startup sweep. Keep the existing 30-second parent-
death polling interval rather than claiming immediate detection. Test actual
server/executor descendants,
not only wrapper sentinels that no longer represent the execution topology.

Update generators and committed `.codex/hooks.json`, `.codex/config.toml` and
`.claude/scripts/capy.sh` together; remove the committed Codex wrapper and its
generation/drift-list entry. Update setup output, doctor checks, AGENTS artifact
inventory and relevant CLI help. A missing Codex wrapper is now normal. The
five-event catalog, matchers and context-limit rules stay unchanged. Existing
external hook handlers, unrelated config, trust state and owner marker content
must remain intact. Changed hook command text requires normal host review/trust.

## 5. Bounded Codex stdin

### 5.1 Budgets and scope

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

### 5.2 Framing and failure contract

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

Reader failures carry a typed classification to both direct hook and
`codex-launch` entry paths, which use the same CLI error mapping. Do not call
`os.Exit` in the hook package. The launcher and compact outer hook supervisor
continue to map failures to exit 2. Successful policy denials remain exit 0.
Suppress usage text for these operational input errors; diagnostics describe
the boundary and suggest inspecting the hook launcher/input producer. They
must not include input content, command bodies, keys or a permission-bypass tip.

### 5.3 Reader lifecycle

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

## 6. Atomic guidance claims

### 6.1 Representation and claiming

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

### 6.2 Upgrade, errors and reset

Before creating a new marker, recognize existing
`.capy/guidance-<component>.json` state read-only. Bound the read to 4 KiB and
require a regular non-symlink file containing one complete boolean-map JSON
object. Preserve the old membership semantics for known keys, including a
present false value, because the existing helper suppresses on key presence. Ignore unrelated
keys; malformed/oversized/unreadable legacy state is not a successful claim.
Do not rewrite, delete or eagerly migrate old files during normal hook calls.

Concurrent v2 invocations are safe because each final marker has one creator.
An older shared-guidance JSON writer cannot participate in the new atomic
claim protocol. Restart affected launchers across binary changes. A released
binary using the older shared-guidance representation may show another advisory
because it does not read the new markers; policy behavior is unaffected. This
state-format consideration does not require support for the interim Codex launch.

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

## 7. Main-agent availability through observed success

### 7.1 What evidence means

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

### 7.2 State identity and isolation

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

### 7.3 Routing order and consumption

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

### 7.4 Cleanup and presentation

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
`additionalContextLimit` omission unchanged. The launch work integrates the
existing server lifecycle; it adds no MCP tool, readiness daemon or new
credential-resolution semantics.

## 8. Rollout and development checkouts

- This PR ships one final launch format. No v1 compatibility parser, automatic
  conversion, dual-format tests or compatibility wrapper is required.
- Stop affected sessions before replacing loaded commands. Update the binary
  available on PATH and regenerate/review the final configuration, then restart
  and trust changed hooks through the normal host interface. Preserve the owner
  marker's ID when it already exists.
- A checkout containing artifacts from an earlier iteration of this same PR
  must replace/remove those obsolete capy bindings before setup can inspect the
  final format. Restore the updated committed artifacts or remove only the
  obsolete capy entries from an external terminal, preserving unrelated handlers
  and the owner marker, then rerun setup. Do not introduce permissive substring
  adoption or a legacy parser merely to automate this development cleanup.
- Fresh setup and repeated final-format setup are the supported installation
  paths. Reverting a development checkout restores its binary and generated
  files together after stopping sessions; mixed interim formats are unsupported.
- Owner overlap, missing-marker, setup idempotency, startup context and Claude
  acceptance remain mandatory. Wrapper-execution/missing-wrapper assertions
  are replaced with direct-dispatch/no-wrapper assertions. Non-Git host discovery
  and universal runtime/platform support remain unclaimed.

## 9. Diagnostics, performance and verification

Use existing stderr/slog conventions. Input failures report a stable reason
(idle, absolute deadline, too large, read/guard failure) and bounded numeric
metadata; never raw payloads. Advisory-state errors log a bounded warning and
keep the tool path usable. Avoid duplicate error logging inside helper and CLI
layers. Normal evidence record/consume operations do not log tool content or
add a user-visible progress stream.

New metrics, tracing, dashboards, alerts, profiling endpoints and RUM are
**N/A**: these short-lived local hooks add no telemetry service. Operational
evidence is returned decisions, bounded stderr, process outcomes and test/live
records. Database access is **N/A for hooks**; direct serve dispatch inherits
the existing driver, queries, transactions, pool ownership and encryption from
the [knowledge credential flow](../../../architecture.md#knowledge-credential-flow)
and [WAL close contract](../../../adr/016-wal-mode-and-checkpoint-strategy.md).
Capture credentials once, preserve store/vault ownership, and close pools before
checkpointing. No SQL/schema/migration changes are introduced. gRPC is **N/A**:
no new network API is added.

Launch removes the redundant wrapper and binary lookup; the PATH capability
probe runs only at setup. The remaining additional work is bounded file I/O: a
guidance legacy lookup plus exclusive
claim, and one main observation transaction only for relevant success/HTTP paths.
No server process scan, DB access or subprocess version probe enters PreToolUse.
Measure warm installed-hook latency before/after and investigate material
regressions; do not invent a machine-independent latency target. Quality
benchmarks are unnecessary unless implementation expands into their required
search/indexing/chunking/executor areas.

Tests must include PATH/protocol failures before setup writes, direct hook/MCP
dispatch without a project wrapper, owner preservation and signal/EOF cleanup,
real pipes held open, trickle input, exact size boundaries,
direct and generated exit codes, cross-process guidance races, legacy state,
main/child cross-consumption negatives, owner/session separation, failed writes,
TTL/cap/lock contention and policy precedence. Live verification must exercise
main fallback → successful execute → one redirect → fallback under normal trust,
from a project subdirectory, and keep raw keys out of evidence. Synthetic stdin
tests are not proof of live host delivery. Detailed steps are in the
[implementation plan](implementation-v2.md).

## 10. Assumptions and readiness

| Assumption | Required verification / response if false |
|---|---|
| Codex inherits a PATH containing a compatible capy executable | Setup probes its own PATH; live host tests verify the actual launch environment and document that each collaborator must configure it |
| The pinned Codex runtime delivers complete hook JSON promptly over a pipe | Capture timing and run large/slow-chunk fixtures; revise the proposed constants before release if normal input exceeds them |
| Pollable inherited pipes can be guarded on supported Unix targets | Prove cancellation/deadline behavior with the actual compiled CLI; do not substitute a timer around an unbounded read |
| Main PostToolUse supplies stable session identity and explicit success metadata | Root live execute round trip must produce a root-only entry; otherwise do not claim root evidence support |
| Empty child ID denotes the main caller, as already used by v1 | Verify root/type-only/child payloads; never manufacture an agent ID or infer it from transcripts |
| Local exclusive creation and observation locking have their normal semantics | Run multiple processes on supported local filesystems; network filesystems and hostile concurrent directory replacement are outside this guarantee |
| Sessions are stopped while generated launch definitions are replaced | Live regeneration/re-trust checks; mixed interim PR launch formats are unsupported |

These are proposed contracts backed by source inspection, v1 evidence and the
user's availability-policy choice. V2 is not implemented or independently
reviewed yet. The optional review recommendations do not themselves establish
readiness or test results.

## 11. Not doing

- New hook events or session continuity — the user limited this amendment to
  existing-hook behavior.
- Noisy-build routing, routine output budgets, external-MCP guidance or trivial
  command exemptions — separate from the selected launch and hook changes.
- Machine-wide readiness sentinels, heartbeat/PID discovery or host trust-state
  mutation — none proves this agent can call a tool.
- Confirmed-negative availability or per-request MCP identity protocols — the
  user selected fallback for unknown state, so no such new boundary is needed.
- Credential/store/vault changes, automatic archival or database access from
  hooks — preserve existing ownership and encryption invariants.
- New runtime config knobs, module upgrades, a general state framework or a
  broad cleanup/retention redesign — unnecessary for these bounded contracts.
- Backward compatibility with the interim v1 launch form, automatic conversion
  of its artifacts or a retained Codex wrapper — both iterations ship in this PR.
- Hosted web interception, command rewriting, native permission emulation,
  universal platform support or overlapping installations — preserve v1 scope.

## 12. Rejected alternatives

| Alternative | Reason rejected |
|---|---|
| Retain Go → project wrapper → Go | Repeats binary discovery and supervision after the owner is already validated |
| Keep Codex fallback install-directory searches | Obscures the launch environment and inflates every hook; this amendment explicitly requires PATH |
| Move the whole bootstrap into a project script | Still needs a trustworthy locator and missing-script failure handling; direct Go dispatch removes the extra dependency |
| Support both interim launch grammars or migrate v1 automatically | Unnecessary release machinery for two iterations of the same PR |
| Use bare `exec` for PreToolUse | Cannot remap a subsequently failed/missing/incompatible executable to the required exit 2 |
| Depend only on host timeout | Can delay the decision and loses capy's own failure classification; host termination is not an exit-2 response |
| Parse buffered JSON on idle or accept a prefix | Cannot prove framing/trailing-data validity and may silently truncate input |
| Unbounded read goroutine plus a timer | Returns control without reliably terminating the blocked reader |
| Lock and rewrite the whole guidance JSON map | Can work, but adds lock/atomic-replacement machinery for three independent presence bits; exclusive markers are smaller |
| Delete legacy guidance state on upgrade | Repeats existing advisories and needlessly mutates historical state |
| Any live MCP sentinel means tools are available | Cross-project/process liveness is not session-scoped tool availability |
| Keep denying main HTTP with no evidence | Conflicts with the user's selected unknown-state fallback and can route into an unavailable tool |
| Invent a reserved child ID for the main agent in the existing file | Can collide with a host-supplied child identity and couples unrelated caller scopes |
| Persist a permanent ready flag or accumulate redirect credits | A past success does not guarantee later availability and can cause repeated dead ends |
| Treat every tool error as confirmed unavailability | Execution/domain errors do not prove the tool disappeared; transport failures may produce no PostToolUse at all |
