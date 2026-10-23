# Tasks: native macOS captured execution

> Design: [design.md](design.md)
> Contract: [contracts.md](contracts.md)
> Implementation: [implementation.md](implementation.md)
> Evidence: [native process verification](process-proof.md) · [source verification](verification.md)
> Status: in progress — Tasks 1–5 done; observational Darwin contract accepted; Tasks 6–13 pending
> Created: 2026-10-22
> Not Doing: older macOS majors, Intel support, certified Darwin group retirement, full Darwin detached-descendant cleanup, best-effort tracker, VMs, persistent/privileged services, restricted entitlements, self-hosted runner provisioning, other OS ports, PTY/stdin, persistent jobs, cross-owner cancellation, knowledge/vault migration, run rekey, automatic discard/downgrade, trust/security changes

Tasks 1–2 implement the bounded process-observation policy; Tasks 3–4 close the lease proof gate. They are required
functionality, not exclusions. Use [bootstrap.md](bootstrap.md) only where Codex
enforcement requires it. Task 1 implementation and native proof are recorded below; its temporary
verification exceptions are removed after use. Contract fields remain fixed before
their implementation.

## Task 1: Select hosted support and prove bounded child waiting

- **Status:** done
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 3
- **Strategy:** Risk-First
- **Docs:** [bounded waiting](implementation.md#task-1-select-hosted-support-and-prove-bounded-child-waiting)

### Subtasks

- [x] 1.1 Select the floor/deployment target from verified APIs/toolchain and stable hosted arm64 runner availability; record explicit labels and OS/kernel/SDK versions in `process-proof.md` → verify: every declared major, including the floor, is exercisable in CI without self-hosted provisioning.
- [x] 1.2 Correct the `wait_darwin.go` ESRCH/NOTE_EXIT assumption and prove bounded terminal observation with one reaper → verify: stopped, exiting and cancelled direct-child fixtures.
- [x] 1.3 Grade each matrix entry source-aligned or contract-backed → verify: missing matching sources are disclosed, while critical unsupported ordering remains unresolved.

### Execution context

- **2026-10-22 — baseline and scope:** Started from clean `master` at `13204a9a68a3def965950e765ca910c615232913`; the original design baseline is `44ceb813ce2d4f129d058f2534faa2a57b07d8bd`. Task 1 covers the Darwin waiter, dedicated fixtures, a pre-existing macOS path assertion fix, and `process-proof.md`. Group retirement, storage/protocol work, public activation and hosted release gates remain later tasks.
- **2026-10-22 — explicitly accepted decision:** The user accepted **macOS 26-only on Apple Silicon**, saying “let's not overcomplicate this and block the feature on old compatibility contracts that we can't prove.” Floor/deployment target: **26.0**; hosted label: **`macos-26`**. This supersedes the implementer's earlier macOS 15 proposal and its unverified-compatibility blocker. Proposed hosted SDK choices are future CI configuration, not a separate Task 1 gate; actual test SDKs are recorded.
- **2026-10-22 — implementation and evidence:** Kqueue ESRCH/NOTE_EXIT are wakeups only. The public libc `waitid(P_PID, …, WEXITED|WNOWAIT|WNOHANG)` binding validates the requested PID, SIGCHLD and terminal status, with bounded cancellation and one terminal reaper. The pinned x/sys has no Darwin wrapper; cgo uses SDK types without a new module. Evidence is **contract-backed**, with unmatched XNU source disclosed. The optional shared mutex refactor was removed; `executor.go` now differs only in comments, leaving Linux runtime behavior unchanged. Its inherited error-path reap is not claimed to be bounded.
- **2026-10-22 — verification fixes:** Independent review caught consuming stop synchronization; the fixture now uses `WSTOPPED|WNOWAIT|WNOHANG` and checks preservation. The initial race run (14.812s) and untouched baseline (13.982s) both failed the Rust fixture's `/var` versus `/private/var` equality. Recording physical paths fixes the fixture without changing production cwd or weakening its assertions. Initial polling overhead was reduced with bounded kqueue wakeups; all attempts remain in `bench-results/macos-wait-task1-validation/`.
- **2026-10-22 — native results:** On macOS 26.6.2 (25G83), Darwin 25.6.0 / xnu-12377.161.14~5, arm64, Go 1.27.2, SDK 27.0 (26A425), final-scope tests with deployment target 26.0 passed: 20 waiter repetitions **5.604s**, full executor race/integration **15.714s**, and vet. Earlier target-15.0 runs were also on this macOS 26 host, never proof of macOS 15 compatibility. Matching-target six-sample performance comparison is recorded in `process-proof.md`: serial medians −0.68%, −0.31%, +0.30%; parallel +2.33%, −2.24%, +1.74%, with about three extra allocations per run and no significance claim.
- **2026-10-22 — quality comparison and release follow-up:** Baseline and candidate `make bench-quality` both exit 2 at the same four server capture threshold cases because `internal/execution.Supported` still rejects Darwin. Store/vault checks pass, matching dataset hashes and all non-metadata quality results. No assertion or skip changed. Owner: feature implementer; next action: rerun the complete capture benchmark after Task 10 enables the proven backend; verification condition: all four real capture cases pass. This is a later public-activation requirement, not a Task 1 waiter defect.
- **2026-10-22 — authorization and review:** The user explicitly approved six exact bootstrap test/benchmark exceptions, keeping routine enforcement enabled. The first verification round restored original configuration byte-for-byte and confirmed denial with an installed-hook probe. The same authorized scope was reused for the macOS 26 follow-up, then removed; a second read-only installed-hook probe confirmed restored enforcement and the original config bytes match. Final isolated code review **APPROVED** the narrowed Task 1 scope with no findings. Native results are author-attributed; the reviewer inspected source and performance/quality artifacts. Task 1 is complete; group certification, public activation and full release gates remain later tasks. PAL is unavailable. No new project conventions require indexing because the evidence is recorded here and in `process-proof.md`.

## Task 2: Implement bounded Darwin group observation

- **Status:** done
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** Task 3, Task 4
- **Strategy:** Risk-First
- **Docs:** [group observation](implementation.md#task-2-implement-bounded-darwin-group-observation) · [accepted policy](design.md#darwin-process-lifecycle-and-observational-cleanup)

### Subtasks

- [x] 2.1 Add a dormant `supervised_darwin.go` lifecycle and bounded documented `sysctl` inventory with retained leader identity and natural drainage → verify: ordinary/nonzero/cancelled/compiler phases, live child with closed output and escaped pipe holder; no private libproc production fallback.
- [x] 2.2 Enforce `darwin_process_group_observation_v1` completion and failure conditions → verify: inventory/signalling/drain/reap errors stay incomplete, actual command/capture outcomes remain distinct, and the existing re-entry counterexample never becomes a `retired` claim. Fork-publication and in-flight-join impossibility proofs are explicitly no longer required.
- [x] 2.3 Update `process-proof.md` with native policy evidence, API status and accepted limits; keep public dispatch disabled → verify: successful dormant behavior, bounded failure fixtures and independent review. Shared typed/storage/stream integration remains Tasks 5–9.

### Execution context

- **2026-10-22 — explicitly accepted contract revision (current):** After the recommendation to adopt observational Darwin cleanup, the operator replied “Agree. Let's do that.” This authorizes changing the cleanup guarantee, result semantics, normal reuse and retention policy. The current scope is `darwin_process_group_observation_v1` with state `observed`; Linux retains `retired`. This supersedes the former group-retirement gate and later “decision stays open” notes below, which remain historical. The native counterexample remains valid evidence of the accepted limit. Task 2 still requires implementing and testing the observation policy; lease ownership, real failures/quarantine, capture completeness and Linux behavior remain mandatory.
- **2026-10-22 — revised design review:** Isolated `kk:review-design` returned SOUND with no findings across the seven revised documents and targeted source contracts. PAL was unavailable. Local validation passed 125 relative links/anchors, both JSON examples and `git diff --check`. Proceeding with the dormant observational backend; this review is not native runtime verification. Typed storage/protocol integration and activation remain later tasks.
- **2026-10-22 — backend implementation prepared, verification pending:** Added a private Darwin phase/compile-run lifecycle, bounded documented sysctl inventory and native/fault fixtures. Existing public stream and execution admission remain unchanged. Native test/race/vet and repository-required benchmark comparisons await the exact bootstrap extension recorded in `bootstrap.md`; no claim of backend runtime success is made yet. The prior re-entry fixture results remain separate.
- **2026-10-22 — backend verification supersedes pending entry above:** Final focused native count-five suite passed **19.216s**; full executor FTS5/integration race passed **26.696s**; vet passed. Tests cover ordinary/control/compiler/capture paths, public sysctl partial-buffer/identity behavior, natural drainage and all review-requested wait/reap failures. Six-sample baseline comparisons are recorded in `process-proof.md`; all non-metadata quality results match, with the same four inherited public-capture failures. Owner: feature implementer; rerun those capture cases after Task 10 activation, retaining current assertions. All attempts are preserved under `bench-results/macos-observed-task2-validation/`.
- **2026-10-22 — code review and bootstrap closure:** Initial isolated code review returned COMMENT for three missing failure cases and pending benchmarks, with no source defect. Those fixtures/results were added and the reviewer returned **APPROVE** for the dormant source scope, with no findings. PAL unavailable; execution remains author-attributed. The original config bytes were restored in candidate and baseline, and an installed-hook probe confirmed routine denial. Final evidence-document review is pending; no new runtime or public-activation work is hidden in that closure.
- **2026-10-22 — completed current scope:** Final isolated review approved the dormant backend and all evidence/closure documentation with no findings. Local documentation checks passed 129 relative links/anchors, both JSON examples and `git diff --check`. Task 2 is **done** under the explicitly accepted observational contract. Runtime execution is author-attributed; no PAL or released/hosted certification is claimed. No new knowledge notes are indexed because the decision, evidence and limits are captured in these documents. Task 3 is the next ready task; lease/storage/protocol integration and public activation remain intentionally pending.
- **2026-10-22 — observed baseline and boundary:** Started from clean `feat/macos_captured_execution` at `a93aa9830f04fbf18bb0295bba8f8e1bd64a0a21` (branch identity corrected from the initial `master` attribution after checking `git branch --show-current`). Task 1's non-reaping waiter is present; `supervised_other.go` and `execution.Supported` still reject Darwin. This task must justify group retirement before implementing a successful dormant phase path. Storage, protocol changes, Linux retirement and public activation remain outside this slice.
- **2026-10-22 — investigation in progress:** The selected host remains macOS 26.6.2 / 25G83, Darwin 25.6.0 / xnu-12377.161.14~5, arm64. Inspecting the public group APIs and published XNU ordering against the fork-publication and same-session re-entry obligations in `design.md`. Task 1's expired exceptions authorize only Task 1 verification; none has been reapplied for Task 2. No Task 2 native test has run.
- **2026-10-22 — observed source / unresolved certificate:** [Task 2 evidence](process-proof.md#task-2-investigation-the-candidate-does-not-close-group-membership) records a same-session re-entry interleaving after the final group observation and before leader reap, plus the distinct group-insertion/process-list-publication gap. Pinned Apple source `ac9718fb1af618d5ce8678d0dc6e8a58f252216f` is not matched to this host; no native reproduction or universal impossibility claim is made. No successful certificate was established. Per Task 2.3, dependent supervisor implementation is stopped pending resolution; all three subtasks remain open.
- **2026-10-22 — proposed next action, not an accepted change:** Owner: project operator for the contract choice; feature implementer for follow-through. Preserve certified retirement and identify a justified membership-closure mechanism, or explicitly accept observational group cleanup and revise its public/storage/reuse/retention semantics before implementation. The evidence report describes the counterexample, consequences and verification conditions. Native reproduction is deferred with a concrete fixture outline there; existing runtime/storage/security behavior is unchanged.
- **2026-10-22 — verification and isolated review:** `git diff --check` passed for this documentation-only update. Independent `kk:review-code` review approved the evidence/disposition with no findings, including the added post-reap in-flight-join analysis; it did not approve Task 2 completion or certify host behavior. PAL was unavailable. No tests/builds/benchmarks ran, no routing exceptions were applied, and no findings require indexing because the evidence is recorded here. Task 2 remains in progress at its design gate.
- **2026-10-22 — accepted fixture scope / verification pending:** The operator requested the proposed deterministic native fixture. Added integration-only leader/member re-exec, pipe barriers, bounded I/O/waits and fixture cleanup. It exercises the cancellation variant: successful group SIGKILL, retained terminal session leader, natural output EOF, then a controlled same-session re-entry and live response after leader reap. A graceful early-abort control exercises fixture cleanup. The installed captured test route rejected before admission; exact native command exceptions are proposed in `bootstrap.md`. This request does not change the cleanup guarantee or complete Task 2.
- **2026-10-22 — native fixture results supersede pending verification above:** The operator explicitly approved both exact fixture commands. On macOS 26.6.2 arm64, all 20 ordinary repetitions passed (0.869s), as did the focused race run (3.416s). Each reproduced live re-entry after successful group signalling, terminal leader observation and natural pipe EOF; each confirmed fixture cleanup. The early-abort control also passed. Details, private libproc diagnostic status, raw-log locations and untested boundaries are in [the native fixture record](process-proof.md#deterministic-native-re-entry-fixture). This completes the requested experiment, not the group certificate or Task 2. No production code or contract changed.
- **2026-10-22 — fixture review and bootstrap closure:** Isolated `kk:review-code` approved the five-file experiment with no findings after inspecting the code, waiter/reaper contracts, diagnostic API and both author-supplied logs; no reviewer test execution or PAL review is claimed. `git diff --check` passed. Both temporary native exceptions were removed, original config bytes matched, and an installed-hook probe confirmed restored denial. No new findings/conventions need indexing because the evidence is documented. The requested deterministic fixture is complete; the remaining Task 2 mechanism/contract decision stays open.

## Task 3: Construct acquisition-proven lease references

- **Status:** done
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 1, Task 2
- **Strategy:** Risk-First
- **Docs:** [acquired references](implementation.md#task-3-construct-acquisition-proven-lease-references)

### Subtasks

- [x] 3.1 Encapsulate successful EX owner/SH lifecycle acquisitions in `internal/runstore/lease_refs.go` and the existing acquisition paths → verify: arbitrary/unlocked/wrong-mode files cannot construct valid launch inputs.
- [x] 3.2 Bind borrowing/lifetime and exchange-gate identity to the shared acquisition context → verify: alias/concurrent borrower handling and close-not-unlock behavior.
- [x] 3.3 Record the provenance boundary in `lease-proof.md`; exercise real reset/slot probes while references remain → verify: no new exchange/backend work is hidden in this slice.

### Execution context

- **2026-10-22 — observed baseline and authorized scope:** The user requested Task 3 on Linux x86_64, starting from clean `c833fff220fae4b8a303f61face9bc58632946fb`. Tasks 1–2 are complete. This slice encapsulates acquired owner/lifecycle references, lifetime and the shared exchange gate, and narrows the production launch boundary without changing protocol v1, storage schema, process cleanup or public platform support. Task 4 owns the actual exchange and its two-platform proof.
- **2026-10-22 — implementation approach:** Replace raw owner lease exposure with an opaque close/stat handle; construct launch references only from authenticated shared lifecycle attachment and successful exclusive owner acquisition. Bind immutable store/slot/generation identity and one gate to the lifecycle acquisition, including all owner aliases sharing it. Existing references pin locks through store/owner close; release closes descriptors without unlock/conversion. Native macOS verification remains unperformed on this Linux host and will be recorded explicitly in `lease-proof.md`.
- **2026-10-22 — Linux verification:** The unchanged baseline passed full runstore/execution FTS5 integration race tests. Candidate tests found and corrected a new fixture stderr-read race and missing test import; assertions were preserved. The full candidate suite then passed (runstore 91.833s; execution 34.978s), as did vet. Selected public capture/recovery/reset race checks passed (server 8.896s; CLI 20.490s), followed by the full FTS5 build and `git diff --check`. Commands, captured run IDs, provenance, lifetime/gate behavior and native-evidence limits are in [lease-proof.md](lease-proof.md).
- **2026-10-22 — review and completion:** Isolated `kk:review-code` (gpt-6.1-sol) approved all 14 changed files with no findings, tracing acquisition, borrowers/gate, shutdown, inherited locks, helper validation and unchanged recovery/index consumers. Tests remain author-attributed; PAL code review was unavailable. Task 3 is done for the authorized Linux implementation scope. No findings or new conventions require indexing because the boundary and evidence are documented. Owner: feature implementer; Task 4 must execute the reference/inheritance and exchange fixtures on macOS 26 arm64 and Linux before closing the two-platform lease-proof gate. No bootstrap exception, schema/protocol change or Darwin activation was introduced.

## Task 4: Prove the common exchange and pre-start recovery

- **Status:** done
- **Depends on:** Task 3
- **Size:** M
- **Can run in parallel with:** Task 2
- **Strategy:** Risk-First
- **Docs:** [exchange plan](implementation.md#task-4-prove-the-common-exchange-and-pre-start-recovery) · [wire contract](contracts.md#private-protocol-v2)

### Subtasks

- [x] 4.1 Implement one portable `lease_exchange.go` sequence for Linux and Darwin, with additional Linux fdinfo verification → verify: real helper exec and causal two-way observations for both roles.
- [x] 4.2 Cover independent same-inode/shared holders, swapped roles, reflected/replayed frames and invalid offsets → verify: identity alone accepts same-description unlocked aliases, but Task 3 excludes them from production.
- [x] 4.3 Serialize cancellation with the first start-frame write; before it, kill/reap the direct helper within the separate observation budget and release the gate only after confirmation → verify: SIGSTOP timeout recovery and a subsequent run without restart.
- [x] 4.4 Acquire the gate before spawning under the same startup budget; track its one-time release → verify: gate-wait timeout spawns no helper and cancellation after ready cannot release another exchange's token.
- [x] 4.5 Cover partial authorization, late actors and failed/unbounded reap → verify: retained gate/leases/reservations when necessary, no post-authorization force-kill shortcut, and continued reset/slot exclusion.
- [x] 4.6 Complete `lease-proof.md` with fixtures on both OS families and unchanged permanent bytes/lock modes.

### Execution context

- **2026-10-22 — authorized implementation and baseline:** The user requested Task 4 after committing Task 3 as `e32af29265d519373fadc7f6ac1c5ac7ab9f176a`; work starts from that clean Linux x86_64 checkout. Task 4 implements and privately exercises the common protocol-v2 startup exchange and cancellation/recovery state machine. Ordinary Linux execution stays on protocol v1 until Task 8; typed storage/terminal outcomes and Darwin backend activation remain later tasks. Existing Go implement/test/review guidance remains applicable with no new profile.
- **2026-10-22 — acceptance limit:** This host cannot run native macOS fixtures. Implement and verify the portable mechanism and Linux controls here; keep Task 4 open until the reference/inheritance and exchange fixtures also pass on macOS 26 arm64. Owner: feature implementer; native verification conditions and commands will be recorded in `lease-proof.md`.
- **2026-10-22 — Linux implementation and evidence:** Added the private common offset exchange, strict v2 startup/message validation, post-exchange binding callback and owner-retained startup/reaping state machine. Real helpers cover causal identity, impostors/replay, SIGSTOP recovery, ready-token ownership, partial writes, delayed/unverified reaping and a late actor after signal failure. The first run exposed filesystem rejection of large valid-wire offsets; generated markers now stay within the positive 31-bit subset without changing wire limits or assertions. The expanded focused race suite and full affected-package race/integration, vet and build passed. A final-source execution race/integration rerun passed 60.957s, followed by vet and the full FTS5 build. See [lease-proof.md](lease-proof.md) for commands, run IDs and native acceptance limits.
- **2026-10-22 — isolated review and outstanding acceptance:** `kk:review-code` (gpt-6.1-sol) found no code defects after inspecting all ten changed files and the relevant unchanged consumers. It supports the private implementation but returned overall COMMENT because native macOS acceptance is missing; final Linux results are author-attributed, and PAL was unavailable. Code and Linux controls for 4.1–4.5 are implemented; checkboxes and task status remain open for the two-platform gate. Owner: feature implementer; run the documented reference/inheritance and exchange fixtures, full affected-package suite and vet on macOS 26 arm64 with deployment target 26.0, record actual OS/kernel/SDK/toolchain/results, and resolve any failures before completing Task 4. No code findings are deferred. Ordinary v1 execution and schema 8 remain unchanged; production v2 wiring is still Task 8.
- **2026-10-22 — CI verification route:** The user asked to run these native tests in CI. Added a `macos-26` arm64 proof job to the existing PR workflow, with deployment target 26.0, synthetic keys, full affected-package integration/race tests, vet/build, named-fixture execution checks and uploaded environment/test evidence. This pulls the narrow native proof job forward from Task 12 without claiming its broader integration targets, diagnostics or release-artifact gates. Hosted execution/results are pending; successful native CI can satisfy Task 4's native-host requirement.
- **2026-10-22 — hosted evidence supersedes native-pending entries:** [CI run 39836412099](https://github.com/serpro69/capy/actions/runs/39836412099/job/120025807793) passed on macOS 26.6.2 / 25G83, Darwin 25.6.0 arm64, Xcode 26.6 / 17F113, SDK 26.5 / 25F70 and Go 1.27.2, deployment target 26.0. Runstore passed 113.297s, execution 28.286s, all 53 lease test/subtest events passed, the required 18-case guard passed, and vet/build passed. Source `0e430eed1ebfb93122e5f208b64e38403d220c90` has identical Go code to the first native attempt at `a246ba0dc692d7e9c36329874c8db9a120d13d23`. The first run's two retention failures did not recur; the conditional baseline comparison was therefore skipped, not passed. No storage bug is claimed fixed or attributed. Temporary baseline workflow code was removed, and investigation is explicitly tracked in Task 12.6.
- **2026-10-22 — completion:** Isolated `kk:review-code` approved closure with no findings after independently reading both native environment/JSON artifacts and the closure diff. Linux and native macOS evidence satisfy Task 4's specific lease-proof scope; Task 4 is done. Tests were executed by the implementation session/CI, not rerun by the reviewer; PAL remains unavailable. The permanent native job and first-failure evidence remain. Task 5 is next; ordinary v1 execution is unchanged, and public Darwin activation and broader release reliability remain later tasks.

## Task 5: Enforce scoped proof on fresh stores

- **Status:** done
- **Depends on:** Task 2, Task 4
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [fresh stores](implementation.md#task-5-enforce-scoped-proof-on-fresh-stores) · [storage contract](contracts.md#storage-and-typed-evidence)

### Subtasks

- [x] 5.1 Add `executor.CleanupEvidence`, schema-9 scope/state constraints without DEFAULT, and cross-scope pairing checks → verify: fresh SQL rejects omitted/NULL/unknown scope/state, Darwin retired and Linux observed; current Linux behavior remains usable.
- [x] 5.2 Centralize immutable scope/state transitions and policy-completion predicates in `internal/runstore` → verify: normal reuse/retention accepts Linux retired and Darwin observed, while pending/errors/inconsistency preserve quarantine. Observed never implies host quiescence.
- [x] 5.3 Synchronize manifest child proof transactionally and validate homogeneous batch aggregation → verify: child-log eviction does not erase authoritative proof.
- [x] 5.4 Compare retained rows/manifest copies and derive aggregates in one read snapshot, closing it before indexing → verify: concurrent completion causes no false mismatch.
- [x] 5.5 Keep existing-v8 stores explicitly refused on this unreleased intermediate branch; do not recreate them or ship before Task 6.
- [x] 5.6 Keep the intermediate Linux v1 path usable through the explicitly scoped transitional adapter in the plan; forbid unknown/Darwin fallback and schedule its removal in Task 8.

### Execution context

- **2026-10-23 — authorized scope and baseline:** The user requested Task 5 on Linux, starting from clean `828ee23bc33e1a18d4a848cdd75feb793c76375d`. Tasks 1–4 are complete. This slice adds fresh schema-9 scoped evidence, transactional manifest copies and snapshot validation, and a temporary Linux-v1 completion adapter. Existing schema-8 stores are explicitly refused until Task 6; public version-2 publication, protocol-v2 production wiring and Darwin activation remain Tasks 7–10. Verification uses synthetic keys and isolated stores; installed capture remains on its existing binary/store.
- **2026-10-23 — implementation approach:** Backend admission supplies an explicit scope independently of user program input. Storage transitions validate scope/state together and preserve scope/generation; completion and reuse/retention use policy acceptance, never a host-quiescence claim. Manifest proofs survive log eviction and are compared with retained rows in one bounded snapshot, released before lazy indexing. Public v1 serialization remains unchanged while typed cleanup and private manifest storage are introduced.
- **2026-10-23 — verification and fixes:** Final full runstore/execution integration race passed (102.501s / 62.194s), as did selected public server/CLI capture regressions, affected-code vet and the full FTS5 build. Both quality reports pass with all reported metrics unchanged. Six-sample runstore benchmark medians: capture +2.46%, first search −4.35%, repeat search −2.67%, fragmented search −5.72%; no significance claim. Initial compile errors and a quadratic batch-recovery regression were corrected without weakening assertions or deadlines. Commands, captured run IDs, native limits and evidence are in [storage-proof.md](storage-proof.md).
- **2026-10-23 — review and completion:** Isolated `kk:review-code` approved Task 5 after a P2 fix preserving committed `not_spawned` outcomes when a helper close/notification fails. A real-helper race fixture and the final full integration run verify the correction. No open code finding remains; PAL was unavailable and tests are author-attributed. `kk:test` and `kk:document` completed for this slice. No new conventions/findings need indexing because the evidence record captures them. Task 5 is done; Task 6 is next. Schema-8 migration, public v2, protocol-v2 production and native activation remain explicit later tasks; no new macOS CI result is claimed.

## Task 6: Preserve version-8 histories through migration

- **Status:** pending
- **Depends on:** Task 5
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [migration plan](implementation.md#task-6-preserve-version-8-histories-through-migration) · [upgrade contract](contracts.md#upgrade-representation)

### Subtasks

- [ ] 6.1 Implement authenticated exclusive-lifecycle transactional rebuild and policy update in `migrate.go`/attachment → verify: explicit Linux backfill, no final column DEFAULT, SQL omission/cross-scope-pair rejection after upgrade, and no conversion of Linux retired to observed.
- [ ] 6.2 Preserve IDs, logs, cursors, timestamps, quarantine/accounting and saved child proofs under the specified retained/evicted rules → verify: missing nonterminal or inconsistent evidence aborts safely.
- [ ] 6.3 Cover wrong keys, live handles, interruption/rollback, replaced stores, unexpected versions and FK/index preservation → verify: no diagnostic/supervisor migration or automatic recreation/discard.
- [ ] 6.4 Account for transient encrypted table-copy/WAL disk requirements separately from retained quota → verify: space failure rolls back without eviction or duplicate retained-byte charges.
- [ ] 6.5 Return actionable authenticated found/supported-version errors; preserve old-binary symptom documentation and the explicit rollback path.

## Task 7: Publish version-2 recovery views and hook parsing

- **Status:** pending
- **Depends on:** Task 6
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [public results](implementation.md#task-7-publish-version-2-recovery-views-and-hook-parsing) · [public contract](contracts.md#public-version-2-envelopes)

### Subtasks

- [ ] 7.1 Publish cleanup at the exact run/batch/list/search placements and normalize historical blobs → verify: all views agree and bounded serialization preserves essential proof.
- [ ] 7.2 Preserve compatible cursor/redaction formats and actual CLI/MCP outcomes → verify: prior cursors return identical retained bytes and incomplete cleanup keeps exit/signal evidence.
- [ ] 7.3 Add explicit v1/v2 `healthyRunEnvelope` rules → verify: pending running eligibility, Linux retired / Darwin observed terminal eligibility, cross-scope/malformed/unknown rejection, no printed-JSON fallback, and unchanged one-use/security rules.
- [ ] 7.4 Exercise null proof for unadmitted errors and aggregate/evicted-child cases → verify: no manufactured or contradictory retirement.

## Task 8: Bind protocol-v2 proof through Linux execution

- **Status:** pending
- **Depends on:** Task 7
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Linux integration](implementation.md#task-8-bind-protocol-v2-proof-through-linux-execution) · [wire contract](contracts.md#private-protocol-v2)

### Subtasks

- [ ] 8.1 Connect acquisition references, common exchange and fdinfo validation to Linux startup; bind startup/backend/row scope → verify: malformed or mismatched frames cannot authorize work.
- [ ] 8.2 Replace unqualified wire `Retired` with typed cleanup evidence across stream/outcome/store adapters → verify: compiler phases, capture failure, lost notices and local-versus-durable state remain truthful; only valid scope/state pairs cross v2 boundaries.
- [ ] 8.3 Remove the transitional Linux adapter and aggregate proof across all phases → verify: compiler retirement followed by runtime spawn failure remains retired, unresolved phases remain pending, and failed persistence never upgrades the durable proof.
- [ ] 8.4 Preserve the Linux descendant algorithm and run stored ownership/death fixtures → verify: escaped descendants, delayed backend close and pending-proof reset/reuse protection.

## Task 9: Integrate a private Darwin captured run

- **Status:** pending
- **Depends on:** Task 8
- **Size:** M
- **Can run in parallel with:** —
- **Strategy:** Risk-First
- **Docs:** [private Darwin integration](implementation.md#task-9-integrate-a-private-darwin-captured-run)

### Subtasks

- [ ] 9.1 Connect `launch_darwin.go`/`supervisor_darwin.go` to the proven executor and shared capture/storage → verify: one private real captured run with encrypted stdout/stderr recovery, nonzero exit and sanitized reads.
- [ ] 9.2 Exercise owner loss with buffered authorization and capture/finalization failure → verify: no premature gate/lease reuse or duplicate execution.
- [ ] 9.3 Keep production capability disabled until private integration passes; extract common launch mechanics only for the fixed shared sequence.
- [ ] 9.4 Adapt Task 2's private observation outcome into shared stream dispatch and `CleanupEvidence`; split unsupported stubs → verify: Darwin observed compiler/run aggregation, no Linux Retired shortcut, and pending on actual observation/capture-finalization failures as specified.

## Task 10: Enable native CLI and ordinary MCP execution

- **Status:** pending
- **Depends on:** Task 9
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [public activation](implementation.md#task-10-enable-native-cli-and-ordinary-mcp-execution)

### Subtasks

- [ ] 10.1 Enable only the proved OS/architecture matrix in shared service/capability selection → verify: compiled CLI/MCP once-only capture and recovery.
- [ ] 10.2 Preserve owner, credential, security and cwd selection through existing adapters → verify: unsupported hosts reject before spawn and no storage fallback appears.
- [ ] 10.3 Exercise CLI exit codes, MCP flags and response budgets for runtime failures → verify: thin adapters share the same scoped contract.

## Task 11: Verify yielded, file and batch execution

- **Status:** pending
- **Depends on:** Task 10
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [remaining modes](implementation.md#task-11-verify-yielded-file-and-batch-execution)

### Subtasks

- [ ] 11.1 Exercise compiled fixtures for `wait_ms`/background, execute-file and serial/parallel batches → verify: one execution, continued capture, deadlines, stable identity and recovery selectors.
- [ ] 11.2 Verify startup-gate serialization against scheduler capacity/running concurrency → verify: no sibling corruption or quarantine bypass.
- [ ] 11.3 Exercise shutdown/cancel across phases → verify: joins/reservations and batch expiry retain child proof; file/policy boundaries remain intact.

## Task 12: Add diagnostics and reproducible native CI

- **Status:** pending
- **Depends on:** Task 11
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [diagnostics and CI](implementation.md#task-12-add-diagnostics-and-reproducible-native-ci)

### Subtasks

- [ ] 12.1 Add capability, Darwin observational-limit wording and authenticated schema diagnostics with mixed-version/rollback guidance → verify: no storage creation, owner claims, probes, quiescence claims or trust assertions.
- [ ] 12.2 Add `make test-integration` and `make test-integration-race` with the specified packages/tags/timeouts; add Linux integration CI while retaining its current checks → verify: pre-feature baseline is recorded and newly exposed fixture failures are explicitly repaired.
- [ ] 12.3 Add native jobs for the hosted matrix selected in Task 1 → verify: named integration suites execute rather than skip; no late self-hosted dependency appears.
- [ ] 12.4 Extend actual macOS release-artifact smoke capture/recovery → verify: count, stdout/stderr and status beyond version printing.
- [ ] 12.5 Update operator docs and changed generators/committed counterparts → verify: parity/idempotency, explicit discard loss, and no hidden trust/enforcement changes.
- [ ] 12.6 Investigate intermittent native retention failures exposed by Task 4 CI → verify: preserve both hosted runs, reproduce/instrument close reconciliation in `TestLargeAdmissionMakesProgressAcrossEvictionBatches` and maintenance in `TestLargeEvictionPersistsAfterAppendRetryFails`, compare an immutable baseline, identify and fix the cause without skipped tests, relaxed assertions or enlarged capture deadlines, and obtain stable native regression evidence before release. **Owner:** feature implementer. **Reason deferred:** Task 4's lease fixtures passed in both runs and the full native suite passed with unchanged Go code on the second; retention failure causality remains unproven and needs separate targeted investigation. See [lease-proof.md](lease-proof.md#task-4-evidence). The early macOS proof job satisfies only part of Task 12.3; final integration targets and release-artifact coverage remain pending.

## Task 13: Complete release verification and documentation

- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3, Task 4, Task 5, Task 6, Task 7, Task 8, Task 9, Task 10, Task 11, Task 12
- **Size:** S
- **Can run in parallel with:** —
- **Docs:** [final verification](implementation.md#task-13-final-verification-and-documentation)

### Subtasks

- [ ] 13.1 Run `kk:test` for observed-policy completion/failures, lease proof, migration, every public mode, required full FTS5/race/vet/build, new Linux/macOS integration targets and artifact parity; record actual versions and missing evidence.
- [ ] 13.2 Run required executor quality/performance comparisons with matching immediate baselines; distinguish new Darwin capture measurements from comparable regressions.
- [ ] 13.3 Run `kk:document` for operations/rollback, architecture, help, AGENTS and proof records; close the bootstrap record and remove only authorized temporary entries.
- [ ] 13.4 Run `kk:review-code` with Go input and `kk:review-spec`; durably disposition findings and revisit architecture review if the documented topology changed.
- [ ] 13.5 Confirm bounded Darwin observation and lease proof gates, native all-mode acceptance, preserved Linux behavior/history and real CI coverage; then mark the feature complete. Accepted residual-process uncertainty is not a renamed retirement-proof blocker.

## Dependency Graph

```text
1 → 2 ─┐
       ├→ 5 → 6 → 7 → 8 → 9 → 10 → 11 → 12 → 13
3 → 4 ─┘
1..12 ───────────────────────────────────────→ 13
```

Parallel markers describe dependency freedom only, not authorization for agents
or overlapping edits. The public contract and Linux adapter tasks are sequential.
