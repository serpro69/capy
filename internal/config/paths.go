package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DetectProjectRoot finds the project root directory using:
//  1. CLAUDE_PROJECT_DIR env var
//  2. git rev-parse --show-toplevel
//  3. Walk up from cwd looking for .git/, .capy.toml, .capy/
//  4. Fallback: cwd
func DetectProjectRoot() string {
	if dir := os.Getenv("CLAUDE_PROJECT_DIR"); dir != "" {
		return dir
	}

	if out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output(); err == nil {
		// Trim trailing newline.
		s := string(out)
		if len(s) > 0 && s[len(s)-1] == '\n' {
			s = s[:len(s)-1]
		}
		return s
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}

	dir := cwd
	for {
		for _, marker := range []string{".git", ".capy.toml", ".capy"} {
			if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return cwd
}

// MainWorktreeDir returns the main worktree of the git repository that
// contains projectDir. For a normal checkout, the main worktree itself, or a
// non-git directory, it returns projectDir unchanged. For a linked git
// worktree — identified by a .git that is a regular FILE rather than a
// directory — it follows the file's "gitdir:" pointer to the common git
// directory and returns that directory's parent.
//
// It never invokes git: the worktree layout is read directly from disk, which
// is more robust than relying on a committed marker file and works even when
// git is unavailable. Any malformed or unexpected layout falls back to
// returning projectDir, so a failure to resolve never breaks DB path
// resolution — it only forgoes the worktree redirect.
func MainWorktreeDir(projectDir string) string {
	dotGit := filepath.Join(projectDir, ".git")
	info, err := os.Stat(dotGit)
	if err != nil || info.IsDir() {
		// No .git, or a normal repo / main worktree (.git is a directory).
		return projectDir
	}

	// A linked worktree's .git is a file: "gitdir: <worktree git dir>".
	data, err := os.ReadFile(dotGit)
	if err != nil {
		return projectDir
	}
	content := strings.TrimSpace(string(data))
	if !strings.HasPrefix(content, "gitdir:") {
		return projectDir
	}
	gitDir := strings.TrimSpace(strings.TrimPrefix(content, "gitdir:"))
	if gitDir == "" {
		return projectDir
	}
	if !filepath.IsAbs(gitDir) {
		// A relative gitdir is relative to the dir holding .git — always projectDir.
		gitDir = filepath.Join(projectDir, gitDir)
	}

	// A linked worktree's git dir always lives at "<common>/worktrees/<name>".
	// Anything else — most notably a submodule's "<super>/.git/modules/<name>" —
	// is not a worktree and must keep its own database.
	if filepath.Base(filepath.Dir(gitDir)) != "worktrees" {
		return projectDir
	}

	main := filepath.Dir(commonGitDir(gitDir)) // parent of "<main>/.git"
	if mainInfo, err := os.Stat(main); err != nil || !mainInfo.IsDir() {
		return projectDir
	}
	return main
}

// commonGitDir resolves the shared (main) .git directory for a linked
// worktree's git directory. Git records the relative path to the common
// directory in a "commondir" file; when that is missing or empty it falls back
// to stripping the trailing "worktrees/<name>" segments.
func commonGitDir(worktreeGitDir string) string {
	if data, err := os.ReadFile(filepath.Join(worktreeGitDir, "commondir")); err == nil {
		rel := strings.TrimSpace(string(data))
		if rel != "" {
			if filepath.IsAbs(rel) {
				return filepath.Clean(rel)
			}
			return filepath.Clean(filepath.Join(worktreeGitDir, rel))
		}
	}
	// Fallback: "<common>/worktrees/<name>" -> "<common>".
	return filepath.Dir(filepath.Dir(worktreeGitDir))
}

// ProjectHash returns a deterministic 16-hex-char hash of the absolute project path.
func ProjectHash(dir string) string {
	abs, _ := filepath.Abs(dir)
	h := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(h[:8])
}

// ClaudeProjectsDir returns the path to Claude Code's projects directory.
// It honors CLAUDE_CONFIG_DIR (using $CLAUDE_CONFIG_DIR/projects/) for
// non-default installations, falling back to ~/.claude/projects/.
func ClaudeProjectsDir() (string, error) {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "projects"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

// UnmanglePath recovers the original filesystem path from a Claude Code mangled
// directory name (where "/" and "." are replaced with "-") by probing the
// filesystem. Returns "" if the path cannot be determined (e.g. the project no
// longer exists on disk, or the name has no leading dash). It is the exported
// entry point for the internal unmangle/probe machinery.
func UnmanglePath(mangled string) string {
	return unmanglePath(mangled)
}

// ProjectDirResolution holds the result of resolving a --project-dir value.
type ProjectDirResolution struct {
	// SourceDir is the original project directory on disk. Empty when the
	// source could not be recovered (e.g. project was renamed/deleted).
	SourceDir string
	// SessionDir is the Claude Code session directory, set only when the
	// input was detected as a session directory path.
	SessionDir string
	// IsSessionDir is true when the input was under the Claude projects root
	// (see config.ClaudeProjectsDir, which honors CLAUDE_CONFIG_DIR).
	IsSessionDir bool
}

// ResolveSourceProject normalizes a --project-dir value. If the given path is
// a Claude Code session directory (under the Claude projects root), it recovers the
// original project path by probing the filesystem. When the source project no
// longer exists on disk, the session directory is still returned so that sweep
// can operate on orphaned sessions.
func ResolveSourceProject(projectDir string) (ProjectDirResolution, error) {
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		return ProjectDirResolution{}, fmt.Errorf("resolving absolute path: %w", err)
	}

	claudeDir, err := ClaudeProjectsDir()
	if err != nil {
		return ProjectDirResolution{}, fmt.Errorf("resolving claude projects dir: %w", err)
	}

	prefix := claudeDir + string(filepath.Separator)
	if !strings.HasPrefix(abs, prefix) {
		return ProjectDirResolution{SourceDir: abs}, nil
	}

	rest := abs[len(prefix):]
	mangled, _, _ := strings.Cut(rest, string(filepath.Separator))
	sessDir := filepath.Join(claudeDir, mangled)

	source := unmanglePath(mangled)
	return ProjectDirResolution{
		SourceDir:    source,
		SessionDir:   sessDir,
		IsSessionDir: true,
	}, nil
}

// unmanglePath attempts to recover the original filesystem path from a
// Claude Code mangled directory name (where / and . are replaced with -).
// Returns "" if the original path cannot be determined.
func unmanglePath(mangled string) string {
	if !strings.HasPrefix(mangled, "-") {
		return ""
	}
	parts := strings.Split(mangled[1:], "-")
	return unmangledProbe("/", parts)
}

// unmangledProbe recursively builds a filesystem path by re-joining mangled
// segments and checking which combinations correspond to existing directories.
// Tries shortest segments first with backtracking so that literal dashes and
// dots in directory names are handled correctly.
//
// Each "-" in the mangled name could originally have been "/", ".", or a
// literal "-". For each grouping of parts the function tries "-" (literal
// dash), "." (interior dot, e.g. "my.repo"), and "." as a leading prefix
// (hidden dirs like ".config"). Uses os.Stat rather than os.ReadDir because
// parent directories may not be listable on macOS (/var/folders/).
func unmangledProbe(prefix string, parts []string) string {
	if len(parts) == 0 {
		return prefix
	}
	for i := 1; i <= len(parts); i++ {
		dashSegment := strings.Join(parts[:i], "-")

		candidates := []string{filepath.Join(prefix, dashSegment)}

		if i > 1 {
			dotSegment := strings.Join(parts[:i], ".")
			candidates = append(candidates, filepath.Join(prefix, dotSegment))
		}

		// A dot-prefixed directory like ".hidden" mangles to "-hidden",
		// colliding with a path separator.
		if i == 1 {
			candidates = append(candidates, filepath.Join(prefix, "."+dashSegment))
		}

		for _, candidate := range candidates {
			info, err := os.Stat(candidate)
			if err != nil || !info.IsDir() {
				continue
			}
			if i == len(parts) {
				return candidate
			}
			if result := unmangledProbe(candidate, parts[i:]); result != "" {
				return result
			}
		}
	}
	return ""
}

// DBProjectDir returns the project directory whose knowledge database the
// session at projectDir should use. When store.path is project-scoped (a
// relative path stored inside the project tree) and projectDir is a linked git
// worktree, it returns the repository's main worktree so that every worktree
// of a repo shares a single database (see issue #69 — a per-worktree copy of a
// committed DB strands its writes and conflicts unresolvably on merge).
// Absolute store paths are already shared, and the XDG default is keyed per
// project, so both are returned unchanged.
func (c *Config) DBProjectDir(projectDir string) string {
	if c.Store.Path != "" && !filepath.IsAbs(c.Store.Path) {
		return MainWorktreeDir(projectDir)
	}
	return projectDir
}

// ResolveDBPath returns the path to the SQLite knowledge base.
// If Config.Store.Path is set, it is resolved relative to DBProjectDir(projectDir).
// Otherwise, the default XDG data location is used.
func (c *Config) ResolveDBPath(projectDir string) string {
	if c.Store.Path != "" {
		if filepath.IsAbs(c.Store.Path) {
			return c.Store.Path
		}
		return filepath.Join(c.DBProjectDir(projectDir), c.Store.Path)
	}

	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, _ := os.UserHomeDir()
		dataHome = filepath.Join(home, ".local", "share")
	}
	hash := ProjectHash(projectDir)
	return filepath.Join(dataHome, "capy", hash, "knowledge.db")
}
