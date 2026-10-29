# Captured commands and run recovery

Run tests, builds and installs once, then recover their output by ID. Capture
starts before the submitted program can spawn. Both stdout and stderr are stored
as exact bytes in the selected owner's encrypted `.capy/runs/runs.db`; responses
and default reads use a sanitized projection. Search is built lazily from that
projection. New run output does not become knowledge or vault content.

The captured execution supervisor supports Linux and macOS 26 on Apple Silicon
with CGO. Other macOS majors, Intel Macs and other platforms fail before spawn.
Commands inherit the caller's OS confinement; capy does not grant filesystem or
network permissions.

Native lifecycle verification is reproducible with `make test-integration` and
`make test-integration-race`, after setting synthetic `CAPY_DB_KEY` and
`CAPY_VAULT_KEY` values as shown in the repository build instructions. Both use
CGO, `fts5,integration`, uncached execution, serial package scheduling, and a
20-minute package timeout for `internal/executor` and `internal/execution`.
Tests use isolated fixture roots. CI retains the full ordinary FTS5/race/vet
checks and adds these lifecycle suites; macOS proof runs on the explicit
`macos-26` arm64 runner. Release builds verify capture and restart recovery with
the actual binary before packaging, including both streams and a nonzero exit.

## Execute and recover

From a project with its existing knowledge credential configured:

```bash
capy execute --code 'go test -tags fts5 ./...' --timeout-ms 600000 --json
capy runs list --limit 16 --json
```

Copy the returned ID into `RUN_ID`, then recover without rerunning:

```bash
RUN_ID='paste-returned-run-id'
capy runs status --run-id "$RUN_ID" --json
capy runs read --run-id "$RUN_ID" --stream stderr --max-bytes 4096 --json
capy runs search --run-id "$RUN_ID" --query 'FAIL' --query 'panic' --json
```

Pass the returned `next_cursor` to the same operation with `--cursor`. For a
known raw line number, use `--stream stdout --start-line 120` without a cursor.
`end_of_available` means no more current bytes; `eof` also requires terminal
execution and finalized projection. Merged output follows capy's observation
order; only each individual stream has exact program-byte order.

For explicit **unsanitized**, byte-exact output:

```bash
capy runs read --run-id "$RUN_ID" --stream stdout --raw --encoding base64 --json
```

Decode and concatenate the returned fragments, following every cursor. Raw reads
can reveal secrets. They never feed automatic previews or search. A projection
failure makes default reads unavailable even when raw capture is complete.

Use `--language` for a supported non-shell runtime, or `--code-file program.sh`
instead of `--code`. Code files are checked inputs, at most 1 MiB. CLI execution
stays attached; interrupt that CLI process to cancel it. It has no `--background`
or `--wait-ms` option. CLI preserves the child exit code; wrapper failures include
124 for deadline, 126/127 for start failure and 74 for successful execution with
incomplete capture. The result separately reports actual child status.

MCP `capy_execute` and `capy_execute_file` return the same version-2 run contract.
`timeout` is the execution deadline in milliseconds; `wait_ms` (0–30,000) controls
only how long the response waits. A yielded run remains owned by that MCP server,
and `capy_runs(action=status|read|list|search)` recovers it. Cancel through that
same server with `capy_runs(action=cancel,run_id=...)`, then poll for completion.
Another MCP owner cannot cancel it. Server shutdown cancels owned jobs; retained
logs survive restart, but jobs are not persistent.

Legacy MCP `background=true` is execute-only: `timeout` becomes the initial wait
and execution has no deadline. Capture continues after return. It cannot be
combined with `wait_ms`; prefer an explicit deadline plus `wait_ms`.

An admission rejection has a null run ID and means the command did not start.
An admitted ID with `start_failed`, a lost response, or incomplete capture requires
status/list recovery before retrying. In particular, capture failure after spawn
does not kill a command that may already have modified the project.

Linux and macOS startup verify acquired lease continuity before authorizing a command.
A helper stalled before authorization is terminated and reaped before its slot
can be reused. If reaping cannot be confirmed, ownership and capacity remain held
and new execution is refused. Once authorization may have been sent, cancellation
uses the helper's cooperative cleanup path. Compiler phases count as execution:
a completed compiler followed by runtime spawn failure retains its scope's
completed cleanup state while reporting `start_failed` and preserving the
compiler's captured output.

Version-2 responses expose durable `cleanup: {scope, state}` alongside capture.
Linux uses scope `linux_descendants_v1` and completed state `retired`. The
Darwin scope `darwin_process_group_observation_v1` uses `observed`: completion of
a bounded observation policy, which does not prove all processes are gone.
Escaped descendants or processes racing the final group observation can survive;
normal reuse and retention accept this limitation. Required observation, signal,
drainage or reap failures still leave cleanup pending and preserve quarantine.
Both scopes also support `not_spawned` and `pending`. Pending proof remains
pending when final persistence fails, even if local cleanup completed; actual
exit or signal information is reported separately. A rejection without an
admitted run and a query error without a loaded subject carry `cleanup: null`.

