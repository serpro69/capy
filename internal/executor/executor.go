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
		result, err := e.executeRust(ctx, rt, scriptPath, tmpDir, req)
		if result != nil && result.Backgrounded {
			cleanupTmp = false
		}
		return result, err
	}

	// Working directory: projectDir for shell, tmpDir for others.
	workDir := tmpDir
	if req.Language == Shell {
		workDir = e.projectDir
	}

	result, err := e.runProcess(ctx, bin, args, workDir, tmpDir, req.Background)
	if result != nil && result.Backgrounded {
		cleanupTmp = false
	}
	return result, err
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

	// Wait for the process in a goroutine so we can return early
	// in background mode when the context times out.
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()

	// Monitor context cancellation and hard cap.
	var hardKilled atomic.Bool
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				if !background {
					killProcessGroup(cmd)
				}
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

	// Decide how to wait: in background mode, return early on timeout.
	// In normal mode, wait for the process to finish.
	if background {
		select {
		case waitErr := <-waitDone:
			// Process finished before timeout — normal result.
			return e.buildResult(waitErr, &stdout, &stderr, ctx, hardKilled.Load()), nil
		case <-ctx.Done():
			// Timeout fired while process still running — background it.
			pid := cmd.Process.Pid
			result := &ExecResult{
				Stdout:       SmartTruncate(stdout.String(), e.maxOutputBytes),
				Stderr:       SmartTruncate(stderr.String(), e.maxOutputBytes),
				Backgrounded: true,
				PID:          pid,
			}
			e.trackBackgroundPid(pid)
			// Untrack PID when the process exits to prevent killing
			// unrelated processes if the OS reuses the PID.
			go func() {
				<-waitDone
				e.untrackBackgroundPid(pid)
			}()
			return result, nil
		}
	}

	// Normal (non-background) path: wait for process to finish.
	waitErr := <-waitDone
	return e.buildResult(waitErr, &stdout, &stderr, ctx, hardKilled.Load()), nil
}

func (e *PolyglotExecutor) buildResult(waitErr error, stdout, stderr *safeBuffer, ctx context.Context, hardKilled bool) *ExecResult {
	result := &ExecResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode(waitErr),
	}

	if ctx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
	}

	if hardKilled {
		result.Killed = true
		result.Stderr += "\n[output capped at 100MB — process killed]"
	}

	result.Stdout = SmartTruncate(result.Stdout, e.maxOutputBytes)
	result.Stderr = SmartTruncate(result.Stderr, e.maxOutputBytes)

	return result
}

func (e *PolyglotExecutor) trackBackgroundPid(pid int) {
	e.bgMu.Lock()
	defer e.bgMu.Unlock()
	e.backgroundPids[pid] = struct{}{}
}

func (e *PolyglotExecutor) untrackBackgroundPid(pid int) {
	e.bgMu.Lock()
	defer e.bgMu.Unlock()
	delete(e.backgroundPids, pid)
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
