# Implementation: Codex hooks v2

> Status: proposed — implementation pending
> Created: 2026-10-12
> Design: [design-v2.md](design-v2.md) · Tasks: [tasks-v2.md](tasks-v2.md)
> Baseline: capy `ac60b81` (same source tree as comparison commit `a58f90d`); follow the `-v2` documents, not the completed v1 task list

## 1. Orientation and boundaries

The generated hook path is host → inline bootstrap → `capy codex-launch` →
owner's `.codex/scripts/capy.sh` → `capy hook --platform codex <event>`.
`internal/platform/codex_owner.go` validates every ancestor's owner/binding
before any project wrapper runs. `cmd/capy/hook.go` selects the adapter;
`internal/hook/hook.go` reads stdin, resolves project/cwd, and dispatches.
The Codex adapter supplies stable namespaced identity and supported responses.

Hooks are separate processes. State coordination must work across OS processes,
not just goroutines. Existing child observations in `observations.go` already
provide a permanent lock, bounded read/atomic replacement, expiry and one-use
consumption. Guidance is currently an unprotected per-session JSON map.

The user selected root advisory fallback when availability is unknown. Do not
implement server heartbeats, enumerate global sentinels, or make root HTTP
denials conditional on process liveness. Main observations are written by the
existing PostToolUse hook, with no MCP-server code changes.

Use the Go module's existing dependencies and runtime facilities. Do not add a
dependency or config option merely to implement these helpers. If a new API
becomes necessary, follow `kk:dependency-handling` before adopting it and update
the design. The relevant Go deadline caveat and pinned references are in
[design §4.3](design-v2.md#43-reader-lifecycle).

V1 documents and verification are historical. New implementation evidence goes
to `verification-v2.md`; review records should carry `-v2` in their names. Those
files are created during implementation, not treated as pre-existing proof.

## 2. Task 1 — bounded Codex input through the installed CLI

### Files and seams

- Add `internal/hook/input.go` and input-focused tests. Keep transport admission
  separate from JSON payload validation and policy decisions.
- Update `internal/hook/hook.go:Run` to select the guarded reader only for Codex,
  after platform/event validation and before `handleEvent`.
- Update `cmd/capy/hook.go` to map typed Codex PreToolUse input failures to
  `exitError{code: 2}` and suppress usage text for those failures. Reuse the
  existing root exit handling; other errors/platforms retain their contracts.
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
2. Apply the exact idle/absolute/byte budgets from design §4.1, with a short
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
4. Map typed transport failures at the CLI boundary and retain wrapper/launcher
   supervision. Do not emit pre-tool JSON for another event. → verify:
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
V1 command grammar, owner IDs and wrapper generation should remain unchanged.
Host timeout/kill can still precede the reader when a user sets a shorter host
deadline or startup/preflight stalls; document rather than claim that case away.

## 3. Task 2 — atomic once-per-session guidance

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

## 4. Task 3 — one main-agent HTTP redirect per observed success

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

## 5. Task 4 — release verification and documentation

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
4. Validate transport and cleanup limitations separately. → verify: stalled
   generated input produces exit 2 in a subprocess; EOF framing, size bounds
   and SessionEnd short deadline are recorded as controlled probes. Host
   delivery uses complete input, so do not manufacture a claim of a naturally
   stalled live host. Record any untested platform, kernel-I/O limitation and
   unavailable live cleanup path explicitly. Stop disposable sessions normally.
5. Run `kk:document` for README routing/repair/limits, `docs/architecture.md`,
   AGENTS guidance-marker/main-observation invariants and hook CLI help where
   user-facing constraints need explanation. → verify: docs distinguish routing
   fallback from policy denial, disclose the first/unobserved-call behavior,
   document legacy marker compatibility and mixed-version limitations, and make
   no output-budget or continuity claims. Update both sides of any generated
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

## 6. Test matrix and implementation cautions

| Concern | Required negative/control cases |
|---|---|
| Reader bounds | no bytes/open pipe; partial/open; valid JSON/open; slow trickle; cap/cap+one; empty EOF; read failure; unsupported guard; complete invalid JSON |
| Hook effects | invalid input leaves policy/state/DB untouched; PreToolUse exact exit 2 versus structured-denial exit 0; other events no fabricated pre-tool output; Claude input unchanged |
| Guidance | 32-process single winner; multiple types/sessions; valid legacy claims; killed winner; false-value legacy membership; corrupt/oversized/symlink state; quiescent reset |
| Main evidence | successful execute only; unknown/missing/failure states; TTL boundary; write failure; lock contention; two simultaneous candidates; no accumulated credits |
| Isolation | main/child; root/type-only; sibling session; separate owner/worktree; Codex/Claude raw-ID collision; no cross-file migration |
| Enforcement | deny/ask/scanner precedence; no evidence consumption on policy block/safe download; owner overlap and broken bindings fail before wrapper/state |
| Cleanup | root-only Codex end; no key/DB access; independent scope failures; stable lock files; absent state creates nothing |
| Installed operation | setup twice; generated commands; root/subdirectory MCP; compact guidance; external handlers; full suite/race/drift; live main round trip |

Avoid timing-only race tests: synchronize worker start and observe persisted
claims/decisions. Inject clocks for TTL/budget boundary unit tests, and use a
small number of real-process deadline tests to validate OS behavior. Tests must
not contact public HTTP endpoints or depend on installed production credentials.
Do not replace an exact behavioral assertion with a broad substring merely to
make a platform fixture pass.

## 7. Follow-ups outside v2

The v1 deferred toolbox enhancement, #132 output policy, non-Git discovery and
authoritative host capability feed remain separate work. The user's selection
removes the need for a readiness-daemon design in this amendment. A later request
to block unknown callers until confirmed unavailability would require a new
reliable signal and another policy decision; do not silently add it here.
