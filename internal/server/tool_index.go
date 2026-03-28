package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mark3labs/mcp-go/mcp"
)

func (s *Server) handleIndex(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	content := req.GetString("content", "")
	path := req.GetString("path", "")
	source := req.GetString("source", "")

	if content == "" && path == "" {
		return errorResult("Either content or path must be provided"), nil
	}

	// Read file content if path provided
	if path != "" && content == "" {
		// Resolve relative paths against project root
		if !filepath.IsAbs(path) {
			path = filepath.Join(s.projectDir, path)
		}
		const maxFileSize = 10 * 1024 * 1024 // 10MB
		info, err := os.Stat(path)
		if err != nil {
			return errorResult(fmt.Sprintf("Failed to read file: %v", err)), nil
		}
		if info.Size() > maxFileSize {
			return errorResult(fmt.Sprintf("File too large (>%dMB)", maxFileSize/(1024*1024))), nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return errorResult(fmt.Sprintf("Failed to read file: %v", err)), nil
		}
		content = string(data)
		if source == "" {
			source = filepath.Base(path)
		}
	}

	if source == "" {
		source = "indexed-content"
	}

	// Track raw bytes being indexed
	s.stats.AddBytesIndexed(int64(len(content)))

	st := s.getStore()
	result, err := st.Index(content, source, "")
	if err != nil {
		return errorResult(fmt.Sprintf("Index error: %v", err)), nil
	}

	text := fmt.Sprintf(
		"Indexed %d sections (%d with code) from: %s\nUse search(queries: [\"...\"]) to query this content. Use source: %q to scope results.",
		result.TotalChunks, result.CodeChunks, result.Label, result.Label,
	)
	return s.trackToolResponse("capy_index", textResult(text)), nil
}
