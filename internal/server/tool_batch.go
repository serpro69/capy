package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/store"
)

const maxBatchOutput = 80 * 1024 // 80 KB total output cap

func (s *Server) handleBatchExecute(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()

	commands := coerceCommandsArray(args["commands"])
	queries := coerceStringArray(args["queries"])
	timeout := int(req.GetFloat("timeout", 60000))

	if len(commands) == 0 {
		return errorResult("missing required parameter: commands"), nil
	}
	if len(queries) == 0 {
		return errorResult("missing required parameter: queries"), nil
	}

	// Security: check each command individually
	for _, cmd := range commands {
		if denied := s.checkDenyPolicy(cmd.Command); denied != nil {
			return denied, nil
		}
	}

	// Execute each command separately with budget management
	var perCommandOutputs []string
	startTime := time.Now()

	for i, cmd := range commands {
		elapsed := time.Since(startTime)
		remaining := time.Duration(timeout)*time.Millisecond - elapsed
		remainingSec := int(remaining.Seconds())
		if remainingSec <= 0 {
			for j := i; j < len(commands); j++ {
				perCommandOutputs = append(perCommandOutputs,
					fmt.Sprintf("# %s\n\n(skipped — batch timeout exceeded)\n", commands[j].Label))
			}
			break
		}

		result, err := s.executor.Execute(ctx, executor.ExecRequest{
			Language:   executor.Shell,
			Code:       cmd.Command + " 2>&1",
			TimeoutSec: remainingSec,
		})
		if err != nil {
			perCommandOutputs = append(perCommandOutputs,
				fmt.Sprintf("# %s\n\n(error: %v)\n", cmd.Label, err))
			continue
		}

		output := result.Stdout
		if output == "" {
			output = "(no output)"
		}
		perCommandOutputs = append(perCommandOutputs,
			fmt.Sprintf("# %s\n\n%s\n", cmd.Label, output))

		if result.TimedOut {
			// Mark remaining commands as skipped
			for j := i + 1; j < len(commands); j++ {
				perCommandOutputs = append(perCommandOutputs,
					fmt.Sprintf("# %s\n\n(skipped — batch timeout exceeded)\n", commands[j].Label))
			}
			break
		}
	}

	combinedOutput := strings.Join(perCommandOutputs, "\n")
	totalBytes := len(combinedOutput)
	totalLines := strings.Count(combinedOutput, "\n") + 1

	// Track sandboxed bytes
	s.stats.AddBytesSandboxed(int64(totalBytes))
	s.stats.AddBytesIndexed(int64(totalBytes))

	// Index combined output as markdown
	st := s.getStore()
	sourceLabel := "batch:" + truncateLabel(commands)
	indexed, err := st.Index(combinedOutput, sourceLabel, "markdown")
	if err != nil {
		return s.trackToolResponse("capy_batch_execute",
			errorResult(fmt.Sprintf("indexing error: %v", err))), nil
	}

	// Build section inventory
	allSections, secErr := st.GetChunksBySource(indexed.SourceID)
	var inventory []string
	inventory = append(inventory, "## Indexed Sections", "")
	if secErr != nil {
		inventory = append(inventory, fmt.Sprintf("- (inventory unavailable: %v)", secErr))
	} else {
		for _, sec := range allSections {
			kb := fmt.Sprintf("%.1f", float64(len(sec.Content))/1024)
			inventory = append(inventory, fmt.Sprintf("- %s (%sKB)", sec.Title, kb))
		}
	}

	// Search each query with three-tier fallback
	var queryResults []string
	outputSize := 0

	for _, query := range queries {
		if outputSize > maxBatchOutput {
			queryResults = append(queryResults,
				fmt.Sprintf("## %s\n(output cap reached — use search(queries: [%q]) for details)\n", query, query))
			continue
		}

		// Tier 1: scoped search
		results, searchErr := st.SearchWithFallback(query, 3, store.SearchOptions{Source: sourceLabel})
		crossSource := false

		// Tier 2: global fallback
		if len(results) == 0 && searchErr == nil {
			results, searchErr = st.SearchWithFallback(query, 3, store.SearchOptions{})
			crossSource = len(results) > 0
		}

		queryResults = append(queryResults, fmt.Sprintf("## %s", query))
		if crossSource {
			queryResults = append(queryResults,
				"> **Note:** No results in current batch output. Showing results from previously indexed content.")
		}
		queryResults = append(queryResults, "")

		if searchErr != nil {
			queryResults = append(queryResults, fmt.Sprintf("(search error: %v)", searchErr), "")
		} else if len(results) > 0 {
			for _, r := range results {
				snippet := ExtractSnippet(r.Content, query, 3000, r.Highlighted)
				sourceTag := ""
				if crossSource {
					sourceTag = fmt.Sprintf(" _(source: %s)_", r.Label)
				}
				queryResults = append(queryResults,
					fmt.Sprintf("### %s%s", r.Title, sourceTag),
					snippet, "")
				outputSize += len(snippet) + len(r.Title)
			}
		} else {
			queryResults = append(queryResults, "No matching sections found.", "")
		}
	}

	// Distinctive terms
	terms, _ := st.GetDistinctiveTerms(indexed.SourceID, 40)

	// Build final output
	var out strings.Builder
	fmt.Fprintf(&out, "Executed %d commands (%d lines, %.1fKB). Indexed %d sections. Searched %d queries.\n\n",
		len(commands), totalLines, float64(totalBytes)/1024, indexed.TotalChunks, len(queries))
	out.WriteString(strings.Join(inventory, "\n"))
	out.WriteString("\n\n")
	out.WriteString(strings.Join(queryResults, "\n"))
	if len(terms) > 0 {
		fmt.Fprintf(&out, "\nSearchable terms for follow-up: %s", strings.Join(terms, ", "))
	}

	return s.trackToolResponse("capy_batch_execute", textResult(out.String())), nil
}

// truncateLabel builds a source label from command labels, truncated to 80 chars.
func truncateLabel(commands []CommandInput) string {
	var labels []string
	for _, c := range commands {
		labels = append(labels, c.Label)
	}
	joined := strings.Join(labels, ",")
	if len(joined) > 80 {
		joined = joined[:80]
	}
	return joined
}
