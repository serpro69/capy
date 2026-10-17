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
	if !ok || (action != "status" && action != "read" && action != "list") {
		return fail("invalid_argument", "action must be status, read or list")
	}
	if action == "list" {
		for name := range args {
			if name != "action" && name != "states" && name != "cursor" && name != "limit" {
				return fail("invalid_argument", "unsupported argument for list")
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
			page, err := s.executionService().List(ctx, runstore.ListOptions{States: states, Cursor: cursor, Limit: min(int(limit), max(1, n/256))})
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
	if action == "status" {
		result, err := s.executionService().Status(ctx, id)
		if err != nil {
			return operationError(err)
		}
		r, err := execution.Budget(s.config.Runs.MaxResponseBytes, func(n int) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultStructured(map[string]any{"version": 1, "kind": "query", "run": execution.BoundResult(result, n)}, fmt.Sprintf("Run %s: %s; capture %s.", id, result.State, result.Capture.State)), nil
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
		mcp.WithDescription("Recover a captured run without executing it again. Supports status, list, sanitized reads and explicit raw reads. Other actions are currently unavailable."),
		mcp.WithString("action", mcp.Required(), mcp.Enum("status", "read", "list")),
		mcp.WithString("run_id", mcp.Description("Captured run UUID in this owner's run store")),
		mcp.WithBoolean("raw", mcp.Description("Explicitly request unsanitized bytes (default false)")),
		mcp.WithString("encoding", mcp.Enum("text", "base64")),
		mcp.WithString("stream", mcp.Enum("stdout", "stderr", "merged")),
		mcp.WithString("cursor", mcp.Description("Opaque continuation returned by the previous read")),
		mcp.WithArray("states", mcp.Items(map[string]any{"type": "string"})),
		mcp.WithNumber("limit", mcp.Min(1), mcp.Max(64)),
		mcp.WithNumber("start_line", mcp.Min(1), mcp.Description("Raw line number; requires stdout or stderr and no cursor")),
		mcp.WithNumber("max_bytes", mcp.Min(1), mcp.Max(65536)),
	)
}
