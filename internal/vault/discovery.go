package vault

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/serpro69/capy/internal/config"
)

// maxSidecarBytes caps a single non-subagent sidecar file (tool-results, build
// logs, screenshots — all reproducible). Larger ones are skipped during
// discovery with a warning. The cap NEVER applies to the main session JSONL
// (always stored uncapped) nor to subagents/*.jsonl (irreproducible
// conversation content). See design.md DB Size Projection.
const maxSidecarBytes = 5 * 1024 * 1024

// SessionFile is one discovered session: its main JSONL plus every sidecar in
// the matching <uuid>/ directory.
type SessionFile struct {
	Path            string           // full path to the main <uuid>.jsonl
	UUID            string           // extracted from the filename (minus .jsonl)
	ProjectDir      string           // mangled project-dir name (or, for loose imports, the containing dir basename)
	AssociatedFiles []AssociatedFile // every file under <uuid>/ (subagents, tool-results, sidecars)
}

// AssociatedFile is one sidecar from a session directory.
type AssociatedFile struct {
	AbsPath      string // full path on disk
	RelativePath string // path relative to <uuid>/ (e.g. "subagents/agent-abc.jsonl")
}

// DiscoverSessions finds all session JSONL files (and their sidecar
// directories) reachable from rootDir. An empty rootDir resolves to Claude
// Code's projects directory (config.ClaudeProjectsDir, honoring
// CLAUDE_CONFIG_DIR). The input type is auto-detected (see detectProjectDirs):
//
//   - a Claude config dir (contains projects/)  → walk every project under projects/
//   - a projects/ root (subdirs hold *.jsonl)    → walk every project subdir
//   - a single project dir (holds *.jsonl)       → walk just that dir
//
// Sidecars larger than maxSidecarBytes are skipped (subagent JSONLs and the
// main JSONL are never skipped). Results are sorted by path for determinism.
func DiscoverSessions(rootDir string) ([]SessionFile, error) {
	if rootDir == "" {
		resolved, err := config.ClaudeProjectsDir()
		if err != nil {
			return nil, fmt.Errorf("resolving claude projects dir: %w", err)
		}
		rootDir = resolved
	}

	projectDirs, err := detectProjectDirs(rootDir)
	if err != nil {
		return nil, err
	}

	var sessions []SessionFile
	for _, projDir := range projectDirs {
		found, err := discoverProject(projDir)
		if err != nil {
			// Log and continue: one unreadable project dir must not abort discovery.
			slog.Warn("vault discovery: skipping project directory", "dir", projDir, "error", err)
			continue
		}
		sessions = append(sessions, found...)
	}

	sort.Slice(sessions, func(i, j int) bool { return sessions[i].Path < sessions[j].Path })
	return sessions, nil
}

// ProjectSessionDir resolves the Claude Code session directory for a real
// project path, scoping discovery to a single project (the server-startup
// sweep's "current project only" path). It honors CLAUDE_CONFIG_DIR via
// config.ClaudeProjectsDir() — unlike session.SessionDir, which still hardcodes
// ~/.claude/projects/ (a deferred follow-up in docs/wip/vault/tasks.md). When
// projectDir is already inside the projects root (i.e. it is itself a mangled
// session dir) it is returned unchanged; otherwise the absolute path is mangled
// (/ and . → -) to Claude Code's directory convention. The returned path is not
// stat-checked — DiscoverSessions reports a missing directory, and callers (the
// sweep) skip it gracefully.
func ProjectSessionDir(projectDir string) (string, error) {
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		return "", fmt.Errorf("resolving absolute path: %w", err)
	}
	projects, err := config.ClaudeProjectsDir()
	if err != nil {
		return "", fmt.Errorf("resolving claude projects dir: %w", err)
	}
	if strings.HasPrefix(abs, projects+string(filepath.Separator)) {
		return abs, nil
	}
	mangled := strings.NewReplacer("/", "-", ".", "-").Replace(abs)
	return filepath.Join(projects, mangled), nil
}

