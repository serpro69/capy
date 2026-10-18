package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/execution"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
)

func (s *Server) executionService() *execution.Service {
	s.runsMu.Lock()
	defer s.runsMu.Unlock()
	if s.runs == nil {
		s.runs = execution.NewService(s.projectDir, s.knowledgeKey, s.config.Runs)
	}
	return s.runs
}

func (s *Server) handleExecute(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	reject := func(message string) (*mcp.CallToolResult, error) {
		return s.runResult(runstore.Rejection("invalid_argument", message)), nil
	}
	background, err := boolArg(args, "background", false)
	if err != nil {
		return reject(err.Error())
	}
	if _, present := args["wait_ms"]; present && background {
		return reject("background and wait_ms cannot be combined")
	}
	code, ok := args["code"].(string)
	if !ok || code == "" {
		return reject("missing required parameter: code (nonempty string)")
	}
	languageName, ok := args["language"].(string)
	if !ok {
		return reject("missing required parameter: language")
	}
	language := executor.Language(languageName)
	switch language {
	case executor.Shell, executor.Python, executor.Go, executor.Rust, executor.JavaScript, executor.TypeScript, executor.Ruby, executor.Perl, executor.PHP, executor.R, executor.Elixir:
	default:
		return reject("unsupported execution language")
	}
	intent := ""
	if value, present := args["intent"]; present {
		var ok bool
		intent, ok = value.(string)
		if !ok {
			return reject("intent must be a string")
		}
		intent = strings.TrimSpace(intent)
	}
	defaultMS := int64(s.config.Executor.Timeout) * 1000
	millis, err := integerArg(args, "timeout", defaultMS, 1, math.MaxInt64/int64(time.Millisecond))
	if err != nil {
		return reject(err.Error())
	}
	waitMS, err := integerArg(args, "wait_ms", int64(s.config.Runs.WaitMS), 0, 30000)
	if err != nil {
		return reject(err.Error())
	}
	timeout := time.Duration(millis) * time.Millisecond
	wait := time.Duration(waitMS) * time.Millisecond
	if background {
		wait = timeout
		timeout = 0
	}
	cwd, err := s.executionCwd(args)
	if err != nil {
		return reject(err.Error())
	}
	if denied := s.securityCheck(language, code); denied != nil {
		message := "blocked by security policy"
		for _, content := range denied.Content {
			if text, ok := content.(mcp.TextContent); ok {
				message = text.Text
				break
			}
		}
		return s.runResult(runstore.Rejection("permission_denied", message)), nil
	}
	var response *mcp.CallToolResult
	result, err := s.executionService().ExecuteResponse(ctx, executor.ExecRequest{Language: language, Code: code, Cwd: cwd}, intent, timeout, wait, func(result runstore.Result) error {
		var err error
		response, err = s.buildRunResult(result)
		return err
	})
	s.stats.AddBytesSandboxed(result.Capture.ObservedBytes)
	if err != nil {
		result.Error = &runstore.Error{Code: "response_unavailable", Message: "Response preparation failed; attached execution was cancelled. Recover this run before retrying."}
		return s.runResult(result), nil
	}
	return s.trackToolResponse("capy_execute", response), nil
}

func integerArg(args map[string]any, name string, fallback, low, high int64) (int64, error) {
	value, present := args[name]
	if !present {
		if fallback < low || fallback > high {
			return 0, fmt.Errorf("%s default is outside its supported range", name)
		}
		return fallback, nil
	}
	var n float64
	switch v := value.(type) {
	case int:
		n = float64(v)
	case int64:
		n = float64(v)
	case float64:
		n = v
	case json.Number:
		var err error
		n, err = v.Float64()
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer", name)
		}
	default:
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	if math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || n < float64(low) || n > float64(high) {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", name, low, high)
	}
	return int64(n), nil
}

func (s *Server) runResult(result runstore.Result) *mcp.CallToolResult {
	r, err := s.buildRunResult(result)
	if err != nil {
		r = boundedRunError("response_unavailable", "Run response unavailable; list existing runs before retrying execution.")
	}
	if result.RunID == nil {
		return r
	}
	return s.trackToolResponse("capy_execute", r)
}

func (s *Server) buildRunResult(result runstore.Result) (*mcp.CallToolResult, error) {
	return execution.Budget(s.config.Runs.MaxResponseBytes, func(allowance int) (*mcp.CallToolResult, error) {
		bounded := execution.BoundResult(result, allowance)
		text := "Execution rejected before admission."
		if result.RunID != nil {
			text = fmt.Sprintf("Run %s: %s. Recover with capy_runs; do not repeat execution.", *result.RunID, result.State)
			if result.ExitCode != nil {
				text += fmt.Sprintf(" Exit code: %d.", *result.ExitCode)
			}
		}
		if bounded.Error != nil {
			text += " " + bounded.Error.Message
		}
		r := mcp.NewToolResultStructured(bounded, text)
		r.IsError = execution.ExecutionFailed(result)
		return r, nil
	}, nil)
}

func boundedRunError(code, message string) *mcp.CallToolResult {
	r := mcp.NewToolResultStructured(map[string]any{"version": 1, "kind": "query", "error": runstore.Error{Code: code, Message: message}}, "Run operation failed.")
	r.IsError = true
	return r
}
