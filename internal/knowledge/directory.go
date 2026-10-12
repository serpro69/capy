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
// canonical directory. Opt-in descendant following keeps targets in that root.
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
	w := directoryWalk{
		indexer: i, selection: selection, worktree: worktree, requestedRoot: requested, out: &out,
		directories: make(map[string]int), files: make(map[string]bool),
	}
	w.walk(ctx, root, requested, 0)
	// Store writes and final metadata reads can finish after cancellation.
	// Preserve committed progress, but never describe that scan as complete.
	if err := ctx.Err(); err != nil && !errors.Is(w.err, err) {
		out.fail(root, err)
		w.err = errors.Join(w.err, err)
	}
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
	directories   map[string]int // canonical path -> shallowest admitted walk depth
	files         map[string]bool
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

func (w *directoryWalk) walk(ctx context.Context, dir, alias string, depth int) {
	if w.canceled(ctx) {
		return
	}
	if previous, seen := w.directories[dir]; seen && previous <= depth {
		w.out.Skipped++
		return
	}
	w.directories[dir] = depth
	denied, failed := w.out.Denied, w.out.Failed
	defer func() {
		// An alias-specific denial must not hide an otherwise readable target
		// when it is later reached through its ordinary path. Entries still
		// consume the shared work budget; files are deduplicated separately.
		if w.out.Denied != denied || w.out.Failed != failed {
			delete(w.directories, dir)
		}
	}()
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
	type candidate struct {
		path, alias, encountered string
		isDir                    bool
		depth                    int
	}
	candidates := make([]candidate, 0, len(entries))
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if w.canceled(ctx) {
			return
		}
		path := filepath.Join(dir, entry.Name())
		encountered := path // physical parent + entry; safe for Git even for a link
		aliasPath := alias + string(filepath.Separator) + entry.Name()
		rel, err := filepath.Rel(w.out.Root, path)
		if err != nil {
			w.fail(path, err)
			continue
		}
		rel = filepath.ToSlash(rel)
		link := entry.Type()&os.ModeSymlink != 0
		if mandatoryExcluded(rel) || (link && !w.selection.followSymlinks) {
			w.out.Skipped++
			continue
		}
		if link {
			path, err = filepath.EvalSymlinks(path)
			if err != nil {
				w.fail(encountered, err)
				continue
			}
		}
		if !w.admitAlias(encountered, aliasPath) {
			continue
		}
		isDir, regular := entry.IsDir(), entry.Type().IsRegular()
		entryDepth := depth
		if link {
			if !withinDirectory(w.out.Root, path) {
				w.out.Skipped++
				continue
			}
			rel, err = filepath.Rel(w.out.Root, path)
			if err != nil {
				w.fail(path, err)
				continue
			}
			rel = filepath.ToSlash(rel)
			info, err := os.Stat(path)
			if err != nil {
				w.fail(path, err)
				continue
			}
			isDir, regular = info.IsDir(), info.Mode().IsRegular()
			// A shallow alias cannot bypass the canonical target's depth cap.
			entryDepth = max(depth, strings.Count(rel, "/"))
			if !w.admitTarget(path, rel, isDir, entryDepth) {
				continue
			}
		}
		if matchesAny(w.selection.exclude, rel) {
			w.out.Skipped++
			continue
		}
		if isDir {
			nested, err := hasGitMarker(path)
			if err != nil {
				w.fail(path, err)
				continue
			}
			if nested {
				w.out.Skipped++
				continue
			}
		} else if !regular || !w.selection.selectsFile(rel) {
			w.out.Skipped++
			continue
		}
		candidates = append(candidates, candidate{path, aliasPath, encountered, isDir, entryDepth})
		paths = append(paths, path)
		if link {
			// Never pass alias/child to Git: it rejects paths beyond a
			// directory symlink. Check only the link entry and real target.
			paths = append(paths, encountered)
		}
	}
	ignored, err := ignoredPaths(ctx, w.worktree, paths)
	if err != nil {
		w.fail(dir, err)
		w.stopped = true
		return
	}
	for _, entry := range candidates {
		if w.canceled(ctx) {
			return
		}
		path := entry.path
		if ignored[path] || ignored[entry.encountered] {
			w.out.Skipped++
			continue
		}
		if entry.isDir {
			if entry.depth >= w.selection.maxDepth {
				w.out.Skipped++
				w.limit("max_depth", false)
				continue
			}
			w.walk(ctx, path, entry.alias, entry.depth+1)
			continue
		}
		w.indexFile(ctx, path, entry.alias)
	}
}

// A followed target can jump over directories the ordinary walk would prune.
// Check those canonical ancestors before Git sees the target or content opens.
func (w *directoryWalk) admitTarget(path, rel string, isDir bool, depth int) bool {
	if mandatoryExcluded(rel) {
		w.out.Skipped++
		return false
	}
	if depth > w.selection.maxDepth {
		w.out.Skipped++
		w.limit("max_depth", false)
		return false
	}
	for current := path; current != w.out.Root; current = filepath.Dir(current) {
		relative, err := filepath.Rel(w.out.Root, current)
		if err != nil {
			w.fail(current, err)
			return false
		}
		if matchesAny(w.selection.exclude, filepath.ToSlash(relative)) {
			w.out.Skipped++
			return false
		}
		if !w.admitPath(current) {
			return false
		}
		if current == path && !isDir {
			continue
		}
		nested, err := hasGitMarker(current)
		if err != nil {
			w.fail(current, err)
			return false
		}
		if nested {
			w.out.Skipped++
			return false
		}
	}
	return true
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

func (w *directoryWalk) indexFile(ctx context.Context, path, alias string) {
	if w.files[path] {
		w.out.Skipped++
		return
	}
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
	if !w.admitAlias(path, alias) {
		return
	}
	w.files[path] = true
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
	if err := ctx.Err(); err != nil {
		w.fail(path, err)
		w.stopped = true
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
	return w.admissionResult(path, w.checkPath(path))
}

func (w *directoryWalk) admitAlias(path, alias string) bool {
	// Retain descendant aliases under both spellings of an explicit root link.
	// Canonicalizing the entire path would erase those lexical Read denies.
	rel := strings.TrimPrefix(alias, w.requestedRoot+string(filepath.Separator))
	rootAlias := w.out.Root + string(filepath.Separator) + rel
	return w.admissionResult(alias, w.indexer.policy.Check(alias)) &&
		w.admissionResult(rootAlias, w.indexer.policy.Check(rootAlias)) && w.admitPath(path)
}

func (w *directoryWalk) admissionResult(path string, err error) bool {
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
