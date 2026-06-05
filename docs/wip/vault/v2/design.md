# Vault v2 — Design

> Parent feature: [../design.md](../design.md) · [../implementation.md](../implementation.md) · [../tasks.md](../tasks.md)
> Implementation plan: [./implementation.md](./implementation.md)
> Tasks: [./tasks.md](./tasks.md)
> Status: draft
> Created: 2026-06-05
> Profile: `go` (SQLite/FTS5 via sqlite3mc; `database.md` checklist applied)

## What v1 shipped, what v2 adds

v1 delivered verbatim, encrypted, forever-archival of Claude Code sessions: a separate `vault.db` (`CAPY_VAULT_KEY`), FTS5 search, the full CLI surface (`import`/`list`/`search`/`show`/`stats`/`checkpoint`/`restore`/`resume`/`delete`), a current-project MCP-server-startup sweep, and a TUI (`--tui`). Single-version-per-UUID semantics; larger-total-content wins on conflict.

v2 closes the gaps v1 explicitly deferred. It is **one milestone, flat task list** (no phase boundary). Task V2.0 (investigate the PreCompact payload) is the only hard gate, and it gates only the PreCompact-archival tasks (V2.13–V2.15); everything else proceeds independently.

## How Might We

> How might we make the vault a *complete, durable, portable* forever-archive — closing the compaction data-loss gap, shrinking storage ~5×, and enabling true cross-machine merge and key rotation — **without** bloating the lean binary or widening the local secret-exposure surface?

## Target user

Solo developer running capy across multiple machines and projects. Same persona as v1: cares about never losing session context, keeps an encrypted archive they can search/restore/resume, and copies `vault.db` between machines.

## Success criteria

