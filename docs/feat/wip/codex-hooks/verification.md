# Codex hooks verification record

> Updated: 2026-10-12
> Current scope: Tasks 1–6; live Codex CLI 0.162.1 acceptance recorded below
> Design: [design.md](design.md) · Dispositions: [review-resolution.md](review-resolution.md)

## Design-stage evidence

- Read all three feature documents, existing setup/wrapper, policy loading and
  matching, hook ask/deny formatting, and the current toolbox startup scripts.
- Read Codex source pinned at `rust-v0.162.1` for hook discovery, command cwd,
  pre-tool failure parsing, PermissionRequest, MCP argv/cwd and output spilling;
  checked official project-layer/trust references. Exact source links and
  qualified conclusions are in the disposition record.
- Replayed the current MCP wrapper argv from `internal/`, after checking the
  relative wrapper was absent there. Result: exit **127**, with
  `bash: .codex/scripts/capy.sh: No such file or directory`. This proves the
  current path-resolution failure only; no server or live Codex session was
  started by this probe.
- No production Go code changed, so no Go suite, race suite or quality benchmark
  was run for this documentation revision. A read-only validator checked all
  34 local links/anchors across the five documents. The initial diff whitespace
  check found space indentation in the task graph; the graph was corrected
  before rerunning the check. The final `git diff --check` passed, as did
  validation of all six task records, dependency references and subtask numbers.

## RD-1 readiness correction

The [readiness finding](readiness-review.md) invalidated the marker-only lookup.
This revision changes the written contract and acceptance plan; it does not
implement the launcher. Re-read the existing policy loader, explicit-project
hook context, wrapper binary lookup and CLI dispatch. They support the stated
consequence (a substituted owner selects different policy/state) and a separate
read-only launch command without initializing storage. The existing TOML
dependency is available; no dependency change is proposed.

