## Architecture Review — design.md

**Artifact:** [design.md](design.md)
**Type:** Design-doc architecture section
**Scope:** “Ownership and startup”
**Overall assessment:** **SOUND**

The original wording required retaining the exchange gate until reap for every failure before a start write. The implementation releases it after validated `ready`, when offset mutation ends. The author corrected that wording during review; the refreshed claims C36/C43 and new explicit claims C52/C53 match the implementation. No open findings remain. This is a read-only topology review; tests were not run.

### Claim Set (Pass 0 — verbatim)

> Extraction completeness is not certified: recall has no production oracle, so an assertion missed during extraction would be absent.
>
> Source lines refer to the reviewed artifact’s “Ownership and startup” section. Existing claim IDs were retained when the artifact was corrected; C52/C53 identify newly explicit assertions.

| id | claim | source_span | dimension | tense | provenance | evidence_class |
| --- | --- | --- | --- | --- | --- | --- |
| C1 | The service remains the logical execution owner. | L85 | 2 | present | harvested | Execution ownership and admission bindings |
| C2 | The helper retains owner-slot and lifecycle lease references through cleanup, capture finalization, and backend close. | L85–86 | 5 | present | harvested | Helper resource-lifetime and finalization structure |
| C3 | Executed commands do not inherit the helper’s private descriptors. | L87 | delegated | present | harvested | Private descriptor inheritance boundary |
| C4 | Credentials use a bounded private channel rather than argv, new environment variables, or plaintext manifests. | L87–89 | delegated | present | harvested | Credential transport boundary |
| C5 | Owner EOF remains an execution-control input. | L89–90 | 4 | present | harvested | Owner-liveness failure handling |
| C6 | Cancellation remains an execution-control input. | L89–90 | 4 | present | harvested | Cancellation transport and handling |
| C7 | Deadlines remain execution-control inputs. | L89–90 | 4 | present | harvested | Execution deadline wrappers |
| C8 | A buffered start frame can race owner death. | L90–91 | unrouted | present | harvested | Runtime ordering evidence |
| C9 | Cleanup covers the buffered-start/owner-death race. | L90–91 | 4 | present | harvested | Post-start owner-loss cleanup path |
| C10 | Linux and Darwin perform the same shared-offset identity exchange. | L93 | 5 | present | harvested | Common lease-continuity exchange and platform entry points |
| C11 | Linux additionally validates descriptor locks through fdinfo before the exchange. | L94–95 | 5 | present | harvested | Linux descriptor-lock validation placement |
| C12 | Both platforms use one protocol-v2 message sequence without platform-negotiated shortcuts. | L95–96 | 6 | present | harvested | Private protocol version and shared message definitions |
| C13 | Portable fixture coverage runs on Linux and Darwin. | L96 | unrouted | present | harvested | Platform test execution records |
| C14 | Linux’s descendant cleanup algorithm remains unchanged. | L96–97 | unrouted | present | harvested | Historical behavioral comparison |
| C15 | New startup failure and latency paths require regression checks and measurement. | L97–98 | unrouted | present | harvested | Verification requirements and measurement records |
| C16 | Duplicated or inherited regular-file descriptions share offsets; independent opens do not. | L100–101 | unrouted | present | harvested | Operating-system descriptor semantics |
| C17 | The exchange uses ordered observations on each process’s own descriptor without changing lock modes or file contents. | L101–102 | 5 | present | harvested | Descriptor-offset exchange operations |
| C18 | Echoed tokens and matching initial offsets are insufficient identity evidence. | L102–103 | pass2 | present | harvested | Identity-mechanism rationale |
| C19 | Production launch consumes runstore-controlled references to the exact acquired owner EX and lifecycle SH descriptions. | L105–107 | 5 | present | harvested | Acquired-lease constructors and launch interface |
| C20 | Arbitrary files cannot construct usable production launch references. | L107–108 | 5 | present | harvested | Lease-reference construction boundary |
| C21 | Acquired references preserve continuous lifetime without unlocking or converting the inherited locks. | L108–109 | 5 | present | harvested | Lease reference counting and lock operations |
| C22 | File-safety checks remain in place. | L109 | delegated | present | harvested | File-safety validation boundary |
| C23 | Binding retains inode/store/slot, generation, and policy checks. | L109–110 | 5 | present | harvested | Run-to-lease and policy binding validators |
| C24 | The protocol assumes cooperating Capy processes rather than resistance to arbitrary same-user forgery. | L110–111 | delegated | present | harvested | Protocol threat assumptions |
| C25 | Startup follows admission → identity exchange → helper binding → ready/start → durable pending proof → command spawn. | L113–115 | 5 | present | harvested | Startup state-machine placement |
| C26 | The identity exchange precedes inherited-descriptor authorization of helper storage access. | L115–116 | delegated | present | harvested | Storage authorization boundary |
| C27 | Exchange messages are bound to role, phase, and exchange. | L116 | 5 | present | harvested | Message schema and sequence validation |
| C28 | Startup retains bounded framing. | L116–117 | 4 | present | harvested | Startup frame and I/O bounds |
| C29 | Exchange failures prevent program spawn. | L117 | 4 | present | harvested | Exchange failure exits before execution |
| C30 | Exchanges serialize across aliases of either description, including parallel batch launches. | L119–120 | 5 | present | harvested | Shared acquisition gate and batch launch wiring |
| C31 | Gate acquisition precedes helper creation. | L120 | 5 | present | harvested | Gate-to-process-creation ordering |
| C32 | Gate waiting consumes the startup deadline, shortened by any earlier execution deadline. | L120–121 | 4 | present | harvested | Gate wait deadline construction |
| C33 | Gate timeout leaves known-not-spawned work without creating a stranded helper. | L121–122 | 4 | present | harvested | Gate failure path before helper creation |
| C34 | Failure before any start-write attempt disables authorization. | L123–124 | 5 | present | harvested | Startup cancellation state transition |
| C35 | Failure before any start-write attempt SIGKILLs the retained direct helper. | L123–124 | 4 | present | harvested | Pre-start direct-child termination path |
| C36 | Pre-start failure reaps the helper before releasing any exchange gate it still holds. | L123–125 | 5 | present | harvested | Terminal-status guard around retained gate release |
| C37 | Teardown has a separate three-second observation budget. | L127 | 4 | present | harvested | Teardown deadline construction |
| C38 | An ordinary SIGSTOPped helper recovers without restarting its owner. | L128 | unrouted | present | harvested | Native stopped-helper runtime evidence |
| C39 | Cancellation and the first start write share an atomic phase boundary. | L129 | 5 | present | harvested | Shared cancellation/authorization state guard |
| C40 | Partial start writes count as possible authorization. | L129–130 | 5 | present | harvested | Start-write-attempt state transition |
| C41 | After that boundary, failures retain cooperative cleanup and durable-proof handling rather than directly killing the helper. | L130–131 | 4 | present | harvested | Post-authorization failure handling |
| C42 | SIGKILL does not guarantee a bounded reap. | L133 | pass2 | present | harvested | Teardown uncertainty rationale |
| C43 | Unconfirmed teardown retains the actor, leases, and any still-held gate under cleanup tracking. | L133–134 | 5 | present | harvested | Retained startup actor and resource ownership |
| C44 | Unconfirmed teardown blocks affected-owner startup until safe. | L134–135 | 4 | present | harvested | Unresolved-cleanup admission guard |
| C45 | Teardown does not abandon its waiter. | L135 | 4 | present | harvested | Retained sole-waiter ownership |
| C46 | Signal delivery alone does not release a held exchange gate. | L135–136 | 5 | present | harvested | Terminal-status prerequisite for teardown release |
| C47 | Joining does not hold the general service mutex. | L136–137 | 5 | present | harvested | Join placement relative to service locking |
| C48 | Normally running helpers no longer mutate lease offsets. | L137 | 5 | present | harvested | Exchange-phase separation from execution |
| C49 | Gate release is one-time, including cancellation after ready. | L138 | 5 | present | harvested | Per-exchange release token |
| C50 | Failed proof returns to the ownership decision instead of silently substituting inode checks or incoming-FD lock acquisition. | L142–143 | pass2 | present | harvested | Ownership fallback decision |
| C51 | Helper acquisition or protected transfer may change topology and are not pre-approved automatic alternatives. | L143–145 | pass2 | present | harvested | Alternative ownership topology and decision scope |
| C52 | Validated ready releases the exchange token after the helper stops changing offsets. | L125–126 | 5 | present | harvested | Ready-phase gate release |
| C53 | Later teardown neither reacquires that gate nor releases a subsequent exchange’s token. | L126–127 | 5 | present | harvested | Teardown token ownership and release isolation |

