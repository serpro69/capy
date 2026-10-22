//go:build linux || darwin

package execution

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/serpro69/capy/internal/runstore"
)

const preStartTeardownTimeout = 3 * time.Second

// leaseLauncher is the private startup mechanism for Task 4. Task 8 will connect
// its retained reservations to Service scheduling and scoped persisted outcomes.
// One instance belongs to an execution owner, never to an individual request.
type leaseLauncher struct {
	mu           sync.Mutex
	reservations map[*exchangeProcess]bool // true means unresolved failed startup
	// Operations are fixed before use; fixtures inject signal/wait/write faults.
	kill       func(*os.Process) error
	wait       func(*exec.Cmd) error
	startWrite func(*os.File, leaseMessage) error
	authorize  func(context.Context, *exchangeProcess, exchangeIO, string) error
}

func newLeaseLauncher() *leaseLauncher {
	return &leaseLauncher{
		reservations: make(map[*exchangeProcess]bool),
		kill:         (*os.Process).Kill,
		wait:         (*exec.Cmd).Wait,
		startWrite:   func(f *os.File, m leaseMessage) error { return writeFrame(f, m) },
		authorize: func(ctx context.Context, p *exchangeProcess, x exchangeIO, scope string) error {
			return p.authorize(ctx, x, scope)
		},
	}
}

type exchangeProcess struct {
	owner                     *leaseLauncher
	cmd                       *exec.Cmd
	refs                      *runstore.LeaseRefs
	gate                      *runstore.ExchangeLease
	input, credential, events *os.File
	mu                        sync.Mutex
	cancelled                 bool
	startWriteAttempted       bool
	waitStarted               bool
	done                      chan struct{}
	waitErr                   error // published by closing done
	terminal                  bool  // actual OS terminal status, not signal delivery
}

func (l *leaseLauncher) blockedLocked() bool {
	for _, blocked := range l.reservations {
		if blocked {
			return true
		}
	}
	return false
}

func (l *leaseLauncher) launch(ctx context.Context, cmd *exec.Cmd, start exchangeStartup, key string, refs *runstore.LeaseRefs) (_ *exchangeProcess, retErr error) {
	// The budget begins before any gate wait. A run deadline cannot be extended
	// by serialization, process creation, or the helper's own startup timeout.
	deadline := minDeadline(time.Now().Add(startupTimeout), start.Deadline)
	if d, ok := ctx.Deadline(); ok {
		deadline = minDeadline(deadline, d)
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	start.Version, start.ExchangeID = exchangeProtocolVersion, uuid.NewString()
	if err := start.validate(); err != nil {
		return nil, err
	}
	if err := ValidateCredentialTransfer(key); err != nil {
		return nil, err
	}
	if cmd == nil || cmd.Process != nil || len(cmd.ExtraFiles) != 0 {
		return nil, errors.New("invalid exchange helper command")
	}
	l.mu.Lock()
	blocked := l.blockedLocked()
	l.mu.Unlock()
	if blocked {
		return nil, errors.New("lease startup cleanup is unresolved")
	}
	pin, err := refs.Retain()
	if err != nil {
		return nil, err
	}
	gate, err := pin.AcquireExchange(ctx)
	if err != nil {
		return nil, errors.Join(err, pin.Close())
	}
	var pipes []*os.File
	started := false
	defer func() {
		if !started {
			for _, f := range pipes {
				f.Close()
			}
			retErr = errors.Join(retErr, gate.Release(), pin.Close())
		}
	}()
	for range 3 {
		r, w, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		pipes = append(pipes, r, w)
	}
	p := &exchangeProcess{owner: l, cmd: cmd, refs: pin, gate: gate,
		input: pipes[1], credential: pipes[3], events: pipes[4], done: make(chan struct{})}
	// Check again after gate waiting: another exchange may have become stranded.
	l.mu.Lock()
	if l.blockedLocked() {
		l.mu.Unlock()
		return nil, errors.New("lease startup cleanup is unresolved")
	}
	l.reservations[p] = false
	l.mu.Unlock()
	err = pin.WithFiles(string(start.StorePath), start.Generation, func(owner, lifecycle *os.File) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		cmd.ExtraFiles = []*os.File{pipes[0], pipes[2], pipes[5], owner, lifecycle}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		err := cmd.Start()
		cmd.ExtraFiles = nil
		return err
	})
	if err != nil {
		l.mu.Lock()
		delete(l.reservations, p)
		l.mu.Unlock()
		return nil, err
	}
	started = true
	pipes[0].Close()
	pipes[2].Close()
	pipes[5].Close()
	cancelDone := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { p.cancelStartup(); close(cancelDone) })
	stopCancellation := func() {
		if !stop() {
			<-cancelDone
		}
	}
	err = pin.WithFiles(string(start.StorePath), start.Generation, func(owner, lifecycle *os.File) error {
		if err := p.input.SetWriteDeadline(minDeadline(deadline, time.Now().Add(writeTimeout))); err != nil {
			return err
		}
		if err := writeFrame(p.input, start); err != nil {
			return err
		}
		if err := p.credential.SetWriteDeadline(minDeadline(deadline, time.Now().Add(writeTimeout))); err != nil {
			return err
		}
		if err := writeFrameLimit(p.credential, []byte(key), maxCredentialFrameBytes); err != nil {
			return err
		}
		if err := p.credential.Close(); err != nil {
			return err
		}
		x := exchangeIO{input: p.events, output: p.input, id: start.ExchangeID, deadline: deadline}
		if err := coordinateLeaseExchange(x, Leases{Owner: owner, Lifecycle: lifecycle}); err != nil {
			return err
		}
		ready, err := x.receive("", "ready", "", 0)
		if err != nil {
			return err
		}
		if ready.CleanupScope != start.CleanupScope {
			return errors.New("helper cleanup scope mismatch")
		}
		// The helper has permanently stopped seeking. This exact token is safe
		// to return now; later cancellation must not affect another exchange.
		if err := p.gate.Release(); err != nil {
			return err
		}
		return l.authorize(ctx, p, x, start.CleanupScope)
	})
	stopCancellation()
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		if err = p.events.SetReadDeadline(time.Time{}); err == nil {
			return p, nil
		}
	}
	l.mu.Lock()
	l.reservations[p] = true
	l.mu.Unlock()
	p.cancelStartup()
	signalErr := p.beginWait(true)
	// This observation budget is independent of the expired startup context.
	teardown, stopTeardown := context.WithTimeout(context.Background(), preStartTeardownTimeout)
	defer stopTeardown()
	return p, errors.Join(err, signalErr, p.waitFor(teardown))
}