1. **No content lost to `/compact`** — a PreCompact-triggered snapshot captures the pre-compaction transcript, provably restorable (`restore --snapshot`). *Contingent on V2.0 confirming favorable hook timing — see [PreCompact Archival](#5-precompact-archival).*
2. **`vault.db` measurably ~5× smaller** on a real corpus after `capy vault compact`, with no change to search/restore correctness.
3. **`merge --from` unites two machines' vaults non-destructively** — idempotent, larger-total-wins, source location/`machine_id` carried.
4. **`rekey` round-trips a key change** — old key in, new `CAPY_VAULT_KEY` out, verified open, `.bak` preserved.
5. **All-projects sweep + TUI completion are opt-in / non-regressive** — default behavior unchanged.

## Scope

Four work buckets, all selected:

- **Storage & cross-machine:** zstd BLOB compression, `vault compact`, `vault merge --from`, `vault rekey`.
- **Durability:** PreCompact hook archival (`vault_snapshots` cold storage + snapshot restore), gated on V2.0.
- **Reach & TUI polish:** opt-in all-projects server sweep; TUI completion (`r`/`R`/`c`/`f` keybindings + in-list filter); glamour markdown rendering behind a build tag.
- **Correctness & tech debt:** exclude 0-message sessions from import; consolidate `beginImmediate`/`isBusy` into `sqliteutil`; `context.Context` propagation through **both** `internal/store` and `internal/vault`; route `session.SessionDir()` through the shared `config.ClaudeProjectsDir()`.

## Architecture deltas (vs. v1)

```
internal/vault/
  codec.go        NEW  zstd encode/decode at the BLOB seam (self-describing)
  merge.go        NEW  MergeFrom: source-vault iteration + idempotent upsert
  snapshots.go    NEW  vault_snapshots writes/reads (cold storage)
  migrations.go   EDIT first real migration (vault_snapshots) + migration runner
  store.go        EDIT decode on read / encode on write; ctx params; snapshot stmts
  import.go       EDIT 0-msg exclusion; ctx already present
  discovery.go    EDIT all-projects discovery helper
  tui/            EDIT keybindings, filter, glamour (build-tagged) render path
internal/sqliteutil/
  sqliteutil.go   EDIT host beginImmediate/isBusy; new Rekey() (backup-API)
internal/store/
  *.go            EDIT ctx propagation; consume shared beginImmediate/isBusy/Rekey
internal/hook/
  precompact.go   EDIT stub → archival handler (gated on V2.0)
internal/session/
  sweep.go        EDIT SessionDir() routes through config.ClaudeProjectsDir()
cmd/capy/
  vault.go        EDIT new subcommands: compact, merge, rekey, snapshots
  encrypt.go      EDIT rekey machinery moves to shared helper; rewire
```

No change to the v1 invariants: mandatory encryption, FTS5 build tag, WAL-checkpoint-on-close, single-version-per-UUID (snapshots are the **documented exception**), 1:1 location columns.

---

## 1. BLOB Compression (zstd)

**Goal:** shrink `raw_jsonl` and `vault_files.raw_content`. JSONL is highly compressible (5–8×). Target: ~110 MB → ~15–25 MB on a 214-session corpus.

**Library:** `github.com/klauspost/compress/zstd` (verified via context7). `Encoder.EncodeAll(src, nil)` / `Decoder.DecodeAll(src, nil)` are thread-safe and reentrant — a single package-level `*Encoder` and `*Decoder` are shared across the sweep goroutine and CLI with no locking. No streaming, no per-call allocation churn (a shared encoder caches compressors).

**Self-describing storage (chosen) vs. an `encoding` column (rejected).** New blobs are stored as a zstd frame; existing blobs stay raw JSONL. On read, peek the first 4 bytes: zstd frame magic is `0x28 0xB5 0x2F 0xFD`; raw JSONL always starts with `{` (`0x7B`). The two can never collide, so `decodeBlob` auto-detects with **zero schema change** and transparently handles a vault that is half-migrated. An `encoding TEXT` column was rejected: it adds a migration, a write path, and a failure mode (column says "zstd" but bytes aren't) for no benefit over a self-describing frame.

**Invariant preserved:** `content_hash`, `size_bytes`, and FTS text are all computed on **uncompressed** bytes (see `import.go:computeContentHash`, `buildRecord`). Compression is applied *only* to the bytes handed to the `INSERT`/`UPDATE` BLOB parameters, and reversed *immediately after* `Scan` in `GetSession`/`GetFiles`. Idempotency, the larger-wins merge tiebreaker, and search are byte-for-byte unchanged.

**Seam:** `internal/vault/codec.go` — `encodeBlob([]byte) []byte`, `decodeBlob([]byte) ([]byte, error)`. Write callers: `store.go:writeRecord` (raw_jsonl), `writeChildren` (file content), and the snapshot writer. Read callers: `scanSessionMeta`/`GetSession`, `GetFiles`, snapshot reads, the source side of `MergeFrom`, and `render.go`/`restore.go`/TUI viewer (all go through `GetSession`/`GetFiles`, so they inherit decode for free).

**`capy vault compact`:** existing rows stay raw until rewritten, so the file does not shrink on upgrade. `compact` rewrites every uncompressed blob compressed (UPDATE in batched transactions) and then runs `VACUUM` to reclaim freed pages (SQLite never shrinks the file automatically). VACUUM requires no other connection holding the DB; run it on a dedicated single connection after the pool closes, mirroring `Checkpoint`. Report before/after file size.

## 2. `capy vault rekey`

**Goal:** rotate a compromised `CAPY_VAULT_KEY` without decrypt-and-reimport.

**Approach (backup-API, not `PRAGMA rekey`):** `cmd/capy/encrypt.go:rekeyEncrypted` already rotates an *already-encrypted* DB by opening the source with the old key, checkpointing, copying into a fresh temp DB opened with the new key via the SQLite **backup API**, then `swapAndVerify` (remove WAL/SHM, `.bak` the original, move temp into place, verify open). This sidesteps the WAL/`PRAGMA rekey` incompatibility (ADR-020) entirely — no journal-mode dance needed — because it writes a brand-new file rather than rekeying in place.

**Refactor (V2.7):** those helpers (`openEncrypted`, `checkpointDB`, `backupDB`, `swapAndVerify`, `rekeyEncrypted`) live in `package main`. Extract the reusable rotation into `internal/sqliteutil` as `Rekey(dbPath, oldKey, newKey string) error` (parameterized; no env coupling). Rewire `capy encrypt`'s key-rotation path to call it. **This touches encryption-critical code shared with the knowledge store — land it as its own behavior-preserving, green commit** (same caution as v1 Task 1.1). `capy encrypt`'s *initial* unencrypted→encrypted path (`encryptPlain`, which does use `PRAGMA rekey` after `journal_mode = DELETE`) is **not** moved — the vault is always encrypted, so it never needs it.

**`capy vault rekey` (V2.8):** prompt for the current passphrase, read the new one from `CAPY_VAULT_KEY` (the new key must already be exported, consistent with `capy encrypt`), call `sqliteutil.Rekey(VaultDBPath(), old, new)`. Refuse if the MCP server might hold the DB open (checkpoint reports busy pages → abort with guidance).

## 3. `capy vault merge --from <path>`

**Goal:** v1 cross-machine is destructive replace. `merge` imports sessions from a source vault into the destination without overwriting the destination wholesale.

**Design:** open the source vault read-only with its key (`--key` flag or `CAPY_VAULT_MERGE_KEY`; default to `CAPY_VAULT_KEY` when both machines share a key) via `sqliteutil.OpenWithCanary`. Iterate source `vault_sessions` + `vault_files`, decoding blobs (source may be compressed). For each session, apply the **same idempotent decision the disk import uses** (`dest.SessionDigest(uuid)` → skip if same `content_hash` or smaller total `size_bytes`; else insert/replace). Re-scan the decoded `raw_jsonl` + subagent blobs through the existing scanner to rebuild FTS in the destination's current schema (don't copy source FTS rows — re-scanning guarantees schema consistency across versions).

**Metadata carry:** preserve the source row's `machine_id`, `claude_project_dir`, `project_path`, `git_branch` (do **not** recompute `project_path` from cwd — the source already resolved it, possibly for a path that doesn't exist on this machine).

