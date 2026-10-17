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
	if !ok || (action != "status" && action != "read") {
		return fail("invalid_argument", "action must be status or read")
	}
	id, ok := args["run_id"].(string)
	if !ok || uuid.Validate(id) != nil {
		return fail("invalid_argument", "run_id must be a UUID")
	}
	allowed := map[string]bool{"action": true, "run_id": true}
	if action == "read" {
		for _, name := range []string{"raw", "encoding", "stream", "cursor", "max_bytes"} {
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
	if !raw {
		return fail("redaction_unavailable", "sanitized output projection is unavailable; raw output requires explicit raw=true")
	}
	r, err := execution.Budget(s.config.Runs.MaxResponseBytes, func(n int) (*mcp.CallToolResult, error) {
		page, err := s.executionService().ReadRaw(ctx, id, stream, cursor, min(int(maxBytes), max(1, n)))
		if err != nil {
			return nil, err
		}
		page.Run = execution.BoundResult(page.Run, n)
		return mcp.NewToolResultStructured(page, "Unsanitized raw output: base64 event fragments."), nil
	}, nil)
	if err != nil {
		return operationError(err)
	}
	return s.trackToolResponse("capy_runs", r), nil
}

func toolRuns() mcp.Tool {
	return mcp.NewTool("capy_runs", mcp.WithToolAnnotation(annotationCleanup),
		mcp.WithDescription("Recover a captured run without executing it again. Supports status and explicit raw reads; sanitized output and other actions are currently unavailable."),
		mcp.WithString("action", mcp.Required(), mcp.Enum("status", "read")),
		mcp.WithString("run_id", mcp.Required(), mcp.Description("Captured run UUID in this owner's run store")),
		mcp.WithBoolean("raw", mcp.Description("Explicitly request unsanitized bytes (default false)")),
		mcp.WithString("encoding", mcp.Enum("text", "base64")),
		mcp.WithString("stream", mcp.Enum("stdout", "stderr", "merged")),
		mcp.WithString("cursor", mcp.Description("Opaque continuation returned by the previous raw read")),
		mcp.WithNumber("max_bytes", mcp.Min(1), mcp.Max(65536)),
	)
}
