package server

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

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

	// fileBacked holds the resolved absolute path when content was read from
	// disk; it stays "" for inline-content indexing. Only file-backed sources
	// get stale auto-refresh, so it gates the IndexWithFilePath call below.
	var fileBacked string

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
		} else {
			// Absolute inputs skip the resolve-and-Clean above, so canonicalize
			// them here too. Without this, an absolute path with traversal
			// segments (/tmp/a/../b.md) would be stored uncleaned as both the
			// source label and the file_path column, defeating dedup (two
			// spellings of one file → two sources). The earlier deny check runs
			// EvaluateFilePath, which Cleans+resolves symlinks independently, so
			// cleaning here does not affect the security decision.
			path = filepath.Clean(path)
		}
		maxFileSize := int64(s.config.Store.MaxSourceBytes)
		if maxFileSize <= 0 {
			maxFileSize = int64(store.DefaultMaxSourceBytes)
		}
		// fd-bound read: bind the deny check (above), the stat, and the read to
		// a single descriptor so the file cannot be swapped between them.
		// O_NONBLOCK prevents a FIFO from blocking the open before the
		// IsRegular guard can reject it (no-op for regular files); IsRegular
		// also blocks device/socket/dir reads.
		f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return errorResult(fmt.Sprintf("Failed to read file: %v", err)), nil
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return errorResult(fmt.Sprintf("Failed to read file: %v", err)), nil
		}
		if !info.Mode().IsRegular() {
			f.Close()
			return errorResult("Failed to read file: not a regular file"), nil
		}
		if info.Size() > maxFileSize {
			f.Close()
			return errorResult(fmt.Sprintf("File too large: %d bytes exceeds %d byte limit (configure store.max_source_bytes to adjust)", info.Size(), maxFileSize)), nil
		}
		data, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			return errorResult(fmt.Sprintf("Failed to read file: %v", err)), nil
		}
		content = string(data)
		fileBacked = path
		if source == "" {
			// Default the label to the resolved absolute path, not its basename.
			// filepath.Base drops the directory, so two distinct files sharing a
			// basename (docs/api.md and notes/api.md) would collide on the label
			// "api.md" — and since dedup is keyed by label, indexing the second
			// destroys the first. The absolute path is unique per file and
			// canonicalizes equivalent spellings (foo.md, ./foo.md) to one label.
			source = path
		}
	}

	if source == "" {
		source = "indexed-content"
	}

	// Track raw bytes being indexed
	s.stats.AddBytesIndexed(int64(len(content)))

	st := s.getStore()
	var result *store.IndexResult
	var err error
	if fileBacked != "" {
		// Record the backing path so search can auto-refresh this source when
		// the file changes on disk.
		result, err = st.IndexWithFilePath(content, source, "", store.KindDurable, fileBacked)
	} else {
		result, err = st.Index(content, source, "", store.KindDurable)
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
