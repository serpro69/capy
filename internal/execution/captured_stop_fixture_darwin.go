//go:build darwin && cgo && integration

package execution

/*
#include <errno.h>
#include <signal.h>
#include <sys/wait.h>

// Match the executor's native stop fixture: preserve notification for the sole
// later reaper and validate the event kind, not just the returned child PID.
static int capy_capture_fixture_stopped(int pid, int *stopped) {
	siginfo_t info = {0};
	*stopped = 0;
	if (waitid(P_PID, (id_t)pid, &info, WSTOPPED | WNOWAIT | WNOHANG) == -1) {
		return errno;
	}
	*stopped = info.si_pid == pid && info.si_signo == SIGCHLD &&
	    info.si_code == CLD_STOPPED && info.si_status == SIGSTOP;
	return 0;
}
*/
import "C"

import "syscall"

// Cgo is unsupported in _test.go; the integration tag excludes this fixture
// binding from ordinary builds.
func darwinCaptureHelperStopped(pid int) (bool, error) {
	var stopped C.int
	if errno := C.capy_capture_fixture_stopped(C.int(pid), &stopped); errno != 0 {
		return false, syscall.Errno(errno)
	}
	return stopped != 0, nil
}