func (p *exchangeProcess) authorize(ctx context.Context, x exchangeIO, scope string) error {
	if err := p.input.SetWriteDeadline(minDeadline(x.deadline, time.Now().Add(writeTimeout))); err != nil {
		return err
	}
	p.mu.Lock()
	if p.cancelled || ctx.Err() != nil {
		p.mu.Unlock()
		return errors.New("lease startup cancelled before authorization")
	}
	p.startWriteAttempted = true // even a zero-byte failure may have authorized work
	p.mu.Unlock()
	return p.owner.startWrite(p.input, leaseMessage{Version: exchangeProtocolVersion, ExchangeID: x.id, Action: "start", CleanupScope: scope})
}

func (p *exchangeProcess) cancelStartup() {
	p.mu.Lock()
	p.cancelled = true
	p.mu.Unlock()
	p.input.Close()
	p.credential.Close()
	p.events.Close()
}

// beginWait serializes the final possible direct-child signal with ownership of
// the sole waiter. The waiter is retained by this actor and the owner's registry;
// a response timeout never releases references or starts a competing reap.
func (p *exchangeProcess) beginWait(failed bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.waitStarted {
		return nil
	}
	var signalErr error
	if failed && !p.startWriteAttempted {
		signalErr = p.owner.kill(p.cmd.Process)
	}
	p.waitStarted = true
	go func() {
		err := p.owner.wait(p.cmd)
		if p.cmd.ProcessState != nil {
			if status, ok := p.cmd.ProcessState.Sys().(syscall.WaitStatus); ok {
				p.terminal = status.Exited() || status.Signaled()
			}
		}
		if p.terminal {
			err = errors.Join(err, p.gate.Release(), p.refs.Close())
		}
		p.input.Close()
		p.credential.Close()
		p.events.Close()
		p.waitErr = err
		p.owner.mu.Lock()
		if p.terminal {
			delete(p.owner.reservations, p)
		} else {
			p.owner.reservations[p] = true
		}
		close(p.done)
		p.owner.mu.Unlock()
	}()
	return signalErr
}

func (p *exchangeProcess) waitFor(ctx context.Context) error {
	select {
	case <-p.done:
		if !p.terminal {
			return errors.Join(errors.New("helper reap is unverified"), p.waitErr)
		}
		return p.waitErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *exchangeProcess) Wait(ctx context.Context) error {
	if err := p.beginWait(false); err != nil {
		return err
	}
	return p.waitFor(ctx)
}

func (p *exchangeProcess) knownNotSpawned() bool {
	select {
	case <-p.done:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.terminal && !p.startWriteAttempted
	default:
		return false
	}
}
