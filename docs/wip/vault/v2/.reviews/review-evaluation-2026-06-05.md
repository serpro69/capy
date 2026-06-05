# Vault v2 — Review Findings Evaluation

> Created: 2026-06-05
> Inputs: 4 independent design-review sessions (R1–R4)
> Method: each claim corroborated across reviews, then verified against the v2 docs and the live codebase.
> Status: **applied** (2026-06-05) — all 12 confirmed items fixed in design.md / implementation.md / tasks.md under the single-PR delivery model (see Disposition).

Reviews are labelled R1–R4 in the order supplied. "✓N" = N independent reviews raised it.

## Corroboration matrix

| ID | Claim | Reviews | ✓ | Verdict | Fix priority |
|----|-------|---------|---|---------|--------------|
| A | Store-side ctx: Scope + Architecture-deltas still say `internal/store` gets ctx, but it's dropped everywhere else; Task 3 keeps executable checkboxes | R1, R2 | ✓2 | CONFIRMED | P1 (consistency) |
| B | `compact` hardcodes `encoding='zstd'` while `encodeBlob` may return `'raw'` → corrupt reads + false "second run is no-op" | R1, R2 | ✓2 | CONFIRMED | **P1 (correctness)** |
| C | `merge` Task 9.4 calls `InsertSnapshot`/reads `vault_snapshots` built in Task 14; no declared 9→14 dep; if V2.0 unfavorable → build break, not "no-op" | R1, R3, R4 | ✓3 | CONFIRMED | **P1 (build/structure)** |
| D | `merge` reads v2-only `encoding` column **and** `vault_snapshots` from a source it deliberately doesn't migrate → "no such column/table" on a v1 source | R2, R3 | ✓2 | CONFIRMED | **P1 (correctness)** |
| E | impl.md assumption #2 still asserts magic-byte safety — stale after the explicit-`encoding`-column correction | R1 | ✓1 | CONFIRMED | P3 (trivial doc) |
| F | `rekey` leaves `<db>.bak` decryptable by the **old/compromised** key; contradicts HMW + SC#4 | R3 | ✓1 | CONFIRMED | **P1 (security)** |
| G | `OpenReadOnly` is a misnomer — it checkpoints the source (a write); breaks on read-only media/immutable source | R3 | ✓1 | CONFIRMED | P2 (design smell) |
| H | Extracting `swapAndVerify` into `sqliteutil` drags hardcoded `capy encrypt:` stdout/stderr into a low-level util (wrong prefix for `vault rekey`, layering violation) | R3 | ✓1 | CONFIRMED | P2 (layering) |
| I | `min_reader_version` is written but never read by any v2 code → protects no one; design claim "protects v2→v3+" is false as specified | R3, R4 | ✓2 | CONFIRMED | P2 (dead safeguard) |
| J | `rekey` "consistent with `capy encrypt`" is inaccurate — `encrypt` prompts for the new key, `rekey` errors if env unset; silent-stale-key footgun | R3, R4 | ✓2 | CONFIRMED | P3 (doc/UX) |
| K | PreCompact: if step-1 `Import` excludes (0-msg, Task 11) or errors, no parent row → step-2 `InsertSnapshot` fails FK; untested in 15.5 | R3 | ✓1 | CONFIRMED (low) | P3 (guard+test) |
| L | PreCompact handler does `DiscoverSessions(dir)`→filter — walks **every** session's sidecar tree on the `/compact` critical path to act on one known UUID | R4 | ✓1 | CONFIRMED | P2 (latency) |
| M | `merge` concurrency vs a running server is unstated (compact/rekey both address it) | R4 | ✓1 | CONFIRMED (trivial) | P3 (doc) |

No finding was rejected. Two had their recommended *fix* adjusted (F, and the C+D resolution) — see below.

---

## Detail, evidence, and recommended fix

