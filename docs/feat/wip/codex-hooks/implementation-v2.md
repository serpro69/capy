# Implementation: Codex hooks v2

> Status: in-progress — Tasks 1–2 direct launch and bounded input implemented; Tasks 3–5 pending
> Created: 2026-10-12
> Design: [design-v2.md](design-v2.md) · Tasks: [tasks-v2.md](tasks-v2.md)
> Baseline: capy `112f2c8` (includes the Codex `hooks.state` parser fix); follow the `-v2` documents, not the completed v1 task list

## 1. Orientation and boundaries

At the v1 baseline, the hook path was host → inline bootstrap → `capy codex-launch` →
owner's `.codex/scripts/capy.sh` → `capy hook --platform codex <event>`.
`internal/platform/codex_owner.go` validates every ancestor's owner/binding
before any project wrapper runs. `cmd/capy/hook.go` selects the adapter;
`internal/hook/hook.go` reads stdin, resolves project/cwd, and dispatches.
The Codex adapter supplies stable namespaced identity and supported responses.

Task 1 replaces that path with a compact hook supervisor and one PATH-resolved
capy process that validates the owner and dispatches internally. MCP directly
launches the same Go entry without a shell. Stop generating/requiring the Codex
project wrapper. The earlier launch format is an unpublished iteration of this
same PR: replace it outright, with no backward-compatible parser or automatic
conversion. The `.codex/capy-owner` ID itself remains stable.

Hooks are separate processes. State coordination must work across OS processes,
not just goroutines. Existing child observations in `observations.go` already
provide a permanent lock, bounded read/atomic replacement, expiry and one-use
consumption. Guidance is currently an unprotected per-session JSON map.

The user selected root advisory fallback when availability is unknown. Do not
implement server heartbeats, enumerate global sentinels, or make root HTTP
denials conditional on process liveness. Main observations are written by the
existing PostToolUse hook; availability needs no server-side producer.
The launch foundation factors/reuses serve initialization and existing lifecycle
cleanup; it does not add server-side availability state or change storage rules.