### Verdicts by dimension (Pass 1)

All evidence paths below are concrete paths relative to `/home/sergio/Projects/personal/capy`. Verdicts confirm mechanism presence and placement, not runtime correctness.

#### 1. Structural Boundaries

(none)

#### 2. Data Ownership

| Claim | Verdict | Mode | Evidence inspected | Confirmation |
| --- | --- | --- | --- | --- |
| C1 | verified | reality | `internal/execution/service.go`; `internal/execution/stored_run.go`; `internal/runstore/owners.go` | The service claims the owner and admits runs; helper storage binds to the admitted generation. |

#### 3. NFR Mechanisms

(none)

#### 4. Failure Isolation

| Claim | Verdict | Mode | Evidence inspected | Confirmation |
| --- | --- | --- | --- | --- |
| C5 | verified | reality | `internal/execution/supervisor_unix.go` | Control-channel read failure feeds `owner_lost` cancellation. |
| C6 | verified | reality | `internal/execution/launch.go`; `internal/execution/supervisor_unix.go` | Cancel frames connect owner cancellation to the helper execution context. |
| C7 | verified | reality | `internal/execution/supervisor_unix.go` | The admitted deadline wraps the execution context. |
| C9 | verified | reality | `internal/execution/supervisor_unix.go`; `internal/executor/supervised_linux.go`; `internal/executor/supervised_darwin.go` | Post-start owner-loss cancellation reaches platform cleanup paths. Race behavior itself is not certified. |
| C28 | verified | reality | `internal/execution/protocol.go`; `internal/execution/lease_exchange.go`; `internal/execution/lease_helper.go` | Length-limited frames and deadline-bound reads/writes wrap startup. |
| C29 | verified | reality | `internal/execution/lease_helper.go`; `internal/execution/supervisor_unix.go` | Exchange errors return before backend execution. |
| C32 | verified | reality | `internal/execution/lease_launch.go`; `internal/runstore/lease_refs.go` | The minimum deadline is established before context-bound gate acquisition. |
| C33 | verified | reality | `internal/execution/lease_launch.go`; `internal/execution/service.go` | Gate acquisition failure returns before `cmd.Start`; the service has an unstarted-result path. |
| C35 | verified | reality | `internal/execution/lease_launch.go` | The pre-write failure branch invokes the retained direct process’s `Kill`. |
| C37 | verified | reality | `internal/execution/lease_launch.go` | `preStartTeardownTimeout` is three seconds; teardown uses a fresh background timeout. |
| C41 | verified | reality | `internal/execution/lease_launch.go`; `internal/execution/launch.go`; `internal/execution/service.go` | Direct kill is gated on no start-write attempt; later failures retain joining, control-channel cleanup, durable-state inspection, and quarantine. |
| C44 | verified | reality | `internal/execution/lease_launch.go`; `internal/execution/service.go`; `internal/runstore/owners.go` | Unresolved reservations, service quarantine, and stored admission quarantine supply blocking mechanisms. |
| C45 | verified | reality | `internal/execution/lease_launch.go`; `internal/execution/service.go` | The actor owns one waiter and remains registered while death is unconfirmed; cleanup tracking survives the response timeout. |

