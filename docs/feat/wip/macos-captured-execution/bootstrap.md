# Native verification bootstrap record

> Status: Task 1, Task 2 and Task 9 exceptions removed; routine enforcement verified restored 2026-10-26
> Applies to: Codex hosts enforcing Capy's routine-capture gate
> Plan: [implementation.md](implementation.md#execution-rules)

Claude Code receives routine guidance without this enforcement gate. A normal
terminal or CI job without the Capy Codex hook likewise needs no Capy routine
exception. Native host permissions/security policy still apply in every case.
Record the actual host; do not prescribe a universal exception.

## Authorization and configuration

On an enforcing Codex host where installed Darwin capture cannot start, the human
project/operator user must explicitly authorize the selected native test/build
commands before the agent changes routing. An existing explicit authorization may
be reused within its scope; ordinary design approval is not such authorization.
Keep `enabled=true`, preserve security checks, and merge only approved entries
into the selected owner's effective configuration (normally `.capy.toml`; check
the precedence over `.capy/config.toml` and global settings).

Example shape only, **not applied or authorized by this document**:

```toml
[routing.routine]
enabled = true
native_allow = [
  "Bash(go test -tags fts5,integration -count=1 ./internal/executor ./internal/execution)",
  "Bash(make build)",
]
```

Replace examples with the exact approved invocations, including any wrappers or
different flags. Do not broaden to `Bash(*)`, replace unrelated existing entries,
or disable the whole routine policy. Synthetic key/environment configuration is
separate; never record actual credentials here. After the planned Makefile targets
exist, their exact names may be authorized instead of direct Go invocations.

## Execution record

Fill this table before using an exception; leave the current entries honest until
implementation begins. If no exception is needed, record the host and reason.

| Item | Current value |
| --- | --- |
| Host / enforcement state | Task 1, 2026-10-22: macOS 26.6.2 arm64; installed MCP capture rejects before admission; initial local verification completed |
| Approving operator and dated message/reference | Project operator, 2026-10-22: “Yes” in response to the six-command verification request |
| Selected owner and effective config path | `/Users/sergio/Projects/personal/capy`; override `.capy.toml`; inspected project and default global configuration contain no existing routine rules |
| Exact approved command patterns | The six exact entries in the Task 1 command set below |
| Prior config and specifically added entries | Preserved original `[store]` section byte-for-byte; added only the approved `[routing.routine]` section. Original saved at `/private/tmp/capy-task1-validation/original-capy.toml`. Isolated baseline copy uses the same exceptions. |
| Purpose and expiry/removal condition | Authorized: Task 1 native waiter tests and baseline comparisons only; remove added entries when this verification ends |
| Applied by / date | Codex, 2026-10-22 |
| Effective routing verification | Exact approved waiter command passed (20 repeats). An initial form with output redirection was rejected before execution; no exception was broadened. |
| Removal by / date and verification | Codex, 2026-10-22: restored project/baseline configs byte-for-byte (SHA-256 `3ef676645a2a962811a97b54a602d55e362075be5152aa4d27fc05558ba0e415`). A read-only `capy hook pretooluse --platform codex` probe for the formerly excepted waiter command returned `permissionDecision=deny` with routine-capture-required guidance. The probe did not execute the command. |

Record scope adjustments and renewals here. At completion remove only the entries
added under this authorization, preserving subsequent unrelated operator edits.
Verify restored enforcement; do not merely claim the file was reverted. Store
test outputs/proof results in the corresponding proof report, not this permission
record.

## Proposed Task 1 verification

Prepared and explicitly **authorized/applied 2026-10-22**, following the operator’s “Yes” reply; **removed the same day** after local verification. Installed MCP capture on
the local macOS 26.6.2 arm64 host rejected before admission. The Task 1 waiter and
fixtures are reviewable in the working tree. Proposed scope: this repository and
an isolated copy of baseline `13204a9a68a3def965950e765ca910c615232913` for comparisons;
expire and remove the added exceptions when Task 1 verification ends.

Keep `enabled=true`; merge only these exact command patterns into the selected
owner's effective `.capy.toml`, preserving existing settings and security checks:

```toml
[routing.routine]
enabled = true
native_allow = [
  "Bash(go test -tags fts5,integration -run=TestDarwinWait -count=20 -timeout 5m ./internal/executor)",
  "Bash(go test -tags fts5,integration -race -count=1 -timeout 10m ./internal/executor)",
  "Bash(go vet -tags fts5 ./internal/executor)",
  "Bash(go test -tags fts5 -run=TestNoSuchTest -bench=BenchmarkExecutorOverhead -benchmem -count=6 -benchtime=1s ./internal/executor)",
  "Bash(make bench-quality BENCH_BRANCH=macos-wait-task1)",
  "Bash(make bench-quality BENCH_BRANCH=macos-wait-baseline)",
]
```

