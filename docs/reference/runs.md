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

Session run byte totals are posted once at each owner completion, including late
output after handoff and each batch child. In-progress bytes remain available in
run status. Observed, captured and serialized response bytes are distinct:
discarded output is observed but not captured; captured totals are historical,
not current retention. Unknown bytes after helper loss are not invented. A
session-stat reset clears counters; runs completing afterward contribute their
whole known outcome. Doctor/stats do not include programs, output, keys or stored
error text in run diagnostics.

## Run schema upgrades

Run schema 9 preserves schema-8 history through an encrypted transactional upgrade.
Stop all run owners, readers and supervisors for the selected owner, then start
captured execution with the compatible new binary. Only normal execution-owner
attachment performs the upgrade. Recovery queries, supervisors and doctor report
the found/supported schema versions without migrating.

The upgrade preserves run/batch IDs, output, cursors, expiry timestamps and pending
cleanup evidence. It does not restart old jobs or clear quarantine. An old
`retired` record remains Linux retirement evidence. The batch manifest gains
scoped proof; public version-2 responses normalize historical blobs from that
proof and the authoritative run columns without rewriting retained output.
Normal TTL/quota retention resumes after attachment, including expiry of history
already beyond its recorded TTL.

Migration requires free filesystem space for the temporary encrypted table copy
and WAL work, separately from retained-log quota. The preflight conservatively
asks for three database images plus 2 MiB of headroom; it is not a disk reservation.
A capacity or write failure rolls back without evicting logs to make the upgrade
fit. `schema_upgrade_busy` requires stopping remaining handles;
`schema_upgrade_capacity` identifies filesystem or retained-quota limits. If the
stored retained quota is insufficient, adjust it with a schema-8-compatible binary
before retrying. No error automatically recreates or discards the store.

Older binaries cannot read schema 9 and may misleadingly report
`storage_unavailable` with path/permissions advice. Use the compatible new binary's
doctor to identify the stored and supported versions. Keeping schema-9 logs requires
a compatible reader; there is no reverse migration. To return to a schema-8 binary,
export needed logs first, stop all owners/readers/helpers and establish quiescence
of any old execution groups, then explicitly run `capy runs reset --discard` with
the compatible binary for that owner before installing the older binary and its
matching bindings. This permanently removes **all** retained run/batch logs for that
owner, including protected history older than the default TTL. Knowledge, vault
data and permanent locks remain intact.

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
