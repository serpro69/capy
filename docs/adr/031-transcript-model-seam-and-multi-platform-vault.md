# ADR-031: Transcript-model seam and multi-platform vault (Codex sessions)

## Status

Accepted

## Context

Until this decision the vault read Claude Code session JSONL in three independent
readers — `scanner.go` (FTS extraction), `render.go` (`vault show`) and
`transcript.go` (the TUI viewer) — each with its own pass over the raw lines,
sharing only the wire types. The project's knowledge base recorded the recurring
bug shape this produced: a line type handled in one reader was silently lost from
the other two, and a "three parsers must stay in sync" convention existed only to
fight it.

Codex CLI sessions ([issue #77](https://github.com/serpro69/capy/issues/77)) are the
first non-Claude corpus the vault must archive. Codex rollouts live under
`$CODEX_HOME/sessions/YYYY/MM/DD/` and `archived_sessions/`, use a
`{timestamp, type, payload}` envelope, keep tool results on their own lines, record
each spawned sub-agent as a separate top-level rollout, compact append-only, may be
zstd-compressed, and carry the session id both in the filename and in
`session_meta.id`. Adding a Codex reader trio the same way would have doubled the
drift surface to six readers.

Older capy binaries are platform-blind. Without a guard, `restore` would write a
Codex row under `~/.claude/projects/`, `resume` would launch `claude --resume` on a
Codex uuid, and an older binary's `merge` would rescan a Codex blob with the Claude
scanner and write it into the destination as `platform = 'claude-code'` with empty
FTS — permanently, because a later same-hash re-import is skipped and reindex trusts
the stored platform.

Full design, corpus evidence and review triage:
[docs/feat/wip/codex-vault-sessions/](../feat/wip/codex-vault-sessions/design.md).

## Decision

1. **One format seam: a pre-policy transcript model.** A `Decoder`
   (`transcript_model.go`; `DecoderFor(Platform)`) turns archived bytes into a
   `Transcript{Meta, []Entry}`. Entries are Human, Assistant, ToolResult or System,
   each anchored to its physical `LineIndex`. An Assistant entry is an **ordered**
   `[]Part` of text parts and `ToolCall`s — order is load-bearing, because the FTS
   row text and the `show` arrow lines interleave text and calls in block order.
   Decoders resolve call↔result correlation, snapshot merging, format-noise
   stripping and diffs themselves; they never sanitize, truncate, exclude or
   collapse. The former readers become thin consumers (`ScanTranscript`,
   `displayMessages`, `transcriptMessages`) that own every policy: secret stripping,
   head/tail bounds, `ftsExcludedResult`, collapse thresholds, labels. Contract
   details consumers rely on: an empty Human/Assistant/System entry is not emitted,
   a ToolResult with an empty body **is**; `System.SearchOnly` preserves the
   existing index-but-never-display asymmetry of generic attachments; the Claude
   title fallback (plain-string user content only) is computed by the decoder as
   `Meta.TitleFallback`.

2. **The refactor is gated byte-identical for Claude.** Golden files for every
   former `switch` arm across the four public readers, a written divergence
   inventory mapping each old reader difference to a model field or a consumer
   policy, and a real-corpus digest canary must all be unchanged before any Codex
   surface lands. `currentIndexVersion` is **not** bumped: Claude extraction did
   not change.

3. **Platform is a stored column, validated in Go.** Migration `0006_platform` adds
   `vault_sessions.platform TEXT NOT NULL DEFAULT 'claude-code'`, nullable
   `parent_uuid` and `idx_sessions_parent` (the index lives in the migration only —
   `schemaSQL` runs before migrations and may reference only columns every
   historical schema has). `Platform` is a closed constant set checked by
   `ParsePlatform`; there is deliberately **no SQL `CHECK`**, so a third platform is
   a constant, not a migration. An empty in-memory `Platform` is Claude on both
   sides (`writePlatform` on write, `Platform.OrClaude()` at display and restore
   dispatch) while `DecoderFor` stays strict — the decoder seam is never loosened to
   absorb a zero value.

4. **Dispatch from the column; sniff only corrupted values.** Import stamps the
   platform the discoverer walked and never sniffs. Reindex, merge, show, the TUI,
   restore and resume dispatch on the stored value. `DetectFormat(firstLine)` is
   Codex-positive only (`type == "session_meta"` with an object payload ⇒ Codex, any
   other JSON object ⇒ Claude, non-JSON ⇒ error) and is consulted in exactly one
   situation: a stored value that is **present but unrecognized**. Reindex rebuilds
   with the sniffed platform, warns, and does not rewrite the column (FTS-only path,
   ADR-025); merge stores the resolved value; an undetectable blob is a per-session
   error, never Claude by default. An **absent** column — a pre-0006 merge source —
   is Claude, unsniffed: 41 of 467 real Claude sessions open with a
   `file-history-snapshot` line that carries none of the "Claude-shaped" keys.

5. **Row identity is the filename uuid; the location hint is a relative path.**
   `SessionFile.UUID` comes from the rollout filename (what Codex's own DB-less
   resume scans for); `session_meta.id` is informational and a mismatch logs a
   warning. `claude_project_dir` keeps its name and generalizes to **the platform's
   location hint** — the mangled project dir for Claude, the slash-separated rollout
   path relative to `$CODEX_HOME` with `.zst` stripped for Codex (the only faithful
   way to restore a local-time filename). Codex archive/unarchive is a move, so the
   first same-hash sighting of a Codex uuid at a new path performs a metadata-only
   `UpdateLocationHint`; discovery walks `sessions/` before `archived_sessions/` so
   an active copy always wins. Import keeps an in-run map of every uuid it has seen
   so two files for one thread never collide on the primary key in one batch. `.zst`
   rollouts are decompressed before hashing: `raw_jsonl`, `content_hash` and
   `size_bytes` are always the plain bytes.

6. **Sub-agents are standalone child rows.** A Codex child rollout is its own
   `vault_sessions` row with `parent_uuid` from its `session_meta` and **no foreign
   key**: a child may be imported before its parent, and deleting a parent must
   neither cascade nor fail (`delete` warns instead). Children are hidden from
   `list` by default, shown from the parent's `show` header, and opened from the
   parent's launch markers in the TUI through a root-routed action (the viewer owns
   no store handle). A child from Codex ≥ 0.147 records no human turn; its title
   falls back to `agent_nickname · agent_role`.

7. **Reader version bumps to 3, and every new platform constant is a reader
   bump.** The first non-Claude row stamps `vault_meta.min_reader_version = 3`;
   `supportedReaderVersion` becomes 3. `markMinReaderVersion` is a monotonic upsert
   that takes the version to stamp, and **every stamping site passes the version its
   own write requires**: the batch writer stamps the highest version any record in
   the transaction needs (2 for a zstd blob, 3 for a Codex row); `compact` passes
   `readerVersionZstd`, so compacting a Claude-only vault leaves the marker at 2. An
   older binary refuses a Codex-bearing vault at open and refuses it as a merge
   source, exactly as the v2 zstd bump did. **Standing rule:** any future platform
   constant is likewise a reader-version bump (`platform_test.go` pairs each
   constant with its `readerVersion*` and fails on an unpaired one). This rule is
   what makes decision 4 sound: a vault this binary opens holds only platforms it
   knows, so an unrecognized value can only be corruption.

8. **Revert variants are skipped loudly in v1.** A `rollout-…_<rollout_id>` file
   (Codex `thread/revert`) shares a prefix with its base rollout and then diverges,
   so neither size nor hash establishes containment and larger-wins could discard
   unique bytes. With zero local samples, discovery recognizes the suffix, warns,
   and reports the count in `DiscoveryReport.SkippedRevertVariants`; only the base
   rollout is archived.

9. **No new setup, env var or opt-out.** Codex sessions are discovered whenever
   `$CODEX_HOME` (default `~/.codex`) holds a rollout root. The startup sweep
   discovers each platform independently and bounds its Codex cost by handing the
   discoverer a skip predicate over the already-archived `(relative path, on-disk
   size)` set, evaluated from directory metadata before any open; the residual
   cost — one bounded first-line read per rollout not archived at its current
   `(path, size)`, other projects included, every start — is accepted. There is no
   env var to disable the sweep or the reader bump; the v2 precedent (upgrade the
   older binary before merging) applies.

10. **Resume for Codex fails loudly.** `capy vault resume` refuses a Codex row
    before any lookup or restore, naming `capy vault restore` and
    `codex resume <uuid>`; launching Codex is a recorded follow-up.

## Consequences

- A new agent CLI is one `Discoverer`, one `Decoder` and one platform constant plus
  a reader-version bump — not a third copy of three readers. A new line type is
  handled once, in the decoder; the "three parsers must stay in sync" convention is
  retired.
- Claude output of `ScanSession`, `RenderText`, `RenderMarkdown` and
  `ParseTranscript` is byte-identical to the pre-model readers, pinned by goldens
  and the parity canary; no Claude reindex was forced.
- Older binaries refuse a vault that holds a Codex row and refuse it as a merge
  source, instead of polluting `~/.claude/projects/` or permanently mislabeling
  rows. A vault that never receives a Codex row stays at marker 2, including after
  `compact`.
- An unrecognized stored platform is treated as corruption: sniffed with a warning
  (reindex rebuilds, merge stores the resolution), never scanned as Claude by
  default; an undetectable blob is a per-session error.
- `apply_patch` joins `diffResultTools`: its boilerplate body is FTS-excluded, the
  `apply_patch <files>` summary stays searchable on the assistant row, `show`
  prints the verbatim body, and the TUI collapses it to a diff marker — the
  Edit/Write treatment, name-keyed in the consumers.
- Codex `exec_command` file dumps are indexed like any `Bash` output (bounded by the
  16 KiB head/tail cap); there is no Read-analog exclusion because no tool name
  distinguishes a read from any other shell command.
- Short ids are platform-aware in the CLI (`Platform.ShortID`: 12 characters for
  Codex, whose UUIDv7 prefixes collide at 8); lookups still accept 8+ characters.
- `Meta.Source` (cli/exec/vscode/mcp/subagent) is decoded but not persisted;
  hiding non-interactive sub-agent sources, archiving revert variants, unifying the
  `SearchOnly` asymmetry and Codex resume are recorded follow-ups in the
  implementation plan.

## Alternatives considered

- **Parallel Codex reader trio behind sniff-and-dispatch.** Fastest to ship with
  zero Claude edits, rejected because it institutionalizes the recorded parser-drift
  bug at twice the surface.
- **Translate Codex to Claude-shaped lines on read.** Zero reader changes, rejected
  because translated lines no longer map to physical `line_index` anchors
  (breaking search-to-view jumps) and Codex semantics with no Claude equivalent —
  tool outputs as separate lines, the `apply_patch` diff living in the call input —
  are lost behind a leaky shim.
- **`Assistant{Text, []ToolCall}` instead of ordered parts.** Simpler struct,
  rejected because it discards the block order that today's assistant text and
  arrow rendering preserve.
- **Fold Codex children into the parent's `vault_files` as sidecars.** Reuses the
  sidecar open path, rejected because the same bytes are also a standalone
  discovery hit, a child can arrive before its parent, and it diverges from Codex's
  own first-class thread model.
- **Sniff-only detection, no `platform` column.** Zero schema change, rejected
  because every dispatch would need a blob read and `list --platform` would need a
  scan.
- **Claude-shaped-key sniffing, and sniffing absent-column rows.** Rejected on
  corpus evidence: 8.8 % of real Claude sessions open with a `file-history-snapshot`
  line, so legacy merges would drop them.
- **Fail loud (no sniff) on an unrecognized `platform` value.** Simpler, rejected
  because one corrupted column would make an intact session unviewable and
  unmergeable when the Codex-positive test resolves it exactly; the reader-version
  rule already excludes the only case where sniffing could mislabel.
- **Keep `min_reader_version` at 2 and document the downgrade gap.** Rejected: the
  platform-blind restore, resume and merge paths of an older binary would pollute
  Claude's projects tree and permanently mislabel Codex rows.
- **A SQL `CHECK` on `platform`.** Rejected so a third platform does not require a
  migration.
- **Rename `claude_project_dir`.** Cleaner name, rejected as a wide refactor for no
  behavior gain that also violates the additive-schema constraint.
- **Key rows by rollout-file identity to archive revert variants now**, or
  **larger-wins across variants.** Rejected: the first changes identity semantics
  for a case with zero samples; the second would silently discard unique bytes.
- **A sweep opt-out env var / a negative `(path, size) → cwd` cache.** Rejected as a
  new env var (confirmed constraint) and as persisted state to invalidate for a cost
  that is one bounded read per file.

## References

- [ADR-021](021-session-jsonl-format-resilience.md) — unknown line types are
  skipped with observability, never fatal
- [ADR-025](025-vault-index-version-and-reindex.md) — `index_version`, FTS-only
  reindex, deferred generic tool-input rendering
- [ADR-027](027-vault-is-sole-session-store.md),
  [ADR-028](028-corpus-agnostic-retrieval-and-rrf-federation.md) — vault as sole
  session store; RRF federation (unchanged by this decision)
- [ADR-030](030-vault-session-names-and-latest-wins-merge.md) — names table;
  platform-native rename records are not promoted (Codex `/rename` likewise)
- [Original vault design](../feat/done/vault/design.md) — its *Not Doing* entry for
  Codex is superseded by this decision
