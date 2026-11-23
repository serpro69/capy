# A brief for an archived session

**Status** Ready for implementation · [Implementation](implementation.md) · [Tasks](tasks.md) · [Review resolution](review-design-2026-11-23.md#resolution-after-recheck)

A developer deciding whether to reopen a conversation can ask for the opening request and the latest indexed exchange in one named archive. The same brief is available from the terminal and MCP. It works after the original transcript has been removed from disk. The full transcript is available through `vault show`. [Vault workflows](../../../reference/vault.md)

## Interface

Use `capy vault brief <session-id>` or `capy_vault_brief({"session":"<id>"})`. CLI accepts the usual vault path selection and rejects `--tui`. MCP accepts only `session`, optional `project` and optional `all_projects`. Supply a session ID or literal prefix of at least eight bytes, following existing vault lookup semantics. Session and project selectors must be free of embedded NULs. A physical-line argument, including `--through-line` or `through_line`, is an input error. Silently serving the latest exchange in response to a historical request would give the caller the wrong answer.

CLI resolves the ID across the selected vault. MCP uses the same scope as vault search. The default matches the imported path for the current project. An explicit project matches a literal substring of the effective label or its imported-path fallback. Both path and label filters retain the vault’s ASCII case-insensitive substring semantics. Exact `"*"` or true `all_projects` widens to all projects. Apply that scope before deciding whether the prefix has zero, one or multiple matches. Ambiguity asks for a longer ID. It reveals neither out-of-scope candidates nor a guessed total. [Project selectors](../../../../internal/vault/session_project.go#L58)

### What the reader sees

Show the selected archive's ID, stored platform, current effective project and current effective title. Quote the opening indexed user entry, the latest indexed user entry and the last indexed assistant entry following it in the same turn. Show the opening and latest user entry once when they are the same row. A tool-only assistant entry qualifies and is labelled “assistant.” An exchange whose request has no indexed assistant entry gets that explicit status. If the archive has no indexed user entry, return its metadata and that fact.

Cite each excerpt with its stored role and zero-based **message-start line** in the main transcript. Claude snapshots can add text to an earlier message. Codex can attach a later call to an open assistant message. The citation takes the reader to the beginning of that normalized entry. A selected child UUID opens the child's own main transcript. Claude sidecar entries stay in their own coordinate space. [Decoder decision](../../../adr/031-transcript-model-seam-and-multi-platform-vault.md)

The complete brief fits in **4,096 valid UTF-8 bytes**, including citations, labels, omission marks and the final line ending. CLI stdout and the decoded MCP text item use the same rendering. Quote and escape untrusted text so transcript content cannot supply another role header or terminal control. Redact recognized secrets in metadata as well as excerpts before clipping. Give the latest request and assistant entry priority over the opening request. An oversized omitted field keeps its label, citation where applicable and a visible omission marker. [Rendering rules](implementation.md#render-the-brief)

For example, a brief can show an unfinished request like this. The two line numbers refer to the selected session.

```text
Session "48285c38-32fd-46f3-b18f-121cba7471ce" · claude-code
Project "capy" · Title "Release checks"
Opening user · message starts at line 1: "Check the release."
Latest user · message starts at line 18: "Which checks are still running?"
No indexed assistant entry in this turn.
```

Each call reads one committed snapshot of the current archive. A later call can show a renamed session, a different latest exchange after replacement, or a miss after deletion or reassignment outside the requested scope. A below-current per-session index still serves its available rows with a short `capy vault reindex` hint. An index with no user rows supplies no evidence of what happened outside that index.

### Following a search hit

In both `capy_search` and `capy_vault_search`, label a vault hit's line as the **chunk start**. An eligible main-transcript hit offers the **latest exchange in this session**, using the full ID and the original project selectors. For example, after a search with `project: "billing"`, use `capy_vault_brief({"session":"<full ID from the hit>","project":"billing"})`. A term may match later in the chunk or only in its title. A sidecar hit displays its sidecar ID and chunk start without the brief action. Independently archived children use their own UUID. [Chunk windows](../../../../internal/vault/chunker.go#L40) · [Compact search replies](../compact-search-replies/design.md)

### Outcomes

| Outcome | CLI and MCP behavior |
| --- | --- |
| Unique scoped archive, including no indexed user or assistant | CLI exit 0, MCP success. Show current metadata, available evidence and any stale-index hint. |
| Invalid request, ambiguous prefix or no scoped match | CLI exit 1, MCP `IsError`. Identify the input problem, ambiguity or scoped miss respectively. |
| Vault key missing, vault file absent or layout incompatible | Distinct errors. Identify the credential source, absent archive or compatibility problem. An authenticated empty vault gives a scoped miss. |
| Authentication, storage or deadline failure | Return an error and discard the partial brief. An encrypted file that fails authentication may have the wrong key or be damaged. Use the vault's diagnostic wording and keep credentials and archive content out of errors. |

All error outcomes use CLI exit 1 or MCP `IsError`. CLI reports lookup failures on stderr before writing any brief to stdout and propagates output failures. MCP reports failure when the connection still permits a response. The read has a two-second context deadline, shortened by the caller's deadline. Once the session is resolved, a timeout can point the operator to `capy vault show` with the safely representable identity and the same vault path selection.

## Assumptions

“Latest” refers to the available main-transcript index in the selected vault. Archival can lag a live host, and older index versions may omit messages a current decoder would recover. Lookup cost grows with the total per-message index. A read-only encrypted prototype selected three rows from 100,000 in about 236 ms on this machine. The [implementation workload](implementation.md#verification) checks the finished operation, including rendering and cancellation.

## Not Doing

Historical reconstruction requires the origin of each contributing message part, or a separate bounded replay of the archived transcript prefix. V1 uses the sanitized index. Hook injection, inferred decisions, verbatim export and launching a host belong to other workflows.

## Rejected alternatives

Selecting whichever session is newest can show a different conversation when several agents are active. A second event journal would add another ingestion, deletion and replica-merge lifecycle. The existing vault already owns the evidence for the latest indexed exchange. [Session ownership](../../../adr/027-vault-is-sole-session-store.md)