// detectProjectDirs classifies rootDir and returns the list of project
// directories to walk. The checks are mutually exclusive in practice: a single
// project dir holds *.jsonl files directly, while a projects root holds only
// project subdirs (whose own children include <uuid>/ session dirs, not
// top-level *.jsonl).
func detectProjectDirs(rootDir string) ([]string, error) {
	info, err := os.Stat(rootDir)
	if err != nil {
		return nil, fmt.Errorf("reading input path: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("input path is not a directory: %q", rootDir)
	}

	// Claude config dir: contains a projects/ subdirectory.
	if sub := filepath.Join(rootDir, "projects"); isDir(sub) {
		return childDirs(sub)
	}

	// Single project dir: holds *.jsonl files directly.
	if has, err := hasJSONLFiles(rootDir); err != nil {
		return nil, err
	} else if has {
		return []string{rootDir}, nil
	}

	// projects/ root: child dirs that directly hold *.jsonl files.
	dirs, err := childDirs(rootDir)
	if err != nil {
		return nil, err
	}
	var projectDirs []string
	for _, d := range dirs {
		has, err := hasJSONLFiles(d)
		if err != nil {
			// Log and continue: one unreadable child must not silently drop the
			// rest, nor surface only as a misleading "no session files" error.
			slog.Warn("vault discovery: skipping unreadable project directory", "dir", d, "error", err)
			continue
		}
		if has {
			projectDirs = append(projectDirs, d)
		}
	}
	if len(projectDirs) == 0 {
		return nil, fmt.Errorf("no session files found under %q", rootDir)
	}
	return projectDirs, nil
}

// discoverProject lists the main <uuid>.jsonl files in a single project dir and
// collects each one's sidecar files from its <uuid>/ directory.
func discoverProject(projectDir string) ([]SessionFile, error) {
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return nil, fmt.Errorf("listing project dir: %w", err)
	}
	projectName := filepath.Base(projectDir)

	var sessions []SessionFile
	for _, e := range entries {
		// Only regular files are real session JSONLs. e.Type().IsRegular() is
		// false for directories AND symlinks — a symlink named "<uuid>.jsonl"
		// (whose target os.ReadFile would blindly follow) must not be treated as
		// a session file.
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		uuid := strings.TrimSuffix(e.Name(), ".jsonl")
		sf := SessionFile{
			Path:       filepath.Join(projectDir, e.Name()),
			UUID:       uuid,
			ProjectDir: projectName,
		}
		sf.AssociatedFiles = collectAssociatedFiles(filepath.Join(projectDir, uuid))
		sessions = append(sessions, sf)
	}
	return sessions, nil
}

// collectAssociatedFiles walks the <uuid>/ session directory recursively and
// returns every file, relative to that directory. A non-subagent file larger
// than maxSidecarBytes is skipped with a warning; subagent JSONLs are always
// kept. A missing or unreadable directory yields no files (not an error) — many
// sessions have no sidecars.
func collectAssociatedFiles(sessionDir string) []AssociatedFile {
	var files []AssociatedFile
	err := filepath.WalkDir(sessionDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Skip non-regular entries (symlinks, devices, sockets, FIFOs). For a
		// symlink WalkDir reports d.Info().Size() as the link-target *path*
		// length, not the target's size — so the maxSidecarBytes cap below would
		// be silently bypassed, and os.ReadFile (readSidecars) would then follow
		// the link, reading an arbitrary or oversized target into the vault.
		if !d.Type().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(sessionDir, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)

		if !isSubagentJSONL(rel) {
			if info, statErr := d.Info(); statErr == nil && info.Size() > maxSidecarBytes {
				slog.Warn("vault discovery: skipping oversize sidecar file",
					"file", rel, "size", info.Size(), "max_bytes", maxSidecarBytes)
				return nil
			}
		}
		files = append(files, AssociatedFile{AbsPath: path, RelativePath: rel})
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		slog.Warn("vault discovery: partial sidecar walk", "dir", sessionDir, "error", err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].RelativePath < files[j].RelativePath })
	return files
}

// isSubagentJSONL reports whether a session-relative path is a subagent
// transcript (subagents/*.jsonl) — irreproducible conversation content that is
// never size-capped.
func isSubagentJSONL(rel string) bool {
	return strings.HasPrefix(rel, "subagents/") && strings.HasSuffix(rel, ".jsonl")
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// childDirs returns the immediate subdirectories of dir, as full paths.
func childDirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("listing directory: %w", err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(dir, e.Name()))
		}
	}
	return dirs, nil
}

// hasJSONLFiles reports whether dir directly contains at least one *.jsonl file.
func hasJSONLFiles(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, fmt.Errorf("listing directory: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			return true, nil
		}
	}
	return false, nil
}
