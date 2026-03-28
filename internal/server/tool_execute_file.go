package server

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/executor"
)

func (s *Server) handleExecuteFile(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	filePath := req.GetString("path", "")
	language := executor.Language(req.GetString("language", ""))
	code := req.GetString("code", "")
	timeout := int(req.GetFloat("timeout", 30000))
	intent := req.GetString("intent", "")

	if filePath == "" || code == "" {
		return errorResult("missing required parameter: path and code"), nil
	}

	// Security: check file path against Read deny patterns
	if denied := s.checkFilePathDenyPolicy(filePath); denied != nil {
		return denied, nil
	}

	// Security: check code against deny patterns
	if denied := s.securityCheck(language, code); denied != nil {
		return denied, nil
	}

	result, err := s.executor.ExecuteFile(ctx, executor.ExecRequest{
		Language:   language,
		Code:       code,
		FilePath:   filePath,
		TimeoutSec: timeout / 1000,
	})
	if err != nil {
		return errorResult(fmt.Sprintf("execution error: %v", err)), nil
	}

	// Track sandboxed bytes
	s.stats.AddBytesSandboxed(int64(len(result.Stdout) + len(result.Stderr)))

	// Timeout
	if result.TimedOut {
		text := fmt.Sprintf("Timed out processing %s after %dms", filePath, timeout)
		return s.trackToolResponse("capy_execute_file", errorResult(text)), nil
	}

	// Non-zero exit
	if result.ExitCode != 0 {
		cls := executor.ClassifyNonZeroExit(language, result.ExitCode, result.Stdout, result.Stderr)
		source := fmt.Sprintf("file:%s", filePath)
		if cls.IsError {
			source += ":error"
		}
		return s.handleIntentOrReturnWithSource(ctx, "capy_execute_file", cls.Output, cls.IsError, intent, source), nil
	}

	// Success
	stdout := result.Stdout
	if stdout == "" {
		stdout = "(no output)"
	}
	source := fmt.Sprintf("file:%s", filePath)
	return s.handleIntentOrReturnWithSource(ctx, "capy_execute_file", stdout, false, intent, source), nil
}

// handleIntentOrReturnWithSource is like handleIntentOrReturn but with explicit source label.
func (s *Server) handleIntentOrReturnWithSource(_ context.Context, toolName, output string, isError bool, intent, source string) *mcp.CallToolResult {
	if intent != "" && len(output) > intentSearchThreshold {
		s.stats.AddBytesIndexed(int64(len(output)))
		text := s.intentSearch(output, intent, source, 5)
		r := textResult(text)
		if isError {
			r.IsError = true
		}
		return s.trackToolResponse(toolName, r)
	}

	r := textResult(output)
	if isError {
		r.IsError = true
	}
	return s.trackToolResponse(toolName, r)
}
