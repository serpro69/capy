package executor

import "golang.org/x/sys/unix"

// waitForExit observes the child exiting without consuming its wait status.
// The zombie keeps its PID reserved until the executor has drained the pipes
// and can serialize the single cmd.Wait with every process-group signal.
func waitForExit(pid int) error {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err != unix.EINTR {
			return err
		}
	}
}
