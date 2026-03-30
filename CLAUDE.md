# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

`capy` is an MCP (Model Context Protocol) server and Claude Code plugin that solves context window flooding. Aims to achieve ~98% context reduction (315 KB to 5.4 KB) by keeping raw tool outputs in isolated subprocesses and indexing them into SQLite FTS5 with BM25 ranking. Large command outputs, log files, API responses, and documentation never enter the context window - only concise summaries and search results do.

It's a port of `context-mode` project written in Go, aiming to utilize Go's features to achieve even better context-reduction performance.

## Architecture Overview

`capy` operates as a Claude Code plugin that intercepts data-heavy tool calls (Bash, Read, WebFetch, Grep) and redirects them through sandboxed execution. Raw data stays in subprocesses; only printed summaries enter the LLM context. A persistent FTS5 knowledge base indexes all sandboxed output for on-demand retrieval via BM25-ranked search with three-tier fallback (Porter stemming, trigram substring, fuzzy Levenshtein correction).

# Extra Instructions

@.claude/CLAUDE.extra.md

# capy — MANDATORY routing rules

You have capy MCP tools available. These rules are NOT optional — they protect your context window from flooding. A single unrouted command can dump 56 KB into context and waste the entire session.

## BLOCKED commands — do NOT attempt these

### curl / wget — BLOCKED
Any Bash command containing `curl` or `wget` is intercepted and replaced with an error message. Do NOT retry.
Instead use:
- `capy_fetch_and_index(url, source)` to fetch and index web pages
- `capy_execute(language: "javascript", code: "const r = await fetch(...)")` to run HTTP calls in sandbox

### Inline HTTP — BLOCKED
Any Bash command containing `fetch('http`, `requests.get(`, `requests.post(`, `http.get(`, or `http.request(` is intercepted and replaced with an error message. Do NOT retry with Bash.
Instead use:
- `capy_execute(language, code)` to run HTTP calls in sandbox — only stdout enters context

### WebFetch — BLOCKED
WebFetch calls are denied entirely. The URL is extracted and you are told to use `capy_fetch_and_index` instead.
Instead use:
- `capy_fetch_and_index(url, source)` then `capy_search(queries)` to query the indexed content

## REDIRECTED tools — use sandbox equivalents

### Bash (>20 lines output)
Bash is ONLY for: `git`, `mkdir`, `rm`, `mv`, `cd`, `ls`, `npm install`, `pip install`, and other short-output commands.
For everything else, use:
- `capy_batch_execute(commands, queries)` — run multiple commands + search in ONE call
- `capy_execute(language: "shell", code: "...")` — run in sandbox, only stdout enters context

### Read (for analysis)
If you are reading a file to **Edit** it → Read is correct (Edit needs content in context).
If you are reading to **analyze, explore, or summarize** → use `capy_execute_file(path, language, code)` instead. Only your printed summary enters context. The raw file content stays in the sandbox.

### Grep (large results)
Grep results can flood context. Use `capy_execute(language: "shell", code: "grep ...")` to run searches in sandbox. Only your printed summary enters context.

## Tool selection hierarchy

1. **GATHER**: `capy_batch_execute(commands, queries)` — Primary tool. Runs all commands, auto-indexes output, returns search results. ONE call replaces 30+ individual calls.
2. **FOLLOW-UP**: `capy_search(queries: ["q1", "q2", ...])` — Query indexed content. Pass ALL questions as array in ONE call.
3. **PROCESSING**: `capy_execute(language, code)` | `capy_execute_file(path, language, code)` — Sandbox execution. Only stdout enters context.
4. **WEB**: `capy_fetch_and_index(url, source)` then `capy_search(queries)` — Fetch, chunk, index, query. Raw HTML never enters context.
5. **INDEX**: `capy_index(content, source)` — Store content in FTS5 knowledge base for later search.

## Subagent routing

When spawning subagents (Agent/Task tool), the routing block is automatically injected into their prompt. Bash-type subagents are upgraded to general-purpose so they have access to MCP tools. You do NOT need to manually instruct subagents about capy.

## Output constraints

- Keep responses under 500 words.
- Write artifacts (code, configs, PRDs) to FILES — never return them as inline text. Return only: file path + 1-line description.
- When indexing content, use descriptive source labels so others can `capy_search(source: "label")` later.

## capy commands

| Command | Action |
|---------|--------|
| `capy stats` | Call the `capy_stats` MCP tool and display the full output verbatim |
| `capy doctor` | Call the `capy_doctor` MCP tool and display as checklist |
