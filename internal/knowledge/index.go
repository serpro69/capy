// Package knowledge orchestrates filesystem ingestion independently of MCP.
// The store continues to own sanitization, deduplication, chunking and writes.
package knowledge

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/serpro69/capy/internal/fileio"
	"github.com/serpro69/capy/internal/security"
	"github.com/serpro69/capy/internal/store"
)

// FileResult describes one attempt, including byte accounting on failure.
// Result.AlreadyIndexed is the store's authoritative deduplication outcome.
type FileResult struct {
	Path         string
	BytesRead    int64 // Actual content bytes read, including an oversize probe.
	BytesIndexed int64 // Raw bytes offered to the store, including unchanged content.
	Result       *store.IndexResult
}

// Indexer captures the selected project's ingestion dependencies. It never
// selects credentials or storage from the input file's location.
type Indexer struct {
	projectDir     string
	maxSourceBytes int
	policy         *security.FilePolicy
	store          *store.ContentStore
	// Set before first use; the seam allows deterministic growth/read failures.
	readContent func(io.Reader, int) ([]byte, error)
}

// NewIndexer uses the same size default as ContentStore. The prepared policy
// must belong to the selected project; a nil policy fails admission closed.
func NewIndexer(projectDir string, maxSourceBytes int, policy *security.FilePolicy, st *store.ContentStore) *Indexer {
	if maxSourceBytes <= 0 {
		maxSourceBytes = store.DefaultMaxSourceBytes
	}
	return &Indexer{
		projectDir: projectDir, maxSourceBytes: maxSourceBytes,
		policy: policy, store: st, readContent: fileio.ReadAll,
	}
}

// IndexFile indexes one durable, file-backed source. Relative inputs retain
// the existing project-containment rule; explicit absolute inputs may be
// external, subject to Read denies. Labels retain lexical absolute paths.
func (i *Indexer) IndexFile(ctx context.Context, path, source string) (FileResult, error) {
	var out FileResult
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if err := i.policy.Check(path); err != nil {
		return out, fmt.Errorf("file access blocked by security policy: %w", err)
	}
	if i.projectDir == "" {
		return out, fmt.Errorf("selected project directory is empty")
	}
	root, err := filepath.Abs(i.projectDir)
	if err != nil {
		return out, fmt.Errorf("resolve project directory: %w", err)
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return out, fmt.Errorf("path escapes project directory")
		}
	} else {
		path = filepath.Clean(path)
	}
	// Check the final spelling too: a policy with a different rule cwd must
	// not authorize a relative name that opens a denied project file.
	if err := i.policy.Check(path); err != nil {
		return out, fmt.Errorf("file access blocked by security policy: %w", err)
	}
	out.Path = path
	// TODO(D2): bind policy admission atomically to open. A descriptor binds
	// stat/read to one file but cannot prevent replacement before open.
	// See upstream-sync-v1.0.169/upstream-audit.md's checked-file handoff.
	// Nonblocking open prevents a FIFO from hanging before the mode check.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return out, fmt.Errorf("read file: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return out, fmt.Errorf("stat file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return out, fmt.Errorf("not a regular file")
	}
	if info.Size() > int64(i.maxSourceBytes) {
		return out, &fileio.TooLargeError{Size: info.Size(), Limit: i.maxSourceBytes}
	}
	data, err := i.readContent(f, i.maxSourceBytes)
	out.BytesRead = int64(len(data))
	if err != nil {
		return out, fmt.Errorf("read file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if source == "" {
		source = path
	}
	out.BytesIndexed = int64(len(data))
	out.Result, err = i.store.IndexWithFilePath(string(data), source, "", store.KindDurable, path)
	if err != nil {
		return out, fmt.Errorf("index file: %w", err)
	}
	return out, nil
}