Use the Go module's existing dependencies and runtime facilities. Do not add a
dependency or config option merely to implement these helpers. If a new API
becomes necessary, follow `kk:dependency-handling` before adopting it and update
the design. The relevant Go deadline caveat and pinned references are in
[design §5.3](design-v2.md#53-reader-lifecycle).

V1 documents and verification are historical. New implementation evidence goes
to `verification-v2.md`; review records should carry `-v2` in their names. Those
files are created during implementation, not treated as pre-existing proof.

## 2. Task 1 — direct launch with an explicit PATH prerequisite

### Files and seams

- `cmd/capy/codex_launch.go`: final four-argument protocol, metadata-only probe,
  owner validation and direct hook/serve dispatch; remove the wrapper subprocess.
- `cmd/capy/serve.go` and existing knowledge-target helpers: one reusable serve
  entry accepting the selected owner/context; preserve key/config/lifecycle code.
- `internal/server/server.go` and its lifecycle integration: propagate cancellation
  before background-work joins on direct MCP shutdown, using the existing guard
  rather than adding a second signal/parent monitor.
- `internal/platform/setup_codex_launch.go`, `codex_owner.go` and
  `setup_codex_mcp.go`: one final hook-supervisor/direct-MCP grammar and strict
  recognition. Do not retain or recognize the old bootstrap as a supported form.
- `cmd/capy/setup.go` and `internal/platform/setup.go`: prerequisite validation,
  explicit `--binary` semantics and removal of Codex wrapper generation; retain
  Claude-only and DB-repo setup behavior.
- `internal/platform/doctor_codex.go`, generated-artifact guards, committed
  `.codex/hooks.json`, `.codex/config.toml`, `.claude/scripts/capy.sh` and AGENTS:
  synchronize the final topology; remove committed `.codex/scripts/capy.sh`.

### Execution and verification

1. Replace the launch grammar and recognizers together. Hook shell arguments
   and direct MCP argv carry the final protocol identifier, owner ID, mode and
   event as data; keep all-ancestor inspection and adjacent-marker validation.
   Ignore opaque `hooks.state` only as already fixed in the baseline. → verify:
   `TestCodexFinalLaunchBindings` covers final hook/direct-MCP forms, alternate
   aliases, equivalent JSON/TOML serialization, invalid identifier/ID/mode/event,
   missing markers, copied IDs and overlaps. Obsolete commands return an
   actionable unsupported-format error, never an automatic conversion or a
   fallback to another owner.
2. Dispatch hooks in process through the existing adapter/router. Extract a
   common serve entry used by both `serve` and validated `codex-launch`, with
   explicit owner/context and no second command/process. → verify:
   `TestCodexDirectDispatch` proves hook output/identity/cwd/policy behavior and
   real MCP initialize/list/harmless-call behavior from root and subdirectories,
   including external configured storage. A sentinel project wrapper is never
   invoked; absent/unreadable/symlink wrapper paths cannot become launch gates.
   Hook probes use no DB keys and create no DB.
3. Add the exact side-effect-free protocol check and bounded PATH prerequisite
   from design §4.3. Resolve once before writes in CLI setup including default-
   both mode; share that validated result with the setup writer, while direct
   `SetupCodex` calls also validate. Use an explicit test seam, not a global
   cached result. → verify: `TestCodexSetupPathPrerequisite` covers missing,
   nonexecutable, incompatible, timed-out, oversized-output and wrong-response
   candidates; no generated file, permission or owner-marker change occurs on
   failure. Match/mismatch cases for `--binary` are tested. Claude-only/DB-repo
   modes do not acquire the Codex prerequisite.
4. Generate the compact hook supervisor with PATH-only external execution and
   direct MCP argv. Preserve stdin, failure codes, stderr/protocol separation
   and cancellation; reuse the server's existing lifecycle guard and executor
   cleanup rather than adding another guard. → verify:
   `TestCodexLaunchFailureAndCancellation` covers missing/older/crashed binaries,
   owner failure, exit-0 structured denial, byte-identical input and failed serve
   status. Signal the outer hook and the direct MCP process, and close MCP stdin:
   no Go/server/executor process should remain beyond the existing bounded
   cleanup contract. Do not substitute a wrapper-only sentinel for real server
   lifecycle evidence.
5. Stop writing/using the Codex wrapper and remove Codex-only branches from the
   shared wrapper generator. Update setup output, doctor and artifact inventory;
   regenerate both final Codex registrations and the remaining Claude wrapper.
   → verify: generated whole-file/merge/completeness guards match the new output
   set without a Codex-wrapper exemption. Fresh/final-format repeat setup retains
   owner ID, external handlers and trust metadata. Doctor validates final bindings
   without requiring a wrapper; leftover wrapper-only files are not a capy
   registration. Existing Claude wrapper results remain unchanged.
6. Rebase existing location, ownership and failure fixtures onto the final
   topology. Remove obsolete wrapper-execution expectations rather than adding
   v1 compatibility fixtures. → verify: `TestCodexLaunchLocations`,
   `TestCodexMCPLaunchLocations`, copied-ID/missing-marker and overlap fixtures
   still pass. Keep separate tests proving that generated event commands are
   shorter in structure (no fallback search or wrapper dispatch), and that
   PATH candidates used by setup and runtime agree in the fixture environment.

The core changes are launcher dispatch, binding grammar and prerequisite
validation; doctor/catalog/artifact updates are consequences of those changes.
Do not expand this slice into a new installer, PATH editor or migration service.
Native Codex discovery and re-trust of changed definitions are verified in the
final task, not inferred from configuration-only tests.

## 3. Task 2 — bounded Codex input through the installed CLI

### Files and seams

- Add `internal/hook/input.go` and input-focused tests. Keep transport admission
  separate from JSON payload validation and policy decisions.
- Update `internal/hook/hook.go:Run` to select the guarded reader only for Codex,
  after platform/event validation and before `handleEvent`.
- Update the shared CLI hook error mapping used by `cmd/capy/hook.go` and direct
  `codex-launch` dispatch to map typed Codex PreToolUse input failures to
  `exitError{code: 2}` and suppress usage text. Other errors/platforms retain
  their contracts.
- Extend CLI/generated-launch tests in `cmd/capy/hook_codex_test.go` and
  `cmd/capy/codex_launch_test.go`, or add `hook_input_test.go` beside them.

### Execution and verification

1. Establish one owned stdin read path with bounded chunks and injected options
   for tests. Prove the actual inherited pipe is pollable, or create a suitable
   owned wrapper around a duplicated nonblocking descriptor. Reject an
   unsupported pipe/deadline setup before an unbounded read. Preserve regular
   redirected-file input with bounded reads and the documented filesystem-I/O
   limitation. → verify: `TestCodexHookInputDescriptors` covers anonymous pipe,
   closed pipe, regular file and unsupported descriptor/guard setup; a subprocess
   with an open writer exits on its own, without a test harness kill.
2. Apply the exact idle/absolute/byte budgets from design §5.1, with a short
   SessionEnd profile. Reset idle only on nonzero reads; require EOF and reject
   the first excess byte rather than parsing a prefix. → verify:
   `TestCodexHookInputBudgets` covers empty/partial idle, empty EOF, valid JSON
   held open, a trickle that avoids idle but exceeds the total deadline, repeated
   zero-length reads in the unit seam, exact cap, cap-plus-one, and multibyte/raw
   escaped input counted by serialized byte size.
3. Dispatch only fully admitted bytes. Reader errors must not call an event
   handler, select/load policies, or create guidance/observation/DB files.
   → verify: `TestCodexHookInputNoSideEffects` uses temporary owners and sentinels;
   test a complete invalid payload separately to retain the v1 parse-denial path.
4. Map typed transport failures at both CLI entry paths and retain the compact
   hook supervisor's failure contract. Do not emit pre-tool JSON for another event. → verify:
   `TestCodexHookInputExitCodes` drives direct and setup-generated commands with
   unset keys: PreToolUse exit 2 + nonempty stderr + empty stdout; other events
   diagnostic nonzero + empty stdout; complete malformed PreToolUse exit 0 with
   denial JSON; ordinary valid payload unchanged. Normal MCP stdio must not
   accidentally use the hook reader.
5. Validate timeout ordering using the actual generated path and production
   defaults. Use test-sized limits only for fine-grained reader tests; at least
   one CLI idle test and one trickle test must exercise production timing.
   → verify: no intentional EOF delay; stalled ordinary reads finish within
   budget plus a generous scheduling margin, while the parent pipe remains open;
   SessionEnd's shorter profile is tested separately. All spawned processes and
   descriptors are reaped/closed. Record Linux results and macOS coverage/limits.

Do not add a timeout override to generated hooks to conceal a failing reader.
Use the final launch grammar established in Task 1; do not add another launch
layer or reintroduce project wrappers. Owner IDs remain unchanged.
Host timeout/kill can still precede the reader when a user sets a shorter host
deadline or startup/preflight stalls; document rather than claim that case away.

## 4. Task 3 — atomic once-per-session guidance

### Files and seams

- Update `internal/hook/guidance.go`; retain `sessionIDComponent` and the public
  test-helper names to avoid unrelated identity/API changes.
- Extend `internal/hook/guidance_test.go` and relevant existing hook tests.
- Add a generated-CLI concurrency fixture in `cmd/capy/` for the Codex Bash
  advisory. Tests that assert exactly one JSON state file must now assert the
  specified legacy/new layout; do not weaken their traversal/isolation checks.

### Execution and verification

1. Implement a bounded, non-symlink legacy JSON lookup and a fixed guidance-type
   mapping. Recognize existing known-key membership read-only, with no eager
   conversion. → verify: `TestGuidanceLegacyState` covers read/bash/grep keys,
   present false values, unrelated keys, empty/malformed/trailing JSON, oversized
   state, symlink/nonregular inputs and no mutation of legacy bytes.
2. Serialize the advisory, then exclusively create the zero-byte final marker
   named `guidance-<component>-<type>.once`. Suppress only on an existing valid
   marker or legacy claim. → verify: `TestGuidanceAtomicClaim` has one winner
   among concurrent claims; invalid types cannot create arbitrary paths; marker
   mode/type/content and all existing session-component cases are checked.
3. Preserve advisory fallback for unavailable/invalid state; never return a
   tool denial because guidance persistence failed. Close descriptors on every
   path. → verify: `TestGuidanceStateFailureFallback` uses controlled readers or
   unprivileged fixtures, confirms no outside writes through symlinks, and checks
   bounded diagnostics without command/payload content. Missing identity creates
   no `.capy` state.
4. Update `ResetGuidanceFile` to remove exactly the old file and three known
   markers for a quiescent session. Keep other sessions and all observation
   files/locks untouched. → verify: `TestGuidanceResetIsolation` includes
   overlapping-looking IDs, malicious IDs, an empty-ID sentinel and unrelated
   files; the next claim emits once again.
5. Prove coordination through real processes, not only a Go mutex/race test.
   → verify: `TestCodexGuidanceConcurrentCLI` starts 32 synchronized direct or
   generated hook invocations for the same session and asserts exactly one
   advisory; a mixed type/session fixture asserts one per distinct pair and no
   lost suppression state. Existing Claude guidance responses remain unchanged.

No production SessionEnd guidance cleanup or periodic stale-marker sweep is
introduced. A killed winner can leave a valid empty claim; subsequent calls
suppress that optional advisory. Include that outcome as a test and documented
at-most-once-claim limitation, rather than implementing an acknowledgment system.

## 5. Task 4 — one main-agent HTTP redirect per observed success

### Files and seams

- Update `internal/hook/observations.go` with a closed internal scope choice for
  child versus Codex main state. Default child behavior, format and paths must
  remain unchanged. Keep lock/expiry/consume/write logic shared.
- Update `internal/hook/posttooluse.go`, `pretooluse.go` and `sessionend.go`.
  Thread explicit adapter/platform identity from `hook.go` where needed; do not
  infer the platform from a session prefix or process environment.
- Update `internal/hook/routing.go` and
  `internal/platform/routing.go:GenerateRoutingInstructions()` together with
  committed `.capy/AGENTS.md`. Preserve Claude wording/behavior where it differs.
- Extend `internal/hook/observations_test.go`, `observation_routing_test.go`,
  `codex_lifecycle_test.go` and the installed CLI lifecycle fixture in
  `cmd/capy/hook_codex_lifecycle_test.go`. Mechanical signature/test updates do
  not authorize changing unrelated tests or storage behavior.

### Execution and verification

1. Add the separate main file/lock scope and its strict schema constraints:
   stable session, empty `agent`, execute-only tool map, 64 KiB/128 entries,
   60-second TTL and 20 ms lock budget. Reuse the storage algorithm and retain
   no-create-on-fallback/absent-cleanup behavior. → verify:
   `TestCodexMainObservationStore` covers valid state, malformed/oversized/trailing
   input, future timestamps, duplicates, nonempty main agent IDs, unsupported
   main tools, cap eviction and permanent-lock inode stability; all existing
   child fixtures still pass unchanged in meaning.
2. Record a success only for Codex, no child ID, stable session and accepted
   `capy_execute` PostToolUse. All failure/cancellation/serialized-result checks
   remain those of `successfulPostToolUse`. → verify:
   `TestCodexMainObservationPayloads` covers root, type-only, actual child,
   missing identity, different transcript UUIDs, arbitrary server aliases,
   successful/failed/ambiguous results, fetch-only success, other capy tools and
   Claude root payloads. Ineligible calls create no main store and do not renew
   existing timestamps.
3. In `routeBash`, keep command security and safe-download detection ahead of
   routing evidence. Apply the new main scope only to Codex main HTTP candidates;
   return no permission grant on advisory fallback. → verify:
   `TestCodexMainRoutingEvidence` covers no state → advisory, execute success →
   one redirect, next attempt → advisory, failed retry → advisory, a later
   success → one new redirect, and expiry exactly at 60 seconds. Fetch/batch
   success alone must not authorize arbitrary Bash HTTP.
4. Keep security and ownership failures independent of availability. → verify:
   `TestCodexMainRoutingSecurityPrecedence` repeats deny, matched ask, later deny
   in compound input and scanner failures under absent/fresh/expired/corrupt/
   locked evidence. Exact pattern/source remains visible and evidence is not
   consumed. Existing owner-preflight/overlap failures retain exit 2 before state
   access. Existing Claude main HTTP denials and child routing remain unchanged.
5. Prove isolation and atomic consumption. → verify:
   `TestCodexMainChildIsolation` uses one owner with root/child and sibling sessions,
   another owner and a Claude raw-ID collision; none can consume the others'
   evidence. `TestCodexMainConcurrentRedirect` submits simultaneous HTTP candidates
   against one success and gets exactly one redirect with healthy state. Inject
   consumption-write failure/lock timeout and assert advisory fallback rather
   than an uncommitted denial.
6. Extend root Codex SessionEnd cleanup for both scopes, independently and
   without nested locks. Ignore child-labelled Codex SessionEnd for root cleanup;
   retain Claude cleanup. → verify: `TestCodexMainSessionEnd` checks sibling
   retention, both permanent inodes, absent-state no-op, one scope failing while
   the other cleans, missing identity and a child-labelled event. No keys/DBs.
7. Synchronize runtime/generated guidance and drive the full installed path.
   → verify: `TestCodexMainObservationLifecycle` installs setup in a temporary
   Git project, invokes its generated commands from a subdirectory with synthetic
   payloads, and checks the sequence above; existing child and MCP location probes
   pass. Startup/child context remains at most 4,096 UTF-8 bytes; generated-artifact
   equality/idempotency/completeness guards pass with external handlers retained.

Synthetic installed-command coverage does not establish actual host delivery.
The live main-session sequence belongs to the final verification task below.

## 6. Task 5 — release verification and documentation

1. Run `kk:test` over the resulting implementation using FTS5 and synthetic
   `CAPY_DB_KEY`/`CAPY_VAULT_KEY`, with an empty temporary capy config home.
   Hook subprocesses separately clear both keys. → verify: full repository
   suite passes; focused adapter/hook/platform/CLI race checks pass; vet and
   generated-artifact guards pass. Record exact commands, toolchain and genuine
   environment limitations in `verification-v2.md`. Quality benchmarks apply
   only if implementation expands into search/indexing/chunking/executor code.
2. In a trusted disposable Git installation, record the exact Codex version and
   run from an ordinary subdirectory. Use normal project/exact-hook review, an
   isolated capy config, synthetic project policies/keys and no production vault
   key. Retaining the existing host authentication/trust home is allowed under
   [the v1 acceptance adjustment](implementation.md#52-live-acceptance-and-release-checks),
   with global-policy inheritance disclosed. → verify: a main harmless dead-code
   HTTP-shaped probe first executes with advisory; an actual main `capy_execute`
   success enables one routing denial; repeating the harmless native probe then
   succeeds. Never perform an unnecessary HTTP request or retry a policy-denied
   marker. Require real tool results, not only the model's report.
3. Validate enforcement under the new fallback policy. → verify: harmless
   synthetic deny and ask markers still never execute and name their exact
   fixture rule/source, both before and after main evidence exists; child
   evidence stays isolated; MCP remains usable from the subdirectory. Check
   compact startup payload/no spill and unchanged unrelated-hook behavior.
   Also confirm that the actual host inherits the intended PATH and that changed
   generated definitions pass normal hook review/re-trust. Setup success in a
   different terminal does not prove this host environment is configured.
4. Validate transport and cleanup limitations separately. → verify: stalled
   generated input produces exit 2 in a subprocess; EOF framing, size bounds
   and SessionEnd short deadline are recorded as controlled probes. Host
   delivery uses complete input, so do not manufacture a claim of a naturally
   stalled live host. Record any untested platform, kernel-I/O limitation and
   unavailable live cleanup path explicitly. Stop disposable sessions normally.
5. Run `kk:document` for README PATH/setup/routing/repair/limits,
   `docs/architecture.md`, AGENTS artifact/state invariants and setup/hook help.
   → verify: docs distinguish routing
   fallback from policy denial, disclose the first/unobserved-call behavior,
   document older shared-guidance state, the final launch prerequisite, normal
   re-trust and unsupported interim PR formats, and make no output-budget or
   continuity claims. Update both sides of any generated
   artifact change. Do not alter v1's completed task statuses or evidence.
6. Run isolated `kk:review-code` with Go input and isolated `kk:review-spec`
   explicitly against `design-v2.md`, `implementation-v2.md` and `tasks-v2.md`,
   using v1 as inherited context. → verify: findings resolved or given durable
   actionable dispositions in v2-named records. Reviewers must not accidentally
   treat v1's `done` header as proof v2 exists.
7. Check the three documents, relative links, task dependencies and final
   generated fixtures. → verify: `tasks-v2.md` alone changes to done after its
   implementation evidence is complete. No release publishing, issue mutation
   or commit is implied by this design task.

## 7. Test matrix and implementation cautions

| Concern | Required negative/control cases |
|---|---|
| Launch | one final grammar; no project wrapper; PATH missing/incompatible/changed; bounded side-effect-free probe; setup failure before writes; `--binary` match/mismatch; same owner/cwd/storage |
| Launch lifecycle | pre-tool exit 2; structured denial exit 0; MCP stdout/status; outer hook signals; direct MCP EOF/signals/initialization failure; no orphaned server/executor |
| Reader bounds | no bytes/open pipe; partial/open; valid JSON/open; slow trickle; cap/cap+one; empty EOF; read failure; unsupported guard; complete invalid JSON |
| Hook effects | invalid input leaves policy/state/DB untouched; PreToolUse exact exit 2 versus structured-denial exit 0; other events no fabricated pre-tool output; Claude input unchanged |
| Guidance | 32-process single winner; multiple types/sessions; valid legacy claims; killed winner; false-value legacy membership; corrupt/oversized/symlink state; quiescent reset |
| Main evidence | successful execute only; unknown/missing/failure states; TTL boundary; write failure; lock contention; two simultaneous candidates; no accumulated credits |
| Isolation | main/child; root/type-only; sibling session; separate owner/worktree; Codex/Claude raw-ID collision; no cross-file migration |
| Enforcement | deny/ask/scanner precedence; no evidence consumption on policy block/safe download; owner overlap and broken bindings fail before handler/state/DB |
| Cleanup | root-only Codex end; no key/DB access; independent scope failures; stable lock files; absent state creates nothing |
| Installed operation | fresh/final-format setup twice; stable marker; trust-state preservation; regenerated commands and normal re-trust; root/subdirectory MCP; compact guidance; external handlers; full suite/race/drift; live main round trip |

Avoid timing-only race tests: synchronize worker start and observe persisted
claims/decisions. Inject clocks for TTL/budget boundary unit tests, and use a
small number of real-process deadline tests to validate OS behavior. Tests must
not contact public HTTP endpoints or depend on installed production credentials.
Do not replace an exact behavioral assertion with a broad substring merely to
make a platform fixture pass.

## 8. Follow-ups outside v2

The v1 deferred toolbox enhancement, #132 output policy, non-Git discovery and
authoritative host capability feed remain separate work. The user's selection
removes the need for a readiness-daemon design in this amendment. A later request
to block unknown callers until confirmed unavailability would require a new
reliable signal and another policy decision; do not silently add it here.
