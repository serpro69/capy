# Compact multi-query search replies

**Status** Proposed · [Implementation](implementation.md) · [Tasks](tasks.md)

Capy encourages an agent to batch related questions into one search. When two queries return the same passage, the answer spends context quoting it twice. When many queries miss, even the headings and notices can exceed the intended response size. In `handleSearch` the 40 KiB check happens before adding the next group and charges only some paths. Dedicated vault search limits hits per query and joins every group. [Knowledge search](../../../../internal/server/tool_search.go) · [Vault search](../../../../internal/server/tool_vault_search.go)

## Reply contract

Cap the **complete text payload** at 40 KiB per MCP keyword search call, including headings, status notices, partial failures, duplicate references, separators, and the shared-budget footer where applicable. This measures UTF-8 text before JSON-RPC wrapping. Preserve query order, hit rank and attribution to the knowledge or vault corpus.

Show the first passage in full with a short response-local reference such as `[1]`. On a later query, cite that reference if the same source, location and display passage would be presented again. A different excerpt of the same chunk earns its own entry. Never merge hits from different corpora, sessions or sidecars just because their text is identical. Keep the relationship to each requested query visible. A zero-hit query, a failed corpus, and a query skipped for lack of room have different summaries.

Reserve room for a completion notice and footer before rendering hits. At the first group that cannot fit, report its one-based query number, whether it was partially shown, and how many following queries were skipped. A caller can retry that query or the remaining suffix with the original selectors. Stop querying when nothing more can be rendered. A single excessively long field gets a bounded excerpt with an omission marker, so an otherwise ordinary first hit can be shown. A continuation names input positions and does not echo unbounded query text.

## Assumptions

Response-local references need to survive just one call. A practical first version may use a conservative duplicate key that occasionally repeats a passage. Mistakenly equating two different pieces of evidence is worse. Capy already applies admission once per `capy_search` call and has separate eligibility rules for `capy_vault_search`.

## Not Doing

Durable rank cursors would require a cross-request consistency contract over independently changing corpora. CLI searches accept one query and already have a different output contract.

## Rejected alternatives

Truncating the final concatenated text could remove the only error or continuation notice after all queries had run. Omitting every hit with the same title would hide genuine alternatives.