**Refactor seam:** v1's `Import` is coupled to disk (`os.ReadFile`). Rather than rework it, `MergeFrom` builds `SessionRecord`s from in-memory source blobs and reuses `VaultStore.SessionDigest` + `InsertSession`/`ReplaceSession`/`WriteBatch`. Extract the FTS-rebuild-from-bytes portion of `buildRecord` into a helper both paths share (`buildFTS(uuid, mainBytes, files)`), so the scanner wiring isn't duplicated.

## 4. All-Projects Server-Startup Sweep (opt-in)

**Goal:** v1 sweeps only the current project. Opt-in flag walks all projects.

**Design:** gate on `CAPY_VAULT_SWEEP_ALL` (env) — **off by default**. When set, `vaultSweep` discovers from `config.ClaudeProjectsDir()` (the projects root, which `DiscoverSessions` already auto-detects) instead of `ProjectSessionDir(s.projectDir)`. Everything downstream is unchanged: same background goroutine, same `bgWg`, same cooperative `ctx` cancellation, same batched import. design.md flags the trade-off — startup-walk latency + `vault.db` write contention across hundreds of projects — which is exactly why it's opt-in and not default. Log a one-line summary of how many projects were swept.

## 5. PreCompact Archival

**Hard gate — V2.0 first.** `handlePreCompact` is a stub. Assumption #2 (the hook fires **before** Claude Code mutates the session file) is **unverified**. V2.0 adds a debug handler (gated behind `CAPY_DEBUG_PRECOMPACT=1`, 0600 temp file) that dumps the raw payload, triggers `/compact`, and documents the JSON shape + the timing (file mtime before/after). **If V2.0 finds the hook fires *after* mutation, file-based capture is impossible and V2.13–V2.15 are re-scoped** (e.g. capture from a SessionStart-cached copy, or dropped). This is stated loud so the contingency is visible, not buried.

**`vault_snapshots` (first real migration, V2.13):** append-only cold storage, separate from the active `vault_sessions` row, so multiple pre-compaction versions of one session don't pollute FTS (per the indexed arch-decision "Hybrid active + cold storage"). Columns: `snapshot_id` (PK autoincrement), `session_uuid` (FK → `vault_sessions`, ON DELETE CASCADE), `content_hash`, `size_bytes`, `captured_at`, `trigger` (`'precompact'`), `raw_jsonl` (compressed BLOB). `UNIQUE(session_uuid, content_hash)` dedups identical re-captures. Index `(session_uuid, captured_at DESC)`. **Not** in FTS. Added via the migration framework — which today only has `ensureVaultMigrationsTable`; V2.13 adds the `vaultMigrationApplied(name)` guard + the apply-loop, following `internal/store/migrate.go`.

**Hook handler (V2.14):** parse the payload per V2.0's findings → locate the session file + session UUID + project dir. Read the pre-compaction file, scan, and (a) `INSERT` into `vault_snapshots` (dedup via UNIQUE), (b) upsert the active `vault_sessions` row via the normal idempotent path. Hooks are short-lived processes (CLAUDE.md invariant): open vault → single write → checkpoint/close, fast and synchronous, so the snapshot is durable before compaction proceeds. Failures are logged, never block the user's `/compact`.

