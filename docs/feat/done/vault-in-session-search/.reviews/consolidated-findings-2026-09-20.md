# Consolidated design findings — verified and addressed in the plan

> Date: 2026-09-20
> Reviewed documents: commit 3ffcd81
> Production baseline: 0ed4a94 (no feature implementation)
> Current scope: [Design](../design.md), [Implementation](../implementation.md), [Tasks](../tasks.md)
> Status: Six unique claims verified; design corrections applied; implementation and re-review pending

## Method and boundaries

The supplied three review rounds were deduplicated before editing. Every distinct claim was checked against freshly read documents and relevant production/dependency source. Runtime claims were independently reproduced using the repository's pinned module with downloads disabled, on Go 1.26.4 linux/amd64.

All verification preceded the design corrections. The two existing review reports are preserved unchanged: [root report](../review-design-2026-09-20.md) and [hidden-directory report](review-design-2026-09-20.md). Their “open” status describes the original review; this report records the subsequent design disposition. The first review's four findings were supplied in the conversation and are fully accounted for below.

## Consolidated verdicts

| ID | Origin | Severity | Verified verdict | Necessary correction |
| --- | --- | --- | --- | --- |
| C1 | Review 2.1 + Review 3.1; F1 / RD-1 in saved reports | P1 | Valid, independently reproduced | Stop using the pinned matcher for this feature; preserve original NUL-containing lines and byte positions |
| C2 | Review 2.2 + Review 3.2; F2 / RD-2 | P1 | Valid, independently reproduced | Bound scoring before arithmetic and generate positions independently of scores for all 1–256-rune queries |
| C3 | Review 1 performance gate | P2 | Valid early-evidence gap; “no degraded fallback” is not itself a defect | Mandatory Task 1 gate, then fuzzy integration gate before final verification |
| C4 | Review 1 frame sequencing | P2 | Valid sequencing ambiguity; dual state was possible, not inevitable | Complete/migrate frame ownership before hidden-detail search consumes it |
| C5 | Review 1 no-color indication | P3 | Valid missing visual contract | Specify actual ASCII markers and width/mapping behavior |
| C6 | Review 1 uniform M sizing | P3 | Valid scope/risk concern; M is not objectively an invalid size tag | Separate local-frame migration from root/raw/action integration |

## C1 — NUL safety

Original design searchable fields were the unsanitized original parsed Body values, while escaping occurred only in presentation. Original implementation Task 4 called sahilm/fuzzy's unsorted matcher directly. Current go.mod pins v0.1.1 indirectly.

Source tracing also confirms reachability: [splitUserContent](../../../../../internal/vault/claude_decoder.go) decodes the JSON string and calls [cleanText](../../../../../internal/vault/scanner.go), which removes tags and trims whitespace rather than dropping embedded NUL. The resulting EntryHuman.Text is assigned to [TranscriptMessage.Body](../../../../../internal/vault/transcript.go). This is a code-path check; the runtime probe below exercises JSON decoding and the matcher directly.

The locally installed fuzzy.go indexes the current query rune inside its candidate loop and commits a match on a zero-valued lookahead. An actual NUL and end-of-input share that sentinel. The probe decoded valid JSON containing a\u0000b before calling FindNoSort, so the input is reachable without malformed JSON or a special query.

Observed results:

| Query / candidate | Result |
| --- | --- |
| a / decoded a-NUL-b | Panic: index out of range [1] with length 1 |
| b / decoded a-NUL-b | Valid offset 2 |
| ab / decoded a-NUL-b | Valid offsets 0, 2 |
| ab / NUL-ab | Valid offsets 1, 2 |
| ab / ab-NUL | Panic: index out of range [2] with length 2 |

This is an input-dependent panic, not a claim that every NUL-bearing candidate fails. Display escaping, custom sorting and context cancellation cannot prevent the direct-call fault. Panic recovery alone would turn searchable text into lost results.

**Addressed in the plan:** one local, length-terminated subsequence matcher for all inputs; NUL remains ordinary source text. Tasks require before/after/across-NUL cases, original spans, and worker completion/follow-up-query tests. No line is dropped or searched through generated escape spelling.

## C2 — score overflow and corrupt positions

The pinned source's adjacentCharBonus doubles an accumulated bonus, which is added back to the accumulator. It uses Go int. Overflow affects both total score and the comparison that selects candidate positions; repairing only the final comparator or widening only the output score does not repair the selection algorithm.

Reproduced with identical repeated-a query/candidate pairs:

| Length | Score | Non-increasing/invalid positions |
| --- | --- | --- |
| 32 | 1544183490709875 | 0 |
| 39 | 3377129294182480230 | 0 |
| 40 | -8315356191162110941 | 0 |
| 43 | -1051229425620792066 | 1; byte 41 repeated, byte 42 omitted |
| 64 | 8492147925252739361 | 10 |
| 128 | -8030005217970444752 | 31 |
| 256 | -6653838781291564727 | 76 |

Thus ranking corruption precedes position corruption; a score is not safe merely because a particular example still has valid positions. Exact overflow boundaries depend on machine integer width/input; these measured values describe the reported amd64 environment.

