//go:build darwin && cgo

package executor

/*
#include <errno.h>
#include <signal.h>
#include <sys/wait.h>

// Use the SDK's siginfo_t layout and libc entry point, not a guessed Go ABI or
// a raw Darwin syscall. WNOWAIT preserves status for the sole cmd.Wait owner.
static int capy_observe_exit(int pid) {
	// Darwin may leave siginfo untouched when WNOHANG finds no pending status.
	siginfo_t info = {0};
	if (waitid(P_PID, (id_t)pid, &info, WEXITED | WNOWAIT | WNOHANG) == -1) {
		return -errno;
	}
	// Historical Darwin versions reported stops even with WEXITED. Neither a
	// successful syscall nor a nonzero pid alone is a terminal certificate.
	if (info.si_pid == pid && info.si_signo == SIGCHLD &&
	    (info.si_code == CLD_EXITED || info.si_code == CLD_KILLED ||
	     info.si_code == CLD_DUMPED)) {
		return 1;
	}
	return 0;
}
*/
import "C"

import "syscall"

func observeDarwinExit(pid int) (bool, error) {
	if int(C.int(pid)) != pid {
		return false, syscall.EINVAL
	}
	status := C.capy_observe_exit(C.int(pid))
	if status < 0 {
		return false, syscall.Errno(-status)
	}
	return status == 1, nil
}