The [official hook reference](https://learn.chatgpt.com/docs/hooks) was rechecked
for composition and cwd: matching hooks from multiple files run, and command
hooks use the session cwd. That supports checking all ancestor bindings rather
than assuming the closest config replaced its ancestor. This recheck does not
replace the pinned baseline research or constitute live capy verification.

Design-level traces of the revised preflight, assuming unchanged on-disk
generated bindings during an invocation:

| Fixture | Required outcome before any project wrapper |
|---|---|
| One directory with matching marker and invoked binding | Select that directory |
| Ordinary subdirectory or valid unrelated marker-free config | Select the same sole installation |
| Root/nested installations with distinct IDs | Reject overlapping directories |
| Root/nested installations with copied IDs | Reject overlapping directories; never deduplicate by ID |
| Copied ID, outer marker missing, both outer configs retained | Reject outer binding without its adjacent marker |
| Same case with only outer hook or only outer MCP binding retained | Reject the remaining broken outer binding |
| Equivalent JSON escapes or TOML quoting | Decode to the same binding and reject the same missing-marker case |
| Unreadable, oversized, malformed or symlink config | Fail inspection; do not skip to a nested candidate |
| Marker without the invoked binding, or outside-tree launch | Reject missing binding/owner |
| All outer artifacts removed but its command cached in the host | Outside contract; stop/reload before removal, because copied IDs cannot recover the origin |

The [standard design re-review](readiness-review.md#resolution-and-re-review)
found no remaining design blocker. It explicitly checked the new launcher
boundary, shared inspector, failure supervision, unchanged event stdin, MCP
stdout/status and cancellation requirements. The added subprocess cost and
conservative rejection of uninspectable ancestor configs are documented trade-offs.
No independent reviewer was used.

At the design-correction checkpoint, all six implementation tasks were pending.
Task 2.6 adds the combined
copied-ID/missing-outer-marker acceptance fixture with sentinel wrappers;
Tasks 2/5 cover parsing and setup/doctor parity. No Go suite, runtime test or
benchmark was run for this documentation-only revision. Documentation checks
passed: **45 local links/anchors across six documents**, all six pending task
records, 28 sequential subtasks, acyclic dependencies and `git diff --check`.
The checks read repository files only; no setup, hook activation or trust change
was performed.

## Task 1: main-session CLI

The direct CLI implements `capy hook --platform codex pretooluse|sessionstart`.
Claude remains the default. Task 1 adds a Codex adapter with authoritative
namespaced session identity, platform-aware project selection, shared supported
JSON responses, denial of matched asks with rule/source provenance, malformed
pre-tool denials and compact platform-specific startup guidance. Missing session
identity emits guidance without persisting throttle state. Non-Bash/non-capy
tools pass through without Claude Agent/Task mutation.

`SourcePath` now survives settings loading and command/embedded-command
evaluation. First matched asks are retained while later denies and scanner
errors still win. Claude's ask JSON and legacy policy ordering remain unchanged.
Shared startup content is factored so Claude retains its original wording.

Verification uses synthetic policy files, isolated homes and project directories.
The CLI subprocess tests run without knowledge/vault keys and assert no database
creation. Tests follow the repository's normal FTS5 suite convention so this
coverage runs without an additional integration tag.

| Check | Result |
|---|---|
| Adapter, hook and security package suites, `-tags fts5 -count=1` | Passed |
| CLI hook tests, including `TestHookCodexDispatch` | Passed |
| Adapter, hook and security race checks, `-tags fts5 -race -count=1` | Passed |
| Generated whole-file, merged-artifact idempotency and completeness guards | Passed |
| Full repository FTS5 suite | Completed with environment failures and a pre-existing fetch-stat assertion; see reruns/baseline below. All other packages passed |
| Isolated code review | [APPROVE; no findings](.reviews/task-1-code-review-2026-10-12.md). Independent agent completed; PAL/Gemini unavailable because its configured API key was rejected |

An initial combined test run caught a positional `CommandDecision` literal in
an existing scanner test after the new provenance field was added. The test now
uses named fields and asserts the expected in-memory provenance; the complete
security package passed on rerun. No production assertion was relaxed.

The broad command was `go test -tags fts5 -count=1 ./...`, with synthetic
`CAPY_DB_KEY`/`CAPY_VAULT_KEY` and `GOCACHE=/tmp/capy-go-build`. It exposed two
test-environment constraints:

- Seven vault CLI tests inherited a 196,608-byte minimum import size, excluding
  their tiny fixtures. All seven passed when rerun with an empty temporary
  `XDG_CONFIG_HOME`: the four failing `TestVaultCodex_*` cases (ImportShowRestore,
  MixedPlatformSummary, ParentChildSurfaces, RestoreDefaultRootIsCodexHome),
  `TestVaultRestoreToOutputDir`, `TestVaultRestoreDefaultLocationHonorsConfigDir`
  and `TestVaultMerge_EndToEnd`.
- `internal/config` and `internal/server` need Unix/local HTTP sockets, which
  the default sandbox denied. The config package passed outside that sandbox.
  The complete server package rerun, with socket access and isolated config,
  passed except for the two mapped-IPv4 subcases below.

**Pre-existing server failure, reproduced on the baseline:**
`TestFetchStatsRejectedURLs/batch={false,true}/mapped_IPv4_rejected_by_URL_parser`
asserts zero cache misses but observes one for `https://[::ffff:192.0.2.1]/`.
The same two assertions fail in a disposable `git archive` of unchanged commit
`66cbecc`, using Go 1.26.4 and the focused test selector, before any Task 1 edits.
This is not a green full-suite result. The feature's own checks passed, and no
Task 1 regression was identified. The follow-up in `tasks.md` requires confirming
the intended URL-admission contract, then correcting the validator or fixture
without weakening SSRF protection or depending on a real network timeout.

**Deferred test-fixture isolation follow-up:** `setupCodexVaultEnv` in
`cmd/capy/vault_codex_test.go`, `setupVaultWithSidecar` and
`TestVaultMerge_EndToEnd` in `cmd/capy/vault_test.go` do not isolate global capy
configuration. This predates Task 1 and is outside its implementation scope.
Add `t.Setenv("XDG_CONFIG_HOME", t.TempDir())` to those fixtures, following
`setupVaultEnv`, so the vault CLI tests pass regardless of personal import-size
settings. The isolated test environment is the workaround used here; no vault
behavior or unrelated test assertion was changed.

Final documentation checks passed for local links/anchors and `git diff --check`.
No qualifying review finding needed knowledge indexing; the implementation
decisions and remaining work are already recorded in these feature documents.

Tasks 2–6 remain pending: portable owner bootstrap, installation/registration,
child event dispatch, diagnostics and live Codex acceptance are not part of this
task's completed behavior. The guarded event allowlist and adapter capabilities
carry Task 4 TODOs. This evidence is unit/subprocess verification, not observed
host delivery. Search/indexing/chunking/executor behavior was not changed, so
quality benchmarks are not required for this slice.

## Startup context baseline

Measured directly from repository source on 2026-10-12:

| Capy content | UTF-8 bytes | Producer |
|---|---:|---|
| Generated `.capy/AGENTS.md` | 9,500 | Existing toolbox session-start loader, when present |
| Current `RoutingBlock()` | 3,240 | Existing Go router; baseline for the planned Codex variant |
| Projected combined content | 12,740 | Both startup paths, before toolbox provider/extra instructions |

This is not an observed session with the new integration. The toolbox entry is
currently configured for startup/resume with `additionalContextLimit: 10000`;
the future capy entries will omit that field and keep their own context within
4,096 bytes. Host spill limits operate on individual outputs and do not remove
duplicate instructions from different hooks.

**Accepted temporary overhead:** preserve the external loader until the
[claude-toolbox enhancement follow-up](implementation.md#claude-toolbox-enhancement-pr)
is implemented and the project opts in. On 2026-10-14, the user chose an issue
instead of a PR; [claude-toolbox #170](https://github.com/serpro69/claude-toolbox/issues/170)
now tracks optional cleanup, release/activation prerequisites and preservation
of legacy behavior. No toolbox script was changed by that handoff.

The table above remains the historical projection. [Task 6](#task-6-installed-runtime-and-release-documentation)
later measured 13,801 actual startup/resume bytes, including provider/extras.
The [final v2 acceptance](verification-v2.md#task-5-final-installed-acceptance)
measured a 3,532-byte capy block with independent fixture hooks, not a new
toolbox total. Bytes, host approximate-token estimates and model-visible
previews remain distinct; no reduction is claimed before upstream cleanup.

## Implementation acceptance

- [x] Stable owner marker, bound hook/MCP argv and shared config/marker preflight
  before any project wrapper; correct explicit project owner.
- [x] Root, ordinary subdirectory, move, spaces and sibling-worktree launches.
- [x] Non-Git command resolution labelled separately from unverified host discovery.
- [x] Two-owner/copy/missing-owner rejection before policy, evidence or DB effects.
- [x] Combined copied-ID/missing-outer-marker fixture within one Git repository,
  both configs retained: pre-tool exit 2 with repair stderr, MCP diagnostic
  failure with no protocol stdout, neither wrapper invoked, no policy/state/DB
  access. Repeat with only either outer binding and equivalent escaped strings.
- [x] Bounded config decoding, invalid/unreadable/symlink input rejection, valid
  unrelated config support and setup/runtime/doctor agreement.
- [x] Missing/unreadable/vanished-wrapper and outside-tree bootstrap failures:
  exit exactly 2 plus nonempty repair text for PreToolUse.
- [x] Binary failure/recovery, including a collaborator without capy; exit-0
  structured denials remain distinct from infrastructure failures.
- [x] Synthetic-credential MCP initialization, tools/list and harmless call from
  root and an ordinary subdirectory; hooks separately work without keys.
- [x] Launcher preserves event stdin, MCP stdout/status and cancellation/signal
  delivery without leaving a server process behind.
- [x] Ask denial identifies matched pattern/source, preserves later-deny priority
  and does not turn an unmatched default ask into a denial.
- [x] Startup/child context budget, omitted limit fields and live no-spill result.
- [x] Both trust gates, actual hook delivery and parent/child observation lifecycle.
- [x] Actual mixed-installation startup byte measurements and toolbox follow-up link.
- [x] Required FTS5/race/artifact checks and implementation review dispositions.

## Task 2: portable owner and supervised launch

Task 2 adds the versioned `.codex/capy-owner` marker, the common bootstrap and
`capy codex-launch`. Marker publication uses an exclusive hard link from a
complete temporary file so concurrent creators cannot observe an empty marker.
Setup preserves the winning ID and refuses to recreate missing live markers.
The read-only inspector checks every physical ancestor, beyond Git boundaries,
and validates decoded hook/inline-hook/MCP-alias bindings against their adjacent
markers. Duplicate JSON keys, malformed structures, nonregular/symlink inputs,
oversized files and failed reads are rejected. Configuration JSON preserves
numeric metadata and bounds nesting to 128 levels.

MCP and synthetic hook bindings resolve the same explicit project owner. The
bootstrap runs the installed binary before any project wrapper. Shell and Go
supervision preserve stdin, MCP stdout/status, and signal delivery; Codex
PreToolUse infrastructure failures block with exit 2 and repair diagnostics.
Claude's existing hook exit behavior remains covered by its wrapper tests.
Hook registration itself is Task 3; child events and diagnostics remain pending.

| Check | Result |
|---|---|
| Full platform suite, including marker concurrency, decoded/escaped bindings, setup validation and generated-artifact guards | Passed |
| `TestCodexLaunchLocations` | Passed: root, ordinary subdirectory with unrelated config, paths with spaces, moved tree, sibling linked worktree, non-Git command-only fixture |
| `TestCodexMCPLaunchLocations` | Passed: initialize, tools/list, shell `pwd`, and harmless indexing with a synthetic key; owner cwd and external configured storage verified for each location |
| `TestCodexCopiedOwnerMissingMarker` | Passed: same-Git-tree copy with both bindings, either outer binding alone, and escaped equivalent strings; exit 2/diagnostic MCP failure before sentinel wrappers or state changes |
| `TestCodexOverlappingLaunches` | Passed: distinct/copied IDs, both commands, then missing outer wrapper and marker; no wrapper, observation, or DB side effects |
| Bootstrap/wrapper failure tests | Passed: missing/older/crashed binaries, missing/symlink/crashed wrapper, outside-tree cwd, invalid owner, successful exit-0 denial |
| Controlled file-boundary tests | Passed: injected unreadable config/wrapper and wrapper disappearance after readable-regular validation; supervisor observes launch status 127, which the tested pre-tool supervision maps to 2 |
| Stdin and cancellation | Passed: byte-identical stdin including NUL, serve status 42, outer-bootstrap SIGTERM reaps final process |
| Focused `-tags fts5 -race -count=1` | Passed for platform/adapter/hook; also passed for Codex CLI/platform launch fixtures |
| `go vet -tags fts5 ./internal/platform ./cmd/capy` and diff whitespace checks | Passed |

All hook probes use isolated configuration and no database keys. MCP probes use
synthetic credentials only. These are installed-command subprocess tests, not
proof of host discovery/trust/event delivery; non-Git automatic discovery remains
unverified. No search, indexing, chunking or executor implementation changed, so
quality benchmarks are not required. The TOML merge uses the existing pinned
parser's [v2.2.4 syntax API](https://github.com/pelletier/go-toml/blob/v2.2.4/unstable/parser.go)
to preserve unrelated text and avoid confusing table-like strings/comments with
real table headers; no module versions changed.

The full `go test -tags fts5 -count=1 ./...` run used an empty temporary
`XDG_CONFIG_HOME`, synthetic keys, and local socket access. Every package passed
except the same two `TestFetchStatsRejectedURLs` mapped-IPv4 assertions already
reproduced on the pre-feature baseline in Task 1. This remains a known unrelated
failure, not a green full-suite claim. The CLI package passed (including its
existing vault fixtures with isolated global configuration).

The PAL external-review attempt failed because its configured Gemini API key was
rejected. The independent code-review agent supplies the isolated review.

The isolated review found a process-group cleanup gap after a wrapper exited:
a descendant could remain running. Completion now cleans the group as well as
signal/cancellation paths; both successful and failed wrapper exits have a
regression test. Final focused CLI/platform launch and artifact checks passed
with `-race`, and the complete platform race suite passed after accepting valid
empty/comment-only TOML. See the [Task 2 review record](.reviews/task-2-code-review-2026-10-12.md).


## Task 3: main-session registration and coexistence

`SetupCodex` now installs exactly one owned SessionStart and PreToolUse handler.
Startup matches every source, including `fork`; the anchored pre-tool matcher
covers canonical Bash and the ten registered capy tools under alternate MCP
server aliases. No owned handler sets `additionalContextLimit`. The installed
MCP tools/list probe also checks every real registered tool against the installed
matcher. Task 2's launch fixtures now use actual setup-produced hook definitions
instead of adding synthetic bindings after setup.

The merge validates decoded structure and canonical ownership before writing.
It preserves unrelated groups, mixed-group siblings, top-level/group metadata,
large numeric metadata, inline hooks and external scripts; owned duplicates are
collapsed. An emptied group carrying unknown metadata remains as an empty
no-op group so that metadata is not discarded. Mismatched IDs, unsupported
versions, invalid/missing handler types and malformed JSON remain untouched.
Hooks-file metadata is validated independently of TOML's MCP-server schema.
Both JSON and TOML mergers reject results exceeding the launch inspector's
1 MiB read limit before writing.

The mixed inline-TOML fixture found that the pinned parser panics when a populated
map containing nested array tables is reused for decoding. MCP merge validation
now uses a fresh map. This is covered by setup with inline hooks and an explicitly
disabled hooks feature; both remain intact. No dependency was upgraded.

Setup describes registration separately from activation: project-layer trust
and review/trust of exact definitions in `/hooks` are both required. It retains
platform defaults, explicit feature settings, and trust records. The wording and
configuration shape were checked against the
[official hook reference](https://learn.chatgpt.com/docs/hooks); live host delivery
and trust verification remain Task 6.

### Measured mixed startup fixture

**2026-10-16 update:** The toolbox sync in `7d443c8` removed the duplicate
injection when capy's SessionStart hook is registered. The original measurements
below are historical. `TestCodexMixedStartupContext` now checks the toolbox's
delegation notice, preserved provider/extra instructions and 10,000-byte context
limit, and absence of static routing text. It still checks capy's own guidance
and 4,096-byte ceiling on startup, resume and fork. Removing only capy's
SessionStart registration restores full static routing on startup/resume even
while its other events remain registered.

The updated race-enabled fixture passes. Current decoded payloads are **536
bytes** from toolbox and **3,532 bytes** from capy (**4,068 combined**) on
startup/resume; fork emits only capy's **3,532 bytes**. The generated static file
is **11,075 bytes**. These remain subprocess measurements, not live-host evidence.
`go vet -tags fts5 ./...` passes, and independent code review reported no
findings. The second external reviewer was unavailable because its provider
rejected the configured API key.

The full race run also reproduced seven vault CLI failures from the personal
192 KiB import minimum already recorded in ADR-036. All seven pass when rerun
with `XDG_CONFIG_HOME` pointing to an empty temporary directory. This separate
fixture-isolation issue is deferred from the startup assertion fix: isolate
configuration in `setupCodexVaultEnv` and the standalone restore/merge fixtures
so local import policy cannot exclude their synthetic transcripts.
The same run hit the default ten-minute package timeout in `TestCodexCanary`
while scanning the real local Codex corpus through `ScanTranscript` and
`sanitize.StripSecrets`. CI has no such corpus and skips this canary. Canary
runtime tuning is outside this assertion fix; investigate corpus size and
sanitization cost separately if the local canary needs to fit that timeout.
The vault package passes on rerun with `-tags fts5 -race -count=1
-skip '^TestCodexCanary'`; all other packages passed in the full run except
the seven CLI fixtures whose isolated rerun is recorded above.

`TestCodexMixedStartupContext` runs the actual committed toolbox startup script
and the installed capy hook in an isolated Git project. It includes a synthetic
`.claude/CLAUDE.extra.md`, verifies provider/extra instructions remain in output,
and measures decoded UTF-8 `additionalContext` bytes. These are emitted subprocess
payloads, not host-observed model context or proof of no spilling.

| Content | Startup/resume bytes | Fork bytes |
|---|---:|---:|
| Generated `.capy/AGENTS.md` file | 9,500 | Not read by toolbox |
| Capy content injected by toolbox (trailing newline trimmed by shell) | 9,499 | 0 |
| Complete toolbox payload, including provider/fixture extras | 9,889 | 0 |
| Owned capy startup payload | 3,304 | 3,304 |
| Combined emitted payloads | 13,193 | 3,304 |

The capy-related portion on startup/resume is 12,803 bytes (9,499 + 3,304),
compared with the design-stage 12,740-byte projection that used the older Claude
runtime block and untrimmed file size. Capy's own payload remains within 4,096
bytes. The toolbox handler retains its original matcher and context-limit field.
This deliberate duplication remains until the
[claude-toolbox follow-up](implementation.md#claude-toolbox-enhancement-pr).
No upstream issue/PR exists yet, and this task neither submits that change nor
claims the duplicate content has been removed.

### Checks

- Full platform tests, including `TestCodexHookMatchers`,
  `TestCodexHookContextConfiguration`, `TestMergeCodexHooks`,
  `TestSetupCodexHooks` and all three generated-artifact guards: passed.
- CLI Codex-only/default-both setup and mixed startup/resume/fork subprocess
  fixtures: passed, using isolated configuration and no database credentials.
- Platform/adapter/hook race checks: passed.
- `go vet -tags fts5 ./cmd/capy ./internal/platform`: passed.
- Whitespace validation uses
  `git -c core.whitespace=blank-at-eol,blank-at-eof,space-before-tab diff --check`:
  passed. The local `indent-with-non-tab` Git setting flags the generator's
  existing two-space JSON convention; no global/repository setting was changed.

Child lifecycle, platform doctor changes and live host acceptance remain pending.
No search/indexing/chunking/executor implementation changed; benchmarks are not
required for this slice.

The focused Codex CLI/setup/launch suite passed with `-race` (13.948s). The final
missing-type, owner-binding, and actual MCP-tool matcher checks also passed with
`-race`. Both independent code reviewers approved without findings; see the
[Task 3 review record](.reviews/task-3-code-review-2026-10-12.md). PAL was unavailable
because of its rejected configured key, so a second isolated reviewer completed
that side of the review. No trust activation or external issue/PR was performed.

Task 3's full `go test -tags fts5 -count=1 ./...` run also completed with the
isolated configuration and synthetic credentials. Every package passed except
the same two baseline mapped-IPv4 `TestFetchStatsRejectedURLs` assertions. The
changed platform/CLI behavior passed its suites and focused final reruns; no
additional failure appeared. The full suite is therefore still qualified by
that existing server-fixture issue, rather than reported as fully green.

Tasks 2 and 3 are complete as separate commits. The implementation stayed within
the plan; additional care was needed for process-group cleanup, empty TOML,
reused TOML array-table maps and preserving numeric/duplicate-key semantics.
Those cases now have regression coverage. Tasks 4–6 remain pending.

## Task 4: Child guidance and observation lifecycle

The generated catalog now installs all five planned events. SubagentStart emits
its own event name with the compact Codex routing context. PostToolUse and
SessionEnd reuse the existing observation/cleanup handlers with the adapter's
namespaced parent session and unmodified child identity. The observation
algorithm, expiry, caps and permanent lock are unchanged. Claude retains its
Agent/Task prompt injection; Codex launch arguments and types remain untouched.
Both generated routing surfaces and the committed hooks were synchronized,
preserving the external toolbox handlers and their context limits.

`TestCodexChildObservationLifecycle` replays synthetic host payloads through
setup's generated command, bootstrap, owner inspector, wrapper and real CLI from
an ordinary project subdirectory. With database credentials empty, it verifies
child guidance, fetch-only advisory fallback, one execute redirect consuming
both alternatives, failed-retry fallback, sibling/parent isolation and root
cleanup. A Claude session with the same raw parent ID survives, as does the
lock inode. No project knowledge or vault database is created. This is installed
command evidence, not proof of live host event delivery; Task 6 retains that
acceptance obligation.

`TestCodexObservationPayloads` covers successful, failed and serialized MCP
results, null/different child transcript paths, root/missing/type-only identity,
interruption and excluded tools. `TestCodexNoAgentArgumentRewrite` checks native
and namespaced launch names. Matcher, repeat-setup and all generated-artifact
guards cover the complete catalog.

The updated mixed-startup fixture emits 10,054 bytes from the static routing
file, 10,443 bytes from the retained toolbox payload (including its provider and
extra instructions), and 3,358 bytes from capy's own payload. Startup/resume
therefore totals 13,801 bytes in this fixture; fork emits only capy's 3,358.
SubagentStart uses the same 3,358-byte context, below 4,096. These are synthetic
payload measurements; host spilling and trust activation remain Task 6 checks.
The [toolbox follow-up](implementation.md#claude-toolbox-enhancement-pr) remains
pending; no upstream issue/PR was opened.

Verification uses `GOCACHE=/tmp/capy-go-build`, an empty temporary
`XDG_CONFIG_HOME`, and synthetic `CAPY_DB_KEY`/`CAPY_VAULT_KEY` for Go suites;
hook subprocess fixtures explicitly clear the keys:

- Adapter, hook and platform FTS5 suites, including generated artifacts: passed.
- `go test -tags fts5 -race -count=1 ./internal/adapter ./internal/hook ./internal/platform`: passed.
- Focused CLI lifecycle, dispatch and mixed-startup fixtures with `-race`: passed.
- `go vet -tags fts5 ./internal/adapter ./internal/hook ./internal/platform ./cmd/capy`: passed.

No search, indexing, chunking or executor implementation changed, so quality
benchmarks are not required. The existing state and formatter seams kept the
implementation small; most additions are acceptance fixtures. No new project
convention required knowledge indexing (capy MCP is unavailable in this session).

The complete affected-package run (adapter, hook, platform and CLI) passed; the
CLI suite took 316.7 seconds. The broad `go test -tags fts5 -count=1 ./...` run
passed all packages except config and server, whose Unix/TCP socket fixtures
were blocked by the sandbox. Config passed immediately when rerun with socket
access. The server rerun is tracked separately below; no fully green repository
suite is claimed. The [independent review](.reviews/task-4-code-review-2026-10-12.md)
approved without findings; PAL was unavailable because its configured API key
was rejected.

The Task 4 server rerun with socket access completed: only the two already
documented mapped-IPv4 assertions in `TestFetchStatsRejectedURLs` failed.
Config and all other packages passed. Neither socket failure was a code
regression; both fixtures require socket access denied by the sandbox.

## Task 5: Shared platform registration diagnostics

`CheckPlatformRegistrations` now supplies both CLI and MCP doctor reports.
Claude and Codex registration are labelled independently. Codex-only setups do
not produce a missing-Claude failure. Empty platform directories and valid
unrelated configuration do not count as installations; no registration yields
an actionable setup failure. Old unbound Codex MCP entries, orphaned wrappers
and marker-only installations are reported as incomplete.

Codex checks reuse `InspectCodexOwners` and the decoded invocation recognizers,
including adjacent-marker validation, unsupported invocation shapes and the
complete ancestor scan. Malformed or unreadable configuration is reported as
uncertainty that blocks launch, rather than assumed to contain no binding.
Identical copied owner IDs still identify two physical installations. Tests
retain only the outer hooks or MCP binding while deleting its marker and
assert that doctor, runtime and setup all reject the same broken outer owner.

The hook check derives required events and matchers from the installed catalog,
reports missing/partial and duplicate handlers, and validates canonical matcher
shape. MCP checks validate owner-bound bootstrap argv and duplicate aliased
servers. Both report missing, symlink or unreadable wrappers. An ordinary
subdirectory resolves the same owner as the runtime launcher.

Registration success does not imply activation. A separate warning states that
project trust, exact hook trust and execution are unverified. It reports visible
`features.hooks = false`, the deprecated `features.codex_hooks = false` alias,
and potential overlap with inline handlers for the installed events. It does not
infer effective flag precedence or inspect host trust records, global/plugin
configuration or execution receipts. Context7 did not cover the pinned 0.162.1
version; the hook feature and alias were checked against the
[pinned feature table](https://github.com/openai/codex/blob/rust-v0.162.1/codex-rs/features/src/lib.rs)
and [official hook reference](https://learn.chatgpt.com/docs/hooks#turn-hooks-off).

`TestDoctorCodexPlatformParity` initializes a real MCP server and compares its
doctor platform findings with the actual CLI for complete, partial, visibly
disabled and missing-marker fixtures. It uses direct `serve` for the broken
registration fixture, so the diagnostic can run without claiming the broken
generated launch command succeeds. Credentials are synthetic, homes/config/data
are isolated, and vault remains disabled. Existing credential/store/vault
diagnostics retain their own checks and do not become registration checks.

No CLI flags, input formats, exit codes, command dispatch, credential precedence,
completion or distribution behavior changed. Existing CLI conventions are
inherited from `cmd/capy/doctor.go`; the user-visible change is the platform
checklist entries. CI/publishing changes are N/A for this task. Live host proof
and release guidance remain Task 6.

Author review caught one adjacent diagnostic defect: Claude event counting used
only the substring `hook <event>`, which could misclassify an unrelated tool's
hook as a capy installation and cause a spurious missing-Claude-MCP failure.
The doctor-only recognizer now accepts the generated wrapper and legacy direct
capy command forms (including quoted executable paths). It rejects unrelated
commands and echoed capy mentions. Setup's Claude merger is unchanged. The
Codex-only fixture includes an unrelated Claude hook to guard this distinction.

Verification, using FTS5, isolated configuration and synthetic keys:

- Full platform suite and final doctor fixtures: passed.
- CLI/MCP parity subprocess fixture: passed.
- Adapter/hook/platform race suites: passed.
- Existing MCP doctor tests with the race detector: passed.
- `go vet -tags fts5 ./internal/platform ./internal/server ./cmd/capy`: passed.

The implementation followed the existing diagnostic result type and formatter;
no policy evaluator, state schema or database path was added. No search,
indexing, chunking or executor code changed; quality benchmarks are N/A.

The full `go test -tags fts5 -count=1 ./...` run with socket access passed every
package except the same two baseline mapped-IPv4 assertions in
`TestFetchStatsRejectedURLs`. No new failure appeared. After the final Claude
recognizer correction, the full platform race suite, all CLI doctor/parity race
fixtures and vet passed again. Generated-artifact checks are included in the
platform suite. The [independent review](.reviews/task-5-code-review-2026-10-12.md)
approved the refreshed patch without outstanding findings.

Tasks 4 and 5 are complete as separate commits. Task 6 remains pending, including
live host acceptance, release documentation and the whole-feature spec review.
No trust records, external hooks, upstream issues or PRs were changed. The
diagnostic false-positive fix stayed within Task 5's platform-detection
requirement; no other plan deviation or new project convention was introduced.

## Task 6: installed runtime and release documentation

### Live acceptance

On 2026-10-12, launched **Codex CLI 0.162.1 on Linux** using the normal TUI,
`--no-daemon --no-alt-screen --sandbox workspace-write --ask-for-approval on-request`,
from `/tmp/capy-task6/live project/subdir`. The Git root was
`/tmp/capy-task6/live project`. Capy was built with `CGO_ENABLED=1 go build -tags fts5`
from `e1a453e`; the restored build includes this task's help-text changes.
`capy setup --platform codex --project-dir <root>` installed the real bootstrap,
owner marker, wrapper, MCP entry and five hook definitions.

The folder-access prompt identified the parent Git root when launched from its
subdirectory. After project trust, a separate review prompt listed five hooks.
They were reviewed/trusted through the normal hook browser, which then showed
active handlers. A fresh conversation established trusted startup delivery.
No trust files were hand-edited or bypass flags used; setup did not change trust.

The live host retained its existing authentication/trust home, used an empty
temporary `XDG_CONFIG_HOME`, synthetic `CAPY_DB_KEY`, and **unset `CAPY_VAULT_KEY`**.
No production capy database/vault credentials were used. Synthetic deny/ask
rules lived in the disposable `.claude/settings.json`. Unlike the hermetic
subprocess tests, this interactive run retained the host user's global Claude
settings location; it was not a fully isolated policy home. Probe commands did
not read private files, and only fixture markers were executed.

| Actual host observation | Evidence |
|---|---|
| Startup guidance | Session `01a1316d-d57f-7f93-89f3-bbb1bf4f8c6a` contains a complete 3,358-byte capy developer-context block inline; no spill preview |
| Allowed native shell | Code-mode `tools.exec_command({cmd:"printf CAPY_ALLOWED"})` printed the marker and exited 0 |
| Synthetic deny | `printf CAPY_DENIED` was blocked; reason names `Bash(printf *CAPY_DENIED*)` and the fixture settings path |
| Synthetic matched ask | `printf CAPY_ASK` was blocked; reason names `Bash(printf *CAPY_ASK*)`, the source path, and the unsupported hook-initiated approval requirement |
| Initialized MCP from subdirectory | Host discovered all ten capy tools and completed `capy_stats` through the generated installation |
| Child guidance/evidence | Child `01a1316f-ba3a-7bf3-9925-c6cb40a3d802`, path `/root/capy_child_acceptance`, received an additional inline 3,358-byte block and completed `capy_execute` with `printf CAPY_CHILD_EXEC`; normal one-call MCP approval was retained |
| Parent/child association | Child rollout metadata identifies the parent above; host event records identify the child thread and usage records retain the parent session ID |
| One redirect, then fallback | Within 60 seconds of execute success, `python3 -c 'if False: requests.get("https://example.invalid")'` received a capy execute redirect. One identical retry received unverified-tool advisory context and exited 0. The dead branch made no HTTP request. Observation entries were empty after consumption |
| Mixed external hooks | A second trusted run retained the committed toolbox startup script and an independent synthetic PreToolUse restriction. `CAPY_MIXED_ALLOWED` printed; `CAPY_EXTERNAL_DENIED` was blocked with `Independent fixture restriction retained`; capy's deny still blocked |
| Actual duplicated startup context | Mixed session `01a13173-f8d7-7352-8c63-a9292319d02d` has separate inline messages of **10,443 bytes** from toolbox and **3,358 bytes** from capy: **13,801 total**. Provider text and `Preserve extra instructions.` survived; neither block was replaced by a spill preview |
| Infrastructure failure | Replaced only the disposable PATH launcher with a fixture returning 127. The host blocked `printf CAPY_INFRASTRUCTURE_BLOCKED` with bootstrap repair text. This simulates an unavailable launcher; actual missing-file discovery retains subprocess coverage |
| External-terminal repair | Restored the disposable binary and reran setup without changing trust or bypassing policy; `printf CAPY_REPAIRED` then printed its marker and exited 0 |
| Resume | Stopped and resumed mixed session `01a13173-f8d7-7352-8c63-a9292319d02d`. Newly appended context at `18:57:25Z` again contains 10,443 toolbox bytes plus 3,358 capy bytes, both inline |

Session IDs identify local rollouts under `~/.codex/sessions/2026/10/12/`.
Selected results and context measurements are retained in
[live-acceptance.json](live-acceptance.json), excluding unrelated instructions,
authentication, encrypted reasoning and account metadata. Agent reports were
cross-checked against tool-result/context records. Manual stdin replay is not
counted as host delivery.

SessionEnd deletion/isolation, independent TTLs, contention, failed retries,
missing identities, signal cleanup, missing/unreadable/vanished binaries and
wrappers, overlap, moves and worktrees retain Tasks 1–5's installed CLI/unit
evidence. Live observation consumption was exercised; live SessionEnd cleanup
with outstanding evidence was not. Non-Git host discovery, other Codex surfaces,
other versions and Windows remain unverified. Hosted web interception and output
budgets remain outside scope. The [toolbox follow-up](implementation.md#claude-toolbox-enhancement-pr)
was subsequently filed as [claude-toolbox #170](https://github.com/serpro69/claude-toolbox/issues/170)
on 2026-10-14 at the user's request; cleanup remains pending and #132 is not
closed by this work.

### Release documentation and test correction

Updated README setup, activation, coverage, policy, recovery and rollback;
architecture adapter/owner/diagnostic mechanisms; CLI help; AGENTS marker,
generator and wrapper invariants; and ADR-024 with a dated historical clarification.
Rollback retains the marker while bindings reference it and restores compatible
MCP/wrapper configuration before binary downgrade. Stop/reload sessions before
moving/removing loaded artifacts. Infrastructure repair via an external terminal
or user-controlled `/hooks` is distinguished from bypassing policy denials.
No generator-owned artifact changed in Task 6.

A copy of the mixed installation also verified the documented partial rollback:
exactly five known owned handlers were removed while external handlers and group
metadata remained; MCP binding and marker were unchanged. No live definition
was removed while its session remained running. Binary downgrade is documented
but was not exercised against an older release.

The initial full suite reproduced only the known mapped-IPv4 fetch-stat failure.
Inspection confirmed that valid mapped IPv4 URL syntax is admitted and embedded
addresses are classified by the SSRF transport. The fixture now checks
`https://[::ffff:192.0.2.1]/` only in offline syntax coverage, and uses mapped
loopback for actual single/batch transport rejection. No production admission,
SSRF protection or cache accounting changed. Focused syntax/rejection/IP-policy
race checks passed; the earlier fixture follow-up is resolved.

Validation uses synthetic keys, `GOCACHE=/tmp/capy-go-build`, an empty
`XDG_CONFIG_HOME`, and local socket access for the full suite. Hook subprocess
fixtures separately clear both database keys.

| Check | Result |
|---|---|
| Full `go test -tags fts5 -count=1 ./...` after fixture correction | Passed: every package (CLI 313.288s, server 172.513s, vault 318.217s) |
| `go test -tags fts5 -race -count=1 ./internal/adapter ./internal/hook ./internal/platform` | Passed |
| Focused server syntax/rejection/IP-classification tests with `-race` | Passed |
| Generated equality, merged-artifact idempotency and completeness guards | Passed |
| `go vet -tags fts5 ./cmd/capy ./internal/platform ./internal/hook ./internal/adapter ./internal/server` | Passed |
| Built CLI `setup --help` and `hook --help` | Passed |
| Documentation links, evidence JSON/context bounds, diff whitespace | Passed after repairing four pre-existing README/architecture links to the completed upstream-sync directory |
| Quality benchmarks | N/A: no search/indexing/chunking/executor implementation changed |

CLI flags, dispatch, exit contracts, configuration precedence, completions and
distribution remain unchanged; this task updates help text only. CI/publishing
changes are N/A. The fixture correction is the only code-adjacent addition,
needed to resolve the known suite failure. Live checks confirm the planned
integration without adding runtime instrumentation or a new protocol.

All disposable live Codex conversations were stopped normally after verification.
The feature remains in its existing documentation directory for stable links;
Task 6 completion does not publish a release or mutate upstream issue/PR tracking.

### Reviews and plan adjustment

The isolated [code review](.reviews/task-6-code-review-2026-10-12.md) approved the
documentation/help and fixture changes without findings. PAL's external review
attempt failed at its expert-analysis step because its configured Gemini key
was rejected; no external-model approval is claimed.

The spec reviewer identified one P2 acceptance-plan deviation: the live run kept
the host's authentication/trust home instead of an isolated policy home. The
[acceptance procedure](implementation.md#52-live-acceptance-and-release-checks)
now explicitly permits that arrangement with synthetic project rules/keys,
isolated capy config, recorded global-policy inheritance, exact rule/source
checks and a passing control; hermetic subprocess coverage is retained. This
test-environment adjustment preserves the normal trust interface and obeys the
execution environment's constraint against repurposing HOME/CODEX_HOME. It does
not change production policy behavior. The historical finding and author
context remain in the spec review record; no user-confirmed deviation indexing
is claimed. Capy knowledge tools were unavailable, and the relevant decisions
are already documented here.

The [whole-feature spec review](.reviews/task-6-spec-review-2026-10-12.md) concluded
**CONFORMANT** after that explicit revision, with no outstanding findings. All
six tasks are complete; deferred follow-ups retain their separate scope.