#### 5. State Consistency

| Claim | Verdict | Mode | Evidence inspected | Confirmation |
| --- | --- | --- | --- | --- |
| C2 | verified | reality | `internal/execution/supervisor_unix.go` | Cleanup, queue finalization, terminal persistence, and backend close precede deferred inherited-descriptor closure. |
| C10 | verified | reality | `internal/execution/lease_exchange.go`; `internal/execution/supervisor_linux.go`; `internal/execution/supervisor_darwin.go`; `internal/execution/lease_helper.go` | Both platform entry points use the common helper and exchange implementation. |
| C11 | verified | reality | `internal/execution/supervisor_linux.go`; `internal/execution/lease_helper.go` | Linux fdinfo validation is invoked before the common exchange. |
| C17 | verified | reality | `internal/execution/lease_exchange.go` | Ordered local `Seek` observations implement the exchange; this mechanism contains no file-content writes or lock conversions. |
| C19 | verified | reality | `internal/runstore/owners.go`; `internal/runstore/store.go`; `internal/runstore/lease_refs.go`; `internal/execution/launch.go` | Successful EX/SH acquisitions construct retained references accepted by the production launch interface. |
| C20 | verified | reality | `internal/runstore/lease_refs.go`; `internal/execution/lease_launch.go` | Usable reference state is private, lacks an FD constructor, and is required through `Retain`/`WithFiles`. |
| C21 | verified | reality | `internal/runstore/lease_refs.go`; `internal/execution/lease_launch.go`; `internal/execution/supervisor_unix.go` | Reference counting and close-only release preserve acquired descriptions; production exchange uses no unlock/conversion operation. |
| C23 | verified | reality | `internal/runstore/lease_refs.go`; `internal/runstore/owners.go`; `internal/execution/stored_run.go` | Concrete validators bind store/inodes/slot, owner generation, and admitted policy. Security effectiveness is not assessed. |
| C25 | verified | reality | `internal/execution/service.go`; `internal/execution/lease_helper.go`; `internal/execution/supervisor_unix.go` | The specified stages appear in the stated order, including `Pending` before executor invocation. |
| C27 | verified | reality | `internal/execution/lease_frames.go`; `internal/execution/lease_exchange.go` | Message fields and receive checks bind exchange ID, direction, role, step, and phase. |
| C30 | verified | reality | `internal/runstore/lease_refs.go`; `internal/runstore/store.go`; `internal/execution/batch.go`; `internal/execution/service.go` | Borrowers share their lifecycle-acquisition gate; parallel batch children use the same production launch path. |
| C31 | verified | reality | `internal/execution/lease_launch.go` | `AcquireExchange` precedes helper construction/start. |
| C34 | verified | reality | `internal/execution/lease_launch.go` | Cancellation marks the guarded state and closes startup channels; authorization checks that state. |
| C36 | verified | reality | `internal/execution/lease_launch.go`; `internal/runstore/lease_refs.go` | Teardown releases a still-held token only after terminal OS status; previously released ready tokens are one-time no-ops. |
| C39 | verified | reality | `internal/execution/lease_launch.go` | Cancellation and setting `startWriteAttempted` use the same mutex-protected phase state. |
| C40 | verified | reality | `internal/execution/lease_launch.go` | The attempt flag is set before the write operation, conservatively covering partial writes. |
| C43 | verified | reality | `internal/execution/lease_launch.go`; `internal/execution/service.go` | Unconfirmed termination retains the actor/reference pins and any unreleased gate under reservation and cleanup tracking. |
| C46 | verified | reality | `internal/execution/lease_launch.go` | Signal dispatch is separate from the terminal-status branch that releases retained resources. |
| C47 | verified | reality | `internal/execution/service.go`; `internal/execution/batch.go` | Helper joins occur outside the general service mutex; `Close` unlocks before waiting. |
| C48 | verified | reality | `internal/execution/lease_exchange.go`; `internal/execution/lease_helper.go`; `internal/execution/supervisor_unix.go` | Offset operations reside in the completed startup exchange, before ready/start and execution. |
| C49 | verified | reality | `internal/runstore/lease_refs.go`; `internal/execution/lease_launch.go` | Each exchange token owns a `sync.Once` release shared by ready and teardown paths. |
| C52 | verified | reality | `internal/execution/lease_launch.go`; `internal/execution/lease_helper.go` | Validated ready triggers token release after the helper has left its offset-mutating phase. |
| C53 | verified | reality | `internal/execution/lease_launch.go`; `internal/runstore/lease_refs.go` | Teardown reuses the actor’s original one-time token and contains no gate reacquisition. |