**Snapshot CLI (V2.15):** `capy vault snapshots <id>` lists snapshots for a session (hash, size, captured_at). `capy vault restore <id> --snapshot <hash>` restores from a specific snapshot instead of the active row.

**Retention (open decision — see below):** v2 MVP defaults to **keep-all-distinct** (UNIQUE dedups identical captures; the "archive forever" ethos argues against silent eviction). A configurable cap (keep N most recent per session) is documented as a future knob.

**Delete semantics:** `capy vault delete` cascades to snapshots (full erasure — delete is the erasure tool). Stated explicitly so it isn't a surprise.

## 6. TUI Completion

**Keybindings (V2.11):** wire the design's deferred bindings — `f` (project filter), `c` (copy current message to clipboard), `r` (restore), `R` (resume) — plus in-list fuzzy filtering (v1 disabled the list's built-in `/` so `/` opens global FTS; `f` gets the in-list filter). **`r`/`R` are the destructive/exec surface**: they must `tea.Quit`/teardown the bubbletea program (release the alt-screen + raw TTY) **before** exec'ing `claude --resume` or writing restored files, then optionally relaunch — same care as CLI Task 4b. Copy uses a small clipboard dependency or OSC-52; prefer OSC-52 to avoid a native dep (keeps the binary lean).

**Glamour markdown (V2.12) — behind a build tag.** ⚠️ Dependency caveat (verified via context7): the current glamour is **v2** (`charm.land/glamour/v2`) and pulls **lipgloss v2** (`charm.land/lipgloss/v2`). The vault TUI is pinned to the **v1 line** (bubbletea v1.3.10, lipgloss v1.1.0 — v1 Task 6.1). Pulling glamour v2 would introduce a second lipgloss major. So v2 **must pin glamour to the latest v0.x on the `github.com/charmbracelet/glamour` path** and verify (`go mod graph`) that it resolves `github.com/charmbracelet/lipgloss` (v1 path) and pulls **no** `charm.land/lipgloss/v2`. Rendering lives behind `-tags glamour`: `tui/render_glamour.go` (tag `glamour`) provides a `glamour.NewTermRenderer(...).Render(md)` path; `tui/render_default.go` (tag `!glamour`) keeps the v1 lipgloss-only styling. The default build stays lean; rich rendering is opt-in at build time.

## 7. Correctness & Tech Debt

