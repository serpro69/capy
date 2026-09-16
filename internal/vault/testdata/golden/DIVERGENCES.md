# Reader divergence inventory

> Gate for `docs/feat/done/codex-vault-sessions/` Slices 3 and 4 (tasks 3.x, 4.x).
> Every row below maps to a transcript-model field (design.md § The Transcript
> Model) or is marked **consumer policy** / **log-only**. Slices 3/4 may not start
> while a row is unmapped. Frozen behaviour is pinned by `golden_test.go` (this
> directory) and by the real-corpus digest in `parity_canary_test.go`.

The three Claude JSONL readers — `scanner.go` (`ScanSession`, FTS extraction),
`render.go` (`RenderText`/`RenderMarkdown`, `capy vault show`) and
`transcript.go` (`ParseTranscript`, TUI viewer) — each run their own "pass 1"
over the same wire types (`scanner_types.go`). Most of pass 1 is identical and
becomes the Claude decoder; the differences are listed here so the decoder
reproduces the *union* of what the readers extract, and each consumer keeps only
its own policy.

## Shared pass-1 behaviour (identical in all three → decoder core)

These are not divergences; they are listed so the decoder contract is complete.

- A line is parsed as `jsonlLine`; `message` is parsed as `jsonlMessage` only
  when it is present and not the literal `null`. A `message` that is not a JSON
  object (e.g. a string) leaves the line message-less.
- When `type` is empty and `message.role` is set, the role becomes the type.
- `user` lines with a message create a user entry (even when `content` is
  empty/absent — the entry then yields nothing downstream).
- `assistant` lines merge by `message.id`, falling back to the line `uuid`, into
  one entry whose blocks are deduplicated by the `(Type, Text, Name, ID)` tuple.
  The entry keeps the **first** snapshot's line (and timestamp). An assistant
  `content` that fails to unmarshal as a block array still creates/merges the
  entry, with no blocks.
- `attachment` lines: a `queued_command` attachment becomes a user entry whose
  content is `userTextContent(prompt)` (the prompt then flows through the plain
  string user path, so `cleanText` applies to it).
- `pr-link` → system text via `prLinkText` (empty text → no entry).
- `system` with `subtype == "away_summary"` → system text `TrimSpace(content)`
  (empty → no entry).
- Every other `type` (custom-title, agent-name, progress, permission-mode,
  file-history-snapshot, queue-operation, turn_duration, unknown) is skipped.
- Human text: plain-string content → `cleanText`; block-array content → each
  `text` block `cleanText`ed, joined by `\n`. A block array that fails to
  unmarshal (e.g. a non-object element) yields **no** human text and **no**
  tool results for that line.
- Tool results: `toolResultText(content)` — a string, or the `text` blocks of a
  block array joined by `\n` (image/other blocks skipped), trimmed.
- Call ↔ result correlation: a `tool_use_id → {name, summary}` map is built over
  **all** assistant entries before results are processed, so correlation is
  whole-transcript, not positional.
- `toolUseSummary(name, input)` is the shared label: `Read/Edit/Write <file_path>`,
  `Bash <command>`, `Agent <prompt≤200 runes>` for Agent/Task, else
  `<name> <genericInputSummary>` or the bare name. `genericInputSummary`
  sanitises its own tokens (`StripSecrets`) as part of label construction — this
  is the **only** sanitisation that lives below the seam, because all three
  readers already apply it (D22).
- Oversize lines (over the per-line cap) and malformed JSON lines are skipped
  but still advance the physical line index.

## Divergences

