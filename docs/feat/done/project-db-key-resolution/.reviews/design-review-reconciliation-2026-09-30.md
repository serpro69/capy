# Design review corroboration and corrections

Original report: [design-review-2026-09-30.md](design-review-2026-09-30.md). Corroborated against committed design `74c270d` and the unchanged production code from `d9e09c1`/v0.16.4. The original report is preserved. This record assesses its nine findings independently and explains which recommendations were adopted.

Result: **seven valid findings and two partly valid planning findings**. All accepted corrections are reflected in [design.md](../design.md), [implementation.md](../implementation.md), and [tasks.md](../tasks.md). No feature implementation task has been marked done. Candidate regression tests described below remain implementation work; the old-binary probe and document checks are the evidence actually executed in this reconciliation.

## R1 Vault migration

**Verdict: valid, with narrower triggering conditions than the report's summary. Fixed in the design.**

The [wrapper](../../../../../internal/platform/setup.go) sources the whole main-worktree dotenv only when argument one is `serve` and the inherited knowledge key is empty. Under that condition it can populate the vault key too. [getVault](../../../../../internal/server/server.go) and [RequireVaultKey](../../../../../internal/vault/encryption.go) disable the vault when that environment key is absent. Removing sourcing therefore breaks vault access for users relying on this fallback. It does not remove an already-inherited vault key, and the original wrong-nonempty-knowledge-key scenario did not execute the fallback in the first place.

The report's blanket “silently” wording also needs qualification. [MCP vault search](../../../../../internal/server/tool_vault_search.go) explicitly reports a disabled vault, and [federated search](../../../../../internal/server/tool_search.go) adds guidance when a session-scoped request has no hits. Mixed results with knowledge hits can omit that advice; doctors already report disabled status but do not explain the migration.

Correction: retain the user-approved environment-only vault scope, explicitly document the breaking migration, and require a shared disabled-vault message explaining that wrappers no longer source `.env`. The actual launcher/daemon environment must supply the key; a new terminal export alone does not update an existing daemon. New Task 7 owns the migration message and a worktree/no-inherited-keys test followed by a relaunch with the vault key supplied. This adopts recommendation (a), without introducing the separate vault resolver proposed in (b).

## R2 Empty-key side effects

**Verdict: valid implementation gap. Fixed in the design.**

[ContentStore.getDB](../../../../../internal/store/store.go) creates the directory and writes `.project` before calling `openDB`, where the current empty-key check lives. The previous general instruction to validate before writes did not bind the constructor/fail-on-use path to that location. Direct store/server callers bypass the command resolver.

Correction: Task 2 now requires the captured-key check at the beginning of `getDB` under its mutex, before filesystem mutation, and separately in `openSingleConn` before SQL opening. Its direct-store and standalone-checkpoint tests assert absence of directory, marker, DB, and sidecars. Task 6 adds the direct-server doctor case when the server's explicit-credential option exists. All cover a nonempty inherited key that must not be used as fallback.

## R3 Whole-file dotenv compatibility

**Verdict: valid migration-communication gap; the suggested prefix-only parser is rejected. Fixed in the design.**

The old wrapper skips dotenv entirely for a nonempty inherited key, while the proposed resolver gives a recognized project declaration precedence and validates the whole file. A valid inherited key therefore cannot rescue a file containing a target declaration and unsupported statements elsewhere. The original design mentioned mixed-file migration but did not foreground this as a breaking change or require an actionable parser error.

Correction: name the break in format, rollout, README/ADR work, and tests. Errors include a safe source/line, fixed reason, and `store.key_file` migration hint. Test unsupported statements before and after the target, and explicit-file bypass of the same files. Do not claim every unrelated interpolated value is necessarily invalid; the defined grammar decides that.

Prefix-only validation would miss a later duplicate target declaration and malformed trailing input, contradicting the approved duplicate/error contract. Whole-file validation is retained, with its migration cost made explicit.

## R4 Rollback path

**Verdict: valid. Fixed in the design; unknown-field behavior was verified.**

[config.loadAndMerge](../../../../../internal/config/loader.go) calls the pinned go-toml v2.2.4 `Unmarshal` with default options. Inspection of that module's `unmarshaler.go` confirms unknown fields are ignored unless strict decoding is explicitly enabled.

Executed black-box probe: installed `capy --version` returned v0.16.4. A disposable project had a synthetic `.capy.toml` declaring `store.path = ".capy/rollback-target.db"` plus `key_file` pointing to a nonexistent file. With both key variables and `CLAUDE_PROJECT_DIR` unset and XDG paths isolated, `capy which --project-dir <fixture>` exited 0 and printed `<fixture>/.capy/rollback-target.db`. This demonstrates that the actual old loader ignores `key_file` and retains the configured target; it does not demonstrate old-binary credential-file support or an end-to-end downgrade.

Correction: stop/close and checkpoint before downgrade; supply the existing knowledge key, and optional vault key, in the real launch environment; downgrade; regenerate wrappers using the old binary; restart and verify reads. No key rotation or DB-format rollback is required. Task 7 adds an old-shaped config-decoding fixture and environment-only launch coverage; actual legacy-binary smoke results must be labeled separately. Old wrappers cannot read a raw key file, and old binaries with new wrappers receive no dotenv loading.

## R5 Regular files and bounded reads

