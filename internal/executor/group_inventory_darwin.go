//go:build darwin && cgo

package executor

/*
#include <errno.h>
#include <stdlib.h>
#include <sys/types.h>
#include <sys/proc.h>
#include <sys/sysctl.h>

struct capy_group_member {
	int pid;
	int zombie;
};

// Use the documented MIB and SDK layout, not private libproc interfaces or a
// guessed Go ABI. An ENOMEM partial table is never an observation of absence.
static int capy_group_inventory(int pgid, struct capy_group_member *out,
    int capacity, int *count) {
	size_t capacity_bytes = (size_t)capacity * sizeof(struct kinfo_proc);
	struct kinfo_proc *procs = calloc((size_t)capacity, sizeof(*procs));
	if (procs == NULL) return ENOBUFS;
	int mib[4] = {CTL_KERN, KERN_PROC, KERN_PROC_PGRP, pgid};
	size_t bytes = capacity_bytes;
	if (sysctl(mib, 4, procs, &bytes, NULL, 0) == -1) {
		int err = errno;
		free(procs);
		return err;
	}
	if (bytes > capacity_bytes || bytes % sizeof(*procs) != 0) {
		free(procs);
		return EIO;
	}
	int n = (int)(bytes / sizeof(*procs));
	for (int i = 0; i < n; i++) {
		if (procs[i].kp_proc.p_pid <= 0 || procs[i].kp_eproc.e_pgid != pgid) {
			free(procs);
			return EIO;
		}
		switch (procs[i].kp_proc.p_stat) {
		case SIDL: case SRUN: case SSLEEP: case SSTOP: case SZOMB:
			break;
		default:
			free(procs);
			return EIO;
		}
		out[i].pid = procs[i].kp_proc.p_pid;
		out[i].zombie = procs[i].kp_proc.p_stat == SZOMB;
	}
	free(procs);
	*count = n;
	return 0;
}
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"syscall"
)

const darwinGroupMemberLimit = 4096

type darwinGroupMember struct {
	pid      int
	isZombie bool
}

// observeDarwinGroup returns one successful inventory of visible members. It
// cannot exclude unpublished processes or a subsequent/in-flight group join.
// Allocation and retries are bounded; kernel scheduling is not a realtime SLA.
func observeDarwinGroup(ctx context.Context, pgid int) ([]darwinGroupMember, error) {
	if pgid <= 1 || int(C.int(pgid)) != pgid {
		return nil, syscall.EINVAL
	}
	for capacity := 16; capacity <= darwinGroupMemberLimit; capacity *= 2 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		members, err := readDarwinGroup(pgid, capacity)
		if errors.Is(err, syscall.ENOMEM) {
			continue // discard the partial table; retry only up to the fixed cap
		}
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return members, nil
	}
	return nil, errors.New("darwin process group inventory limit exceeded")
}

func readDarwinGroup(pgid, capacity int) ([]darwinGroupMember, error) {
	if pgid <= 1 || int(C.int(pgid)) != pgid || capacity < 1 || capacity > darwinGroupMemberLimit {
		return nil, syscall.EINVAL
	}
	entries := make([]C.struct_capy_group_member, capacity)
	var count C.int
	if errno := C.capy_group_inventory(C.int(pgid), &entries[0], C.int(capacity), &count); errno != 0 {
		return nil, fmt.Errorf("observing darwin process group: %w", syscall.Errno(errno))
	}
	if count < 0 || int(count) > capacity {
		return nil, errors.New("invalid darwin process group inventory length")
	}
	members := make([]darwinGroupMember, int(count))
	seen := make(map[int]struct{}, len(members))
	for i := range members {
		pid := int(entries[i].pid)
		if _, duplicate := seen[pid]; duplicate {
			return nil, errors.New("duplicate darwin process group member")
		}
		seen[pid] = struct{}{}
		members[i] = darwinGroupMember{pid: pid, isZombie: entries[i].zombie != 0}
	}
	return members, nil
}
