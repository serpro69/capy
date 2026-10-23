package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// PolyglotExecutor runs code in sandboxed subprocesses.
type PolyglotExecutor struct {
	runtimes       map[Language]string
	projectDir     string
	maxOutputBytes int
	detectOnce     sync.Once
	observeExit    func(int) error // fixed at construction; replaceable in tests

	bgMu           sync.Mutex
	backgroundPids map[int]*backgroundProcess
}

type backgroundProcess struct {
	cmd            *exec.Cmd
	stdout, stderr *safeBuffer
}

// NewExecutor creates a new PolyglotExecutor.
func NewExecutor(projectDir string, maxOutputBytes int) *PolyglotExecutor {
	if maxOutputBytes <= 0 {
		maxOutputBytes = MaxOutputBytes
	}
	return &PolyglotExecutor{
		projectDir:     projectDir,
		maxOutputBytes: maxOutputBytes,
		observeExit:    waitForExit,
		backgroundPids: make(map[int]*backgroundProcess),
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
	p, err := e.prepare(req)
	if err != nil {
		return nil, err
	}
	req.Cwd = p.workDir

	// Timeout.
	timeout := time.Duration(req.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			os.RemoveAll(p.tmpDir)
		}
	}()

	// Rust special case: compile then run.
	if p.bin == "__rust_compile_run__" {
		result, err := e.executeRust(ctx, p.runtime, p.scriptPath, p.tmpDir, req)
		if result != nil && result.Backgrounded {
			cleanupTmp = false
		}
		return result, err
	}

	// Scripts and TMPDIR stay isolated; relative user I/O and runtime project
	// discovery use the request's execution directory for every language.
	result, err := e.runProcess(ctx, p.bin, p.args, p.workDir, p.tmpDir, req)
	if result != nil && result.Backgrounded {
		cleanupTmp = false
	}
	return result, err
}

// ExecuteFile resolves relative paths from the project and injects FILE_CONTENT
// boilerplate. Server callers already supply the admitted absolute path.
func (e *PolyglotExecutor) ExecuteFile(ctx context.Context, req ExecRequest) (*ExecResult, error) {
	if !filepath.IsAbs(req.FilePath) {
		req.FilePath = filepath.Join(e.projectDir, req.FilePath)
	}
	absPath, err := filepath.Abs(req.FilePath)
	if err != nil {
		return nil, fmt.Errorf("resolving file path: %w", err)
	}
	req.FilePath = absPath
	req.Code = injectFileContent(req.Language, req.Code)
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

	// Keep compilation artifacts temporary, but run with the request's cwd.
	return e.runProcess(ctx, binPath, nil, req.Cwd, tmpDir, req)
}

