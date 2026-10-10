package server

import (
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/security"
)

// checkDenyPolicy checks a shell command against deny policies.
// Returns an error result if denied or scanning fails, nil if allowed.
func (s *Server) checkDenyPolicy(command string) *mcp.CallToolResult {
	decision, err := security.EvaluateCommandDenyOnly(command, s.security)
	if err != nil {
		return errorResult(fmt.Sprintf("Command blocked: %v", err))
	}
	if decision.Decision == "deny" {
		return errorResult(fmt.Sprintf(
			"Command blocked by security policy: matches deny pattern %s",
			decision.MatchedPattern,
		))
	}
	return nil
}

// checkNonShellDenyPolicy extracts shell commands from non-shell code and
// checks each against deny policies. Scanner failures block execution.
func (s *Server) checkNonShellDenyPolicy(code, language string) *mcp.CallToolResult {
	commands := security.ExtractShellCommands(code, language)
	if len(commands) == 0 {
		return nil
	}
	for _, cmd := range commands {
		decision, err := security.EvaluateCommandDenyOnly(cmd, s.security)
		if err != nil {
			return errorResult(fmt.Sprintf("Embedded shell command blocked: %v", err))
		}
		if decision.Decision == "deny" {
			return errorResult(fmt.Sprintf(
				"Command blocked by security policy: embedded shell command %q matches deny pattern %s",
				cmd, decision.MatchedPattern,
			))
		}
	}
	return nil
}

// checkFilePathDenyPolicy checks a file path against Read deny patterns
// cached at server construction.
func (s *Server) checkFilePathDenyPolicy(filePath string) *mcp.CallToolResult {
	if err := s.checkReadPath(filePath); err != nil {
		return errorResult(fmt.Sprintf("File access blocked by security policy: %v", err))
	}
	return nil
}

func (s *Server) checkReadPath(filePath string) error {
	if s.readPolicyErr != nil {
		return s.readPolicyErr
	}
	return s.readPolicy.Check(filePath)
}