### A — store-side ctx is both dropped and still in scope/deltas  (P1 consistency, ✓2)
- **Evidence.** `design.md:39` (Scope, Correctness bucket): "context.Context propagation through **both** internal/store and internal/vault". `design.md:56` (Architecture deltas): `internal/store/ *.go EDIT ctx propagation; consume shared beginImmediate/isBusy/Rekey`. These contradict `design.md:150` (§7), `design.md:193` (Not Doing), `tasks.md:53-59` (Task 3 DROPPED), and `implementation.md:33-35`.
- `tasks.md:58-59`: Task 3 is `Status: dropped` yet still carries unchecked actionable boxes `3.1`/`3.2` ("Convert internal/store … → *Context variants") and `Size: —`.
- R2 cites `design.md:150` as a stale spot; that line is actually the *correct* (dropped) statement — the genuinely stale lines are **39 and 56**. R2's prose is right; the citation is imprecise.
- **Fix.** (1) `design.md:39`: drop "through both internal/store and" → "context.Context propagation through internal/vault". (2) `design.md:56`: remove "ctx propagation;" — keep "consume shared beginImmediate/isBusy/Rekey" (store *does* consume those per Task 1). (3) `tasks.md` Task 3: convert `3.1`/`3.2` from checkboxes to rationale prose (non-actionable) so no implementer executes a dropped task.

### B — compact vs encodeBlob raw semantics  (P1 correctness, ✓2)
- **Evidence.** `implementation.md:54`: `encodeBlob` returns `(b,"raw")` when `CAPY_VAULT_NO_COMPRESS` is set *or* the blob doesn't shrink. `implementation.md:74` + `tasks.md:102`: compact does `UPDATE … SET raw_jsonl = ?, encoding = 'zstd'` with `encodeBlob(rawBytes)` — i.e. it writes the literal `'zstd'` regardless of what `encodeBlob` actually produced.
- **Two real defects from one root.** (a) **Corruption** (R2): an incompressible/NO_COMPRESS blob is stored raw but labelled `'zstd'`; on read `decodeBlob("zstd", rawBytes)` → `DecodeAll` fails on a non-frame → read error. (b) **False no-op** (R1): `WHERE encoding IS NULL OR encoding = 'raw'` plus "every row becomes zstd; second run is a no-op" (`implementation.md:78`, `tasks.md:105`) is impossible when some rows are legitimately incompressible — the assertion "every row now has `encoding='zstd'`" fails and such rows are re-selected forever.
- **Fix.** compact must persist `encodeBlob`'s **returned** encoding verbatim (never hardcode). Then redefine the idempotency signal: select **`WHERE encoding IS NULL`** only (legacy/uncompacted), so an incompressible legacy row settles to an explicit `'raw'` and is not retried; second run finds no NULL rows → true no-op. Update the test assertion to "no row is NULL; each is `'zstd'` or `'raw'`". Additionally specify compact's behavior under `CAPY_VAULT_NO_COMPRESS` (warn + skip rather than rewrite-everything-raw + VACUUM for nothing).

### C + D — merge / snapshot / source-schema coupling  (P1, ✓3 / ✓2)
These are two faces of one structural problem; fix them together.
- **C (build-time).** `tasks.md:134` Task 9 `Depends on: Task 5, Task 11`. `tasks.md:145` subtask 9.4 calls `InsertSnapshot` + reads `vault_snapshots`. `tasks.md:200,209` build those in Task 14, gated on `Task 0 (favorable)`. No 14→9 edge in the graph (`tasks.md:254-270`). Verified in code: `vault_snapshots`, `InsertSnapshot`, and the migration runner **do not exist** (`migrations.go` has only `ensureVaultMigrationsTable`). So if V2.0 is unfavorable and Task 14 drops, 9.4 references undefined symbols — a build break, not the "no-op against an empty table" design.md:106 claims.
- **D (run-time).** `implementation.md:99` / `tasks.md:142`: `OpenReadOnly` deliberately skips `schemaSQL`/`migrateVault` on the source (correct — don't mutate someone's vault). But 9.3 then `SELECT`s the `encoding` column and 9.4 `SELECT`s `vault_snapshots`. A **v1 source** has neither (encoding added by migration 0001; snapshots by 0002) → "no such column: encoding" / "no such table: vault_snapshots". R2 correctly notes this hits the `encoding` column too, which is broader than the snapshot subset R3/R4 focus on.
- **Recommended fix (decision needed — see below).** (1) **Make the snapshot storage layer unconditional**: migration `0002_vault_snapshots` + `InsertSnapshot`/`ListSnapshots`/`GetSnapshot` ship regardless of V2.0; gate only the *populating* path (Task 15 PreCompact hook) — and arguably the snapshot CLI read (Task 16) stays too, since a machine can *receive* snapshots via merge from a PreCompact-enabled peer. Then the dest always has the table and design.md:106's no-op reasoning becomes true at build time. Add Task-14-schema as a real dep + graph edge for Task 9. (2) **Feature-detect the source** in merge: `PRAGMA table_info(vault_sessions)` → no `encoding` ⇒ treat all source blobs as raw; `sqlite_master` probe → no `vault_snapshots` ⇒ skip snapshot carry. (3) Add a **v1-source merge test**.

