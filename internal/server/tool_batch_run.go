package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/execution"
	"github.com/serpro69/capy/internal/runstore"
)

func (s *Server) handleBatchExecute(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	reject := func(err error) (*mcp.CallToolResult, error) {
		code := "invalid_argument"
		var typed *runstore.Error
		if errors.As(err, &typed) {
			code = typed.Code
		}
		return s.runResultForTool("capy_batch_execute", runstore.Rejection(code, err.Error())), nil
	}
	concurrency, err := integerArg(args, "concurrency", 1, 1, 8)
	if err != nil {
		return reject(err)
	}
	for name := range args {
		switch name {
		case "commands", "queries", "query_scope", "concurrency", "cwd", "timeout", "wait_ms":
		default:
			return reject(fmt.Errorf("unsupported batch argument"))
		}
	}
	commands, err := capturedBatchCommands(args["commands"])
	if err != nil {
		return reject(err)
	}
	queries, scope, err := batchSearchArgs(args)
	if err != nil {
		return reject(err)
	}
	input := runstore.BatchInput{Commands: commands, Queries: queries, QueryScope: scope, Concurrency: int(concurrency)}
	if err = input.Validate(); err != nil {
		return reject(err)
	}
	millis, err := integerArg(args, "timeout", int64(s.config.Executor.Timeout)*1000, 1, math.MaxInt64/int64(time.Millisecond))
	if err != nil {
		return reject(err)
	}
	wait, err := integerArg(args, "wait_ms", int64(s.config.Runs.WaitMS), 0, 30000)
	if err != nil {
		return reject(err)
	}
	input.Cwd, err = s.executionCwd(args)
	if err != nil {
		return reject(err)
	}
	// Validate every command before opening storage or executing the first one.
	for _, command := range commands {
		if denied := s.checkDenyPolicy(command.Command); denied != nil {
			return s.runResultForTool("capy_batch_execute", runstore.Rejection("permission_denied", resultTextForPolicy(denied))), nil
		}
	}
	var response *mcp.CallToolResult
	result, err := s.executionService().ExecuteBatchResponse(ctx, input, time.Duration(millis)*time.Millisecond, time.Duration(wait)*time.Millisecond, func(result runstore.BatchResult) error {
		var err error
		response, err = s.buildBatchRunResult(result)
		return err
	})
	if result.BatchID == "" && err != nil {
		return reject(err)
	}
	if err != nil {
		result.Error = &runstore.Error{Code: "response_unavailable", Message: "batch response preparation failed; recover batch status before retrying"}
	}
	if len(queries) > 0 && result.BatchID != "" && err == nil {
		page, searchErr := s.searchRuns(ctx, runstore.SearchOptions{BatchID: result.BatchID, Queries: queries, QueryScope: scope, Limit: 5})
		if searchErr == nil {
			result.Search = &page
		} else {
			result.Error = &runstore.Error{Code: "index_failed", Message: "initial batch search unavailable; use the batch search_action to recover"}
		}
	}
	if result.BatchID != "" {
		response, err = s.buildBatchRunResult(result)
		if err != nil {
			return boundedRunError("response_unavailable", "Recover batch with capy_runs list before retrying."), nil
		}
		return s.trackToolResponse("capy_batch_execute", response), nil
	}
	return response, nil
}

func resultTextForPolicy(result *mcp.CallToolResult) string {
	for _, content := range result.Content {
		if text, ok := content.(mcp.TextContent); ok {
			return text.Text
		}
	}
	return "blocked by security policy"
}

func capturedBatchCommands(value any) ([]runstore.BatchCommand, error) {
	if text, ok := value.(string); ok {
		if len(text) > 2<<20 {
			return nil, fmt.Errorf("commands exceed input limit")
		}
		if json.Unmarshal([]byte(text), &value) != nil {
			return nil, fmt.Errorf("commands must be an array")
		}
	}
	values, ok := value.([]any)
	if !ok || len(values) == 0 || len(values) > 256 {
		return nil, fmt.Errorf("commands must be a nonempty array of at most 256 objects")
	}
	result := make([]runstore.BatchCommand, 0, len(values))
	for i, value := range values {
		// Preserve existing string shorthand, but never silently drop an invalid
		// inventory member: a bad final member rejects every earlier command.
		if command, ok := value.(string); ok {
			result = append(result, runstore.BatchCommand{Label: fmt.Sprintf("cmd_%d", i+1), Command: command})
			continue
		}
		object, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("every command must be a command object or string")
		}
		label, labelOK := object["label"].(string)
		command, commandOK := object["command"].(string)
		if !labelOK || !commandOK || len(object) != 2 {
			return nil, fmt.Errorf("command objects require string label and command fields only")
		}
		result = append(result, runstore.BatchCommand{Label: label, Command: command})
	}
	return result, nil
}

func (s *Server) buildBatchRunResult(result runstore.BatchResult) (*mcp.CallToolResult, error) {
	return execution.Budget(s.config.Runs.MaxResponseBytes, func(n int) (*mcp.CallToolResult, error) {
		text := "Batch rejected before admission."
		if result.BatchID != "" {
			text = fmt.Sprintf("Batch %s: %s. Recover children with capy_runs(action=list,batch_id=%s); do not resubmit execution.", result.BatchID, result.State, result.BatchID)
		}
		bounded := execution.BoundBatch(result, n)
		if bounded.Error != nil {
			text += " " + bounded.Error.Message
		}
		r := mcp.NewToolResultStructured(bounded, text)
		r.IsError = execution.BatchFailed(result)
		return r, nil
	}, nil)
}
