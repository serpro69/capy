# Task 2 v2 isolated code review — 2026-10-13

**Result: APPROVE.** The independent code reviewer found no P0–P3 issues in
Task 2's bounded-input implementation or its fixture correction.

## Scope and method

Reviewed the current Task 2 diff, including new untracked input/CLI tests,
against `design-v2.md` §5, `implementation-v2.md` §3 and `tasks-v2.md` Task 2.
Task 1's owner, direct-dispatch, Claude, MCP and signal contracts were checked
for regressions. Tasks 3–5 remain intentionally pending.

The reviewer used an isolated session without implementation history and loaded
the Go profile before reading evidence: SOLID, removal, security, style, error
handling, injection, naming, performance and concurrency checklists. It inspected
the tests without executing them; verification results belong to the implementing
agent and are recorded in [verification-v2.md](../verification-v2.md#task-2-acceptance-evidence).

## Findings

No substantiated P0, P1, P2 or P3 findings. The reviewer verified EOF framing,
byte limits, idle renewal, absolute deadlines, descriptor ownership and closure,
admission before parsing/policies/state, and shared CLI transport-error mapping.
No removal work or blocking test gaps were identified.

The full suite exposed one credential-test fixture using nil stdin, which
`os/exec` turns into `/dev/null`. Task 2 explicitly rejects device input. The
fixture now supplies an empty pipe, preserving its credential-free hook and
Claude assertions. The independent reviewer inspected that follow-up and retained
APPROVE. No production behavior was relaxed to accommodate the fixture.

The initial review cautioned that a parent `go test -race` alone does not
instrument CLI subprocesses. The implementing agent supplied the separate
passing `GOFLAGS=-race` subprocess run (18.540s), and the reviewer withdrew that
coverage limitation. The build helper inherits `GOFLAGS`; timing fixtures set
`GORACE=atexit_sleep_ms=0` inside their isolated subprocess environment.

## Reviewer availability and limits

PAL's external review was attempted with `gemini-3.8-flash`. Its expert step
failed with `API_KEY_INVALID`, so it produced no independent findings. This
approval is from the isolated code-reviewer only.

Linux pipe behavior is covered by controlled subprocess probes. macOS runtime
behavior and interrupting regular-file kernel I/O remain unverified as planned;
live v2 host acceptance belongs to Task 5. Capy MCP tools were unavailable, and
there are no systemic findings to index or new conventions absent from the
feature documentation.
