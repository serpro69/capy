# Design readiness review: upstream-sync-v1.0.169

> Reviewed starting revision: `22d1310` (runtime remains at the previously inspected baseline)
> Scope: complete design, implementation plan, tasks, upstream audit and previous reconciliation
> Mode: `kk:review-design` standard; author self-review, not an additional independent reviewer
> Initial assessment: CONCERNS_FOUND
> Final assessment: SOUND after the corrections below; implementation may start
> Runtime feature status: not implemented; all implementation checkboxes remain pending

## Summary

The re-read found **9 additional issues: 0 critical, 3 high, 5 medium, 1 low**. All were resolved in the current documentation. Some were omissions in the prior fixes, so the earlier passing link/dependency checks were insufficient to call the packet technically clean.

The packet now separates the responsibilities clearly: [design](../design.md) defines contracts and tradeoffs, [implementation](../implementation.md) names owners and verification, [tasks](../tasks.md) tracks 26 bounded slices, and [audit](../upstream-audit.md) records provenance and exclusions. The [previous reconciliation](reconciliation-2026-10-10.md) is explicitly historical; its old section numbers and 25-task count do not override the current packet.

## P0 — Critical

(none)

## P1 — High

### R01 — Git ignore lookup and followed directory symlinks were incompatible

- **Type:** TECH_RISK
- **Confidence:** 10/10 — reproduced with installed Git 2.34.1 in a disposable repository.
- **Evidence:** querying `alias/ignored.txt` when `alias` points to a real directory exits 128: `fatal: pathspec 'alias/ignored.txt' is beyond a symbolic link`. Querying `real/ignored.txt` succeeds and reports the ignore match. The former plan combined optional following with Git lookup without choosing a compatible path representation.
- **Correction:** design §5.4 uses canonical target paths for descendant ignores/selection/labels, checks alias and target mandatory exclusions, and prunes nested Git roots. Explicitly selected roots have their own admission contract. New Task 13a owns optional following and its Git integration, after default directory indexing works.

### R02 — Observed child capabilities could become permanent false evidence

- **Type:** TECH_RISK
- **Confidence:** 9/10 — direct consequence of the proposed state machine; not a claim about already implemented behavior.
- **Evidence:** the prior design recorded successes but specified neither expiry nor consumption after a redirect. If the server subsequently became unavailable, every attempted native fallback could be blocked by the same old success. “Cleared with session state” named no real mechanism: `internal/hook/sessionend.go` is a no-op.
- **Correction:** observations now expire independently per tool after 60 seconds and are consumed before one redirect. Failure does not renew them; the next native attempt may fall back. The plan defines suitable alternatives, stable identity requirements, a single bounded project-state file, atomic updates, short lock waiting and explicit session cleanup without DB access. Missed cleanup cannot produce unbounded per-session files. Task 5a verifies retry failure, expiry, sibling isolation and lifecycle behavior. D5 still covers unknown first-call availability.

### R03 — Stale-refresh budgeting could prevent valid files from ever being checked

- **Type:** INCONSISTENT / TECH_RISK
- **Confidence:** 10/10 — the two specified bounds contradicted each other.
- **Evidence:** a configured maximum `M > 8 MiB` gave a pass budget of `M`, while the bounded reader needed `M + 1` to detect growth. Under the “only admit if bounded read fits” rule, the largest legitimate file could never start. A deadline including slow metadata initialization could similarly allow zero file progress in every new CLI instance. The cooldown alone also does not forbid an earlier slow pass remaining active when a later window starts.
- **Correction:** §5.5 budgets `max(8 MiB, M + 1)`, includes checked arithmetic and probe bytes, starts its admission clock after metadata selection, and allows at most one active pass per store instance. Scheduling updates are atomic short transactions, identify the checked source incarnation and surface failed progress writes. Task 12a explicitly tests large limits, slow metadata/I/O, concurrency and restart progress.

## P2 — Medium

### R04 — Directory counters did not define a bound on actual work

- **Type:** AMBIGUOUS
- **Confidence:** 9/10 — admitted-content bytes and bytes actually read are distinct quantities.
- **Evidence:** the old 32 MiB “admitted content” language left failed/growing/unchanged reads unaccounted for and did not specify whether failed eligible files consumed `max_files`. Partial operational failures versus hard-limit outcomes were also not explicit for MCP.
- **Correction:** §5.3 defines eligible attempts, visited entries and actual raw content-read bytes separately. Failed/unchanged reads and probe bytes consume the byte budget. Partial files are not indexed on exhaustion. Caps return incomplete-success; operational failures/cancellation return marked partial errors with committed progress retained. Tasks 13/13a cover these outcomes.

### R05 — Fetch validation time could incorrectly include indexing-queue delay

- **Type:** AMBIGUOUS
- **Confidence:** 9/10 — the previous wording did not identify when “successful validation time” was captured.
- **Evidence:** both the current and planned batch paths fetch concurrently and index serially. Stamping at commit would make a response waiting in that queue look newly fetched, defeating short TTLs. Taking a maximum timestamp independent of content also mislabels out-of-order commits.
- **Correction:** §7.1 captures a timestamp per admitted complete HTTP body and carries it with the content into the atomic write. Conversion/queue/lock delay does not renew it. Task 16a tests delayed queues and out-of-order commits as well as unchanged-content renewal.

### R06 — Bounded batch summaries still needed the dedup outcome contract

