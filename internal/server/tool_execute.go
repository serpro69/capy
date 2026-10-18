package server

import (
	"encoding/json"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/executor"
)

// securityCheck runs the appropriate security check for shell vs non-shell.
func (s *Server) securityCheck(language executor.Language, code string) *mcp.CallToolResult {
	if language == executor.Shell {
		return s.checkDenyPolicy(code)
	}
	return s.checkNonShellDenyPolicy(code, string(language))
}

// trackToolResponse tracks stats and returns the result.
func (s *Server) trackToolResponse(toolName string, r *mcp.CallToolResult) *mcp.CallToolResult {
	if r.StructuredContent != nil {
		encoded, err := json.Marshal(r)
		if err != nil {
			return errorResult("could not serialize structured tool result")
		}
		s.stats.TrackResponse(toolName, int64(len(encoded)))
		return r
	}
	var bytes int64
	for _, c := range r.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			bytes += int64(len(tc.Text))
		}
	}
	s.stats.TrackResponse(toolName, bytes)
	return r
}
