# Native verification bootstrap record

> Status: Task 1 exceptions authorized, used, and removed 2026-10-22; routine enforcement verified restored
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
