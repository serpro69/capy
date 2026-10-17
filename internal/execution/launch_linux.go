package execution

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Leases are borrowed open-file descriptions. Launch never unlocks or closes
// them. The owner must release them by closing, not flock(LOCK_UN), because the
// supervisor's inherited references protect exactly the same lock descriptions.
// Supervisor owns the child handle and private channels, not store/owner leases.
// Wait must be called exactly once. Cancel is safe concurrently with Wait.
type Supervisor struct {
	cmd           *exec.Cmd
	input, events *os.File
	mu            sync.Mutex
	closed        bool
}

// Launch reexecs the same binary in hidden supervisor mode without a shell.
// No raw source or credential appears in argv/environment. ctx bounds startup;
// after return the owner explicitly calls Cancel/Wait and owns the lifetime.
// Once a helper has started, even an error returns its handle. The owner MUST
// retain and join that handle; admission failure is not evidence of quiescence.
func Launch(ctx context.Context, executable string, start Startup, key string, leases Leases) (*Supervisor, error) {
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
		return &Supervisor{cmd: cmd, input: inputW, events: eventR, closed: true},
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
	var ready notice
	if err := readFrame(eventR, &ready); err != nil || ready.Version != protocolVersion || ready.Type != "ready" || ready.Outcome != nil {
		return failed()
	}
	if err := ctx.Err(); err != nil {
		return failed()
	}
	if err := writeFrame(inputW, control{Action: "start"}); err != nil {
		return failed()
	}
	if err := eventR.SetReadDeadline(time.Time{}); err != nil {
		return failed()
	}
	if !stop() {
		return failed()
	}
	keep = true
	return &Supervisor{cmd: cmd, input: inputW, events: eventR}, nil
}

func (s *Supervisor) Cancel() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	defer s.input.Close()
	if err := s.input.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return errors.New("setting supervisor cancel deadline")
	}
	return writeFrame(s.input, control{Action: "cancel"})
}

func (s *Supervisor) Wait() (Outcome, error) {
	defer s.events.Close()
	defer func() { s.mu.Lock(); s.closed = true; s.input.Close(); s.mu.Unlock() }()
	var terminal notice
	err := readFrame(s.events, &terminal)
	// Notice loss is not cancellation. The command still owns its deadline and
	// liveness channel; keep joining it without turning a reporting failure into
	// an execution failure. Owner shutdown can independently call Cancel.
	waitErr := s.cmd.Wait()
	if err != nil || waitErr != nil || terminal.Version != protocolVersion || terminal.Type != "terminal" || terminal.Outcome == nil {
		return Outcome{Reason: "cleanup_unverified"}, errors.New("supervisor lost; durable cleanup proof must be checked before reuse")
	}
	return *terminal.Outcome, nil
}
