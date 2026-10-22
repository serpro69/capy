# Tasks: native macOS captured execution

> Design: [design.md](design.md)
> Contract: [contracts.md](contracts.md)
> Implementation: [implementation.md](implementation.md)
> Evidence: [verification.md](verification.md) · Review: [review-resolution.md](review-resolution.md)
> Status: planned — corroborated design fixes applied; implementation/native proof pending
> Created: 2026-10-22
> Not Doing: Intel support, full Darwin detached-descendant cleanup, best-effort tracker, VMs, persistent/privileged services, restricted entitlements, self-hosted runner provisioning, other OS ports, PTY/stdin, persistent jobs, cross-owner cancellation, knowledge/vault migration, run rekey, automatic discard/downgrade, trust/security changes

Tasks 1–2 close the process gate; Tasks 3–4 close the lease gate. They are required
functionality, not exclusions. Use [bootstrap.md](bootstrap.md) only where Codex
enforcement requires it. No exception, implementation, or runtime proof was
created by this design revision. Contract fields are fixed before coding.

The former nine tasks are split before implementation. See the old→new mapping
in [review-resolution.md](review-resolution.md#task-split-map); all remain pending.

## Task 1: Select hosted support and prove bounded child waiting

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 3
- **Strategy:** Risk-First
- **Docs:** [bounded waiting](implementation.md#task-1-select-hosted-support-and-prove-bounded-child-waiting)

### Subtasks

- [ ] 1.1 Select the floor/deployment target from verified APIs/toolchain and stable hosted arm64 runner availability; record explicit labels and OS/kernel/SDK versions in `process-proof.md` → verify: every declared major, including the floor, is exercisable in CI without self-hosted provisioning.
- [ ] 1.2 Correct the `wait_darwin.go` ESRCH/NOTE_EXIT assumption and prove bounded terminal observation with one reaper → verify: stopped, exiting and cancelled direct-child fixtures.
- [ ] 1.3 Grade each matrix entry source-aligned or contract-backed → verify: missing matching sources are disclosed, while critical unsupported ordering remains unresolved.

## Task 2: Prove the Darwin group certificate

- **Status:** pending
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** Task 3, Task 4
- **Strategy:** Risk-First
- **Docs:** [group certificate](implementation.md#task-2-prove-the-darwin-group-certificate)

### Subtasks

- [ ] 2.1 Add a dormant `supervised_darwin.go` phase lifecycle with retained leader identity and bounded drainage; keep public admission disabled → verify: ordinary/cancelled/compiler phases and a live child with closed output.
- [ ] 2.2 Resolve fork/spawn publication and group re-entry against the precise scope → verify: justified certificate plus native adversarial fixtures, never signal/EOF/snapshot substitution.
- [ ] 2.3 Complete `process-proof.md` with evidence limits and the certificate; if unproved, return to design rather than mark rejection-only behavior done.

## Task 3: Construct acquisition-proven lease references

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 1, Task 2
- **Strategy:** Risk-First
- **Docs:** [acquired references](implementation.md#task-3-construct-acquisition-proven-lease-references)

### Subtasks

- [ ] 3.1 Encapsulate successful EX owner/SH lifecycle acquisitions in `internal/runstore/lease_refs.go` and the existing acquisition paths → verify: arbitrary/unlocked/wrong-mode files cannot construct valid launch inputs.
- [ ] 3.2 Bind borrowing/lifetime and exchange-gate identity to the shared acquisition context → verify: alias/concurrent borrower handling and close-not-unlock behavior.
- [ ] 3.3 Record the provenance boundary in `lease-proof.md`; exercise real reset/slot probes while references remain → verify: no new exchange/backend work is hidden in this slice.

## Task 4: Prove the common exchange and pre-start recovery

- **Status:** pending
- **Depends on:** Task 3
- **Size:** M
- **Can run in parallel with:** Task 2
- **Strategy:** Risk-First
- **Docs:** [exchange plan](implementation.md#task-4-prove-the-common-exchange-and-pre-start-recovery) · [wire contract](contracts.md#private-protocol-v2)

### Subtasks

- [ ] 4.1 Implement one portable `lease_exchange.go` sequence for Linux and Darwin, with additional Linux fdinfo verification → verify: real helper exec and causal two-way observations for both roles.
- [ ] 4.2 Cover independent same-inode/shared holders, swapped roles, reflected/replayed frames and invalid offsets → verify: identity alone accepts same-description unlocked aliases, but Task 3 excludes them from production.
- [ ] 4.3 Serialize cancellation with the first start-frame write; before it, kill/reap the direct helper within the separate observation budget and release the gate only after confirmation → verify: SIGSTOP timeout recovery and a subsequent run without restart.
- [ ] 4.4 Acquire the gate before spawning under the same startup budget; track its one-time release → verify: gate-wait timeout spawns no helper and cancellation after ready cannot release another exchange's token.
- [ ] 4.5 Cover partial authorization, late actors and failed/unbounded reap → verify: retained gate/leases/reservations when necessary, no post-authorization force-kill shortcut, and continued reset/slot exclusion.
- [ ] 4.6 Complete `lease-proof.md` with fixtures on both OS families and unchanged permanent bytes/lock modes.

## Task 5: Enforce scoped proof on fresh stores

- **Status:** pending
- **Depends on:** Task 2, Task 4
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [fresh stores](implementation.md#task-5-enforce-scoped-proof-on-fresh-stores) · [storage contract](contracts.md#storage-and-typed-proof)

### Subtasks

- [ ] 5.1 Add the shared typed proof and schema-9 scope column without DEFAULT → verify: fresh SQL rejects omitted, NULL and unknown scope; current Linux behavior remains usable.
- [ ] 5.2 Centralize immutable scope/state transitions and safe-terminal predicates in `internal/runstore` → verify: admission, completion, recovery/reuse and retention respect pending uncertainty.
- [ ] 5.3 Synchronize manifest child proof transactionally and validate homogeneous batch aggregation → verify: child-log eviction does not erase authoritative proof.
- [ ] 5.4 Compare retained rows/manifest copies and derive aggregates in one read snapshot, closing it before indexing → verify: concurrent completion causes no false mismatch.
- [ ] 5.5 Keep existing-v8 stores explicitly refused on this unreleased intermediate branch; do not recreate them or ship before Task 6.
- [ ] 5.6 Keep the intermediate Linux v1 path usable through the explicitly scoped transitional adapter in the plan; forbid unknown/Darwin fallback and schedule its removal in Task 8.

## Task 6: Preserve version-8 histories through migration

- **Status:** pending
- **Depends on:** Task 5
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [migration plan](implementation.md#task-6-preserve-version-8-histories-through-migration) · [upgrade contract](contracts.md#upgrade-representation)

### Subtasks

- [ ] 6.1 Implement authenticated exclusive-lifecycle transactional rebuild and policy update in `migrate.go`/attachment → verify: explicit Linux backfill, no final column DEFAULT, and SQL omission rejection after upgrade.
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
- [ ] 7.3 Add explicit v1/v2 `healthyRunEnvelope` rules → verify: correct pending/retired eligibility, malformed/unknown rejection, no printed-JSON fallback, and unchanged one-use/security rules.
- [ ] 7.4 Exercise null proof for unadmitted errors and aggregate/evicted-child cases → verify: no manufactured or contradictory retirement.

## Task 8: Bind protocol-v2 proof through Linux execution

- **Status:** pending
- **Depends on:** Task 7
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Linux integration](implementation.md#task-8-bind-protocol-v2-proof-through-linux-execution) · [wire contract](contracts.md#private-protocol-v2)

### Subtasks

- [ ] 8.1 Connect acquisition references, common exchange and fdinfo validation to Linux startup; bind startup/backend/row scope → verify: malformed or mismatched frames cannot authorize work.
- [ ] 8.2 Replace unqualified wire `Retired` with typed proof across stream/outcome/store adapters → verify: compiler phases, capture failure, lost notices and local-versus-durable state remain truthful.
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

- [ ] 12.1 Add capability and authenticated schema diagnostics with mixed-version/rollback guidance → verify: no storage creation, owner claims, probes or trust assertions.
- [ ] 12.2 Add `make test-integration` and `make test-integration-race` with the specified packages/tags/timeouts; add Linux integration CI while retaining its current checks → verify: pre-feature baseline is recorded and newly exposed fixture failures are explicitly repaired.
- [ ] 12.3 Add native jobs for the hosted matrix selected in Task 1 → verify: named integration suites execute rather than skip; no late self-hosted dependency appears.
- [ ] 12.4 Extend actual macOS release-artifact smoke capture/recovery → verify: count, stdout/stderr and status beyond version printing.
- [ ] 12.5 Update operator docs and changed generators/committed counterparts → verify: parity/idempotency, explicit discard loss, and no hidden trust/enforcement changes.

## Task 13: Complete release verification and documentation

- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3, Task 4, Task 5, Task 6, Task 7, Task 8, Task 9, Task 10, Task 11, Task 12
- **Size:** S
- **Can run in parallel with:** —
- **Docs:** [final verification](implementation.md#task-13-final-verification-and-documentation)

### Subtasks

- [ ] 13.1 Run `kk:test` for proof, migration, every public mode, required full FTS5/race/vet/build, new Linux/macOS integration targets and artifact parity; record actual versions and missing evidence.
- [ ] 13.2 Run required executor quality/performance comparisons with matching immediate baselines; distinguish new Darwin capture measurements from comparable regressions.
- [ ] 13.3 Run `kk:document` for operations/rollback, architecture, help, AGENTS and proof records; close the bootstrap record and remove only authorized temporary entries.
- [ ] 13.4 Run `kk:review-code` with Go input and `kk:review-spec`; durably disposition findings and revisit architecture review if the documented topology changed.
- [ ] 13.5 Confirm both proof gates, native all-mode acceptance, preserved Linux behavior/history and real CI coverage; then mark the feature complete.

## Dependency Graph

```text
1 → 2 ─┐
       ├→ 5 → 6 → 7 → 8 → 9 → 10 → 11 → 12 → 13
3 → 4 ─┘
1..12 ───────────────────────────────────────→ 13
```

Parallel markers describe dependency freedom only, not authorization for agents
or overlapping edits. The public contract and Linux adapter tasks are sequential.
