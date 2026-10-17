# Task 2: synchronous capture and recovery

Task 2 connects ordinary synchronous `capy_execute` calls to the Task 1 Linux
supervisor and a separate encrypted run store. The actual capy executable now
supplies the authenticated helper backend. `capy_runs` exposes status and
explicit raw reads; successful command output is recovered by run ID rather than
being executed again.

## Scope and current behavior

Normal execution returns the version-1 run envelope, including actual exit/signal,
capture status and persisted identity. Exit 1 remains a failure even when stdout
is present. A status query for a failed command is a successful query. Default
output views return `redaction_unavailable`; automatic previews and knowledge
indexing never receive raw bytes. Explicit raw reads return base64 fragments with
stream/phase/sequence and byte/line anchors, plus bounded cursors bound to the
store UUID, run and stream. Read pages include the run's capture status so a
retained prefix is not presented as a complete capture.

This slice remains synchronous. Background execution keeps the existing path
until Task 6; execute-file and batch remain on their existing paths until Tasks
11–13. Explicit `wait_ms` is rejected. An intent is recorded, with an explicit
unavailable-index state until Task 10. Existing execute:* knowledge rows retain
their legacy selectors; new synchronous runs create no knowledge or vault rows.

The new path supports the Linux supervision mechanism proved by Task 1. It does
not enable the Codex routine routing gate, advertise pending run actions, or claim
the rest of the feature is ready for release.
The initial catalogue now contains eleven tools. The Codex PreToolUse matcher
generator and committed `.codex/hooks.json` were both extended for `capy_runs`,
preserving the owner ID and external handlers. Broader routing guidance remains
in Task 15; this matcher update enables the existing hook coverage, not the gate.

## Storage and admission

Storage is always `<physical-selected-owner>/.capy/runs/runs.db`, independent of
execution cwd or knowledge `store.path`. Implicit direct serve validates the full
Codex ancestor chain before choosing the nearest bound owner; explicit project
selection preserves the CLI contract. Nested owners and linked worktrees retain
separate run paths even when their knowledge database is shared.

The service captures the selected ADR-032 credential once. The helper receives
that exact credential through a separate bounded anonymous pipe, never through
argv, a new environment variable or an on-disk manifest. Base64 framing preserves
non-UTF-8 bytes and supports every Linux environment-string size; the Task 1
4,096-byte prototype restriction is removed. The control protocol retains its
separate 16-KiB bound. Existing child environment semantics remain unchanged.

Empty/invalid selected credentials fail before creating run artifacts. Existing
stores authenticate before new owner-slot files/rows are created; wrong keys and
corrupt/plaintext stores are never reset or replaced automatically. Owned run
directories use 0700 and files use 0600. Symlinks and special files are rejected;
lock opens use no-follow/nonblocking flags. The shared SQLite URI builder now
escapes literal percent signs, preventing a physical owner containing `%2F` from
selecting a different decoded path.

Schema version 1 contains store identity, fixed owner generations, encrypted
program/result metadata and ordered raw events. Lookup indexes follow run ID,
owner-slot/terminal status and run/stream/sequence access patterns. Programs and
output live only in encrypted SQLite pages; the ordinary executor's temporary
source files keep their existing execution lifecycle. No plaintext run-log spool,
new dependency, external migration framework, knowledge schema or vault schema is
introduced.

Admission commits a run row as its small write probe. The helper authenticates,
checks generation/deadline and binds inherited lease descriptors to this exact
store/slot, then persists pending before spawn. A real-spawn callback records
running, including compiler phases. Append and finalization use bounded SQLite
transactions; append failure stops capture attempts while execution keeps draining.
Closing joins helpers before pool/lease release and closes each pool before its
final checkpoint. Busy checkpoints from other owners are tolerated.

Permanent owner slots are allocated now because production helpers require real
leases. Unfinished free slots are conservatively rejected; a known unknown helper
outcome blocks further admission locally and requests persistent store quarantine.
Failure to persist that quarantine is reported explicitly. Task 7 still owns
automatic proven-state reconciliation and periodic recovery; Task 9 owns explicit
operator-quiescent reset. Queries remain available during quarantine. No recovery
signals a PID from stored metadata.

## Configuration and deferred integration

