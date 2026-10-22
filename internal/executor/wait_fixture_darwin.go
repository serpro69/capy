//go:build darwin && cgo && integration

package executor

/*
#include <errno.h>
#include <signal.h>
#include <sys/wait.h>

// Fixture synchronization must leave the stop notification pending. wait4 with
// WUNTRACED consumes it and can hide the historical Darwin WEXITED caveat.
static int capy_fixture_stopped(int pid, int *stopped) {
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

// This binding lives outside _test.go because Go does not support cgo in test
// files. The integration tag keeps it out of ordinary repository builds.
func darwinChildStopped(pid int) (bool, error) {
	var stopped C.int
	if errno := C.capy_fixture_stopped(C.int(pid), &stopped); errno != 0 {
		return false, syscall.Errno(errno)
	}
	return stopped != 0, nil
}
