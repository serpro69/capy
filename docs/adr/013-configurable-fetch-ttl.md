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

## Consequences

- `CacheConfig.FetchTTLHours` added to config with default 24
- TTL cache check in `handleFetchAndIndex` reads from config instead of hardcoded constant
- Stats report shows TTL remaining based on configured value, not hardcoded 24
