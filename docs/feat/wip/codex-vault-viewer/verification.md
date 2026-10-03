# Codex vault viewer verification

## Task 1 — compatibility baseline (2026-10-03)

Task 1 adds test fixtures and compatibility gates only. Production code remains
at `0be03aaeaeb8429c480fd631de371007b56b7d38`; no decoder, scanner, renderer,
index, or viewer implementation changed during baseline capture. Tasks 2–6
remain pending, so this is not a claim that the viewer feature is implemented.

### Synthetic compatibility

Three independently authored fixtures cover a wrapped four-file update
(+23 / −14), two independent nested edits in one exec, and a legacy direct
call → event → result. Calls after edit events also pin the existing assistant
grouping. Nine checked-in goldens under `internal/vault/testdata/codex-edits/`
freeze the complete `ScanOutput` (metadata, rows, source lines, and `ToolNames`),
plain text, and Markdown. Viewer output is intentionally outside this gate.

`TestCodexParity_*` exercises deterministic serialization, changes to each reader,
identical-input output mismatches, true append versus rewritten input, additions,
removals, files vanishing between discovery/read, lost category coverage, rejection
of zero comparisons, baseline/manifest integrity, both roots, compression, and
revert variants. The initial synthetic capture and harness tests passed before
any production edit.

### Local corpus baseline

The machine-local baseline is `bench-results/codex-vault-viewer-parity.tsv`.
Its companion `.tsv.json` records original byte lengths and category membership,
bound to the baseline SHA-256. Both files are ignored by Git. Neither raw
recordings nor per-session paths are included in this document.

- Production revision: `0be03aaeaeb8429c480fd631de371007b56b7d38`.
- Baseline SHA-256: `c68edd3a7734c202c364d7e87a02298050fb2e64be7a50ca5337b050c1a08055`.
- Companion SHA-256: `d7178b74f3810e02f6d48c98626c0361b3feec2721f3404acf440bf9674abc4f`.
- Captured recordings: 482, including eligible main and child rollouts discovered
  under both Codex roots. Compressed: 0; revert variants: 0; vanished: 0.
- Capture duration: 67.26 seconds. Environment: Go 1.26.4, Linux/amd64.

Category counts are recordings, not events; one recording can cover several
categories.

| Category | Captured recordings | Unchanged recordings compared |
| --- | ---: | ---: |
| Completed paginated edits | 113 | 111 |
| Failed paginated edits | 8 | 8 |
| Declined paginated edits | 1 | 1 |
| Completed moves | 3 | 2 |
| Legacy direct edits | 13 | 13 |

The TSV reuses the existing parity baseline reader/writer and records uncompressed
input SHA-256 plus a digest of three independently named output SHA-256 values.
The companion permits exact prefix comparison for appends and enforces original
category coverage on unchanged inputs. Existing baselines are never refreshed
on mismatch. Missing/invalid companions, permission/decompression errors, zero
comparisons, and lost representative coverage fail visibly.

Capture and subsequent comparison use the same command, from the repository root:

```sh
GOCACHE=/tmp/capy-codex-vault-viewer-go-cache \
CAPY_DB_KEY=test-key-for-development CAPY_VAULT_KEY=test-key \
CAPY_CODEX_PARITY_BASELINE="$PWD/bench-results/codex-vault-viewer-parity.tsv" \
  go test -tags fts5 -count=1 -run '^TestCodexParityCanary$' -v ./internal/vault
```

The comparison passed in 67.69 seconds: **480 unchanged recordings compared,
zero output mismatches**, 2 appended, 0 otherwise changed, 3 new, 0 removed, and
0 vanished. All required categories retain unchanged representatives. The local
corpus grew to 485 recordings between capture and comparison; the excluded live
inputs do not count toward byte parity or category coverage. Preserve these same
baseline files for Tasks 2–6; do not regenerate them after production changes.

### Validation environment

The initial focused tests and golden capture passed. Subsequent default-cache
commands encountered a read-only Go cache; runs now use the task-specific `/tmp`
cache above. The sandbox also prevents existing Unix/TCP socket tests from
running. An unrestricted `go test -tags fts5 -count=1 ./...` passed every package
except `cmd/capy`, including the full vault and TUI suites (268.54 seconds and
23.12 seconds). These setup failures are not successful test results.

The CLI failures came from personal import policy: fixtures of 291/741 bytes were
excluded by a 196608-byte minimum. Rerunning the entire CLI package with an empty
`XDG_CONFIG_HOME` passed in 214.26 seconds. Together with the unrestricted full
run, every package passed; the original full invocation itself remained a failure.

```sh
capy_test_config_dir=$(mktemp -d /tmp/capy-vault-viewer-config.XXXXXX)
XDG_CONFIG_HOME="$capy_test_config_dir" \
GOCACHE=/tmp/capy-codex-vault-viewer-go-cache \
CAPY_DB_KEY=test-key-for-development CAPY_VAULT_KEY=test-key \
  go test -tags fts5 -count=1 ./cmd/capy
```

The focused synthetic suite also passed with the race detector, including after
the review fixes to failure diagnostics:

```sh
GOCACHE=/tmp/capy-codex-vault-viewer-go-cache \
CAPY_DB_KEY=test-key-for-development CAPY_VAULT_KEY=test-key \
  go test -race -tags fts5 -count=1 \
    -run '^(TestCodexParity_|TestCodexConsumers_EditCompatibility)' ./internal/vault
```

Performance, retrieval quality, both viewer builds, and feature/spec verification
remain Task 6 work. Task 1 changes no search, indexing, chunking, executor, or
runtime code, so it does not require a retrieval benchmark rerun.

### Review and scope

[`kk:review-code:isolated`](.reviews/task-1-code-review-2026-10-03.md) completed
with an independent code reviewer and Gemini 3.1 Pro. All findings were fixed:
rollout I/O errors now omit private paths while retaining the underlying error
identity, vanished-file assertions print only counts, and baseline I/O/JSON
errors include operation context. The independent reviewer approved the privacy
fix; focused race tests passed after the final edits. No findings remain deferred
within Task 1, and no P0/P1 findings required knowledge-base indexing.

The companion manifest is the only implementation detail added to the plan: the
shared two-digest TSV alone cannot distinguish true appends or retain original
category coverage. The captured TSV and its companion remain unchanged after
review. No new project convention needs indexing; this procedure is recorded here
and in the implementation plan.

### Pre-existing test-isolation follow-up

Some CLI vault tests inherit personal import settings because they do not all
isolate `XDG_CONFIG_HOME` (for example, `setupCodexVaultEnv` and standalone restore
fixtures). This is outside the test-only compatibility task and is not changed
here. Run them with an empty `XDG_CONFIG_HOME` for reproducible validation; a
separate follow-up should isolate config consistently in those fixture helpers
and pin a regression with a non-default personal minimum-size policy.
