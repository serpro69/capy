# Implementation: Generic tool-input rendering in vault tool entries

> Design: [design.md](design.md)
> Tasks: [tasks.md](tasks.md)
> Language: Go (build/test tag: `fts5`; `CAPY_DB_KEY` + `CAPY_VAULT_KEY` required)

This plan assumes a skilled Go engineer with no prior context on capy's vault. Read
[design.md](design.md) first — it explains why `toolUseSummary` is the single
*summary* point (one function feeds FTS + `vault show` + TUI), why the change forces
an `index_version` bump, and why that bump also drags in three user-facing backlog
messages (Task 2).

## Orientation — files you will touch

| File | Role | Change |
|------|------|--------|
| `internal/vault/scanner.go` | tool summary + all extraction helpers | add `genericInputSummary` (sanitizing, bounded); replace the trailing `return name` in `toolUseSummary` (there is **no** `default:` clause — see below) |
| `internal/vault/scanner_test.go` | scanner unit tests | table tests for `genericInputSummary` + `toolUseSummary` fall-through + an end-to-end `ScanSession` secret test |
| `internal/vault/store.go` | `currentIndexVersion` constant | bump `3 → 4`; extend the doc comment; generalize the chunk-specific v3 wording where it feeds user messages |
| `internal/vault/migrations_test.go` | asserts the current index version | update the `currentIndexVersion` assertion + message |
| `internal/server/tool_search.go` | zero-hit vault advice (`:261`) | generalize "not yet chunk-searchable" → version-neutral wording |
| `internal/server/tool_vault_search.go` | zero-hit vault-search advice (`:120`) | same generalization |
| `internal/platform/doctor.go` | `CheckVault` warn detail (`:359`) | same generalization + comment update (`:341-345`) |
| `internal/server/*_test.go`, `internal/platform/doctor_test.go` | message assertions | update expected backlog strings |

Files you will **read but not change** (to confirm behaviour):

- `internal/vault/render.go` — `renderAssistantBlocks` → `toolUseSummary`
  (`vault show`). Re-parses `raw_jsonl`; picks up the change with no reindex.
