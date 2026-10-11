package knowledge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

const (
	maxDirectoryEntries = 20_000
	maxDirectoryBytes   = 32 << 20
	maxFailureDetails   = 10
)

var errDirectoryByteLimit = errors.New("directory read-byte limit reached")

// DirectoryResult records committed progress even when the returned error is
// non-nil. Limits are successful but incomplete; failures/cancellation are errors.
// Visited counts enumerated entries, including filtered entries, not just files.
type DirectoryResult struct {
	Root, SourcePrefix                  string
	Complete                            bool
	GitIgnoreStatus                     string
	Visited, Attempts                   int
	Indexed, Unchanged, Denied, Skipped int
	Failed, Chunks                      int
	BytesRead, BytesIndexed             int64
	Limits, Failures                    []string
	OmittedFailures                     int
}

// PathResult distinguishes file and directory outcomes without an MCP dependency.
type PathResult struct {
	File      *FileResult
	Directory *DirectoryResult
}

// IndexPath dispatches explicit file/directory inputs. Controls are validated
// even for a file, but recursive exclusions never apply to an explicit file.
func (i *Indexer) IndexPath(ctx context.Context, path, source string, options DirectoryOptions) (PathResult, error) {
	if err := options.Validate(); err != nil {
		return PathResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return PathResult{}, err
	}
	abs, err := i.explicitPath(path)
	if err != nil {
		return PathResult{}, err
	}
	// Explicit files historically clean before opening. Prefer that target
	// before probing the uncleaned spelling for a directory: alias/../file can
	// name a different physical object, or nothing, after following the alias.
	lexicalInfo, lexicalErr := os.Stat(filepath.Clean(abs))
	knownFile := lexicalErr == nil && !lexicalInfo.IsDir()
	if !knownFile {
		if info, err := os.Stat(abs); err == nil && info.IsDir() {
			out, err := i.IndexDirectory(ctx, path, source, options)
			return PathResult{Directory: &out}, err
		}
	}
	out, err := i.IndexFile(ctx, path, source)
	return PathResult{File: &out}, err
}

func (i *Indexer) explicitPath(path string) (string, error) {
	if path == "" || i.projectDir == "" {
		return "", fmt.Errorf("input path and selected project directory must be nonempty")
	}
	if err := i.policy.Check(path); err != nil {
		return "", fmt.Errorf("file access blocked by security policy: %w", err)
	}
	root, err := filepath.Abs(i.projectDir)
	if err != nil {
		return "", fmt.Errorf("resolve project directory: %w", err)
	}
	if !filepath.IsAbs(path) {
		// Preserve symlink-before-.. semantics until physical resolution.
		path = root + string(filepath.Separator) + path
		if !withinDirectory(root, filepath.Clean(path)) {
			return "", fmt.Errorf("path escapes project directory")
		}
	}
	if err := i.policy.Check(path); err != nil {
		return "", fmt.Errorf("file access blocked by security policy: %w", err)
	}
	return path, nil
}

