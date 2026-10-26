//go:build darwin && cgo

package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const darwinCleanupTimeout = 3 * time.Second

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
	version, err := unix.Sysctl("kern.osproductversion")
	if err != nil {
		return fmt.Errorf("reading macos version: %w", err)
	}
	return darwinObservationMatrix(runtime.GOARCH, version)
}

func darwinObservationMatrix(arch, version string) error {
	if arch != "arm64" {
		return errors.New("darwin observation requires apple silicon")
	}
	major, _, _ := strings.Cut(version, ".")
	if major != "26" {
		return errors.New("darwin observation requires macos 26")
	}
	return nil
}

// PrepareSupervisor checks the verified native matrix. Darwin needs no process-wide
// subreaper setup; each phase creates and owns a new session leader.
func PrepareSupervisor() error { return darwinObservationSupported() }
func checkSupervisor() error   { return darwinObservationSupported() }

func initialStreamCleanup() CleanupEvidence {
	return CleanupEvidence{Scope: CleanupDarwinObservation, State: CleanupNotSpawned}
}

func runStreamPhase(ctx context.Context, bin string, args []string, cwd, tmpDir string, req ExecRequest, phase string, w *eventWriter) (StreamResult, error) {
	return runDarwinStreamPhase(ctx, darwinPhase{
		bin: bin, args: args, cwd: cwd, tmpDir: tmpDir, name: phase, request: req,
	}, w, nativeDarwinPhaseOps())
}

func runDarwinStreamPhase(ctx context.Context, phase darwinPhase, w *eventWriter, ops darwinPhaseOps) (result StreamResult, resultErr error) {
	result.Phase = phase.name
	result.Cleanup = initialStreamCleanup()
	if w == nil || w.sink == nil || ops.waitExit == nil || ops.inventory == nil || ops.killGroup == nil || ops.budget <= 0 {
		return result, errors.New("invalid darwin phase dependencies")
	}
	if err := ctx.Err(); err != nil {
		result.StopReason = streamStopReason(ctx)
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
		result.SpawnCode = 126
		if os.IsNotExist(err) {
			result.SpawnCode = 127
		}
		return result, fmt.Errorf("starting darwin phase: %w", err)
	}
	result.Started = true
	result.Cleanup.State = CleanupPending
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
		result.CaptureLost = w.err != nil
		result.ObservedBytes = w.observed
	}()

	waitErr := ops.waitExit(ctx, cmd.Process.Pid)
	terminal := waitErr == nil
	cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), ops.budget)
	defer cancelCleanup()
	cleanupErr := closeErr
	signalled := false
	if !terminal {
		if ctx.Err() == nil || (!errors.Is(waitErr, context.Canceled) && !errors.Is(waitErr, context.DeadlineExceeded)) {
			// Lost/unknown wait ownership forbids further numeric signals or an
			// assumed-immediate Wait. Durable integration will keep this pending.
			return result, errors.Join(cleanupErr, waitErr, cmd.Process.Release())
		}
		result.StopReason = streamStopReason(ctx)
		killErr := ops.killGroup(cmd.Process.Pid)
		signalled = killErr == nil
		cleanupErr = errors.Join(cleanupErr, killErr)
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
		if otherLive && !signalled {
			if err := ops.killGroup(cmd.Process.Pid); err != nil {
				cleanupErr = fmt.Errorf("signalling darwin group: %w", err)
				break
			}
			signalled = true
		}
		// A successful SIGKILL already requests termination of the group.
		// Inventory can still report members during exit; another signal can
		// fail after the last live member disappears. Observe until drainage
		// and the final inventory succeed. A surviving/new member still times
		// out as pending; never infer completion from signal delivery alone.
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
	reapErr := reapDarwinLeader(cmd, &result)
	if observed && cleanupErr == nil && reapErr == nil {
		result.Cleanup.State = CleanupObserved
	}
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
