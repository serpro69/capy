# Design review corroboration and resolution

> Date: 2026-10-22
> Reviewed baseline: `64709e336c7410676620c904e338ab5a7c973438`
> Scope: ten user-supplied findings; independent source corroboration by the design author
> Result: all ten identify valid gaps requiring fixes; several recommendations qualified below
> Artifacts: [design](design.md), [contracts](contracts.md), [implementation](implementation.md), [tasks](tasks.md), [bootstrap record](bootstrap.md)

This is a disposition of the supplied review, not a claim of an additional
independent review. Repository and primary-source inspection corroborated the
findings. Only planning documents changed; no implementation, Makefile target,
CI job, routing exception, or runtime proof was executed/installed here.

## Findings

### F1 — P1: unspecified public/storage/private contracts

**Valid; fixed.** Existing [run types](../../../../internal/runstore/types.go),
[helper frames](../../../../internal/execution/protocol.go), and
[hook parsing](../../../../internal/hook/posttooluse.go) expose the concrete
interfaces the earlier prose failed to specify. The hook currently hard-checks
version 1. These are design choices, not Darwin feasibility questions.

[contracts.md](contracts.md) now defines the SQL column/constraints, canonical
typed proof, exact public placements/nullability, batch aggregation and evicted
child copies, v1/v2 JSON excerpts, hook eligibility, and all protocol-v2 fields
and ordering. Tasks 5–8 reference it and the public/private adapter tasks are
sequential. The kernel certificate alone remains deferred to its proof task.

### F2 — P2: Linux offset-exchange participation

