# ADR-003: Markdown detection false positive on shell scripts

**Status:** Accepted (known limitation)
**Date:** 2026-03-20

## Context

`DetectContentType` in `detect.go` checks for markdown indicators including `strings.HasPrefix(trimmed, "#")`. This matches shell comments (`#!/bin/bash`, `# comment`). Combined with one other indicator (e.g., a URL containing `](`), a shell script could be misclassified as markdown.

## Decision

Keep the current heuristic. The threshold of 2+ indicators in the first 50 lines provides sufficient accuracy for the target use case.

## Rationale

- The function requires **two** independent indicators to classify as markdown. A shell script would need both `#` comments and either code fences or markdown-style links — an unlikely combination.
- Even when misclassified, markdown chunking produces usable results on non-markdown content. It splits by headings (which won't match shell comments since they require `# ` with a space after `#` in the heading regex), falling back to a single "Content" chunk.
- Adding shell-script detection (e.g., checking for `#!/`) would add complexity for a marginal improvement. The caller can always pass an explicit content type to bypass auto-detection.

## Consequences

Shell scripts with comments and markdown-like link patterns may be chunked as markdown. The impact is cosmetic (different chunk titles), not functional (content is preserved and searchable).
