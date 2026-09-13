# Tasks: Generic tool-input rendering in vault tool entries

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> Status: pending
> Created: 2026-09-13
> Issue: [#89](https://github.com/serpro69/capy/issues/89)
> Not Doing: per-tool field registry, recursive/nested-object JSON expansion, fixing the JSON-quoted-key gap in sanitize.go, new TUI expand-input markers, changing result-exclusion policy

## Task 1: Generic input summary (bounded, sanitized) in show + TUI
- **Status:** done
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [implementation.md#task-1--genericinputsummary--wire-into-toolusesummary](./implementation.md)

### Subtasks
- [x] 1.1 Add the four caps near `agentPromptMaxChars` in `internal/vault/scanner.go`: `genericMaxFields=6`, `genericKeyMaxChars=40`, `genericTokenMaxChars=80`, `genericSummaryMaxChars=200`, each with a one-line rationale (design.md § Constants)
- [x] 1.2 Add a fixed, tool-agnostic salient-key priority slice (`query, queries, command, content, source, url, path, pattern, prompt, code, name`), matched case-insensitively — NOT a per-tool map
- [x] 1.3 Implement `genericInputSummary(input json.RawMessage) string` in `scanner.go`: unmarshal to `map[string]json.RawMessage` (empty/error/non-object → `""`); select priority keys (in list order) then remaining alphabetically up to `genericMaxFields`; record omitted count; render each field to a `key=value` token (key capped; string unquoted; array → `json.Compact` with non-scalar elements replaced by `{…}`/`[…]`; nested object → `{…}` no descent; number/bool/null → literal via `json.Compact`); **`sanitize.StripSecrets` the full token, THEN `truncateRunes` it**; normalize control chars to spaces; join with `+N` marker when omitted; apply final `genericSummaryMaxChars` cap
- [x] 1.4 Replace the trailing `return name` in `toolUseSummary` (no `default:` clause exists) with the `genericInputSummary` fall-through; rewrite the stale deferral comment to reference `design.md`
- [x] 1.5 Table-driven tests for `genericInputSummary` in `scanner_test.go`: priority ordering (a 6-field `capy_search`-shaped input emits `queries`/`source` before the alphabetical fill); `+N` marker for an input with >`genericMaxFields` (e.g. 8) fields; array-of-strings → compact JSON `[…]`; nested object → `{…}` (contents absent); long key truncated; embedded-newline value → single line; empty/non-object/malformed → bare name; `Bash` regression guard
- [x] 1.6 End-to-end secret tests through `ScanSession`: over-cap `<private>…</private>` value → absent from assistant row, tool_result prefix, and chunks; top-level `api_key` prefix secret → redacted in FTS; nested `{"api_key":…}` → `{…}` indexed, credential absent
- [x] 1.7 Display assertions on **both** wrappers (no code change): `render_test.go` (`vault show`) — MCP `tool_use` renders `→ <name> key=value…` and a credential-shaped value is **redacted**; `transcript_test.go` (TUI, via `assistantBodyAndLaunches`) — same MCP block renders. Assert each surface explicitly, not "and/or"

## Task 2: FTS reindex + generalized backlog messaging
- **Status:** done
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [implementation.md#task-2--bump-currentindexversion-to-4--verify-reindex](./implementation.md)

### Subtasks
- [x] 2.1 Bump `currentIndexVersion` `3 → 4` in `internal/vault/store.go`; extend the constant's doc comment (v4 = generic/MCP tool-input summaries, issue #89)
- [x] 2.2 Update the literal assertion in `internal/vault/migrations_test.go` from `3` to `4` and its message
- [x] 2.3 Confirm no other test hard-codes the index version as a literal: `grep -rn "currentIndexVersion" internal/vault/*_test.go` (only `migrations_test.go` used the literal `3`; now `4` — all others use the constant)
- [x] 2.4 Extend `internal/vault/reindex_test.go`: write an assistant `tool_use` MCP session at `currentIndexVersion-1`, run reindex, assert the rebuilt FTS row contains the generic input summary and `IndexVersion == currentIndexVersion` (`TestReindex_RebuildsGenericToolInputSummary`)
- [x] 2.5 Generalize the backlog wording to version-neutral text keeping the `capy vault reindex` action at `internal/server/tool_search.go`, `internal/server/tool_vault_search.go:120`, and `internal/platform/doctor.go` (+ comment). **Scope note:** per user decision, also generalized two non-backlog `chunk-searchable` sites the design did not name — `internal/server/tool_vault_search.go:49` (enablement message) and `internal/platform/routing.go:76` (docs table) — so 2.6's grep is truly clean
- [x] 2.6 Update message assertions in `internal/platform/doctor_checks_test.go` (server tests assert only the stable `capy vault reindex` substring, so no change needed); `grep -rn "chunk-searchable" internal/` returns no production-code hits (one legitimate test-only assertion about swept-content chunk search remains)

## Task 3: Feature-specific quality gate + final verification
- **Status:** pending
- **Depends on:** Task 1, Task 2
- **Size:** M
- **Can run in parallel with:** —

### Subtasks
- [ ] 3.1 Add a feature-specific retrieval-quality check for the generic-input change: either extend the vault bench harness (`internal/vault/bench_test.go`) with `tool_use`-bearing fixtures + competing sessions, or add a focused ranking/false-positive test measuring whether low-signal generic keys degrade retrieval of a salient needle. **Do NOT rely on `make bench-quality` alone — its fixtures are text-only and never call `toolUseSummary`** (implementation.md § Task 3)
- [ ] 3.2 Run `/kk:test` — full suite with `-tags fts5` and `-race` (`CAPY_DB_KEY` + `CAPY_VAULT_KEY` set)
- [ ] 3.3 Run `/kk:document` — mark the parent `docs/feat/done/vault-tool-entries/design.md` § Deferred as done + link here; note the v4 bump in ADR-025 version semantics
- [ ] 3.4 Run `/kk:review-code` with `go` input to review the implementation
- [ ] 3.5 Run `/kk:review-spec` to verify implementation matches design and implementation docs

## Dependency Graph

```
Task 1 ─→ Task 2 ─→ Task 3
```
