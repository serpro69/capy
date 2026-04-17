package server

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/store"
)

const (
	searchWindowDuration   = 60 * time.Second
	searchMaxResultsAfter  = 3 // after 3 calls: 1 result per query
	searchBlockAfter       = 8 // after 8 calls: refuse
	searchMaxTotalBytes    = 40 * 1024
	searchSnippetMaxLen    = 1500
)

func (s *Server) handleSearch(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()

	// Normalize: accept both queries (array) and query (string)
	var queryList []string
	if raw, ok := args["queries"]; ok {
		queryList = coerceStringArray(raw)
	}
	if len(queryList) == 0 {
		if q := req.GetString("query", ""); q != "" {
			queryList = []string{q}
		}
	}
	if len(queryList) == 0 {
		return errorResult("Error: provide query or queries."), nil
	}

	limit := int(req.GetFloat("limit", 3))
	source := req.GetString("source", "")

	includeKinds, err := parseIncludeKinds(args["include_kinds"])
	if err != nil {
		return errorResult("Error: " + err.Error()), nil
	}

	// Progressive throttling (atomic check+reset+increment)
	callNum, windowAge := s.throttle.advance(searchWindowDuration)

	if callNum > searchBlockAfter {
		text := fmt.Sprintf(
			"BLOCKED: %d search calls in %ds. You're flooding context. STOP making individual search calls. Use batch_execute(commands, queries) for your next research step.",
			callNum, int(windowAge.Seconds()),
		)
		return s.trackToolResponse("capy_search", errorResult(text)), nil
	}

	// Determine effective limit based on throttle level
	effectiveLimit := min(limit, 2)
	if callNum > searchMaxResultsAfter {
		effectiveLimit = 1
	}

	st := s.getStore()

	// Early return when knowledge base is empty — guide the user to indexing tools
	kbStats, err := st.Stats()
	if err == nil && kbStats.SourceCount == 0 {
		return s.trackToolResponse("capy_search", &mcp.CallToolResult{
			Content: []mcp.Content{mcp.NewTextContent(
				"The knowledge base is empty — nothing has been indexed yet.\n\n" +
					"To populate it, use:\n" +
					"  • capy_batch_execute(commands, queries) — run commands, auto-index output, and search in one call\n" +
					"  • capy_fetch_and_index(url) — fetch a URL, index it, then search with capy_search\n" +
					"  • capy_index(content, source) — manually index text content\n\n" +
					"After indexing, capy_search becomes available for follow-up queries.",
			)},
			IsError: true,
		}), nil
	}

	var sections []string
	totalSize := 0
	hasResults := false

	// Lazily fetched once if any query returns zero results while ephemeral is excluded.
	// Cached across queries in this request; a concurrent write could shift the count
	// by ±1 — acceptable, the user-facing number is directional, not authoritative.
	// -1 = not yet checked.
	ephemeralIncluded := includesEphemeral(source, includeKinds)
	ephemeralExcluded := !ephemeralIncluded
	ephemeralCount := -1

	for _, q := range queryList {
		if totalSize > searchMaxTotalBytes {
			sections = append(sections, fmt.Sprintf("## %s\n(output cap reached)\n", q))
			continue
		}

		results, err := st.SearchWithFallback(q, effectiveLimit, store.SearchOptions{
			Source:       source,
			IncludeKinds: includeKinds,
		})
		if err != nil {
			results = nil
		}

		if len(results) == 0 {
			noResults := fmt.Sprintf("## %s\nNo results found.", q)
			if ephemeralExcluded {
				if ephemeralCount < 0 {
					if n, cErr := st.CountSourcesByKind(store.KindEphemeral); cErr == nil {
						ephemeralCount = n
					} else {
						ephemeralCount = 0
					}
				}
				if ephemeralCount > 0 {
					noResults += fmt.Sprintf(
						"\n\n%d ephemeral source(s) present but excluded by default. To include command output from capy_execute / capy_execute_file / capy_batch_execute, retry with:\n"+
							"  • include_kinds: [\"durable\",\"ephemeral\"]  (search across both kinds), or\n"+
							"  • source: \"execute:<lang>\" / \"file:<path>\" / \"batch:…\"  (explicit-source override; e.g., source: \"execute:shell\")",
						ephemeralCount,
					)
				}
			}
			sections = append(sections, noResults)
			continue
		}

		hasResults = true
		var formatted []string
		for _, r := range results {
			header := fmt.Sprintf("--- [%s] ---", r.Label)
			heading := fmt.Sprintf("### %s", r.Title)
			snippet := ExtractSnippet(r.Content, q, searchSnippetMaxLen, r.Highlighted)
			formatted = append(formatted, fmt.Sprintf("%s\n%s\n\n%s", header, heading, snippet))
		}

		block := strings.Join(formatted, "\n\n")
		sections = append(sections, fmt.Sprintf("## %s\n\n%s", q, block))
		totalSize += len(block)
	}

	output := strings.Join(sections, "\n\n---\n\n")

	// Add throttle warning
	if callNum >= searchMaxResultsAfter {
		output += fmt.Sprintf(
			"\n\n⚠ search call #%d/%d in this window. Results limited to %d/query. Batch queries: search(queries: [\"q1\",\"q2\",\"q3\"]) or use batch_execute.",
			callNum, searchBlockAfter, effectiveLimit,
		)
	}

	// When no query produced any results, append the list of indexed sources
	// to help the caller pick a better query. Preserves per-query messages
	// (including the ephemeral-recovery hint) by appending rather than overwriting.
	// Ephemeral labels are filtered out unless the caller opted into ephemeral,
	// to keep the listing consistent with the default-exclude-ephemeral contract.
	if !hasResults {
		sources, _ := st.ListSources()
		var parts []string
		for _, src := range sources {
			if !ephemeralIncluded && src.Kind == store.KindEphemeral {
				continue
			}
			parts = append(parts, fmt.Sprintf("%q (%d sections)", src.Label, src.ChunkCount))
		}
		if len(parts) > 0 {
			output += "\n\nIndexed sources: " + strings.Join(parts, ", ")
		}
	}

	return s.trackToolResponse("capy_search", textResult(output)), nil
}

// parseIncludeKinds normalizes the include_kinds argument to a typed slice.
// Returns (nil, nil) when absent or empty so the store layer applies the default.
func parseIncludeKinds(raw any) ([]store.SourceKind, error) {
	if raw == nil {
		return nil, nil
	}
	values := coerceStringArray(raw)
	if len(values) == 0 {
		return nil, nil
	}
	out := make([]store.SourceKind, 0, len(values))
	seen := make(map[store.SourceKind]bool, len(values))
	for _, v := range values {
		k := store.SourceKind(v)
		if !k.Valid() {
			return nil, fmt.Errorf("invalid include_kinds value %q (accepted: \"durable\", \"ephemeral\")", v)
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out, nil
}

// includesEphemeral reports whether the resolved kind filter would include ephemeral sources.
func includesEphemeral(source string, kinds []store.SourceKind) bool {
	if source != "" {
		// Explicit source override — kind filter is bypassed entirely.
		return true
	}
	if len(kinds) == 0 {
		return false
	}
	return slices.Contains(kinds, store.KindEphemeral)
}
