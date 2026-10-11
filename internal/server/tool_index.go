package server

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/knowledge"
	"github.com/serpro69/capy/internal/store"
)

func (s *Server) handleIndex(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	content := req.GetString("content", "")
	path := req.GetString("path", "")
	source := req.GetString("source", "")

	if content == "" && path == "" {
		return errorResult("Either content or path must be provided"), nil
	}

	var result *store.IndexResult
	var err error
	if path != "" && content == "" {
		if s.readPolicyErr != nil {
			return errorResult(fmt.Sprintf("File access blocked by security policy: %v", s.readPolicyErr)), nil
		}
		ingestion := knowledge.NewIndexer(s.projectDir, s.config.Store.MaxSourceBytes, s.readPolicy, s.getStore())
		outcome, err := ingestion.IndexFile(ctx, path, source)
		s.stats.AddBytesIndexed(outcome.BytesIndexed)
		if err != nil {
			return errorResult(fmt.Sprintf("Failed to read file or index it: %v", err)), nil
		}
		result = outcome.Result
	} else {
		// Supplying both still indexes inline content, but the supplied path
		// must pass the existing Read policy even though it is not opened.
		if path != "" {
			if denied := s.checkFilePathDenyPolicy(path); denied != nil {
				return denied, nil
			}
		}
		if source == "" {
			source = "indexed-content"
		}
		s.stats.AddBytesIndexed(int64(len(content)))
		result, err = s.getStore().Index(content, source, "", store.KindDurable)
	}
	if err != nil {
		return errorResult(fmt.Sprintf("Index error: %v", err)), nil
	}

	text := fmt.Sprintf(
		"Indexed %d sections (%d with code) from: %s\nUse search(queries: [\"...\"]) to query this content. Use source: %q to scope results.",
		result.TotalChunks, result.CodeChunks, result.Label, result.Label,
	)
	return s.trackToolResponse("capy_index", textResult(text)), nil
}
