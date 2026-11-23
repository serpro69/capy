# Implement session byte accounting

## Workload totals

In `internal/server/stats.go`, distinguish submitted knowledge bytes from the raw body accounting already done in the single and batch paths of `tool_fetch.go`. Trace the inline/file/directory contributions in `tool_index.go`, including same-hash and failure paths, and describe exactly the events actually counted. Preserve `RecordRunCompletion` as the sole owner of observed/captured totals. Avoid adding the legacy sandbox counter to the same bytes twice. Derive calls and returned volume from the maps in one mutex-protected snapshot. Apply the reset to all categories with the existing reset lifecycle.

Render the estimate, denominator, separate cache estimate and exactly labelled inputs in `tool_stats.go`. Keep fetched, retained and observed volumes distinguishable. A process start timestamp is not a reset timestamp: use the existing uptime label for process age, and record the measurement interval start under the mutex at reset if displaying its start time. Step → verify the example, zero input, returned bytes exceeding input, cache-only calls, mixed file/inline/fetched content and reset. An incomplete capture with observed bytes still contributes once to workload input.

## Report overhead

Use `trackToolResponse` to count the actual stats response after formatting, as today. Partition the `capy_stats` map entry away from workload totals and show it as report overhead through the preceding snapshot. All workload numbers must reconcile to their rows at the same boundary. An error before a handler's existing tracking point stays outside the tracked workload; label the counts accordingly. Update the tool description and relevant assertions that encode the old formula. Step → verify two consecutive reads, a run yielding and completing between reads, and repeated run reads that add only returned bytes. Keep run diagnostic queries read-only and leave their evidence logic in the existing inspection path.

## Verification

Exercise `stats_test.go`, `tool_utility_test.go`, fetch stats tests and the owner-completion tests, including a concurrent snapshot/reset under the race detector. Validate the report's numbers and labels independently from timing or Markdown spacing. Document the example and the two response-byte conventions. [Captured run accounting](../../../reference/runs.md) · [Tool reference](../../../reference/tools.md)
