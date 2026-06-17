package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/store"
	"golang.org/x/sync/errgroup"
)

const maxBatchOutput = 80 * 1024 // 80 KB total output cap

// maxBatchConcurrency caps the parallel worker pool for capy_batch_execute.
const maxBatchConcurrency = 8

func (s *Server) handleBatchExecute(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()

	commands := coerceCommandsArray(args["commands"])
	queries := coerceStringArray(args["queries"])
	timeout := int(req.GetFloat("timeout", 60000))
	concurrency := int(req.GetFloat("concurrency", 1))

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

	// Clamp concurrency to [1, min(maxBatchConcurrency, len(commands))] — never
	// spawn more workers than there are commands.
	concurrency = max(1, min(concurrency, maxBatchConcurrency, len(commands)))

	// Execute commands. Serial preserves the shared-timeout-budget semantics with
	// cascading skip-on-timeout; parallel gives each command the full timeout and
	// runs them concurrently. Indexing and search downstream operate on the same
	// per-command output slice regardless of which path produced it.
	var perCommandOutputs []string
	if concurrency <= 1 {
		perCommandOutputs = executeBatchSerial(ctx, commands, timeout, s.executor)
	} else {
		perCommandOutputs = executeBatchParallel(ctx, commands, timeout, concurrency, s.executor)
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
	indexed, err := st.Index(combinedOutput, sourceLabel, "markdown", store.KindEphemeral)
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

	// Search each query against batch output
	var queryResults []string
	outputSize := 0

	for _, query := range queries {
		if outputSize > maxBatchOutput {
			queryResults = append(queryResults,
				fmt.Sprintf("## %s\n(output cap reached — use search(queries: [%q]) for details)\n", query, query))
			continue
		}

		results, searchErr := st.SearchWithFallback(query, 3, store.SearchOptions{
			Source:          sourceLabel,
			SourceMatchMode: "exact",
		})

		queryResults = append(queryResults, fmt.Sprintf("## %s", query), "")

		if searchErr != nil {
			queryResults = append(queryResults, fmt.Sprintf("(search error: %v)", searchErr), "")
		} else if len(results) > 0 {
			for _, r := range results {
				snippet := ExtractSnippet(r.Content, query, 3000, r.Highlighted)
				queryResults = append(queryResults,
					fmt.Sprintf("### %s", r.Title),
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
	out.WriteString("\n💡 To search across ALL indexed content (not just this batch), use capy_search(queries: [...])\n")
	if len(terms) > 0 {
		fmt.Fprintf(&out, "\nSearchable terms for follow-up: %s", strings.Join(terms, ", "))
	}

	return s.trackToolResponse("capy_batch_execute", textResult(out.String())), nil
}

// executeBatchSerial runs commands one at a time, sharing a single timeout
// budget across the whole batch. When the budget is exhausted, or a command
// times out, the remaining commands are recorded as skipped. This is the
// pre-concurrency behavior, preserved unchanged for concurrency <= 1.
func executeBatchSerial(ctx context.Context, commands []CommandInput, timeout int, exec *executor.PolyglotExecutor) []string {
	outputs := make([]string, 0, len(commands))
	startTime := time.Now()

	for i, cmd := range commands {
		elapsed := time.Since(startTime)
		remaining := time.Duration(timeout)*time.Millisecond - elapsed
		remainingSec := int(remaining.Seconds())
		if remainingSec <= 0 {
			for j := i; j < len(commands); j++ {
				outputs = append(outputs,
					fmt.Sprintf("# %s\n\n(skipped — batch timeout exceeded)\n", commands[j].Label))
			}
			break
		}

		result, err := exec.Execute(ctx, executor.ExecRequest{
			Language:   executor.Shell,
			Code:       cmd.Command + " 2>&1",
			TimeoutSec: remainingSec,
		})
		if err != nil {
			outputs = append(outputs,
				fmt.Sprintf("# %s\n\n(error: %v)\n", cmd.Label, err))
			continue
		}

		output := result.Stdout
		if output == "" {
			output = "(no output)"
		}
		outputs = append(outputs,
			fmt.Sprintf("# %s\n\n%s\n", cmd.Label, output))

		if result.TimedOut {
			// Mark remaining commands as skipped
			for j := i + 1; j < len(commands); j++ {
				outputs = append(outputs,
					fmt.Sprintf("# %s\n\n(skipped — batch timeout exceeded)\n", commands[j].Label))
			}
			break
		}
	}
	return outputs
}

// executeBatchParallel runs commands concurrently with a bounded worker pool.
// Each command gets the full timeout — wall-clock is bounded by timeout, not
// timeout×N, because commands run in parallel. Results are written to a
// pre-sized, index-keyed slice so output order matches input order, and a
// failing or timed-out command never affects its siblings. The executor is
// safe for concurrent use (sync.Once detection, per-call temp dirs, a mutex
// over background PIDs).
func executeBatchParallel(ctx context.Context, commands []CommandInput, timeout, concurrency int, exec *executor.PolyglotExecutor) []string {
	results := make([]string, len(commands))
	// Each command gets the full timeout. Guard the sub-second case: truncating
	// ms→s would yield 0, which the executor reads as "no timeout → 30s default",
	// silently extending a tight batch budget. The serial path avoids this via
	// its remainingSec <= 0 skip guard.
	timeoutSec := int((time.Duration(timeout) * time.Millisecond).Seconds())
	if timeoutSec <= 0 && timeout > 0 {
		timeoutSec = 1
	}

	var g errgroup.Group
	g.SetLimit(concurrency)

	for i, cmd := range commands {
		g.Go(func() error {
			result, err := exec.Execute(ctx, executor.ExecRequest{
				Language:   executor.Shell,
				Code:       cmd.Command + " 2>&1",
				TimeoutSec: timeoutSec,
			})
			if err != nil {
				results[i] = fmt.Sprintf("# %s\n\n(error: %v)\n", cmd.Label, err)
				return nil
			}

			output := result.Stdout
			if output == "" {
				output = "(no output)"
			}
			if result.TimedOut {
				output += "\n(timed out)"
			}
			results[i] = fmt.Sprintf("# %s\n\n%s\n", cmd.Label, output)
			return nil
		})
	}

	// Each goroutine handles its own error in-slot and returns nil, so Wait
	// never reports an error — it only blocks until all workers finish. If a
	// future edit makes the closure return a non-nil error, this discard must be
	// revisited (and sibling-cancellation semantics reconsidered).
	_ = g.Wait()
	return results
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