#### 6. Evolution & Versioning

| Claim | Verdict | Mode | Evidence inspected | Confirmation |
| --- | --- | --- | --- | --- |
| C12 | verified | reality | `internal/execution/protocol.go`; `internal/execution/lease_frames.go`; `internal/execution/lease_exchange.go`; `internal/execution/lease_helper.go` | Version 2 and the common message schema/sequence are shared; platform validation does not negotiate an alternative exchange. |

#### 7. Domain Binding

(none)

### Not Reviewed

**Delegated — informational; security architecture → `mcp__pal__secaudit`:**

- **C3:** Private descriptor isolation from executed commands.
- **C4:** Credential transport and exposure boundaries.
- **C22:** File-safety enforcement.
- **C24:** Same-user forgery and cooperation assumptions.
- **C26:** Storage authorization based on inherited descriptors.

These claims were extracted but no security audit was performed.

**Unrouted — informational:**

- **C8:** Runtime start/owner-death race.
- **C13:** Whether portable fixtures actually ran on both platforms.
- **C14:** Historical equivalence of Linux cleanup behavior.
- **C15:** Completion and adequacy of regression measurements.
- **C16:** Operating-system descriptor semantics.
- **C38:** Actual recovery of SIGSTOPped helpers without owner restart.

### Pass 2 — Decision Soundness & Reversibility