### E — stale magic-byte assumption  (P3 trivial, ✓1)
- **Evidence.** `implementation.md:197` assumption #2: "zstd frame magic never collides with raw JSONL leading bytes (true by construction…)" — the design abandoned magic-byte detection for the explicit `encoding` column (`design.md:76`, §1 "corrected after design review"). `design.md:176` assumption #2 was updated; impl.md's copy was not.
- **Fix.** Replace `implementation.md:197` with design.md's wording: "Legacy (v1) blobs are losslessly readable when `encoding IS NULL`; arbitrary sidecar bytes (incl. ones starting `0x28B52FFD`) are never mis-decoded because the column — not magic-byte guessing — is authoritative."

### F — rekey leaves the old key able to decrypt `.bak`  (P1 security, ✓1)
- **Evidence.** `encrypt.go:249` `swapAndVerify` renames the original to `<db>.bak` and never removes it. `design.md:89` / Future-Improvements `design.md:476` state rekey's purpose is rotating a **compromised** key; `design.md:29` SC#4 lists ".bak preserved" as success. The retained `.bak` is still encrypted with the old key → compromised-key + FS access = full plaintext. Directly contradicts the HMW ("without widening the local secret-exposure surface").
- **Fix (adjusted from R3).** Acknowledge the tradeoff in §2 + SC#4; after a verified rekey, offer `--remove-backup` (plain unlink) and **warn loudly** that `.bak` retains the old key. Note R3's `--shred-backup` is *unreliable* on SSD/CoW/log-structured filesystems (wear-levelling, copy-on-write) — promise removal, not guaranteed erasure; document that true erasure is disk/FS-dependent. Pre-existing in `capy encrypt`; fix in the shared helper so both benefit.

### G — OpenReadOnly is a misnomer and assumes a writable source  (P2, ✓1)
- **Evidence.** `implementation.md:99`/`tasks.md:142`: it "checkpoints any pending WAL first" — a write to the source main file + `-wal` truncate, requiring write access. WAL mode also needs a writable `-shm`. So the name is false and it fails on genuinely read-only sources (read-only mount, immutable file, optical/backup media).
- **Fix.** Rename (`OpenSourceForMerge`), document the writable-source requirement, and temp-copy the source when read-only. **Also reconsider whether the checkpoint is needed at all**: a normal (read-write-capable) open over a `.db`+`-wal`+`-shm` triple already sees WAL-resident rows without an explicit checkpoint; the documented pre-copy `capy vault checkpoint` is the real fix for the copied-live case. If so, drop the in-merge checkpoint and require the sidecars be copied (or temp-copy) — which would let it be closer to genuinely read-only.

