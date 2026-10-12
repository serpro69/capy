# Task 2 isolated code review

Scope: execute-file containment, external Read grants and admitted-path handoff.
Tasks 1 and 2a are regression context; pending sync tasks and the explicitly
deferred D2 atomic handoff are outside this review.

`kk:review-code:isolated` used an independent code-reviewer with no implementation
history. PAL was unavailable. Review was static; the implementation session owns
the verification recorded in [verification.md](../verification.md#task-2-execute-file-path-admission).

The Go profile activated on `.go` files. Checklists: SOLID, removal, security,
code style, error handling, injection and naming; the follow-up also considers
executor concurrency and performance. No new module dependency was introduced.

## Initial finding

**P1 — physical-path handoff could interpolate a different, denied filename.**
The canonical path passed at `internal/server/tool_execute_file.go` reached
`injectFileContent`, which embedded `strconv.Quote` output in Ruby, Elixir, PHP
and Perl double-quoted source strings. A safe `input.txt` alias pointing to
`allowed#{1+1}.txt` could therefore read denied `allowed2.txt` in Ruby/Elixir.
PHP `$variable` and Perl `$variable`/`@array` offered similar substitutions.
This required no filesystem race or malicious submitted code. Reviewer
confidence: 99%; initial assessment: request changes. No other finding or removal
candidate was reported.

## Implemented resolution

The child receives the admitted absolute path through the request-local
`CAPY_FILE_CONTENT_PATH` environment value. All eleven wrappers read it as data
into their established path variables, and `injectFileContent` no longer takes a
filename argument. The value overrides inherited data without mutating the
parent environment; Rust receives it on binary execution. This avoids both
interpolation and cross-language escape syntax without changing runtime cwd.

Regression fixtures use safe aliases, literal interpolation-bearing physical
names, denied alternative destinations, quotes, backslashes, controls and
Unicode. Actual JavaScript, Python, shell, Go, Rust and Perl execution passes.
Capy's detector found no TypeScript, Ruby, PHP, R or Elixir runtime, so their
runtime tests skip explicitly. Node v24.21.0 is present and runs a direct
TypeScript probe; capy's TypeScript candidate list excludes Node. That detection
limitation is outside Task 2. The Rust fixture preserves compiler discovery when
isolating HOME.

API checks used installed Go documentation and official runtime documentation:
[Ruby ENV.fetch](https://docs.ruby-lang.org/en/3.4/ENV.html),
[PHP getenv](https://www.php.net/manual/en/function.getenv.php),
[Perl ENV](https://perldoc.perl.org/variables/%25ENV),
[Rust env::var](https://doc.rust-lang.org/std/env/fn.var.html),
[R Sys.getenv](https://stat.ethz.ch/R-manual/R-devel/library/base/html/Sys.getenv.html),
and [Elixir System.fetch_env!](https://hexdocs.pm/elixir/1.13.0/System.html#fetch_env!/1).
Context7 covered Ruby/PHP; Elixir required the official-doc fallback.

## Follow-up finding

**P2 — JavaScript/TypeScript snippets declaring `process` shadowed the new
environment lookup.** The first environment preamble used `process.env`; a later
`const process = "worker"` in the submitted snippet placed that access in the
temporal dead zone. The reviewer confirmed the original P1 was addressed and
found no other concurrency/performance issue, but requested this compatibility
fix (99% confidence).

The wrapper now uses `require("process").env`, preserving the pre-existing
`require` dependency without reserving `process` in submitted code. Both
JavaScript/TypeScript regression snippets declare a local `process`; the
installed JavaScript runtime passes, and TypeScript remains an explicit skip.

The finding and resolution are captured here and in the implementation document,
so the repository's knowledge protocol skips a duplicate `kk:review-findings`
entry. D2 remains documented at the admission helper: the runtime still opens a
path after policy evaluation, so concurrent replacement is not eliminated.

## Final disposition

**APPROVE.** The independent reviewer inspected both fixes and found no remaining
P0–P3 finding or removal candidate. The final runtime/wrapper race regression
passed after the JS/TS correction, and vet passed again. Review remains static;
the unavailable-runtime and D2 limits above still apply.
