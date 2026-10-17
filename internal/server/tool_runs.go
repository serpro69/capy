package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/runstore"
)

func (s *Server) handleRuns(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	fail := func(code, message string) (*mcp.CallToolResult, error) {
		r := mcp.NewToolResultStructured(map[string]any{"version": 1, "kind": "query", "error": runstore.Error{Code: code, Message: message}}, message)
		r.IsError = true
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
		r := mcp.NewToolResultStructured(map[string]any{"version": 1, "kind": "query", "run": result}, fmt.Sprintf("Run %s: %s; capture %s.", id, result.State, result.Capture.State))
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
	page, err := s.executionService().ReadRaw(ctx, id, stream, cursor, int(maxBytes))
	if err != nil {
		return operationError(err)
	}
	r := mcp.NewToolResultStructured(page, "Unsanitized raw output is provided as base64 event fragments in structured content.")
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