Status/cancel wrappers and read pages carry proof in their nested run or batch.
Lists carry it in each entry; search carries the selected subject's proof, with
per-child proof on batch coverage entries. Batch aggregates use one homogeneous
scope and retain child proof after log eviction. Response shortening preserves
proof on every returned object. Cursor and redaction versions remain unchanged,
so existing cursors can still recover retained bytes. Hooks accept healthy v1
responses and validate scoped v2 evidence explicitly; this evidence does not
grant permissions or establish host quiescence.

## Batches and capacity

`capy_batch_execute` validates every command before any can run. Each child has
an ordinal and run ID, even when queued, skipped or cancelled. Recover a batch:

```bash
BATCH_ID='paste-returned-batch-id'
capy runs status --batch-id "$BATCH_ID" --json
capy runs list --batch-id "$BATCH_ID" --limit 16 --json
capy runs search --batch-id "$BATCH_ID" --query 'failure' --json
```

Read a child's output by its run ID. A retained manifest can still list terminal
child status after its log is gone (`output_available=false`). `query_scope=batch`
searches child logs; `global` adds separately labelled knowledge results. Omit
queries during execution to avoid indexing. Later batch search can reuse the
original nonempty query array.

The default owner allows four active runs and 64 pending batch children.
Standalone work rejects when full; queries and cancellation remain available.
Batch concurrency is 1–8, bounded by the same owner capacity; eligible batches
share slots fairly. A serial batch's deadline includes queue time. Parallel
children receive their timeout at dispatch, while undispatched children are
skipped at the admission-time cutoff. `completed` means every child is terminal,
not that all commands succeeded.

## Storage, policy and diagnostics

Implicit CLI selection validates the full physical Codex ancestor chain and uses
the nearest installation. Without Codex artifacts, ordinary project discovery
applies. `--project-dir` explicitly selects a project. `--cwd` changes execution
only. Nested owners and linked worktrees have separate run stores even when
their relative `store.path` shares a knowledge database. The run key follows the
selected knowledge credential resolver; vault credentials are separate.

The selected owner must be writable inside the host sandbox. If it is excluded,
stop and repair writable-root/project selection with the operator. Capy never
silently relocates logs or grants a native execution escape. Setup ignores
`.capy/**` while keeping `.capy/AGENTS.md` tracked.

