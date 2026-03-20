package executor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// PolyglotExecutor runs code in sandboxed subprocesses.
type PolyglotExecutor struct {
	runtimes       map[Language]string
	projectDir     string
	maxOutputBytes int
	detectOnce     sync.Once

	bgMu           sync.Mutex
	backgroundPids map[int]struct{}
}

// NewExecutor creates a new PolyglotExecutor.
func NewExecutor(projectDir string, maxOutputBytes int) *PolyglotExecutor {
	if maxOutputBytes <= 0 {
		maxOutputBytes = MaxOutputBytes
	}
	return &PolyglotExecutor{
		projectDir:     projectDir,
		maxOutputBytes: maxOutputBytes,
		backgroundPids: make(map[int]struct{}),
	}
}

// Runtimes returns the detected runtime map (triggers detection if needed).
func (e *PolyglotExecutor) Runtimes() map[Language]string {
	e.detectOnce.Do(func() {
		e.runtimes = detectRuntimes()
	})
	return e.runtimes
}

// Execute runs code in a sandboxed subprocess.
func (e *PolyglotExecutor) Execute(ctx context.Context, req ExecRequest) (*ExecResult, error) {
	runtimes := e.Runtimes()
	rt, ok := runtimes[req.Language]
	if !ok {
		return nil, fmt.Errorf("no runtime found for language %q", req.Language)
	}

	// Timeout.
	timeout := time.Duration(req.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Temp directory.
	tmpDir, err := os.MkdirTemp("", "capy-exec-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			os.RemoveAll(tmpDir)
		}
	}()

	// Apply auto-wrapping.
	code := autoWrap(req.Language, req.Code, e.projectDir)

	// Write script file.
	filename := scriptFilenames[req.Language]
	scriptPath := filepath.Join(tmpDir, filename)
	perm := os.FileMode(0o644)
	if req.Language == Shell {
		perm = 0o700
	}
	if err := os.WriteFile(scriptPath, []byte(code), perm); err != nil {
		return nil, fmt.Errorf("writing script: %w", err)
	}

	// Build command.
	bin, args := buildCommand(req.Language, rt, scriptPath)

	// Rust special case: compile then run.
	if bin == "__rust_compile_run__" {
		return e.executeRust(ctx, rt, scriptPath, tmpDir, req)
	}

	// Working directory: projectDir for shell, tmpDir for others.
	workDir := tmpDir
	if req.Language == Shell {
		workDir = e.projectDir
	}

	return e.runProcess(ctx, bin, args, workDir, tmpDir, req.Background)
}

// ExecuteFile resolves a file path and injects FILE_CONTENT boilerplate.
func (e *PolyglotExecutor) ExecuteFile(ctx context.Context, req ExecRequest) (*ExecResult, error) {
	absPath, err := filepath.Abs(req.FilePath)
	if err != nil {
		return nil, fmt.Errorf("resolving file path: %w", err)
	}
	req.Code = injectFileContent(req.Language, req.Code, absPath)
	return e.Execute(ctx, req)
}

func (e *PolyglotExecutor) executeRust(ctx context.Context, rustc, srcPath, tmpDir string, req ExecRequest) (*ExecResult, error) {
	binPath := filepath.Join(tmpDir, "main")

	// Compile.
	compileCmd := exec.CommandContext(ctx, rustc, srcPath, "-o", binPath)
	compileCmd.Dir = tmpDir
	compileCmd.Env = BuildSafeEnv(tmpDir)
	var compileErr bytes.Buffer
	compileCmd.Stderr = &compileErr
	if err := compileCmd.Run(); err != nil {
		return &ExecResult{
			Stderr:   compileErr.String(),
			ExitCode: exitCode(err),
		}, nil
	}

	// Run.
	return e.runProcess(ctx, binPath, nil, tmpDir, tmpDir, req.Background)
}

func (e *PolyglotExecutor) runProcess(ctx context.Context, bin string, args []string, workDir, tmpDir string, background bool) (*ExecResult, error) {
	// Don't use exec.CommandContext — it only kills the process, not the
	// process group. We manage timeout ourselves via the context + SIGKILL
	// to the entire process group.
	cmd := exec.Command(bin, args...)
	cmd.Dir = workDir
	cmd.Env = BuildSafeEnv(tmpDir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Thread-safe buffers: the monitoring goroutine reads Size() while
	// exec.Cmd's internal goroutines write concurrently.
	var stdout, stderr safeBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting process: %w", err)
	}

	// Monitor context cancellation and hard cap in a background goroutine.
	var (
		timedOut   atomic.Bool
		hardKilled atomic.Bool
	)
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				timedOut.Store(true)
				if !background {
					killProcessGroup(cmd)
				}
				// In background mode, don't kill — let the process run
				// detached. The caller is responsible for cleanup via
				// CleanupBackgrounded().
				return
			case <-ticker.C:
				total := stdout.Size() + stderr.Size()
				if total > int64(HardCapBytes) {
					hardKilled.Store(true)
					killProcessGroup(cmd)
					return
				}
			}
		}
	}()

	err := cmd.Wait()
	close(done)

	result := &ExecResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode(err),
	}

	if timedOut.Load() {
		if background {
			result.Backgrounded = true
			result.PID = cmd.Process.Pid
			e.trackBackgroundPid(cmd.Process.Pid)
		} else {
			result.TimedOut = true
		}
	}

	if hardKilled.Load() {
		result.Killed = true
		result.Stderr += "\n[output capped at 100MB — process killed]"
	}

	// Smart truncation.
	result.Stdout = SmartTruncate(result.Stdout, e.maxOutputBytes)
	result.Stderr = SmartTruncate(result.Stderr, e.maxOutputBytes)

	return result, nil
}

func (e *PolyglotExecutor) trackBackgroundPid(pid int) {
	e.bgMu.Lock()
	defer e.bgMu.Unlock()
	e.backgroundPids[pid] = struct{}{}
}

// CleanupBackgrounded kills all tracked background processes.
func (e *PolyglotExecutor) CleanupBackgrounded() {
	e.bgMu.Lock()
	defer e.bgMu.Unlock()
	for pid := range e.backgroundPids {
		syscall.Kill(-pid, syscall.SIGTERM)
	}
	e.backgroundPids = make(map[int]struct{})
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return -1
}
