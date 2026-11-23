# Explain which execution runtimes are available

**Status** Ready for implementation · [Implementation](implementation.md) · [Tasks](tasks.md) · [Review resolution](review-design-2026-11-23.md#resolution-after-recheck)

When R is missing from an execute tool’s advertised languages, the operator needs to know that capy looked for `Rscript` and then `r`, and which process to configure. Doctor currently lists only the detected languages. The executor already owns the candidate names and their order.

## Operator contract

Both doctors show one marked `Runtimes` check followed by one informational bullet per supported language, sorted by language name. The check counts languages, including languages that share an executable. Zero selections keep the failing `none detected` summary, one gives a warning, and two or more pass. A missing language has no severity of its own.

The aggregate line tells the reader when each doctor sampled PATH and how to refresh the server’s inventory. A selected row gives the executable basename. A missing row says `no candidate selected` and names the candidates in lookup order. The operator can check that one is executable in an absolute directory on the launcher’s PATH. A missing shell row also identifies the workflows that need it. This excerpt shows a report where only Python and Ruby resolve.

```text
- [x] Runtimes: 2/11 (python, ruby). CLI samples this invocation. MCP shows startup discovery. Restart the MCP server from the intended environment to refresh its inventory.
  - python — selected python3
  - r — no candidate selected. Check Rscript, r on capy's PATH.
  - shell — no candidate selected. Check bash, sh on capy's PATH. Required for default capy execute and every capy_batch_execute command.
```

| Where the operator looks | What the observation means |
| --- | --- |
| `capy_doctor` in the agent | The MCP server cached this selection when it registered `capy_execute` and `capy_execute_file`. Restart the server from the intended launch environment to refresh both the inventory and the advertised list. |
| `capy doctor` in a terminal | This invocation samples its own PATH. The terminal and agent launcher can have different environments. |
| A captured run | Each admitted run’s supervisor performs its own lookup during preparation. Adding a shim in an existing PATH directory can make a run succeed while the server still advertises a miss. The language argument accepts all supported languages. Policy, host and storage checks still apply. |

### Execution errors

A direct executor caller gets the language and ordered candidate names when preparation fails to select a supported runtime. For a newly produced captured result with `state=start_failed` and `error.code=runtime_unavailable`, the message says an execution phase could not start and points to **both** doctors for PATH discovery. A missing interpreter, a visible shim whose interpreter is missing, and a compiler that produced no executable can all yield that code. A later doctor report cannot establish which happened to an earlier run. [Run and phase outcomes](../../../reference/runs.md#L71)

The CLI returns 127 for that startup failure. A child that actually starts and exits 127 retains `state=exited` and its own exit code. Unsupported languages still fail argument validation before admission. The new wording applies at result construction. Older runs retain their recorded messages. Response budgets can shorten displayed error text on either surface.

## Assumptions

PATH discovery uses the executor’s existing candidate order and selection. It can identify a selected name even when its dependencies are missing. Capy invokes a selected shim through the path returned by lookup so a name-sensitive dispatcher can select the right program. Host capability, run storage and cleanup evidence have their own diagnostics. [Runtime lookup](../../../../internal/executor/runtime.go) · [Capture diagnostics](../../../reference/runs.md#storage-policy-and-diagnostics)

## Not Doing

Runtime installation belongs to the operator. Version probes would execute candidate binaries during diagnosis. Automatic rescans would change the server’s advertised-inventory lifetime.

## Rejected alternatives

A second detector could select a different shim from execution. The runtime section exposes registered names and selected basenames only. PATH directories, symlink targets and raw OS errors would disclose local details, and the cached selections contain no per-directory failure history.
