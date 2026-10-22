# macOS captured execution: isolated verification

> Status: source verification complete; design written; runtime feasibility unproved
> Date: 2026-10-22
> Repository baseline: `44ceb813ce2d4f129d058f2534faa2a57b07d8bd`
> Method: `kk:chain-of-verification:isolated`, four independent agents
> Scope selected by user: Apple Silicon first; direction A, native process-group supervision with explicit macOS cleanup limits
> Related feature: [routine command capture](../../done/routine-command-capture/design.md)

This report is evidence for [the design](design.md), not an implementation,
independent design review, or successful runtime proof. All four agents started
without the initial answer or curated search results. They performed read-only
research. No tests, builds, or executable probes ran. Repository code is unchanged.

The local host reports macOS 26.6.2, build 25G83. Published XNU `main` sources
were inspected but were not matched to this kernel build. The newer Endpoint
Security API described below is declared for macOS 27.0 and its inspected Apple
documentation is marked beta. Neither source inspection nor SDK declarations
establish behavior on the local host.

## Initial answer

A native Darwin supervisor appears to be the most practical route for Apple
Silicon. It would retain encrypted capture, bounded output, cancellation,
deadlines, and cleanup after the owning Capy process dies. Its cleanup scope
would exclude descendants that leave the command's process group; Linux would
retain its stronger descendant handling.

The proposal depends on safely determining when the Darwin process group has
finished and on preserving and validating storage locks across helper processes.
The existing recovery code must distinguish the narrower cleanup scope rather
than treating it as the current Linux proof. Extra descendant tracking may
improve coverage, but has not been shown to establish complete ownership.

The initial recommendation was A over B (additional best-effort descendant
tracking) and C (full Linux-equivalent proof before enabling macOS). This was
a direction recommendation, not a claim that a small backend port had been proved
feasible. The following verification strengthens that distinction.

## Verification questions and independent answers

### Q1: What can Darwin prove about process-group termination?

**Agent:** `/root/cove_darwin_groups` — default/general-purpose, isolated context

**Status:** completed; source citations supplied; no runtime verification

Darwin provides non-reaping direct-child observation through `waitid` with
`WNOWAIT`. The verifier requires checking the returned PID and terminal status,
not merely a successful syscall. Go records a historical Darwin stopped-child
reporting issue. Retaining the direct child, with exactly one reaper and normal
child-retention semantics, prevents unrelated reuse of its PID. XNU's allocator
also checks process-group and session identifiers.

The existing [Darwin wait helper](../../../../internal/executor/wait_darwin.go)
overstates what a kqueue registration `ESRCH` establishes. XNU can make an exiting
process unavailable to `proc_find` before potentially blocking exit cleanup and
before it becomes a waitable zombie. `NOTE_EXIT` also precedes that zombie
transition. Neither condition alone establishes that the subsequent `cmd.Wait`
will complete immediately.

Group signalling snapshots membership; it does not atomically contain forks or
wait for every member to terminate. A successful group signal, pipe EOF, and
leader exit are individually insufficient retirement evidence. In particular,
a process can close both output descriptors and continue running. A zero signal
is not a reliable empty-group predicate while the retained leader is a zombie.

A candidate stronger check retains a terminal leader, repeatedly signals its
original group, and requires a complete group enumeration containing only that
leader. This remains **unproven**: fork/spawn publication can involve group/hash
insertion before insertion into the enumerated process list, and a descendant
that left the group but remains in the same session can later rejoin. The design
must settle these conditions rather than promote repeated observations into an
atomic retirement guarantee.

Sources:

