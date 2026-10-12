# ADR-013: Configurable fetch TTL (vs hardcoded 24h)

**Status:** Accepted
**Date:** 2026-03-31
**Upstream:** context-mode hardcodes `const TTL_MS = 24 * 60 * 60 * 1000` for `fetch_and_index` cache.

## Context

The TS upstream added a 24-hour TTL cache for `ctx_fetch_and_index` — if a URL was fetched within 24h, return a cache hint instead of re-fetching. The TTL is hardcoded.

capy has its own config system (ADR-008), making configurability trivial.

## Decision

Make the TTL configurable via `[store.cache] fetch_ttl_hours = 24` in `.capy.toml`. Default: 24 (matching upstream).

## Rationale

- Different projects have different freshness needs: fast-moving API docs vs stable language references
- A shorter TTL (e.g., 4h) makes sense for actively-developed documentation
- A longer TTL (e.g., 168h / 1 week) makes sense for stable references (Go stdlib, SQL spec)
- Configurable per-project via `.capy.toml`
- The `force: true` parameter on `capy_fetch_and_index` always bypasses the cache regardless of TTL

## Amendment: successful validation renews freshness (2026-10-11)

Task 16a of upstream-sync-v1.0.169 replaces `indexed_at` as the cache clock with
nullable `sources.fetch_validated_at_ms`. Same-hash indexing preserves indexing
age, so using it for freshness caused repeated network fetches after expiry.
`last_accessed_at` is also unsuitable because ordinary searches change it.

Capture each successful response's timestamp after its complete body is admitted,
before HTML conversion, response cleanup, batch queueing or database lock waits.
`IndexFetched` commits content, requested kind and the supplied timestamp in one
transaction. Unchanged content renews this marker and returns stored chunk counts;
it retains `indexed_at` and existing kind/access behavior. A delayed older response
that commits last carries its own timestamp, never a newer response's marker.
Failures leave the old entry intact. HTML conversion errors are now returned
instead of falling back to a cacheable raw-HTML entry.

Both fetch modes require a matching source kind and
`0 <= now_ms - validated_at_ms < configured_ttl_ms`. NULL, nonpositive, unreadable
or future markers miss conservatively. Generic indexing never stamps validation;
replacement clears it. Cache hits and search access cannot renew it. Ephemeral
retention still uses indexing age, so cleanup can evict an old unchanged source
immediately after successful revalidation. Durable retention scoring is unchanged.

Migration 021 is additive, idempotent and transactional on encrypted stores.
Existing rows intentionally miss once after upgrade. A binary from `c5a2f297`
(before this migration) was verified reading/writing the upgraded database while
a new store was open and after reopen; old replacement writes leave the marker
NULL. Downgrades retain readable content but lose the new freshness guarantees.
Per-call controls and cache statistics are recorded below.

## Amendment: request-scoped fetch freshness (2026-10-11)

Task 16 adds optional `ttl` in milliseconds to both fetch modes. Omission uses
configured hours, zero bypasses, and `force: true` always bypasses. Accept integral
JSON numbers and trimmed decimal digit strings, bounded by 9,223,372,036,854 ms
so multiplication into Go's nanosecond duration cannot overflow. Validate supplied
values even when force is true. Negative/overflowing configured hours also fail
before I/O when omission selects them. No per-item overrides or persisted TTLs
are introduced. String exponent/decimal/sign syntax is deliberately excluded.

The effective duration is passed as a request-local value to each batch worker.
The persisted validation predicate keeps its strict millisecond boundary, so an
entry aged exactly 500 ms misses under `ttl: 500`. Changing a later request's TTL
re-evaluates the same marker; cache reads never renew it. Output separates fetch
freshness from the configured ephemeral retention period or durable scoring.

Fetch dispatch errors retain their typed causes until classification. Temporary
or timeout DNS, uncanceled network timeouts, and network/host-unreachable errors
may append one fixed retry suggestion. Cancellation, permission and NXDOMAIN
errors do not qualify; neither do ordinary TLS/certificate, SSRF, HTTP, body,
conversion or indexing failures. This keeps guidance actionable without an
automatic retry, permissive string heuristic or change to security policy.

## Amendment: cache outcome visibility (2026-10-12)

Task 17 counts one hit or attempted miss per eligible URL in both fetch modes.
Bypasses and failed DNS/network, HTTP, body, conversion or indexing work count
as attempted misses. Malformed URLs, SSRF policy blocks, Git-platform redirects
and cancellation before dispatch count as neither. Typed outcomes and transport
policy errors preserve this distinction before error messages are formatted.
Counters, estimated avoided bytes, snapshot and reset share the stats mutex;
`purge_all` preserves the original session start time.

The cache section always reports counts and a hit rate, with `n/a` when unused
and `0.0%` when every attempt missed. **Default fetch freshness** reports configured
hours. The former uptime-derived **TTL remaining** was misleading: entries have
different validation times and requests may choose different TTLs. Both context
savings displays keep the existing formula but use one decimal, capped at
`99.9%` when returned bytes are nonzero. Positive processed bytes with no returned
bytes display `100.0%`; no processed bytes display `0.0%`.

## Consequences

- `CacheConfig.FetchTTLHours` added to config with default 24
- TTL cache check in `handleFetchAndIndex` reads from config instead of hardcoded constant
- Stats report shows configured default fetch freshness, hit/miss counts and a zero-safe hit rate; it does not infer any entry's remaining freshness from session uptime