| # | Divergence | `scanner.go` | `render.go` | `transcript.go` | Mapped to |
|---|---|---|---|---|---|
| D1 | Title fallback | First `user` line whose `message.content` is a **plain JSON string**; the string is `TrimSpace`d and must be non-empty and not `<`-prefixed. It is **raw** — `cleanText` is *not* applied (a `<system-reminder>` in the middle of the prompt stays in the fallback). Block-array text and `queued_command` prompts are never eligible; scanning continues past ineligible lines to the first eligible one. Stored title = `truncate(sanitize(fallback), 120)`. | — | — | `Meta.TitleFallback` — decoder computes with exactly this rule; scanner applies sanitize → truncate |
| D2 | Explicit title | Last non-empty `aiTitle` on an `ai-title` line (last wins). Stored title = `sanitize(aiTitle)`. | ignored | ignored | `Meta.ExplicitTitle` |
| D3 | CWD / Branch | First `user`-typed line (after role inference) carrying a non-empty `cwd` / `gitBranch` — captured **even when the line has no parseable `message`**. Other line types (assistant lines also carry `cwd` in real sessions) are never consulted. | — | — | `Meta.CWD`, `Meta.Branch` |
| D4 | Start / End time | First / last parseable RFC3339(Nano) `timestamp` across **every valid-JSON line**, including types no reader handles (queue-operation, progress, file-history-snapshot) and lines that produce no entry. Oversize and malformed lines contribute nothing. | — | — | `Meta.StartTime`, `Meta.EndTime` |
| D5 | Entry timestamp | Each entry carries its line's timestamp (merged snapshot: first snapshot's). | — | — | `Entry.Timestamp` |
| D6 | Line anchor | `lineIndex` per entry (first snapshot for a merged assistant). | not tracked | `SourceLine`, same rule | `Entry.LineIndex` |
| D7 | `attachment` line with `message.content` (not `queued_command`) | `attachmentText(message.content)` → **system** row (searchable). On a line that has both a `queued_command` attachment and a message, the queued prompt wins and the message is ignored (early return). | ignored | ignored | `System{Text, SearchOnly: true}` — scanner indexes, render/TUI skip `SearchOnly` |
| D8 | Queued marker | User entry, no flag (no observable effect). | `queued: true` → label `You · queued` | `Queued: true` | `Human.Queued`; scanner ignores it (consumer policy) |
| D9 | Warnings on malformed content | `slog.Warn` on a malformed JSONL line and on malformed assistant `content`. | silent skip | silent skip | **log-only.** The decoder keeps the scanner's warnings (one per malformed line/content); output is identical either way |
| D10 | `scanLines` read error | Returned as `reading session: %w`. | discarded (`_ =`) | discarded | **consumer policy.** `ScanSession` returns the decoder error; `Render*`/`ParseTranscript` ignore it (a `bytes.Reader` never errors) |
| D11 | Per-line byte cap | `scanLineCap` (= `maxScanLineBytes`, 16 MiB) | `renderLineCap` (= `renderMaxLineBytes`, 16 MiB) | `renderLineCap` | Identical values; one decoder cap. The golden harness lowers both together for the oversize case |
| D12 | Snapshot dedup helper | `mergeBlocks` | `dedupBlocks` | `dedupBlocks` | Same rule → one decoder helper |
| D13 | Result body, **excluded** tools (`Read`, `NotebookRead`) | Body dropped (`ftsExcludedResult`); the call stays searchable on the assistant row. | `collapsedToolResult`: `<summary>\n⋯ [output omitted from index — N line(s)]` (`tool result` when no summary) | `Collapsed: true`, full `Body`, `ToolSummary` | **consumer policy** over `ToolResult{CallName, CallSummary, Body}` |
| D14 | Result body, **diff** tools (`Edit`, `Write`) | Body dropped. | Body shown verbatim, prefixed with the summary. | With a `structuredPatch`: collapsed `Diff` marker, `ToolSummary = "<summary> (+a −b)"`, `Body` = unified diff. The **first** diff-tool result on a line claims the line's patch; later ones fall through to their plain body. No/invalid patch → plain body (collapse by threshold only). | **consumer policy**; the diff text itself comes from `ToolResult.Diff` (D15) |
| D15 | Top-level `toolUseResult.structuredPatch` | not read | not read | Read for `diffResultTools` results via `diffBodyFromToolResult` (hunks with no lines dropped; all-empty → not a diff). | `ToolResult.Diff{Text, Added, Removed}` — decoder fills it for the first diff-tool result on the line, exactly as `splitUserContentForViewer` claims it today |
| D16 | Result body bound | `truncateHeadTail(16 KiB)` **after** prefixing. | unbounded | unbounded; **collapse** when > 20 lines or > 2000 bytes | **consumer policy** |
| D17 | Result prefix | `prefixToolResult(summary, body)`; unknown id → unprefixed. | same (non-excluded) | Not collapsed: same. Collapsed: `Body` unprefixed, summary on `ToolSummary`. | **consumer policy** |
| D18 | Result with **empty** body | skipped | skipped | skipped **unless** it is a diff-tool result with a patch → `Diff` marker still emitted (D14) | Decoder **must emit a `ToolResult` even when `Body == ""`**; consumers skip empty bodies |
| D19 | Assistant text composition | Text parts + `toolUseSummary` joined by `\n`, **no arrow**; Task/Agent contribute their `Agent <prompt>` summary like any call. | Text + `→ <summary>` per call, in block order. | Text + `→ <summary>` per call, **except** Task/Agent → a separate `RoleSubagent` marker labelled by `subagentLaunchLabel` (`description` › `subagent_type` › `prompt`, ≤ 100 runes, else `subagent`); the marker is emitted **after** the assistant body. | Ordered `Assistant.Parts`; `ToolCall.Summary`; `ToolCall.Launch{Label}` — the decoder computes **both** the summary and the launch label for Task/Agent |
| D20 | Assistant `ToolNames` | `toolUseNames` in block order (chunker title). | — | — | Derived by the scanner from `Parts` (`ToolCall.Name` in order) |
| D21 | Turn / message indices, `MessageCount` | Human text starts a turn; tool-only user entries continue it; `MessageCount` = user + assistant rows. | — | — | **consumer policy** |
| D22 | Secret stripping | `StripSecrets` on every emitted row (after `TrimSpace`) and on the title. | none (verbatim) | none (verbatim) | **consumer policy** (scanner). Exception noted above: the in-label sanitisation inside `genericInputSummary` is shared by all three and stays in the decoder's summary computation |
| D23 | Launch marker → sidecar mapping | — | — | Count-based: when `len(markers) == len(subagentIDs)` every marker gets `AgentID` and `Openable`; otherwise none. | **consumer policy** (Claude keeps it; Codex uses `Launch.ChildUUID`) |
| D24 | Subagent handling | `ScanSubagent` stamps `SubagentID` on every row. | CLI appends each sidecar as its own section. | `ParseTranscript(raw, nil)` per sidecar. | **consumer policy** |
| D25 | Assistant heading | — | `Claude` / `🤖 Claude` | TUI `roleLabel` | **consumer policy** (platform-aware from Slice 4) |
| D26 | Empty input | Empty `ScanOutput`, no error. | `""` | `nil` | **consumer policy** |
| D27 | Malformed block-array user content | Whole line yields no human text and no results (all three). Scanner does **not** warn here (only `extractUserBlocks` returns empty). | same | same | Decoder: a user entry with no parts; log-only difference none |

