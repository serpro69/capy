package executor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var ErrRuntimeUnavailable = errors.New("execution runtime unavailable")

type preparedExecution struct {
	workDir, tmpDir, runtime, scriptPath, bin string
	args                                      []string
}

// prepare shares runtime, cwd, source wrapping and environment-path semantics
// between legacy execution and the new supervisor path.
func (e *PolyglotExecutor) prepare(req ExecRequest) (p preparedExecution, err error) {
	p.workDir, err = ResolveCwd(e.projectDir, req.Cwd)
	if err != nil {
		return p, err
	}
	var ok bool
	p.runtime, ok = e.Runtimes()[req.Language]
	if !ok {
		return p, fmt.Errorf("%w for language %q", ErrRuntimeUnavailable, req.Language)
	}
	p.tmpDir, err = os.MkdirTemp("", "capy-exec-*")
	if err != nil {
		return p, fmt.Errorf("creating temp dir: %w", err)
	}
	defer func() {
		if err != nil {
			os.RemoveAll(p.tmpDir)
		}
	}()
	absTmpDir, err := filepath.Abs(p.tmpDir)
	if err != nil {
		return p, fmt.Errorf("resolving temp dir: %w", err)
	}
	p.tmpDir = absTmpDir
	code := autoWrap(req.Language, req.Code, p.workDir)
	p.scriptPath = filepath.Join(p.tmpDir, scriptFilenames[req.Language])
	perm := os.FileMode(0o600)
	if req.Language == Shell {
		perm = 0o700
		code = buildShellScript(code, os.Getenv("PATH"))
	}
	if err = os.WriteFile(p.scriptPath, []byte(code), perm); err != nil {
		return p, fmt.Errorf("writing script: %w", err)
	}
	p.bin, p.args = buildCommand(req.Language, p.runtime, p.scriptPath)
	return p, nil
}
