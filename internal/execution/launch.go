package execution

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
)

type supervisorLaunch func(context.Context, string, Startup, string, *runstore.LeaseRefs) (*Supervisor, error)

// Launch accepts only acquired runstore references. Services retain one launcher
// across requests so unresolved startup cleanup also blocks subsequent launches.
func Launch(ctx context.Context, executable string, start Startup, key string, refs *runstore.LeaseRefs) (*Supervisor, error) {
	return newSupervisorLaunch()(ctx, executable, start, key, refs)
}

// Supervisor owns private channels and exactly one retained child reaper. The
// startup actor supplies the join/provenance operations, fixed before publication.
// Wait is called once; Cancel is safe concurrently with it.
type Supervisor struct {
	cmd             *exec.Cmd
	input, events   *os.File
	exchangeID      string
	scope           executor.CleanupScope
	join            func() error
	knownNotSpawned func() bool
	reaped          <-chan struct{}
	mu              sync.Mutex
	closed          bool
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
	return writeFrame(s.input, control{Version: protocolVersion, ExchangeID: s.exchangeID, Action: "cancel"})
}

func (s *Supervisor) Wait() (Outcome, error) {
	defer s.events.Close()
	defer func() { s.mu.Lock(); s.closed = true; s.input.Close(); s.mu.Unlock() }()
	var terminal notice
	err := readFrame(s.events, &terminal)
	// Notice loss is not cancellation. The retained actor joins without signalling
	// after authorization, and keeps leases/reservations if reaping is unverified.
	waitErr := s.join()
	if err != nil || waitErr != nil || terminal.ExchangeID != s.exchangeID || terminal.Type != "terminal" || terminal.Outcome == nil || terminal.Outcome.validate(s.scope) != nil {
		return Outcome{Reason: "cleanup_unverified"}, errors.New("supervisor lost; durable cleanup proof must be checked before reuse")
	}
	return *terminal.Outcome, nil
}

// Unverified OS reaping keeps the service reservation and inherited references.
func (s *Supervisor) cleanupPending() <-chan struct{} {
	select {
	case <-s.reaped:
		return nil
	default:
		return s.reaped
	}
}
