# Task 6 implementation review

> Scope: project credentials in direct MCP sessions
> Review mode: `kk:review-code:isolated`, mid-implementation
> Reviewers: independent `code-reviewer` agent and PAL `gemini-3.1-pro-preview`

## Result

The independent agent reported one P2 test reliability finding and no production correctness or security findings. PAL completed its two-step external review with no findings.

- **P2 — startup-failure assertion depends on scheduling**, `cmd/capy/key_resolution_mcp_test.go`, original line 247. A process that correctly rejects credentials can exit before the parent writes initialization, causing EPIPE instead of the asserted EOF. The helper now keeps stdin open, waits for process exit with a bounded timer, then checks nonzero status, empty stdout, and safe stderr. Registered cleanup still reaps a child if the deadline expires. Author context: this strengthens the failure-phase assertion because rejection must occur without any stdin activity.

The independent agent re-reviewed the narrow fix and approved it with no remaining findings. No findings qualify for knowledge indexing. The review covered four Go files and task bookkeeping. Go checklists covered SOLID, removal, security, style, errors, injection, naming, and concurrency. Completed Tasks 1–5 supplied regression context; pending Tasks 7–11 were excluded from missing-implementation assessment.

## Implementation

`serveRunE`, including bare invocation, now uses strict target loading, shared credential resolution, and explicit-key encryption preflight. Configuration and resolution errors return before server construction. `WithKnowledgeCredentials` supplies the key and safe `config.KeySource` separately; default server construction captures `CAPY_DB_KEY` once. Lazy `getStore` passes that snapshot to `store.WithEncryptionKey`, including explicit empty input. Executor and security scope retain the selected working project.

The existing Task 5 stdio fixture drives two simultaneous projects with crossed inherited credentials, for both key-file and dotenv selection. Real index/search/doctor calls verify distinct markers, then reopened processes read retained content without re-indexing. Bare invocation, subdirectory cwd, and project flags before/after `serve` exercise file-only credentials with no inherited key. Failure cases cover missing/empty/invalid files, invalid dotenv/configuration, missing environment credentials, and plaintext databases. Wrong selected keys remain lazy tool errors, retain safe source hints, leave encrypted bytes unchanged, and preserve searchable content after restoring the correct credential.

Direct server tests change the environment or key file before lazy initialization, then verify encryption and shutdown checkpointing still use the captured key. An explicit empty credential under a nonempty inherited key leaves the database directory, marker, database, and sidecars absent while doctor still reports available FTS5.

Search retains its existing per-query error text and `IsError=false` contract; index returns a tool error. Tests assert each surface's error, source, secret exclusion, and retained-data behavior rather than imposing a new search response contract.

## Verification

Passed:

- Focused FTS5 direct-server and MCP credential tests, plus environment-only stdio/serve regression coverage.
- Focused tests with `-race`, including doctor error coverage and a rerun of MCP credential cases after the review fix.
- Full `make test`, including generated-artifact guards.
- `go vet -tags fts5 ./internal/server ./cmd/capy`, formatting, and whitespace checks.

The initial sandbox run hit existing Unix/HTTP socket restrictions; its CLI result also included an earlier test assertion since corrected. The successful full-suite run permitted local sockets and used synthetic keys with isolated home/config/data/vault paths. No production behavior was changed to accommodate the environment.

The race run instruments direct server tests and the stdio harness. The candidate server built by the fixture remains a normal FTS5 binary; this does not establish race coverage inside that subprocess. Search/indexing algorithms are unchanged, so quality benchmarks do not apply. Full-feature race/matrix verification and durable migration documentation remain assigned to Task 11. No daemon-host smoke check was performed.

No design deviation or new dependency was needed. Wrapper rollout and remaining CLI integrations retain their pending tasks.