- **Type:** MISSING
- **Confidence:** 10/10 — verified in `internal/store/index.go:indexPreparedChunks`.
- **Evidence:** the same-hash return has `AlreadyIndexed=true` but leaves `TotalChunks` zero. The current batch formatter uses that result field. New omission counts cannot use it as the stored inventory size.
- **Correction:** §4.4 and Task 18 obtain stored totals on unchanged batches and test repeated identical calls. The implementation owner list now names the required bounded store metadata query rather than only server files.

### R07 — The admitted-source bound was overstated as a stored-chunk bound

- **Type:** INCONSISTENT
- **Confidence:** 10/10 — verified against chunker code and Go's actual JSON encoder.
- **Evidence:** `walkJSON`/`chunkJSONObject`/`chunkJSONArray` reserialize values; escaping and pretty-printing can expand them. A standard-library probe using the same `json.Marshal` behavior produced `input=400002 encoded_leaf=2400002 default_source_limit=2097152`. This is a serializer probe plus source inspection, not a full store benchmark. Sanitization/overlap can also change output length.
- **Correction:** §5.2 and D4 now distinguish admitted input, rendered response limits and stored size. Structured splitting remains deliberately deferred; the docs no longer promise an exact 2 MiB maximum for those stored chunks or DB growth.

### R08 — Anchored hook context selection lacked its actual owner and failure transport

- **Type:** INCOMPLETE
- **Confidence:** 9/10 — verified against `config/paths.go`, `cmd/capy/hook.go` and the generated wrapper.
- **Evidence:** current `DetectProjectRoot()` has no start-directory argument and probes process cwd. The wrapper intentionally masks hook exit failure. Simply returning an error after invalid explicit selection or file-policy preparation could therefore pass through instead of issuing the promised block.
- **Correction:** Task 5 names an anchored detection helper with no process-wide cwd/env mutation. The design defines invalid payload fallback versus invalid explicit selection and requires structured PreToolUse blocks for admission failures; non-permission events report errors and skip state changes. Tests cover directory precedence and the wrapper-facing response.

## P3 — Low

### R09 — Historical numbering and unresolved small contracts weakened readability

- **Type:** STRUCTURE / AMBIGUOUS
- **Confidence:** 9/10 — visible in the previous artifact text.
- **Evidence:** the design placed §3.2a before §3.2, mixed boolean inputs with fetch-retry guidance, and both hashed and optionally ignored empty guidance IDs. Task 13 deferred its own decomposition instead of separating the now-substantial symlink/Git path.
- **Correction:** design section numbering is sequential, boolean/retry topics are separate, ID alphabet/length/digest/empty behavior are exact, and Task 13a is a separate vertical slice. Original task IDs remain stable for review traceability. Historical review counts are labeled as historical, and all current links/dependencies are checked against the current artifacts.

## Clean areas and remaining implementation risks

- Exact upstream endpoints and selected/covered/excluded distinctions remain intact. This pass did not reopen settled platform/analytics exclusions or add another persistence subsystem.
- Prepared Read policy, external-file exceptions and conservative legacy-deny treatment remain explicit. Complete host policy emulation is not promised; unsupported syntax produces a visible admission failure.
- Cache freshness remains independent of retention, with nullable millisecond metadata and a conservative legacy miss. Migration/old-reader/concurrent-write tests are release gates, not completed validation.
- Every response budget covers its defined complete surface; input/read/serialized-output/storage quantities are now distinguished. D4 remains a stored-structure limitation.
- Task dependencies are semantic. The directory split keeps optional following independently reviewable. Shared source files still need normal edit coordination.
- Encryption, key ownership, vault isolation, generator/copy synchronization, race testing and baseline benchmarks remain required. No production code or generated artifact was changed in this review.

No known unresolved design blocker remains after these edits. The highest-risk implementation checks are prepared-rule end-to-end behavior, migration/scheduler concurrency and actual host payload fixtures for the observation flow. Failure of those tests should drive a concrete correction before the relevant slice ships; it is not a reason to keep running speculative document reviews first.

D1–D5 remain explicit accepted scope limits. In particular, the design does not claim per-agent MCP throttling, an OS filesystem sandbox, unique concurrent batch identities, universal structured-chunk caps or authoritative first-call child capability knowledge.

## Verification

- Re-read the complete five-document packet and relevant current code owners; read truncated output ranges separately rather than relying on snippets.
- Queried existing architecture/review notes read-only; no knowledge indexing or category-label replacement performed.
- Ran the real Git alias/canonical-path experiment and a standard-library JSON encoding probe in a temporary directory. Their observations are recorded above; the fixture is not an implementation test suite.
- Temporary fixture cleanup was blocked by automatic approval review as destructive file removal. `/tmp/capy-design-readiness-dEZloF` remains outside the repository; remove it only with explicit deletion authorization. This housekeeping limitation does not block the feature or documentation checks.
- Final checks passed for 6 documents, 72 local links and all 26 tasks: anchors, required metadata, dependency cycles, parallel-pair validity, final-task coverage, fences and whitespace. Implementation statuses remain pending.
- Did not repeat the previously passing runtime suites or benchmarks: only documentation changed, and those checks cannot validate unimplemented contracts.

Optional prose editing can target the materially revised design/implementation/tasks/audit via `kk:clarify-docs`; it is not a prerequisite to start coding. The next action is `kk:implement upstream-sync-v1.0.169`, selecting a task whose dependencies are satisfied. This standard author review should not be described as a third independent approval.
