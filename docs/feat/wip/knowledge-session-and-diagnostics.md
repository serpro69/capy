# Knowledge, session recall and diagnostics

These eight proposals address distinct operator and developer workflows. Each has its own implementation plan, task list and release criteria. Any one can ship first.

| When someone uses capy to… | Proposed feature | First observable improvement |
| --- | --- | --- |
| Index a manual or a structured API response | [Structured knowledge sections](structured-knowledge-sections/design.md) | A short fence inside an example preserves its section. An oversized JSON record keeps its identity across bounded pieces. |
| Find the line that answers a question | [Match-centered knowledge search](knowledge-search-excerpts/design.md) | A late-matching function or JSON record appears in the CLI and MCP excerpt. |
| Find the next section of a known manual | [Knowledge source browsing](knowledge-source-browsing/design.md) | Open at the beginning from an index or single-URL fetch receipt, or continue immediately after a ranked knowledge hit. |
| Ask several related questions | [Compact multi-query search replies](../done/compact-search-replies/design.md) | Repeated passages receive references. Omissions identify the first query to retry. |
| Fetch an article or API reference | [Readable fetched pages](readable-fetched-pages/design.md) | Relative references resolve after redirects. An empty application shell preserves the last good copy. |
| Pick up an archived conversation | [Vault session brief](vault-session-brief/design.md) | Read the latest indexed request and assistant entry in a named archive, with citations to each message's start. |
| Explain context consumption | [Session byte accounting](session-byte-accounting/design.md) | Reconcile measured workload bytes, report overhead and cache estimates. |
| Diagnose a missing interpreter | [Runtime availability diagnostics](runtime-availability-diagnostics/design.md) | See the candidate executable names checked for the missing language. |

The three knowledge search presentation changes meet in the MCP knowledge handler. Review that file together if branches land concurrently. Source browsing works with historical chunks. Match-centered search can extract a JSON record from an existing array chunk. Compact replies can use either excerpt renderer. Compact replies and the vault brief also share the vault-hit formatter. Its chunk-start citation stays with the search passage, and its brief action opens the named archive's latest exchange using the search's original project selectors. Each proposal can ship independently.

## Existing capabilities considered

| Area | How capy serves it | Consequence for these designs |
| --- | --- | --- |
| Relevance and project scope | Knowledge and vault already share Porter/trigram FTS, reciprocal rank fusion and reranking. Vault filtering precedes result selection. | Improve the returned evidence and source navigation using the existing ranks. [Retrieval decision](../../adr/028-corpus-agnostic-retrieval-and-rrf-federation.md) |
| Batch locality, yielding and output recovery | Each batch and child has an owner-bound ID, bounded output, and recoverable sanitized projection. Cleanup evidence has platform-specific scope. | Preserve the run handle as the way to recover a command. Knowledge sections describe explicitly indexed or fetched material. [Run contract](../../reference/runs.md) |
| Project memory and compaction | The encrypted vault owns transcripts, role/line anchors and deletion state. Hook processes have separate bounded state and can run without database credentials. | Read a named archive on demand. A second event journal, ambient memory-directory scan or replay into a new prompt would introduce separate consent, lifecycle and freshness questions. [Vault ownership](../../adr/027-vault-is-sole-session-store.md) |
| Page conversion and network admission | The fetcher already converts HTML, validates resolved addresses, bounds redirects/body size and keeps validation separate from retention. | Improve article selection and reference links inside an admitted response. Keep transport policy and source identity with the existing fetcher. [Fetch contract](../../reference/tools.md#fetch-freshness-and-statistics) |
| Tool routing, setup and recovery | Project-owned setup, hook observation, explicit operator recovery and generated-artifact checks already define those paths. | Runtime diagnosis can report what its process found without changing trust, replacing native tool results, activating hooks or repairing storage. [Operations](../../reference/operations.md) |
| Hook write contention | Guidance and tool observations have bounded file-state and one-use semantics across short-lived processes. | A delayed spool could publish or consume obsolete routing evidence. On-demand session recall takes a vault read outside hook dispatch. [Hook architecture](../../architecture.md) |
| Operator reporting | Local workload counters and read-only run diagnostics already have separate lifetimes. | Account for measurements within the running server. Pricing, remote telemetry and multi-day charts would require a different dataset. [Run inspection](../../reference/runs.md) |

Each design records its readiness for implementation. Its acceptance examples describe checks to run against the implemented feature.