### H — shared Rekey extraction drags command-specific I/O into sqliteutil  (P2, ✓1)
- **Evidence.** `encrypt.go:261,262,269,271,273,275,282,283` — `swapAndVerify` prints `capy encrypt:`-prefixed messages to stdout/stderr (incl. "done. Encrypted: …"). `implementation.md:83` extracts `swapAndVerify` into `sqliteutil` as-is. Called from `capy vault rekey` it would emit "capy encrypt: … Encrypted" — wrong command, wrong verb — and a low-level util doing user-facing I/O is a layering violation.
- **Fix.** Strip all `fmt.Print*` from the extracted helpers; return structured errors/results (the CRITICAL rollback paths become wrapped errors carrying the `.bak` path). Each cmd layer (`cmd/capy/encrypt.go`, `cmd/capy/vault.go`) prints its own prefixed messages. Flag in Task 7 that this makes V2.7 a "move **+** I/O-extraction", not a pure behavior-preserving move — `capy encrypt`'s output must stay equivalent post-move.

### I — min_reader_version written but never read  (P2, ✓2)
- **Evidence.** `design.md:84` (mitigation 2) + `implementation.md:62` + `tasks.md:87` all specify only *writing* the marker on first compressed write. No task adds an open-time read/refuse path. v2 writes "2" but never compares it, so it protects nothing — a hypothetical v3 bumping it to "3" would still be opened (and mis-read) by v2.
- **Fix (pick one).** (a) Add a small `openDB()` check: read `vault_meta.min_reader_version`, refuse with a clear error if it exceeds v2's supported-reader constant — makes the v2→v3+ claim real (cheap, ~10 lines + a test). **Recommended**, since the design explicitly sells the protection. (b) Else downgrade the claim to "records intent; enforcement deferred to the version that adds the check," and mark the marker write-only.

### J — rekey "consistent with capy encrypt" is inaccurate  (P3, ✓2)
- **Evidence.** `design.md:95` / `implementation.md:91`: rekey reads the new key from `CAPY_VAULT_KEY` and **errors if unset**, "consistent with `capy encrypt`". But `encrypt.go:64-70`: `capy encrypt` reads `CAPY_DB_KEY` *and falls back to prompting* ("New passphrase:") when unset. The analogy is false, and there's a real footgun: forgetting to update `CAPY_VAULT_KEY` first silently rekeys old→old (a no-op the user believes rotated a compromised key).
- **Fix.** Correct the phrasing (encrypt prompts; rekey errors). Spell out operator steps in `--help`. Recommended safety add (R4): a confirmation showing it will "rotate to the key currently in `CAPY_VAULT_KEY`" and/or reject when the new key equals the old.