**Valid ambiguity; fixed by choosing the common exchange on both platforms.**
The prior Darwin-specific seam and Linux fdinfo wording did not define a message
sequence. [Linux validation](../../../../internal/execution/supervisor_linux.go)
is retained before the common exchange, not replaced. Both platforms use the
[same v2 frames](contracts.md#private-protocol-v2) and portable fixtures; there is
no negotiated short sequence. The added Linux startup behavior requires tests
and performance checks; its cost is not assumed negligible.

### F3 — P2: pre-start helper can stall the owner

**Valid; fixed with a bounded recovery attempt and a necessary qualification.**
[Launch](../../../../internal/execution/launch_linux.go) retains a direct child;
[ServeInherited](../../../../internal/execution/supervisor_linux.go) requires start
authorization before pending/spawn. Therefore killing that helper is safe while
authorization has definitely not been attempted.

The [teardown contract](contracts.md#pre-start-teardown) now atomically tracks the
first start write, kills/reaps pre-start failures, and releases the gate after
confirmed reap. SIGSTOP recovery must permit another run without owner restart.
**Rejected part of the recommendation:** refusal cannot be limited absolutely to
post-authorization failures. SIGKILL delivery does not guarantee bounded reaping
of a kernel-stuck child. If the bounded attempt fails, keep ownership/gate and
refuse startup until safe. A failed/partial start write is treated conservatively
as possible authorization, never a reason to kill a potentially executing helper.

### F4 — P2: downgrade and mixed-version recovery

**Valid; fixed.** [Store attachment](../../../../internal/runstore/store.go)
checks exact schema and maps mismatches to generic path/permission advice today.
[Reset](../../../../cmd/capy/runs_reset.go) is the existing explicit discard path.

[Rollback instructions](design.md#rollback-and-mixed-versions) now cover exports,
quiescence, explicit owner-local discard, coherent binary/PATH/plugin/binding
downgrade, and the return of old macOS limitations. The new binary must report
authenticated versions clearly; old-binary generic symptoms are documented.
**Qualification:** 24 hours is a configurable default, not a bound on lost history;
unverified evidence may be older. No plaintext marker is added: old binaries do
not read it, so it cannot fix their error and would add a stale-state surface.

### F5 — P2: support floor versus hosted CI availability

**Valid; fixed.** Current [CI](../../../../.github/workflows/ci.yml) is Ubuntu-only;
the [release job](../../../../.github/workflows/release.yml) only smoke-checks a
macOS build. [GitHub's authoritative runner list](https://docs.github.com/en/actions/reference/runners/github-hosted-runners)
is a limited, changing matrix; `-latest` is not a promised OS floor.

Task 1 must select the intersection of API/toolchain requirements and available
stable hosted arm64 runners, including a testable floor, before committing the
support matrix. Record labels and actual OS builds. Self-hosted provisioning is
explicitly out of scope; an empty intersection stops the initial gate rather than
creating a late impossible release task.

### F6 — P2: oversized M tasks

**Valid structural concern; fixed by splitting before implementation.** Original
Tasks 1/2/3/6 bundled distinct waiting/certificate, acquisition/exchange, fresh
schema/migration, and private-helper/public-activation responsibilities. The
generic instruction to split later did not provide actionable commit boundaries.

The plan now has thirteen pending slices; see the map below. Acquisition/reference
construction is separate from its exchange state machine, and fresh-store proof
semantics are separate from migration. Tests for one mechanism remain with that
mechanism. No L task is relabelled M, and no unresolved shared type ownership is
hidden behind parallel markers.

### F7 — P3: scope DEFAULT would silently classify new rows

**Valid; fixed.** SQLite's documented ADD COLUMN rule requires a non-null default
for NOT NULL; the inspected pinned binding reports SQLite **3.51.2**, not the
newer ALTER COLUMN capabilities in current docs. [SQLite ALTER TABLE](https://www.sqlite.org/lang_altertable.html).

The [upgrade contract](contracts.md#upgrade-representation) chooses a transactional
table rebuild with the explicit Linux value only in the old-row copy. Final SQL
has no DEFAULT and must reject an INSERT omitting scope, NULL, or unknown scope.
FK/index preservation and failure rollback are required. Temporary copy/WAL disk
headroom is budgeted separately from logical retained-data quota; copied data
must not be counted twice as retained logs. This is a planned migration, not an
executed one.

### F8 — P3: host-specific bootstrap and authorization record

**Valid; fixed.** [Codex routing](../../../../internal/hook/routine.go) enforces
the routine gate, while [operator documentation](../../../reference/runs.md)
explicitly gives Claude guidance-only behavior. [Configuration tests](../../../../internal/config/routing_test.go)
confirm the `routing.routine.native_allow` shape and precedence.

[bootstrap.md](bootstrap.md) now names enforcing Codex hosts, gives a narrow TOML
example, identifies the human operator as authorizer, and provides a durable
authorization/application/removal record. It preserves prior entries/security
and does not authorize or apply an exception. Non-enforcing hosts record why none
is required rather than inventing a universal permission step.

### F9 — P3: unmatched XNU source evidence

**Valid overstrict wording; fixed without assuming tests prove kernel races.**
[The verification record](verification.md) already states that inspected XNU
sources were not matched to the host. Requiring matching source for every version
was stronger than the available evidence model.

The [evidence standard](design.md#evidence-standard) distinguishes source-aligned
and documented-contract-backed evidence with native fixtures. It records the
latter as weaker, but does not block solely because Apple has not published that
kernel tree. If a critical invariant relies on undocumented implementation
ordering, passing stress tests still cannot close it; narrow the matrix or revise
the mechanism. Historical verification limits remain unchanged.

### F10 — P3: Linux integration coverage is new work

**Valid for CI/Makefile; fixed.** [Makefile](../../../../Makefile) and
[CI](../../../../.github/workflows/ci.yml) select FTS5/race/glamour, not integration.
The [supervisor](../../../../internal/execution/supervisor_integration_test.go),
[stored recovery](../../../../internal/execution/stored_recovery_integration_test.go),
and [batch](../../../../internal/execution/batch_integration_test.go) suites require
`linux && integration`. **Qualification:** “skipped everywhere” is too broad;
earlier proof reports record manual tagged runs. The automated coverage gap is real.

Task 12 now explicitly adds Linux integration coverage and reproducible
`test-integration`/`test-integration-race` Makefile targets. It preserves existing
Linux checks, records a pre-feature baseline, and budgets newly exposed fixture
failures for explicit fixes/evidence rather than removing tags or weakening tests.
Those implementation changes remain pending.

## Task split map

| Original task | Revised tasks |
| --- | --- |
| 1: process proof | 1: hosted matrix/bounded wait; 2: group certificate |
| 2: lease proof | 3: acquired references; 4: common exchange/pre-start recovery |
| 3: scoped storage/upgrade | 5: fresh scoped stores; 6: preserved v8 migration |
| 4: public v2 | 7: public recovery/hook contract |
| 5: Linux live proof | 8: private v2/Linux integration, after 7 |
| 6: Darwin integration | 9: private captured run; 10: public CLI/MCP activation |
| 7: remaining modes | 11: yielded/file/batch acceptance |
| 8: diagnostics/CI | 12: diagnostics and new reproducible integration CI |
| 9: final verification | 13: release verification/documentation |

## Validation of this revision

Completed local validation: all seven Markdown documents' links/anchors and
whitespace, both JSON examples, the TOML example, thirteen pending tasks' required
metadata, acyclic dependencies, reciprocal/nonconflicting parallel markers, and
ten finding dispositions passed. `git diff --check` also passed. The author
cross-checked contract-to-task references and the preserved feasibility limits.

These checks establish documentation consistency, not runtime behavior or
implementation feasibility. Native process/lease proofs remain mandatory
implementation gates. No new capy knowledge notes are needed: the findings and
qualifications now have repository-local durable records.