**Verdict: valid contract omission. Fixed in the design.**

The prior format placed no file-type or size bound on input. The installed Go standard library's `os.ReadFile` opens a path and reads until EOF, using file size to size/grow its buffer. That is not a bounded credential-file interface and does not reject a FIFO before opening it. There is no candidate reader yet to accuse of an implemented defect; the missing constraint belongs in the design.

Correction: require a regular-file target before open and recheck the opened descriptor; allow symlinks to regular files; reject pre-existing FIFOs/devices/directories. Read at most limit plus one byte and reject overflow without truncation/fallback. Limits are 4,096 raw bytes for key files (newline included) and 1 MiB for dotenv. The same admission helper covers both paths, avoiding an equivalent unbounded dotenv reader. Tests cover exact/over-limit, regular symlinks, nonregular targets, and prompt failure for a pre-existing FIFO. Environment-only passphrase lengths are unchanged.

## R6 Linked worktree dotenv

**Verdict: valid clarity improvement; no precedence bug. Fixed in the design.**

[DBProjectDir](../../../../../internal/config/paths.go) redirects relative database paths to the main checkout. Thus the previous stated precedence already selects main dotenv directly and skips a duplicate main fallback; it never consults the linked checkout's own file in this mode. The old wrapper also used the Git common directory.

Correction: state that exclusion explicitly, contrast it with absolute/XDG modes that keep the selected worktree as owner, and require conflicting-main/linked-dotenv tests. No ownership or precedence semantics changed.

## R7 Task serialization and graph

**Verdict: partly valid. Corrected the planning opportunity and diagram.**

Original Task 7's dependency on wrapper Task 6 was valid for completing the whole task because its pre-commit subtask needed that wrapper. It was not an invalid dependency or runtime defect. However, independent direct cleanup/checkpoint work could be delivered earlier, and the ASCII joins made Task 9's route harder to interpret.

Correction: new Task 8 contains direct maintenance and depends only on credential Task 4. New Task 7 owns wrapper/pre-commit integration and waits for direct MCP Task 6 and direct maintenance Task 8. The graph lists explicit prerequisite edges without ambiguous joins. All dependency/parallel metadata and implementation sections use the new numbering; a mapping preserves original review references.

## R8 Task sizes and transport harness

**Verdict: partly valid. Split the transport risk; retained and bounded the parser task.**

The report provides no evidence that original Task 4 must be L: it names five bounded files, a fixed single-line grammar, and an existing selection seam. The skill measures complexity as well as file count. Its scope is now explicitly bounded and justified as M; general shell/dotenv functionality remains excluded.

Original Task 5 did combine credential wiring with construction of a real subprocess transport harness, which deserves a separate risk slice. The suggested existing [capy helper](../../../../../cmd/capy/main_test.go) is not such a harness: it runs `go run` with captured output and no stdin. The [server test helpers](../../../../../internal/server/tool_execute_test.go) call handlers directly, bypassing transport.

Correction: new Task 5 builds passing environment-only stdio round trips, deadlines, cleanup, and A/B isolation using current behavior. New Task 6 reuses that fixture for project credentials. This preserves a testable vertical path in each task rather than splitting parser and resolver into horizontal layers. The plan now has eleven tasks, all still pending.

## R9 Rotation assumption

**Verdict: valid categorization issue. Fixed in the design.**

Whether an operator stopped every attached process is an operating precondition, not an implementation test oracle. The implementation cannot infer that action merely because a test passes. The current rotation flow swaps files, so the procedure matters independently of credential selection.

Correction: move stopping processes and updating credential material into an explicit rotation-preconditions section and command-help/README work. Task 10 tests guidance, captured-key behavior, and unchanged credential files; it does not claim to prove operator compliance. No automatic process stopping or live key reload was added.

## Validation and scope

- Production code is unchanged. This task fixes the feature's design/plan/task documents and records evidence; runtime implementation and its regression suite remain pending.
- The original external report and earlier CoVe evidence are preserved, including their historical task numbering and claims. This reconciliation contains the qualifications above rather than silently rewriting those reports.
- Document validation passed for seven Markdown files: local links/anchors, whitespace, eleven pending task records, per-subtask verification, acyclic dependencies, parallel markers, exact graph edges, and all nine dispositions. `git diff --check` passed.
- An independent `code-reviewer` agent reviewed the correction diff and reconciliation with no author history: **APPROVE**, no P0–P3 findings. It explicitly checked all nine dispositions, cross-document consistency, bounded input, migration/rollback, dependencies, and the separate stdio harness. It did not rerun the legacy-binary probe or runtime tests.
- PAL/Gemini 3.1 Pro completed a two-step review and returned no defects or priority fixes. The isolated-review protocol treats zero findings from both PAL steps as no additional finding signal; its response also reported zero embedded files, so this is not counted as an independently file-verified approval. The independent agent's review and the direct checks above remain the supporting evidence.
- A final author consistency correction assigns direct-server empty-key tests to Task 6, where the server option exists; Task 2 retains direct-store/checkpoint tests. This preserves the early-validation requirement without making Task 2 depend on an unimplemented server interface. The independent reviewer checked this final delta separately and approved it with no findings.
- No new systemic P0/P1 findings arose from the correction review, so there are no additional review findings to index. The original review's architectural observations were already present in the knowledge base.