### K — PreCompact FK edge when no parent row is created  (P3 low, ✓1)
- **Evidence.** Task 15 (`tasks.md:222`) Import-first to create the parent row for the snapshot FK. But Task 11 (`tasks.md:169`) makes `Import` *exclude* `MessageCount==0` sessions (no row), and a read/scan error also yields no row. Either ⇒ step-2 `InsertSnapshot` fails the FK. Triggering case (a 0-msg session being `/compact`-ed) is implausible — hence low — but the Task 11 ↔ Task 15 interaction is real and untested.
- **Fix.** Handler verifies the parent row exists (or checks Import's per-session status) before `InsertSnapshot`; if absent, log + skip the snapshot (never crash `/compact`). Add the excluded/failed-parent case to the Task 15.5 matrix. Keep proportionate — a guard + test, not a redesign.

### L — PreCompact walks the whole project on the critical path  (P2, ✓1)
- **Evidence.** `tasks.md:222`/`implementation.md:145`: `DiscoverSessions(dir) → filter to this UUID → Import`. `discovery.go:discoverProject` runs `collectAssociatedFiles` (a `filepath.WalkDir`) for **every** `*.jsonl` in the dir. The hook blocks `/compact` ("durable before compaction proceeds"), so a heavy project pays O(all sessions) sidecar-walk I/O to act on one known UUID.
- **Fix.** Add a per-session discovery helper (`discoverOneSession(dir, uuid)` = `collectAssociatedFiles(filepath.Join(dir, uuid))`) and build the single `SessionFile` directly; reuse it in the handler. State an expected hook-latency budget in §5.

### M — merge concurrency vs a running server is unstated  (P3 trivial, ✓1)
- **Evidence.** §1 compact has a busy pre-check; §2 rekey has the hard "stop the server"; §3 merge says nothing. merge writes the **dest** via `WriteBatch`→`beginImmediate`, so it's retry-tolerant like `import` (not file-swap like rekey) and needs no stop-server requirement.
- **Fix.** One line in §3: "merge writes the destination via batched `beginImmediate` and tolerates a concurrent server sweep via `busy_timeout` + retry — same posture as `import`; no stop-server requirement (unlike rekey)."

---

## Decision (resolved) — C+D under the single-PR model

Delivery model confirmed: **the entire v2 ships as one PR** (PreCompact included, with V2.0 decided pre-PR), tasks implemented independently — each in its own session, one commit per task, all landing on the same branch.

Under this model the **Option 1 vs Option 2 restructure is moot**: with PreCompact in the unit, the snapshot schema + storage ship anyway, so the dest always has `vault_snapshots`/`InsertSnapshot` — there is no "feature might be dropped" build hazard to engineer around, and no doc-restructure earns anything. The **C** concern collapses to a *commit-ordering* note (a task built in a fresh session needs its dependency already on the branch), and **D** survives unchanged (it is about external v1 *source* vaults, independent of how we ship).

**Applied for C+D:**
- Declared the **Task 14 → 9.4 ordering dependency** (Task 9 header + dependency graph) so the merge snapshot-carry commit lands after the snapshots task on the shared branch.
- Added **unconditional source feature-detection** to merge (probe `encoding` column + `vault_snapshots` table; absent ⇒ raw / skip) + a v1-source merge test (9.3, 9.6, design §3, impl).
- Rewrote design.md:106's false "no-op against an empty table" into a true **source**-probing statement.

## Disposition

| ID | Applied where | Note |
|----|---------------|------|
| A | design.md:39, :56; tasks.md Task 3 | Scope + arch-deltas vault-only; Task 3 checkboxes → rationale prose |
| B | design.md §1; impl V2.6; tasks 6.2/6.5 | persist `encodeBlob`'s returned encoding; select `encoding IS NULL` only; abort under `CAPY_VAULT_NO_COMPRESS`; incompressible-blob test |
| C | tasks.md Task 9 dep + graph | 14→9.4 ordering edge (commit-ordering under single-PR) |
| D | design §3, Open Dec #2; impl V2.9; tasks 9.3/9.6 | source feature-detect `encoding`+`vault_snapshots`; v1-source test |
| E | impl Assumptions #2 | synced to the `encoding IS NULL` framing |
| F | design §2 + SC#4; impl V2.7/V2.8; tasks 7.1/8.2/8.3 | `.bak` warning + `--remove-backup`; deletion ≠ erasure (SSD/CoW) |
| G | design §3, Open Dec #2; impl V2.9; tasks 9.1 | renamed `OpenSourceForMerge`; writable-source requirement; temp-copy for RO |
| H | impl V2.7; tasks 7.1/7.2/7.3 | strip `capy encrypt:` I/O → `RekeyResult`; cmd layer prints |
| I | design §1; impl V2.5; tasks 5.4/5.7 | open-time read/refuse of `min_reader_version` + `supportedReaderVersion`; test |
| J | design §2; impl V2.8; tasks 8.1 | corrected the encrypt analogy; confirm + reject new==old |
| K | design §5; impl V2.14; tasks 15.3/15.5 | FK guard before `InsertSnapshot`; no-parent-row test |
| L | design §5; impl V2.14; tasks 15.2 | `DiscoverSession(dir,uuid)` helper; no whole-project walk on the `/compact` path |
| M | design §3; tasks 9.5 | merge concurrency stated (retry-tolerant like `import`) |

New API surface introduced by the fixes (for the implementer): `sqliteutil.OpenSourceForMerge`, `sqliteutil.Rekey → (RekeyResult, error)`, `vault.DiscoverSession(dir, uuid)`, a `supportedReaderVersion` constant + open-time check, and `--remove-backup` on `rekey`/`encrypt`.
