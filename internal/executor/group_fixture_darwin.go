//go:build darwin && cgo && integration

package executor

/*
#include <errno.h>
#include <libproc.h>

// libproc.h labels these interfaces private. This bounded observation is only
// for a native experiment, not a production cleanup primitive or certificate.
static int capy_fixture_group(int pgid, int *pids, int capacity, int *count) {
	errno = 0;
	int bytes = proc_listpids(PROC_PGRP_ONLY, (uint32_t)pgid,
	    pids, capacity * (int)sizeof(int));
	if (bytes <= 0 && errno != 0) return errno;
	if (bytes < 0 || bytes % sizeof(int) != 0) return EIO;
	// A full buffer cannot establish a complete snapshot; do not truncate.
	if (bytes >= capacity * (int)sizeof(int)) return EOVERFLOW;
	*count = bytes / sizeof(int);
	return 0;
}
*/
import "C"

import "syscall"

func darwinFixtureGroup(pgid int) ([]int, error) {
	if pgid <= 0 || int(C.int(pgid)) != pgid {
		return nil, syscall.EINVAL
	}
	var pids [16]C.int
	var count C.int
	if errno := C.capy_fixture_group(C.int(pgid), &pids[0], C.int(len(pids)), &count); errno != 0 {
		return nil, syscall.Errno(errno)
	}
	result := make([]int, int(count))
	for i := range result {
		result[i] = int(pids[i])
	}
	return result, nil
}
