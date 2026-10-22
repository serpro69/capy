# Native verification bootstrap record

> Status: no temporary routing exception authorized or applied by this design work
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
| Host / enforcement state | Design session; no native proof invoked |
| Approving operator and dated message/reference | None for a new bootstrap exception |
| Selected owner and effective config path | Not selected for implementation |
| Exact approved command patterns | None |
| Prior config and specifically added entries | No changes |
| Purpose and expiry/removal condition | Required before authorization; normally end of native proof/verification |
| Applied by / date | Not applied |
| Effective routing verification | Not run |
| Removal by / date and verification | Not applicable yet |

Record scope adjustments and renewals here. At completion remove only the entries
added under this authorization, preserving subsequent unrelated operator edits.
Verify restored enforcement; do not merely claim the file was reverted. Store
test outputs/proof results in the corresponding proof report, not this permission
record.
