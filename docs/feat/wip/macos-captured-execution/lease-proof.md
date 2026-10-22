# Acquired lease references

> Task 3 complete: implementation, Linux verification and independent review, 2026-10-22.
> Baseline: `c833fff220fae4b8a303f61face9bc58632946fb`.
> Task 4 transfer identity and native Darwin verification remain pending.

## Acquisition boundary

`runstore.Open` authenticates storage while holding the lifecycle lock. Only a
successful **shared** attachment creates the private `leaseAcquisition` context.
Exclusive attachments used for policy updates/reclamation cannot claim a launch
owner or lend launch references. `ClaimOwner` pins that context while acquiring
an **exclusive** owner-slot lock and publishing its generation. A successful claim
creates an opaque `OwnerLease`, bound to the exact acquisition, slot and generation.

`Store.BorrowLeases` validates that binding and returns opaque `LeaseRefs`.
There is no constructor accepting caller-provided files. Zero handles, fabricated
owner metadata, changed generations/slots and handles from another attachment
are rejected. An independent open of the same inode, whether unlocked or held in
the wrong mode, cannot be imported into a production launch reference. File mode,
inode and expected-path checks remain in `ValidateLeaseBinding` and are repeated
before use.

`execution.Launch` now accepts only `*runstore.LeaseRefs`. The service and stored
recovery fixtures use this boundary. Its private `launch` implementation still
receives raw descriptors, as do helper-side `Leases` and the existing synthetic
backend fixtures. Only the public wrapper calls the private launcher in production;
helper-side inherited lock validation and store/generation/policy binding remain
unchanged. Protocol v1, schema 8 and Linux-only execution admission remain in force.

## Lifetime and trust

Owners, borrowers, active file callbacks and exchange tokens retain references to
the same acquired descriptions. Store shutdown stops new claims/borrowing before
closing database connections and checkpointing. Owner close stops new borrowing
through that owner. Existing borrowers remain usable; owner handles also retain
the lifecycle lock if the store closes first. The last reference closes the file.
No release issues `LOCK_UN`, converts a lock, or reopens a lease path as a substitute.
Repeated close on copied handles releases the shared handle only once.

`LeaseRefs.WithFiles` is a narrow trusted callback for launch/exchange code. It
pins both descriptions across concurrent close and validates the requested path
and generation. Its caller must not close, unlock, convert, or retain the files;
only controlled helper inheritance is allowed. Offsets must remain untouched
outside the future gated exchange. This is a boundary between cooperating Capy
components, not protection against arbitrary same-user code or unsafe Go access.
An open-file identity exchange cannot establish that a lock was acquired: Task 3's
controlled construction supplies that provenance.

Apple's [flock contract](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/flock.2.html)
describes inherited/duplicated descriptors as references to one lock and explains
that an explicit child unlock also removes the parent's protection. The implementation
keeps the existing pinned `golang.org/x/sys v0.43.0` calls and adds no module or
native API. Source contracts alone do not establish native Darwin test results.

## Exchange gate

Every owner and borrower under one lifecycle acquisition shares one cancellable
gate. This includes different owner slots claimed through the same Store: they
share the lifecycle offset even though their owner descriptions differ. Separate
attachments have separate lifecycle descriptions and gates.

`AcquireExchange` pins both descriptions while waiting and while its token is
held. Cancellation releases the waiting pin. Copies of a token share one-time
release state, so releasing an old token cannot return a later exchange's gate.
Neither closing a borrower nor closing the Store releases an outstanding token.
The token's owner must call `Release` only when no actor can still mutate offsets.

Task 3 does not acquire this gate on the production v1 launch path, exchange
offsets, add helper messages, or change pre-start teardown. Those are Task 4 work;
Task 8 later binds protocol v2 into ordinary Linux execution.

## Verification

