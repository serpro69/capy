# Compact multi-query search replies

**Status** Done — Tasks 1–5 implemented, reviewed and verified, 2026-11-26 · [Implementation](implementation.md) · [Tasks](tasks.md)

Agents batch related questions to save round trips. Today two questions can quote the same evidence twice. A batch containing only misses can also exceed the intended limit because the federated handler counts hit bodies and the dedicated vault handler limits hits per query. In a baseline probe, two valid misses with long query headings returned 44,453 and 44,079 bytes respectively. [Knowledge search](../../../../internal/server/tool_search.go) · [Vault search](../../../../internal/server/tool_vault_search.go)

## What the caller receives

Every keyword reply from `capy_search` and `capy_vault_search` fits in **40,960 bytes of valid UTF-8**. Count the text a client reads after JSON decoding, including headings, references, errors, separators, omission markers, guidance and the budget footer. JSON-RPC framing and escape syntax are outside this measure. The bound also applies to argument errors, blocked searches and disabled vaults.

Number query groups from one in the original input order. Keep hits in their existing rank order, including knowledge before vault on a federated tie. Mark each first passage with a response-local reference such as `[1]`. When a later hit has the same corpus, source, location, attribution and passage, cite the earlier reference at that hit's rank. A different window into the same chunk gets its own entry. A reference counts as a shown hit.

A full vault entry identifies the session and the chunk's starting transcript line, with the sidecar ID when the line belongs to a sidecar. Independently archived children retain their own session UUID and parent attribution. When the vault brief is available, a main-transcript hit with an intact usable ID can offer its latest exchange in the original project scope. A reference leads back to that full hit. [Vault brief](../../wip/vault-session-brief/design.md#following-a-search-hit)

| Query outcome | What appears in its group |
| --- | --- |
| All eligible passes completed with zero hits | A miss. Shared advice about excluded ephemeral knowledge appears once per reply when applicable. |
| A corpus or FTS layer failed | The affected corpus and a bounded cause. Show any surviving ranked hits as partial results. With zero surviving hits, report the failure. |
| The byte budget interrupted the batch | A completion notice gives the first unfinished query number and the count of following queries skipped. For a query that ran, it gives hits shown out of hits returned at the effective limit. It identifies a query skipped before lookup as unsearched. |

Use bounded previews for unusually long fields. Clipping one field has a visible omission marker and still counts as showing that hit. The complete admitted result set for **one** query fits on a fresh reply, including ten distinct vault hits at the dedicated tool's maximum. Formatting limits apply to display only. Retrieval uses the complete query and original selectors.

## Retrying an unfinished batch

Retry from the first unfinished query, **including that query**, with the original selectors and requested limit. For example, if query 4 showed one of two hits and three later queries were skipped, submit original queries 4–7. Positions and references start over in the new reply. The retry searches current data from the first rank. The corpus and the effective limit can change between calls.

Each valid federated request consumes one shared admission. By default, call 3 can show two hits per query and call 4 can show only one. To see a second rank omitted on call 3, use the reported reset time and retry with a requested limit of at least two in an untapered window. Other callers share that window. A blocked call reports its reset time. Dedicated vault search uses its own per-query limit, default three and maximum ten. [Shared budget decision](../../../adr/008-own-config-system.md#amendment-shared-search-budget-2026-10-12)

## Admission and guidance

Keep the existing source, kind, project and date selectors. Dedicated vault search will reject a supplied malformed `queries` array before opening the vault, including a non-string member in either a native or a serialized array. Dropping such a member shifts every later position. Valid serialized string arrays and the single-`query` fallback for absent or empty arrays remain supported.

Direct new captured command output to `capy_runs` with its run or batch ID. Manual knowledge, fetched material and historical execution rows already indexed in knowledge remain searchable with `capy_search`. For excluded ephemeral sources, retain both recovery selectors, `include_kinds` and `source`. [Tool reference](../../../reference/tools.md) · [Run storage](../../../reference/runs.md)

## Assumptions

References last for one reply. When a formatter cannot establish that two passages came from the same location, repeating the passage preserves the evidence. Compact replies can ship using the current snippet renderer. The proposed match-centered renderer can supply its chosen passage through the same writer. [Match-centered knowledge search](../../wip/knowledge-search-excerpts/design.md)

## Not Doing

Persistent rank cursors would require a consistency contract across changing corpora. This feature covers MCP keyword replies. CLI output, run search, section browsing and changes to retrieval scoring have separate contracts.

## Rejected alternatives

Truncating the joined response could cut off its only failure or retry notice after every query had already run. Deduplicating by title or by chunk alone would hide distinct evidence.