- [Apple wait definitions](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/sys/wait.h)
- [Go's Darwin waitid caveat](https://github.com/golang/go/blob/master/src/os/wait_waitid.go)
- [XNU PID allocation](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/kern_fork.c)
- [XNU exit ordering](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/kern_exit.c)
- [XNU kqueue implementation](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/kern_event.c)
- [XNU group iteration](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/kern_proc.c)
- [XNU group signalling](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/kern_sig.c)
- [XNU process enumeration](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/proc_info.c)
- [Apple pipe semantics](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/pipe.2.html)

### Q2: What lock lifetime and ownership validation are available?

**Agent:** `/root/cove_darwin_locks` — default/general-purpose, isolated context

**Status:** completed; source citations supplied; passive ownership inspection inconclusive

Darwin `flock` has the needed reference lifetime: duplicated/inherited references
share one lock, and parent death does not release it while a surviving helper
retains a reference. Explicit unlock or conversion affects the shared lock;
conversion can release the prior mode. The initial helper exec must receive the
intended descriptors, and the helper must prevent their inheritance by command
children.

No verified supported public API was found that proves an incoming descriptor
currently owns the required shared/exclusive flock without modifying it. This is
a scoped research conclusion, not proof that no private facility exists.

- Matching inode/device proves the file, not the lock-owning open-file description.
- Descriptor-sharing metadata does not mean a shared flock.
- The historical `FWASLOCKED` flag is not current lock-mode evidence.
- `F_GETLK`/OFD conflict queries can observe conflicting flocks but do not identify
  the supplied descriptor as their owner. OFD record-lock and flock identities
  are deliberately distinct in XNU.
- Calling `flock` with the expected mode is acquisition/conversion, not passive
  validation. It may accept an unlocked descriptor or alter a wrong-mode lock.

The existing [Linux validator](../../../../internal/execution/supervisor_linux.go)
and [store binding check](../../../../internal/runstore/owners.go) establish
different facts. Replacing the former with the latter loses provenance. Permanent
lock inodes also survive reset and generation reuse, so inode equality does not
establish continuity of the database or generation.

Two protocol alternatives deserve design analysis, not immediate adoption:

1. A helper opens and acquires its own lifecycle and owner leases before admission.
   This makes acquisition authoritative, but changes ownership placement: a new
   per-run helper cannot independently acquire an exclusive slot already held
   through the coordinator's separately opened description.
2. A release/reacquire handoff establishes a real bridge first: helper-held shared
   lifecycle protection against reset and a registry barrier respected by every
   slot reclaimer. After acquisition, revalidate the current store, generation,
   admission, and quarantine. Parent death before that bridge loses continuity;
   old work cannot simply continue. Existing duplicate holders complicate transfer.

Sources:

- [Apple flock semantics](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/flock.2.html)
- [XNU fcntl documentation](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/man/man2/fcntl.2)
- [XNU descriptor locking](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/kern_descrip.c)
- [XNU final-close locking](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/vfs/vfs_vnops.c)
- [XNU file flags](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/sys/fcntl.h)
- [XNU lock identity conversion](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/sys/file_internal.h)
- [XNU conflicting-lock lookup](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/kern_lockf.c)

### Q2 follow-up: Can shared offsets establish descriptor continuity?

**Agent:** `/root/cove_darwin_locks` — same isolated verifier; completed

After the user accepted the scope and completion contract, the root agent asked
an additional open question: can coordinated observations of shared regular-file
offsets establish that the helper received the exact descriptions on which a
cooperating coordinator already acquired the required locks? The question supplied
the candidate protocol and threat assumptions, but no expected answer or initial
recommendation. No implementation or runtime probes ran.

The verifier found this **defensible under explicit conditions**. Duplicates and
inherited descriptors share an offset; an independent open has its own offset.
XNU's `lseek` changes the offset on the same `fileglob` identity used by flock.
Whole-file flock coverage does not depend on that offset. The handshake need not
write file contents or acquire, convert, or release any lock.

The evidence must consist of causally ordered, locally observed changes in both
directions. A coordinator changes one offset, the helper observes it, the helper
changes its descriptor's offset while the coordinator makes no changes, and the
coordinator observes that change on its own descriptor; a reverse change is also
checked. Perform the exchange for each lease role. Matching initial values or
echoing expected tokens is not sufficient. All offset writers must be serialized,
with strict exchange/role/phase binding and bounded private frames.

**This proves shared description identity, not current lock mode.** Two aliases
of the same unlocked or wrong-mode description pass it. Production must obtain
the exact coordinator descriptions through successful owner/lifecycle acquisition
and retain that provenance and lifetime, with no unlock or conversion by any
alias. The current generic `Launch(..., Leases{*os.File, *os.File})` does not itself
encode that guarantee. Source inspection of acquisition and an enforceable
construction boundary are part of the proof; a parent saying "locked" is not a
replacement. Store/slot inode, generation, and policy checks remain necessary.

The important concurrency obligation is **gate lifetime after cancellation**.
A stopped or timed-out helper can later resume an offset operation. The shared
exchange gate cannot be released until that helper has conclusively left the
offset-mutating phase or has been joined. Otherwise later handshakes can be
corrupted. Keep the gate unavailable and refuse new admission when necessary;
do not turn a bounded response timeout into proof that the old actor has stopped.

Once verified, no release/reacquire gap is introduced: the helper already owns
references to the coordinator's locked descriptions. Parent death closes only
the parent's references. A complete start frame may nevertheless be buffered at
death, so existing liveness cancellation must handle a spawn racing owner loss.

Required native fixtures include independent same-inode descriptors (including
separate shared holders), wrong acquisition mode, role swaps, real helper exec,
close-on-exec isolation, malformed/replayed/reflected frames, late resumed helpers,
parent/helper death at each phase, concurrent reset/slot claims, and unchanged
permanent-file contents and size. The same-description-unlocked case must
explicitly demonstrate the distinction between identity and acquisition proof.

This is the preferred **candidate** for preserving the current per-run topology,
subject to the lock feasibility gate. Independently acquired helper leases and
serialized ownership transfer remain alternatives if it fails; neither is an
automatic fallback that may change the design unnoticed.

Sources:

- [Darwin duplicate descriptor semantics](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/man/man2/dup.2)
- [Darwin fork inheritance](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/man/man2/fork.2)
- [Darwin exec descriptor preservation](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/man/man2/execve.2)
- [XNU seek implementation](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/vfs/vfs_syscalls.c)
- [XNU whole-file flock implementation](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/kern_descrip.c)

### Q3: What mechanisms cover detached descendants?

**Agent:** `/root/cove_darwin_descendants` — default/general-purpose, isolated context

**Status:** completed; source citations supplied; no ordinary-CLI subreaper equivalent established

No documented equivalent of Linux subreaper ownership was established for an
ordinary, unentitled native CLI. Ordinary waits operate on the caller's children;
parent exit reparents survivors to `initproc`. Kqueue supplies process observation,
not adoption or reaping ownership. Its automatic child-tracking flags are
unsupported, fork notifications can coalesce, and they do not deliver the child
PID. Libproc ancestry traversal observes current relationships and can miss
intermediate processes that disappear between scans.

Two qualifications prevent overly broad claims:

- Darwin has `proc_signal_with_audittoken`, which can validate identity when
  signalling. Therefore “Darwin only has unsafe numeric-PID signalling” would be
  wrong. The interface is in the private-labelled libproc header, and identity
  handling across exec and supported releases requires validation. This does not
  solve missed descendant discovery or confer wait ownership.
- `es_new_descendants_client` is declared for macOS 27.0. Its inspected beta
  documentation describes recursive descendant events without root or TCC
  approval, but still requires Apple's restricted Endpoint Security entitlement.
  ES events can be dropped; sequence numbers detect gaps. This is a specialized
  observation path, not a verified atomic terminate-and-reap-subtree mechanism,
  and is not available on this session's macOS 26.6.2 host.

Launchd's documented cleanup concerns the job's process group; service
registration does not establish escape-proof descendant ownership. Coalition
control needs privileged membership, and coalition termination is not itself
“kill all members.” Tracing/instrumentation needs its own proof; deprecated
sandbox APIs are not an adoption mechanism. A VM changes the execution
environment and cannot establish ownership of native host descendants.

Sources:

- [Apple process-event definitions](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/sys/event.h)
- [Apple libproc API header](https://github.com/apple-oss-distributions/xnu/blob/main/libsyscall/wrappers/libproc/libproc.h)
- [Apple libproc wrappers](https://github.com/apple-oss-distributions/xnu/blob/main/libsyscall/wrappers/libproc/libproc.c)
- [Apple launchd contract](https://github.com/apple-oss-distributions/launchd/blob/main/man/launchd.plist.5)
- [Apple descendant ES client](https://developer.apple.com/documentation/endpointsecurity/es_new_descendants_client(_:_:))
- [Apple restricted entitlement provisioning](https://developer.apple.com/forums/thread/791996)
- [Apple ES event-loss documentation](https://developer.apple.com/documentation/endpointsecurity/es_message_t/seq_num)
- Local SDK: `/Library/Developer/CommandLineTools/SDKs/MacOSX.sdk/usr/include/EndpointSecurity/ESClient.h`, declarations at inspected lines 701 and 798

### Q4: Which existing behaviors depend on the cleanup proof?

**Agent:** `/root/cove_cleanup_contract` — default/general-purpose, isolated context

**Status:** completed; repository citations supplied; no tests run

The present proof covers all supervisor-owned descendants and output pipes,
including adopted children that escaped the original process group. It is not
kernel containment: forced supervisor death can leave commands alive. The
pre-spawn `Retired=true` case is valid because no command has started.

The proof is materially coupled to storage and public results:

- [StreamResult](../../../../internal/executor/stream.go) carries local retirement
  separately from command status and capture loss. Rust compilation must retire
  before its execution phase proceeds.
- [Outcome](../../../../internal/execution/protocol.go) distinguishes that local
  proof from durable acknowledgement. Lost terminal IPC can be resolved from
  persisted proof; pending or unknown proof causes quarantine.
- [Finish and outcome mapping](../../../../internal/runstore/runs.go) use retirement
  to determine complete capture, interrupted state, the cleanup column, and the
  database terminal bit. `capture.terminal_persisted` alone is not proven cleanup.
- [Recovery](../../../../internal/runstore/recovery.go) requires both lease
  exclusion and valid persisted proof. A free lock is insufficient; pending
  cleanup can block admission store-wide. Recovery never kills persisted PIDs.
- [Retention](../../../../internal/runstore/retention.go) and
  [batch completion](../../../../internal/runstore/batch.go) depend on terminal
  evidence. Unknown cleanup must not disappear through ordinary expiry.
- [Reset](../../../../internal/runstore/reset.go) is explicit discard under
  exclusive lifecycle ownership. It does not prove that all old descendants
  stopped; operator quiescence remains necessary after unverified cleanup.
- [MCP success](../../../../internal/execution/response.go) and
  [CLI exit mapping](../../../../internal/execution/policy.go) depend on the
  resulting process and capture states.

At the inspected baseline, storage schema version is 8, the private protocol is
version 1, and public run results are version 1. The schema permits only
`not_spawned`, `pending`, and `retired` cleanup values. There is no persisted or
public field expressing cleanup scope. An OS label or display warning alone
cannot change the old recovery SQL's meaning.

A group-only backend therefore cannot truthfully reuse the existing unqualified
proof. Its scope and status must be distinguishable across restart and
compatibility boundaries, with explicit decisions for normal reuse, retention,
batch completion, and public success. The exact representation and version
transition remain design work.

Ordinary command execs receive no private lease/control/credential FDs; the
helper sets them close-on-exec and passes no `ExtraFiles` to commands. The pinned
SQLite implementation also uses close-on-exec for its descriptors. This is
descriptor hygiene, not isolation against arbitrary same-user access to files or
credentials already inherited in the environment.

Existing tests relevant to the future implementation:

- [Supervisor integration](../../../../internal/execution/supervisor_integration_test.go): escaped descendants, capture/finalization failures, private FDs, lost supervision.
- [Stored recovery integration](../../../../internal/execution/stored_recovery_integration_test.go): owner SIGKILL, delayed helper close, forced helper death and quarantine.
- [Recovery tests](../../../../internal/runstore/recovery_test.go): pending/mixed proofs and reuse.
- [Retention/reset tests](../../../../internal/runstore/retention_test.go): preserving unknown evidence and exclusion.
- [Policy/version tests](../../../../internal/runstore/policy_test.go): incompatible storage rejection.
- [CLI response tests](../../../../cmd/capy/run_cli_test.go): process/capture exit mapping.

## Reconciliation

This table separates the baseline's claims from additional implementation
shortcuts examined during verification. A contradicted shortcut is not a claim
that the initial answer had already recommended it.

| Claim or candidate assumption | Verification | Result | Required correction or action |
| --- | --- | --- | --- |
| Darwin can retain direct-child identity while observing exit | Q1 | Confirmed with conditions | Use one reaper and validated terminal wait status; do not equate a notification with waitability. |
| Existing kqueue exit observation is enough for a bounded final wait | Q1 | Contradicted | Existing ESRCH/NOTE_EXIT assumptions need correction before reuse in captured execution. |
| Process-group signalling plus EOF can certify group retirement | Q1 | Contradicted as a sufficient proof | Require a separately justified retirement certificate; signalling and pipe closure are independent observations. |
| A small native backend is already known to satisfy A's cleanup promise | Q1, Q2 | Inconclusive | Keep group retirement and lease ownership as prerequisite feasibility gates. |
| Inherited Darwin flocks can survive coordinator death | Q2 | Confirmed | Preserve references through cleanup and backend close; never explicitly unlock shared descriptions. |
| Linux's incoming-lock validation can be directly ported | Q2 | Inconclusive; no suitable public query found | Design explicit acquisition/transfer ownership, not an inode-only substitute. |
| A shared-offset exchange can retain the current helper topology | Q2 follow-up | Supported under cooperative acquisition/lifetime/serialization assumptions; untested | Prove identity causally, preserve trusted acquisition provenance, and retain the gate until late helpers cannot mutate offsets. |
| Ordinary descendant scanning does not establish Linux subreaper ownership | Q3 | Confirmed within inspected interfaces | Keep B as best-effort coverage, not a stronger certified contract. |
| Native descendant monitoring necessarily requires root | Q3 | Contradicted | New macOS 27 ES descendant client does not require root/TCC, but is entitlement-gated and not a proved cleanup primitive. |
| Darwin has no identity-aware signalling | Q3 | Contradicted | Such an interface exists; its availability/private status and discovery limits still matter. |
| A needs a persisted, explicit cleanup-scope distinction | Q4 | Confirmed | Version and apply the distinction in recovery, reuse, retention, batches, and response semantics. |
| Encryption/capture-before-spawn and Linux behavior can remain intact | Q4 | Compatible design goal, not tested | Preserve existing paths and verify parity; no implementation evidence yet. |

The root agent searched `kk:` for the inconclusive/corrected Darwin wait, lock
transfer, and cleanup-proof subjects after receiving verification. No relevant
project-specific evidence resolved these gaps. The kqueue/EOF shortcuts and broad
root/identity claims in the table are additional issues identified by the
verifiers; they were not premises of the selected A recommendation.

## Revised direction and outstanding work

A remains the selected design direction: native execution with a deliberately
narrower macOS cleanup scope. **It is not yet a proven process-group retirement
algorithm or a direct port of the Linux supervisor.** The design must start with
the following explicit proof and contract work:

1. Establish bounded Darwin waitability and an honest group-cleanup certificate.
   Audit creation/publication races and group re-entry; run adversarial native
   fixtures on declared supported OS versions. If the promised certificate cannot
   be established, stop and revise the contract explicitly rather than treating
   successful signalling as completion.
2. Select and prove lease acquisition provenance and reference validation before
   helper storage access or command spawn. The shared-offset candidate preserves
   the coordinator's existing admission; it must finish before the helper uses
   inherited leases as authority. Cover coordinator
   death on each handshake boundary, concurrent reset, slot reuse, malformed
   descriptors, multiple active runs, and helper loss. Keep the current permanent
   locks and unknown-cleanup quarantine authoritative throughout the transition.
3. Specify persisted proof scope/status, compatibility/version handling, and
   consequences for completion, reuse, retention, batch results, and CLI/MCP
   success. Preserve existing Linux proof semantics and legacy-row interpretation.
4. Repair the directly relevant pre-existing `wait_darwin.go` waitability assumption
   when implementing the bounded Darwin waiting path. It is recorded here because
   this verification was read-only; the next step is a terminal-state regression
   fixture and a bounded, validated wait implementation, not a relaxed assertion.
5. Include capability reporting, native macOS CI, end-to-end capture/recovery and
   failure fixtures. Do not advertise a supported backend based on compilation.

The implementation also needs an explicit test-bootstrap procedure: the current
macOS capture path rejects execution, while installed Codex routing requires
capture for recognized routine commands. Native test/build execution requires an
operator-authorized routing exception or another approved capture route. This
design session did not change routing or run native tests around that gate.

## Scope carried into design

- Apple Silicon first; minimum macOS version and CI matrix remain to be specified.
- Native project commands, shared encrypted run storage and bounded recovery;
  preserve Linux behavior and the existing security/credential boundaries.
- Full detached-descendant cleanup is outside A's promise. Best-effort tracking
  must not be mistaken for proof; it is not selected for the initial backend.
- No VM, persistent job daemon, privileged service, or restricted entitlement is
  selected. The newer ES API is evidence affecting alternative C, not an adopted
  dependency.
- Group-retirement and lock-protocol work are prerequisites, not exclusions or
  silently deferred functionality. Implementation completion and support claims
  remain pending until those obligations are addressed. The approved design
  sections are now recorded in `design.md`, with the gates assigned in `tasks.md`.
