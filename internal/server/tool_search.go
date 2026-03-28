package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
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
	var sections []string
	totalSize := 0

	for _, q := range queryList {
		if totalSize > searchMaxTotalBytes {
			sections = append(sections, fmt.Sprintf("## %s\n(output cap reached)\n", q))
			continue
		}

		results, err := st.SearchWithFallback(q, effectiveLimit, source)
		if err != nil {
			results = nil
		}

		if len(results) == 0 {
			sections = append(sections, fmt.Sprintf("## %s\nNo results found.", q))
			continue
		}

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

	if strings.TrimSpace(output) == "" {
		sources, _ := st.ListSources()
		if len(sources) > 0 {
			var parts []string
			for _, src := range sources {
				parts = append(parts, fmt.Sprintf("%q (%d sections)", src.Label, src.ChunkCount))
			}
			output = "No results found.\nIndexed sources: " + strings.Join(parts, ", ")
		} else {
			output = "No results found."
		}
	}

	return s.trackToolResponse("capy_search", textResult(output)), nil
}