func (e *PolyglotExecutor) runProcess(ctx context.Context, bin string, args []string, workDir, tmpDir string, req ExecRequest) (*ExecResult, error) {
	// Don't use exec.CommandContext — it only kills the process, not the
	// process group. We manage timeout ourselves via the context + SIGKILL
	// to the entire process group.
	cmd := exec.Command(bin, args...)
	cmd.Dir = workDir
	cmd.Env = BuildSafeEnv(tmpDir)
	if req.FilePath != "" {
		// Keep the admitted path out of generated source: language interpolation
		// and incompatible string escapes must never select a different file.
		// os/exec uses the last value for duplicate keys, overriding inherited data.
		cmd.Env = append(cmd.Env, fileContentPathEnv+"="+req.FilePath)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Own the readers so pipe draining can finish BEFORE cmd.Wait reaps the
	// group leader. An unreaped leader reserves its PID even after it exits,
	// allowing timeout/shutdown to safely signal descendants holding the pipes.
	var stdout, stderr safeBuffer
	outRead, outWrite, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}
	errRead, errWrite, err := os.Pipe()
	if err != nil {
		outRead.Close()
		outWrite.Close()
		return nil, fmt.Errorf("creating stderr pipe: %w", err)
	}
	cmd.Stdout = outWrite
	cmd.Stderr = errWrite

	if err := cmd.Start(); err != nil {
		outRead.Close()
		outWrite.Close()
		errRead.Close()
		errWrite.Close()
		return nil, fmt.Errorf("starting process: %w", err)
	}
	outWrite.Close()
	errWrite.Close()
	copied := make(chan error, 2)
	drain := func(reader *os.File, buffer *safeBuffer) {
		_, err := io.Copy(buffer, reader)
		reader.Close()
		copied <- err
	}
	go drain(outRead, &stdout)
	go drain(errRead, &stderr)
	process := &backgroundProcess{cmd: cmd, stdout: &stdout, stderr: &stderr}

	// One waiter owns cmd.Wait and eventual detached cleanup. The same mutex
	// serializes completion and detachment so an exited child cannot be added
	// back to the background registry after the waiter removed it.
	waitDone := make(chan struct{})
	var waitErr, captureErr error
	finished, detached := false, false // guarded by bgMu
	go func() {
		observeErr := e.observeExit(cmd.Process.Pid)
		if observeErr != nil {
			// Observation does not reap. On failure stop our still-owned
			// group, then close readers so failed observation cannot strand
			// the waiter. ECHILD means ownership was lost: never signal it.
			e.bgMu.Lock()
			if !errors.Is(observeErr, syscall.ECHILD) {
				syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
			finished = true
			if e.backgroundPids[cmd.Process.Pid] == process {
				delete(e.backgroundPids, cmd.Process.Pid)
			}
			e.bgMu.Unlock()
			outRead.Close()
			errRead.Close()
		}
		copyErr := errors.Join(<-copied, <-copied)
		e.bgMu.Lock()
		// Successful terminal observation and drained streams permit reaping.
		// On observation failure the legacy waiter still owns the final Wait;
		// that error path has no bounded-reap guarantee. All group signals use
		// this same lock, so none can follow reaping.
		if observeErr != nil {
			captureErr = fmt.Errorf("observing process exit: %w", observeErr)
		}
		if copyErr != nil {
			captureErr = errors.Join(captureErr, fmt.Errorf("draining process output: %w", copyErr))
		}
		waitErr = cmd.Wait()
		finished = true
		cleanup := detached
		if detached && e.backgroundPids[cmd.Process.Pid] == process {
			delete(e.backgroundPids, cmd.Process.Pid)
		}
		e.bgMu.Unlock()
		if cleanup {
			if captureErr != nil {
				slog.Warn("background process capture failed", "pid", cmd.Process.Pid, "error", captureErr)
			}
			os.RemoveAll(tmpDir)
		}
		close(waitDone)
	}()
	kill := func() {
		e.bgMu.Lock()
		defer e.bgMu.Unlock()
		if !finished {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}

	// Monitor only while attached. Returning stops the ticker; there is no
	// lingering monitor that can signal a completed or detached process.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-waitDone:
			return e.buildResult(waitErr, &stdout, &stderr, ctx, false), captureErr
		case <-ctx.Done():
			if !req.Background {
				kill()
				<-waitDone
				return e.buildResult(waitErr, &stdout, &stderr, ctx, false), captureErr
			}
			e.bgMu.Lock()
			if finished {
				e.bgMu.Unlock()
				<-waitDone
				return e.buildResult(waitErr, &stdout, &stderr, ctx, false), captureErr
			}
			// Keep the pipe readers alive, but release captured bytes and
			// discard subsequent writes before handing off the child.
			pid := cmd.Process.Pid
			result := &ExecResult{
				Stdout:       SmartTruncate(stdout.SnapshotAndDiscard(), e.maxOutputBytes),
				Stderr:       SmartTruncate(stderr.SnapshotAndDiscard(), e.maxOutputBytes),
				Backgrounded: true,
				PID:          pid,
			}
			detached = true
			e.backgroundPids[pid] = process
			e.bgMu.Unlock()
			return result, nil
		case <-ticker.C:
			if stdout.Size()+stderr.Size() > int64(HardCapBytes) {
				kill()
				<-waitDone
				return e.buildResult(waitErr, &stdout, &stderr, ctx, true), captureErr
			}
		}
	}
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

// CleanupBackgrounded kills all tracked background processes.
func (e *PolyglotExecutor) CleanupBackgrounded() {
	e.bgMu.Lock()
	defer e.bgMu.Unlock()
	for pid := range e.backgroundPids {
		// The waiter removes the entry while holding bgMu across reaping.
		// Until then the unreaped group leader reserves this identifier.
		syscall.Kill(-pid, syscall.SIGTERM)
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

// quotePosixSingle wraps value in single quotes for safe interpolation into a
// POSIX shell script. Each embedded single quote is replaced by the standard
// four-character escape (close-quote, backslash, escaped-quote, reopen-quote)
// so the resulting token stays a single shell word. The exact replacement lives
// in the raw-string literal below rather than in this prose because gofmt
// rewrites a literal pair of single quotes in comments into a typographic quote.
func quotePosixSingle(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// buildShellScript prepends a PATH restoration line to a shell script so that
// startup files sourced by the subprocess cannot hide tools the MCP process
// had on its PATH. inheritedPath is passed explicitly (rather than read from
// the environment) so the function stays pure and unit-testable. An empty
// inheritedPath returns code unchanged.
func buildShellScript(code, inheritedPath string) string {
	if inheritedPath == "" {
		return code
	}
	return fmt.Sprintf("export PATH=%s\n%s", quotePosixSingle(inheritedPath), code)
}
