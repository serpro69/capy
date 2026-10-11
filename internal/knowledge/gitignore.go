package knowledge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const gitBatchSize = 128

// findWorktree avoids requiring Git for a non-Git tree, but a present marker
// makes any Git discovery failure fatal rather than silently disabling ignores.
func findWorktree(ctx context.Context, root string) (string, error) {
	for dir := root; ; dir = filepath.Dir(dir) {
		found, err := gitDiscoveryMarker(dir)
		if err != nil {
			return "", err
		}
		if found {
			out, _, err := runGit(ctx, root, nil, 64*1024, "rev-parse", "--show-toplevel")
			if err != nil {
				return "", fmt.Errorf("discover Git worktree: %w", err)
			}
			top := strings.TrimSuffix(string(out), "\n")
			top, err = filepath.EvalSymlinks(top)
			if err != nil || !filepath.IsAbs(top) || !withinDirectory(top, root) {
				return "", fmt.Errorf("Git returned an invalid worktree root")
			}
			return top, nil
		}
		if filepath.Dir(dir) == dir {
			return "", nil
		}
	}
}

// Git itself ignores empty .git directories during discovery. A gitfile or a
// directory containing HEAD is a candidate; any subsequent Git error remains
// visible. Nested-root pruning deliberately uses the stricter any-marker rule.
func gitDiscoveryMarker(dir string) (bool, error) {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return true, nil
	}
	_, err = os.Stat(filepath.Join(dir, ".git", "HEAD"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func hasGitMarker(dir string) (bool, error) {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func ignoredPaths(ctx context.Context, worktree string, paths []string) (map[string]bool, error) {
	ignored := make(map[string]bool)
	if worktree == "" {
		return ignored, nil
	}
	for start := 0; start < len(paths); start += gitBatchSize {
		batch := paths[start:min(start+gitBatchSize, len(paths))]
		input := []byte(strings.Join(batch, "\x00") + "\x00")
		out, code, err := runGit(ctx, worktree, input, len(input), "check-ignore", "--no-index", "--stdin", "-z")
		if err != nil && code != 1 {
			return nil, fmt.Errorf("evaluate Git ignores: %w", err)
		}
		if len(out) > 0 && out[len(out)-1] != 0 {
			return nil, fmt.Errorf("invalid Git ignore response")
		}
		for _, path := range bytes.Split(out, []byte{0}) {
			if len(path) > 0 {
				ignored[string(path)] = true
			}
		}
	}
	return ignored, nil
}

// boundedGitOutput drains all output but retains only the command's allowance.
type boundedGitOutput struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedGitOutput) Write(p []byte) (int, error) {
	n := min(len(p), b.limit-b.Len())
	b.Buffer.Write(p[:n])
	b.overflow = b.overflow || n < len(p)
	return len(p), nil
}

func runGit(ctx context.Context, dir string, input []byte, limit int, args ...string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = make([]string, 0, len(os.Environ()))
	for _, value := range os.Environ() {
		// Repository/config routing overrides must not redirect ignore lookup.
		if !strings.HasPrefix(value, "GIT_") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	stdout := &boundedGitOutput{limit: limit}
	stderr := &boundedGitOutput{limit: 4096}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(input), stdout, stderr
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, -1, ctx.Err()
	}
	if stdout.overflow || stderr.overflow {
		return nil, -1, fmt.Errorf("Git output exceeds its bounded allowance")
	}
	if err != nil {
		code := -1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		}
		return stdout.Bytes(), code, fmt.Errorf("git %s: %w (%q)", args[0], err, stderr.String())
	}
	return stdout.Bytes(), 0, nil
}