## Decoder contract implications

Collected from the rows above; each is pinned by at least one golden case.

1. **`Meta.TitleFallback` is the raw `TrimSpace`d plain string**, not `cleanText`
   output, and only from plain-string user content (D1 — `title_fallback_rules`).
2. **Timestamps come from every valid-JSON line**, not only from lines that
   produce entries (D4 — `metadata_lines`, `malformed_lines`).
3. **CWD/Branch come from user-typed lines only**, including message-less ones
   (D3 — `metadata_lines`).
4. **Emit every `tool_result` block as a `ToolResult`, even with an empty body**;
   the TUI needs the `Diff` on an empty-bodied Edit result (D18 — `edit_variants`).
5. **Task/Agent calls carry both `Summary` and `Launch.Label`** — they differ in
   source keys and length caps (D19 — `subagent_markers_openable`).
6. **`Diff` is claimed by the first diff-tool result on a line** (D15 —
   `edit_variants`).
7. **`SearchOnly` system entries** come from generic attachment message content;
   a `queued_command` on the same line takes precedence (D7 —
   `attachment_message`).
8. **Keep the scanner's warnings** on malformed lines/content (D9 — log-only, not
   pinned by goldens).
9. **A queued prompt is `cleanText`ed** like any plain-string user content
   (shared behaviour — `queued_command`).
