# Benchmark Comparison

How capy compares against other persistent memory and context management solutions for AI coding agents.

capy solves two problems simultaneously: **context window reduction** (keeping raw tool output out of the LLM context) and **persistent agent memory** (searchable knowledge base across sessions). Most tools address one or the other. We compare against both categories.

Numbers come from our own benchmarks or published results from primary sources. We link to sources wherever possible so you can reproduce. Where benchmarks aren't directly comparable, we say so.

---

## Retrieval Quality

**No direct comparison exists yet.** Every system below was measured on a different benchmark with different corpus sizes and domains. We show the numbers side by side so you can see the landscape, but comparing across rows is misleading — read the caveats.

capy uses pure lexical search: SQLite FTS5 with BM25 ranking, two-layer RRF (Porter stemming + trigram substring), fuzzy Levenshtein correction, and post-processing. No embeddings, no vector DB, no API key.

| System | Search Approach | R@5 | NDCG@10 | MRR | Benchmark | Cases |
|---|---|---|---|---|---|---|
| capy (BM25 + trigram + fuzzy) | Lexical (FTS5/BM25) | 98.7% | 0.952 | 0.941 | Internal NIAH (synthetic) | 156 |
| agentmemory (BM25 + Vector) | Hybrid (BM25 + embeddings) | 95.2% | 87.9% | 88.2% | LongMemEval-S (academic) | 500 |
| agentmemory (BM25-only) | Lexical (BM25) | 86.2% | 73.0% | 71.5% | LongMemEval-S (academic) | 500 |
| MemPalace | Vector-only | ~96.6% | — | — | LongMemEval-S (academic) | 500 |
| Letta / MemGPT | Extraction-based | — | — | — | LoCoMo (different benchmark) | — |
| Mem0 | Extraction-based | — | — | — | LoCoMo (different benchmark) | — |

### Why you shouldn't compare these numbers directly

