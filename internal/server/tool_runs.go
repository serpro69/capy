package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/execution"
	"github.com/serpro69/capy/internal/runstore"
)

func (s *Server) handleRuns(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	fail := func(code, message string) (*mcp.CallToolResult, error) {
		r, err := execution.Budget(s.config.Runs.MaxResponseBytes, func(n int) (*mcp.CallToolResult, error) {
			return boundedRunError(code, execution.SafeMessage(message, n)), nil
		}, nil)
		if err != nil {
			r = boundedRunError("response_unavailable", "Run query response unavailable.")
		}
		return s.trackToolResponse("capy_runs", r), nil
	}
	operationError := func(err error) (*mcp.CallToolResult, error) {
		var typed *runstore.Error
		if errors.As(err, &typed) {
			return fail(typed.Code, typed.Message)
		}
		return fail("storage_unavailable", "run query failed")
	}
	action, ok := args["action"].(string)
	if !ok || (action != "status" && action != "read" && action != "list" && action != "cancel" && action != "search" && action != "inspect") {
		return fail("invalid_argument", "action must be status, read, list, search, cancel or inspect")
	}
	if action == "inspect" {
		result, err := s.inspectRuns(ctx, args)
		if err != nil {
			var typed *runstore.Error
			if errors.As(err, &typed) {
				return fail(typed.Code, inspectionMCPMessage(typed.Message))
			}
			return operationError(err)
		}
		return s.trackToolResponse("capy_runs", result), nil
	}
	if action == "search" {
		options, err := runSearchOptions(args)
		if err != nil {
			return fail("invalid_argument", err.Error())
		}
		page, err := s.searchRuns(ctx, options)
		if err != nil {
			return operationError(err)
		}
		r, err := execution.Budget(s.config.Runs.MaxResponseBytes, func(n int) (*mcp.CallToolResult, error) {
			result := mcp.NewToolResultStructured(execution.BoundSearch(page, n), "Search covers only the reported sanitized prefix; repeat search to resume indexing. Use hit read_cursor with the same run_id and stream.")
			result.IsError = page.Error != nil || page.KnowledgeError != nil
			return result, nil
		}, nil)
		if err != nil {
			return fail("response_unavailable", "Run search response unavailable.")
		}
		return s.trackToolResponse("capy_runs", r), nil
	}
	if batch, present := args["batch_id"]; present && (action == "status" || action == "cancel") {
		id, ok := batch.(string)
		if !ok || uuid.Validate(id) != nil || len(args) != 2 {
			return fail("invalid_argument", "batch status/cancel requires only action and a batch_id UUID")
		}
		var result runstore.BatchResult
		var requested bool
		var err error
		if action == "cancel" {
			result, requested, err = s.executionService().CancelBatch(ctx, id)
		} else {
			result, err = s.executionService().BatchStatus(ctx, id)
		}
		if err != nil {
			return operationError(err)
		}
		response, err := execution.Budget(s.config.Runs.MaxResponseBytes, func(n int) (*mcp.CallToolResult, error) {
			body := map[string]any{"version": runstore.EnvelopeVersion, "kind": "query", "batch": execution.BoundBatch(result, n)}
			if action == "cancel" {
				body["cancel_requested"] = requested
			}
			return mcp.NewToolResultStructured(body, "Batch status; list children by batch_id and read each run_id."), nil
		}, nil)
		if err != nil {
			return operationError(err)
		}
		return s.trackToolResponse("capy_runs", response), nil
	}
	if action == "list" {
		for name := range args {
			if name != "action" && name != "states" && name != "cursor" && name != "limit" && name != "batch_id" {
				return fail("invalid_argument", "unsupported argument for list")
			}
		}
		batchID := ""
		if value, present := args["batch_id"]; present {
			var ok bool
			batchID, ok = value.(string)
			if !ok || uuid.Validate(batchID) != nil {
				return fail("invalid_argument", "batch_id must be a UUID")
			}
		}
		limit, err := integerArg(args, "limit", 16, 1, 64)
		if err != nil {
			return fail("invalid_argument", err.Error())
		}
		cursor := ""
		if value, present := args["cursor"]; present {
			var ok bool
			cursor, ok = value.(string)
			if !ok || len(cursor) > 1024 {
				return fail("cursor_invalid", "invalid list cursor")
			}
		}
		var states []string
		if value, present := args["states"]; present {
			switch values := value.(type) {
			case []string:
				if len(values) > 11 {
					return fail("invalid_argument", "too many state filters")
				}
				states = values
			case []any:
				if len(values) > 11 {
					return fail("invalid_argument", "too many state filters")
				}
				for _, v := range values {
					state, ok := v.(string)
					if !ok {
						return fail("invalid_argument", "states must contain strings")
					}
					states = append(states, state)
				}
			default:
				return fail("invalid_argument", "states must be an array")
			}
		}
		r, err := execution.Budget(s.config.Runs.MaxResponseBytes, func(n int) (*mcp.CallToolResult, error) {
			page, err := s.executionService().List(ctx, runstore.ListOptions{BatchID: batchID, States: states, Cursor: cursor, Limit: min(int(limit), max(1, n/256))})
			if err != nil {
				return nil, err
			}
			return mcp.NewToolResultStructured(page, "Captured run summaries; use status or read by run_id."), nil
		}, nil)
		if err != nil {
			return operationError(err)
		}
		return s.trackToolResponse("capy_runs", r), nil
	}
	id, ok := args["run_id"].(string)
	if !ok || uuid.Validate(id) != nil {
		return fail("invalid_argument", "run_id must be a UUID")
	}
	allowed := map[string]bool{"action": true, "run_id": true}
	if action == "read" {
		for _, name := range []string{"raw", "encoding", "stream", "cursor", "max_bytes", "start_line"} {
			allowed[name] = true
		}
	}
	for name := range args {
		if !allowed[name] {
			return fail("invalid_argument", "unsupported argument for this run operation")
		}
	}
	if action == "status" || action == "cancel" {
		var result runstore.Result
		var requested bool
		var err error
		if action == "cancel" {
			result, requested, err = s.executionService().Cancel(ctx, id)
		} else {
			result, err = s.executionService().Status(ctx, id)
		}
		if err != nil {
			return operationError(err)
		}
		r, err := execution.Budget(s.config.Runs.MaxResponseBytes, func(n int) (*mcp.CallToolResult, error) {
			body := map[string]any{"version": runstore.EnvelopeVersion, "kind": "query", "run": execution.BoundResult(result, n)}
			if action == "cancel" {
				body["cancel_requested"] = requested
			}
			return mcp.NewToolResultStructured(body, fmt.Sprintf("Run %s: %s; capture %s.", id, result.State, result.Capture.State)), nil
		}, nil)
		if err != nil {
			return fail("response_unavailable", "Run status response unavailable.")
		}
		return s.trackToolResponse("capy_runs", r), nil
	}
	raw, err := boolArg(args, "raw", false)
	if err != nil {
		return fail("invalid_argument", err.Error())
	}
	stream := "merged"
	if value, present := args["stream"]; present {
		var ok bool
		stream, ok = value.(string)
		if !ok {
			return fail("invalid_argument", "stream must be a string")
		}
	}
	cursor := ""
	if value, present := args["cursor"]; present {
		var ok bool
		cursor, ok = value.(string)
		if !ok || len(cursor) > 1024 {
			return fail("cursor_invalid", "invalid cursor")
		}
	}
	encoding := "text"
	if raw {
		encoding = "base64"
	}
	if value, present := args["encoding"]; present {
		var ok bool
		encoding, ok = value.(string)
		if !ok {
			return fail("invalid_argument", "encoding must be a string")
		}
	}
	if (raw && encoding != "base64") || (!raw && encoding != "text") {
		return fail("invalid_argument", "raw reads require base64; sanitized reads require text")
	}
	maxBytes, err := integerArg(args, "max_bytes", 4096, 1, 65536)
	if err != nil {
		return fail("invalid_argument", err.Error())
	}
	var startLine int64
	if _, present := args["start_line"]; present {
		startLine, err = integerArg(args, "start_line", 0, 1, 1<<53-1)
		if err != nil {
			return fail("invalid_argument", err.Error())
		}
		if cursor != "" || stream == "merged" {
			return fail("invalid_argument", "start_line requires an explicit single stream and no cursor")
		}
	}
	r, err := execution.Budget(s.config.Runs.MaxResponseBytes, func(n int) (*mcp.CallToolResult, error) {
		fallback := "Sanitized output from the persisted projection."
		if raw {
			fallback = "Unsanitized raw output: base64 event fragments."
		}
		page, err := s.executionService().Read(ctx, id, runstore.ReadOptions{Stream: stream, Cursor: cursor, StartLine: startLine, MaxBytes: min(int(maxBytes), max(1, n)), Raw: raw})
		if err != nil {
			return nil, err
		}
		page.Run = execution.BoundResult(page.Run, n)
		return mcp.NewToolResultStructured(page, fallback), nil
	}, nil)
	if err != nil {
		return operationError(err)
	}
	return s.trackToolResponse("capy_runs", r), nil
}