**Mode: internal-soundness.**

**Appropriateness:** No mismatch found against the artifact’s stated ownership, bounded-startup, and uncertain-cleanup constraints.

- **C18:** Rejecting echoed tokens and matching initial offsets is consistent with requiring continuity of the acquired descriptions.
- **C42:** Acknowledging potentially unbounded reap is consistent with bounded observation plus retained ownership and blocked admission.
- **C50/C51:** Reopening the ownership decision before adopting a different topology is consistent with the document’s prohibition on silently weakening proof.

**Reversibility:**

| Decision-bearing claims | Door classification | Assessment |
| --- | --- | --- |
| C1, C2, C19–C21, C23 | Two-way | Internal ownership, reference construction, lifetime, and binding mechanisms can change without a new external contract. |
| C10, C11, C17, C27, C30, C31, C36, C43, C46, C48, C49, C52, C53 | Two-way | Exchange implementation, serialization, and token management are internal mechanisms. |
| C5–C7, C9, C25, C28, C29, C32–C35, C37, C39–C41, C44, C45, C47 | Two-way | Startup/control ordering, teardown budgets, and cleanup tracking are internal implementation choices. |
| C12 | Two-way | The scoped choice is one private, paired helper protocol; no independently deployed consumer contract is asserted here. |
| C18, C42 | Two-way mechanisms informed by rationale | These observations justify the exchange and teardown choices; they introduce no additional irreversible commitment. |
| C50, C51 | Two-way | Returning to design review preserves optionality; alternative ownership topologies are explicitly uncommitted. |

No missing reversibility justification or P1→P0 escalation applies.

**Provenance:** No inconsistency found. The claims are first-hand artifact assertions (`harvested`); none is explicitly reverse-engineered or invented content presented as independently ratified policy.
