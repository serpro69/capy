# Design: Fix over-preservation and over-routing (Issue #44)

**Issue:** [#44](https://github.com/serpro69/capy/issues/44)
**ADR:** [023-fetch-ephemeral-default-and-routing-rewrite](../../adr/023-fetch-ephemeral-default-and-routing-rewrite.md)
**Status:** Design complete

## Overview

Two coordinated fixes addressing capy over-preserving task artifacts in the knowledge DB and agents over-routing operations through capy where it hurts quality.

**Problem A (storage):** `capy_fetch_and_index` hardcodes `KindDurable`. Transient task artifacts (GitHub issues, PRs, investigation pages) become permanent DB residents, causing bloat and BM25 pollution.

**Problem B (routing):** AGENTS.md and tool descriptions push agents to route all commands through capy — including git diffs for code review (destroying sequential comprehension via BM25 fragmentation) and small commands (pure overhead for <50 lines of output).

**Problem C (control-plane noise):** The search handler's no-results fallback dumps an unbounded source listing — context pollution from capy's own output.

## Design Decisions

All decisions documented in [ADR-023](../../adr/023-fetch-ephemeral-default-and-routing-rewrite.md). Summary:

| Decision | Choice | Rationale |
|----------|--------|-----------|
| fetch_and_index default kind | `ephemeral` (opt-in `durable`) | Most fetched URLs are task-scoped; pre-release allows breaking change |
| capy_index kind | Stays hardcoded `durable` | Explicit indexing = explicit persist intent |
| Promote tool | No — implicit re-index promotion | Existing store path works; no new API surface |
| Search fallback listing | Summary-only (count line) | Eliminate unbounded source dump |
| AGENTS.md | Full rewrite — task-aware routing | "Comprehension vs extraction" principle replaces "capy-first" bias |
| Tool descriptions | Soften mandatory/primary language | Remove git from capy_execute examples; reframe batch_execute |
| Search default kinds | Keep {durable, session} | ADR-017 rationale preserved; source: filter handles fetch-then-search |