The initial runs used synthetic `CAPY_DB_KEY`/`CAPY_VAULT_KEY`, `CGO_ENABLED=1`,
and `MACOSX_DEPLOYMENT_TARGET=15.0`. The user subsequently selected macOS 26-only;
follow-up runs use `MACOSX_DEPLOYMENT_TARGET=26.0` under the same approved command
scope. No real credentials enter the evidence. Benchmark
commands run once per candidate/baseline under matching conditions, retaining all
results. Fix-driven reruns use the same authorized commands. Compilation is
included in `go test`; no installation, publishing, or broad policy exception is
requested. macOS 15 verification was removed by the user-approved scope decision.

## macOS 26 scope follow-up

On 2026-10-22 the user accepted macOS 26-only scope. The same six previously
approved command patterns were reapplied for retargeted verification and existing
baseline comparisons; no pattern was added or broadened. The prior source/config
snapshots remain in `/private/tmp/capy-task1-validation/`. Runs now use deployment
target 26.0. The temporary section was removed after these checks from both
project and baseline; their original bytes match the recorded SHA-256. A second
read-only installed-hook probe returned the routine-capture denial for the formerly
excepted command. The follow-up is closed; no exception remains.

## Task 2 deterministic re-entry fixture

2026-10-22: the operator requested “let's try a deterministic native fixture as
you propose.” The fixture is prepared in `internal/executor/group_darwin_test.go`
with an integration-only membership binding in `group_fixture_darwin.go`.
The attempted captured test was rejected **before admission** with
`captured execution currently requires Linux supervision`; no run ID exists and
no test executed. The operator subsequently approved “both fixture commands” in
the exact-command authorization prompt. The following exceptions were applied
for this fixture only; the original config was saved at
`/private/tmp/capy-task2-validation/original-capy.toml`:

```toml
[routing.routine]
enabled = true
native_allow = [
  "Bash(go test -tags fts5,integration -run=TestDarwinGroupReentry -count=20 -timeout 5m -v ./internal/executor)",
  "Bash(go test -tags fts5,integration -run=TestDarwinGroupReentry -race -count=1 -timeout 5m -v ./internal/executor)",
]
```

Use synthetic `CAPY_DB_KEY`/`CAPY_VAULT_KEY`, `CGO_ENABLED=1`, deployment target
26.0 and temporary build caches. Scope: this repository's fixture, including
fix-driven reruns of these exact commands. Preserve the current `.capy.toml`
`[store]` section. Remove only the added exception entries after verification and
verify restored enforcement with an installed-hook probe. No broad test, benchmark,
installation or production activation exception is proposed.

Verification completed: 20 focused repetitions passed (0.869s), followed by the
focused race run (3.416s). The two entries were removed on 2026-10-22. The restored
`.capy.toml` matches the saved original byte-for-byte. A read-only
`capy hook pretooluse --platform codex` probe for the formerly excepted repeated
test returned `permissionDecision=deny` with routine-capture guidance; it did not
execute the command. No Task 2 exception remains.

## Task 2 observational backend verification

The user accepted the observational contract on 2026-10-22. The dormant backend,
public sysctl inventory binding and native lifecycle/failure tests are prepared.
The earlier two-command authorization covered the re-entry experiment only.
Installed captured execution still rejects Darwin, so the following exact
extension was approved by the operator (“Approve these six commands”) for this
repository and an isolated copy of immediate baseline
`a93aa9830f04fbf18bb0295bba8f8e1bd64a0a21`. The entries were applied with the
original config saved at `/private/tmp/capy-task2-backend-validation/original-capy.toml`:

```toml
[routing.routine]
enabled = true
native_allow = [
  "Bash(go test -tags fts5,integration -run=TestDarwinObserved -count=5 -timeout 5m -v ./internal/executor)",
  "Bash(go test -tags fts5,integration -race -count=1 -timeout 10m ./internal/executor)",
  "Bash(go vet -tags fts5 ./internal/executor)",
  "Bash(go test -tags fts5 -run=TestNoSuchTest -bench=BenchmarkExecutorOverhead -benchmem -count=6 -benchtime=1s ./internal/executor)",
  "Bash(make bench-quality BENCH_BRANCH=macos-observed-task2)",
  "Bash(make bench-quality BENCH_BRANCH=macos-observed-task2-baseline)",
]
```

