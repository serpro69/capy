# Design: Search & Security Improvements (ported from agentmemory)

**Status:** Draft
**Date:** 2026-04-05
**Origin:** Analysis of [agentmemory](https://github.com/rohitg00/agentmemory) — a TypeScript persistent memory system for AI coding agents. Only algorithmic, zero-LLM ideas were selected. The agentmemory source is available at `/agentmemory/` in this repo for reference.

## Overview

Five independent, additive improvements to capy's search quality and security posture. Each can be implemented and tested in isolation. No changes to the MCP tool API surface or hook system. No LLM dependencies. No new external dependencies.

| # | Feature | Value | Effort | Packages affected |
|---|---------|-------|--------|-------------------|
| 1 | Domain synonym expansion | High | Low | `internal/store` |
| 2 | Secret stripping before indexing | High | Low | new `internal/sanitize`, `internal/store` |
| 3 | Per-source result diversification | Medium | Low | `internal/store` |
| 4 | Entity-aware query boosting | Medium | Low | `internal/store` |
| 5 | Retention-scored cleanup | Medium | Medium | `internal/store` |

---

## Feature 1: Domain Synonym Expansion

### Problem

Capy's FTS5 search relies on Porter stemming (`tokenize='porter unicode61'`) which normalizes word forms (authenticating → authenticate) but cannot handle developer abbreviations or domain equivalences. Searching `"db perf"` won't find content mentioning `"database performance"` or `"latency"`.

### Design

A static lookup table of ~36 developer-domain synonym groups. Groups containing terms that overlap with capy's stopword list (e.g., `test`, `deps`) are excluded — stopwords are stripped at index time, so expanding them at query time would produce silent misses. The `init()` function enforces this invariant with a panic guard. At search time, each query term is checked against the table. If a match is found, the term is replaced with a parenthesized FTS5 OR group containing the term and all its synonyms.

**Synonym groups** (ported from `agentmemory/src/state/synonyms.ts`):

```
auth ↔ authentication ↔ authn ↔ authenticating
authz ↔ authorization ↔ authorizing
db ↔ database ↔ datastore
perf ↔ performance ↔ latency ↔ throughput ↔ slow ↔ bottleneck
optim ↔ optimization ↔ optimizing ↔ optimise
k8s ↔ kubernetes ↔ kube
config ↔ configuration ↔ configuring
env ↔ environment
fn ↔ function
impl ↔ implementation ↔ implementing
msg ↔ message ↔ messaging
repo ↔ repository
req ↔ request
res ↔ response
ts ↔ typescript
js ↔ javascript
pg ↔ postgres ↔ postgresql
err ↔ error ↔ errors
api ↔ endpoint ↔ endpoints
ci ↔ continuous-integration
cd ↔ continuous-deployment
doc ↔ documentation ↔ docs
infra ↔ infrastructure
deploy ↔ deployment ↔ deploying
cache ↔ caching ↔ cached
log ↔ logging ↔ logs
monitor ↔ monitoring
observe ↔ observability
sec ↔ security ↔ secure
validate ↔ validation ↔ validating
migrate ↔ migration ↔ migrations
debug ↔ debugging
container ↔ containerization ↔ docker
webhook ↔ webhooks ↔ callback
middleware ↔ mw
paginate ↔ pagination
serialize ↔ serialization
encrypt ↔ encryption
hash ↔ hashing
```

### Query structure

Current behavior: `sanitizeQuery("db perf", "OR")` → `"db" OR "perf"`

New behavior: `sanitizeQueryWithSynonyms("db perf")` → `("db" OR "database" OR "datastore") ("perf" OR "performance" OR "latency" OR "throughput")`

The space between parenthesized groups is implicit AND in FTS5. This is more precise than flat OR for multi-term queries. Single-term queries produce a single OR group (equivalent to current behavior).

**Fallback:** If the grouped AND query returns zero results, the caller (`rrfSearch`) falls back to flat OR mode. This prevents regression for queries where only one term appears in the corpus.

### Application scope

- Applied to **both** Porter and trigram tables. Trigram tokenization handles substrings but cannot map abbreviations to full words (`"db"` shares no trigrams with `"database"`).
- For trigram queries, the existing min-3-char filter still applies — short synonyms like `"db"` are dropped from trigram queries, which is fine since Porter handles them.
- **Tokenization order:** The query is first cleaned of FTS5 special characters (same `ftsSpecialRe` used by the existing `sanitizeQuery`), then quoted phrases are detected and preserved intact (e.g., `"db config"` stays as a single FTS5 phrase — no synonym expansion). Only unquoted individual terms are expanded. This prevents breaking FTS5 phrase query syntax.
- Synonyms are matched after lowercasing but before FTS5 quoting, so the Porter stemmer still applies to expanded terms.
- The fuzzy correction pass runs after synonym expansion — if a term has synonyms, it's considered "known" and skipped by fuzzy correction.

### Alternatives considered

**Index-time expansion** (inject synonym tokens into content before FTS5 indexing): Rejected because it breaks FTS5's `highlight()` function (used by capy for snippet extraction via STX/ETX markers), bloats the index, and requires full reindexing when synonym groups change. Standard SQLite FTS5 doesn't support custom tokenizers that could handle this cleanly.

**Trigram-only (no Porter expansion):** Rejected because trigram matching is structural — `"db"` shares zero 3-character subsequences with `"database"`, so the abbreviation→full-word mapping that makes synonyms valuable would be lost entirely.

**Hybrid (expand Porter only, skip trigram):** Rejected for the same reason — skipping trigram expansion means the trigram RRF score contributes nothing for synonym matches, weakening the fusion signal.

**Weighted synonyms (0.7 weight for synonyms vs 1.0 for original):** agentmemory uses this in its custom BM25 implementation. FTS5 doesn't support per-term weighting in queries. However, capy's existing proximity reranker naturally handles this — results where expanded terms appear close together (indicating a real topical match) score higher than scattered synonym hits.

---

## Feature 2: Secret Stripping Before Indexing

### Problem

Capy indexes raw content directly into SQLite FTS5. If a user pipes `.env` contents, API responses with tokens, or config files through `capy_execute` or `capy_index`, those secrets are stored persistently in the knowledge base. The DB file lives at `~/.capy/<project-hash>/knowledge.db` and persists across sessions.

### Design

A new `internal/sanitize/` package provides a `StripSecrets(content string) string` function. It replaces detected secret patterns with `[REDACTED_SECRET]` placeholders (or `[REDACTED]` for private tags) using compiled Go regexes.

**Patterns detected** (ported from `agentmemory/src/functions/privacy.ts`):

| Pattern | Example | Regex |
|---------|---------|-------|
| Generic key=value | `api_key=sk-abc123...` | `(?i)(?:api[_-]?key\|secret\|token\|password\|credential\|auth)\s*[=:]\s*["']?[A-Za-z0-9_\-/.+]{20,}["']?` |
| Anthropic keys | `sk-ant-api03-...` | `sk-ant-[A-Za-z0-9\-_]{20,}` |
| Generic prefixed tokens | `sk-abc123...` | `(?:sk\|pk\|rk\|ak)-[A-Za-z0-9]{20,}` |
| GitHub PATs | `ghp_xxxx...` | `ghp_[A-Za-z0-9]{36}` |
| GitHub fine-grained PATs | `github_pat_xxxx...` | `github_pat_[A-Za-z0-9_]{22,}` |
| Slack tokens | `xoxb-xxxx...` | `xoxb-[A-Za-z0-9\-]+` |
| AWS access keys | `AKIAIOSFODNN7EXAMPLE` | `AKIA[0-9A-Z]{16}` |
| Google API keys | `AIzaSyC...` | `AIza[A-Za-z0-9\-_]{35}` |
| JWTs | `eyJhbGci...` | `eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}` |
| npm tokens | `npm_xxxx...` | `npm_[A-Za-z0-9]{36}` |
| GitLab tokens | `glpat-xxxx...` | `glpat-[A-Za-z0-9\-_]{20,}` |
| DigitalOcean tokens | `dop_v1_xxxx...` | `dop_v1_[A-Za-z0-9]{64}` |
| Private tags | `<private>...</private>` | `(?is)<private>.*?</private>` |

### Integration point

Called inside `ContentStore.Index()`, right after content-type detection and before chunking. This is the single chokepoint for all indexing — `capy_index`, `capy_batch_execute`, `capy_execute` (via `intentSearch`), and `capy_fetch_and_index` all flow through `Index()`.

**Content hash is computed after stripping.** This means if a new secret pattern is added in a future version and the same content is re-indexed, the hash will differ (because more content is now redacted), triggering a re-index. This is correct behavior.

### What it does NOT do

- Does not strip secrets from content returned to the LLM context (stdout from `capy_execute`). That content is ephemeral and controlled by the user's own code. The stripping only protects the persistent FTS5 knowledge base.
- Does not block indexing — it silently redacts and continues. Users are not warned (the content is already in context from the tool call; the stripping just prevents persistence).

### Alternatives considered

**Placing in `internal/security/`:** Rejected because `internal/security/` handles command evaluation and shell-escape detection — it's about blocking actions. Secret stripping is about sanitizing data for storage. Different concerns warrant separate packages.

**Stripping at tool-handler level (in `server/`):** Rejected because there are multiple code paths that call `Index()` — `handleIndex`, `handleBatchExecute`, `handleExecute` (via `intentSearch`), `handleFetchAndIndex`. Placing the stripping in `Index()` covers all paths with a single integration point. Defense in depth.

**Stripping stdout before returning to context:** Out of scope. The user explicitly ran code that produced that output. Redacting it would break legitimate use cases (e.g., debugging auth flows). The boundary is: ephemeral context = user's responsibility, persistent storage = capy's responsibility.

---

## Feature 3: Per-Source Result Diversification

### Problem

When a large document is indexed (e.g., React docs with 200+ chunks), a broad search query can return all results from that single source, hiding relevant results from other indexed sources.

### Design

A post-RRF diversification pass caps results per source (by `SourceID`). After proximity reranking produces the final ranked list, walk the list and enforce a per-source cap:

**Pre-condition: over-fetching.** The FTS5 queries in `rrfSearch` must fetch more candidates than the final `limit` to give diversification a meaningful pool. If the initial query returns only `limit` results and a single source dominates, the second pass has nothing to backfill with except the same skipped results. `rrfSearch` uses a fetch multiplier of 5× (i.e., `fetchLimit = requestedLimit * 5`), and the final truncation to `limit` happens after diversification in `SearchWithFallback`.

1. **First pass:** Accept results in rank order until per-source cap is reached for a given label. Skip over-represented sources.
2. **Second pass:** If fewer than `limit` results were selected, accept previously-skipped results to fill remaining slots.

This ensures diversification has a diverse candidate pool and never reduces total result count below `min(limit, totalCandidates)`.

### Parameters

- **Default cap:** 2 results per source per query.
- **Exposed via:** New `MaxPerSource int` field on `SearchOptions`. Zero value means "use default (2)".
- **Not in MCP schema:** This is an internal tuning knob. Server tool handlers don't expose it.

### Where applied

In `SearchWithFallback` in `internal/store/search.go`, after `mergeRRFResults` (which deduplicates primary + fuzzy results) and before entity boosting. This placement ensures diversification sees the full candidate set from both direct and fuzzy-corrected RRF passes, and is applied exactly once (not separately inside `rrfSearch` where it would run twice and be undone by the fuzzy merge).

### Alternatives considered

**Per-source cap at the FTS5 query level:** Rejected because FTS5 has no built-in way to limit results per source in a single query. Would require multiple queries with exclusion filters, adding complexity and latency for marginal benefit. Post-processing is simpler and equally effective.

**Exposing the cap in MCP tool schema:** Rejected to keep the MCP API surface minimal. If users need per-source control, they can use the `source` filter parameter to scope to a specific source.

---

## Feature 4: Entity-Aware Query Boosting

### Problem

For queries containing specific identifiers (e.g., `ContentStore FTS5 search`) or quoted phrases (e.g., `"React useEffect" cleanup`), the current search treats every term equally. A result mentioning "ContentStore" should rank higher than one that just mentions "content" and "store" separately.

### Design

A two-step process:

**Step 1: Entity extraction** from the query string:
- **Quoted phrases:** `"React useEffect"` → extract `React useEffect`
- **Capitalized identifiers:** `ContentStore`, `FTS5`, `handleBatchExecute` → extracted via `\b[A-Z][a-zA-Z0-9_.-]+\b`
- **Sentence-starter filter:** A single capitalized word at position 0 of the query that lacks identifier patterns (no underscores, dots, or interior capitals like camelCase) is treated as a sentence starter and excluded. E.g., `"Getting started with deploy"` → no entity (just "Getting"), but `"GetConfig options"` → entity `GetConfig` (camelCase signals an identifier).
- **Stop words filtered:** `The`, `This`, `What`, `How`, `When`, `Where`, `Why`, `Who`, `Which`, `Did`, `Does`, `Do`, `Is`, `Are`, `Was`, `Were`, `Has`, `Have`, `Had`, `Can`, `Could`, `Would`, `Should`, `Will`, `May`, `Might`, `If`, `And`, `But`, `Or`, `Not`, `For`, `From`, `With`, `About`, `After`, `Before`, `Between`
- **Minimum length:** 2 characters

Extraction is independent of the search query transformation (synonym expansion, FTS5 sanitization) and can be performed once at the start of `SearchWithFallback` before any query modification.

**Step 2: Post-RRF score boost.** For each result in the RRF-ranked list, check `SearchResult.Content` for **word-boundary** matches of extracted entities (not plain substring — prevents `"DB"` from matching inside `"sandbox"`). For single-word entities, use case-sensitive matching to respect the user's casing intent. For multi-word quoted phrases, match case-insensitively. Boost formula: `FusedScore *= (1.0 + 0.3 * min(matchCount, 5))` — capped at 5 matches (max 2.5× boost) to prevent a single frequently-mentioned entity from overwhelming RRF normalization. Re-sort after boosting.

### When it activates

Only when extraction finds at least one entity. For plain lowercase queries like `database migration`, no entities are found and the boost pass is skipped — zero overhead on the common case.

### Where applied

After diversification (Feature 3) and before returning results. The ordering is: RRF fusion → proximity rerank → diversification → entity boost → final truncation.

**Interaction with diversification:** Entity boosting re-sorts results by `FusedScore`, which can reorder diversified results and push multiple results from the same source back to the top. This is intentional — entity matches are a stronger relevance signal than source diversity. When a user searches for a specific identifier like `"ContentStore FTS5"`, results that match both entities should rank highest regardless of source. Diversification remains the default ordering; entity boosting is a targeted override for queries with high-specificity terms.

### Alternatives considered

**Running entity phrases as a separate FTS5 phrase query:** e.g., `"React useEffect"` as a third RRF layer. Rejected because it adds another concurrent query, another RRF layer, and complexity for a marginal improvement. A post-hoc substring match on the already-fetched result content (typically 6-10 candidates) achieves the same effect with zero DB overhead.

**Boosting at the FTS5 query level via term repetition:** e.g., expanding `ContentStore` to `"ContentStore" OR "ContentStore" OR "ContentStore"` to artificially boost term frequency. Rejected because FTS5's BM25 implementation deduplicates repeated terms in queries — the trick doesn't work with standard SQLite.

---

## Feature 5: Retention-Scored Cleanup

### Problem

Capy's current cleanup logic (`cleanup.go`) uses a binary classification: cold tier (last accessed 30+ days ago) with zero access count = deletable. This is too blunt — it treats all cold, never-searched sources equally regardless of content type or age.

The existing conservative policy (ADR-011) is correct: only evict never-accessed sources. But the tier classification itself can be more nuanced.

### Design

Replace `classifyTier`'s flat day-threshold logic with a continuous retention score computed from existing DB columns.

**Formula:**

```
score = salience × exp(-λ × daysSinceIndexed) + σ × accessBoost
```

Where:
- **salience** = base importance by content type + access frequency bonus
  - Code sources: 0.7 base
  - Mixed (code + prose): 0.6 base
  - Prose: 0.5 base
  - Access bonus: `min(0.2, accessCount × 0.02)` — caps at 10 accesses
- **λ (lambda)** = 0.045 — temporal decay rate, calibrated so never-accessed code (salience 0.7) reaches the evictable threshold (~0.15) at ~35 days, consistent with the existing cold tier boundary
  - 7 days: decay factor 0.73
  - 30 days: 0.26
  - 60 days: 0.07
  - 90 days: 0.02
- **accessBoost** = `1 / (1 + daysSinceLastAccess)` — recent access strongly counters age decay
- **σ (sigma)** = 0.3 — weight of access boost

**Tier thresholds:**
- Hot: score >= 0.7
- Warm: score >= 0.4
- Cold: score >= 0.15
- Evictable: score < 0.15

**Score is computed at query time, not stored.** The score depends on `now`, so it changes every call. Computing from existing columns (`access_count`, `last_accessed_at`, `indexed_at`, `content_type`, `code_chunk_count`, `chunk_count`) avoids stale-score bugs and requires no schema changes.

### What changes

- `classifyTier()` → computes retention score and maps to tier
- `Cleanup()` → uses `score < 0.15 AND access_count == 0` for eviction candidates (preserving ADR-011's conservative never-accessed-only policy)
- `Cleanup()` → drops the `maxAgeDays` parameter. The retention score formula with λ=0.045 subsumes the fixed-day threshold — never-accessed code becomes evictable at ~35 days, making a separate age cutoff redundant. **This amends ADR-011:** the `maxAgeDays` mechanism is replaced by the continuous decay; the conservative eviction principle (only evict never-accessed sources) is preserved.
- `capy_cleanup` MCP tool schema → `max_age_days` parameter removed
- `Stats()` → reports tier distribution using scored classification; `StoreStats` gains an `EvictableCount` field
- `SourceInfo` → `Tier` field populated from computed score, `RetentionScore` field added for observability

### What doesn't change

- The conservative-only-evict-never-accessed policy (ADR-011) — retained as-is
- Database schema — no new columns or tables

### Alternatives considered

**Storing the score in a DB column:** Rejected because the score is time-dependent — it would require periodic recalculation jobs, and any stale score would produce incorrect tier assignments. Computing on the fly from existing columns is correct and fast (it's just arithmetic on a few columns per source, and source counts are typically in the dozens to low hundreds).

**Ebbinghaus-style spaced repetition (agentmemory's full model):** agentmemory tracks per-access timestamps and uses a sum-of-reinforcements model. Rejected as over-engineered for capy's use case — capy already stores `access_count` and `last_accessed_at` which provide sufficient signal. The formula above captures the same intuition (frequent recent access = high retention) without requiring per-access timestamp storage.

**Changing the conservative policy to evict low-scoring accessed sources:** Rejected per ADR-011's rationale. A source accessed 50 times but dormant for 60 days still has value. If aggressive cleanup is ever needed, it should be opt-in via a flag, not the default.

---

## Addendum: Post-Implementation Improvements

Discovered during Task 1 code review. These are enhancements to the core features above, not new features. To be designed and implemented after the core tasks (1–5) are complete.

### Improvement A: Synonym-Aware Proximity Reranking

**Affects:** Feature 1 (Domain Synonym Expansion) × proximity reranking system

**Problem:** `proximityRerank` operates on the raw query terms and looks for them literally in result content. When a document matches via synonym expansion (e.g., query `"k8s config"`, document contains `"kubernetes configuration"`), the proximity reranker can't find `"k8s"` in the content and skips the proximity boost. The document still ranks via RRF fusion but misses the proximity multiplier (up to ~2×).

The behavior is also inconsistent: if the original term happens to be a substring of the synonym (e.g., `"config"` inside `"configuration"`), proximity partially works via `strings.Contains`. But `"k8s"` inside `"kubernetes"` doesn't match.

**Impact:** Limited to multi-term queries where the document uses synonym forms and the original terms aren't substrings. Single-term queries skip proximity entirely. RRF fusion still provides reasonable ranking without the boost. Confirmed during isolated code review (corroborated by both code-reviewer and pal).

**Proposed fix:**

1. In `proximityRerank`, expand each raw query term via `ExpandSynonyms` to build term groups: `[["k8s", "kubernetes", "kube"], ["config", "configuration", "configuring"]]`
2. Modify `findMinSpanFromHighlights` to accept `[][]string` — match highlighted text against any term in the group
3. Modify the content fallback to find positions of all terms in each group, merge and sort position lists, then compute min span
4. Update tests for the new `[][]string` signature

**Scope:** ~50–80 lines in `search.go`, changes to `proximityRerank`, `findMinSpanFromHighlights`, and content fallback. Affects all searches (hot path), so requires careful testing.