- **Exclude 0-msg sessions (V2.4):** in `Import`, after `buildRecord`, skip sessions whose scanned `MessageCount == 0` (record a new `StatusExcluded` outcome, not silent). These are empty shells (observed: 7 untitled 2–4 KB rows). A re-import catches them once they gain content (idempotent), so nothing is permanently lost. Mirror the same guard in `MergeFrom`.
- **`beginImmediate`/`isBusy` → `sqliteutil` (V2.1):** both `internal/vault/migrations.go` and `internal/store/retry.go` carry copies (there's a literal `TODO` in migrations.go). Move both into `internal/sqliteutil`; both stores import it. Behavior-preserving.
- **`context.Context` propagation, both stores (V2.2a store, V2.2b vault):** per the user decision, convert `internal/store` and `internal/vault` to `*Context` variants (`QueryContext`/`ExecContext`/`BeginTx`) and add `ctx` params to public methods, in **two adjacent behavior-preserving commits** (store first, then vault). **Direct trade-off note:** today this is largely ceremony — `VaultStore.ctx()` returns `context.Background()` and the knowledge store has no cancelling callers. The one real beneficiary is the all-projects background sweep (V2.10), a genuinely long, cancellable operation; without ctx in the DB methods, an in-flight transaction there can't be interrupted (only the per-session loop check in `Import` can). The cost is touching working, encryption-critical code across both packages. We do it because the user chose consistency over divergence, and V2.10 finally supplies the cancelling caller that justifies it.
- **`SessionDir()` routing (V2.3):** `internal/session/sweep.go:SessionDir()` still mangles against a hardcoded `~/.claude/projects/`; route it through `config.ClaudeProjectsDir()` (already `CLAUDE_CONFIG_DIR`-aware after v1 Task 3.1) so vault and session-sweep agree. Re-run `internal/session` tests.

---

## Schema changes

One migration (`vault_snapshots`, V2.13) — the **first real vault migration**. No change to `vault_sessions`/`vault_files`/`vault_fts`/`vault_meta`. Compression is a value-encoding change inside existing BLOB columns, requiring **no** schema migration (self-describing frames). Per the `go` `database.md` checklist, schema DDL is human-authored and reviewed; the migration is guarded, idempotent, and applied inside a `beginImmediate` transaction.

## Dependencies

| Dependency | Version constraint | Why / caveat |
|---|---|---|
| `github.com/klauspost/compress/zstd` | latest stable | `EncodeAll`/`DecodeAll` thread-safe & reentrant; shared encoder/decoder; no streaming. Verified via context7. |
| `github.com/charmbracelet/glamour` | **v0.x (v1-compatible) only** | v2 (`charm.land/glamour/v2`) drags in lipgloss v2 — incompatible with the pinned bubbletea/lipgloss v1 stack. Pin to the `github.com/charmbracelet/...` path; verify `go mod graph` shows no `charm.land/lipgloss/v2`. Behind `-tags glamour`. |
| clipboard / OSC-52 | prefer OSC-52 (no dep) | `c` keybinding; avoid a native clipboard dep to keep the binary lean. |

## Assumptions

**Must be true (dealbreakers):**
1. **PreCompact fires before file mutation.** UNVERIFIED — V2.0 is the gate. If false, PreCompact archival (V2.13–15) is re-scoped or dropped; the rest of v2 is unaffected.
2. **zstd frame magic never collides with raw JSONL leading bytes.** True by construction: JSONL starts with `{` (0x7B); zstd frame magic is `0x28B52FFD`. Validated by a round-trip + mixed-corpus read test.

**Should be true (important):**
3. **The SQLite backup-API rekey works for sqlite3mc-encrypted vaults** as it does for the knowledge store (it already ships in `capy encrypt`). Validated by a rekey round-trip test.
4. **Re-scanning decoded source blobs in `merge` reproduces equivalent FTS** to the original import. Validated by a merge → search test.
5. **VACUUM after `compact` reclaims the freed pages** to deliver the projected size win. Validated by before/after `os.Stat` in the compact test.

**Might be true (nice to have):**
6. Snapshot growth is bounded enough that keep-all-distinct is acceptable without a retention cap for typical users.

## Not Doing

- **Cloud sync / multi-user / Codex sessions / session diffing / real-time watch / automatic retention** — unchanged from v1; still out.
- **Sharing/export with redaction** — still its own future design (secret scanning, deny patterns, LLM review). Not in v2.
- **TUI lazy-windowing viewer & 3-panel split** — deliberate v1 deviations deferred *pending evidence of a problem*; remain documented "do-if-profiling-shows-lag" items, **not** v2 tasks. (Not selected.)
- **`encryptPlain` (unencrypted→encrypted) extraction** — the vault is always encrypted; only the already-encrypted rekey path is shared.
- **Snapshot retention eviction policy** — keep-all-distinct in v2; a configurable cap is deferred (see Assumption #6).

## Open design decisions (for `/kk:review-design`)

1. **Snapshot retention:** keep-all-distinct (chosen) vs. keep-N-recent-per-session. Revisit if Assumption #6 fails.
2. **`merge` source open:** read-only reuse of `sqliteutil.OpenWithCanary` vs. a lighter dedicated read-only opener (the canary path also runs schema DDL/migrations against the *source* — undesirable for a read-only source vault). Likely needs a read-only variant that skips DDL.
3. **`compact` VACUUM under a running server:** abort-if-busy (chosen, mirrors rekey) vs. online incremental vacuum. Abort-if-busy is simpler and safe.

## References

- v1: [../design.md](../design.md), [../implementation.md](../implementation.md), [../tasks.md](../tasks.md) (follow-ups + "Not Doing")
- ADR-016 (WAL checkpoint on close), ADR-019/020 (encrypted DB, WAL/rekey incompatibility), ADR-017 (source-kind separation)
- Indexed `kk:arch-decisions`: "Hybrid active + cold storage", "Merge semantics: larger size_bytes wins", "Machine identity outside DB"
- `klauspost/compress` zstd `EncodeAll`/`DecodeAll`; `charmbracelet/glamour` `NewTermRenderer` (both via context7)
