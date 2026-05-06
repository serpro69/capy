# ADR-021: Session JSONL format resilience strategy

**Status:** Accepted
**Date:** 2026-05-05

## Context

Sessionflow RAG (issue #24) parses Claude Code's JSONL session files to index conversational history into capy's FTS5 knowledge base. The JSONL format is **undocumented** — our parser was reverse-engineered from analysis of 96 sessions across 2 active projects. Claude Code can change this format at any time with no notice or changelog.

The parser (`internal/session/parse.go`) extracts human/assistant text, tool names, away summaries, and sub-agent conversations from session files. It must handle format evolution without crashing, corrupting the knowledge base, or requiring manual intervention.

### What we observed

Each JSONL line is a JSON object with a `type` field routing to message types (`user`, `assistant`, `system`, `attachment`, `permission-mode`, `file-history-snapshot`, etc.). User message content can be a bare string or an array of typed blocks. Assistant messages use progressive snapshots — multiple JSONL lines with the same `message.id`, each containing a more complete view of the response. A `version` field (e.g., `"2.1.118"`) is present on most lines.

### Failure modes

| Change type | Example | Current behavior |
|---|---|---|
| New message type | `type: "context-summary"` | Silently skipped — no extraction, no error |
| New content block type | `{"type": "citation", ...}` in assistant array | Ignored — only `text` and `tool_use` are extracted |
| New top-level field | `agentMode: true` | Ignored — `json.Unmarshal` into known struct skips unknown fields |
| Field rename | `sessionId` → `session_id` | SessionID falls back to filename; `StartTime` may be zero |
| Structural change | Content format changes from array to object | `json.Unmarshal` into `[]contentBlock` fails, returns empty — session gated out by `IsIndexable()` |
| Encoding change | Not JSONL anymore | Every line fails to parse, logged as warnings, empty session returned |

## Decision

### Approach: silent degradation with observability

The parser degrades gracefully rather than failing loudly on format changes. The rationale: session indexing is a background enhancement — a broken parser should never block Claude Code's MCP server startup or corrupt existing indexed content.

**Current resilience mechanisms:**

1. **Unknown types are skipped.** The `switch` on `type` has no `default` panic — unrecognized values fall through silently.
2. **Malformed lines are logged and skipped.** `json.Unmarshal` failures produce a `slog.Warn` with file, line number, and error. Parsing continues with the next line.
3. **Scanner errors are non-fatal.** If `bufio.Scanner` hits `ErrTooLong` (line >16MB), lines read so far are still processed. The session is not discarded.
4. **Progressive snapshot deduplication** uses `message.id` to keep only the last (most complete) snapshot. If the dedup key moves or disappears, each snapshot becomes a standalone message — producing duplicate text but not data loss.
5. **`IsIndexable()` gate** requires min 2 non-subagent turn pairs and 200+ chars of assistant text. Sessions where parsing extracted nothing are automatically excluded from indexing.
6. **`json.RawMessage` for content.** User and assistant content is stored as raw bytes until extraction, allowing the parser to handle both string and array formats without a rigid schema.
7. **Type inference fallback.** When the top-level `type` field is missing, the parser falls back to `message.role` — handling a format variant observed in real progressive snapshot lines.

**Observability (added in sweep, Task 4):**

The sweep orchestrator logs a warning when a non-trivial session file (>1KB) produces 0 turn pairs, including the session's `version` field. This surfaces format degradation without blocking indexing:

```
WARN session parsed to 0 turns version=2.3.0 file=abc123.jsonl size=45032
```

A cluster of these warnings after a Claude Code update is the signal that the parser needs updating.

### What this does NOT do

- **No version pinning.** We do not reject sessions with `version` above a known ceiling. This would block all session indexing on every Claude Code update until someone bumps the ceiling — too disruptive for a background feature.
- **No schema validation.** We do not validate that parsed fields meet expected types beyond what `json.Unmarshal` enforces. A field changing from string to int would produce a zero value, not an error.
- **No format migration.** If the format changes fundamentally (e.g., not JSONL), the parser produces empty results rather than attempting to detect and adapt.

## Consequences

### Positive

- Claude Code updates never break the MCP server. Session indexing may degrade to zero indexed sessions, but all other capy functionality continues.
- No maintenance burden from version pinning — no ceiling to bump on every release.
- The `IsIndexable()` gate prevents noise from entering the knowledge base when parsing fails partially.

### Negative

- Format breakage is **silent by default** at the session level. A user won't notice that session search stopped working until they search for something and get no results. The sweep-level warning log is the only automated signal.
- Partial extraction errors (e.g., tool names stop being extracted because the block type changed from `tool_use` to `tool_call`) are invisible — the session still indexes, just with degraded metadata.

## Future improvements

If session RAG proves valuable enough to warrant stronger guarantees:

1. **Canary integration test** (Task 6.3). ✅ Implemented in `internal/session/canary_test.go`. Discovers real session files from `~/.claude/projects/`, parses up to 20 across projects, and asserts basic invariants (non-empty session ID, non-zero assistant chars when turn pairs exist, non-zero StartTime). Uses `t.Skip` when no sessions are found (CI-safe).

2. **Version-aware warnings.** The sweep could track the highest `version` value seen in successfully-parsed sessions and warn when it encounters a version jump that correlates with degraded extraction. This avoids hard pinning while still surfacing version-correlated breakage.

3. **Format fingerprinting.** Hash the set of `type` values and content block types seen in a session file. Compare against a known-good fingerprint. Divergence triggers a structured warning with the new types/blocks discovered — actionable for updating the parser without reading raw JSONL.

4. **Version ceiling with grace period.** If breakage frequency justifies it: pin a `maxTestedVersion`, allow sessions above it to parse (no blocking), but log at `WARN` level. A scheduled task or CI job bumps the ceiling after verifying the parser against new-version sessions.

## References

- [Design doc: Session File Format](../wip/sessionflow-rag/design.md#session-file-format) — observed JSONL taxonomy
- [ADR-017: Source kind separation](017-source-kind-separation.md) — `KindSession` lifecycle
- [GitHub issue #24](https://github.com/serpro69/capy/issues/24) — feature request
