package server

import (
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/security"
)

// checkDenyPolicy checks a shell command against deny policies.
// Returns an error result if denied, nil if allowed. Fail-open on error.
func (s *Server) checkDenyPolicy(command string) *mcp.CallToolResult {
	decision := security.EvaluateCommandDenyOnly(command, s.security)
	if decision.Decision == "deny" {
		return errorResult(fmt.Sprintf(
			"Command blocked by security policy: matches deny pattern %s",
			decision.MatchedPattern,
		))
	}
	return nil
}

// checkNonShellDenyPolicy extracts shell commands from non-shell code and
// checks each against deny policies. Fail-open on error.
func (s *Server) checkNonShellDenyPolicy(code, language string) *mcp.CallToolResult {
	commands := security.ExtractShellCommands(code, language)
	if len(commands) == 0 {
		return nil
	}
	for _, cmd := range commands {
		decision := security.EvaluateCommandDenyOnly(cmd, s.security)
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
	denied, pattern := security.EvaluateFilePath(filePath, s.readDenyGlobs)
	if denied {
		return errorResult(fmt.Sprintf(
			"File access blocked by security policy: path matches Read deny pattern %s",
			pattern,
		))
	}
	return nil
}