- `internal/vault/transcript.go` — **`assistantBodyAndLaunches`** (`:264`) →
  `toolUseSummary` (TUI viewer). Note: Agent/Task tool_use blocks are diverted to
  `subagentLaunchLabel` as launch markers; **all other** tool_use blocks (including
  MCP/generic) route through `toolUseSummary`, so the generic summary reaches the TUI.
  It is **not** `renderAssistantBlocks` (that is render.go's wrapper).
- `internal/vault/reindex.go` / `internal/vault/import.go` — the existing
  `OutdatedSessionUUIDs` → `RebuildFTSBatch` reindex path that the version bump
  activates. **No code change** — verify it triggers.
- `internal/sanitize/sanitize.go` — `StripSecrets`, `privateTagRe` (needs both
  `<private>` and `</private>`), and the generic `key=value` regex (`:47`, needs a
  20+char value and does **not** match JSON-quoted keys). These constraints drive the
  sanitize-then-truncate order and the no-nested-descent rule.

## Existing primitives to reuse (do NOT reinvent)

- `truncateRunes(s string, max int) string` (`scanner.go`) — rune-safe head
  truncation with a `…` suffix. Use for the per-token cap **after** sanitization.
- `asJSONString(raw json.RawMessage) (string, bool)` — unwrap a JSON string value.
- `jsonStringField(raw, key)` — existing single-key string extractor (used by the
  known-tool cases; a useful reference for the map-unmarshal pattern).
- `sanitize.StripSecrets(string) string` — apply to each full `key=value` token
  **before** truncating it (see Task 1 step 5).
- `agentPromptMaxChars = 200` (`scanner.go`) — the existing summary-length ceiling;
  reuse its value for `genericSummaryMaxChars`. Introduce the four generic caps
  (`genericMaxFields=6`, `genericKeyMaxChars=40`, `genericTokenMaxChars=80`,
  `genericSummaryMaxChars=200`) as siblings near it (design.md § Constants).

## Task 1 — `genericInputSummary` + wire into `toolUseSummary`

**Design ref:** design.md § "Chosen approach". This task delivers the *display*
improvement end-to-end: on completion, `vault show` and the TUI viewer render
generic tool inputs immediately (they re-parse `raw_jsonl`), with no reindex.

Steps:

1. Add the four constants near `agentPromptMaxChars` in `scanner.go`
   (`genericMaxFields=6`, `genericKeyMaxChars=40`, `genericTokenMaxChars=80`,
   `genericSummaryMaxChars=200`), each with a one-line rationale. See design.md
   § Constants.
2. Add a small package-level salient-key priority list (design.md § step 2):
   `query, queries, command, content, source, url, path, pattern, prompt, code,
   name` — a fixed, tool-agnostic slice (NOT a per-tool map). Matched
   case-insensitively.
3. Implement `genericInputSummary(input json.RawMessage) string` in `scanner.go`,
   next to `toolUseSummary`:
   - Unmarshal into `map[string]json.RawMessage`; on error, empty map, or non-object
     input return `""`.
   - **Select fields:** take present priority keys in list order, then remaining keys
     `sort.Strings`-ordered, up to `genericMaxFields`. Record `omitted =
     len(keys) - rendered` for the marker.
   - **Render each selected field to one token** `key=value` (design.md § step 4):
     - key truncated to `genericKeyMaxChars`;
     - value by type: **string** → raw, unquoted; **array** → `json.Compact` with any
       non-scalar element replaced by a `{…}`/`[…]` placeholder first (all-scalar
       arrays become plain compact JSON, e.g. `["a","b"]`); **nested object** → `{…}`
       (no descent); **number/bool/null** → literal via `json.Compact`.
     - **Sanitize the full token** with `sanitize.StripSecrets`, **then**
       `truncateRunes(token, genericTokenMaxChars)` (order is load-bearing —
       design.md § Secret handling).
     - Replace any newline/tab/control rune in the token with a single space.
   - Join tokens (append `+N` when `omitted > 0`) with single spaces; apply
     `truncateRunes(joined, genericSummaryMaxChars)` as the final hard cap; return.
   - → verify: the unit tests in step 5 pass.
4. Replace the trailing `return name` in `toolUseSummary` (the switch has **no**
   `default:` clause) with: `if s := genericInputSummary(input); s != "" { return
   name + " " + s }; return name`. Rewrite the stale deferral comment to describe the
   new behaviour and reference design.md.
   - → verify: the known-tool cases (`Read`/`Bash`/`Agent`) are untouched — their
     existing tests still pass.
5. Add table-driven tests for `genericInputSummary` in `scanner_test.go`:
   - priority ordering (a `capy_search`-shaped 6-field input emits `queries`/`source`
     **before** the alphabetical fill `all_projects`/`include_kinds`/…);
   - `+N` marker for an input with **more than** `genericMaxFields` (e.g. 8) fields;
   - array of strings → compact JSON `[…]`; nested object → `{…}` placeholder
     (contents absent);
   - long key → truncated to `genericKeyMaxChars`;
   - value with an embedded newline → single-line token;
   - empty/non-object/malformed → bare name;
   - a known tool (`Bash`) → unchanged (regression guard).
6. **Secret end-to-end tests** (`ScanSession`, the real FTS path) — these are the
   gate for the sanitize-then-truncate order:
   - an over-cap value `<private>…</private>` whose closing tag falls past
     `genericTokenMaxChars` → assert the secret reaches **neither** the assistant FTS
     row, **nor** a tool_result prefix, **nor** a chunk;
   - a top-level field `api_key` holding a 20+char prefix secret (`sk-ant-…`) →
     assert redacted in FTS;
   - a nested `{"api_key":"…"}` object value → assert the `{…}` placeholder is
     indexed and the credential is absent.
   - → verify: `go test -tags fts5 -run 'GenericInput|ToolUseSummary|ScanSession' ./internal/vault/`
7. Display assertions on **both** wrappers (no code change to render/transcript):
   - `render_test.go` (via `RenderText`/`RenderMarkdown`, `vault show`): an MCP
     `tool_use` renders `→ <name> key=value…` and a credential-shaped value is
     **redacted** (pins the sanitize-on-display decision);
   - `transcript_test.go` (via the TUI transcript parse): the same MCP block renders
     through `assistantBodyAndLaunches`. Assert each surface explicitly — not
     "and/or".
   - → verify: `go test -tags fts5 ./internal/vault/` passes.

**Sanitization note (reversed from the first draft):** `genericInputSummary`
**does** call `sanitize.StripSecrets` — per token, before truncation. This is
required because the emit-boundary sanitizer runs *after* truncation, and truncating
first splits secrets below the sanitizer's length floor / away from their closing
`</private>` tag (`scanner.go:322-323` documents this exact hazard). In-function
sanitization also makes the **display** path safe (it does not pass through the emit
sanitizer). The emit-boundary `StripSecrets` stays as a harmless backstop. Do NOT
sanitize the known-tool cases (Bash/Read/…) — their verbatim rendering is unchanged.

## Task 2 — bump `currentIndexVersion` to 4 + verify reindex

**Design ref:** design.md § "Index version & reindex". This task delivers the
*search* improvement: existing vaults index the new generic inputs after
`capy vault reindex`.

Depends on Task 1 (bumping the version before the extraction change would mark
sessions stale for a no-op).

Steps:

1. In `store.go`, change `currentIndexVersion` from `3` to `4`. Update the
   constant's doc comment to add: v4 = generic/MCP tool-input summaries in
   `toolUseSummary` (issue #89).
2. Update `migrations_test.go` — the assertion currently reads
   `assert.Equal(t, 3, currentIndexVersion, ...)`. Change the expected value to `4`
   and update the message to name the tool-input-summary bump.
   - → verify: `go test -tags fts5 -run 'Migration|IndexVersion' ./internal/vault/`
3. Confirm no other test hard-codes `3` for the index version. Search
   `internal/vault` for literal `3` used as an index version — the other
   references use the `currentIndexVersion` constant (import_test, reindex_test,
   store_test) and update automatically.
   - → verify: `grep -rn "currentIndexVersion" internal/vault/*_test.go` shows all
     dynamic (constant-based) except the one asserting the literal in step 2.
4. Verify the reindex path activates for a stale session (no new code, behavioural
   check): a test that writes a session at `index_version = currentIndexVersion-1`,
   runs the reindex, and asserts the rebuilt FTS row for an assistant `tool_use`
   now contains the generic input summary. Prefer extending an existing
   `reindex_test.go` case over adding a whole new harness.
   - → verify: `go test -tags fts5 ./internal/vault/` (reindex_test.go) passes.
5. **Generalize the backlog messages** (design.md § "Backlog messaging must be
   generalized"). `OutdatedSessions` is a general `index_version < current` count, so
   after the v4 bump the current "not yet chunk-searchable" wording is false for v3
   sessions (which *are* chunk-searchable). Update all three sites to version-neutral
   wording, e.g. *"N archived session(s) were indexed by an older version (v%d) and
   may omit newer indexed content — run `capy vault reindex` to update them"* (keep
   the `capy vault reindex` action):
   - `internal/server/tool_search.go:261`,
   - `internal/server/tool_vault_search.go:120`,
   - `internal/platform/doctor.go:359` (and its comment `:341-345`).
   - → verify: `go test -tags fts5 ./internal/server/... ./internal/platform/...`
6. Update the message assertions in `internal/server/*_test.go` and
   `internal/platform/doctor_test.go` to the new wording. `grep -rn
   "chunk-searchable" internal/` must return **no** production hits after this task.
   - → verify: `grep -rn "chunk-searchable" internal/` shows only historical
     references in design/ADR docs, not code.

## Task 3 — full verification

Depends on Tasks 1–2. See tasks.md for the checklist.

- `/kk:test` — full suite with `-tags fts5` (and `-race`).
- `/kk:document` — update the parent
  [vault-tool-entries/design.md](../../done/vault-tool-entries/design.md) § Deferred
  to mark this work **done** and link here; note the v4 bump in
  [ADR-025](../../../adr/025-vault-index-version-and-reindex.md) version semantics.
- `/kk:review-code go` and `/kk:review-spec`.

**`make bench-quality` is NOT a valid gate for this change — do not rely on it.**
The vault quality harness (`internal/vault/bench_test.go:207-209`) synthesizes every
assistant fixture turn as a `{"type":"text"}` block and never emits `tool_use`
blocks, so `toolUseSummary` / `genericInputSummary` are never exercised and the
score cannot move. The "confirm no retrieval-quality regression" claim in the first
draft was wrong. The BM25 noise/ranking concern that motivates bounding must instead
be covered by a **feature-specific** check (Task 3, below) — either extend the vault
bench harness with `tool_use`-bearing fixtures + competing sessions, or add a focused
ranking/false-positive test that measures whether low-signal generic keys degrade
retrieval of a salient needle.

## Testing approach

- All vault tests require `-tags fts5`, `CAPY_DB_KEY`, and `CAPY_VAULT_KEY`
  (AGENTS.md § Build & Test).
- Prefer table-driven unit tests for `genericInputSummary` (pure function, easy to
  exhaust the cases: selection, caps, nested-object placeholder, control-char
  normalization, degradation).
- The **secret** cases must be end-to-end through `ScanSession` (not just the pure
  function), because the hazard is the truncation/sanitization *ordering* across the
  scan pipeline (Task 1 step 6).
- For the reindex behavioural check, reuse the existing `reindex_test.go` fixtures
  and helpers (`mk(...)`, `UpdateSessionFTS`, `OutdatedSessionUUIDs`) rather than
  standing up a new vault.
- Benchmarks are gated by `CAPY_BENCH_RESULTS` and skipped in `go test ./...`. If you
  add `tool_use` fixtures to the vault bench harness, run `make bench-quality` and
  compare against `master`; otherwise the targeted ranking test above is the gate.
