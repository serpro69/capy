# Explain MCP session byte usage

**Status** Proposed · [Implementation](implementation.md) · [Tasks](tasks.md)

An operator who asks how much context a tool used should be able to reconcile the report with the work that ran. Today the savings denominator adds input, returned bytes, and estimated network bytes avoided by cache hits. A stats call records its own reply after taking its snapshot, so a second report changes the workload totals. The counters live in one MCP server process, which can serve several callers. [Current reporting code](../../../../internal/server/tool_stats.go)

## Report contract

Show the scope as this MCP server since process start or the last statistics reset. Report bytes submitted for knowledge indexing, raw fetched bytes offered for conversion, run bytes observed at owner completion, run bytes captured, and counted workload response bytes. Label input volumes as processed bytes. A repeated index or a fetch that subsequently fails conversion can contribute input volume. Captured bytes are a subset of observed run bytes. Put estimated bytes avoided by TTL cache hits alongside cache hits and attempted misses.

Compute a **workload byte reduction estimate** using input bytes (knowledge submissions + fetched input + observed run output) and counted workload response bytes. For positive input, the numerator is `max(input − returned, 0)`; the percentage denominator is input. For zero input, display the returned byte count and show the estimate as unavailable. Apply the existing display clamp for nonzero returned bytes. For example, 10,000 input bytes, 2,000 response bytes and a 1,600-byte cache estimate yield an estimated 8,000 bytes or 80.0% reduction. Captured bytes and the cache estimate do not enter this arithmetic. Explain that data supplied inline may already have been in context and that the estimate describes byte flow, not model billing.

Count each tracked workload reply with its existing measurement convention (serialized structured result or text payload). Show calls and returned bytes per tool from the same workload snapshot. Report-only `capy_stats` calls have their own call/byte row outside these totals. Run reads and status calls still incur returned bytes; new input is recorded once at owner completion. A second report after no other activity has identical workload numbers. Storage inventories and live run observations have their own observation times.

## Assumptions

Input counters reflect submission and observation events. They cannot establish unique retained bytes or prove what a host placed into context. Completion of a yielded run can arrive after the reply or after a stats reset. Attribute that input when the completion callback records it.

## Not Doing

Historical dashboards, persisted cross-process metrics and model-specific bills require additional data. Per-agent attribution requires identities the MCP server cannot always establish.

## Rejected alternatives

Adding cache estimates to measured input makes a cache hit change the savings denominator without new content. Treating retained store size as input would lose evicted runs and count surviving data across reporting intervals.
