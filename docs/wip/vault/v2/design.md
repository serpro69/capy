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
4. **`rekey` round-trips a key change** — old key in, new `CAPY_VAULT_KEY` out, verified open, and the old-key `.bak` is handled deliberately: a prominent warning that it remains decryptable by the (compromised) old key, plus a `--remove-backup` option to delete it after a verified rotation (see §2).
5. **All-projects sweep + TUI completion are opt-in / non-regressive** — default behavior unchanged.

## Scope

Four work buckets, all selected:

- **Storage & cross-machine:** zstd BLOB compression, `vault compact`, `vault merge --from`, `vault rekey`.
- **Durability:** PreCompact hook archival (`vault_snapshots` cold storage + snapshot restore), gated on V2.0.
- **Reach & TUI polish:** opt-in all-projects server sweep; TUI completion (`r`/`R`/`c`/`f` keybindings + in-list filter); glamour markdown rendering behind a build tag.
- **Correctness & tech debt:** exclude 0-message sessions from import; consolidate `beginImmediate`/`isBusy` into `sqliteutil`; `context.Context` propagation through `internal/vault` (store-side dropped — see §7); route `session.SessionDir()` through the shared `config.ClaudeProjectsDir()`.

## Architecture deltas (vs. v1)

```
internal/vault/
  codec.go        NEW  zstd encode/decode at the BLOB seam (encoding-column keyed)
  merge.go        NEW  MergeFrom: source-vault iteration + idempotent upsert
  snapshots.go    NEW  vault_snapshots writes/reads (cold storage)
  migrations.go   EDIT first real migration (vault_snapshots) + migration runner
  store.go        EDIT decode on read / encode on write; ctx params; snapshot stmts
  import.go       EDIT 0-msg exclusion; ctx already present
  discovery.go    EDIT all-projects discovery helper; single-session DiscoverSession (PreCompact)
  tui/            EDIT keybindings, filter, glamour (build-tagged) render path
internal/sqliteutil/
  sqliteutil.go   EDIT host beginImmediate/isBusy; new Rekey()/RekeyResult (backup-API, no stdout); OpenSourceForMerge
internal/store/
  *.go            EDIT consume shared beginImmediate/isBusy/Rekey (no ctx propagation — dropped, see §7)
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

**Explicit per-blob `encoding` discriminator (corrected after design review).** An earlier draft proposed self-describing zstd magic-byte detection (peek 4 bytes; raw JSONL starts with `{`, a zstd frame with `0x28B52FFD`, so they can't collide). That is **only safe for the main transcript**, which is always JSONL. It is **unsafe for `vault_files.raw_content`**, which holds arbitrary sidecar bytes — tool-results, build logs, screenshots, even files that are themselves compressed (`discovery.go:15`, `collectAssociatedFiles`). A raw sidecar that happens to begin with `0x28B52FFD`, or a sidecar that is already a zstd file, would be mis-decoded on read and corrupted on restore. So v2 uses an **explicit `encoding` column** beside each blob — `vault_sessions.encoding` and `vault_files.encoding` (both added by migration `0001`; legacy rows have `encoding IS NULL` → read as raw), and `vault_snapshots.encoding` (baked into the new table's DDL). `encoding ∈ {NULL|'raw', 'zstd'}`; the discriminator is authoritative for arbitrary bytes — no magic-byte guessing. Tiny blobs where compression doesn't shrink are stored `raw`. A regression fixture stores a raw sidecar that begins with the zstd magic and asserts it round-trips byte-identical.

**Compression requires the migration runner.** Adding `encoding` columns is the **first real vault migration** (`0001_blob_encoding`), so V2.5 must first build the migration runner (`vaultMigrationApplied` + apply-loop) that today's `migrations.go` lacks. The `vault_snapshots` migration (`0002`) reuses it.

**Invariant preserved:** `content_hash`, `size_bytes`, and FTS text are all computed on **uncompressed** bytes (see `import.go:computeContentHash`, `buildRecord`). Compression is applied *only* to the bytes handed to the `INSERT`/`UPDATE` BLOB parameters, and reversed *immediately after* `Scan` in `GetSession`/`GetFiles`. Idempotency, the larger-wins merge tiebreaker, and search are byte-for-byte unchanged.

**Seam:** `internal/vault/codec.go` — `encodeBlob([]byte) (data []byte, encoding string)`, `decodeBlob(encoding string, data []byte) ([]byte, error)`. Write callers select `encoding` and store it alongside the blob: `store.go:writeRecord` (raw_jsonl + `vault_sessions.encoding`), `writeChildren` (file content + `vault_files.encoding`), the snapshot writer. Read callers pass the row's `encoding`: `scanSessionMeta`/`GetSession`, `GetFiles`, snapshot reads, the source side of `MergeFrom`, and `render.go`/`restore.go`/TUI viewer (all go through `GetSession`/`GetFiles`, so they inherit decode for free).

**Downgrade / mixed-version safety.** The `encoding` column makes *v2's own* reads correct over arbitrary bytes, but does **not** make a v2 vault readable by a v1 binary: v1 selects `raw_jsonl` and treats compressed bytes as corrupt JSONL (it doesn't know the column exists). With the multi-machine persona, once any machine writes a compressed blob (the default) or runs `compact`, the shared `vault.db` is effectively **v2+-only** — an older binary on another machine silently gets garbage. Mitigations: (1) document the "upgrade all machines together; the vault becomes v2+-only after first compression" constraint in the design + README; (2) on the first compressed write, set a `vault_meta` `min_reader_version` marker, **and have v2's own `openDB()` read it on open and refuse with a clear error when it exceeds the binary's `supportedReaderVersion` constant (`2` in v2)** — without that read step the marker is inert and protects no one. It can't protect against v1 (which predates the marker), but a future v3 that bumps the marker to `3` is then refused by a v2 binary rather than silently mis-reading the DB; (3) a `CAPY_VAULT_NO_COMPRESS` escape hatch lets a user keep a vault v1-readable during a staggered rollout.

**`capy vault compact`:** existing legacy rows (`encoding IS NULL`) stay uncompressed until rewritten, so the file does not shrink on upgrade. `compact` first **busy-pre-checks** (a `Checkpoint()` that reports busy pages → abort with "stop the server first", mirroring rekey) *before* the rewrite phase, so it fails fast instead of doing all the UPDATE work and only discovering contention at VACUUM. It also **aborts early if `CAPY_VAULT_NO_COMPRESS` is set** (with it on, `encodeBlob` compresses nothing, so the rewrite + VACUUM would be pure I/O). It then rewrites every legacy (`encoding IS NULL`) blob by running it through `encodeBlob` and **persisting the encoding that call returns** — `'zstd'` when it shrinks, `'raw'` when it doesn't (never hard-coding `'zstd'`, which would mislabel an incompressible blob and corrupt its read). `'raw'` is a *terminal* state ("compression considered, declined"); only `NULL` (never-considered, legacy v1) rows are selected, so a second `compact` finds nothing to do — a true no-op. (A row stored `'raw'` by a `CAPY_VAULT_NO_COMPRESS` import is intentionally *not* recompressed later; force-recompression is out of scope.) It then runs `VACUUM` to reclaim freed pages (SQLite never shrinks the file automatically). VACUUM runs on a dedicated single connection after the pool closes, mirroring `Checkpoint`; SQLite's exclusive lock genuinely protects it (unlike rekey's file-swap — see §2). Set `PRAGMA temp_store = MEMORY` for the VACUUM so the transient copy isn't written to an unencrypted on-disk temp file (the §HMW goal is explicitly *not* to widen the secret-exposure surface; sqlite3mc likely encrypts temp files, but in-memory is belt-and-suspenders). Report before/after file size.

## 2. `capy vault rekey`

**Goal:** rotate a compromised `CAPY_VAULT_KEY` without decrypt-and-reimport.

**Approach (backup-API, not `PRAGMA rekey`):** `cmd/capy/encrypt.go:rekeyEncrypted` already rotates an *already-encrypted* DB by opening the source with the old key, checkpointing, copying into a fresh temp DB opened with the new key via the SQLite **backup API**, then `swapAndVerify` (remove WAL/SHM, `.bak` the original, move temp into place, verify open). This sidesteps the WAL/`PRAGMA rekey` incompatibility (ADR-020) entirely — no journal-mode dance needed — because it writes a brand-new file rather than rekeying in place.

**Refactor (V2.7):** those helpers (`openEncrypted`, `checkpointDB`, `backupDB`, `swapAndVerify`, `rekeyEncrypted`) live in `package main`. Extract the reusable rotation into `internal/sqliteutil` as `Rekey(dbPath, oldKey, newKey string) (RekeyResult, error)` (parameterized; no env coupling). **Strip the user-facing I/O on the way (corrected after review):** today `swapAndVerify` prints hard-coded `capy encrypt:`-prefixed messages (`encrypt.go:261,269,282,283`) — a low-level util must not print, and reused by `vault rekey` it would emit the wrong command name. So `Rekey` **returns** a `RekeyResult` (carrying the `.bak` path) and wrapped errors; each command layer prints its own correctly-prefixed messages. Rewire `capy encrypt`'s key-rotation path to call it and print from the result (its observable output stays equivalent). **This touches encryption-critical code shared with the knowledge store — land it as its own behavior-preserving, green commit** (same caution as v1 Task 1.1). `capy encrypt`'s *initial* unencrypted→encrypted path (`encryptPlain`, which does use `PRAGMA rekey` after `journal_mode = DELETE`) is **not** moved — the vault is always encrypted, so it never needs it.

**`capy vault rekey` (V2.8):** prompt for the current passphrase, read the new one from `CAPY_VAULT_KEY` (the new key must already be exported — `rekey` **errors if it is unset**; note this is *not* identical to `capy encrypt`, which falls back to a "New passphrase:" prompt when `CAPY_DB_KEY` is unset — see `encrypt.go:64-70`). Because a forgotten env-var update would silently rekey old→old (a no-op the operator believes rotated a compromised key), `rekey` **confirms before proceeding** ("rotating to the key currently in `CAPY_VAULT_KEY` — proceed?") and **rejects a new key equal to the old**. It then calls `sqliteutil.Rekey(VaultDBPath(), old, new)`. **Do not run a `VaultStore.Checkpoint()` pre-check** — `Checkpoint()` opens the DB with `CAPY_VAULT_KEY`, which now holds the *new* key, so it cannot open a DB still encrypted with the *old* key and would fail before rotation begins (corrected after review). The checkpoint that flushes the WAL happens *inside* `Rekey`, on the source connection opened with the **old** key (`rekeyEncrypted`'s `checkpointDB(srcDB)`), so no separate pre-check is needed.

**The old-key `.bak` is a residual secret (corrected after review).** `swapAndVerify` renames the pre-rotation DB to `<vault>.bak` and never removes it (`encrypt.go:249`). For `capy encrypt` that `.bak` is a safety net; for `rekey` — whose entire purpose is rotating a **compromised** key — it is a liability: the `.bak` is still encrypted with the old key, so anyone holding the compromised key plus filesystem access can decrypt it, directly at odds with the HMW's "without widening the local secret-exposure surface." So `rekey` **warns prominently** that `.bak` retains the old key and adds a `--remove-backup` flag to unlink it once the new file verifies open. The fix lands in the shared helper (`Rekey` returns the `.bak` path rather than printing), so `capy encrypt` surfaces the same warning and can adopt the same flag. Note this is *deletion*, not guaranteed erasure: on SSDs and copy-on-write/log-structured filesystems, wear-levelling and CoW can leave recoverable copies, so the warning states that true erasure depends on the disk/filesystem and is the operator's responsibility.

**Concurrency is a hard "stop the server first" requirement, not a guarded one (corrected after review).** Unlike `compact`'s VACUUM — which SQLite's exclusive lock genuinely protects — `rekey` finishes with `swapAndVerify`, a filesystem `rename`. A `rename` is **not** mediated by SQLite locking, and a busy-page check is only a proxy for "server running": an idle-but-attached process with no pending WAL passes it, then keeps writing to the now-unlinked old file descriptor → lost writes until restart. So `rekey` documents (in `--help` and the README) that the MCP server **must be stopped** first; the old-key checkpoint inside `Rekey` is a best-effort guard, not a guarantee.

## 3. `capy vault merge --from <path>`

**Goal:** v1 cross-machine is destructive replace. `merge` imports sessions from a source vault into the destination without overwriting the destination wholesale.

**Design:** open the source vault with its key (`--key` flag or `CAPY_VAULT_MERGE_KEY`; default to `CAPY_VAULT_KEY` when both machines share a key) via a dedicated `sqliteutil.OpenSourceForMerge` (Open Decision #2, now resolved — it requires a **writable** source; see below). **WAL handling matters (corrected after review):** a *copied live* source `vault.db` can carry a pending `-wal`; opening it with `immutable=1` would silently skip WAL-resident rows, and opening WAL read-only without the `-wal`/`-shm` sidecars can fail outright. `OpenSourceForMerge` therefore opens the source with a normal (writable) connection and **checkpoints any pending WAL first** (folding it into the main file — non-destructive to data), then performs only read queries. The checkpoint is a write, so the source file and its directory must be writable (WAL also needs a writable `-shm`); a read-only source (read-only mount, immutable file, backup media) must be temp-copied first and the copy opened — which is why the helper is **not** named `OpenReadOnly` (it is not a pure read-only open). A cleanly-closed source has no pending WAL (the WAL-checkpoint-on-close invariant), so this is a no-op in the common case; it's the live-copy case that needs it. Iterate source `vault_sessions` + `vault_files`, decoding blobs by their `encoding` column (source may be compressed). **A v1 source predates the `encoding` column** (added by migration `0001`); `MergeFrom` feature-detects it via `PRAGMA table_info` and treats every blob as `raw` when the column is absent — a blind `SELECT encoding` would raise "no such column" against a v1 source. For each session, apply the **same idempotent decision the disk import uses** (`dest.SessionDigest(uuid)` → skip if same `content_hash` or smaller total `size_bytes`; else insert/replace). Re-scan the decoded `raw_jsonl` + subagent blobs through the existing scanner to rebuild FTS in the destination's current schema (don't copy source FTS rows — re-scanning guarantees schema consistency across versions). Apply the same 0-msg exclusion as disk import (Task 11).

**Carry `vault_snapshots` too (added after review).** Snapshots are the pre-compaction data-loss safety net — the whole reason the durability bucket exists. If `merge` (the portability mechanism) left them machine-local, the "complete, durable, **portable** forever-archive" claim and Success criterion #3 would be silently false. So when the source has snapshots, `MergeFrom` iterates `vault_snapshots` and inserts them into the destination, deduped by the same `UNIQUE(session_uuid, content_hash)`, after the parent session row exists. **Source feature-detection (corrected after review):** because the source is opened without migrating it (Open Decision #2), an older or PreCompact-dropped source may have **no `vault_snapshots` table at all** — querying it blindly raises "no such table", not the no-op an earlier draft assumed. `MergeFrom` therefore probes `sqlite_master` for the table first and **skips the snapshot step entirely when it is absent** (a true runtime no-op). Because all of v2 ships as one unit with PreCompact included, the *destination* always has the table and `InsertSnapshot`; only the *source* can lack it. (At the task level, `merge`'s snapshot-carry subtask consumes `InsertSnapshot` from the snapshots task, so on the shared branch it is sequenced after that task's commit — see tasks.md Task 9 deps.)

**Metadata carry:** preserve the source row's `machine_id`, `claude_project_dir`, `project_path`, `git_branch` (do **not** recompute `project_path` from cwd — the source already resolved it, possibly for a path that doesn't exist on this machine).

**Concurrency (added after review):** `merge` writes only the *destination* via batched `beginImmediate`, so a concurrent server-startup sweep on the same `vault.db` is absorbed by `busy_timeout` + retry — the same posture as `import`. Unlike `rekey` (a filesystem swap) it needs no "stop the server first" requirement, and unlike `compact` (VACUUM) it needs no busy pre-check. (The *source* is a different file, normally not the one the server holds open.)

**Refactor seam:** v1's `Import` is coupled to disk (`os.ReadFile`). Rather than rework it, `MergeFrom` builds `SessionRecord`s from in-memory source blobs and reuses `VaultStore.SessionDigest` + `InsertSession`/`ReplaceSession`/`WriteBatch`. Extract the FTS-rebuild-from-bytes portion of `buildRecord` into a helper both paths share (`buildFTS(uuid, mainBytes, files)`), so the scanner wiring isn't duplicated.

## 4. All-Projects Server-Startup Sweep (opt-in)

**Goal:** v1 sweeps only the current project. Opt-in flag walks all projects.

**Design:** gate on `CAPY_VAULT_SWEEP_ALL` (env) — **off by default**. When set, `vaultSweep` discovers from `config.ClaudeProjectsDir()` (the projects root, which `DiscoverSessions` already auto-detects) instead of `ProjectSessionDir(s.projectDir)`. Everything downstream is unchanged: same background goroutine, same `bgWg`, same cooperative `ctx` cancellation, same batched import. design.md flags the trade-off — startup-walk latency + `vault.db` write contention across hundreds of projects — which is exactly why it's opt-in and not default. Log a one-line summary of how many projects were swept.

## 5. PreCompact Archival

**Hard gate — V2.0 first.** `handlePreCompact` is a stub. Assumption #2 (the hook fires **before** Claude Code mutates the session file) is **unverified**. V2.0 adds a debug handler (gated behind `CAPY_DEBUG_PRECOMPACT=1`, 0600 temp file) that dumps the raw payload and triggers `/compact`. **The gate is content-level, not mtime-level (corrected after review):** an mtime delta cannot distinguish "hook fired pre-mutation" from "hook fired post-mutation" — by the time the hook runs the file may already be truncated, yet still show a fresh mtime. The real success criterion is: **does the file the hook can read still contain the messages that compaction is about to remove?** V2.0 must capture the file contents *at hook time* and confirm they include pre-compaction turns that the post-`/compact` file no longer has. **If the hook fires *after* mutation (the captured bytes are already the compacted transcript), file-based capture is impossible and V2.13–V2.15 are re-scoped** (e.g. capture from a SessionStart-cached copy, or dropped). Stated loud so the contingency is visible, not buried.

**`vault_snapshots` (migration `0002`, V2.13):** append-only cold storage, separate from the active `vault_sessions` row, so multiple pre-compaction versions of one session don't pollute FTS (per the indexed arch-decision "Hybrid active + cold storage"). Columns: `snapshot_id` (PK autoincrement), `session_uuid` (FK → `vault_sessions`, ON DELETE CASCADE), `content_hash`, `size_bytes`, `captured_at`, `trigger` (`'precompact'`), `encoding` (the blob discriminator — see §1), `raw_jsonl` (compressed BLOB). `UNIQUE(session_uuid, content_hash)` dedups identical re-captures. Index `(session_uuid, captured_at DESC)`. **Not** in FTS. The migration runner (`vaultMigrationApplied` + apply-loop, following `internal/store/migrate.go`) is built in V2.5 for migration `0001`; this is migration `0002` and reuses it.

**Snapshot scope: main transcript only.** `/compact` mutates the **main** session JSONL, not the sidecars (subagents, file-history). So a snapshot stores only the main transcript blob; `vault_snapshots` has no file rows. Its `content_hash` is therefore `sha256(rawJSONL)` over the main transcript bytes alone — **not** the composite `computeContentHash` (main + sidecars) used for `vault_sessions`. The two hashes live in different columns of different tables and never interact; `UNIQUE(session_uuid, content_hash)` dedups identical main-transcript captures. This is why `restore --snapshot` is main-only by design (§ Snapshot CLI).

**Parent row must exist before the snapshot (FK; corrected after review).** With `PRAGMA foreign_keys = ON` (schemaSQL), inserting a snapshot whose `session_uuid` has no `vault_sessions` row fails the FK — and for a brand-new session not yet touched by the startup sweep, that row does **not** exist at PreCompact time. The handler therefore creates/updates the parent **first**, then inserts the snapshot (see Hook handler).

**Hook handler (V2.14) — order matters (corrected after review):** parse the payload per V2.0's findings → locate the session file + session UUID + project dir. Then, **in this order**:
1. **Archive the active session first** by running the existing single-session import path for that one session. **Build the single target `SessionFile` directly from the known (dir, UUID)** via a per-session discovery helper (`DiscoverSession(dir, uuid)` = main `<dir>/<uuid>.jsonl` + `collectAssociatedFiles(<dir>/<uuid>)`), **not** `DiscoverSessions(dir)`-then-filter — the latter walks every session's sidecar tree in the project on the latency-sensitive `/compact` critical path (the hook blocks compaction) just to act on one known UUID. Then `Import` that one `SessionFile`: this reads the pre-compaction **main + sidecars** from disk, applies the normal idempotent decision, and *creates the parent `vault_sessions` row if absent*. Reusing `Import` (not a bespoke main-only upsert) avoids two bugs: it satisfies the snapshot FK, and it never clobbers a larger main+sidecar active row with a main-only blob (a `ReplaceSession` built from a sidecar-less record would `DELETE` then fail to re-insert the sidecars).
2. **Guard the FK, then insert the snapshot.** `Import` does **not** always create the parent row — the Task-11 0-message exclusion (and a read/scan error) yield `StatusExcluded`/`StatusError` with no row. Before `InsertSnapshot`, confirm the parent exists (check `Import`'s per-session status, or `SessionDigest`); if absent, log and skip the snapshot rather than hitting the FK. Otherwise `InsertSnapshot` (dedup via UNIQUE) of the main transcript.

Hooks are short-lived processes (CLAUDE.md invariant): open vault → import one session + insert snapshot → checkpoint/close, fast and synchronous, so the snapshot is durable before compaction proceeds. Failures are logged, never block the user's `/compact`. The triggering case for the FK guard above (a 0-message session being `/compact`-ed) is implausible, but the Task-11 ↔ handler interaction is real, so test explicitly with **no pre-existing parent row** *and* with **an `Import` that produced no row** (excluded/errored).

**Snapshot CLI (V2.15):** `capy vault snapshots <id>` lists snapshots for a session (hash, size, captured_at). `capy vault restore <id> --snapshot <hash>` restores from a specific snapshot instead of the active row. **Main-transcript only (documented limitation):** a snapshot stores no sidecars, so `restore --snapshot` reconstructs only `<uuid>.jsonl` (not the `<uuid>/` sidecar tree). This is correct because `/compact` only mutates the main transcript; the sidecars on disk are still current and are restorable from the *active* row's files. `--help` states this.

**Retention (measurable, not a vague hedge — corrected after review):** v2 MVP defaults to **keep-all-distinct** (`UNIQUE(session_uuid, content_hash)` dedups identical captures; the "archive forever" ethos argues against silent eviction). The open question is whether distinct (growing) pre-compaction states accumulate unacceptably for a heavy `/compact` user. Falsification: on the V2.0 capture corpus, measure snapshot bytes added per session per `/compact` and project monthly growth. **Threshold:** if projected snapshot growth exceeds **the active-session total** (i.e. snapshots would more than double `vault.db`), ship the keep-N-recent-per-session cap (a cheap `DELETE … WHERE snapshot_id NOT IN (SELECT … ORDER BY captured_at DESC LIMIT N)` after each insert) in V2.13 rather than deferring it. Until that measurement exists, keep-all-distinct ships, and the cap stays a ready, scoped mitigation (see Assumption #6, Open Decision #1).

**Delete semantics:** `capy vault delete` cascades to snapshots (full erasure — delete is the erasure tool). Stated explicitly so it isn't a surprise.

## 6. TUI Completion

**Keybindings (V2.11):** wire the design's deferred bindings — `f` (project filter), `c` (copy current message to clipboard), `r` (restore), `R` (resume) — plus in-list fuzzy filtering (v1 disabled the list's built-in `/` so `/` opens global FTS; `f` gets the in-list filter). **`r`/`R` are the destructive/exec surface**: they must `tea.Quit`/teardown the bubbletea program (release the alt-screen + raw TTY) **before** exec'ing `claude --resume` or writing restored files, then optionally relaunch — same care as CLI Task 4b. Copy uses a small clipboard dependency or OSC-52; prefer OSC-52 to avoid a native dep (keeps the binary lean).

**Glamour markdown (V2.12) — behind a build tag.** ⚠️ Dependency caveat (verified via context7): the current glamour is **v2** (`charm.land/glamour/v2`) and pulls **lipgloss v2** (`charm.land/lipgloss/v2`). The vault TUI is pinned to the **v1 line** (bubbletea v1.3.10, lipgloss v1.1.0 — v1 Task 6.1). Pulling glamour v2 would introduce a second lipgloss major. So v2 **must pin glamour to the latest v0.x on the `github.com/charmbracelet/glamour` path** and verify (`go mod graph`) that it resolves `github.com/charmbracelet/lipgloss` (v1 path) and pulls **no** `charm.land/lipgloss/v2`. Rendering lives behind `-tags glamour`: `tui/render_glamour.go` (tag `glamour`) provides a `glamour.NewTermRenderer(...).Render(md)` path; `tui/render_default.go` (tag `!glamour`) keeps the v1 lipgloss-only styling. The default build stays lean; rich rendering is opt-in at build time.

## 7. Correctness & Tech Debt

- **Exclude 0-msg sessions (V2.4):** in `Import`, after `buildRecord`, skip sessions whose scanned `MessageCount == 0` (record a new `StatusExcluded` outcome, not silent). These are empty shells (observed: 7 untitled 2–4 KB rows). A re-import catches them once they gain content (idempotent), so nothing is permanently lost. Mirror the same guard in `MergeFrom`.
- **`beginImmediate`/`isBusy` → `sqliteutil` (V2.1):** the helpers are split across files (corrected after review). In the **store**: `beginImmediate` lives in `internal/store/migrate.go:109`, `isBusy` in `internal/store/retry.go:14`. In the **vault**: both live in `internal/vault/migrations.go` (with a literal `TODO` to move them). Move all into `internal/sqliteutil` as `BeginImmediate`/`IsBusy`; both stores import it. Behavior-preserving.
- **`context.Context` propagation — vault only (V2.2 / Task 4); store-side DROPPED.** Convert `internal/vault` to `*Context` variants (`QueryContext`/`ExecContext`/`BeginTx`), replace `VaultStore.ctx()` (returns `context.Background()`, `store.go:200`) with a threaded `ctx`, and add `ctx` params to its public methods — in one behavior-preserving commit. The cancelling caller, the all-projects sweep (V2.10), lives in this package, so threading `ctx` makes the vault's public API cancellation-ready where it matters. **Store-side propagation (formerly V2.2a) is dropped**, not deferred-with-intent-to-do: it has **no functional beneficiary** — the knowledge store has no cancelling callers, and the sweep already gets cooperative cancellation from `Import`'s per-session loop check (`import.go:136`) without it. Converting it would spend regression risk on encryption-critical code (CLAUDE.md's foremost invariant) purely for sibling symmetry. Re-trigger: only if a store-side caller ever needs sub-transaction cancellation. (Recorded in tasks.md Task 3 and § Not Doing.) Note vault's batched writes are also short, so even vault-side ctx mostly buys API readiness, not mid-transaction interruption — but it's low-risk and lives with its caller.
- **`SessionDir()` routing (V2.3):** `internal/session/sweep.go:SessionDir()` still mangles against a hardcoded `~/.claude/projects/`; route it through `config.ClaudeProjectsDir()` (already `CLAUDE_CONFIG_DIR`-aware after v1 Task 3.1) so vault and session-sweep agree. Re-run `internal/session` tests.

---

## Schema changes

**Two migrations** (corrected after review — compression now needs schema, since the magic-byte scheme was replaced by an explicit `encoding` column):

- `0001_blob_encoding` (V2.5): `ALTER TABLE vault_sessions ADD COLUMN encoding TEXT` and the same on `vault_files` (cheap — SQLite `ADD COLUMN` doesn't rewrite rows; legacy rows read as `NULL`/raw). This is the **first real vault migration**, so V2.5 also builds the migration runner (`vaultMigrationApplied` + apply-loop).
- `0002_vault_snapshots` (V2.13): create `vault_snapshots` (with its own `encoding` column baked in) + index.

No change to `vault_fts`/`vault_meta` schema (a `vault_meta` `min_reader_version` *row* is written at runtime on first compressed write — not a schema change). Per the `go` `database.md` checklist, schema DDL is human-authored and reviewed; each migration is guarded, idempotent, and applied inside a `BeginImmediate` transaction.

## Dependencies

| Dependency | Version constraint | Why / caveat |
|---|---|---|
| `github.com/klauspost/compress/zstd` | latest stable | `EncodeAll`/`DecodeAll` thread-safe & reentrant; shared encoder/decoder; no streaming. Verified via context7. |
| `github.com/charmbracelet/glamour` | **v0.x (v1-compatible) only** | v2 (`charm.land/glamour/v2`) drags in lipgloss v2 — incompatible with the pinned bubbletea/lipgloss v1 stack. Pin to the `github.com/charmbracelet/...` path; verify `go mod graph` shows no `charm.land/lipgloss/v2`. Behind `-tags glamour`. |
| clipboard / OSC-52 | prefer OSC-52 (no dep) | `c` keybinding; avoid a native clipboard dep to keep the binary lean. |

## Assumptions

**Must be true (dealbreakers):**
1. **PreCompact fires before file mutation.** UNVERIFIED — V2.0 is the gate. If false, PreCompact archival (V2.13–15) is re-scoped or dropped; the rest of v2 is unaffected.
2. **Legacy (v1) blobs are losslessly readable when `encoding IS NULL`.** The explicit `encoding` column (not magic-byte detection) discriminates compressed from raw, so arbitrary sidecar bytes — including ones beginning with the zstd magic or already-compressed files — are never mis-decoded. True by construction; validated by a round-trip + a regression fixture whose raw sidecar starts with `0x28B52FFD`. (Cross-version corollary, NOT an assumption but a stated constraint: a v1 binary cannot read v2-compressed blobs — see §1 Downgrade safety.)

**Should be true (important):**
3. **The SQLite backup-API rekey works for sqlite3mc-encrypted vaults** as it does for the knowledge store (it already ships in `capy encrypt`). Validated by a rekey round-trip test.
4. **Re-scanning decoded source blobs in `merge` reproduces equivalent FTS** to the original import. Validated by a merge → search test.
5. **VACUUM after `compact` reclaims the freed pages** to deliver the projected size win. Validated by before/after `os.Stat` in the compact test.

**Might be true (nice to have):**
6. Snapshot growth is bounded enough that keep-all-distinct is acceptable without a retention cap for typical users. **Falsification (added after review):** on the V2.0 capture corpus, measure snapshot bytes added per `/compact` and project monthly growth; if it would more than double `vault.db` (exceeds the active-session total), ship the keep-N cap in V2.13 instead of deferring (see §5 Retention, Open Decision #1).

## Not Doing

- **Cloud sync / multi-user / Codex sessions / session diffing / real-time watch / automatic retention** — unchanged from v1; still out.
- **Sharing/export with redaction** — still its own future design (secret scanning, deny patterns, LLM review). Not in v2.
- **TUI lazy-windowing viewer & 3-panel split** — deliberate v1 deviations deferred *pending evidence of a problem*; remain documented "do-if-profiling-shows-lag" items, **not** v2 tasks. (Not selected.)
- **`encryptPlain` (unencrypted→encrypted) extraction** — the vault is always encrypted; only the already-encrypted rekey path is shared.
- **Snapshot retention eviction policy** — keep-all-distinct in v2; a configurable cap is deferred (see Assumption #6).
- **Store-side `context.Context` propagation** (`internal/store`) — dropped (was Task 3). No cancelling caller; consistency-only churn on encryption-critical code. Vault-side ctx (Task 4) is kept because its caller (V2.10 sweep) lives in that package. Re-trigger: a store-side caller that needs sub-transaction cancellation.

## Open design decisions (for `/kk:review-design`)

1. **Snapshot retention:** keep-all-distinct (chosen) vs. keep-N-recent-per-session — now with a **measurable trigger** to switch (see §5 Retention / Assumption #6), not an open-ended "revisit."
2. **`merge` source open:** ~~open question~~ **RESOLVED** — a dedicated `sqliteutil.OpenSourceForMerge` that does **not** run schema DDL/migrations against the source, and **checkpoints any pending WAL first** (folding a copied-live vault's `-wal` into the main file) rather than `immutable=1` (which would silently skip WAL-resident rows). It is deliberately **not** named `OpenReadOnly`: the checkpoint is a write, so it **requires a writable source** (writable file + dir; WAL also needs a writable `-shm`) and fails on read-only media — temp-copy a read-only source and open the copy. Because the source schema is left untouched, `merge` **feature-detects** the source's `encoding` column and `vault_snapshots` table (absent ⇒ raw blobs / skip snapshot carry — a v1 source has neither). See §3.
3. **`compact` VACUUM under a running server:** abort-if-busy (chosen) via a busy pre-check *before* the rewrite phase (mirrors rekey, fail-fast). VACUUM itself is genuinely lock-protected by SQLite (unlike rekey's file-swap). Online incremental vacuum rejected as unnecessary complexity.

## References

- v1: [../design.md](../design.md), [../implementation.md](../implementation.md), [../tasks.md](../tasks.md) (follow-ups + "Not Doing")
- ADR-016 (WAL checkpoint on close), ADR-019/020 (encrypted DB, WAL/rekey incompatibility), ADR-017 (source-kind separation)
- Indexed `kk:arch-decisions`: "Hybrid active + cold storage", "Merge semantics: larger size_bytes wins", "Machine identity outside DB"
- `klauspost/compress` zstd `EncodeAll`/`DecodeAll`; `charmbracelet/glamour` `NewTermRenderer` (both via context7)
