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

// ProjectHash returns a deterministic 16-hex-char hash of the absolute project path.
func ProjectHash(dir string) string {
	abs, _ := filepath.Abs(dir)
	h := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(h[:8])
}

// ClaudeProjectsDir returns the path to ~/.claude/projects/.
func ClaudeProjectsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

// ProjectDirResolution holds the result of resolving a --project-dir value.
type ProjectDirResolution struct {
	// SourceDir is the original project directory on disk. Empty when the
	// source could not be recovered (e.g. project was renamed/deleted).
	SourceDir string
	// SessionDir is the Claude Code session directory, set only when the
	// input was detected as a session directory path.
	SessionDir string
	// IsSessionDir is true when the input was under ~/.claude/projects/.
	IsSessionDir bool
}

// ResolveSourceProject normalizes a --project-dir value. If the given path is
// a Claude Code session directory (under ~/.claude/projects/), it recovers the
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
// segments with "-" and checking which combinations correspond to existing
// directories. Tries shortest segments first with backtracking so that
// literal dashes in directory names (e.g. "claude-starter-kit") are handled.
//
// Uses os.Stat (not os.ReadDir) because parent directories may not be
// listable even when children are accessible — e.g. /var/folders/ on macOS.
func unmangledProbe(prefix string, parts []string) string {
	if len(parts) == 0 {
		return prefix
	}
	for i := 1; i <= len(parts); i++ {
		segment := strings.Join(parts[:i], "-")

		candidate := filepath.Join(prefix, segment)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			if i == len(parts) {
				return candidate
			}
			if result := unmangledProbe(candidate, parts[i:]); result != "" {
				return result
			}
		}

		// A dot-prefixed directory like ".hidden" mangles to "-hidden",
		// colliding with a path separator. Try the dot variant for the
		// first segment only (dots only appear at the start of a name).
		if i == 1 {
			dotCandidate := filepath.Join(prefix, "."+segment)
			if info, err := os.Stat(dotCandidate); err == nil && info.IsDir() {
				if i == len(parts) {
					return dotCandidate
				}
				if result := unmangledProbe(dotCandidate, parts[i:]); result != "" {
					return result
				}
			}
		}
	}
	return ""
}

// ResolveDBPath returns the path to the SQLite knowledge base.
// If Config.Store.Path is set, it is resolved relative to projectDir.
// Otherwise, the default XDG data location is used.
func (c *Config) ResolveDBPath(projectDir string) string {
	if c.Store.Path != "" {
		if filepath.IsAbs(c.Store.Path) {
			return c.Store.Path
		}
		return filepath.Join(projectDir, c.Store.Path)
	}

	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, _ := os.UserHomeDir()
		dataHome = filepath.Join(home, ".local", "share")
	}
	hash := ProjectHash(projectDir)
	return filepath.Join(dataHome, "capy", hash, "knowledge.db")
}
