# Task 1 performance evidence

The implemented main-transcript exact-search path passes the early 100 ms gate
in both builds. The final measured maximum was **27.052 ms**. This report covers
Task 1 only; hidden-target navigation, nested frame suspension, raw/child return,
and fuzzy selection remain Tasks 2–5 and have no performance certification here.

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
