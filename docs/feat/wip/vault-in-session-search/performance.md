# In-session search performance evidence

The [complete Task 6 protocol](#task-6-complete-protocol--2026-10-02) passes: all
86 operation classes × 100 samples per build meet 100 ms, with a maximum of
**29.135 ms default / 28.601 ms glamour**. It includes the final suspension and
retained-memory measurements. Earlier sections below preserve the historical
Task 1/3/5 gates and their limits at the time of measurement.

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

## Task 6 complete protocol — 2026-10-02

The final harness passes **all 86 operation classes × 100 completed samples per
build**: maximum **29.135 ms default / 28.601 ms glamour**, below the 100 ms gate.
This includes the earlier 39 exact/fuzzy classes and 47 new hidden-transition,
local-frame, child-session and raw-return classes. Ordinary `TestFindLifecycle`
runs the same new scenario assertions for both Claude and Codex without timing
thresholds. Final raw measurements:
[default](.reviews/task6-latency-default.txt),
[glamour](.reviews/task6-latency-glamour.txt).

Machine: Apple M4 Pro (14 logical CPUs), macOS 26.6.2 (25G83), Go 1.25.2
darwin/arm64, GOMAXPROCS=14, CGO enabled. Measurements ran sequentially after the
repository test/race/vet/build jobs finished. The machine sample before the final
runs reported 92.1% CPU idle. Revision: dirty `feat/session_search` at `9f89f15`;
Task 6 adds test harnesses and documentation plus a narrow one-row status layout
correction found by final spec review. The search-core source digest
uses Task 5's same file order:
`eefba817459a4cff20fb39247beeeef52cda8c2d7d8b05505324b908ef6ba57e`.
Harness digest (`find_bench_test.go`, then `find_lifecycle_bench_test.go`):
`eedce7f824e74e8a214835376e97d66b37982122b6988447548cb7a6c3af0c4c`.

Both final runs were repeated after the one-row root status correction. The
full viewer bundle digest (the seven Task 5 files followed by `app.go`, `raw.go`)
is `a951ce7132e364accea0fd35c2959d382e972e13845e549298d55679f413d703`.

The fixture remains seed 20260920, SHA-256
`d565dddaa76fbb50f300b5ab7d5c79e3f5b0add1c46fa09d1d8528175b9a0ef9`:
10,000 content lines, 1,034,588 field bytes, 200 messages and 100 collapsed bodies.
The original 1/8/32/256-code-point queries and no-match query are unchanged.
Initial size is 100×30; return cases include 80×30 width changes and 100×10 height
changes. These are parsed-message fixtures installed in already-open scopes;
archive loading/decoding and raw formatting finish before timed search returns.
Model.Update, command scheduling, computation, result application and Model.View
are included. The terminal emulator's painting remains outside this measure.

New exact cases cover visible → summary → hidden body → visible, replacement of
one tool detail by another, hidden resize, draft cancellation, clear, and both
clear-then-back and immediate back. Local cases restore a tool's sidecar and a
sidecar's parent; root cases restore two child levels, with and without resize.
Raw-return cases cover main, sidecar, manual tool, search-selected tool and child,
each with exact or accepted fuzzy selection and all three terminal sizes. Checks
pin original source positions, query/highlight identity, target, bounded frame
depth, fresh epochs, worker retirement and zero search/metadata mutations.

Each row below reports milliseconds and mean allocated bytes per operation.
Timing includes correctness assertions as well as the application work. Test
cleanup joins all dispatched Batch children, including cosmetic cursor timers;
those waits are outside search-update timing. The initial default run also passed
(maximum 27.924 ms), but its unjoined timer closures contaminated retained-memory
checkpoints. The table and heap evidence below use the corrected, re-reviewed
harness and fresh runs; no failing reference operation was dropped.

| Operation | Default median | p95 | Maximum | Bytes/op | Glamour median | p95 | Maximum | Bytes/op |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| open/cold-projection | 10.038 | 10.463 | 13.432 | 6508520 | 10.113 | 10.908 | 12.191 | 6499092 |
| edit/0/1-runes | 4.379 | 4.579 | 4.813 | 14691754 | 4.421 | 4.675 | 5.177 | 14706203 |
| edit/1/8-runes | 2.902 | 3.099 | 3.228 | 638292 | 2.894 | 3.191 | 3.508 | 643229 |
| edit/2/32-runes | 2.672 | 2.812 | 3.075 | 534746 | 2.703 | 3.093 | 3.242 | 536743 |
| edit/3/256-runes | 2.438 | 2.665 | 2.813 | 524447 | 2.430 | 2.601 | 2.816 | 523682 |
| edit/4/256-runes | 2.559 | 2.778 | 2.950 | 561076 | 2.521 | 2.746 | 2.969 | 561734 |
| edit/5/15-runes | 2.808 | 3.049 | 3.231 | 509548 | 2.765 | 3.060 | 3.193 | 508793 |
| enter | 0.181 | 0.203 | 0.346 | 281086 | 0.185 | 0.231 | 0.490 | 281834 |
| navigate/n | 0.244 | 0.263 | 0.689 | 509812 | 0.249 | 0.353 | 0.616 | 506959 |
| navigate/N | 0.274 | 0.456 | 0.707 | 614000 | 0.275 | 0.462 | 0.615 | 607657 |
| selected/resize | 10.105 | 10.401 | 10.575 | 5751123 | 10.186 | 10.913 | 12.073 | 5753800 |
| rapid/final-keystroke | 2.875 | 3.104 | 3.178 | 2336382 | 2.850 | 3.094 | 3.466 | 2334272 |
| full-corpus/build+scan0 | 3.834 | 4.005 | 4.207 | 16302297 | 3.798 | 4.019 | 4.374 | 16302167 |
| full-corpus/build+scan1 | 2.824 | 3.109 | 3.202 | 2351132 | 2.815 | 3.229 | 3.272 | 2351129 |
| full-corpus/build+scan2 | 2.647 | 2.879 | 3.094 | 2248732 | 2.622 | 3.016 | 3.192 | 2248728 |
| full-corpus/build+scan3 | 2.334 | 2.629 | 2.783 | 2227226 | 2.333 | 2.704 | 2.802 | 2227228 |
| full-corpus/build+scan4 | 2.340 | 2.745 | 2.812 | 2225690 | 2.349 | 2.782 | 2.931 | 2225688 |
| full-corpus/build+scan5 | 2.757 | 3.002 | 3.139 | 2224176 | 2.727 | 3.074 | 3.206 | 2224180 |
| fuzzy/open-cold | 11.067 | 11.515 | 11.575 | 8960424 | 11.160 | 11.736 | 11.950 | 8960370 |
| fuzzy/edit0/1 | 25.573 | 26.832 | 29.135 | 9032603 | 25.032 | 26.641 | 28.601 | 9032385 |
| fuzzy/edit1/8 | 17.218 | 18.142 | 18.644 | 3867726 | 17.060 | 19.008 | 20.574 | 3867658 |
| fuzzy/edit2/32 | 6.865 | 7.197 | 7.322 | 1082732 | 6.841 | 7.554 | 7.787 | 1082771 |
| fuzzy/edit3/256 | 6.492 | 6.875 | 7.087 | 997282 | 6.460 | 6.767 | 7.049 | 997344 |
| fuzzy/edit4/256 | 5.608 | 5.884 | 6.112 | 642500 | 5.648 | 5.991 | 6.429 | 642428 |
| fuzzy/edit5/15 | 5.524 | 5.842 | 6.019 | 439023 | 5.690 | 6.324 | 6.562 | 439041 |
| fuzzy/down | 0.068 | 0.148 | 0.285 | 218894 | 0.077 | 0.117 | 0.273 | 218906 |
| fuzzy/pgdown | 0.064 | 0.130 | 0.218 | 218897 | 0.077 | 0.123 | 0.283 | 218897 |
| fuzzy/pgup | 0.069 | 0.088 | 0.242 | 218898 | 0.078 | 0.085 | 0.214 | 218868 |
| fuzzy/picker-resize | 8.902 | 9.317 | 9.410 | 3473133 | 9.177 | 10.334 | 10.724 | 3473106 |
| fuzzy/accept-visible | 9.995 | 10.378 | 10.600 | 4261442 | 9.882 | 10.253 | 10.643 | 4261382 |
| fuzzy/resize-visible | 10.261 | 10.791 | 11.107 | 5754207 | 10.231 | 10.851 | 12.467 | 5746388 |
| fuzzy/accept-summary | 10.103 | 10.793 | 12.544 | 4370794 | 9.924 | 10.298 | 11.008 | 4366435 |
| fuzzy/resize-summary | 10.362 | 10.834 | 11.445 | 5880939 | 10.157 | 10.595 | 10.802 | 5880156 |
| fuzzy/accept-body | 9.992 | 10.394 | 10.560 | 4362953 | 9.832 | 10.271 | 10.376 | 4365629 |
| fuzzy/resize-body | 10.220 | 10.596 | 10.843 | 5882015 | 10.451 | 11.272 | 12.462 | 5887614 |
| fuzzy/accept-browse | 9.879 | 10.268 | 10.537 | 4261462 | 9.923 | 10.379 | 10.616 | 4261440 |
| fuzzy/resize-browse | 10.256 | 10.662 | 10.982 | 5754573 | 10.321 | 11.016 | 12.088 | 5750286 |
| fuzzy/cancel-exact | 0.186 | 0.213 | 0.351 | 276495 | 0.190 | 0.208 | 0.426 | 276491 |
| fuzzy/rapid-final | 17.346 | 17.947 | 18.288 | 5555476 | 17.575 | 18.427 | 19.625 | 5553934 |
| exact/visible-to-summary | 0.395 | 0.426 | 1.421 | 680703 | 0.400 | 0.483 | 1.096 | 677895 |
| exact/summary-to-body | 0.360 | 0.384 | 0.819 | 576280 | 0.359 | 0.397 | 0.921 | 580562 |
| exact/body-to-visible | 0.393 | 0.421 | 0.887 | 589858 | 0.398 | 0.443 | 0.856 | 587693 |
| exact/detail-to-detail | 0.386 | 0.441 | 0.913 | 658096 | 0.371 | 0.405 | 0.926 | 655980 |
| exact/hidden-resize | 10.411 | 10.826 | 11.117 | 5898509 | 10.600 | 11.546 | 12.175 | 5901204 |
| exact/cancel-hidden | 0.197 | 0.228 | 0.539 | 276356 | 0.190 | 0.223 | 0.815 | 276335 |
| exact/clear-hidden | 0.245 | 0.421 | 0.553 | 529483 | 0.264 | 0.299 | 0.794 | 530891 |
| exact/back-cleared | 0.143 | 0.164 | 0.364 | 263821 | 0.141 | 0.165 | 0.604 | 264304 |
| exact/back-hidden | 0.149 | 0.173 | 0.426 | 273334 | 0.151 | 0.164 | 0.412 | 272330 |
| local/tool-return/resize=false | 0.239 | 0.299 | 0.628 | 292904 | 0.239 | 0.268 | 0.880 | 291455 |
| local/tool-return/resize=true | 10.333 | 10.801 | 11.035 | 5730540 | 10.397 | 11.028 | 11.602 | 5732607 |
| local/sidecar-return/resize=false | 0.212 | 0.244 | 0.459 | 280174 | 0.218 | 0.248 | 0.649 | 280879 |
| local/sidecar-return/resize=true | 10.313 | 10.899 | 12.037 | 5717557 | 10.242 | 10.753 | 11.125 | 5721127 |
| root/child-return/resize=false | 0.389 | 0.416 | 0.893 | 359852 | 0.386 | 0.440 | 1.119 | 361969 |
| root/child-return/resize=true | 10.353 | 10.858 | 11.203 | 5770229 | 10.439 | 10.949 | 11.153 | 5767358 |
| root/grandchild-return/resize=false | 0.382 | 0.405 | 0.417 | 377496 | 0.384 | 0.414 | 0.715 | 379654 |
| root/grandchild-return/resize=true | 10.361 | 10.808 | 10.993 | 5786495 | 10.537 | 11.183 | 12.329 | 5783673 |
| raw/main/fuzzy=false/100x30 | 0.215 | 0.233 | 0.247 | 279456 | 0.213 | 0.235 | 0.762 | 280205 |
| raw/main/fuzzy=false/100x10 | 0.123 | 0.142 | 0.559 | 230442 | 0.121 | 0.135 | 0.146 | 229696 |
| raw/main/fuzzy=false/80x30 | 10.533 | 11.299 | 11.576 | 5716821 | 10.360 | 10.774 | 10.946 | 5718899 |
| raw/main/fuzzy=true/100x30 | 0.199 | 0.242 | 0.601 | 273846 | 0.191 | 0.209 | 0.566 | 274547 |
| raw/main/fuzzy=true/100x10 | 0.132 | 0.145 | 0.159 | 230979 | 0.123 | 0.138 | 0.141 | 230978 |
| raw/main/fuzzy=true/80x30 | 10.297 | 10.776 | 11.406 | 5849392 | 10.417 | 11.091 | 11.269 | 5850054 |
| raw/sidecar/fuzzy=false/100x30 | 0.238 | 0.258 | 0.755 | 281307 | 0.240 | 0.263 | 0.794 | 282011 |
| raw/sidecar/fuzzy=false/100x10 | 0.117 | 0.128 | 0.138 | 226608 | 0.118 | 0.146 | 0.471 | 228060 |
| raw/sidecar/fuzzy=false/80x30 | 10.267 | 10.841 | 10.906 | 5720903 | 10.288 | 10.785 | 11.124 | 5719451 |
| raw/sidecar/fuzzy=true/100x30 | 0.187 | 0.202 | 0.216 | 273800 | 0.184 | 0.199 | 0.209 | 273800 |
| raw/sidecar/fuzzy=true/100x10 | 0.123 | 0.142 | 0.576 | 230581 | 0.118 | 0.143 | 0.372 | 231284 |
| raw/sidecar/fuzzy=true/80x30 | 10.295 | 10.858 | 10.935 | 5857641 | 10.449 | 11.156 | 11.983 | 5860381 |
| raw/manual-tool/fuzzy=false/100x30 | 0.194 | 0.217 | 0.683 | 275603 | 0.202 | 0.219 | 0.520 | 276308 |
| raw/manual-tool/fuzzy=false/100x10 | 0.121 | 0.140 | 0.149 | 229832 | 0.135 | 0.153 | 0.491 | 230580 |
| raw/manual-tool/fuzzy=false/80x30 | 0.263 | 0.284 | 0.984 | 500711 | 0.273 | 0.297 | 0.748 | 496447 |
| raw/manual-tool/fuzzy=true/100x30 | 0.195 | 0.225 | 0.589 | 273841 | 0.204 | 0.229 | 0.545 | 274552 |
| raw/manual-tool/fuzzy=true/100x10 | 0.122 | 0.144 | 0.366 | 231287 | 0.131 | 0.145 | 0.462 | 231987 |
| raw/manual-tool/fuzzy=true/80x30 | 0.262 | 0.279 | 0.491 | 497969 | 0.264 | 0.299 | 0.555 | 500837 |
| raw/selected-tool/fuzzy=false/100x30 | 0.200 | 0.222 | 0.332 | 276324 | 0.200 | 0.220 | 0.478 | 275661 |
| raw/selected-tool/fuzzy=false/100x10 | 0.121 | 0.142 | 0.350 | 229820 | 0.121 | 0.137 | 0.227 | 229073 |
| raw/selected-tool/fuzzy=false/80x30 | 10.395 | 10.836 | 10.972 | 5819351 | 10.306 | 10.847 | 11.310 | 5814417 |
| raw/selected-tool/fuzzy=true/100x30 | 0.189 | 0.208 | 0.882 | 274544 | 0.189 | 0.213 | 0.442 | 275252 |
| raw/selected-tool/fuzzy=true/100x10 | 0.122 | 0.135 | 0.359 | 230578 | 0.119 | 0.133 | 0.401 | 230584 |
| raw/selected-tool/fuzzy=true/80x30 | 10.332 | 10.855 | 10.945 | 5846480 | 10.374 | 10.882 | 11.097 | 5847145 |
| raw/child/fuzzy=false/100x30 | 0.212 | 0.231 | 0.752 | 280932 | 0.214 | 0.233 | 0.721 | 280934 |
| raw/child/fuzzy=false/100x10 | 0.119 | 0.143 | 0.355 | 231172 | 0.124 | 0.144 | 0.337 | 230466 |
| raw/child/fuzzy=false/80x30 | 10.432 | 11.532 | 12.428 | 5717532 | 10.419 | 11.322 | 11.627 | 5717532 |
| raw/child/fuzzy=true/100x30 | 0.193 | 0.217 | 0.598 | 274568 | 0.218 | 0.261 | 0.767 | 275279 |
| raw/child/fuzzy=true/100x10 | 0.125 | 0.140 | 0.148 | 229856 | 0.129 | 0.145 | 0.429 | 231310 |
| raw/child/fuzzy=true/80x30 | 10.334 | 11.115 | 12.356 | 5849369 | 10.317 | 10.827 | 12.658 | 5847170 |

### Comparison with the earlier gates

Task 1's maximum was 27.052 ms on Linux/Intel; different hardware and an earlier
partial interaction set prevent a speedup/regression inference. Task 5 used this
same Apple/Go configuration and recorded 28.876 ms default / 27.462 ms glamour
across its 39 classes. Task 6 reruns those classes and adds 47; all samples still
pass. Small timing differences are observed run-to-run variation, not a claim of
statistical equivalence or improvement. The normal viewer's load/render cost is
outside all three search-update protocols.

### Stress and retained memory

These larger inputs assess scaling and cancellation; the 100 ms contract applies
to the reference fixture, not arbitrary input size. Exact retained bytes keep the
corpus, matches and projection alive across GC after baseline source strings
already exist. Fuzzy retained bytes are incremental for full matching, sorting
and prepared snippets while the exact structures also remain live. No result cap
or dropped line is used.

| Workload | Default exact time / retained bytes | Glamour exact time / retained bytes | Default fuzzy time / extra bytes | Glamour fuzzy time / extra bytes |
| --- | --- | --- | --- | --- |
| 100,000 lines (1,199,999 bytes) | 18.081542ms / 19657688 | 19.948542ms / 19657936 | 623.2155ms / 23829320 | 623.878ms / 23829256 |
| One MiB line (1,048,576 bytes) | 11.751958ms / 2883208 | 11.506584ms / 2883208 | 6.762042ms / 2936 | 6.383416ms / 2936 |
| Long graphemes (1,049,152 bytes / 65 lines) | 12.579125ms / 2208128 | 10.190625ms / 2208192 | 11.071875ms / 1195752 | 9.878208ms / 1195752 |

Cancellation is injected after four scanner checkpoints, including inside long
lines. These values include work before that checkpoint, not an OS scheduling
guarantee. Every cancellation returned the typed cancellation result.

| Workload | Default exact / fuzzy | Glamour exact / fuzzy |
| --- | --- | --- |
| 100,000 lines | 875ns / 792ns | 1.042µs / 1µs |
| One MiB line | 27.458µs / 28.625µs | 28.042µs / 28.042µs |
| Long graphemes | 15.208µs / 61µs | 15.375µs / 58.25µs |

`TestFindLifetimeStress` warms the normal viewer, then measures exact search and
an open fuzzy picker. Twenty cycles open a Codex child with a fresh reference
corpus, open its tool detail, accept a fuzzy selection, inspect raw data and return
to the parent. Every heap checkpoint follows command retirement and GC; suspended
and discarded stack capacities are asserted empty on return. The final viewer
exit also asserts nil corpus/projection and empty stacks.

| Heap delta, bytes | Default | Glamour |
| --- | ---: | ---: |
| Exact above warm normal | 3032664 | 3039512 |
| Open picker above exact | 636384 | 637064 |
| Nested child/tool/fuzzy above parent | 6567048 | 6448040 |
| After 1 return cycle | -176 | -208 |
| After 10 return cycles | 5424 | 10496 |
| After 20 return cycles | 13424 | 14048 |
| After leaving viewer | -6657448 | -6528344 |

Large nested search structures are released on return. Small residual deltas
include testing/runtime bookkeeping and caches; these are measured heap samples,
not an assertion that every byte of process memory is constant. Negative exit
deltas reflect releasing the parent as well. Initial measurements before timer
retirement are excluded from this table because they mixed live cosmetic work
with retained frames.

Reproduction uses the synthetic keys, `CGO_ENABLED=1`, writable
`GOCACHE=/tmp/capy-go-build` and canonical `TMPDIR=/private/tmp`:

~~~sh
CAPY_FIND_BENCH=1 go test -tags fts5 -run '^TestFind(Latency|Stress|LifetimeStress)$' -count=1 -v ./internal/vault/tui/
CAPY_FIND_BENCH=1 go test -tags fts5,glamour -run '^TestFind(Latency|Stress|LifetimeStress)$' -count=1 -v ./internal/vault/tui/
~~~

### Conventional benchmark detail

All 15 corpus/exact/fuzzy/projection/highlight sub-benchmarks completed six
repetitions in each build after the final latency runs. Raw outputs:
[default](.reviews/task6-bench-default.txt),
[glamour](.reviews/task6-bench-glamour.txt). These isolate costs; the application
latency gate remains the evidence above. The benchmarked corpus, matcher,
projection and highlight functions are identical to the final Task 5 source;
Task 6's root one-row status correction does not change these functions.

| Benchmark | Default ns/op range | Glamour ns/op range | Default B/op range | Glamour B/op range |
| --- | ---: | ---: | ---: | ---: |
| Corpus | 289393–304540 | 283526–286329 | 2224182–2224190 | 2224184–2224190 |
| Exact/query0 | 3574338–3668358 | 3533777–3753505 | 14077937–14077940 | 14077968–14077978 |
| Exact/query1 | 2708763–2745592 | 2707167–2725758 | 126952–126952 | 126952–126953 |
| Exact/query2 | 2469773–2531006 | 2468497–2515520 | 24552–24552 | 24552–24552 |
| Exact/query3 | 2209760–2239860 | 2214403–2245732 | 3048–3048 | 3048–3048 |
| Exact/query4 | 2233007–2295864 | 2237241–2250273 | 1512–1512 | 1512–1512 |
| Exact/query5 | 2605696–2665434 | 2611041–2620967 | 0–0 | 0–0 |
| Projection | 9724287–10322810 | 9966724–10494996 | 3745264–3745272 | 3745275–3745281 |
| Highlight | 98918–101283 | 99428–102876 | 13896–13896 | 13896–13896 |
| Fuzzy/query0 | 4516420–4624202 | 4523932–4905815 | 2082970–2082974 | 2082974–2082976 |
| Fuzzy/query1 | 5961222–6234230 | 5721998–5951949 | 573464–573465 | 573464–573467 |
| Fuzzy/query2 | 5825992–5922080 | 5643101–5751152 | 293912–293912 | 293912–293914 |
| Fuzzy/query3 | 5532836–5644782 | 5334815–5485325 | 240408–240408 | 240408–240409 |
| Fuzzy/query4 | 5860838–6113196 | 5598954–5773420 | 120600–120600 | 120600–120600 |
| Fuzzy/query5 | 5837849–6073638 | 5632651–5939249 | 24–24 | 24–24 |

~~~sh
go test -tags fts5 -run '^$' -bench '^BenchmarkFind' -benchmem -count=6 ./internal/vault/tui/
go test -tags fts5,glamour -run '^$' -bench '^BenchmarkFind' -benchmem -count=6 ./internal/vault/tui/
~~~

### Final retrieval compatibility

`make bench-quality BENCH_BRANCH=vault-find-task6` and
`make bench-compare BASE=master TARGET=vault-find-task6` passed. The
[comparison output](.reviews/task6-quality-compare.txt) verifies dataset SHA-256
`7d45338724b05181ebc92bd0b74eb7708bdd4fba27a8d6830b54a127f2b6ba2d`.
Every retrieval-quality and context-reduction metric is unchanged.

Baseline: the existing matching `bench-results/master.json`, produced from
**detached HEAD at 397ecfd**, not a claim that the file was measured on the current
master. Target: `bench-results/vault-find-task6.json`, measured on dirty
`feat/session_search` at **9f89f15** with an explicit report label. No existing
report was overwritten and no new detached checkout/report was created.
The optional benchstat half was skipped because that binary is unavailable;
both conventional six-repetition benchmark sets above completed and retain their
raw results. This is the completed feature comparison, separate from earlier gates.