Defaults are in [operations](operations.md#configuration-defaults). The configured
executor timeout now takes effect: 300 seconds replaces the old MCP hardcoded
30-second execute/file and 60-second batch defaults. Responses default to 8,192
serialized bytes, including metadata and escaping. `executor.max_output_bytes`
is a deprecated preview limit; it does not limit stored capture.

Store policy records TTL (24 hours), per-run raw capture (64 MiB), retained quota
(512 MiB), schema/redaction versions and accounting mode. TTL is recorded at
admission and expiry at completion; reads and later policy edits do not renew it.
Quota can evict completed output before TTL. Active runs are protected. Accounting
includes reservations, raw/projection metadata and FTS debit; physical SQLite/WAL
allocation can exceed it. Optional indexing can be quota-blocked while reads work.

For a policy change, stop all MCP owners, attached CLI work, readers and remaining
supervisors for that owner. Merge the desired `[runs]` settings into its TOML,
restart matching MCP configurations, and admit new work. A normal run operation
publishes a revision only under exclusive lifecycle ownership. A live old-config
owner causes `policy_mismatch`; diagnostic inspection never publishes policy.
Lower quotas may evict completed logs; existing expiry timestamps stay unchanged.

```bash
capy doctor --project-dir .
```

CLI doctor, MCP doctor and MCP stats share the run-store checks: host/build
execution capability and its cleanup scope, safe path,
configured capacity, authoritative policy, accounted/physical usage, retained and
unfinished counts, and at most eight recent run states/scoped cleanup/expiry/byte counts. An
unfinished record is not proof of a live process. Only the receiving MCP owner
reports its actual active/pending reservations. Inspection has a two-second
budget, does not create a run store or locks, and neither reconciles, indexes nor
cleans up. A readable store does not prove sandbox writability or hook activation.
Capability checks launch no command and perform no cleanup proof probe. Darwin
`observed` never proves host quiescence: escaped or racing descendants may survive.
Each retained run's cleanup scope comes from its authoritative row, so historical
Linux `retired` rows remain distinguishable from Darwin observations. Authenticated
stored/supported schema versions are reported separately from executable capability.

`capy runs inspect` reports durable incidents, recorded quarantine markers and unfinished observations
independently of those eight recent runs. It supports schema 8, 9 and 10 without
migration. Pending work without recorded quarantine is an unattributed observation,
not a confirmed incident, a live-process claim or a guarantee of healthy admission.

```bash
capy runs inspect --project-dir . --limit 16 --json
capy runs inspect --project-dir . --run-id RUN_UUID
capy runs inspect --project-dir . --cursor CURSOR --json
```

Schema 10 records incident UUIDs, historical subjects, causes, detection stages
and the last durable phase during normal reconciliation. Reobserving the same
unresolved episode preserves its first detection time; later material evidence
revises it. A later incident after resolution gets a new UUID. Legacy markers
with no known causal run retain explicit unattributed metadata and no invented
detection time. Live-owner cause attribution and later phase updates are still
pending; current reconciliation observations remain labeled as inferred.

Keep the same owner and selector when continuing a page. Schema-10 incident and
receipt membership is bound to the recovery epoch; a membership/marker change
invalidates its continuation. Legacy schema-8/9 cursors bind registry markers and
a creation cutoff instead. Pending/run observations are current to each page,
separate from stable incident membership. Response shortening preserves a cursor
after the last displayed subject. Only allowlisted metadata is returned, without
commands, output or raw stored errors. Identity/proof/manifest inconsistencies
remain diagnostic blockers that operator confirmation cannot override.

Reconciliation commits incident publication, quarantine markers, accounting and
epoch changes together. Live quarantine writes its safety marker and epoch without
requiring incident quota or an index recount; runtime attribution remains Task 4.
Capacity or attribution-write failures refuse new execution without partial
attribution. Configured status/read/list queries can attach in SQLite read-only
mode to recover retained history when attribution cannot be committed; this
attachment performs no maintenance and can never claim an execution owner. A
service that used this fallback remains query-only: after repairing the store,
restart it before executing new work. Supported recovery and offline quota growth
require stopping all store users first. Helpers do not use that fallback. Epoch exhaustion refuses new admission even while the
affected execution owner still holds its lease.
Unchanged observations and read-only inspection do not advance the epoch.

Inspection uses a shared lifecycle lease for active WAL stores. For a closed
store without a WAL it takes a fresh nonblocking exclusive lifecycle lease,
rechecks WAL absence and rejects a nonempty rollback journal before an immutable
read. If the lease is busy, stop attached store users and retry inspection.
While that brief exclusive snapshot runs, a concurrent ordinary attachment can
also return `storage_unavailable` with “run store lifecycle is busy”; retry
attachment after inspection finishes rather than repairing or discarding storage. Both
paths prevent creation of missing WAL/SHM files and perform no maintenance.

This increment provides diagnostics and offline schema upgrade. History-preserving
administrative recovery application remains unavailable; upgrading does not clear
quarantine.
`--receipt-id` accepts a recovery receipt UUID, never an incident UUID; `--plan`
looks up a retained applied receipt, not an unapplied plan. Schema 8/9 has no
receipts and returns `receipt_unavailable`; absence never proves nonapplication.
Schema 10 looks up retained receipts by exact UUID or plan hash, even after their
audit deadline. A pruning receipt retains its summary with incomplete detail;
lookup never reconstructs or validates an unapplied plan.
Copyable inspection commands appear separately from explanatory next-step text.
Inspection does not apply recovery or retry the original command. CLI inspection
returns 0 on success, 2 for invalid arguments, 1 for storage/lookup failures and
74 if output delivery fails. `--json` uses the configured run response budget.

Session run byte totals are posted once at each owner completion, including late
output after handoff and each batch child. In-progress bytes remain available in
run status. Observed, captured and serialized response bytes are distinct:
discarded output is observed but not captured; captured totals are historical,
not current retention. Unknown bytes after helper loss are not invented. A
session-stat reset clears counters; runs completing afterward contribute their
whole known outcome. Doctor/stats do not include programs, output, keys or stored
error text in run diagnostics.

## Run schema upgrades

Run schema 10 supports fresh storage and encrypted upgrades from schema 8 or 9.
Normal authenticated, configured execution attachment automatically attempts the
upgrade before admission. Readers, helpers, inspection and doctor never migrate.
An execute refused for quarantine may already have upgraded the database; inspect
the actual schema instead of assuming it remained unchanged.

For an upgrade without submitting any application command, stop all run owners,
readers and supervisors for the selected owner, then use an ordinary terminal:

```bash
capy runs inspect --project-dir . --json
capy runs upgrade --project-dir . --json
```

Do not run the administrative command inside `capy_execute`: that capture holds
the lifecycle lease the upgrade needs exclusively. `runs upgrade` never creates a
missing store, claims a generation, performs retention or clears quarantine.
Normal execution attachment retains its ordinary post-migration maintenance,
including expiry of history already beyond its recorded TTL.

The stages commit separately: 8→9 adds Linux cleanup scope and normalizes batch
proof as before; 9→10 preserves original result/program/output bytes and adds
required phase metadata with explicit legacy `unknown`. Both preserve run/batch
IDs, row IDs, cursors, expiry, retained proof and quarantine. Schema 10 also
provisions bounded recovery metadata for later recovery support. The public
envelope and helper protocol remain version 2. No old job is restarted.

Migration needs free filesystem space for the encrypted table copy and WAL,
separately from retained-log quota. Preflight conservatively requires three
database images plus 2 MiB; this is not a disk reservation. Capacity/write failure
rolls back the current stage without eviction. A committed earlier stage remains
valid and can be resumed with the same command. `schema_upgrade_busy` means stop
remaining attached users; `schema_upgrade_capacity` identifies disk or quota limits.

If retained quota is insufficient, increase only `runs.max_retained_bytes` in the
selected configuration, keeping TTL and per-run limits unchanged, then run
`capy runs upgrade`. The command authenticates the existing schema and commits the
quota increase **before** migration checks. It rejects decreases and TTL/per-run
changes, including on schema 10, and works while quarantined. Its result reports
schema and policy revision. A later migration failure can leave that quota
increase committed; the output reports it explicitly. Automatic execution
migration uses the stored quota and never silently applies this administrative
increase. No failure evicts logs or recreates the store.

Reload compatible CLI/MCP binaries after upgrade. Older binaries refuse schema
10; there is no reverse migration. Retained history requires a compatible reader.
Explicit `capy runs reset --discard` remains a separate destructive option after
stopping all store users and establishing quiescence of old execution groups. It
permanently removes **all** retained run/batch logs for that owner, including
protected history. Knowledge, vault data and permanent locks remain intact.

Upgrade returns 0 on success, 2 for invalid arguments, 1 for failure and 74 for
output-delivery failure. A lost response does not establish rollback: inspect
current schema/policy before retrying. Response budgeting formats one captured
operation result and never repeats the upgrade.

## Key rotation and explicit data loss

`capy encrypt` rotates **knowledge only**. Run caches are not rekeyed; there is
no `capy runs rekey` or log-preserving rotation workflow.

1. Stop knowledge users, all affected run owners/readers/supervisors, and old
   execution groups. Include separate worktrees sharing the same credential.
2. From the knowledge project, run `capy encrypt`; provide the old passphrase and
   the new environment/prompted passphrase as described in the README.
3. Update each selected credential file/environment privately. It is not rewritten
   by `encrypt`. Existing run logs now need explicit discard.
4. For **each affected owner**, from that owner's directory, run:

   ```bash
   capy runs reset --discard --project-dir .
   ```

   This permanently deletes its run DB and known SQLite sidecars. No old key is
   required. It preserves permanent locks, settings, knowledge and vault data.
5. Restart owners and check `capy doctor`. The next execution creates a fresh
   encrypted cache; old cursors are stale.

If the original credential was selected accidentally, restore it instead of
resetting. Nothing is deleted automatically on wrong-key or corrupt storage.
`cleanup_unverified` additionally requires verifying that old execution groups
have stopped; an unlocked store does not prove noncooperating descendants exited.
Reset is an operator recovery action, never an automatic agent retry.

## Rollout and rollback

The Codex routine gate requires capture for recognized tests/builds/installs,
including mixed HTTP/routine requests, for root and child callers regardless of
availability evidence. Whole-request security is checked first. Claude receives
routine guidance only. `[routing.routine] enabled=false` disables this output
policy; `native_allow` supplies explicit `Bash(pattern)` exceptions per routine
element. Neither setting bypasses security, and HTTP-only routing stays separate.

Before upgrading loaded hooks, stop affected sessions and owners. Put the
compatible binary on PATH, run `capy setup --platform codex --project-dir .`
(and the existing Claude setup variant where used), then restart and review/trust
the generated hooks. Preserve owner IDs and external handlers. Verify that the
reloaded MCP exposes `capy_runs` and `wait_ms`, then run one harmless captured
command and recover it. Registration and doctor cannot prove host activation.

Before downgrading, disable the routine gate in the current project configuration:

```toml
[routing.routine]
enabled = false
```

Stop owners and sessions, then restore a matching older binary and its generated
bindings using that binary's setup. Keep credentials stable. Do not pair new
strict hooks with an old MCP server. Older binaries may not understand run
storage; keep the current binary available to recover logs, or explicitly discard
them after quiescence if needed. Do not change encryption merely to downgrade.
Older nested-installation limitations still apply.

The original installed-routing acceptance is recorded in
[Task 17](../feat/done/routine-command-capture/tasks.md#task-17-verify-installed-acceptance-and-release-readiness).
Native macOS capture, schema-8 preservation and Linux regression evidence are
tracked in the [native execution release record](../feat/done/macos-captured-execution/release-proof.md).