**Addressed in the plan:** [local matcher contract](../design.md#fuzzy-matcher-contract) separates leftmost source alignment from bounded additive scoring, caps penalties as they accumulate, and uses int64 arithmetic within a proven fixed range. Tests require all supported lengths, mixed/repeated patterns, original increasing spans and complete highlighting. No fuzzy dependency upgrade/import is planned.

The alignment is deliberately leftmost rather than score-optimal within each candidate. That ranking trade-off is explicit; correctness and the query limit are retained.

## C3 — early performance evidence

Original implementation.md Task 1 ended with the optional “if useful” reference-corpus check. Original Task 5 introduced the actual 10,000-line gate after detail, scope and fuzzy integration. Design.md acknowledged that target was unmeasured. Those facts support the timing-risk claim.

The review's suggestion that the workload is “very achievable” is not performance evidence, and no current measurement of the unimplemented feature validates it. The stronger assertion that a degraded mode is required is not accepted: result caps, dropped matches or approximate jumps would violate the user's explicit correctness contract. Optimizing or returning for a concrete agreed revision is an appropriate failure response.

**Addressed in the plan:** Task 1 now builds the real full-field corpus and must pass measured projection/exact operations before Task 2. Task 5 gates integrated fuzzy operations; Task 6 repeats complete acceptance. Measurements name which interactions exist at each stage, so an early pass cannot falsely certify hidden navigation or fuzzy UI.

## C4 — frame contract before consumers

Original implementation Task 2 created a search-owner frame and typed tool target; original Task 3 replaced the legacy detail state and migrated all call sites. Current viewer.go still has inSub, subID, inInline, inlineLabel and savedMainLine as mutable state, and its open/return helpers update them directly. app.go reads those details for raw selection; root viewerStack stores complete viewer models.

That establishes a real ambiguity about interim ownership. It does not prove that an implementer must introduce competing authorities: a complete migration could have been chosen within Task 2. The original plan simply did not require it.

**Addressed in the plan:** new Task 2 defines and migrates the complete local frame contract, including existing consumer adapters, before new Task 3 adds search-selected detail targets. Derived getters are permitted; independently mutable shadow flags are forbidden. New Task 4 integrates lifecycle behavior without reshaping the frame.

## C5 — non-color selected occurrence

The original requirement named a “non-color indication” and tests but no actual symbol/geometry. styles.go provides color and attributes; render.go's existing marker-row implementation explicitly uses different glyphs to preserve selection without styling. Neither specifies which search occurrence to mark, particularly when two hits occupy one row.

**Addressed in the plan:** selected exact spans receive visible ASCII brackets, including each visible segment of a wrapped match. Two cells are reserved before wrapping; generated brackets are not searchable or copied. Fuzzy picker selection has a literal > prefix. ANSI-stripped tests assert these exact outputs, including multiple hits on one row. Optional styling is no longer the only indication.

## C6 — separate local migration from root integration

The original Task 3 grouped local state migration, child-stack suspension, raw provenance, rename/status behavior and test migration. The five primary-file count alone was compatible with an M tag, so it would be inaccurate to call the metadata a hard format violation or mechanically relabel it L.

The combined risk and dependency on earlier search-detail work justified a split. New Task 2 provides the manual nested-return path and complete local authority; Task 4 provides root suspension/actions using that stable contract. Both are bounded user-facing slices with separate checks. The task graph now has six tasks; all task references were updated together.

## Reproducible evidence

The probes are standalone review artifacts, excluded from normal Go package discovery by the .reviews directory. They do not implement the feature and do not access a vault.

From the repository root:

~~~sh
GOPROXY=off GOSUMDB=off go run -mod=readonly -tags fts5 docs/feat/wip/vault-in-session-search/.reviews/probes/pinned/main.go
GOPROXY=off GOSUMDB=off go run -mod=readonly -tags fts5 docs/feat/wip/vault-in-session-search/.reviews/probes/proposed/main.go
~~~

- [Pinned probe source](probes/pinned/main.go) and [captured output](probes/pinned/output.txt): recover is used only to report subsequent cases, not proposed as a fix.
- [Local-contract prototype](probes/proposed/main.go) and [captured output](probes/proposed/output.txt): checks 363,909 exhaustive pairs, repeated ASCII/Unicode queries at every length 1–256, NUL placement, score bounds, source ranges, fold cases, and pre-cancelled execution against an independent existence oracle. Rank examples produce 88, 86, 76 as specified.

The prototype validates the proposed core algorithm, not rendering, worker integration, mid-line cancellation latency, ranking quality on real sessions, or the 100 ms end-to-end gate. Those remain mandatory implementation tests/measurements. No production suite or feature benchmark was claimed for this documentation correction.

## Remaining gates

All six findings have a concrete design correction; no accepted finding is silently deferred. Their production implementations and tests are the pending tasks linked above. Re-review the revised plan, then implement and measure it. An unsuccessful runtime check must not be marked fixed solely because this report says its design disposition is addressed.
