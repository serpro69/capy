package config

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
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
