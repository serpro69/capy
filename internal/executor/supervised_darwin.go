//go:build darwin && cgo

package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const darwinCleanupTimeout = 3 * time.Second

// This dormant result is deliberately outside StreamResult's v1 wire contract.
// Task 9 will adapt it to the scoped CleanupEvidence introduced by Task 5.
// observed means the documented policy completed, not that all processes died.
type darwinPhaseResult struct {
	command  StreamResult
	observed bool
}

type darwinPhase struct {
	bin, cwd, tmpDir, name string
	args                   []string
	request                ExecRequest
}

// Dependencies are per-call so failure fixtures cannot change other executions.
type darwinPhaseOps struct {
	waitExit  func(context.Context, int) error
	inventory func(context.Context, int) ([]darwinGroupMember, error)
	killGroup func(int) error
	budget    time.Duration
}

func nativeDarwinPhaseOps() darwinPhaseOps {
	return darwinPhaseOps{
		waitExit:  waitForExitContext,
		inventory: observeDarwinGroup,
		killGroup: func(pid int) error { return syscall.Kill(-pid, syscall.SIGKILL) },
		budget:    darwinCleanupTimeout,
	}
}

func darwinObservationSupported() error {
	if runtime.GOARCH != "arm64" {
		return errors.New("darwin observation requires apple silicon")
	}
	version, err := unix.Sysctl("kern.osproductversion")
	if err != nil {
		return fmt.Errorf("reading macos version: %w", err)
	}
	major, _, _ := strings.Cut(version, ".")
	if major != "26" {
		return errors.New("darwin observation requires macos 26")
	}
	return nil
}

// executeDarwinStream is private until the scoped storage/protocol integration
// lands. Existing PrepareSupervisor/ExecuteStream still reject Darwin. Runtime
// preparation, safe environment and event ordering reuse the existing executor.
// Like ExecuteStream, this belongs in a dedicated per-run helper. An unconfirmed
// terminal state remains incomplete and requires helper teardown/pending-state
// handling; Process.Release is not a reap or proof that the command stopped.
func (e *PolyglotExecutor) executeDarwinStream(ctx context.Context, req ExecRequest, sink EventSink, started func(string) error) (darwinPhaseResult, error) {
	result := darwinPhaseResult{}
	if req.Background || sink == nil {
		return result, errors.New("darwin stream execution requires an attached sink")
	}
	if err := ctx.Err(); err != nil {
		result.command.StopReason = streamStopReason(ctx)
		return result, err
	}
	if err := darwinObservationSupported(); err != nil {
		return result, err
	}
	if req.FilePath != "" {
		if !filepath.IsAbs(req.FilePath) {
			return result, errors.New("darwin stream execution requires an admitted absolute file path")
		}
		req.Code = injectFileContent(req.Language, req.Code)
	}
	p, err := e.prepare(req)
	if err != nil {
		result.command.SpawnCode = 126
		if errors.Is(err, ErrRuntimeUnavailable) {
			result.command.SpawnCode = 127
		}
		return result, err
	}
	defer os.RemoveAll(p.tmpDir)
	w := &eventWriter{sink: sink, started: started}
	ops := nativeDarwinPhaseOps()
	compiled := false
	if req.Language == Rust {
		binary := filepath.Join(p.tmpDir, "main")
		result, err = runDarwinStreamPhase(ctx, darwinPhase{
			bin: p.runtime, args: []string{p.scriptPath, "-o", binary},
			cwd: p.tmpDir, tmpDir: p.tmpDir, name: "compile", request: req,
		}, w, ops)
		if err != nil || !result.observed || result.command.ExitCode == nil ||
			*result.command.ExitCode != 0 || result.command.Signal != 0 || ctx.Err() != nil {
			if result.command.StopReason == "" && err == nil && result.command.ExitCode != nil &&
				*result.command.ExitCode == 0 && ctx.Err() != nil {
				result.command.StopReason = streamStopReason(ctx)
			}
			return result, err
		}
		p.bin, p.args = binary, nil
		compiled = true
	}
	result, err = runDarwinStreamPhase(ctx, darwinPhase{
		bin: p.bin, args: p.args, cwd: p.workDir, tmpDir: p.tmpDir,
		name: "run", request: req,
	}, w, ops)
	if compiled && !result.command.Started {
		// A failed/unstarted runtime cannot erase the completed compiler phase.
		result.command.Started = true
		result.observed = true
	}
	result.command.CaptureLost = w.err != nil
	result.command.ObservedBytes = w.observed
	return result, err
}

