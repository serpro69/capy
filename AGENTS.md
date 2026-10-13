# AGENTS.md

This file provides guidance to AI agents like claude-code, codex, and others, when working with this repository.

## Project

`capy` is an MCP server and Claude Code plugin written in Go. It solves context window flooding by keeping raw tool outputs in sandboxed subprocesses and indexing them into SQLite FTS5 with BM25 ranking. It also provides persistent, queryable knowledge across sessions via an encrypted SQLite database.

Originally a Go port of [context-mode](https://github.com/mksglu/context-mode) (TypeScript), capy has evolved independently with its own feature set: mandatory encryption at rest, three source kinds with distinct lifecycle policies, session transcript indexing, multi-platform hook routing, and a tiered retention system.

## Architecture

See [docs/architecture.md](docs/architecture.md) for the full architecture document.

### Key Packages

```
cmd/capy/           CLI entry points (cobra commands)
internal/
  adapter/          Platform adapter interface (HookAdapter) + Claude Code and Codex implementations
  config/           TOML config loading with 3-level precedence, project root detection, DB path resolution
  executor/         Polyglot code executor (11 languages), process group isolation, output truncation
  giturl/           Git platform URL detection (GitHub/GitLab/Bitbucket/Gitea)
  hook/             Hook event dispatch: PreToolUse routing, guidance, security, subagent injection
  platform/         Setup command (writes hooks/MCP config), doctor diagnostics, routing instructions
  retrieval/        Corpus-agnostic FTS5 retrieval core: RRF two-layer fusion, rerank, entity boosting, query sanitization — shared by store (knowledge) and vault (session chunks) via the Corpus interface (ADR-028)
  sanitize/         Secret stripping (regex-based redaction of API keys, tokens, credentials)
  security/         Settings parsing, glob matching, command splitting, shell-escape detection
  server/           MCP server, 10 tool handlers, stats tracking, lifecycle guard, intent search, cross-corpus vault federation
  sqliteutil/       Shared SQLite open/recovery: canary query, corruption classification, backup
  store/            SQLite FTS5 knowledge base: schema, indexing, search, cleanup, encryption, migration
  vault/            Session vault: verbatim archival, FTS5 search (per-line + chunk), discovery, import, cross-machine merge; sole session store (ADR-027)
  vault/tui/        Interactive TUI for vault browsing, search, and session viewing (bubbletea)
  version/          Build-time version injection via ldflags
```

### Critical Invariants

- **Encryption is mandatory.** Normal knowledge access resolves `store.key_file` → database-owner `.env` → distinct main-worktree `.env` → inherited `CAPY_DB_KEY` (ADR-032). Invalid selected credentials fail without fallback. Store/server instances capture one key for all connections; an explicit empty key fails before filesystem side effects. Vault credentials remain environment-only via `CAPY_VAULT_KEY`; wrappers never source `.env`. Tests require both synthetic environment keys. `capy encrypt` keeps its separate prompted-old / environment-or-prompted-new key contract.
- **FTS5 build tag required.** All builds and tests must use `-tags fts5`. The Makefile handles this.
- **WAL checkpoint on close.** The connection pool must be closed before checkpointing (see `store.go:Close()` and ADR-016).
- **WAL + PRAGMA rekey incompatible.** Encryption path must switch to DELETE journal mode before rekeying (ADR-020).
- **Source kinds are schema-enforced.** `CHECK (kind IN ('ephemeral', 'durable', 'session'))` — no other values accepted.
- **Vault blob `encoding` column is authoritative.** Compressed (`'zstd'`) vs raw (`'raw'`/`NULL`-legacy) blobs are distinguished by the per-row `encoding` column, never magic-byte detection (sidecars hold arbitrary bytes). Compressed writes require reader version 2; platform-aware writes can require version 3. Each write stamps the version it needs in `vault_meta.min_reader_version`; `openDB` refuses a vault whose marker exceeds `supportedReaderVersion` (3). `content_hash`/`size_bytes`/FTS are always computed on **uncompressed** bytes.
- **Vault rekey uses the backup-API, not PRAGMA rekey.** `sqliteutil.Rekey` writes a fresh new-key file (open old → checkpoint → backup-copy → swap+verify), sidestepping the WAL/PRAGMA-rekey incompatibility above. Shared by `capy vault rekey` and `capy encrypt`.
- **Hooks are short-lived processes.** Each hook invocation is a separate Go process: `capy hook <event>` for Claude, direct `capy codex-launch` dispatch for installed Codex hooks. State persists via `.capy/guidance-<sessionID>.json` files.
- **Codex hooks and MCP share one portable owner.** Commit `.codex/capy-owner` with its generated bindings; preserve its ID during setup. The final `capy-codex-launch-v2` grammar requires compatible capy on PATH: hooks retain a compact signal/failure supervisor; MCP invokes capy directly. Both dispatch in process after owner validation, without a project wrapper. Setup probes PATH before any writes (including default-both setup); `--binary` must identify that same executable. The shared inspector checks all physical ancestors for markers and decoded hook/MCP bindings, validates each adjacent marker, and rejects overlap even for copied IDs. Missing live markers must be restored, never regenerated. Stop/reload affected sessions before moving or removing loaded artifacts; cached commands cannot recover entirely deleted provenance.
- **Codex PreToolUse launch failures exit 2 with repair diagnostics.** Successful structured denials exit 0. Preserve Claude's legacy wrapper crash behavior and MCP status/stdout. Setup registers hooks but never changes project trust, exact-hook trust or feature overrides. Matched asks deny with the exact rule/source because this Codex hook cannot request approval. Hooks remain credential-free; static doctor checks cannot prove activation.
- **Child tool observations are one-use evidence.** `.capy/tool-observations.json` is capped at 64 KiB/128 entries; execute/fetch timestamps expire independently after 60 seconds. All alternatives for a child must be consumed atomically before a routing denial. Serialize updates with the separate permanent `tool-observations.lock` (20 ms maximum lock wait); never unlink or replace that lock file. SessionEnd removes only its stable session's entries and never opens a database.
- **`capy setup` generates artifacts in two places — keep the generator and this repo's committed copies in sync.** Everything `capy setup` writes (`internal/platform/setup.go` + `routing.go`) has a committed counterpart in this repo. A fix applied to the committed file but NOT the generator (or vice versa) works here but ships stale to every consumer that re-runs `capy setup`. This has already bitten us more than once (PR #93 wrapper fix; commit `8d5f2a2` routing wording). The generator is the source of truth; the committed files must be reproducible from it. Any fix MUST edit BOTH sides. The generated artifacts and their generators:
  - `.claude/scripts/capy.sh` ← `capyWrapperScript` (whole file); Codex has no generated wrapper
  - `.capy/AGENTS.md` ← `GenerateRoutingInstructions()` (whole file)
  - `.mcp.json` capy server ← `mergeMCPServer`; `.claude/settings*.json` capy hooks ← `mergeHooks`
  - `.codex/hooks.json` capy handlers ← `mergeCodexHooks` + `codexHookEvents`; preserve external handlers
  - `.codex/capy-owner` ← `codexOwnerContent` with the preserved installation ID (creation: `ensureCodexOwner`); parameterized whole-file coverage
  - `.codex/config.toml` `[mcp_servers.capy]` ← `mergeCodexMCPServer`; hook commands and MCP argv ← `codexHookCommand` / `codexLaunchArgs` in `setup_codex_launch.go`
  - root `CLAUDE.md` import ← `ensureClaudeMDImport`; `.gitignore` capy entries ← `ensureGitignoreEntry`
  - `TestGeneratedWholeFileArtifacts` and `TestMergedArtifactsAreIdempotent` (`internal/platform/`) enforce this. Never "fix" a failure by editing just one side.

  `capy setup --db-repo` (`SetupDBRepo`) is the exception: its two outputs have **no committed counterpart in this repo**, so the sync rule above does not apply to them. The guard hook is an uncommitted git hook (written into the target repo's `.git/hooks/`, never tracked anywhere), and the `*.db-wal`/`*.db-shm` `.gitignore` entries land in the *separate DB repo*, not here. It is drift-guarded by its own generator tests instead — `TestSetupDBRepo_WritesOnlyGitignoreAndHook`, `TestPreCommitHookBlockDBRepo_Content`, and the `TestDriftGuardCoversEverySetupArtifact` DB-repo case.

### Build & Test

```bash
export CAPY_DB_KEY=test-key-for-development   # required for knowledge store tests
export CAPY_VAULT_KEY=test-key                # required for vault tests
make build                                    # CGO_ENABLED=1, -tags fts5
make build-glamour                            # + opt-in glamour TUI markdown (-tags fts5,glamour)
make test                                     # all tests
make test-race                                # with race detector
go test -tags fts5 -count=1 ./internal/<pkg>/... # single package
go test -tags fts5,glamour ./internal/vault/tui/... # glamour-tagged TUI subset
```

### Benchmarks

After changing search, indexing, chunking, or executor code, run benchmarks to check for regressions:

```bash
make bench-quality   # quality benchmarks → bench-results/{branch}.json
```

Compare against a baseline with `make bench-compare BASE=master TARGET={branch}` or view a single report with `go run -tags fts5 ./cmd/qualstat bench-results/{branch}.json`. Quality benchmarks are skipped during `go test ./...` (gated by `CAPY_BENCH_RESULTS`). The result file is named after `git rev-parse --abbrev-ref HEAD` with `/` → `-`, so `feat/x` writes `feat-x.json` and a **detached** checkout (e.g. a `git worktree add --detach … master` used to produce the baseline without leaving the branch) writes `HEAD.json` — rename it to `master.json` before comparing. `bench-compare` also runs `benchstat` over `bench-results/{branch}.txt` from `make bench` when `benchstat` is installed, and skips that half otherwise.

Key files: `internal/store/bench_test.go` (retrieval + NIAH), `internal/store/bench_perf_test.go` (performance), `internal/server/bench_integration_test.go` (5000-byte threshold), `internal/store/testdata/bench/*.jsonl` (fixtures). Fixture authoring guide: [benchmark/FIXTURES.md](benchmark/FIXTURES.md).

## ADRs

Architecture Decision Records are in [docs/adr/](docs/adr/).

## Knowledge note labels

Knowledge indexing replaces content with the same source label; it does not append to a category. For plugin-managed notes, use a stable, unique per-concept suffix such as `kk:review-findings:upstream-sync-v1.0.169:fetch-freshness`, and search by the category prefix. Never index a new concept under a bare category label such as `kk:review-findings` or `kk:arch-decisions`. Skip indexing when the finding is already captured in repository documentation.

## Completed Features

Design docs for completed features are in [docs/feat/done/](docs/feat/done/). Each has design.md, implementation.md, and tasks.md.
