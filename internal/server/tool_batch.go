package server

import (
	"context"
	"encoding/json"
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

func (s *Server) handleBatchExecute(ctx context.Context, req mcp.CallToolRequest) (response *mcp.CallToolResult, err error) {
	// Bound every exit, including validation/policy errors. Preserve the existing
	// stats contract: only admitted calls count as tool responses.
	trackResponse := false
	defer func() {
		if response != nil {
			response = boundBatchResult(response)
			if trackResponse {
				response = s.trackToolResponse("capy_batch_execute", response)
			}
		}
	}()
	args := req.GetArguments()
	commands := coerceCommandsArray(args["commands"])
	queries, scope, err := batchSearchArgs(args)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	timeout := int(req.GetFloat("timeout", 60000))
	concurrency := int(req.GetFloat("concurrency", 1))
	if len(commands) == 0 {
		return errorResult("missing required parameter: commands"), nil
	}
	cwd, err := s.executionCwd(args)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	for _, cmd := range commands {
		if denied := s.checkDenyPolicy(cmd.Command); denied != nil {
			return denied, nil
		}
	}
	trackResponse = true
	concurrency = max(1, min(concurrency, maxBatchConcurrency, len(commands)))
	var outputs []batchCommandOutput
	if concurrency <= 1 {
		outputs = executeBatchSerial(ctx, commands, timeout, s.executor, cwd)
	} else {
		outputs = executeBatchParallel(ctx, commands, timeout, concurrency, s.executor, cwd)
	}
	var combined strings.Builder
	var rawBytes, skipped int
	for i, output := range outputs {
		if i > 0 {
			combined.WriteByte('\n')
		}
		combined.WriteString(output.section)
		rawBytes += output.rawBytes
		if output.skipped {
			skipped++
		}
	}
	s.stats.AddBytesSandboxed(int64(rawBytes))
	// This counts the complete payload offered to indexing, including provenance,
	// even on deduplication or admission failure, like other indexing tools.
	s.stats.AddBytesIndexed(int64(combined.Len()))
	st := s.getStore()
	sourceLabel := "batch:" + truncateLabel(commands)
	indexed, err := st.Index(combined.String(), sourceLabel, "markdown", store.KindEphemeral)
	if err != nil {
		return errorResult(fmt.Sprintf("indexing error: %v", err)), nil
	}
	return s.batchResponse(ctx, commands, outputs, queries, scope, sourceLabel, indexed, rawBytes, skipped), nil
}

// batchSearchArgs distinguishes omitted/empty queries from malformed input.
// Serialized arrays remain accepted, but null and non-string members never
// silently turn a search request into an indexing-only operation.
func batchSearchArgs(args map[string]any) ([]string, string, error) {
	scope := "batch"
	if raw, present := args["query_scope"]; present {
		var ok bool
		scope, ok = raw.(string)
		if !ok || (scope != "batch" && scope != "global") {
			return nil, "", fmt.Errorf("invalid parameter %q: expected batch or global", "query_scope")
		}
	}
	raw, present := args["queries"]
	if !present {
		return nil, scope, nil
	}
	invalid := fmt.Errorf("invalid parameter %q: expected an array of strings", "queries")
	if encoded, ok := raw.(string); ok {
		if err := json.Unmarshal([]byte(encoded), &raw); err != nil {
			return nil, "", invalid
		}
	}
	switch values := raw.(type) {
	case []string:
		return values, scope, nil
	case []any:
		queries := make([]string, len(values))
		for i, value := range values {
			query, ok := value.(string)
			if !ok {
				return nil, "", invalid
			}
			queries[i] = query
		}
		return queries, scope, nil
	default:
		return nil, "", invalid
	}
}

type batchCommandOutput struct {
	section  string
	rawBytes int
	skipped  bool
}

func batchOutput(cmd CommandInput, output string, rawBytes int, skipped bool) batchCommandOutput {
	state := "Command"
	if skipped {
		state = "Command (not executed)"
	}
	return batchCommandOutput{
		section: fmt.Sprintf("# %s\n\n%s: %s\n\n%s\n",
			batchDisplay(cmd.Label, 160), state, batchDisplay(cmd.Command, 500), output),
		rawBytes: rawBytes,
		skipped:  skipped,
	}
}

// executeBatchSerial runs commands one at a time, sharing a single timeout
// budget across the whole batch. When the budget is exhausted, or a command
// times out, the remaining commands are recorded as skipped. This is the
// pre-concurrency behavior, preserved unchanged for concurrency <= 1.
func executeBatchSerial(ctx context.Context, commands []CommandInput, timeout int, exec *executor.PolyglotExecutor, cwd string) []batchCommandOutput {
	outputs := make([]batchCommandOutput, 0, len(commands))
	startTime := time.Now()

	for i, cmd := range commands {
		elapsed := time.Since(startTime)
		remaining := time.Duration(timeout)*time.Millisecond - elapsed
		remainingSec := int(remaining.Seconds())
		if remainingSec <= 0 {
			for j := i; j < len(commands); j++ {
				outputs = append(outputs,
					batchOutput(commands[j], "(skipped — batch timeout exceeded)", 0, true))
			}
			break
		}

		result, err := exec.Execute(ctx, executor.ExecRequest{
			Language:   executor.Shell,
			Code:       cmd.Command,
			Cwd:        cwd,
			TimeoutSec: remainingSec,
		})
		if err != nil {
			outputs = append(outputs,
				batchOutput(cmd, fmt.Sprintf("(error: %v)", err), 0, false))
			continue
		}

		output := batchCapturedOutput(result.Stdout, result.Stderr)
		outputs = append(outputs,
			batchOutput(cmd, output, len(result.Stdout)+len(result.Stderr), false))

		if result.TimedOut {
			// Mark remaining commands as skipped
			for j := i + 1; j < len(commands); j++ {
				outputs = append(outputs,
					batchOutput(commands[j], "(skipped — batch timeout exceeded)", 0, true))
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
func executeBatchParallel(ctx context.Context, commands []CommandInput, timeout, concurrency int, exec *executor.PolyglotExecutor, cwd string) []batchCommandOutput {
	results := make([]batchCommandOutput, len(commands))
	// Each command gets the full timeout. Guard the sub-second case: truncating
	// ms→s would yield 0, which the executor reads as "no timeout → 30s default",
	// silently extending a tight batch budget. The serial path avoids this via
	// its remainingSec <= 0 skip guard. An explicit timeout == 0 is deliberately
	// left as 0 here so it flows through to the executor's 30s default (callers
	// who want no per-command ceiling rely on that); only positive sub-second
	// values are clamped up to 1s.
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
				Code:       cmd.Command,
				Cwd:        cwd,
				TimeoutSec: timeoutSec,
			})
			if err != nil {
				results[i] = batchOutput(cmd, fmt.Sprintf("(error: %v)", err), 0, false)
				return nil
			}

			output := batchCapturedOutput(result.Stdout, result.Stderr)
			if result.TimedOut {
				output += "\n(timed out)"
			}
			results[i] = batchOutput(cmd, output, len(result.Stdout)+len(result.Stderr), false)
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

// batchCapturedOutput presents stdout before stderr without modifying the shell
// command (a suffix can break a terminal heredoc). Stream interleaving is not
// preserved; add a newline only if the captured streams need a separator.
func batchCapturedOutput(stdout, stderr string) string {
	if stdout == "" && stderr == "" {
		return "(no output)"
	}
	if stdout != "" && stderr != "" && !strings.HasSuffix(stdout, "\n") && !strings.HasPrefix(stderr, "\n") {
		return stdout + "\n" + stderr
	}
	return stdout + stderr
}

// truncateLabel builds a source label from command labels, truncated to 80 bytes.
// D3: concurrent batches with the same truncated label can replace each other.
// See docs/feat/wip/upstream-sync-v1.0.169/upstream-audit.md#d3-concurrent-batch-source-label-collisions.
// Exact-source scope does not provide request isolation; unique identity is deferred.
// Command labels are user-supplied and may contain multibyte UTF-8, so the cap
// goes through truncateRunes — a raw joined[:80] byte slice can split a rune and
// emit an invalid sequence into the indexed source label.
func truncateLabel(commands []CommandInput) string {
	var labels []string
	for _, c := range commands {
		labels = append(labels, batchOneLine(c.Label))
	}
	joined := strings.Join(labels, ",")
	if len(joined) > 80 {
		joined = truncateRunes(joined, 80)
	}
	return joined
}