func withinDirectory(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// IndexDirectory admits an explicit root (including a symlink) and walks its
// canonical directory. Descendant symlinks are skipped; following is Task 13a.
func (i *Indexer) IndexDirectory(ctx context.Context, path, source string, options DirectoryOptions) (DirectoryResult, error) {
	out := DirectoryResult{SourcePrefix: source, GitIgnoreStatus: "disabled"}
	selection, err := options.prepare()
	if err != nil {
		return out, err
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	requested, err := i.explicitPath(path)
	if err != nil {
		return out, err
	}
	root, err := filepath.EvalSymlinks(requested)
	if err != nil {
		return out, fmt.Errorf("resolve directory root: %w", err)
	}
	if err := i.policy.Check(root); err != nil {
		return out, fmt.Errorf("directory root blocked: %w", err)
	}
	out.Root = root
	info, err := os.Stat(root)
	if err != nil {
		return out, fmt.Errorf("stat directory root: %w", err)
	}
	if !info.IsDir() {
		return out, fmt.Errorf("input root is not a directory")
	}
	worktree := ""
	if selection.respectGitignore {
		out.GitIgnoreStatus = "lookup failed"
		worktree, err = findWorktree(ctx, root)
		if err != nil {
			out.fail(root, err)
			return out, err
		}
		out.GitIgnoreStatus = "not applied (outside a Git worktree)"
		if worktree != "" {
			out.GitIgnoreStatus = "applied using " + worktree
		}
	}
	w := directoryWalk{indexer: i, selection: selection, worktree: worktree, requestedRoot: requested, out: &out}
	w.walk(ctx, root, 0)
	out.Complete = w.err == nil && len(out.Limits) == 0
	return out, w.err
}

type directoryWalk struct {
	indexer       *Indexer
	selection     directorySelection
	worktree      string
	requestedRoot string
	out           *DirectoryResult
	err           error // first cause; bounded individual details live in out
	stopped       bool
}

func (r *DirectoryResult) fail(path string, err error) {
	r.Failed++
	if len(r.Failures) < maxFailureDetails {
		// Bound each diagnostic as well as their count; quote control characters.
		r.Failures = append(r.Failures, fmt.Sprintf("%.512q: %.1024q", path, err.Error()))
	} else {
		r.OmittedFailures++
	}
}

func (w *directoryWalk) fail(path string, err error) {
	w.out.fail(path, err)
	if w.err == nil {
		w.err = err
	}
}

func (w *directoryWalk) limit(name string, stop bool) {
	if !slices.Contains(w.out.Limits, name) {
		w.out.Limits = append(w.out.Limits, name)
	}
	w.stopped = w.stopped || stop
}

func (w *directoryWalk) canceled(ctx context.Context) bool {
	if w.stopped {
		return true
	}
	if err := ctx.Err(); err != nil {
		w.fail(w.out.Root, err)
		w.stopped = true
	}
	return w.stopped
}

func (w *directoryWalk) walk(ctx context.Context, dir string, depth int) {
	if w.canceled(ctx) {
		return
	}
	entries, err := w.entries(ctx, dir)
	if err != nil {
		w.fail(dir, err)
		w.stopped = ctx.Err() != nil
		return
	}
	if w.stopped {
		return
	}
	// Exclusions, policy checks and nested-root pruning happen BEFORE Git sees
	// paths, avoiding errors on submodule descendants and skipped symlinks.
	candidates := make([]os.DirEntry, 0, len(entries))
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if w.canceled(ctx) {
			return
		}
		path := filepath.Join(dir, entry.Name())
		rel, err := filepath.Rel(w.out.Root, path)
		if err != nil {
			w.fail(path, err)
			continue
		}
		rel = filepath.ToSlash(rel)
		if mandatoryExcluded(rel) || matchesAny(w.selection.exclude, rel) || entry.Type()&os.ModeSymlink != 0 {
			w.out.Skipped++
			continue
		}
		if !w.admitPath(path) {
			continue
		}
		if entry.IsDir() {
			nested, err := hasGitMarker(path)
			if err != nil {
				w.fail(path, err)
				continue
			}
			if nested {
				w.out.Skipped++
				continue
			}
		} else if !entry.Type().IsRegular() || !w.selection.selectsFile(rel) {
			w.out.Skipped++
			continue
		}
		candidates = append(candidates, entry)
		paths = append(paths, path)
	}
	ignored, err := ignoredPaths(ctx, w.worktree, paths)
	if err != nil {
		w.fail(dir, err)
		w.stopped = true
		return
	}
	for n, entry := range candidates {
		if w.canceled(ctx) {
			return
		}
		path := paths[n]
		if ignored[path] {
			w.out.Skipped++
			continue
		}
		if entry.IsDir() {
			if depth >= w.selection.maxDepth {
				w.out.Skipped++
				w.limit("max_depth", false)
				continue
			}
			w.walk(ctx, path, depth+1)
			continue
		}
		w.indexFile(ctx, path)
	}
}

// Enumerate incrementally, charging entries immediately. Never process a
// filesystem-order prefix when the whole directory cannot fit in the budget.
func (w *directoryWalk) entries(ctx context.Context, dir string) ([]os.DirEntry, error) {
	// D2 remains: ancestor replacement between admission and open is not atomic.
	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open directory: %w", err)
	}
	defer f.Close()
	var entries []os.DirEntry
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		remaining := maxDirectoryEntries - w.out.Visited
		if remaining == 0 {
			w.limit("max_entries", true)
			return nil, nil
		}
		batch, err := f.ReadDir(min(256, remaining))
		w.out.Visited += len(batch)
		entries = append(entries, batch...)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read directory: %w", err)
		}
	}
	slices.SortFunc(entries, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, nil
}

func (w *directoryWalk) indexFile(ctx context.Context, path string) {
	if w.out.Attempts >= w.selection.maxFiles {
		w.limit("max_files", true)
		return
	}
	remaining := int64(maxDirectoryBytes) - w.out.BytesRead
	if remaining == 0 {
		w.limit("max_bytes", true)
		return
	}
	// Check again immediately before content open; filtered/denied entries do
	// not consume an attempt. Read errors and unchanged/index-failed files do.
	if !w.admitPath(path) {
		return
	}
	w.out.Attempts++
	rel, err := filepath.Rel(w.out.Root, path)
	if err != nil {
		w.fail(path, err)
		return
	}
	label := path
	if w.out.SourcePrefix != "" {
		label = w.out.SourcePrefix + ":" + filepath.ToSlash(rel)
	}
	out, err := w.indexer.indexAdmittedFile(ctx, path, label, remaining, true)
	w.out.BytesRead += out.BytesRead
	w.out.BytesIndexed += out.BytesIndexed
	if ctx.Err() != nil && out.Result == nil {
		w.fail(path, ctx.Err())
		w.stopped = true
		return
	}
	if errors.Is(err, errDirectoryByteLimit) {
		w.limit("max_bytes", true)
		return
	}
	if err != nil {
		w.fail(path, err)
		if ctx.Err() != nil {
			w.stopped = true
		}
		return
	}
	if out.Result.AlreadyIndexed {
		w.out.Unchanged++
		meta, err := w.indexer.store.GetSourceMeta(label)
		if err != nil || meta == nil {
			if err == nil {
				err = fmt.Errorf("indexed source no longer exists")
			}
			w.fail(path, err)
			return
		}
		w.out.Chunks += meta.ChunkCount
	} else {
		w.out.Indexed++
		w.out.Chunks += out.Result.TotalChunks
	}
}

func (w *directoryWalk) checkPath(path string) error {
	rel, err := filepath.Rel(w.out.Root, path)
	if err != nil {
		return err
	}
	// The explicit root may be an alias with path-specific child denies. Keep
	// that spelling for policy checks, while labels and Git use canonical paths.
	requested := w.requestedRoot + string(filepath.Separator) + rel
	if err := w.indexer.policy.Check(requested); err != nil {
		return err
	}
	return w.indexer.policy.Check(path)
}

func (w *directoryWalk) admitPath(path string) bool {
	err := w.checkPath(path)
	if err == nil {
		return true
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		// Resolution I/O failures are operational errors, not intentional denies.
		w.fail(path, err)
	} else {
		w.out.Denied++
	}
	return false
}
