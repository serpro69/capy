//go:build linux && integration

package execution

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/serpro69/capy/internal/executor"
)

// launchFixture drives synthetic backends with deliberately supplied OS leases.
// Stored service fixtures exercise the production acquisition-proven launcher.
// It reexecs the same binary in hidden supervisor mode without a shell.
// No raw source or credential appears in argv/environment. ctx bounds startup;
// after return the owner explicitly calls Cancel/Wait and owns the lifetime.
// Once a helper has started, even an error returns its handle. The owner MUST
// retain and join that handle; admission failure is not evidence of quiescence.
func launchFixture(ctx context.Context, executable string, start Startup, key string, leases Leases) (*Supervisor, error) {
	if err := start.validate(); err != nil {
		return nil, err
	}
	if ValidateCredentialTransfer(key) != nil || leases.Owner == nil || leases.Lifecycle == nil {
		return nil, errors.New("invalid supervisor launch inputs")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var files []*os.File
	keep := false
	defer func() {
		for i, f := range files {
			if !keep || (i != 1 && i != 4) {
				f.Close()
			}
		}
	}()
	for i := 0; i < 3; i++ {
		r, w, err := os.Pipe()
		if err != nil {
			return nil, errors.New("creating supervisor channels")
		}
		files = append(files, r, w)
	}
	inputR, inputW, keyR, keyW, eventR, eventW := files[0], files[1], files[2], files[3], files[4], files[5]
	cmd := exec.Command(executable, SupervisorCommand)
	cmd.ExtraFiles = []*os.File{inputR, keyR, eventW, leases.Owner, leases.Lifecycle}
	// Separate from the coordinator's process group; no parent-death signal.
	// EOF on the owner-only control writer initiates cooperative cleanup.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, errors.New("starting supervisor")
	}
	inputR.Close()
	keyR.Close()
	eventW.Close()
	failed := func() (*Supervisor, error) {
		inputW.Close()
		keyW.Close()
		eventR.Close()
		// A stopped helper cannot be joined within the admission deadline. Keep
		// its process handle visible to the owner instead of blocking startup or
		// killing a helper that may already be responsible for command cleanup.
		return fixtureSupervisor(cmd, inputW, eventR, start, true),
			errors.New("supervisor startup failed; join helper and inspect admitted run before retrying")
	}
	deadline := time.Now().Add(startupTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	for _, f := range []*os.File{inputW, keyW, eventR} {
		if err := f.SetDeadline(deadline); err != nil {
			return failed()
		}
	}
	// Cancellation interrupts blocked pipe reads/writes, without killing a
	// helper that may already need to finish cleanup under inherited leases.
	stop := context.AfterFunc(ctx, func() { inputW.Close(); keyW.Close(); eventR.Close() })
	defer stop()
	if err := writeFrame(inputW, start); err != nil {
		return failed()
	}
	if err := writeFrameLimit(keyW, []byte(key), maxCredentialFrameBytes); err != nil {
		return failed()
	}
	keyW.Close()
	x := exchangeIO{input: eventR, output: inputW, id: start.ExchangeID, deadline: deadline}
	if err := coordinateLeaseExchange(x, leases); err != nil {
		return failed()
	}
	var ready notice
	if err := readFrame(eventR, &ready); err != nil || ready.Version != protocolVersion || ready.ExchangeID != start.ExchangeID || ready.Type != "ready" || ready.CleanupScope != start.CleanupScope || ready.Outcome != nil {
		return failed()
	}
	if err := ctx.Err(); err != nil {
		return failed()
	}
	if err := writeFrame(inputW, control{Version: protocolVersion, ExchangeID: start.ExchangeID, Action: "start", CleanupScope: start.CleanupScope}); err != nil {
		return failed()
	}
	if err := eventR.SetReadDeadline(time.Time{}); err != nil {
		return failed()
	}
	if !stop() {
		return failed()
	}
	keep = true
	return fixtureSupervisor(cmd, inputW, eventR, start, false), nil
}

func fixtureSupervisor(cmd *exec.Cmd, input, events *os.File, start Startup, closed bool) *Supervisor {
	reaped := make(chan struct{})
	return &Supervisor{cmd: cmd, input: input, events: events, exchangeID: start.ExchangeID, scope: executor.CleanupScope(start.CleanupScope), closed: closed,
		knownNotSpawned: func() bool { return false }, reaped: reaped,
		join: func() error {
			err := cmd.Wait()
			if cmd.ProcessState != nil {
				close(reaped)
			}
			return err
		},
	}
}
