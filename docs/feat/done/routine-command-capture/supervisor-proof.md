# Task 1: supervisor ownership proof

This record describes the Task 1 baseline. [Task 2](task2-verification.md) adds the
production encrypted backend and synchronous MCP wiring to the seams proved here.

Task 1 adds a private, dormant supervision path. Public MCP execution still uses
the existing executor. `execute-supervisor` is hidden and rejects execution until
Task 2 supplies an authenticated `OpenRun` backend. The subprocess fixtures
re-execute the same test binary through `ServeInherited`, using a synthetic
backend; a separate compiled-CLI test checks the hidden entry and missing-FD
rejection. This is a process/lease proof, not an encrypted run-store acceptance
test or approval of the pending feature tasks.

## Ownership and protocol

`internal/execution` launches one helper per run without a shell. Five deliberate
descriptors carry control/liveness, the captured credential, lifecycle notices,
the exclusive owner lease, and the shared lifecycle lease. The private startup
record contains run/generation IDs and the store locator; source code belongs to
the admitted backend. The helper checks descriptor kinds, modes, distinct lock
inodes, inherited lock ownership, protocol version, bounded frames, generation
admission, and start authorization before recording pending and spawning.
Lock validation reads at most 4 KiB of Linux fdinfo and requires the incoming
description's own whole-file FLOCK; it never acquires a lock to validate it. Linux
[show_fd_locks](https://github.com/torvalds/linux/blob/v6.12/fs/locks.c)
filters flock entries by their open-file description.

Frames have a four-byte length and at most 16 KiB of JSON. Startup admission is
bounded to three seconds; notices/control writes to 250 ms. The credential never
enters argv or a newly introduced environment variable. Existing executor child
environment behavior is preserved, including credentials already inherited in
the environment. Every private descriptor is close-on-exec and command children
receive no ExtraFiles. The coordinator alone retains the liveness write end.
After helper creation, failed startup returns the helper handle with its error.
The owner must retain and join it independently: a stopped helper cannot satisfy
a bounded admission response and an immediate join at the same time. Closing
startup pipes requests cleanup; it does not authorize retry or lease reuse.

The helper closes inherited lease references after joining pipe readers and the
control reader, recording terminal state, and closing the backend. Neither helper
nor coordinator may use `LOCK_UN` to release a shared open-file description;
each closes its references. The kernel keeps those locks held through owner
SIGKILL while the helper retains its references. This follows Linux's
[flock open-file-description semantics](https://man7.org/linux/man-pages/man2/flock.2.html).

## Linux retirement proof and limits

The new path supports Linux with usable procfs and local flock semantics. It
enables [PR_SET_CHILD_SUBREAPER](https://man7.org/linux/man-pages/man2/PR_SET_CHILD_SUBREAPER.2const.html)
in the dedicated helper before spawn. No unrelated children or other reapers may
run in that process. Other platforms reject the new path before spawning; legacy
execution remains available through its existing interfaces.

Each compiler/runtime phase owns a process group. WNOWAIT reserves its leader's
PID through the last group signal. After reaping that leader, the helper repeatedly
reads its live direct-child inventory, checks wait ownership, kills adopted
children and reaps them until ECHILD. This also covers descendants that call
setsid. It never signals a PID loaded from persisted metadata. Pipe readers must
reach EOF before retirement is certified. Cancellation observation, descendant
retirement and pipe draining are bounded; an inaccessible/oversized child
inventory, lost wait ownership, stuck kernel process or missing EOF yields an
unverified outcome rather than successful cleanup.

This is not a kernel cgroup containment guarantee. Forced supervisor SIGKILL can
leave a command alive; inherited locks then disappear. The already-persisted
pending proof must quarantine that generation/store. Only known not-spawned or
retired evidence permits normal reuse. Actual store-wide reconciliation belongs
to Task 7 and explicit operator-quiescent reset to Task 9. Those tasks must not
replace the proof check with lock availability, heartbeat or a persisted PID.
Arbitrary same-user interference with the supervisor or its storage is outside
the cooperating-process proof.

## Event and storage seams

`ExecuteStream` emits owned byte slices of at most 16 KiB, sequence numbers,
stream labels and compile/run phase labels. The sink is serialized across pipe
readers. An append failure stops further append attempts while both streams keep
draining; it does not cancel the program. Rust compilation uses the same process
and stream lifecycle as execution. The original executor shares runtime/source
preparation but keeps its current public output and background behavior.

`CaptureStore.Pending` must commit cleanup=pending before allowing process
creation. `Finish` may commit cleanup=retired only after `Result.Retired`; an
error leaves `TerminalPersisted=false`. Backend operations must honor their
bounded-operation contracts. The synthetic fixture writes only test data and
proof states; production introduces no plaintext output spool.

Task 2 must implement authenticated generation/program lookup and durable proof
transactions in encrypted runstore, then inject that backend into the hidden
CLI entry. The proof launcher limits its synthetic credential to 4,096 bytes;
Task 2 must reconcile private transfer bounds with ADR-032's environment-key
contract before production admission (including long-key validation before
metadata/filesystem effects). Task 3 must add the bounded queue, append deadlines and complete
capture/result semantics; the current synchronous sink is only the lifecycle
seam. Task 6 owns transport handoff. Tasks 7/9 own real store reuse/reset.

## Verification

The focused subprocess suite and focused race suite pass with FTS5 and synthetic
keys. Fixtures isolate HOME and XDG configuration/data/cache paths. Integration
fixtures use the `integration` build tag and must be included explicitly:

```sh
GOCACHE=/tmp/capy-go-build CAPY_DB_KEY=test-key-for-development CAPY_VAULT_KEY=test-key \
  go test -race -tags fts5,integration -count=1 ./internal/execution ./internal/executor
```

Covered observations:

- Output arrives before command completion and includes late stderr/stdout;
  a 1-MiB binary stream remains exact with bounded event payloads.
- Private descriptors do not reach commands; the transferred credential is absent
  from helper argv/environment. Malformed/oversized/partial input, wrong generation,
  empty credentials, missing authorization, bad lease ownership and absent helpers
  cannot spawn the fixture marker.
- Explicit cancel, execution deadline and owner EOF retire descendants; normal
  leader exit also retires descendants that created a separate session.
- Owner SIGKILL while the helper is stopped keeps owner-slot reuse and exclusive
  lifecycle/reset probes blocked. After resume, those probes remain blocked through
  a deliberately held backend Close, even after retirement is recorded.
- Forced helper loss returns unknown cleanup, leaves pending evidence, and the
  fixture opener refuses that generation even after locks become free.
- Append failure still reaches the command's final marker; finalization failure
  retains pending proof; broken notice IPC does not assert terminal receipt.
- Compiler success, nonzero exit and cancellation use the same event and child
  retirement path. Compiler fixtures are deterministic synthetic runtimes.

The compiled CLI hidden-entry/missing-descriptor test and Go vet pass. The new
execution package also cross-compiles for Darwin/arm64 with its rejection stub;
this is not a Darwin runtime proof. No installed Codex activation is claimed.

Quality benchmarks ran against archived source from baseline commit
`595018f1c54ad5dc4ad3b0a7b57a8d9791b65e0b` and the working tree, using separate
`BENCH_BRANCH=routine-task1-before/after` names so the existing master result was
preserved. `qualstat` verified identical fixture hashes and zero differences in
all retrieval-quality/context-reduction metrics. The source archive lacks Git
metadata, so its JSON labels the commit unknown; the baseline above records its
provenance. Existing FTS query warnings occur on both sides.

Six 100-ms samples of existing executor overhead/scaling benchmarks also passed.
Median shell execution was about 1.00 ms before and 1.07 ms after; 100-KiB output
was 7.63/7.57 ms and 1-MiB output 60.86/59.56 ms. Allocation sizes remained about
138 kB, 635 kB and 6.94 MB respectively. These short local samples are a smoke
comparison, not a statistically established performance regression/improvement;
benchstat is unavailable. End-to-end captured-run performance remains Task 17.

The independent code reviewer approved the corrected implementation. The second,
external reviewer could not run because its provider rejected the configured API
key. See [review findings and corrections](task1-code-review.md).

The full FTS5 suite passed every package except `internal/config` and
`internal/server`, whose Unix/TCP listener fixtures were blocked by sandbox
permissions. Both complete packages passed when rerun with local sockets enabled
and the same synthetic keys/isolated XDG directories. No code/test assertion was
changed to accommodate those restrictions. The final FTS5 CLI build, focused Go
vet and tracked/untracked whitespace checks also passed.

Task 1 is complete. Its gate permits storage implementation to begin on the
documented Linux mechanism; it does not enable capture routing or mark the
remaining design/release acceptance complete.
