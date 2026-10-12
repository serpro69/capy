// Package knowledge orchestrates filesystem ingestion independently of MCP.
// The store continues to own sanitization, deduplication, chunking and writes.
package knowledge

import (
	"context"
	"errors"
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
	// Set before first use, like readContent; permits commit-boundary tests.
	indexContent func(content, label, path string) (*store.IndexResult, error)
}

// NewIndexer uses the same size default as ContentStore. The prepared policy
// must belong to the selected project; a nil policy fails admission closed.
func NewIndexer(projectDir string, maxSourceBytes int, policy *security.FilePolicy, st *store.ContentStore) *Indexer {
	if maxSourceBytes <= 0 {
		maxSourceBytes = store.DefaultMaxSourceBytes
	}
	indexer := &Indexer{
		projectDir: projectDir, maxSourceBytes: maxSourceBytes,
		policy: policy, store: st, readContent: fileio.ReadAll,
	}
	indexer.indexContent = func(content, label, path string) (*store.IndexResult, error) {
		return indexer.store.IndexWithFilePath(content, label, "", store.KindDurable, path)
	}
	return indexer
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
	out, err = i.indexAdmittedFile(ctx, path, source, 0, false)
	return out, errors.Join(err, ctx.Err())
}

// indexAdmittedFile shares descriptor-bound reads with the directory walker.
// A positive allowance includes the detection byte; zero means single-file mode.
func (i *Indexer) indexAdmittedFile(ctx context.Context, path, source string, allowance int64, noFollow bool) (FileResult, error) {
	out := FileResult{Path: path}
	// TODO(D2): bind policy admission atomically to open. A descriptor binds
	// stat/read to one file but cannot prevent replacement before open.
	// See upstream-sync-v1.0.169/upstream-audit.md's checked-file handoff.
	// Nonblocking open prevents a FIFO from hanging before the mode check.
	flags := os.O_RDONLY | syscall.O_NONBLOCK
	if noFollow {
		flags |= syscall.O_NOFOLLOW
	}
	f, err := os.OpenFile(path, flags, 0)
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
	limit := i.maxSourceBytes
	if allowance > 0 && allowance <= int64(limit) {
		limit = int(allowance - 1)
	}
	var data []byte
	if limit == 0 {
		// One remaining byte can only prove that an empty file is complete.
		data, err = io.ReadAll(io.LimitReader(f, 1))
	} else {
		data, err = i.readContent(f, limit)
	}
	out.BytesRead = int64(len(data))
	var tooLarge *fileio.TooLargeError
	if limit < i.maxSourceBytes && len(data) > limit && (err == nil || errors.As(err, &tooLarge)) {
		return out, errDirectoryByteLimit
	}
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
	out.Result, err = i.indexContent(string(data), source, path)
	if err != nil {
		return out, fmt.Errorf("index file: %w", err)
	}
	return out, nil
}
