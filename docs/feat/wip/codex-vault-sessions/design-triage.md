# Design triage — corroboration of four independent design reviews

**Date:** 2026-09-13 · **Inputs:** four independent `kk:review-design` runs (R1–R4) · **Method:** every claim re-verified against `design.md` / `implementation.md` / `tasks.md` as written, the cited code at `master`, and the local corpora (464 Claude sessions under `~/.claude/projects`, 168 Codex rollouts under `~/.codex/sessions`). Nothing below is taken from a review on trust.

**Status:** **applied 2026-09-13.** Every item marked **Fix** below is now reflected in `design.md`, `implementation.md` and `tasks.md`. Decisions taken by the maintainer: **E** — bump `min_reader_version` to 3 on the first Codex write (older binaries refuse the vault); **F** — skip `_<rollout_id>` revert variants loudly in v1 (archiving them is a recorded follow-up). This file is kept as the evidence record for the revision; re-run `/kk:review-design` against the revised docs.

## Summary

| # | Claim (reviews) | Verdict | Severity | Action |
|---|---|---|---|---|
| A | `idx_sessions_parent` in `schemaSQL` breaks legacy vault open (R3, R4) | **Confirmed** | P0 | Fix |
| B | `DetectFormat` Claude rule rejects ~9 % of real Claude first lines; legacy-source sniff drops sessions; import never sniffs (R2, R4) | **Confirmed** | P1 | Fix |
| C | Same-run duplicate UUIDs (active + archived, revert variants) hit the PK inside one batch → `StatusError`; dry run double-counts; same-hash path move never updates the location hint (R3, R4) | **Confirmed** | P1 | Fix |
| D | Codex-only sweep skipped by Claude early returns (R3) | **Confirmed** | P1 | Fix |
| E | Older-binary downgrade is unsafe, not "renders empty"; reader marker cannot be raised (R2, R4, R1) | **Confirmed** | P1 | Fix |
| F | Larger-wins is unfounded for revert rollouts; "correctly handled" over-claims (R2, R4) | **Confirmed** | P1 | Decide + fix |
| G | TUI child-open assigned to a model with no store; no return stack (R4) | **Confirmed** | P1 | Fix |
| H | Child rollouts may have no human turn (R1) — corpus shows 3/13 do, plus three new consequences | **Confirmed, premise wrong in design** | P1 | Fix |
| I | `Meta.ParentUUID` has no handoff into `Session` (R4) | **Confirmed** | P2 | Fix |
| J | `capy_search` result shape: constraint vs MCP vs verification contradict (R2, R3) | **Confirmed** | P2 | Fix wording |
| K | Task 2 "moves" pass-1 vs Task 3 "deletes" it, with 3–6 parallel (R2) | **Confirmed** | P2 | Fix wording |
| L | Startup sweep cost unbounded under the 30 s budget (R2) | **Confirmed** | P2 | Fix |
| M | Success criterion 1 / round-trip wording wrong for `.zst` (R1, R3) | **Confirmed** (wording; not "impossible") | P2 | Fix wording |
| N | `Assistant{Text, []ToolCall}` loses text/call interleaving (R3) | **Partially valid** — 0 occurrences in 464 sessions | P3 | Adopt ordered parts anyway (cheap) |
| O | Child vault UUID must equal parent-resolved id; unstated (R1) | **Partially valid** — holds 11/11 locally | P3 | Add assumption + canary |
| P | Whole-file human fallback can miss individual turns (R4) | **Not evidenced** — 168/168 files reconcile exactly | P3 | Strengthen canary only |
| Q | Six `../../` links broken; corpus is 168 not 164; Task 13.5 partly done (R3) | **Confirmed** | P3 | Fix |
| R | Deferred item 1 is garbled (R2) | **Confirmed** | P3 | Fix wording |
| S | Oversize-line golden needs a cap hook that does not exist (R2) | **Confirmed** | P3 | Fix |
| T | Scanner contract omits "assistant row text includes tool summaries" (R2) | **Confirmed** | P3 | Fix wording |
| U | `ParentUUID` ignores `SubAgentSource::Review/Compact` (R2) | **Partially valid** — variants exist, carry no parent id, 0 local samples | P3 | Record in Deferred |
| V | `apply_patch` Diff built regardless of result success (R1) | **Plausible, unobserved** (21/21 local results succeeded) | P3 | Gate on success (cheap) |
| W | Add an explicit reader-divergence inventory step (R1) | **Valid process improvement** | P3 | Add to Slice 1 |
| X | Tasks 2–6 are horizontal layers (R3) | **Partially valid** — deliberate Risk-First; slicing notes missing on 3–6 | P3 | Annotate |
| Y | Old-binary reindex could overwrite Codex FTS (R1) | **Not a risk** via reindex (version gate); the merge path is — folded into E | — | — |
| N1 | **New:** `<recommended_plugins>` user-role noise absent from the noise list | Confirmed (3 files) | P1 | Fix (part of H) |
| N2 | **New:** `spawn_agent.message` is encrypted in 0.147+ → unusable as Summary/Launch label | Confirmed | P2 | Fix (part of H) |
| N3 | **New:** children with no human turn get an empty title; zero-human warning fires on them | Confirmed | P2 | Fix (part of H) |