The loader validates explicit values and numeric bounds for all proposed `[runs]`
settings, including explicit zero wait, and rejects zero/fractional/overflowing
executor timeouts. Synchronous execution now uses explicit milliseconds or the
configured timeout, whose default is 300 seconds. The legacy preview limit never
truncates stored run bytes.

The per-run byte limit and admission TTL snapshot are used. Full capture queues,
response serialization budgets and exhaustive failure-state mapping remain Task
3. Stateful sanitized projection remains Task 4. Wait/pending scheduling, policy
revision enforcement, retained-data accounting/eviction, recovery, reset and lazy
FTS remain the named later tasks; parsing their configuration now does not claim
those mechanisms are implemented. Existing prototype stores have no promised
migration compatibility before the feature is completed.

## Verification record

Focused checks completed so far:

- Encrypted store reopen, exact binary stdout/stderr paging, exit 7, one-use start
  authorization, missing/empty/wrong credentials, unsafe paths, literal-percent
  owner paths, and unfinished-owner refusal.
- Real MCP execution, status/read and process restart; exactly one command marker;
  selected-key snapshot after its credential file changes; wrong-key preservation
  of the database hash; no knowledge DB created by capture/recovery.
- Default projection refuses raw output, and raw/text, cursor/stream, malformed
  number and unsupported-action combinations fail explicitly.
- Existing cwd, nested-owner, launch, security and boolean-policy tests now recover
  output explicitly where their assertion concerns execution. Valid literal
  true/false strings preserve the existing boolean parser contract.
- Real workspace-write admission with HOME/cache pointing at unwritable procfs;
  an explicitly excluded owner under this repository's protected `.codex` rejects
  before the command's marker is created. This is the native sandbox harness;
  the public capture CLI remains Task 5.
- Focused race tests for runstore/execution/executor and focused vet passed.

The real sandbox harness is opt-in only for its excluded-owner case:

```sh
GOCACHE=/tmp/capy-go-build CAPY_DB_KEY=test-key-for-development CAPY_VAULT_KEY=test-key \
CAPY_RUNS_EXCLUDED_OWNER="$PWD/.codex" \
go test -tags fts5 -count=1 -run TestMCPRunStoreSandboxAdmission ./cmd/capy
```

Use that excluded-owner value only where the host actually protects that
directory. Ordinary test runs skip the excluded-owner subtest.

The full FTS5 suite ran with local test sockets permitted. All failures were the
new tool missing from the generated Codex matcher and one old pipeline fixture
indexing the status text instead of explicitly reading output. Both were fixed;
their focused reruns pass, as do generated-artifact parity/idempotency checks.
All other packages/tests passed in the full run. The final build, focused vet and
race checks passed. Tests use synthetic credentials and isolated configuration /
data/cache paths. No installed Codex activation is claimed.

The independent [code review](task2-code-review.md) approved this slice with no
findings. The external review could not run because its provider rejected the
configured API key. Go formatting and trailing-whitespace checks pass. The user's
global Git `indent-with-non-tab` rule flags the generated JSON's intentional space
indentation; generator equality validates that format, and the whitespace check
used the other standard checks without changing user Git configuration.

### Benchmark observations

`make bench-quality` passed against an archive of baseline commit
`5801bd35cca0824d333001c22e65f2965f325053` and the working tree. The named reports
are `routine-task2-before.json` (in the baseline archive) and
`bench-results/routine-task2-after.json`; the existing master report was preserved.
The archive has no Git metadata, so its JSON labels the commit unknown; the hash
above records the provenance. `qualstat` verified the fixture hash and zero delta
for all existing retrieval-quality/context-reduction metrics.

The threshold fixtures now measure the complete serialized result rather than
only its fallback text. Runs producing 4,999–50,000 bytes returned 1,201–1,203 bytes
of status/recovery metadata, with no knowledge indexing. Measured latency was
1.09 seconds for initial store creation and 0.60–0.66 seconds for later runs.
This is substantially slower than the former in-process path for tiny commands:
each run adds supervisor startup and encrypted connection authentication. It is
recorded as the cost of the current correctness path, not a claim of unchanged
performance. Task 17 must measure representative routine commands and determine
whether authenticated-connection reuse/open-path changes are needed before release.
The absence of a sanitized preview is an explicit temporary limit, not a quality
improvement represented by these smaller response sizes.
