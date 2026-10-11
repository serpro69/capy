package executor

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// waitForExit observes exit without reaping. Registering a process filter on
// an already-exited child may return ESRCH; our sole waiter has not reaped it,
// so its PID remains reserved and it is ready for cmd.Wait in that case too.
func waitForExit(pid int) error {
	// Kqueue has no atomic CLOEXEC flag. Serialize setup against child forks.
	syscall.ForkLock.RLock()
	kq, err := unix.Kqueue()
	if err == nil {
		unix.CloseOnExec(kq)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return err
	}
	defer unix.Close(kq)
	var change unix.Kevent_t
	unix.SetKevent(&change, pid, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_ONESHOT)
	change.Fflags = unix.NOTE_EXIT
	changes := []unix.Kevent_t{change}
	var events [1]unix.Kevent_t
	for {
		n, err := unix.Kevent(kq, changes, events[:], nil)
		if err == unix.ESRCH {
			return nil
		}
		if err == unix.EINTR {
			// Registration may already have succeeded. Re-adding is safe.
			continue
		}
		if err != nil {
			return err
		}
		changes = nil
		if n == 0 {
			continue
		}
		if events[0].Flags&unix.EV_ERROR != 0 {
			err := unix.Errno(events[0].Data)
			if err == unix.ESRCH {
				return nil
			}
			return err
		}
		if events[0].Fflags&unix.NOTE_EXIT != 0 {
			return nil
		}
	}
}