Use synthetic keys, CGO, deployment target 26.0 and temporary caches. Compare the
same benchmark command on baseline/candidate; preserve all attempts. The quality
suite is expected to retain the existing Darwin public-capture failures until
Task 10; compare actual results without changing assertions. Scope includes
fix-driven reruns of these commands, not installation or activation. Keep routine
enforcement enabled, preserve unrelated config, remove these entries when the
verification ends, and probe restored enforcement.

The approved backend verification is complete. Focused native tests passed in
three recorded rounds (18.973s, 19.225s, final 19.216s); full executor integration
race passed in 28.112s, 27.891s and final 26.696s; vet passed. Later rounds added
late-cancellation and reviewer-requested wait/reap failure evidence. Performance
and quality comparisons used the immediate baseline above; see `process-proof.md`
for limits and the identical pre-existing public-capture quality failures.

Both candidate and baseline exceptions were removed on 2026-10-22. Their config
files match the separately saved originals byte-for-byte. The installed hook
denied a read-only probe of the formerly excepted focused backend command with
routine-capture-required guidance; the probe executed no test. No exception
remains. All verification logs/reports are retained under
`bench-results/macos-observed-task2-validation/`; no assertion or skip was changed
to make the inherited quality failures pass.

## Task 9 private captured integration — authorized verification

2026-10-26: Task 9's native supervisor/stream wiring and encrypted integration
fixtures are prepared from baseline `31c0ef259ad8dbcc5f67fc12c7fc9cd5ec15730c`.
Installed MCP capture rejected a readiness probe before admission; no command ran.
The earlier Task 1–2 authorizations do not cover this task. The operator approved
this seven-command scope on 2026-10-26: “Run the native verification.” Temporary
exceptions were applied to the project and isolated baseline `.capy.toml`, then
removed after verification; routine enforcement stayed enabled. Original project configuration is preserved
at `/private/tmp/capy-task9-validation.X0I2Gj/original-capy.toml`.

The authorized scope used these exact temporary exceptions for this repository and an isolated
copy of that baseline, including fix-driven reruns. Preserve existing settings,
keep routine enforcement enabled, use synthetic knowledge/vault keys, CGO,
deployment target 26.0 and temporary build caches. No install, public activation,
push or release is included.

```toml
[routing.routine]
enabled = true
native_allow = [
  "Bash(go test -tags fts5,integration -run=TestDarwin -count=5 -timeout 10m -v ./internal/executor ./internal/execution)",
  "Bash(go test -tags fts5,integration -race -count=1 -p 1 -timeout 20m ./internal/executor ./internal/execution ./internal/runstore)",
  "Bash(go vet -tags fts5,integration ./internal/executor ./internal/execution ./internal/runstore)",
  "Bash(go build -tags fts5 ./...)",
  "Bash(go test -tags fts5 -run=TestNoSuchTest -bench=BenchmarkExecutorOverhead -benchmem -count=6 -benchtime=1s ./internal/executor)",
  "Bash(make bench-quality BENCH_BRANCH=macos-captured-task9)",
  "Bash(make bench-quality BENCH_BRANCH=macos-captured-task9-baseline)",
]
```

Compare the same six-sample executor benchmark on baseline/candidate and retain
all attempts. Both quality runs are expected to retain the public Darwin capture
rejections until Task 10; compare actual results without modifying assertions.
Native private-capture results are new evidence, not a performance comparison
against an already working public Darwin backend. Linux runtime regression
verification remains necessary on Linux/CI; cross-compilation is not a substitute.

Verification completed: the final focused five-repeat suite, full affected
integration race, vet and FTS5 build passed. Both performance runs passed. Both
quality runs retained the same four public-Darwin-capture failures, with identical
non-metadata results. Initial owner-loss fixture failures and all subsequent
results are retained in [capture-proof.md](capture-proof.md) and
`bench-results/macos-captured-task9-validation/`.

Removal completed 2026-10-26. Project and baseline configuration each match the
saved original byte-for-byte. A read-only `capy hook pretooluse --platform codex`
probe of the formerly excepted focused command returned `permissionDecision=deny`
with routine-capture-required guidance. The probe executed no test. Its response
is retained as `enforcement-restored.json` in the validation directory. No Task 9
exception remains; native verification is closed. Linux runtime/release evidence
remains tracked separately in the task and capture proof.