## Confirmed findings — detail and evidence

### A. Index in `schemaSQL` runs before migration 0006 (P0)

`internal/vault/store.go` `openDB`: `schemaSQL` executes at line 439, `migrateVault` at line 444. On a pre-0006 vault `CREATE TABLE IF NOT EXISTS` is a no-op, so `CREATE INDEX IF NOT EXISTS idx_sessions_parent ON vault_sessions(parent_uuid)` in `schemaSQL` (design § Storage Model; implementation Slice 5.1; tasks 5.1 "mirror columns and index in schemaSQL") fails with "no such column" and every existing vault refuses to open.

**Fix.** Keep the two columns in the fresh `CREATE TABLE`; create the index **only** inside `migrate0006AddPlatform`, which runs on fresh and legacy vaults after the columns exist. Keep the pre-0006 open test as the gate. Add to "Invariants that bite": `schemaSQL` may only reference columns that exist in every historical schema.

### B. `DetectFormat` rejects real Claude sessions; merge sniff drops them (P1)

Local tally of the **first line** of all 464 Claude sessions:

| first-line `type` | count | has `uuid` | has `sessionId` | has `message` |
|---|---|---|---|---|
| `last-prompt` | 314 | no | yes | no |
| `mode` | 74 | no | yes | no |
| `file-history-snapshot` | 41 | no | **no** | **no** |
| `permission-mode` | 34 | no | yes | no |
| `queue-operation` | 1 | no | yes | no |

None starts with a `user`/`assistant` line, so the design's "type in the known set" clause never fires; the key-presence fallback saves all but `file-history-snapshot`: **41/464 (8.8 %)** would return an error. Design § Reindex, Merge, Restore says a source without the column is Claude "confirmed by sniffing the first line", and Slice 8.2 records `StatusError` on failure, so merging any pre-0006 vault silently drops ~1 in 11 sessions. Design line 129 also claims import surfaces a sniff error as `StatusError`, but import never sniffs (platform comes from the discoverer). R4's related point stands: the existing scanner tolerates a malformed or oversize first line by skipping it, so first-line sniffing is strictly less resilient than the Claude decoder it would gate.

**Fix.** Make detection Codex-positive only: Codex iff the first line parses and `type == "session_meta"` with an object `payload`; otherwise Claude for any parseable JSON object; error only for non-JSON. Absent column (pre-0006 source) → Claude, no sniff (that schema predates Codex support). Reserve the sniff for a **present but unrecognized** `platform` value. Correct line 129.

### C. Same-run duplicate UUIDs are not reconciled (P1)

`import.go` decides skip/insert/replace from `store.SessionDigest` (committed DB state only, line ~199) and queues writes into a batch; `store.WriteBatch` writes the batch in one transaction and rolls back on any error (lines 686–705). Two files for one thread uuid in the same run (`sessions/` + `archived_sessions/`, or a base file + a `_<rollout_id>` variant) both see `found=false`, both queue as inserts, the batch fails on the primary key, and the per-session retry reports the second as `StatusError` — not the `skipped` the design's Assumption 7 test expects. Dry run reports both as `new`. Separately, a same-hash file that has **moved** (archive/unarchive) is skipped without updating `claude_project_dir`, so the restore location depends on first-import order.

