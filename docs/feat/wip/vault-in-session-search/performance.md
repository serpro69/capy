# In-session search performance evidence

The [Task 5 fuzzy gate](#task-5-fuzzy-gate--2026-10-02) passes on the final reviewed
implementation, with a maximum of **28.876 ms** across both builds. Task 6 still
owns the complete feature verification matrix.

The implemented main-transcript exact-search path passes the early 100 ms gate
in both builds. The final measured maximum was **27.052 ms**. This report covers
the original Task 1 gate; hidden-target navigation, nested frame suspension,
raw/child return and fuzzy selection were not certified by that gate. The
[Task 3 rerun](#task-3-exact-path-rerun--2026-10-02) below records the current
exact sample set separately.

## Reference machine and fixture

Measured 2026-10-01 on an Intel Core i7-11800H (8 cores / 16 threads), Pop!_OS
22.04 LTS, Linux 7.1.1-76070101-generic, Go 1.26.4 linux/amd64, GOMAXPROCS=16.
Build tags: fts5 and fts5,glamour, CGO enabled. No concurrent build or test job
was launched during the measurements. A vmstat sample during the gate showed
93% aggregate CPU idle; the test used roughly one core.

Revision: f99d758 plus the Task 1 working changes. SHA-256 of concatenated
find_text.go, find_render.go, viewer_find.go, viewer.go, app.go, render.go
(in that order, under internal/vault/tui):
4de0d4b179ebcc18dc6e2beaa3cc194dbafa8de2db524007fe5da6f370ef4d1a.

The deterministic generator is in
[find_bench_test.go](../../../../internal/vault/tui/find_bench_test.go).
Seed: 20260920. Fixture digest:
d565dddaa76fbb50f300b5ab7d5c79e3f5b0add1c46fa09d1d8528175b9a0ef9.
It has exactly 10,000 searchable content lines, 1,034,588 field bytes, 200
messages and 100 collapsed tool bodies. Each group contains 79 ordinary lines,
one ToolSummary line and 20 hidden Body lines. Text includes Markdown, diff
prefixes, Unicode, combining marks, repeated needles and long lines. The fixture
test requires every positive query to hit both visible and hidden fields.

Queries, in table order: a; needle08; ab repeated 16 times; Q repeated 256 times;
界 repeated 256 times; NO_SUCH_PASSAGE. Their code-point counts are 1, 8, 32,
256, 256 and 15. Initial terminal size is 100×30; resize targets 80×30.

## Measurement method

Each operation has 100 completed samples. Timings include Model.Update, command
scheduling/delivery, matching, projection preparation when needed, result
application and Model.View. The normal viewer is already loaded and warmed.
Cold slash activation builds its corpus and projection; ordinary edits reuse
immutable scope/width caches. Terminal painting and archive decoding are
excluded. Cosmetic cursor timers run independently of search commands.

The rapid-typing measurement starts at the final keystroke and waits for its
current result. Obsolete work must retire without replacing the displayed query.
Bytes/op is mean TotalAlloc growth across the complete operation. The dedicated
full-corpus rows measure construction and scanning, including every hidden
Body; they are computational evidence, not hidden-target UI measurements.

## Final reference results

All times are milliseconds. Every sample in every recorded operation met
100 ms; the gate is maximum latency, not a percentile substitution.

| Operation | Default median | p95 | Maximum | Bytes/op | Glamour median | p95 | Maximum | Bytes/op |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Cold slash/projection | 14.583 | 16.825 | 21.027 | 6529975 | 14.747 | 17.693 | 25.019 | 6522507 |
| Edit, 1 rune | 9.574 | 14.879 | 16.924 | 17097673 | 8.975 | 12.630 | 16.060 | 17092730 |
| Edit, 8 runes | 4.406 | 5.351 | 8.537 | 684170 | 4.382 | 5.096 | 5.980 | 684934 |
| Edit, 32 runes | 4.108 | 4.818 | 7.292 | 540050 | 4.416 | 5.228 | 6.451 | 542823 |
| Edit, 256 ASCII runes | 2.967 | 3.324 | 3.958 | 521291 | 3.038 | 3.541 | 4.237 | 519202 |
| Edit, 256 Unicode runes | 3.073 | 3.633 | 4.016 | 560348 | 3.119 | 3.555 | 4.024 | 558952 |
| Edit, no match | 4.388 | 4.681 | 5.005 | 505768 | 4.293 | 4.858 | 5.315 | 504336 |
| Enter | 0.253 | 0.432 | 1.044 | 278567 | 0.257 | 0.425 | 0.723 | 279978 |
| Next | 0.262 | 0.420 | 1.040 | 280009 | 0.260 | 0.510 | 0.849 | 280007 |
| Previous | 0.246 | 0.491 | 1.483 | 275247 | 0.247 | 0.302 | 0.803 | 273041 |
| Selected-match resize | 14.376 | 15.102 | 15.617 | 5756630 | 14.617 | 16.837 | 27.052 | 5771425 |
| Rapid typing, final key | 4.437 | 4.793 | 5.589 | 2373515 | 4.452 | 5.342 | 5.859 | 2373300 |
| Full build + scan, 1 rune | 8.295 | 9.633 | 16.560 | 16302198 | 8.362 | 9.542 | 11.009 | 16302141 |
| Full build + scan, 8 runes | 5.299 | 8.704 | 10.324 | 2351105 | 4.768 | 6.673 | 10.165 | 2351105 |
| Full build + scan, 32 runes | 4.646 | 7.453 | 10.016 | 2248706 | 5.071 | 9.025 | 11.907 | 2248705 |
| Full build + scan, 256 ASCII | 3.357 | 6.280 | 8.803 | 2227203 | 3.578 | 7.171 | 7.835 | 2227200 |
| Full build + scan, 256 Unicode | 3.241 | 3.895 | 4.197 | 2225669 | 3.533 | 6.059 | 8.324 | 2225665 |
| Full build + scan, no match | 5.138 | 8.812 | 11.110 | 2224180 | 4.850 | 8.109 | 9.160 | 2224176 |

The earlier pre-review gate also passed (maximum 21.026 ms default and
20.296 ms glamour). The final gate above repeats the protocol after restoration
fixes; small timing variation does not change the result.

## Stress evidence

These are single stress measurements, outside the reference-workload 100 ms
acceptance gate. Retained heap is incremental HeapAlloc after GC, keeping
corpus/results/projection alive; source strings already exist at the baseline.
It measures search structures, not whole-process memory.

| Workload | Bytes / lines / hits | Default build+scan+projection | Retained bytes | Glamour build+scan+projection | Retained bytes |
| --- | --- | ---: | ---: | ---: | ---: |
| 100,000 lines | 1199999 / 100000 / 100000 | 37.686 ms | 19701088 | 44.610 ms | 19701104 |
| One MiB line | 1048576 / 1 / 1 | 17.586 ms | 2919512 | 18.383 ms | 2919512 |
| Long graphemes | 1049152 / 65 / 64 | 18.239 ms | 2244560 | 19.314 ms | 2244560 |

Deterministically cancelling an exact scan after four checkpoints completed in
1.34 / 34.501 / 39.723 microseconds respectively in the default build, and
1.639 / 36.149 / 42.648 microseconds in glamour. These include work up to the
injected cancellation, not an OS scheduling guarantee. Ordinary tests separately
cover within-line cancellation during corpus construction, matching and wrapping,
and stale worker retirement followed by a new query.

Repeated nested open/back and its retained-memory behavior remain unmeasured
until the local frame and root suspension tasks exist. No fuzzy operation is
measured by these exact-search results.

## Reproduction

Use the repository's synthetic keys and CGO, with a writable Go cache if needed:

~~~sh
export CAPY_DB_KEY=test-key-for-development CAPY_VAULT_KEY=test-key CGO_ENABLED=1
CAPY_FIND_BENCH=1 go test -tags fts5 -run '^TestFind(Latency|Stress)$' -count=1 -v ./internal/vault/tui/
CAPY_FIND_BENCH=1 go test -tags fts5,glamour -run '^TestFind(Latency|Stress)$' -count=1 -v ./internal/vault/tui/
go test -tags fts5 -run '^$' -bench '^BenchmarkFind' -benchmem -count=6 ./internal/vault/tui/
go test -tags fts5,glamour -run '^$' -bench '^BenchmarkFind' -benchmem -count=6 ./internal/vault/tui/
~~~

The conventional benchmarks isolate corpus construction, exact scans,
projection wrapping and 28-row highlight rendering. They help locate costs;
they do not replace the measured application path above.

Six repetitions completed per benchmark in each build. Observed ns/op ranges:

| Benchmark | Default | Glamour |
| --- | ---: | ---: |
| Corpus | 473695–506646 | 508374–541823 |
| Exact, 1 rune | 7302458–8049252 | 7341165–7894301 |
| Exact, 8 runes | 3956296–4149128 | 4038312–4177493 |
| Exact, 32 runes | 3707725–4047166 | 3773955–4003979 |
| Exact, 256 ASCII | 2615796–2831726 | 2614173–2663407 |
| Exact, 256 Unicode | 2602459–2774830 | 2635552–2691584 |
| Exact, no match | 4059060–4196249 | 3971943–4111377 |
| Projection | 14502371–14976076 | 13618235–14539233 |
| 28-row highlight | 107938–110211 | 106063–111877 |

Corpus construction allocated about 2.22 MB, projection about 3.75 MB, and
highlight rendering 13,896 bytes/op in both builds. The high-occurrence one-rune
scan allocated about 14.08 MB for its complete result set; no-match scans
allocated zero bytes. No matches are capped to improve these figures.

## Retrieval quality compatibility

make bench-quality passed with BENCH_BRANCH=vault-find-task1. An independent
source archive of f99d758 produced the comparison report with
BENCH_BRANCH=baseline-f99d758. Because that archive has no .git directory,
qualstat labels its revision unknown; the archived commit is explicitly f99d758.
The target ran on master with the Task 1 working changes, not on a named feature
branch. Both reports used dataset
sha256:7d45338724b05181ebc92bd0b74eb7708bdd4fba27a8d6830b54a127f2b6ba2d.

qualstat reported no change in any retrieval-quality or context-reduction
metric. This is the Task 1 compatibility check against its actual starting
revision; Task 6 still owns its separately specified final feature comparison.

## Task 3 exact-path rerun — 2026-10-02

The final exact-search sample set passes in both builds, with a maximum of
**11.569 ms**. This is a rerun on a different machine, not a claim of speedup
against Task 1's Linux measurements.

Machine: Apple M4 Pro, macOS 26.6.2 (25G83), Go 1.25.2 darwin/arm64,
GOMAXPROCS=14. The full-suite, race and vet jobs had finished before these
sequential measurements; no other build/test job was launched during them.
Revision: dirty `feat/session_search` at `81a4fcd`. SHA-256 of concatenated
find_text.go, find_render.go, viewer_find.go, viewer_targets.go and viewer.go,
in that order: `3ebab022edbebdc32eff8e44c446cf125e0f88eb58d4538a1db973411f431b8b`.

The seed, fixture digest, 10,000 lines, 1,034,588 bytes, 200 messages,
100 collapsed bodies, query set and 100×30 terminal are unchanged from the
reference fixture above. Commands use the synthetic keys, CGO and
`GOCACHE=/tmp/capy-go-build`:

~~~sh
CAPY_FIND_BENCH=1 go test -tags fts5 -run '^TestFindLatency$' -count=1 -v ./internal/vault/tui/
CAPY_FIND_BENCH=1 go test -tags fts5,glamour -run '^TestFindLatency$' -count=1 -v ./internal/vault/tui/
~~~

Each row contains 100 completed samples. Times are milliseconds; allocation
is mean bytes per complete operation. The measurement includes result
application and View as before. Unlike Task 1, previous-hit wrap now opens the
last collapsed tool; the harness asserts that target. All corpus hits now
participate in navigation. The full hidden-target transition matrix, nested
suspension, fuzzy operations and retained-memory protocol remain scheduled for
Task 6 and are not certified by this rerun.

| Operation | Default median | p95 | Maximum | Bytes/op | Glamour median | p95 | Maximum | Bytes/op |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| open/cold-projection | 10.119 | 11.265 | 11.569 | 6501644 | 10.244 | 10.899 | 11.394 | 6504762 |
| edit/0/1-runes | 4.495 | 4.741 | 4.921 | 14691587 | 4.331 | 4.724 | 5.218 | 14702616 |
| edit/1/8-runes | 2.904 | 3.099 | 3.248 | 637601 | 2.925 | 3.199 | 3.384 | 636829 |
| edit/2/32-runes | 2.676 | 2.768 | 2.986 | 534540 | 2.722 | 2.920 | 3.152 | 533020 |
| edit/3/256-runes | 2.403 | 2.492 | 2.809 | 520923 | 2.521 | 2.656 | 2.997 | 522248 |
| edit/4/256-runes | 2.516 | 2.745 | 2.930 | 561658 | 2.622 | 2.770 | 3.039 | 560942 |
| edit/5/15-runes | 2.751 | 2.846 | 3.150 | 504577 | 2.817 | 2.938 | 3.079 | 509478 |
| enter | 0.185 | 0.207 | 0.599 | 280542 | 0.196 | 0.237 | 0.622 | 279830 |
| navigate/n | 0.237 | 0.271 | 0.633 | 508448 | 0.241 | 0.458 | 0.904 | 505621 |
| navigate/N | 0.260 | 0.459 | 0.552 | 604522 | 0.264 | 0.419 | 0.816 | 605889 |
| selected/resize | 9.987 | 10.399 | 10.646 | 5750612 | 10.238 | 10.624 | 10.809 | 5758385 |
| rapid/final-keystroke | 2.860 | 3.175 | 3.289 | 2329524 | 2.870 | 3.165 | 3.272 | 2328944 |
| full-corpus/build+scan0 | 3.741 | 4.021 | 4.422 | 16302159 | 3.838 | 4.392 | 6.290 | 16302221 |
| full-corpus/build+scan1 | 3.020 | 3.195 | 3.301 | 2351130 | 2.902 | 3.182 | 3.266 | 2351131 |
| full-corpus/build+scan2 | 2.710 | 2.893 | 3.248 | 2248728 | 2.676 | 2.867 | 2.896 | 2248730 |
| full-corpus/build+scan3 | 2.393 | 2.484 | 2.550 | 2227229 | 2.418 | 2.601 | 2.712 | 2227224 |
| full-corpus/build+scan4 | 2.397 | 2.533 | 2.657 | 2225692 | 2.524 | 2.881 | 3.218 | 2225689 |
| full-corpus/build+scan5 | 2.851 | 3.227 | 3.852 | 2224238 | 2.830 | 3.019 | 3.565 | 2224180 |

The Task 3 quality run also passed. `vault-find-task3.json` was compared with
the existing `master.json` baseline (detached HEAD at `397ecfd`); qualstat
verified the common dataset digest and every retrieval/context-reduction metric
was unchanged. See [Task 3 evidence](tasks.md#task-3-evidence--2026-10-02) for
review and full-suite results.


## Task 5 fuzzy gate — 2026-10-02

The final reviewed implementation passes the 100 ms maximum-latency gate in
both builds: **28.876 ms** default, **27.462 ms** glamour. Each of the 39 operation
classes below has 100 completed samples per build, including 21 integrated fuzzy
classes. The unchanged exact sample set also passes. These are final reruns after
the isolated review corrections, not matcher-only measurements.

Machine: Apple M4 Pro, macOS 26.6.2 (25G83), Go 1.25.2 darwin/arm64,
GOMAXPROCS=14, CGO enabled. Builds and tests were finished before these sequential
measurements; no concurrent build or test job was launched. Revision: dirty
`feat/session_search` at `26fa34c`. SHA-256 of concatenated `find_text.go`,
`find_fuzzy.go`, `find_picker.go`, `find_render.go`, `viewer_find.go`,
`viewer_targets.go`, `viewer.go` (under `internal/vault/tui`, in that order):
`eefba817459a4cff20fb39247beeeef52cda8c2d7d8b05505324b908ef6ba57e`.

The fixture remains seed 20260920, digest
`d565dddaa76fbb50f300b5ab7d5c79e3f5b0add1c46fa09d1d8528175b9a0ef9`:
10,000 content lines, 1,034,588 bytes, 200 messages, 100 collapsed bodies.
Terminal: 100×30; resize: 80×30. Queries remain the six listed above, including
matching 1/8/32/256-code-point cases and a no-match case. All results remain
addressable; there is no match cap.

Measurement includes Model.Update, scheduler delivery, cold corpus construction,
matching and sorting, cell-bounded snippet preparation, projection when accepting,
result application and Model.View. Opening the picker includes empty browsing of
all 10,000 lines. Acceptance cases use actual selection keys to reach ordinary,
summary and hidden-body rows. Rapid typing measures final-keystroke-to-current-
result and verifies obsolete work retires. Archive decoding and terminal painting
remain outside the in-process update measure.

Times are milliseconds; allocation is mean bytes per completed operation.

| Operation | Default median | p95 | Maximum | Bytes/op | Glamour median | p95 | Maximum | Bytes/op |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| open/cold-projection | 10.193 | 10.696 | 13.608 | 6505216 | 10.311 | 11.048 | 12.217 | 6503492 |
| edit/0/1-runes | 4.239 | 4.462 | 4.726 | 14685913 | 4.212 | 4.364 | 4.830 | 14711768 |
| edit/1/8-runes | 2.923 | 3.388 | 3.855 | 640495 | 2.842 | 3.032 | 3.262 | 636105 |
| edit/2/32-runes | 2.679 | 2.806 | 3.004 | 535937 | 2.640 | 2.728 | 2.997 | 532949 |
| edit/3/256-runes | 2.398 | 2.601 | 2.818 | 521625 | 2.580 | 3.287 | 3.439 | 523025 |
| edit/4/256-runes | 2.495 | 2.614 | 2.789 | 561679 | 2.587 | 2.818 | 2.939 | 560294 |
| edit/5/15-runes | 2.773 | 2.890 | 2.954 | 505959 | 2.790 | 3.044 | 3.252 | 505976 |
| enter | 0.185 | 0.224 | 0.519 | 280537 | 0.187 | 0.222 | 0.573 | 282649 |
| navigate/n | 0.235 | 0.267 | 0.609 | 509141 | 0.223 | 0.262 | 0.543 | 507019 |
| navigate/N | 0.261 | 0.409 | 0.599 | 606518 | 0.247 | 0.504 | 0.648 | 605756 |
| selected/resize | 9.935 | 10.337 | 11.259 | 5748497 | 10.407 | 10.809 | 11.655 | 5750618 |
| rapid/final-keystroke | 2.823 | 2.977 | 3.078 | 2327576 | 2.851 | 3.143 | 3.315 | 2327607 |
| full-corpus/build+scan0 | 3.650 | 3.881 | 4.004 | 16302106 | 3.790 | 4.030 | 4.433 | 16302165 |
| full-corpus/build+scan1 | 2.823 | 3.047 | 3.135 | 2351128 | 2.810 | 3.132 | 3.258 | 2351132 |
| full-corpus/build+scan2 | 2.660 | 2.893 | 2.969 | 2248728 | 2.633 | 2.844 | 3.015 | 2248729 |
| full-corpus/build+scan3 | 2.350 | 2.538 | 2.622 | 2227224 | 2.356 | 2.636 | 2.712 | 2227226 |
| full-corpus/build+scan4 | 2.361 | 2.652 | 2.713 | 2225688 | 2.373 | 2.602 | 2.905 | 2225688 |
| full-corpus/build+scan5 | 2.748 | 3.009 | 3.156 | 2224176 | 2.764 | 3.099 | 3.249 | 2224177 |
| fuzzy/open-cold | 10.937 | 12.574 | 13.038 | 8959881 | 10.969 | 11.368 | 11.590 | 8959511 |
| fuzzy/edit0/1 | 24.630 | 25.401 | 28.876 | 9031267 | 24.870 | 25.942 | 27.462 | 9031302 |
| fuzzy/edit1/8 | 16.960 | 17.654 | 17.904 | 3866464 | 17.356 | 18.986 | 20.867 | 3866428 |
| fuzzy/edit2/32 | 6.823 | 7.680 | 8.650 | 1081393 | 7.043 | 7.370 | 7.683 | 1081324 |
| fuzzy/edit3/256 | 6.586 | 6.942 | 7.254 | 995403 | 6.602 | 7.365 | 7.669 | 995326 |
| fuzzy/edit4/256 | 5.684 | 5.989 | 6.291 | 641059 | 6.007 | 6.527 | 6.846 | 641062 |
| fuzzy/edit5/15 | 5.467 | 5.643 | 5.679 | 437603 | 5.613 | 6.689 | 9.229 | 437696 |
| fuzzy/down | 0.064 | 0.087 | 0.406 | 218326 | 0.078 | 0.102 | 0.392 | 218306 |
| fuzzy/pgdown | 0.063 | 0.080 | 0.371 | 218303 | 0.075 | 0.122 | 0.239 | 218305 |
| fuzzy/pgup | 0.063 | 0.081 | 0.239 | 218303 | 0.076 | 0.137 | 0.267 | 218304 |
| fuzzy/picker-resize | 8.905 | 9.282 | 10.080 | 3472213 | 9.303 | 9.757 | 10.060 | 3472118 |
| fuzzy/accept-visible | 9.778 | 10.263 | 11.087 | 4256899 | 10.316 | 10.878 | 11.205 | 4260292 |
| fuzzy/resize-visible | 10.079 | 10.408 | 10.666 | 5752844 | 10.652 | 11.272 | 11.553 | 5754691 |
| fuzzy/accept-summary | 9.766 | 10.169 | 10.413 | 4364309 | 10.448 | 11.588 | 12.612 | 4368292 |
| fuzzy/resize-summary | 10.100 | 10.421 | 10.952 | 5887377 | 10.611 | 10.970 | 11.232 | 5891323 |
| fuzzy/accept-body | 9.908 | 10.344 | 10.615 | 4371347 | 10.233 | 10.950 | 12.189 | 4363322 |
| fuzzy/resize-body | 10.217 | 10.586 | 10.828 | 5894838 | 10.517 | 11.065 | 11.366 | 5885423 |
| fuzzy/accept-browse | 9.781 | 10.678 | 11.972 | 4261257 | 10.149 | 10.703 | 11.176 | 4256188 |
| fuzzy/resize-browse | 10.056 | 10.515 | 10.868 | 5747570 | 10.948 | 11.473 | 11.800 | 5757932 |
| fuzzy/cancel-exact | 0.175 | 0.200 | 0.546 | 275942 | 0.179 | 0.210 | 0.442 | 275197 |
| fuzzy/rapid-final | 17.064 | 17.704 | 18.001 | 5548624 | 17.473 | 18.783 | 19.469 | 5547815 |

The pre-optimization default gate also passed (maximum 38.291 ms), but exposed
about 97 MB of allocations per cold empty-picker update. Reusing one scratch glyph
buffer per snippet reduced the final cold-picker allocation to roughly 9 MB.
The optimization preserves full-line matching and all selectable rows.

Stress cancellation outside the reference gate was also exercised on the final
code. Cancellation was injected after four scanner checkpoints; values include
work up to that checkpoint rather than promising OS scheduling latency.

| Fuzzy cancellation workload | Default | Glamour |
| --- | ---: | ---: |
| 100,000 lines | 792ns | 833ns |
| Single 1 MiB line | 28.416µs | 31.75µs |
| Long grapheme sequences | 56.292µs | 59.416µs |

The existing stress heap measurements still describe exact corpus/projection
structures. Full fuzzy retained-memory and repeated nested open/back measurements,
all root/raw suspension latency paths, and the final feature-wide matrix remain
Task 6. This gate certifies the integrated fuzzy operation classes above.

Reproduction uses synthetic keys, `CGO_ENABLED=1` and
`GOCACHE=/tmp/capy-go-build`:

~~~sh
CAPY_FIND_BENCH=1 go test -tags fts5 -run '^TestFind(Latency|Stress)$' -count=1 -v ./internal/vault/tui/
CAPY_FIND_BENCH=1 go test -tags fts5,glamour -run '^TestFind(Latency|Stress)$' -count=1 -v ./internal/vault/tui/
go test -tags fts5 -run '^$' -bench '^BenchmarkFindFuzzy$' -benchmem -count=6 ./internal/vault/tui/
go test -tags fts5,glamour -run '^$' -bench '^BenchmarkFindFuzzy$' -benchmem -count=6 ./internal/vault/tui/
~~~

`make bench-quality BENCH_BRANCH=vault-find-task5` passed. `bench-compare`
verified the common dataset digest and every retrieval/context-reduction metric
matched `master.json` (detached HEAD at `397ecfd`); the new report records dirty
`feat/session_search` at `26fa34c`. The optional benchstat comparison was skipped
because benchstat is not installed. Final feature baseline comparison remains
Task 6 as specified in the implementation plan.


### Task 5 matcher benchmark detail

Six repetitions of each `BenchmarkFindFuzzy` query completed in both builds on
the final production code. These measure matching plus sorting; they support
cost analysis and do not replace the application-path gate above.

| Query | Default ns/op range | Glamour ns/op range | Default B/op | Glamour B/op |
| --- | ---: | ---: | ---: | ---: |
| 1 rune | 4369715–4451261 | 4314027–4413700 | 2082972–2083013 | 2082976–2083021 |
| 8 runes | 5580879–5767275 | 5532233–5688602 | 573464–573467 | 573465–573468 |
| 32 runes | 5526240–5666007 | 5466741–5514893 | 293912 | 293913–293914 |
| 256 ASCII runes | 5335027–5421404 | 5105981–5294231 | 240408–240409 | 240408–240410 |
| 256 Unicode runes | 5540534–5778477 | 5449132–5594712 | 120600 | 120600 |
| No match | 5427337–5519394 | 5405761–6488367 | 24 | 24 |
