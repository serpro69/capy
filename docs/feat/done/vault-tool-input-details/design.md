# Design: Generic tool-input rendering in vault tool entries

> Status: wip
> Created: 2026-09-13
> Issue: [#89](https://github.com/serpro69/capy/issues/89) — "vault: expand tool input details"
> Parent feature: [vault-tool-entries](../../done/vault-tool-entries/design.md) — this
> implements the **Deferred — generic input rendering for arbitrary/MCP tools**
> section of that design.
> ADR: [ADR-025](../../../adr/025-vault-index-version-and-reindex.md) — the
> `index_version`/reindex mechanism this feature triggers, and the record of the
> original tool-input deferral.

## Problem

Vault tool entries record **which tool was called** and **its inputs** — but only
for a hard-coded set of common tools. `toolUseSummary` (`internal/vault/scanner.go`)
renders the file path for `Read`/`Edit`/`Write`, the command for `Bash`, and a
bounded prompt for `Agent`/`Task`. **Every other tool** — MCP tools
(`mcp__capy__capy_index`, `mcp__capy__capy_search`), `ToolSearch`, `WebFetch`,
custom tools — renders as the **bare tool name** with no inputs.

From a session view, this means a call reads:

```
→ ToolSearch
→ mcp__capy__capy_index
```

with no indication of *what* was searched or *what* was indexed. Issue #89 asks
for the inputs of these calls to be surfaced, exactly as the parent feature's
design already anticipated and deferred.

`toolUseSummary` is the **single shared correlation point** used by all three JSONL
parsers (each through a *different* wrapper — the wrapper names differ, the summary
call does not):

| Parser | Surface | Wrapper calling `toolUseSummary` |
|--------|---------|----------------------------------|
| `scanner.go` | FTS5 search index (assistant `tool_use` rows) | `extractAssistantText` |
| `render.go` | `capy vault show` (text/markdown) | `renderAssistantBlocks` |
| `transcript.go` | TUI viewer | `assistantBodyAndLaunches` (Agent/Task blocks become launch markers via `subagentLaunchLabel`; **all other** tool_use blocks route through `toolUseSummary`) |

It is also reused by `collectToolUseSummaries` to build the `tool_use_id → toolCall`
map that prefixes each `tool_result` with its originating call. So a single change
to the non-common-tool branch of `toolUseSummary` reaches every display and search
surface at once. (Verified: `scanner.go` `extractAssistantText`, `render.go`
`renderAssistantBlocks`, `transcript.go` `assistantBodyAndLaunches`, and
`collectToolUseSummaries` all call `toolUseSummary`.)

## Goal

Extend `toolUseSummary`'s fall-through path to emit a **bounded** summary of the tool
input object — enough to answer "what was this call doing?" without flooding the FTS
index or the rendered transcript with low-signal JSON. Bounding is load-bearing:
arbitrary tool inputs are free-form JSON, and indexing them verbatim would bloat FTS
rows and degrade BM25 ranking (the exact concern that caused the original deferral).

## Chosen approach — bounded `key=value` summary

`toolUseSummary` is a `switch name { case "Read", …: … }` with **no `default:`
clause**; unmatched tools fall through to the trailing `return name` after the
switch. Add a helper `genericInputSummary(input json.RawMessage) string` and replace
that trailing `return name` so the fall-through path attempts a bounded summary
first:

```
// after the switch (no default: case exists — the switch handles only common tools)
if s := genericInputSummary(input); s != "" {
    return name + " " + s
}
return name
```

### Constants (exact, not "~")

| Constant | Value | Role |
|----------|-------|------|
| `genericMaxFields` | `6` | max fields rendered before the omitted-field marker |
| `genericKeyMaxChars` | `40` | per-key cap (keys are attacker/tool-controlled and otherwise unbounded) |
| `genericTokenMaxChars` | `80` | per-token (`key=value`) cap, applied **after** sanitization |
| `genericSummaryMaxChars` | `200` | final hard cap on the whole generic portion (reuses the `agentPromptMaxChars` ceiling) |

### `genericInputSummary` behaviour

1. **Parse.** Unmarshal `input` into `map[string]json.RawMessage`. On error, empty
   object, or a non-object top-level value (rare), return `""` → the caller emits
   the **bare name**. Graceful degradation, never an error (matches the parent
   feature's "unknown id → omit prefix" ethos).
2. **Select and order fields (salience-aware, deterministic).** Plain alphabetical
   order is *salience-blind* in two ways, both of which bite for real inputs
   (verified against the live `capy_search` schema — 6 input fields sorting
   `all_projects, include_kinds, limit, project, queries, source`):
   - **Ordering.** Alphabetical front-loads low-signal fields: `all_projects,
     include_kinds, limit, project` all sort *before* `queries, source`. Under the
     total-length cap (`genericSummaryMaxChars`, step 7) the meaningful fields would
     be the ones truncated away — even when the field *count* fits.
   - **Dropping.** When a tool has more than `genericMaxFields` fields, the ones that
     sort last are dropped — which may be the salient ones.

   Both are fixed by selecting/ordering with a **priority tier first**:
   - **Priority tier** — a small, tool-agnostic salient-key set, matched
     case-insensitively:
     `query, queries, command, content, source, url, path, pattern, prompt, code, name`.
     Present priority keys are emitted **in the fixed order listed** (deterministic,
     not alphabetical) so the reader — and the length cap — sees the meaningful fields
     first.
   - **Then remaining keys alphabetically**, to fill up to `genericMaxFields`.
   - This stays tool-agnostic (no per-tool registry — the rejected alternative C).
     A tool whose salient field isn't in the list still gets it via the alphabetical
     fill; it is only ever *ordered later* or (past `genericMaxFields`) counted in the
     omitted marker — never dropped silently.
3. **Omitted-field marker.** When the input has more than `genericMaxFields` keys,
   append a trailing `+N` token (N = keys not rendered) so the output never looks
   complete when it isn't. E.g. a tool call with 9 fields renders 6 tokens then `+3`.
   (Field-count omission is distinct from the `…` that `truncateRunes` appends when a
   token or the whole summary hits a length cap — both are visible incompleteness
   signals.)
4. **Render each selected field as one `key=value` token.** The value rendering is
   type-directed:
   - **String** → the raw string, **unquoted** (`source=kk:review-findings`). Note:
     unquoted string values may themselves contain spaces (`content=Indexed 1
     sections`), so field boundaries are **best-effort**, not delimiter-escaped. This
     is harmless for BM25 term indexing and stays human-readable; step 6 strips
     control chars so a value cannot inject a newline.
   - **Number / boolean / null** → literal via `json.Compact`.
   - **Array** → `json.Compact` of the array, but with every **non-scalar element**
     (object or nested array) replaced by a `{…}` / `[…]` placeholder first, so nested
     contents never surface. An all-scalar array therefore renders as ordinary
     compact JSON: `queries=["tool input details"]`, `max_results=[1,2]`.
   - **Nested object** (a field whose value is itself an object) → the placeholder
     `{…}` — do **not** descend. Bounds output and avoids surfacing nested credential
     shapes (see Secret handling).

   Key: **sanitized before its own `genericKeyMaxChars` cap** — keys are
   attacker/tool-controlled and otherwise unbounded, so `truncateRunes(sanitize.StripSecrets(key), genericKeyMaxChars)`
   applies the same sanitize-then-truncate rule as the value (step 5): a `<private>…`
   span in a >40-char key would otherwise lose its closing tag to the cap before
   `StripSecrets` ran, leaking the opening fragment on the display path. `json.Compact`
   on every non-string value is **required** (not "if in doubt") so whitespace in the
   source JSONL can't widen a token.
5. **Sanitize, then truncate — in that order (load-bearing).** Build the full
   `key=value` token from the sanitized-and-capped key (step 4) and the **untruncated**
   value, run `sanitize.StripSecrets` on the whole token, **then**
   `truncateRunes(token, genericTokenMaxChars)`. Sanitizing the complete token before
   truncation is what makes secret redaction reliable (see Secret handling) and lets
   the generic `key=…value` regex fire on the rendered form. (The per-key StripSecrets
   in step 4 and this whole-token StripSecrets are both load-bearing: the first bounds
   an over-cap key, the second catches a secret straddling the `key=value` boundary;
   the joined-summary pass in Secret handling then catches a secret straddling two
   fields.)
6. **Normalize control chars.** Replace any newline/tab/control character in each
   token with a single space so the summary is one stable line (unquoted values can
   contain newlines).
7. **Join** selected tokens (plus the optional `+N` marker) with single spaces, then
   apply the final `genericSummaryMaxChars` cap to the whole generic portion:
   `name key1=v1 key2=v2 +N`.

The output is bounded by **all four** caps together — key length, per-token length,
field count, and a final total cap — so no single long key, value, or field count
can blow past `genericSummaryMaxChars`.

Example outputs (priority keys first, then alphabetical fill):

```
→ mcp__capy__capy_search queries=["tool input details"] source=kk:arch-decisions
→ ToolSearch query=select:capy_search max_results=1
→ mcp__capy__capy_index content=Indexed 1 sections… source=kk:review-findings
```

(The first two calls set ≤ `genericMaxFields` fields, so no `+N` marker; a call
setting more than six fields would end with e.g. `… +2`. `capy_search` has six input
fields total — at `genericMaxFields=6` all six can render, and priority ordering
ensures `queries`/`source` lead so they survive the `genericSummaryMaxChars` cap.)

### Why bounded `key=value` (vs. the alternatives)

- **Deterministic & general.** Works for *any* tool — including tools that do not
  exist yet — with zero per-tool maintenance. This is precisely the "long tail of
  arbitrary/MCP tools" the issue is about.
- **Consistent with the parent design's concrete next step**, which named
  "truncated `key=value` pairs" as the intended format.
- **Bounds the noise/size concern** that caused the deferral: the four caps above
  keep FTS rows small and BM25 ranking intact.

## Secret handling

**Decision: `genericInputSummary` sanitizes each token in-function, before
truncation.** This diverges deliberately from the known-tool cases (Bash/Read/… are
rendered verbatim) and is the resolution of two review findings.

### Why not rely on the emit-boundary sanitizer alone

The FTS path sanitizes at the emit boundary — `scanner.go` Pass 2 wraps the
assembled assistant text in `sanitize.StripSecrets(...)` before writing the FTS row
(`emit(roleAssistant, extractAssistantText(...))`, `scanner.go:263`). But
`genericInputSummary` **truncates each value**, and emit-time sanitization runs on
the already-truncated text. Truncating before sanitizing defeats redaction — the
codebase's own title code documents exactly this hazard: *"Sanitize the fallback
BEFORE truncation: truncating first could split a secret so StripSecrets'
length-floored regex no longer matches the fragment"* (`scanner.go:322-323`).
Concretely, with truncate-then-sanitize:

- a value containing `<private>…</private>` whose closing tag falls past the token
  cap loses `</private>`, so `privateTagRe` (needs both tags) never matches; and
- a prefix secret (`sk-ant-…`, `ghp_…`, a JWT) split by the cap leaves a
  sub-20-char fragment that the length-floored patterns (`sanitize.go`) no longer
  match.

So we adopt the codebase's proven **sanitize-then-truncate** order *inside*
`genericInputSummary` (step 5 above). Because the value is sanitized while whole,
no secret is split. The emit-boundary sanitizer remains as a backstop
(double-sanitizing is harmless — placeholders don't re-match).

**Cross-field spans (joined-summary sanitize).** Per-token sanitization redacts a
secret contained *within* one field, but cannot catch one that **straddles two
fields** — e.g. a `<private>` opening tag in field A's token and its `</private>`
closing tag in field B's token, since `privateTagRe` requires both tags in one
string. The FTS path is backstopped by the emit-boundary `StripSecrets`, but the
display path is not. So `genericInputSummary` runs one final `StripSecrets` over
the **joined** summary before the `genericSummaryMaxChars` cap (sanitize-then-
truncate again): the cross-field span is redacted while whole, keeping the display
path safe. (Verified by the `cross-field private span` unit test.)

### Consequence: the display path is now redacted for generic inputs

Sanitizing in-function means generic tool inputs are redacted on **both** the FTS
path and the display path (`vault show`, TUI, Markdown export). This is the chosen
resolution of the display-exposure finding: before this feature, non-common tools
rendered as the **bare name** (no inputs) on the unsanitized display path; rendering
their inputs verbatim would be a **new** category of unsanitized content on display
(the Bash analogy is only partial — Bash was already shown; MCP inputs were not).
Rather than expand the verbatim surface, the new surface is **safe-by-default**:
redacted. Known-tool rendering (Bash/Read/…) is unchanged — still verbatim — so this
is not a blanket display-sanitization change.

### Residual gap (documented, not silently accepted)

`sanitize.StripSecrets`' generic `key=value` regex does not match JSON-quoted keys —
in `{"api_key":"…"}` the `"` between `api_key` and `:` blocks it (`sanitize.go:47`).
The design mitigates this by **not descending into nested objects** (step 4: a nested
object renders as `{…}`, never its contents), so a nested-object credential is never
surfaced in the first place. A **top-level** field literally named `api_key`, however,
renders as the token `api_key=<value>` — which *is* the `key=value` form the regex
matches (redacted). Closing the JSON-shaped-secret gap in `sanitize.go` itself is
**out of scope** (it is a pre-existing, general sanitizer limitation, and changing the
shared redaction regex is risky); this is flagged in Not Doing with a concrete next
step.

## Index version & reindex

Changing the fall-through path changes the FTS content of:

- assistant `tool_use` rows (via `extractAssistantText`), and
- `tool_result` prefixes (via `collectToolUseSummaries`).

Per ADR-025, when scanner extraction changes across a **released** boundary, existing
vaults' persisted FTS rows go stale and must be re-indexed. `currentIndexVersion` is
currently `3` (shipped: chunk FTS). This feature bumps it to **4**.

No new DB code is required: `import` (opportunistic, on-disk) and `capy vault reindex`
(explicit, DB-driven) already re-index every session with
`index_version < currentIndexVersion` via the existing `OutdatedSessionUUIDs` →
`RebuildFTSBatch` path. No schema change (the version is a code constant), satisfying
the Go profile's "never modify schemas" principle.

The **display** path (show + TUI) re-parses `raw_jsonl` on every read, so existing
vaults render the expanded inputs **immediately, with no reindex**. Only *search*
over the new inputs requires `capy vault reindex`.

### Backlog messaging must be generalized (was chunk-specific)

`OutdatedSessions` is a **general** `index_version < currentIndexVersion` count, but
three user-facing sites currently phrase the backlog as *"not yet chunk-searchable"* —
wording that was correct for the v3 chunk backfill:

- `internal/server/tool_search.go:261` (zero-hit vault advice),
- `internal/server/tool_vault_search.go:120` (zero-hit vault-search advice),
- `internal/platform/doctor.go:359` (`CheckVault` warn detail).

After the 3→4 bump, an existing v3 session **is** chunk-searchable (its chunk tables
were built at v3); it merely lacks the new generic tool-input text. Leaving the
"chunk-searchable" wording would make doctor and zero-hit guidance **factually wrong**
after a normal upgrade. These messages must be generalized to version-neutral wording,
e.g. *"N archived session(s) were indexed by an older version (v%d) and may omit newer
indexed content — run `capy vault reindex` to update them"*, keeping the correct action
(`capy vault reindex`). The chunk-specific doc comments at those sites (and in
`store.go`'s `currentIndexVersion` history) are updated to match. This is in-scope for
this feature (Task 2), not deferred.

## Assumptions

1. **`toolUseSummary` is the sole shared summary point** for FTS, `vault show`, and
   the TUI viewer, and `collectToolUseSummaries` reuses it for result prefixes.
   *Verified:* `extractAssistantText` (scanner `scanner.go`), `renderAssistantBlocks`
   (render `render.go:246`), `assistantBodyAndLaunches` (transcript
   `transcript.go:264` — Agent/Task branch to `subagentLaunchLabel`, everything else
   to `toolUseSummary`), and `collectToolUseSummaries` all call it. Falsifiable by
   grepping for other summary construction sites.
2. **In-function sanitization (sanitize-then-truncate) reliably redacts secrets in
   the generic summary.** *Basis:* the codebase's title path uses the same order for
   the same reason (`scanner.go:322-323`); the emit-boundary `sanitize.StripSecrets`
   (`scanner.go:263`) remains a backstop. Falsifiable by an end-to-end `ScanSession`
   test on an over-cap `<private>…</private>` value and a prefix secret (Task 1).
   *Known residual:* JSON-quoted nested-object secrets are not matched by
   `sanitize.go`; mitigated by not descending into nested objects (see Secret
   handling / Not Doing).
3. **`currentIndexVersion = 3` is a released version**, so the bump must go to `4`
   (crossing a released boundary), not an in-place redefinition. *Verified:* v3 is
   the shipped chunk-FTS version (`store.go`, `migrations_test.go`).
4. **Tool inputs are JSON objects at the top level.** Non-object inputs are rare and
   degrade to the bare name. Falsifiable by a tool whose input is a bare
   string/array — such a call simply shows the name, which is the current behaviour.
5. **`import`/`reindex` already re-index outdated sessions** with no new code needed.
   *Verified:* `OutdatedSessionUUIDs(currentIndexVersion)` + `RebuildFTSBatch` drive
   both paths.

## Not Doing

- **Per-tool field registry** — no hand-maintained `tool name → salient fields` map
  (rejected alternative C). The salience *priority tier* is a fixed, tool-agnostic
  list, not a per-tool map. It fails the unknown-tool long tail and needs maintenance
  per new MCP tool.
- **Recursive/deep JSON expansion** — nested objects render as `{…}` (contents not
  surfaced), arrays render only their scalar elements. Deep structures are low-signal
  and a redaction hazard.
- **Closing the JSON-shaped-secret gap in `sanitize.go`** — the generic `key=value`
  regex not matching JSON-quoted keys is a pre-existing, general sanitizer limitation.
  This feature mitigates it (no nested-object descent) but does not fix the shared
  regex. *Concrete next step (if pursued):* add a capture-group variant to
  `sanitize.go`'s generic key=value pattern that tolerates a `"` between key and `:`,
  with its own tests; out of scope here because it touches the shared redaction path.
- **New TUI interactions** (expand-to-full-input openable markers) — inputs are
  bounded inline; there is no expandable full-input view.
- **Changing `excludedResultTools` / `diffResultTools`** — result-body exclusion
  policy is orthogonal and unchanged; only the assistant-side call summary changes.

## Rejected Alternatives

- **A — Single salient field (priority-ordered).** Render only the first present key
  from a priority list. *Rejected:* under-shows multi-field calls; e.g. the
  `capy_index` case in the issue carries both `source` and `content`, and a single
  field would drop one.
- **C — Per-tool field registry.** A hand-maintained map of tool → salient fields.
  *Rejected:* precise for known tools but degrades to bare name for unknown/new
  tools — the exact long tail #89 is about — and adds a maintenance burden.
- **Display-only (no FTS / no version bump).** Render inputs in `vault show`/TUI but
  leave FTS extraction unchanged. *Rejected:* search would not find the new tool
  inputs, breaking the parent feature's "a session stays findable by what it did"
  goal for the whole long tail of tools.
- **Alphabetical-first-N selection.** Sort keys, take the first N. *Rejected:*
  salience-blind — for a fully-populated `capy_search` input the first fields are
  `all_projects, include_kinds, limit, project`, dropping `queries`/`source` (the
  fields #89 exists to surface). Replaced by the salience-priority tier + alphabetical
  fill + `+N` marker.
- **Verbatim display + a separate FTS-only sanitized summary.** Keep `vault
  show`/TUI verbatim and build a second, sanitized summary only for FTS. *Rejected:*
  requires either a parameter on the shared `toolUseSummary` (rippling to all three
  wrappers) or a parallel function, and still leaves the display path as a new
  unsanitized surface. Sanitize-in-function is simpler and makes the new surface
  safe-by-default on both paths; the modest cost is that generic-input display is
  redacted while Bash display stays verbatim.
