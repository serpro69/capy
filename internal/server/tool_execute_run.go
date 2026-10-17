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
	"github.com/serpro69/capy/internal/sanitize"
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
	if _, present := args["wait_ms"]; present {
		return reject("wait_ms is currently unsupported; synchronous calls wait for completion")
	}
	if background {
		return s.handleLegacyExecute(ctx, req)
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
	result := s.executionService().Execute(ctx, executor.ExecRequest{Language: language, Code: code, Cwd: cwd}, intent, time.Duration(millis)*time.Millisecond)
	s.stats.AddBytesSandboxed(result.Capture.ObservedBytes)
	return s.runResult(result), nil
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
	if result.Error != nil {
		safe := *result.Error
		safe.Message = sanitize.StripSecrets(safe.Message)
		result.Error = &safe
	}
	text := "Execution rejected before admission."
	if result.RunID != nil {
		text = fmt.Sprintf("Run %s: %s.", *result.RunID, result.State)
		if result.ExitCode != nil {
			text += fmt.Sprintf(" Exit code: %d.", *result.ExitCode)
		}
		text += " Output projection unavailable; recover status or explicitly request unsanitized raw output with capy_runs. Do not repeat this execution to retrieve its output."
	}
	if result.Error != nil {
		text += " " + result.Error.Message
	}
	r := mcp.NewToolResultStructured(result, text)
	r.IsError = result.State != "exited" || result.ExitCode == nil || *result.ExitCode != 0 || result.Signal != nil || result.Capture.State != "complete"
	if result.RunID == nil {
		return r
	}
	return s.trackToolResponse("capy_execute", r)
}
