package server

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
)

func (s *Server) handleExecuteFile(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return s.handleCapturedExecute(ctx, req, "capy_execute_file")
}
