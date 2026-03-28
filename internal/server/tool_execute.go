package server

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/executor"
)

func (s *Server) handleExecute(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()

	language := executor.Language(req.GetString("language", ""))
	code := req.GetString("code", "")
	timeout := int(req.GetFloat("timeout", 30000))
	background, _ := args["background"].(bool)
	intent := req.GetString("intent", "")

	if code == "" {
		return errorResult("missing required parameter: code"), nil
	}

	// Security check
	if denied := s.securityCheck(language, code); denied != nil {
		return denied, nil
	}

	// NOTE: integer division loses sub-second precision (1500ms → 1s).
	// Matches TS reference behavior; not worth the complexity of ms-level timeouts.
	result, err := s.executor.Execute(ctx, executor.ExecRequest{
		Language:   language,
		Code:       code,
		TimeoutSec: timeout / 1000,
		Background: background,
	})
	if err != nil {
		return errorResult(fmt.Sprintf("execution error: %v", err)), nil
	}

	output := result.Stdout
	if result.Stderr != "" {
		output += "\n\nstderr:\n" + result.Stderr
	}

	// Track sandboxed bytes
	s.stats.AddBytesSandboxed(int64(len(result.Stdout) + len(result.Stderr)))

	// Background mode
	if result.Backgrounded && output != "" {
		text := fmt.Sprintf("%s\n\n_(process backgrounded after %dms — still running, PID %d)_", output, timeout, result.PID)
		return s.trackToolResponse("capy_execute", textResult(text)), nil
	}

	// Timeout handling
	if result.TimedOut {
		if output != "" {
			text := fmt.Sprintf("%s\n\n_(timed out after %dms — partial output shown above)_", output, timeout)
			return s.trackToolResponse("capy_execute", textResult(text)), nil
		}
		text := fmt.Sprintf("Execution timed out after %dms\n\nstderr:\n%s", timeout, result.Stderr)
		r := errorResult(text)
		return s.trackToolResponse("capy_execute", r), nil
	}

	// Non-zero exit
	if result.ExitCode != 0 {
		cls := executor.ClassifyNonZeroExit(language, result.ExitCode, result.Stdout, result.Stderr)
		return s.handleIntentOrReturn(ctx, "capy_execute", cls.Output, cls.IsError, intent, language), nil
	}

	// Success
	stdout := result.Stdout
	if stdout == "" {
		stdout = "(no output)"
	}
	return s.handleIntentOrReturn(ctx, "capy_execute", stdout, false, intent, language), nil
}

// handleIntentOrReturn checks if output should be intent-searched or returned directly.
func (s *Server) handleIntentOrReturn(ctx context.Context, toolName, output string, isError bool, intent string, language executor.Language) *mcp.CallToolResult {
	source := fmt.Sprintf("execute:%s", language)
	if isError {
		source += ":error"
	}
	return s.handleIntentOrReturnWithSource(ctx, toolName, output, isError, intent, source)
}

// securityCheck runs the appropriate security check for shell vs non-shell.
func (s *Server) securityCheck(language executor.Language, code string) *mcp.CallToolResult {
	if language == executor.Shell {
		return s.checkDenyPolicy(code)
	}
	return s.checkNonShellDenyPolicy(code, string(language))
}

// trackToolResponse tracks stats and returns the result.
func (s *Server) trackToolResponse(toolName string, r *mcp.CallToolResult) *mcp.CallToolResult {
	var bytes int64
	for _, c := range r.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			bytes += int64(len(tc.Text))
		}
	}
	s.stats.TrackResponse(toolName, bytes)
	return r
}
