# Issue #110: malformed historical Codex rollouts

Status: implemented and validated; the original writer interruption remains unresolved.
Issue: https://github.com/serpro69/capy/issues/110
Investigation date: 2026-09-20

## Evidence

The original `TestCodexCanary` fails before decoding, in `tallyCodexRaw` at
zero-based physical line 182. Reproduced against unchanged source with:

```sh
CAPY_DB_KEY=test-key-for-development CAPY_VAULT_KEY=test-key \
  go test -tags fts5 -count=1 -run '^TestCodexCanary$' ./internal/vault
```

The result was `unexpected end of JSON input` after 13.65 seconds. A separate
binary-mode JSON scan of 189 plain rollouts (34,513 physical lines at scan time;
no compressed rollouts) found three malformed records in two files. The corpus
can grow while these tests run. The originals were read only, never repaired,
redacted in place, or replaced.

File A: `rollout-2026-09-17T08-31-24-01a0ae10-15df-7ba1-909c-aaa8b95ac150.jsonl`

File B: `rollout-2026-09-17T16-12-24-01a0afb6-2241-70a1-a6cd-c9d591f83297.jsonl`

| File | Physical index (editor line) | Record bytes, excluding LF | Start byte | End byte, excluding LF | Shape |
| --- | ---: | ---: | ---: | ---: | --- |
| A | 182 (183) | 2,517 | 1,037,867 | 1,040,384 | `event_msg/item_completed/CommandExecution`, cut immediately after the `formatted_output` key |
| A | 1674 (1675) | 1,127 | 8,612,761 | 8,613,888 | `response_item/custom_tool_call`, cut inside the `input` string |
| B | 2 (3) | 1,250 | 19,230 | 20,480 | `response_item/message`, developer role, cut inside a text string |

All three record ends are multiples of 4,096. File A has 2,758 physical lines
and 19,091,621 bytes. Its metadata reports CLI 0.147.0 and paginated history.
For its first malformed record, the next valid record is
`thread_settings_applied` at 07:08:50.638Z, reusing ordinal 182 after the
06:39:32.499Z partial record. The next valid record is **not** a complete copy of
that partial record. Its second malformed record has ordinal 1673 at
14:12:38.208Z; the next record at 14:12:57.933Z reuses 1673. File B also
reports CLI 0.147.0/paginated history; its next valid record at
14:12:59.660Z reuses ordinal 2 after the 14:12:29.628Z partial record.

Record SHA-256 values, after removing CRLF/LF, are stored beside their exact
basename and physical index in `internal/vault/codex_canary_corruption_test.go`.
Whole-file SHA-256 values at investigation time:

- A: `065664319985158d56db1b1b6e78519258c0179f4fdc812531e48b8ecec68174`
- B: `0ecbe12b1fdfaef1e3e8a87c70088912a3122d4f1e756887144cdd22a1b66076`

These hashes document preservation; they are not entire-file exemptions.

## Why malformed records precede later records

