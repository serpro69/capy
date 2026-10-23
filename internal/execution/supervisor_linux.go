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
)

// ServeInherited owns FDs 3..7: control/liveness, credential, notices, exclusive
// owner-slot lease, shared lifecycle lease. They are closed (never LOCK_UN) only
// after the store closes. Closing a duplicate must not unlock the owner's OFD.
// The function must run in an otherwise child-free dedicated process.
func ServeInherited(ctx context.Context, open OpenRun) (retErr error) {
	files, err := inheritedExchangeFiles()
	if err != nil {
		return err
	}
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	input, events := files[0], files[2]
	var run *Run
	defer func() {
		if run != nil && run.Store != nil && run.Store.Close() != nil {
			retErr = errors.Join(retErr, errors.New("closing supervisor run store"))
		}
	}()
	start, err := acceptLeaseStartup(ctx, files, linuxExchangeScope, func(admitCtx context.Context, start Startup, key string) error {
		if open == nil {
			return errors.New("run storage is not available in this build")
		}
		if err := executor.PrepareSupervisor(); err != nil {
			return err
		}
		var err error
		run, err = open(admitCtx, start, key, Leases{Owner: files[3], Lifecycle: files[4]})
		if err != nil {
			return err
		}
		if run == nil || run.Store == nil || run.Executor == nil {
			return errors.New("invalid supervisor run backend")
		}
		return nil
	})
	if err != nil {
		return err
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
		} else if command.Action == "cancel" && command.ExchangeID == start.ExchangeID {
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
	if result.StopReason != "" {
		outcome.Reason = result.StopReason
	} else if execErr != nil {
		outcome.Reason = "execution_failed"
		if !result.Started || result.SpawnCode != 0 {
			outcome.Reason = "spawn_failed"
		}
	}
	if !result.Cleanup.Complete() {
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
	if err := outcome.validate(executor.CleanupScope(start.CleanupScope)); err != nil {
		return err
	}
	finishCtx, stopFinish := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	err = run.Store.Finish(finishCtx, outcome)
	stopFinish()
	outcome.TerminalPersisted = err == nil

	if !result.Cleanup.Complete() {
		outcome.Reason = "cleanup_unverified"
	}
	if err := sendNotice(events, notice{Version: protocolVersion, ExchangeID: start.ExchangeID, Type: "terminal", Outcome: &outcome}); err != nil {
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