- **capy's benchmark is internal and small.** 156 synthetic test cases designed around coding-agent content (docs, API responses, logs, transcripts, ADRs). Smaller corpora are easier to search — there are fewer distractors competing for top-K slots. capy's 98.7% R@5 on 156 cases would almost certainly drop on a larger, more diverse corpus.
- **agentmemory and MemPalace** use [LongMemEval-S](https://arxiv.org/abs/2410.10813) (ICLR 2025) — 500 questions across ~48 sessions per question, ~115K tokens. This is an academic benchmark for long-term conversational memory, which is harder and more diverse than capy's synthetic fixtures.
- **Letta and Mem0** publish on [LoCoMo](https://snap-stanford.github.io/LoCoMo/), yet another benchmark.
- **The only fair comparison would be running all systems on the same dataset.** We plan to run capy on LongMemEval-S. If you want to help, open an issue.

---

## Context Reduction

This is where most memory tools have a gap. Persistent memory helps the LLM recall past information, but it doesn't address the core problem: tool output flooding the context window *right now*.

| System | Context Reduction | How |
|---|---|---|
| **capy** | Yes — active | Sandboxed execution. Raw output never enters context. Intent-search returns ~300 bytes from 50 KB input. Auto-indexes above 5 KB threshold. |
| context-mode | Yes — active | Same architecture (capy's predecessor). Sandboxed execution with FTS5 search. No benchmark suite published. |
| agentmemory | No | Memory retrieval only — injected memories add to context, they don't reduce tool output. |
| Mem0 | No | Memory retrieval only. |
| Letta / MemGPT | Partial | Virtual context management via memory editing, but not sandbox-based output reduction. |
| Claude's built-in memory | No | Stores facts across conversations. No tool output interception. |

### capy's measured context reduction

**Byte-level compression** (5000-byte threshold test, uniform synthetic input):

| Output Size | Context After capy | Compression |
|---|---|---|
| 5 KB (below threshold) | 5 KB (passthrough) | 0% |
| 5,001 bytes | 316 bytes | 93.7% |
| 10 KB | 318 bytes | 96.8% |
| 50 KB | 319 bytes | 99.4% |

These numbers look great but they're measured on uniform synthetic data (repeated lines). Real-world content with diverse terms produces larger summaries.

**Information-preserving compression** (NIAH benchmark, 156 cases across realistic content):

| Metric | Overall |
|---|---|
| Compression Ratio | 50.5% |
| Context Recall (needles preserved) | 0.983 |
| Perfect Recall Rate | 97.1% |
| Effective Compression | 50.4% |

On realistic content, capy achieves ~50% compression while preserving ~98% of specific facts. 97% of cases preserve all needles. The "~98% reduction" headline applies to raw byte savings on large uniform outputs — on diverse content with the information-preservation constraint, ~50% effective compression is the real number.

Full methodology: [RESULTS.md](RESULTS.md)

---

## Persistent Memory

| Feature | capy | context-mode | agentmemory | Mem0 | Letta/MemGPT |
|---|---|---|---|---|---|
| **Search engine** | SQLite FTS5 (BM25) | SQLite FTS5 (BM25) | BM25 + vector | Vector | Vector + graph |
| **Embedding model required** | No | No | Optional (local default) | Yes (API) | Yes |
| **API key required** | No | No | No (local embeddings) | Yes | Yes |
| **Source kinds** | 3 (durable, ephemeral, session) | 1 | 1 | — | — |
| **Session transcript indexing** | Yes (JSONL parse + chunk) | No | No | No | Yes (via memory editing) |
| **Retention scoring** | Yes (salience × decay + recency) | No | No | No | No |
| **Content deduplication** | SHA-256 hash, skip unchanged | No | No | — | — |
| **Encryption at rest** | Mandatory (sqlite3mc) | No | No | Depends on backend | Depends on backend |
| **Cross-session persistence** | Yes (per-project SQLite) | Partial | Yes | Yes (cloud) | Yes (cloud/local) |

---

## Architecture

| | capy | context-mode | agentmemory | Mem0 |
|---|---|---|---|---|
| **Language** | Go | TypeScript | TypeScript | Python |
| **Install** | Single binary | `npm install` + Node.js | `npm install` | `pip install` + API key |
| **Runtime deps** | None | Node.js | Node.js | Python + cloud services |
| **Startup** | Native binary, milliseconds | Node.js VM boot | Node.js | Python + network |
| **Memory baseline** | ~10–20 MB | ~50–80 MB | ~30–50 MB | Varies |
| **Storage** | Local SQLite | Local SQLite | Local SQLite | Cloud or local |
| **Privacy** | Fully local, encrypted | Fully local | Fully local | Cloud by default |
| **Platform** | MCP server + Claude Code hooks | MCP server | MCP server | SDK/API |

---

## What capy Does NOT Do

Honest gaps:

- **No semantic/vector search.** capy uses pure lexical search. Queries that require understanding meaning rather than matching words will miss. Example: searching for "authentication" won't find content that only says "login" — unless the fuzzy corrector bridges the gap.
- **No LLM-based memory extraction.** capy indexes raw content and relies on BM25 to find relevant sections. It doesn't use an LLM to extract and summarize facts before storing them (like Mem0 does). This is a deliberate trade-off: no LLM dependency, no API cost, deterministic behavior, but less "intelligent" memory organization.
- **No graph relationships.** Memories are flat documents with source labels, not a knowledge graph. capy can't answer "what did I work on last Tuesday" unless those words appear in the content.
- **No cloud sync.** Everything is local. If you want the same knowledge base on two machines, you sync the encrypted SQLite file yourself.

---

## Corrections Welcome

If you maintain one of these tools and we got something wrong, please open an issue or PR. We'd rather have accurate numbers than convenient ones.

If you want to add your tool to this comparison, open a PR with:

1. A link to your benchmark methodology
2. The metric and dataset you're measuring on
3. A commit hash or version so we can reproduce

**Sources:**

- capy benchmark suite: [benchmark/RESULTS.md](RESULTS.md)
- agentmemory LongMemEval: [benchmark/LONGMEMEVAL.md](https://github.com/rohitg00/agentmemory/blob/main/benchmark/LONGMEMEVAL.md)
- agentmemory comparison: [benchmark/COMPARISON.md](https://github.com/rohitg00/agentmemory/blob/main/benchmark/COMPARISON.md)
- LongMemEval paper: [arxiv.org/abs/2410.10813](https://arxiv.org/abs/2410.10813)
- LoCoMo paper: [snap-stanford.github.io/LoCoMo](https://snap-stanford.github.io/LoCoMo/)
- context-mode: [github.com/mksglu/context-mode](https://github.com/mksglu/context-mode)
