# Explain MCP session byte usage

**Status** Ready for implementation · [Implementation](implementation.md) · [Tasks](tasks.md) · [Review resolution](review-design-2026-11-23.md#resolution)

An operator should be able to read `capy_stats`, add up the per-tool replies and see how much data this MCP server processed over the same interval. Today the denominator includes returned bytes and estimated cache savings. Reading the report also changes the next workload total. [Current reporting code](../../../../internal/server/tool_stats.go)

## Report contract

Show the **server's accounting interval** from process start or the last statistics reset, with server uptime labelled separately. All callers of this server share the counters. Always show the following quantities as exact integer bytes, including zeros.

| Measurement | Event counted |
| --- | --- |
| Knowledge submissions | Raw bytes offered to the store by `capy_index`, including same-hash content and submissions the store rejects. Inline content posts immediately before the store call. A file or directory posts the submitted total once when its ingestion result returns. Partial reads, oversize probes and files rejected before submission contribute zero. |
| Admitted HTTP body bytes | Body bytes accepted by the fetcher's status, read, size and content checks and passed to the conversion/indexing path. Count each fresh body once, even when conversion or indexing fails. Revalidation can contribute a body of the same size again. |
| Observed run output | Known stdout and stderr bytes posted at owner completion for each standalone run or batch child, including late and incomplete captures. |
| Captured run output | The captured subset of observed output at those same completions. Eviction can subsequently reduce the available output. |
| Counted workload response bytes | The combined size of tracked replies from every tool except `capy_stats`, including run reads, status, inspection and doctor. A structured result contributes its complete serialized MCP result, including metadata and fallback content. A text-only result contributes the lengths of its text payloads. Transport framing is outside both measures. |

Use one snapshot for the interval, input components, captured subset, cache figures, response rows and totals. Show each workload tool's **counted replies** and returned bytes. One batch reply counts once regardless of the number of items. Show `capy_stats` separately as report overhead, including its count and byte sum as recorded **before this snapshot**. The current report gets measured after formatting. An idle second read has identical workload numbers and includes the first reply in its overhead. A zero workload reply count still permits measured input, cache activity or prior report overhead.

### Workload byte reduction estimate

Show total input as the sum of knowledge submissions, admitted HTTP bodies and observed run output. Show total workload response bytes alongside it so the operator can recompute the estimate. For positive input, estimated reduction in bytes is `max(input − workload response bytes, 0)`. Divide that value by input and show one decimal percent, capped at 99.9% whenever workload response bytes are positive. For zero input, show the exact response volume and mark both the estimated reduction and its percentage unavailable. Compute this once for the entire interval.

| Input | Workload response | Estimated reduction |
| ---: | ---: | --- |
| 10,000 B | 2,000 B | 8,000 B (80.0%), even with a separate 1,600 B cache estimate |
| 100 B | 120 B | 0 B (0.0%) |
| 10,000 B | 0 B | 10,000 B (100.0%) |
| 0 B | 90 B | Unavailable |

Use this estimate in place of the savings multiplier, estimated-token rows and claims about bytes entering or staying out of context. Inline content may already have appeared in a prompt. The estimate describes measured byte flow over the interval. Captured bytes, cache estimates and report overhead each have their own displayed values.

## Index and fetch replies

Count one final reply for a `capy_index` call that submits inline content or a file, including an empty file and a post-submission error. A file rejected before submission keeps its existing untracked error path. A directory returns one counted summary for the entire walk, including a partial walk or one that submits zero files. For a single fetch, count the reply on a cache hit or an eligible attempted miss, including a network failure with zero admitted body bytes or a later conversion/index error. Preserve existing tracking for advisory redirects and admitted batch summaries. Cache eligibility remains a separate per-URL decision. Other handlers retain their current response-tracking boundaries. [File accounting](../../../../internal/knowledge/index.go) · [Fetch outcome rules](../../../adr/013-configurable-fetch-ttl.md#amendment-cache-outcome-visibility-2026-10-12)

A successful fresh or revalidated fetch receipt gives two exact sizes in bytes. The labels are **admitted HTTP body bytes** and **successful index-input bytes**. The second measures the full representation passed to the store on a successful submission, before clipping the preview. HTML conversion can make it much smaller. A single-URL cache-hit receipt shows zero for both. A batch reports the two sums across its items. An admitted body whose conversion or index fails contributes its size to the first sum and zero to the second. A single-URL error after body admission gives the same two values with the original error. The second size is informational and never adds another input to the estimate.

Put the TTL estimate next to hits, attempted misses and configured freshness. Each hit adds its cached chunk count × 1,600 B. This approximates avoided data, independently of the body sizes on receipts. Hit rate is `hits / (hits + attempted misses)`, unavailable when there were no eligible outcomes. A cache-only interval can show a 100.0% hit rate and an unavailable workload reduction.

## Reset and live inventories

Each update belongs to the interval in which the stats mutex records it. A request can span two intervals. MCP `purge_all: true` with `dry_run: false` starts a new interval when the knowledge purge succeeds, even if optional reclamation later fails. A tracked cleanup reply is posted in the new interval. A dry run or a purge failure before the wipe retains the interval. A run completing after reset contributes its whole known outcome once. Likewise, a report started before reset can contribute overhead after reset. The next report uses the new interval start and the original server uptime.

Knowledge, vault and run inventories have their own observation times. A fresh server can answer a stats call without constructing a knowledge handle or creating run storage. When a knowledge user initializes the handle concurrently, stats can omit that sample or use the fully configured handle. [Run diagnostics](../../../reference/runs.md#storage-policy-and-diagnostics)

## Assumptions

Counters describe events in this MCP server. CLI ingestion, automatic stale-file refresh and vault import have different producers. The server can measure a result it constructs. Host delivery, tokenization and billing require observations beyond that boundary.

## Not Doing

Durable analytics, historical charts and per-agent attribution require persistence or caller identities unavailable here. Existing store formats suffice for deployment and rollback.

## Rejected alternatives

Adding cached estimates to input would change the denominator on a hit without processing a body. Using retained storage would lose evicted runs and count surviving data across intervals. Summing per-tool reductions would conceal the response cost of reading earlier work.
