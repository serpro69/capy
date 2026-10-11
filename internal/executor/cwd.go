package executor

import (
	"fmt"
	"os"
	"path/filepath"
)

// ResolveCwd returns an existing, absolute execution directory. An empty cwd
// selects projectDir; relative overrides are anchored there. This does not
// change project identity or admit any ExecuteFile input path.
func ResolveCwd(projectDir, cwd string) (string, error) {
	if cwd == "" {
		cwd = "."
	}
	if !filepath.IsAbs(cwd) && projectDir != "" {
		// Joining/cleaning first would change the meaning of symlink/.. paths.
		cwd = projectDir + string(filepath.Separator) + cwd
	}
	if !filepath.IsAbs(cwd) {
		processDir, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve cwd: %w", err)
		}
		cwd = processDir + string(filepath.Separator) + cwd
	}
	info, err := os.Stat(cwd)
	if err != nil {
		return "", fmt.Errorf("invalid cwd %q: %w", cwd, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("invalid cwd %q: not a directory", cwd)
	}
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve cwd %q: %w", cwd, err)
	}
	return resolved, nil
}
