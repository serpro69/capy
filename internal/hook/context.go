package hook

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/serpro69/capy/internal/adapter"
	"github.com/serpro69/capy/internal/config"
)

// hookContext keeps settings/state ownership separate from cwd-relative rules.
type hookContext struct {
	projectDir string
	workingDir string
}

func resolveHookContext(explicitProjectDir *string, event *adapter.PreToolUseEvent) (hookContext, error) {
	processDir, err := os.Getwd()
	if err != nil {
		return hookContext{}, fmt.Errorf("resolve hook process directory: %w", err)
	}
	workingDir := validatedPayloadCwd(event)
	projectDir := os.Getenv("CLAUDE_PROJECT_DIR")
	if explicitProjectDir != nil {
		projectDir = *explicitProjectDir
		if projectDir == "" {
			return hookContext{}, fmt.Errorf("hook --project-dir must not be empty")
		}
	} else if projectDir == "" {
		startDir := workingDir
		if startDir == "" {
			startDir = processDir
		}
		projectDir = config.DetectProjectRootFrom(startDir)
	}
	if !filepath.IsAbs(projectDir) {
		projectDir = processDir + string(filepath.Separator) + projectDir
	}
	projectDir, err = normalizeHookDirectory(projectDir)
	if err != nil {
		return hookContext{}, fmt.Errorf("invalid hook project directory: %w", err)
	}
	if workingDir == "" {
		workingDir = projectDir
	}
	return hookContext{projectDir: projectDir, workingDir: workingDir}, nil
}

func validatedPayloadCwd(event *adapter.PreToolUseEvent) string {
	if event.InvalidCwd {
		slog.Warn("ignoring invalid hook cwd: expected an absolute directory string")
		return ""
	}
	if event.Cwd == "" {
		return ""
	}
	if !filepath.IsAbs(event.Cwd) {
		slog.Warn("ignoring nonabsolute hook cwd", "cwd", event.Cwd)
		return ""
	}
	cwd, err := normalizeHookDirectory(event.Cwd)
	if err != nil {
		slog.Warn("ignoring invalid hook cwd", "error", err)
		return ""
	}
	return cwd
}

func normalizeHookDirectory(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("directory %q: %w", path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve directory %q: %w", path, err)
	}
	return resolved, nil
}
