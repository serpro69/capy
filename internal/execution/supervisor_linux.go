package execution

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/serpro69/capy/internal/executor"
	"golang.org/x/sys/unix"
)

// ServeInherited owns FDs 3..7: control/liveness, credential, notices, exclusive
// owner-slot lease, shared lifecycle lease. They are closed (never LOCK_UN) only
// after the store closes. Closing a duplicate must not unlock the owner's OFD.
// The function must run in an otherwise child-free dedicated process.
func ServeInherited(ctx context.Context, open OpenRun) (retErr error) {
	var files []*os.File
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	for fd := 3; fd <= 7; fd++ {
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			return errors.New("missing supervisor descriptors")
		}
		unix.CloseOnExec(fd)
		if fd <= 5 {
			if stat.Mode&unix.S_IFMT != unix.S_IFIFO {
				return errors.New("supervisor channel is not a pipe")
			}
			if err := unix.SetNonblock(fd, true); err != nil {
				return errors.New("supervisor pipe is not deadline capable")
			}
		}
		f := os.NewFile(uintptr(fd), "supervisor-private")
		if f == nil {
			return errors.New("invalid supervisor descriptor")
		}
		files = append(files, f)
	}
	input, credential, events := files[0], files[1], files[2]
	owner, lifecycle := files[3], files[4]
	if err := validateLeases(owner, lifecycle); err != nil {
		return err
	}
	deadline := time.Now().Add(startupTimeout)
	if err := input.SetReadDeadline(deadline); err != nil {
		return errors.New("supervisor input deadline unavailable")
	}
	if err := credential.SetReadDeadline(deadline); err != nil {
		return errors.New("supervisor credential deadline unavailable")
	}
	var start Startup
	if err := readFrame(input, &start); err != nil {
		return err
	}
	if err := start.validate(); err != nil {
		return err
	}
	var credentialBytes []byte
	if err := readFrameLimit(credential, &credentialBytes, maxCredentialFrameBytes); err != nil {
		return err
	}
	key := string(credentialBytes)
	clear(credentialBytes)
	if key == "" {
		return errors.New("missing supervisor credential")
	}
	credential.Close()
	if open == nil {
		return errors.New("run storage is not available in this build")
	}
	if err := executor.PrepareSupervisor(); err != nil {
		return err
	}
	admitCtx, stopAdmission := context.WithDeadline(ctx, deadline)
	run, err := open(admitCtx, start, key, Leases{Owner: owner, Lifecycle: lifecycle})
	stopAdmission()
	key = ""
	if err != nil {
		return errors.New("opening admitted supervisor run")
	}
	if run == nil || run.Store == nil {
		return errors.New("invalid supervisor run backend")
	}
	defer func() {
		// Leases remain held even when Close must finish cleanup. Do not expose
		// backend diagnostics, which can contain DSNs or command data.
		if run.Store.Close() != nil {
			retErr = errors.Join(retErr, errors.New("closing supervisor run store"))
		}
	}()
	if run.Executor == nil {
		return errors.New("missing supervisor executor")
	}
	if err := sendNotice(events, notice{Version: protocolVersion, Type: "ready"}); err != nil {
		return err
	}
	var authorization control
	if err := readFrame(input, &authorization); err != nil {
		return err
	}
	if authorization.Action != "start" {
		return errors.New("supervisor start was not authorized")
	}
	if err := input.SetReadDeadline(time.Time{}); err != nil {
		return errors.New("clearing supervisor input deadline")
	}
	jobCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	if !start.Deadline.IsZero() {
		var stop context.CancelFunc
		jobCtx, stop = context.WithDeadline(jobCtx, start.Deadline)
		defer stop()
	}
	controlDone := make(chan struct{})
	go func() {
		defer close(controlDone)
		var command control
		if err := readFrame(input, &command); err != nil {
			cancel(errors.New("owner_lost"))
		} else if command.Action == "cancel" {
			cancel(errors.New("cancelled"))
		} else {
			cancel(errors.New("control_failed"))
		}
	}()
	defer func() { input.Close(); <-controlDone }()
	pendingCtx, stopPending := context.WithTimeout(jobCtx, writeTimeout)
	err = run.Store.Pending(pendingCtx)
	stopPending()
	if err != nil {
		return errors.New("persisting supervisor pending proof")
	}
	began := time.Now()
	queue := newCaptureQueue(run.Store)
	result, execErr := run.Executor.ExecuteStreamWithStart(jobCtx, run.Request, queue.append, run.Started)
	result.ElapsedMS = time.Since(began).Milliseconds()
	outcome := Outcome{Result: result}
	if jobCtx.Err() != nil {
		if errors.Is(jobCtx.Err(), context.DeadlineExceeded) {
			outcome.Reason = "deadline"
		} else {
			outcome.Reason = context.Cause(jobCtx).Error()
		}
	} else if execErr != nil {
		outcome.Reason = "execution_failed"
		if !result.Started {
			outcome.Reason = "spawn_failed"
		}
	}
	if !result.Retired {
		outcome.Reason = "cleanup_unverified"
	}
	// Freeze execution outcome before storage-only draining: a later deadline
	// cannot retroactively terminate a command that has already retired.
	if reason := queue.close(); reason != "" {
		outcome.Result.CaptureLost = true
		outcome.Result.CaptureReason = reason
	}
	outcome.CapturedBytes = queue.captured
	outcome.LastSequence = queue.lastSequence
	finishCtx, stopFinish := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	err = run.Store.Finish(finishCtx, outcome)
	stopFinish()
	outcome.TerminalPersisted = err == nil

	if !result.Retired {
		outcome.Reason = "cleanup_unverified"
	}
	if err := sendNotice(events, notice{Version: protocolVersion, Type: "terminal", Outcome: &outcome}); err != nil {
		return err
	}
	return nil
}

func sendNotice(f *os.File, n notice) error {
	if err := f.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return errors.New("supervisor event deadline unavailable")
	}
	return writeFrame(f, n)
}

func validateLeases(owner, lifecycle *os.File) error {
	a, err := owner.Stat()
	if err != nil {
		return errors.New("invalid owner lease")
	}
	b, err := lifecycle.Stat()
	if err != nil || os.SameFile(a, b) {
		return errors.New("invalid lifecycle lease")
	}
	for i, f := range []*os.File{owner, lifecycle} {
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return errors.New("unsafe supervisor lease")
		}
		mode := "READ"
		if i == 0 {
			mode = "WRITE"
		}
		if err := verifyDescriptorLock(f, mode); err != nil {
			return err
		}
	}
	return nil
}

// Linux show_fd_locks filters flock entries by their struct file, so fdinfo
// proves this open-file description owns the lock. Probing a new shared flock
// cannot prove inheritance: an unrelated shared holder would also allow it.
// Never acquire/convert/unlock a lease while validating its provenance.
func verifyDescriptorLock(f *os.File, mode string) error {
	info, err := os.Open("/proc/self/fdinfo/" + strconv.Itoa(int(f.Fd())))
	if err != nil {
		return errors.New("supervisor lock information unavailable")
	}
	defer info.Close()
	b, err := io.ReadAll(io.LimitReader(info, 4097))
	if err != nil || len(b) > 4096 {
		return errors.New("supervisor lock information exceeded")
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 9 && fields[0] == "lock:" && fields[2] == "FLOCK" && fields[3] == "ADVISORY" && fields[4] == mode && fields[7] == "0" && fields[8] == "EOF" {
			return nil
		}
	}
	return errors.New("supervisor descriptor does not own the required lease")
}
