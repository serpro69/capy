package executor

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// waitForExit preserves the legacy executor's observation lifetime: its single
// owned waiter lives until the child exits, including after background handoff.
func waitForExit(pid int) error {
	return waitForExitContext(context.Background(), pid)
}

// waitForExitContext observes a direct child's terminal state without reaping.
// The caller must retain sole reaping ownership and ordinary SIGCHLD semantics
// (no auto-reap or competing wait). Success leaves the child waitable and its PID
// reserved; cancellation/error leaves ownership with the caller too. In those
// cases the caller must NOT presume cmd.Wait is immediately safe to call.
//
// Kqueue NOTE_EXIT and registration ESRCH can precede a waitable zombie on
// Darwin. They are wakeups only; a matching terminal waitid status is the evidence.
// Kqueue waits have a bounded timeout and WNOHANG keeps status probes from waiting
// for a child transition. No background waiter is spawned or abandoned on
// cancellation. This is a scheduling bound, not a hard realtime
// guarantee against a stalled kernel. It proves nothing about other group members
// or output drainage.
func waitForExitContext(ctx context.Context, pid int) error {
	if pid <= 0 {
		return os.NewSyscallError("waitid", syscall.EINVAL)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	terminal, err := observeDarwinExit(pid)
	if err != nil && !errors.Is(err, syscall.EINTR) {
		return os.NewSyscallError("waitid", err)
	}
	if terminal {
		return nil
	}
	// Avoid periodic cgo probes while the child is running. The original
	// all-polling implementation added ~0.45 ms to short shell commands.
	if err := waitDarwinExitHint(ctx, pid); err != nil {
		return err
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		terminal, err := observeDarwinExit(pid)
		if err != nil && !errors.Is(err, syscall.EINTR) {
			return os.NewSyscallError("waitid", err)
		}
		if err == nil && terminal {
			return nil
		}
		// Even EINTR and unexpected stopped/continued reports yield to the
		// deadline and timer, rather than spinning or certifying termination.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// waitDarwinExitHint never certifies waitability. Even after NOTE_EXIT or ESRCH,
// the caller must observe a terminal waitid status before it can reap.
func waitDarwinExitHint(ctx context.Context, pid int) error {
	// Kqueue has no atomic CLOEXEC flag. Serialize setup against child forks.
	syscall.ForkLock.RLock()
	kq, err := unix.Kqueue()
	if err == nil {
		unix.CloseOnExec(kq)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return os.NewSyscallError("kqueue", err)
	}
	defer unix.Close(kq)
	var change unix.Kevent_t
	unix.SetKevent(&change, pid, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_ONESHOT)
	change.Fflags = unix.NOTE_EXIT
	changes := []unix.Kevent_t{change}
	var events [1]unix.Kevent_t
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Cancellation does not close a descriptor under a blocked syscall or
		// leave an observer goroutine behind. Bound each kernel event wait.
		budget := 25 * time.Millisecond
		if deadline, ok := ctx.Deadline(); ok {
			budget = min(budget, max(0, time.Until(deadline)))
		}
		timeout := unix.NsecToTimespec(budget.Nanoseconds())
		n, err := unix.Kevent(kq, changes, events[:], &timeout)
		if errors.Is(err, unix.ESRCH) {
			return nil // exiting, not necessarily waitable yet
		}
		if errors.Is(err, unix.EINTR) {
			continue // re-adding after an interrupted registration is safe
		}
		if err != nil {
			return os.NewSyscallError("kevent", err)
		}
		changes = nil
		if n == 0 {
			continue
		}
		if events[0].Flags&unix.EV_ERROR != 0 {
			err := unix.Errno(events[0].Data)
			if errors.Is(err, unix.ESRCH) {
				return nil
			}
			if err != 0 {
				return os.NewSyscallError("kevent", err)
			}
		}
		if events[0].Fflags&unix.NOTE_EXIT != 0 {
			return nil
		}
	}
}