func runDarwinStreamPhase(ctx context.Context, phase darwinPhase, w *eventWriter, ops darwinPhaseOps) (result darwinPhaseResult, resultErr error) {
	result.command.Phase = phase.name
	if w == nil || w.sink == nil || ops.waitExit == nil || ops.inventory == nil || ops.killGroup == nil || ops.budget <= 0 {
		return result, errors.New("invalid darwin phase dependencies")
	}
	if err := ctx.Err(); err != nil {
		result.command.StopReason = streamStopReason(ctx)
		return result, err
	}
	cmd := exec.Command(phase.bin, phase.args...)
	cmd.Dir, cmd.Env = phase.cwd, BuildSafeEnv(phase.tmpDir)
	if phase.request.FilePath != "" {
		cmd.Env = append(cmd.Env, fileContentPathEnv+"="+phase.request.FilePath)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// Stdin is /dev/null; private descriptors are never passed in ExtraFiles.
	outR, outW, err := os.Pipe()
	if err != nil {
		return result, fmt.Errorf("creating darwin stdout: %w", err)
	}
	defer outR.Close()
	defer outW.Close()
	errR, errW, err := os.Pipe()
	if err != nil {
		return result, fmt.Errorf("creating darwin stderr: %w", err)
	}
	defer errR.Close()
	defer errW.Close()
	cmd.Stdout, cmd.Stderr = outW, errW
	if err := cmd.Start(); err != nil {
		result.command.SpawnCode = 126
		if os.IsNotExist(err) {
			result.command.SpawnCode = 127
		}
		return result, fmt.Errorf("starting darwin phase: %w", err)
	}
	result.command.Started = true
	if w.started != nil && w.err == nil {
		w.err = w.started(phase.name)
	}
	closeErr := errors.Join(outW.Close(), errW.Close())
	drained := make(chan error, 2)
	drain := func(reader *os.File, stream string) {
		buf := make([]byte, EventBlockBytes)
		for {
			n, err := reader.Read(buf)
			if n > 0 {
				w.write(phase.name, stream, buf[:n])
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					err = nil
				}
				drained <- err
				return
			}
		}
	}
	go drain(outR, "stdout")
	go drain(errR, "stderr")
	remaining := 2
	defer func() {
		// Close unblocks owned pipe reads on failure. The sink must bound its
		// latency, as required by EventSink; every drain worker is joined.
		outR.Close()
		errR.Close()
		for remaining > 0 {
			resultErr = errors.Join(resultErr, <-drained)
			remaining--
		}
		result.command.CaptureLost = w.err != nil
		result.command.ObservedBytes = w.observed
	}()

	waitErr := ops.waitExit(ctx, cmd.Process.Pid)
	terminal := waitErr == nil
	cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), ops.budget)
	defer cancelCleanup()
	cleanupErr := closeErr
	if !terminal {
		if ctx.Err() == nil || (!errors.Is(waitErr, context.Canceled) && !errors.Is(waitErr, context.DeadlineExceeded)) {
			// Lost/unknown wait ownership forbids further numeric signals or an
			// assumed-immediate Wait. Durable integration will keep this pending.
			return result, errors.Join(cleanupErr, waitErr, cmd.Process.Release())
		}
		result.command.StopReason = streamStopReason(ctx)
		cleanupErr = errors.Join(cleanupErr, ops.killGroup(cmd.Process.Pid))
		waitErr = ops.waitExit(cleanupCtx, cmd.Process.Pid)
		terminal = waitErr == nil
		if !terminal {
			return result, errors.Join(cleanupErr, waitErr, cmd.Process.Release())
		}
	}

	// Terminal status is retained until the final observation/signal. The
	// separate cleanup context ignores later caller cancellation; it cannot
	// rewrite a naturally observed command exit as cancellation.
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	observed := false
	for cleanupErr == nil {
		if err := cleanupCtx.Err(); err != nil {
			cleanupErr = fmt.Errorf("darwin cleanup observation deadline: %w", err)
			break
		}
		for remaining > 0 {
			select {
			case drainErr := <-drained:
				remaining--
				cleanupErr = errors.Join(cleanupErr, drainErr)
			default:
				goto inspect
			}
		}
	inspect:
		if cleanupErr != nil {
			break
		}
		members, err := ops.inventory(cleanupCtx, cmd.Process.Pid)
		if err == nil {
			err = cleanupCtx.Err()
		}
		if err != nil {
			cleanupErr = err
			break
		}
		leaderFound, otherLive := false, false
		for _, member := range members {
			if member.pid == cmd.Process.Pid {
				leaderFound = true
			} else if !member.isZombie {
				otherLive = true
			}
		}
		if !leaderFound {
			cleanupErr = errors.New("retained darwin leader missing from inventory")
			break
		}
		if otherLive {
			if err := ops.killGroup(cmd.Process.Pid); err != nil {
				cleanupErr = fmt.Errorf("signalling darwin group: %w", err)
				break
			}
		}
		if len(members) == 1 && remaining == 0 {
			observed = true // snapshot after both natural EOFs, before sole reap
			break
		}
		select {
		case <-cleanupCtx.Done():
			cleanupErr = errors.New("darwin cleanup observation deadline exceeded")
		case <-ticker.C:
		}
	}
	// Even a failed inventory/drain must preserve the real command status when
	// terminal ownership is known. No group signal is allowed after this call.
	reapErr := reapDarwinLeader(cmd, &result.command)
	result.observed = observed && cleanupErr == nil && reapErr == nil
	return result, errors.Join(cleanupErr, reapErr)
}

func reapDarwinLeader(cmd *exec.Cmd, result *StreamResult) error {
	err := cmd.Wait() // terminal already observed, no os/exec pipe-copy workers
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return fmt.Errorf("reaping darwin leader: %w", err)
	}
	if cmd.ProcessState == nil {
		return errors.New("missing darwin leader exit status")
	}
	code := cmd.ProcessState.ExitCode()
	result.ExitCode = &code
	status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok {
		return errors.New("invalid darwin leader exit status")
	}
	if status.Signaled() {
		result.Signal = int(status.Signal())
	}
	return nil
}