Host: Linux x86_64, kernel `7.1.1-76070101-generic`, Go 1.27.2. All test commands
use synthetic `CAPY_DB_KEY=test-key-for-development`, `CAPY_VAULT_KEY=test-key`,
`CGO_ENABLED=1` and isolated temporary stores. Routine commands use encrypted
Capy run capture; no routing exception was applied.

| Check | Evidence |
| --- | --- |
| Pre-change full affected-package race/integration suite | Passed: runstore 83.090s, execution 34.900s; captured run `96dab4c7-cfed-4d94-914a-0083dfc34566`. |
| First focused candidate run | Failed: fixture read stderr before joining its writer, plus missing `errors` import in the adapted execution fixture; run `47ba46b0-42b1-4346-a6a4-b54a48d6da1a`. Both corrected without changing assertions. |
| Focused run after stderr fix | Runstore passed 9.201s; execution still reported the missing import before its correction; run `fe626db1-e6c4-44b4-8ffa-24e63c4caa4e`. |
| Full affected-package race/integration and vet | Passed: runstore 91.833s, execution 34.978s, vet exit 0; captured run `18d80f11-e4e4-4837-a3ea-fc63da1a5e84`. |
| Selected public consumer race tests and full FTS5 build | Passed: server 8.896s, CLI 20.490s, build exit 0; captured run `42d2d601-a273-46be-beed-6b1ae5375109`. |
| Independent review | Isolated `kk:review-code`, gpt-6.1-sol: APPROVE, no findings across all 14 changed files and relevant unchanged consumers. Source review only; test results author-attributed. PAL code review unavailable. |

Full affected-package command:

```sh
go test -tags fts5,integration -race -count=1 -p 1 -timeout 20m ./internal/runstore ./internal/execution
go vet -tags fts5,integration ./internal/runstore ./internal/execution
```

Public consumer/build checks:

```sh
go test -tags fts5 -race -count=1 -p 1 -timeout 10m ./internal/server ./cmd/capy -run '^(TestCapturedRunRecoveryAndSafeDefault|TestParallelBatchSharedCapacity|TestRunCLICrossInterfaceRecovery|TestRunCLIExitAndCaptureMatrix|TestMCPRunCaptureRecovery|TestRunCLIResetRefusesLiveMCP)$'
go build -tags fts5 ./...
```

These exercise captured recovery, shared parallel capacity, cross-interface
recovery, CLI outcome handling, MCP capture, and reset refusal with a live MCP.
`git diff --check` also passed. Results are implementation-session observations;
independent source review does not itself rerun or certify the commands.

`lease_refs_test.go` covers fabricated/wrong-binding/closed handles, exclusive
lifecycle rejection, concurrent borrowers, copied-handle close, callback pinning,
shared gate identity across owner slots, independent attachments, cancellation,
one-time token release, and real reset/slot-lock probes.
`lease_refs_integration_test.go` re-execs the test binary with the acquired
descriptions, closes every parent reference and verifies reset/slot exclusion
until the helper exits. It checks the distinct EX/SH modes and unchanged permanent
file contents, permissions and inode identity. Existing stored execution fixtures
exercise the narrowed launch boundary with real capture and helper/owner death.

No executor, retrieval, indexing or chunking algorithm changes are made; repository
quality/performance benchmark triggers do not apply to this acquisition-only slice.
CLI flags, public envelopes, generated artifacts and CI configuration are unchanged;
their release updates remain Tasks 7 and 10–13.

## Outstanding platform evidence

Owner: feature implementer. Native macOS 26 arm64 verification is unavailable on
this Linux host. Task 4 must run the portable reference/inheritance fixtures above
alongside the common exchange fixtures on both OS families, record actual
OS/kernel/SDK/toolchain versions, and confirm reset/slot exclusion and unchanged
permanent lock modes/bytes. Neither this report nor Linux success closes that
two-platform lease-proof gate. Public Darwin activation remains dependent on the
later private integration and native acceptance tasks.