func toolRuns() mcp.Tool {
	return mcp.NewTool("capy_runs", mcp.WithToolAnnotation(annotationCleanup),
		mcp.WithDescription("Recover captured runs with status, list, sanitized/raw reads and lazy sanitized search independent of the knowledge-search budget. Inspect reports recorded blockers and retained receipts without creating storage, claiming an owner, migrating or applying recovery; unfinished records do not prove live processes or healthy admission. Search reports coverage; repeat to resume bounded indexing. Cancel requests termination of this MCP owner's runs; poll status. Serial batch status/list/search/cancel use batch_id; list yields child run_id values for reads."),
		mcp.WithString("action", mcp.Required(), mcp.Enum("status", "read", "list", "search", "cancel", "inspect")),
		mcp.WithString("run_id", mcp.Description("Captured run UUID in this owner's run store; inspect selector, mutually exclusive with receipt_id and plan")),
		mcp.WithString("receipt_id", mcp.Description("Inspect only: recovery receipt UUID, not an incident UUID; mutually exclusive with run_id and plan")),
		mcp.WithString("plan", mcp.Description("Inspect only: lookup a retained applied receipt by plan token; does not validate unapplied plans or prove nonapplication when absent")),
		mcp.WithString("batch_id", mcp.Description("Batch UUID for status/list/search/cancel; mutually exclusive with run_id")),
		mcp.WithArray("queries", mcp.Items(map[string]any{"type": "string"}), mcp.Description("Search: 1–8 nonblank queries, at most 512 UTF-8 bytes each")),
		mcp.WithString("query_scope", mcp.Enum("batch", "global"), mcp.Description("Batch search only")),
		mcp.WithBoolean("raw", mcp.Description("Explicitly request unsanitized bytes (default false)")),
		mcp.WithString("encoding", mcp.Enum("text", "base64")),
		mcp.WithString("stream", mcp.Enum("stdout", "stderr", "merged")),
		mcp.WithString("cursor", mcp.Description("Opaque read/list/inspect continuation; inspect requires the same selector")),
		mcp.WithArray("states", mcp.Items(map[string]any{"type": "string"})),
		mcp.WithNumber("limit", mcp.Min(1), mcp.Max(64)),
		mcp.WithNumber("start_line", mcp.Min(1), mcp.Description("Raw line number; requires stdout or stderr and no cursor")),
		mcp.WithNumber("max_bytes", mcp.Min(1), mcp.Max(65536)),
	)
}
