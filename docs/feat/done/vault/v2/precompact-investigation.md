# PreCompact Investigation (Task 0 / V2.0)

> Feature: [vault v2](./design.md) · gates Tasks 14–16 (PreCompact archival)
> Date: 2026-06-17
> Binary: `capy v0.10.2-2-g68deec8-dirty` (debug build with the `CAPY_DEBUG_PRECOMPACT` instrumentation from subtask 0.1)
> Platform: Claude Code (manual `/compact`)

## Method

`handlePreCompact` (`internal/hook/precompact.go`), gated on `CAPY_DEBUG_PRECOMPACT=1`, dumps the raw hook payload and a **hook-time copy** of the `transcript_path` session file to `0600` temp files. The PreCompact hook command in `.claude/settings.local.json` was temporarily pointed at the local debug binary with the env var inline. A real Claude Code session (`7abfb552-…`) had a few turns, then `/compact` was triggered. Artifacts captured: raw payload dump, hook-time session copy, the user's manual `.bak-precompact` snapshot, and the post-`/compact` live file.

## 0.3 — Payload shape

Raw payload for a **manual** `/compact`:

```json
{
  "session_id": "7abfb552-7a9e-4163-9a3f-0c077497ee3a",
  "transcript_path": "/home/sergio/.claude/projects/-tmp-tmp-cDcE1tHtwO/7abfb552-7a9e-4163-9a3f-0c077497ee3a.jsonl",
  "cwd": "/tmp/tmp.cDcE1tHtwO",
  "hook_event_name": "PreCompact",
  "trigger": "manual",
  "custom_instructions": null
}
```

- **`transcript_path`** — absolute path to the main `<uuid>.jsonl`. **Present.** ✅
- **`session_id`** — the session UUID; matches the filename. ✅
- **`cwd`** — the project dir. ✅
- `trigger` is `"manual"` here; expect `"auto"` for context-limit compaction. `custom_instructions` is `null` for a plain `/compact`.

**Conclusion:** session file + UUID + project dir are all resolvable from the payload — exactly what V2.14's handler needs. This matches the `transcript_path` parsing the adapter already does (`internal/adapter/claudecode.go`).

## 0.4 — Timing (content-level, not mtime)

| Artifact | Lines | Relationship |
|---|---|---|
| hook-time session copy | 50 | **byte-identical** to the manual `.bak-precompact` |
| post-`/compact` live file | 80 | first **50 lines byte-identical** to the hook-time copy; +30 appended |

The compaction-summary entry sits at **line 57** — a `type:"user"` message with `isCompactSummary:true` and a `compactMetadata` object:

```json
{"trigger":"manual","preTokens":31652,"postTokens":6873,"durationMs":21268,
 "preCompactDiscoveredTools":["mcp__capy__capy_doctor"],
 "preservedSegment":{"headUuid":"…","anchorUuid":"…","tailUuid":"…"},
 "preservedMessages":{"uuids":["…","…","…"]}}
```

**Conclusion — `/compact` is APPEND-ONLY to the JSONL.** It does not truncate or rewrite earlier lines. It appends a compaction-summary entry (which carries the summarized context the model uses going forward) plus continuation. **The pre-compaction turns remain in the live file** (the hook-time copy is a strict prefix). The hook fires with the full pre-compaction transcript readable.

**Gate criterion (0.6 as originally written):** *"if the hook-time content is already the compacted transcript → STOP."* — **NOT triggered.** Hook-time content is the full pre-compaction transcript. File-based capture is **feasible**.

## Caveats

- Tested **only manual `/compact`, once, on one Claude Code version.** Auto-compaction (`trigger:"auto"`) was **not** tested — the append-only architecture strongly suggests identical behavior, but it is unverified. Future Claude Code versions could change the on-disk behavior.
- The second session file in the project dir (`90b10852-…`) is **this implementation session** co-located in the same `cwd`, **not** a `/compact` artifact (confirmed via `.capy/guidance-90b10852-….json`). `/compact` does **not** create a new session file.

## 0.6 — Decision gate

The investigation answers the literal gate (feasible) but surfaces a finding **beyond its scope** that changes the cost/benefit: **`/compact` does not lose file content.** The vault archives `raw_jsonl` verbatim, and the server-startup sweep reads the full append-only file — so the pre-compaction turns are *already* preserved in the active `vault_sessions` row (as a prefix). A dedicated `vault_snapshots` cold-storage table built to "preserve pre-compaction content" therefore addresses a data loss that **does not occur at the file level**.

**Residual real value:** a session could be `/compact`-ed and its file **deleted before the next sweep archives it** (observed in practice — users prune old session files). A PreCompact hook that archives the active session on compaction closes that specific gap.

### Options

- **A — Build as designed (Tasks 14–16):** `vault_snapshots` cold storage + snapshot CLI. *Pro:* future-proof if CC ever truncates; an explicit restorable pre-compaction point. *Con:* builds cold-storage machinery for a loss that doesn't occur; the "snapshot" is a prefix already recoverable from the active row.
- **B — Re-scope (RECOMMENDED):** replace Tasks 14–16 with **one** task — *archive the active session on PreCompact* (run the existing single-session `Import` on the `(dir, uuid)` from the payload so the active `vault_sessions` row exists/updates before the user moves on). **Drop** the `vault_snapshots` table + snapshot CLI. Delivers the durability guarantee (prompt archival before possible deletion) at a fraction of the complexity, and matches the evidence.
- **C — Drop PreCompact entirely:** rely on the existing server-startup sweep (it already reads the full append-only file). *Pro:* zero new code. *Con:* loses the "archive before deletion" guarantee.

**Recommendation was B; decision is C (drop for now).**

### Decision (2026-06-17): Option C — drop PreCompact archival for now

PreCompact archival (Tasks 14–16) is **dropped**, not deferred-with-a-task.
Rationale: `/compact` is append-only — it loses no file content — and the existing
server-startup sweep already archives the full verbatim transcript, so the
durability gain does not justify the `vault_snapshots` cold-storage machinery (or
even the lighter archive-on-compact hook) at this time. `handlePreCompact` returns
to a pure no-op.

**Re-trigger (pick it up if any becomes true):**

- A Claude Code version changes `/compact` to **truncate or rewrite** the session
  JSONL (re-run this investigation against that version — set
  `CAPY_DEBUG_PRECOMPACT=1`; the instrumentation is in this branch's git history).
- **Auto-compaction** (`trigger:"auto"`) is found to differ from the manual
  `/compact` tested here — untested; verify before relying on either path.
- A need arises to guarantee archival **before** a session file can be pruned (the
  one real gap; met by the Option-B archive-on-compact hook).

**Revival pointer:** the debug instrumentation (`dumpPreCompactDebug` /
`writeDebugTemp` + their tests) lived in `internal/hook/precompact.go` and
`internal/hook/precompact_test.go` on branch `vault_v2` — recover from git history.
