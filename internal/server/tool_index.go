package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/store"
)

func (s *Server) handleIndex(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	content := req.GetString("content", "")
	path := req.GetString("path", "")
	source := req.GetString("source", "")

	if content == "" && path == "" {
		return errorResult("Either content or path must be provided"), nil
	}

	// Security: when a path is supplied, check it against Read deny patterns
	// before any file I/O. This closes a search-index exfiltration path — any
	// file readable by the server process could otherwise be indexed into FTS5
	// and surfaced by capy_search, bypassing the Read deny-pattern mitigation
	// (e.g. .ssh/id_rsa, .env, ~/.aws/credentials). checkFilePathDenyPolicy
	// resolves the raw path against the project root (see EvaluateFilePath), so
	// relative "../" traversal and symlink escapes are caught too. Inline
	// content-only indexing (path == "") is unaffected.
	if path != "" {
		if denied := s.checkFilePathDenyPolicy(path); denied != nil {
			return denied, nil
		}
	}

	// Read file content if path provided
	if path != "" && content == "" {
		// Resolve relative paths against project root with traversal protection
		if !filepath.IsAbs(path) {
			path = filepath.Join(s.projectDir, path)
			// Ensure resolved path stays within project root (prevents ../../../etc/passwd)
			cleanPath := filepath.Clean(path)
			projectRoot := filepath.Clean(s.projectDir)
			if !strings.HasPrefix(cleanPath, projectRoot+string(filepath.Separator)) && cleanPath != projectRoot {
				return errorResult("Path escapes project directory"), nil
			}
			path = cleanPath
		}
		maxFileSize := int64(s.config.Store.MaxSourceBytes)
		if maxFileSize <= 0 {
			maxFileSize = int64(store.DefaultMaxSourceBytes)
		}
		info, err := os.Stat(path)
		if err != nil {
			return errorResult(fmt.Sprintf("Failed to read file: %v", err)), nil
		}
		if info.Size() > maxFileSize {
			return errorResult(fmt.Sprintf("File too large: %d bytes exceeds %d byte limit (configure store.max_source_bytes to adjust)", info.Size(), maxFileSize)), nil
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
	result, err := st.Index(content, source, "", store.KindDurable)
	if err != nil {
		return errorResult(fmt.Sprintf("Index error: %v", err)), nil
	}

	text := fmt.Sprintf(
		"Indexed %d sections (%d with code) from: %s\nUse search(queries: [\"...\"]) to query this content. Use source: %q to scope results.",
		result.TotalChunks, result.CodeChunks, result.Label, result.Label,
	)
	return s.trackToolResponse("capy_index", textResult(text)), nil
}