Codex 0.147.0's
[`open_rollout_for_append` and `ensure_rollout_is_newline_terminated`](https://github.com/openai/codex/blob/rust-v0.147.0/codex-rs/rollout/src/recorder.rs#L1755)
open the existing file for append and add a newline if the final byte is not LF.
Its
[`ordinal_state_for_rollout`](https://github.com/openai/codex/blob/rust-v0.147.0/codex-rs/rollout/src/ordinal.rs#L50)
scans backward, skips rejected records, and continues after the last parseable
ordinal. Together these mechanisms explain how an incomplete tail becomes an
interior malformed record on resume, with its ordinal reused. This is an
explanation of the observed layout, not proof of the historical interruption.

**Inference:** the page-aligned cutoffs and resume pattern are consistent with
interrupted writes or storage persistence. They do not distinguish a killed
process, host/filesystem interruption, or external truncation. There is no
evidence here establishing capy's importer as the writer; its decoder consumes
an `io.Reader` and its import path reads the source.

**Unresolved:** the exact event that truncated each record. No contemporaneous
writer/system failure evidence was established. If this recurs, preserve the
original bytes and correlate writer errors and host/storage events around the
record timestamps before appending another exception. Reproducing the original
writer failure is deferred because the available bytes establish the damage and
resume mechanism, not the initiating event.

## Chosen canary policy

The user selected a finite allowance for the three investigated records,
instead of accepting arbitrary malformed JSON. This is entirely test-side:
production's existing warning-and-skip behavior is unchanged.

- Match the exact rollout basename, physical zero-based index, and SHA-256 of
  the record bytes. Ignore only the optional compression suffix and line ending,
  so archive moves, a different Codex home, and compression preserve the match.
- Only a JSON syntax error after line zero can qualify. A valid JSON envelope
  with incompatible field types, malformed metadata, new bad record, moved
  record, or changed bytes still fails.
- Continue the independent raw tally on surviving records. Do not remove bytes
  before decoding: physical line anchors must stay correct.
- For each decode pass (decoder, text renderer, TUI transcript), require exactly
  the expected malformed-line warnings at the expected physical indices and
  warning severity. Missing, extra, misplaced or payload skip warnings fail.
- Keep all existing metadata, human-turn reconciliation, call/result, child-link,
  zero-human, oversize, and consumer assertions. A known corrupted file receives
  no semantic exemption.
- Report the matched historical damage in verbose canary output and aggregate
  counts. A record disappearing because its file was removed or legitimately
  repaired does not require that every machine retain the original corpus.

A blanket syntax-error skip would turn every new truncation into a success.
Skipping whole files would hide regressions in their many intact records.
Repairing source transcripts would destroy the evidence. None is needed here.

## Regression fixtures and validation

`internal/vault/testdata/codex/malformed_history.jsonl` is a small synthetic,
redacted reconstruction of the observed cutoff shapes, with safe fixed
metadata/text and valid later records. It is **not** a verbatim original, and
does not assert preservation of the original page offsets. It includes ordinal
reuse so tests can distinguish physical anchors from history positions.

Focused tests exercise exact matching, archive/compression paths, line endings,
unknown/changed/moved damage, envelope and payload drift, first-line integrity,
surviving tool correlation, display continuation, and the warning contract.
The synthetic fixture uses its own injected test policy; its hashes cannot
enlarge the live-corpus allowance.

Validation:

- Focused corruption/tally regressions: passed.
- Live canary: passed, 191 rollouts; exactly three reviewed records across two
  files, all semantic assertions active.
- Race detector over corruption/tally regressions and Codex decoder tests: passed.
- Full default-environment suite: every package except `cmd/capy` passed,
  including the full vault package. CLI failures inherited the local
  `~/.config/capy/config.toml` value `vault.min_session_bytes = 430080`,
  so 291–741 byte test fixtures were excluded. This patch changes only vault
  test code and does not compile into the CLI binary.
- Full suite with an empty temporary `XDG_CONFIG_HOME`: all packages passed
  (`go test -tags fts5 -count=1 ./...`, both required test keys set). The
  final reviewed regressions and decoder tests also passed under `-race`.
- `git diff --check`: passed. Both original whole-file hashes still match
  the preservation hashes above after validation.

**Deferred test-isolation fix:** `setupVaultEnv` in
`cmd/capy/vault_test.go` and `setupCodexVaultEnv` in
`cmd/capy/vault_codex_test.go` do not isolate the global config directory.
Keep this separate from #110's corruption policy. Next step: set
`XDG_CONFIG_HOME` to `t.TempDir()` in those shared helpers, then verify
their CLI cases pass even when the invoking user's config enables size
filtering. The new size-filter tests already isolate this directory in
`cmd/capy/vault_min_size_test.go`. The validation workaround changes only
the test process environment, not user settings or test assertions.

Independent review via `kk:review-code` (PAL/Gemini 3.1 Pro) identified a
CRLF-checkout mismatch in synthetic fixture fingerprints, plus two initialization
style items. The fixture-policy builder now hashes `trimEOL` bytes; an explicit
CRLF policy-equality regression covers both the producer and consumer sides.
Both initialization items were corrected. The additional code-reviewer
sub-agent could not inspect files because its required read tools were absent;
it produced no review assurance.

No retrieval/indexing implementation changed; search quality benchmarks are
not applicable.
