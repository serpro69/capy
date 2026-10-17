package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const retirementTimeout = 3 * time.Second

var supervisorPrepared atomic.Bool

// PrepareSupervisor changes process-wide child ownership. Call only in the
// dedicated helper, before starting any child. No other code may reap children.
func PrepareSupervisor() error {
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("enabling supervisor subreaper: %w", err)
	}
	// A restricted procfs must fail before spawn, not during cleanup.
	_, err := directChildren()
	if err == nil {
		supervisorPrepared.Store(true)
	}
	return err
}

func checkSupervisor() error {
	if !supervisorPrepared.Load() {
		return errors.New("execution requires a prepared supervisor process")
	}
	return nil
}

func runStreamPhase(ctx context.Context, bin string, args []string, cwd, tmpDir string, req ExecRequest, phase string, w *eventWriter) (StreamResult, error) {
	result := StreamResult{Retired: true, Phase: phase}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir, cmd.Env = cwd, BuildSafeEnv(tmpDir)
	if req.FilePath != "" {
		cmd.Env = append(cmd.Env, fileContentPathEnv+"="+req.FilePath)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Stdin is /dev/null. No private descriptors are passed via ExtraFiles.
	outR, outW, err := os.Pipe()
	if err != nil {
		return result, errors.New("creating supervised stdout")
	}
	defer outR.Close()
	defer outW.Close()
	errR, errW, err := os.Pipe()
	if err != nil {
		return result, errors.New("creating supervised stderr")
	}
	defer errR.Close()
	defer errW.Close()
	cmd.Stdout, cmd.Stderr = outW, errW
	if err := cmd.Start(); err != nil {
		return result, errors.New("starting supervised process")
	}
	result.Started, result.Retired = true, false
	outW.Close()
	errW.Close()
	drained := make(chan error, 2)
	drain := func(f *os.File, stream string) {
		buf := make([]byte, EventBlockBytes)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				w.write(phase, stream, buf[:n])
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

	// WNOWAIT reserves the leader's PID until the final group signal. Polling
	// avoids an abandoned wait goroutine if a killed child remains in kernel I/O.
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	var cleanupBy time.Time
	var cleanupErr error
	observed := false
	for {
		var info unix.Siginfo
		err = unix.Waitid(unix.P_PID, cmd.Process.Pid, &info, unix.WEXITED|unix.WNOWAIT|unix.WNOHANG, nil)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			cleanupErr = errors.New("supervisor lost child wait ownership")
			break
		}
		if info.Signo != 0 {
			observed = true
			break
		}
		if ctx.Err() != nil && cleanupBy.IsZero() {
			cleanupBy = time.Now().Add(retirementTimeout)
			if err := unix.Kill(-cmd.Process.Pid, unix.SIGKILL); err != nil && err != unix.ESRCH {
				cleanupErr = errors.New("supervisor group cancellation failed")
				break
			}
		}
		if !cleanupBy.IsZero() && time.Now().After(cleanupBy) {
			cleanupErr = errors.New("supervisor retirement deadline exceeded")
			break
		}
		<-tick.C
	}
	if observed {
		// Even a successful leader may leave grandchildren. Retire those before
		// returning, then reap adopted children (including setsid descendants).
		if err := unix.Kill(-cmd.Process.Pid, unix.SIGKILL); err != nil && err != unix.ESRCH {
			cleanupErr = errors.New("supervisor group retirement failed")
		}
		waitErr := cmd.Wait() // no exec pipe-copy goroutines; exit already observed
		var exitErr *exec.ExitError
		if waitErr == nil || errors.As(waitErr, &exitErr) {
			code := cmd.ProcessState.ExitCode()
			result.ExitCode = &code
			if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				result.Signal = int(status.Signal())
			}
		} else {
			cleanupErr = errors.Join(cleanupErr, errors.New("reaping supervised process"))
		}
		cleanupErr = errors.Join(cleanupErr, retireDescendants(time.Now().Add(retirementTimeout)))
	} else {
		// No later signal may use this PID: loss of wait ownership is ambiguous.
		cmd.Process.Release()
	}
	// A foreign process could hold a passed pipe. Bound drains and refuse proof
	// if EOF is unavailable, even after every owned child has been reaped.
	if cleanupErr != nil {
		outR.Close()
		errR.Close()
	}
	timer := time.NewTimer(retirementTimeout)
	defer timer.Stop()
	for n := 0; n < 2; {
		select {
		case err := <-drained:
			cleanupErr = errors.Join(cleanupErr, err)
			n++
		case <-timer.C:
			cleanupErr = errors.Join(cleanupErr, errors.New("supervisor output drain deadline exceeded"))
			outR.Close()
			errR.Close()
		}
	}
	result.Retired = cleanupErr == nil
	return result, cleanupErr
}

// directChildren reads every Go thread's child list. A PID in this list stays
// reserved until our sole reaper consumes it; no persisted PID is ever signalled.
func directChildren() ([]int, error) {
	threads, err := os.ReadDir("/proc/self/task")
	if err != nil || len(threads) > 4096 {
		return nil, errors.New("supervisor procfs unavailable")
	}
	var children []int
	for _, thread := range threads {
		path := filepath.Join("/proc/self/task", thread.Name(), "children")
		f, err := os.Open(path)
		if os.IsNotExist(err) {
			continue
		} // a Go thread exited
		if err != nil {
			return nil, errors.New("reading supervisor children")
		}
		b, readErr := io.ReadAll(io.LimitReader(f, 64*1024+1))
		f.Close()
		if readErr != nil || len(b) > 64*1024 {
			return nil, errors.New("supervisor children inventory unavailable")
		}
		for _, field := range strings.Fields(string(b)) {
			pid, err := strconv.Atoi(field)
			if err != nil || pid <= 0 {
				return nil, errors.New("invalid supervisor child identity")
			}
			children = append(children, pid)
			if len(children) > 16384 {
				return nil, errors.New("supervisor children inventory exceeded")
			}
		}
	}
	return children, nil
}

func retireDescendants(deadline time.Time) error {
	for time.Now().Before(deadline) {
		children, err := directChildren()
		if err != nil {
			return err
		}
		for _, pid := range children {
			// Confirm wait ownership without reaping before signalling. This is
			// essential if a procfs observation ever becomes stale.
			var info unix.Siginfo
			err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT|unix.WNOHANG, nil)
			if err == unix.ECHILD || err == unix.EINTR {
				continue
			}
			if err != nil {
				return errors.New("checking adopted child ownership")
			}
			if err := unix.Kill(pid, unix.SIGKILL); err != nil && err != unix.ESRCH {
				return errors.New("retiring adopted child")
			}
		}
		for {
			pid, err := unix.Wait4(-1, nil, unix.WNOHANG, nil)
			if err == unix.EINTR {
				continue
			}
			if err == unix.ECHILD {
				return nil
			}
			if err != nil {
				return errors.New("reaping adopted child")
			}
			if pid == 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errors.New("adopted child retirement deadline exceeded")
}
