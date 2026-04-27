# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

`capy` is an MCP (Model Context Protocol) server and Claude Code plugin that solves two main problems: context window flooding and persistent queryable memory. Capy aims to achieve ~98% context reduction (315 KB to 5.4 KB) by keeping raw tool outputs in isolated subprocesses and indexing them into SQLite FTS5 with BM25 ranking. Large command outputs, log files, API responses, and documentation never enter the context window - only concise summaries and search results do.

It's a port of `context-mode` project written in Go, aiming to utilize Go's features to achieve even better context-reduction performance.

## Architecture Overview

`capy` operates as a Claude Code plugin that intercepts data-heavy tool calls (Bash, Read, WebFetch, Grep) and redirects them through sandboxed execution. Raw data stays in subprocesses; only printed summaries enter the LLM context. A persistent FTS5 knowledge base indexes all sandboxed output for on-demand retrieval via BM25-ranked search with three-tier fallback (Porter stemming, trigram substring, fuzzy Levenshtein correction).

# Extra Instructions

@.claude/CLAUDE.extra.md

# capy — MANDATORY routing rules

@.capy/AGENTS.md