**Fix.** Specify an in-run reconciliation map (uuid → pending hash/size/decision) consulted before `SessionDigest`, applied identically in dry run. Define the location policy explicitly: on a same-hash path change, perform a metadata-only update of `claude_project_dir` (both `sessions/` and `archived_sessions/` are legitimate; the latest discovered path wins), and test active/archive ordering both ways.

### D. Codex-only sweep is skipped (P1)

`server.go` 254–263: `DiscoverSessions(sessionDir)` error → `return`; zero sessions → `return`. Slice 8.1 says "after the Claude discovery, discover Codex rollouts", which as written never runs when the project has no Claude sessions — exactly the Codex-only case.

**Fix.** Discover per platform independently, aggregate, then decide; a per-platform discovery failure is a debug/warn line, not a return. Add a sweep test with no Claude root.

### E. Downgrade safety is misstated; reader marker cannot be raised (P1)

Design § Storage Model: "an older binary … renders it empty, but corrupts nothing." Verified older-code paths that are platform-blind:

- `cmd/capy/vault.go` `defaultRestoreRoot` joins `ClaudeProjectsDir()` with `claude_project_dir`, so an old binary restores a Codex row to `~/.claude/projects/sessions/2026/…/rollout-…jsonl/<uuid>.jsonl` (junk directories inside Claude Code's tree); `resume` then launches `claude --resume <codex-uuid>`.
- `merge.go` 221–235 rescans every source blob with the Claude scanner and writes the destination row. An old binary merging **from** a 0006 vault does not select `platform`, so the destination row lands as `platform='claude-code'` (column default) with **empty** FTS at the current `index_version`. That misclassification is permanent: a later disk re-import of the same bytes is same-hash → skipped, and reindex trusts the stored platform.
- `codec.go` `markMinReaderVersion` is `INSERT OR IGNORE` (line ~116): it can create the marker but cannot raise an existing `2` to `3`. `supportedReaderVersion` is `2`.
- R1's reindex concern is not a risk on its own: Codex rows are stamped `index_version = 3`, equal to an old binary's `currentIndexVersion`, so old-binary reindex never selects them. The merge path above is the real hole.

**Fix.** Bump the reader contract: stamp `min_reader_version = 3` on the first Codex row write and set `supportedReaderVersion = 3`; change `markMinReaderVersion` to an upsert that raises monotonically (`ON CONFLICT DO UPDATE` with a numeric comparison). Consequence to document: once a vault holds a Codex row, older binaries refuse to open it — which is the marker's stated purpose (v2 precedent). `MergeFrom` already runs `checkReaderVersion` on the source, so the misclassifying merge is refused too. Re-index the `kk:arch-decisions` note that currently says "not bumped".

### F. Revert rollouts: larger-wins over-claims on zero evidence (P1)

Design line 33 promises every rollout verbatim; line 198 maps `_<rollout_id>` variants onto the thread uuid with larger-wins; Assumption 5 says they are "correctly handled". Per the research, `thread/revert` writes a **new immutable file** for the same thread and Codex appends to the new one; the two files share a prefix then diverge, so size does not establish containment. Larger-wins can keep the abandoned branch (and point `claude_project_dir` at a file Codex no longer appends to) until the live file outgrows it, then silently discard the abandoned branch's unique bytes. Local corpus: **0** revert files, so nothing here is testable now.

**Decision needed.** Either (a) key rows by rollout-file identity (`<thread>` for the base file, `<thread>_<rollout_id>` for variants — prefix lookup already returns `AmbiguousUUIDError` candidates), preserving every file; or (b) declare revert variants unsupported in v1: import the base file, **skip `_<rollout_id>` files with a warning** naming the thread, and record the follow-up. Recommendation: (b). It is loud, reversible, and does not touch identity semantics for an unobserved case. Rewrite Assumption 5 accordingly and drop "correctly handled".

### G. TUI child navigation has no viable ownership (P1)

`tui/viewer.go` 26–30: "It owns no store handle: the app fetches the session + sidecars and hands them in via loadSession". `tui/app.go`: `Model` owns `store dataStore`, `ctx`, and `openSession` (lines ~623–644). Task 10.3 tells `viewer.go` to call `GetSession`/`GetFiles` and "push a nested viewer" — the viewer cannot, and `app.go` is missing from the slice.

**Fix.** Define a root-routed `viewerOpenChild{uuid}` action: `Model` loads the child through `dataStore`, maintains a stack of viewer frames (session, files, scroll/focus), `esc` pops a frame, and a direct open from list/search still returns to its originating mode. Route the not-archived status through the root status line. Add `tui/app.go` to Slice 10 and the file table.

### H. The sub-agent premise is wrong for CLI ≥ 0.147, with three consequences (P1)

Design line 200 and § Sub-agent Model: "a Codex child rollout carries one human turn (the spawn prompt)". Corpus, 13 child rollouts:

| CLI | children | `user_message` events | `UserMessage` items | non-noise user response items | `task_started` |
|---|---|---|---|---|---|
| 0.125 / 0.137 | 10 | 1 each | 0 | 1 each | 1 |
| **0.147** | **3** | **0** | **0** | **0** | 1 |

The 0.147 children have no human turn by **any** path. Their only user-role response item is `<recommended_plugins> Here is a list of plugins…`; the rest is `developer` instructions, 1–3 assistant messages and `custom_tool_call`s. R1's feared outcome (zero-message exclusion) does **not** occur — the assistant rows make `MessageCount ≥ 1` — but the stated reasoning is false and three real defects follow:

- **N1 (P1).** `<recommended_plugins>` is not in the design's noise list. Because events yield zero human entries for these files, the `response_item` fallback runs and would index that block as a **human turn** and make it the **title**. Fix: in the fallback, treat any user text whose trimmed form starts with `<` as noise (the rule the Claude title fallback already uses), keep the explicit list as documentation, and add `<recommended_plugins>` to it.
- **N2 (P2).** In 0.147 the parent's `spawn_agent` `arguments.message` is **encrypted** (`gAAAAAB…`), so `Summary = "spawn_agent <message…>"` and `Launch.Label` from `message` are gibberish. Fix: prefer `task_name` and `agent_type` from the arguments, then the parent-side `SubAgentActivity.agent_path`, and only then `message`.
- **N3 (P2).** With no human turn, `TitleFallback` is empty → blank titles in `list`/`show`; and the zero-human **warning** (design § Observability) fires on all three files, so the canary's "never fires" assertion fails on the real corpus. Fix: for `Meta.Source == subagent` (or `thread_source == "subagent"`), `TitleFallback` = `agent_nickname · agent_role` (`agent_path` as a further fallback) from `session_meta`; do not emit the zero-human warning for subagent-sourced files; reword Assumption 2 and the canary to exclude subagent rollouts from the "must have a human turn" expectation.

Update "13 of 164" to 13 of 168, and the Sub-agent Model text to say the spawn prompt is *not* recorded in the child from 0.147 onward.

### I. `Meta.ParentUUID` never reaches `Session` (P2)

Slice 3.1 keeps `ScanSession(p, r) → *ScanOutput`; `scanSessionAndSubagents` returns `(*ScanOutput, fts, chunks)`; Slices 5.3/7.3 tell `buildRecord` to set `ParentUUID` "from `Meta`". `ScanOutput` (`scanner.go` 69–79) has no such field and nothing returns `Meta`. An implementer must invent the API or decode twice.

**Fix.** Extend `ScanOutput` with `Platform`, `ParentUUID`, `Source` (the persisted decoder metadata), populated by `ScanTranscript` from `Meta`; or have `scanSessionAndSubagents` return `Meta`. Pick one and state it in design § Consumer contracts and Slice 3.

### J. `capy_search` result shape (P2, wording)

Design line 54 ("the `capy_search` result shape … do not change") contradicts line 255 (metadata "adds `platform`") and line 285 ("search result columns change"). **Fix:** line 54 → "the federation plumbing, RRF ranking and `session:<uuid>` tagging are untouched; the formatted meta line gains `platform` / `parent_uuid`"; Task 11.1's "unchanged output for a Claude hit" then needs the caveat that the platform label is added.

### K. Task 2 vs Task 3 wording (P2)

Slice 2.3 "Move the pass-1 logic out of the three readers" reads as removing the reader loops, which would break the build until Tasks 3/4 land while Tasks 3–6 are marked parallel. **Fix:** Task 2 *adds* `claude_decoder.go`, freely relocating shared helpers (same package, caller-safe) but leaving the three reader loops intact; Tasks 3/4 replace the loops and delete the orphans.

### L. Startup sweep cost (P2)

`server.go` ~173: the sweep runs under `context.WithTimeout(ctx, 30*time.Second)`; on expiry remaining sessions are silently deferred to the next `import`. Codex has no auto-cleanup, so `sessions/` grows forever, and a `.zst` first-line read decompresses the frame. **Fix:** (1) run the Claude sweep before Codex so Codex growth cannot starve it; (2) before any first-line read, skip rollouts whose relative path and on-disk size match a stored Codex row (one query loading `(claude_project_dir, size_bytes)` for `platform='codex'` into a map — append-only files change size when they change); (3) record the bound as an assumption with a canary timing over the local corpus.

### M. `.zst` round-trip wording (P2)

Design line 33 says "original relative path … SHA-256 equal to the source" while § Discoverer normalizes `.zst` names and § Import hashes decompressed bytes. Not impossible, just mis-stated. **Fix:** "restored path equals the discovered path minus any `.zst` suffix; SHA-256 of the restored bytes equals the stored `content_hash`, i.e. the decompressed input."

### Q. Links, counts, Task 13.5 (P3)

Six links in `design.md` § References use `../../` and resolve to `docs/feat/architecture.md` / `docs/feat/adr/…`, which do not exist; they need `../../../`. (`docs/feat/done/vault-session-renames/design.md` has the same pre-existing defect — mention only, not ours to fix here.) Corpus counts: 168 rollouts now, not 164 (design lines 9, 33, 127, 224, 246; implementation 6.6, 7.5). Task 13.5: the `research.md` banner update is already done; the `kk:project-conventions` note is **not** indexed and must not be until the code lands (the three-parser convention still holds today) — reword 13.5 to the remaining half.

### R, S, T, W (P3)

- **R.** Implementation § Deferred item 1 is garbled ("delete its `state_5.sqlite` row is not allowed … instead pick"). Rewrite: what is forbidden (touching Codex state), then the procedure (copy a rollout Codex has never seen into `sessions/…`, run `codex resume <uuid>` from its cwd).
- **S.** `maxScanLineBytes` and `renderMaxLineBytes` are `const`; `scanLines` takes the cap but every public reader passes the constant. Name the hook: package-level `var scanLineCap = maxScanLineBytes` (and the render equivalent) overridden in tests, or drop the oversize golden and keep the existing unit test.
- **T.** `scanner.go` `extractAssistantText` joins text blocks **and** tool-call summaries in block order. State in design § Consumer contracts that the assistant row text is `Text` + summaries, that a tool-only assistant entry is still an assistant row, and that this is what makes `exec_command`/MCP/`web_search` calls searchable for Codex and counts them in `MessageCount` (parity with Claude tool-only turns, not inflation).
- **W.** Add a Slice 1 sub-step: inventory every behavioral divergence between the three readers (already known: plain-string title rule, scanner-only `attachmentText`, scanner-only warn logs) and map each to a model field or a documented log-only difference before Slice 3/4.

## Partially valid — narrower action

- **N.** Interleaving: 0 of the local assistant lines have a text block after a `tool_use` block, and snapshot merging preserves first-seen order, so Claude byte-identity is not at risk today. The model is new, so make `Assistant` carry ordered parts anyway; it costs nothing and removes a future failure of the parity gate.
- **O.** Parent-resolved child ids vs child files: 11 parent-side ids, all 11 present as child rollouts; 2 children have no parent-side record (parents not local). Add as Assumption 11 with a canary; not a design change.
- **U.** `SubAgentSource` has unit variants `Review`, `Compact`, `MemoryConsolidation` with no parent id; only the lifted `parent_thread_id` could link them. Local corpus: 13/13 `thread_spawn`. Record in Deferred 4 (hide non-interactive/subagent sources) since `Meta.Source` is not persisted.
- **V.** 21/21 local `apply_patch` results begin "Success. Updated the following files". Gate `Diff` on that prefix or `metadata.exit_code == 0`; a failed patch keeps its plain body.
- **X.** Tasks 1–2 carry slicing notes; 3–6 do not. Annotate 3/4 as Risk-First ("Claude output unchanged" is the testable path), 5 as Contract-First (schema), 6 as Contract-First (decoder). The sequencing was a deliberate, user-confirmed choice; no design change.

## Not valid / not evidenced

- **P.** Per-file reconciliation over the whole corpus: 121/121 legacy files have `user_message` count == noise-filtered user response-item count and every event text appears among the response items; 47/47 paginated files have `UserMessage` count == noise-filtered count. No partial loss is evidenced. Adopt the count reconciliation as the canary assertion (cheap and strictly stronger than zero-vs-nonzero); no decoder change.
- **Y.** Old-binary reindex cannot touch Codex rows (version gate). Folded into E.

## Follow-ups this triage creates

1. Apply fixes A–M, N1–N3, Q–T, W to `design.md` / `implementation.md` / `tasks.md`; re-run `/kk:review-design`.
2. Decide F (revert rollouts) explicitly; record the choice in design § Assumptions and Rejected Alternatives.
3. Re-index the `kk:arch-decisions` notes if E changes the reader-version decision (they currently say "not bumped").

---

## Round 2 — 2026-09-13, two independent reviews (R5, R6) plus a code-level self-review

**Status: applied 2026-09-13.** Every **Fix** below is reflected in the three docs. Method as above: each claim re-checked against the revised docs, the code at `master`, and the local corpora (now 467 Claude sessions, 168 Codex rollouts).

| # | Claim | Verdict | Sev | Action |
|---|---|---|---|---|
| AA | `compact.go` `markCompressed` also stamps the reader marker; plan names only `codec.go`, so `compact` on a Claude-only vault would stamp 3 (R5, R6, self) | **Confirmed** (`compact.go:204`) | P1 | Fix: named `readerVersionZstd`/`readerVersionPlatform`; compact passes 2; compact test |
| AB | Location policy is written platform-neutral; for Claude rows it compares an unused `RelativePath` against a mangled dir, and loose `--source` imports change `ProjectDir` (R6) | **Confirmed** (`discovery.go:27`, `vault.go:1267`) | P1 | Fix: gate on `PlatformCodex`; loose-source Claude test |
| AC | Restore round-trip gate says `sha256(restored) == content_hash`; the hash is `computeContentHash`'s framed digest keyed `<uuid>.jsonl` (self) | **Confirmed** (`import.go:454-475`) | P1 | Fix: byte equality (or recomputed framed hash) everywhere the gate appears |
| AD | Zero-human warning keyed on `task_started` fires on real files (self, corpus) | **Confirmed — 23/168 rollouts** are aborted-at-startup shells: 10 lines, `task_started` + `turn_aborted`, 0 assistant, 0 human, all `source=cli`. The canary's "never fires" assertion would fail on the real corpus | P1 | Fix: warn only for non-subagent files with ≥1 Assistant and 0 Human entries; shells are ordinary zero-message exclusions; success criterion 1 and Slice 7.5's "168 candidates" reworded (145 `new`, 23 `excluded`) |
| AE | "Unrecognized platform → sniff" can mislabel a third platform's blob as Claude (R5) | **Partially valid.** True as stated, but the case is excluded once "a new platform constant is a reader-version bump" is a rule: an older binary refuses such a vault at open. Kept the sniff (exact for the two platforms that can exist; failing loud would make a session with one corrupted column unviewable) and made the rule explicit; reindex never rewrites the stored value, merge stores the resolved one | P2 | Fix wording + rule (design § Format Identification, § Reader version, ADR-031, Assumption 13) |
| AF | Task 5 uses symbols Tasks 3 and 7 introduce; Task 6.5 needs Tasks 3 and 4 (R5, R6) | **Confirmed** | P2 | Fix: Task 5 depends on 3; `buildRecord` uses the constant until Task 7; consumer assertions moved to Task 7.6; graph redrawn |
| AG | Sweep size pre-filter is specified *after* the discoverer's eager first-line reads; the bound is O(corpus) for other projects' rollouts, not O(changed); `.zst` first line via `DecodeAll` is a whole-file decompression (R5, R6) | **Confirmed** | P2 | Fix: `Skip` predicate passed into the Codex discoverer and evaluated from `DirEntry` metadata; streaming zstd reader; real bound stated and pinned by a test; negative cache recorded as Not Doing |
| AH | Location policy needs the stored hint and no query returns it; dry-run and `ftsOnly`+moved unstated; runs outside the batch tx (R5, R6) | **Confirmed** (`store.go:1154` `SessionDigest`) | P2 | Fix: `SessionDigest` returns the hint; own tx at decision time; dry run reports without writing; `ftsOnly`+moved does both |
| AI | TUI has its own 8-char `shortID` (R5) | **Wrong number, right location:** `tui/render.go:308` truncates at **12** already. The design's "12 in the TUI" was a no-op, not a gap | P3 | Fix wording: CLI `shortUUID` only |
| AJ | Row identity (filename uuid vs `session_meta.id`) unstated (R5) | **Confirmed**; corpus: 168/168 agree | P3 | Fix: filename wins, `Meta.PlatformID` mismatch is a warning, canary asserts equality |
| AK | "Every base rollout" vs the zero-message exclusion (R5) | **Confirmed** — see AD for the count | P3 | Fix wording |
| AL | `custom_tool_call_output` second-decode failure unspecified (R5) | **Confirmed** | P3 | Fix: raw string body, no exit line, no `Diff` |
| AM | `DiscoverSessions(root)` keeps its signature so `--source` loses the report (R6) | **Confirmed** | P3 | Fix: `DiscoverSessionsReport` primary, old name a wrapper |
| AN | Restore beside an existing `.zst` twin / live file unspecified (R6) | **Confirmed** | P3 | Fix: twin untouched and noted in `RestoreResult.Notes`; Codex picker behavior recorded as Open Question 5 |
| AO | No Read-analog FTS exclusion for Codex; record as a decision (R6) | **Confirmed** | P3 | Fix: recorded under Consumer contracts and Not Doing; `apply_patch` joins `diffResultTools` as the Edit/Write analog (new decision — see below) |
| AP | No opt-out for the Codex sweep / irreversible marker bump (R6) | **Confirmed trade-off** | P3 | Fix: acknowledged under Constraints and Not Doing; the v2 precedent applies |
| AQ | Task 7 is L, Task 1 is not S (R6) | **Confirmed** | P3 | Fix: retagged; Task 7 split into 7a/7b without renumbering |
| AR | `CheckVaultPlatforms(map[Platform]int)` adds a `platform → vault` import edge (R6) | **Confirmed** (`internal/platform` imports `config`, `hook`, `version` only) | P3 | Fix: strings-only `VaultPlatformRoot`; `go list -deps` check |
| AS | Codex `StartTime` from line 0's envelope timestamp trails `payload.timestamp` (self) | **Confirmed** — ≤ 1 s in 8 files, ≤ 60 s in 140, ≤ 10 min in 18, > 10 min in 2 | P3 | Fix: `StartTime = payload.timestamp`, envelope fallback |
| AT | Claude corpus count drift (self) | 467 files now; tally 317/74/41/34/1; `file-history-snapshot` still 8.8 % | P3 | Fix numbers |

**New decision introduced in this round (maintainer may veto):** `apply_patch` joins `diffResultTools`, giving it the Claude `Edit`/`Write` treatment (body FTS-excluded, summary indexed, `show` verbatim, TUI diff marker). Rationale in design § Consumer contracts. It is name-keyed consumer policy and cannot affect Claude output; dropping it costs one line.
