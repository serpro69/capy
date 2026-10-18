package execution

import (
	"context"
	"sync"
	"time"

	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
)

// runCall serializes the request observer, handoff and explicit control. The
// worker alone publishes completion and closes done. stopRequest cannot join an
// observer already running, so that observer also checks this latch.
type runCall struct {
	mu          sync.Mutex
	handedOff   bool
	cancelled   bool
	finished    bool
	cancel      context.CancelFunc
	stopRequest func() bool
	ready       chan runstore.Result
	done        chan struct{}
	cleanup     chan struct{}
	result      runstore.Result
	reservedAt  time.Time
	deadline    time.Time
}

func (c *runCall) cancelAttached() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.handedOff && !c.finished {
		c.cancelled = true
		c.cancel()
	}
}

func (c *runCall) handoff(ctx context.Context) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx.Err() != nil || c.cancelled {
		c.cancelled = true
		c.cancel()
		return false
	}
	if c.finished {
		return false
	}
	c.handedOff = true
	c.stopRequest()
	return true
}

// ExecuteResponse prepares a fully serialized, bounded response before committing
// handoff. A preparation failure cancels and joins the attached run. No return
// value asserts delivery to the client. timeout==0 is legacy background mode:
// wait still applies, but only owner/request/explicit cancellation ends the run.
func (s *Service) ExecuteResponse(ctx context.Context, request executor.ExecRequest, intent string, timeout, wait time.Duration, prepare func(runstore.Result) error) (runstore.Result, error) {
	if timeout < 0 || wait < 0 || prepare == nil {
		result := runstore.Rejection("invalid_argument", "response execution requires nonnegative durations and a response preparer")
		if prepare != nil {
			return result, prepare(result)
		}
		return result, result.Error
	}
	return s.executeResponse(ctx, request, intent, timeout, wait, prepare)
}

func (s *Service) executeResponse(ctx context.Context, request executor.ExecRequest, intent string, timeout, wait time.Duration, prepare func(runstore.Result) error) (runstore.Result, error) {
	if prepare == nil {
		prepare = func(runstore.Result) error { return nil }
	}
	ownerCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	jobCtx, release, err := s.begin(ownerCtx, true)
	if err != nil {
		cancel()
		result := reject(err)
		if result.Error.Code == "capacity_exhausted" {
			result.Capacity = s.capacity()
		}
		return result, prepare(result)
	}
	call := &runCall{cancel: cancel, ready: make(chan runstore.Result, 1), done: make(chan struct{}), reservedAt: time.Now()}
	if timeout > 0 {
		call.deadline = call.reservedAt.Add(timeout)
	}
	call.stopRequest = context.AfterFunc(ctx, call.cancelAttached)
	// Explicitly check before starting; AfterFunc's goroutine may not run yet.
	if ctx.Err() != nil {
		call.cancelAttached()
	}
	go func() {
		result := s.execute(jobCtx, request, intent, call.deadline, call)
		call.mu.Lock()
		call.result = result
		call.finished = true
		call.stopRequest()
		call.mu.Unlock()
		cancel()
		if call.cleanup != nil {
			close(call.done) // startup outcome is available before cleanup joins
			<-call.cleanup
		}
		s.mu.Lock()
		if result.RunID != nil {
			delete(s.running, *result.RunID)
		}
		s.mu.Unlock()
		release() // includes slot release only after helper drain/finalization
		if call.cleanup == nil {
			close(call.done)
		}
	}()
	terminal := func() (runstore.Result, error) {
		<-call.done
		return call.result, prepare(call.result)
	}
	if wait < 0 {
		return terminal()
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-call.done:
		return terminal()
	case <-ctx.Done():
		call.cancelAttached()
		return terminal()
	case <-timer.C:
	}
	var current runstore.Result
	select {
	case <-call.done:
		return terminal()
	case <-ctx.Done():
		call.cancelAttached()
		return terminal()
	case current = <-call.ready:
	}
	queryCtx, stopQuery := context.WithTimeout(jobCtx, 3*time.Second)
	latest, statusErr := s.Status(queryCtx, *current.RunID)
	stopQuery()
	if statusErr == nil {
		current = latest
	}
	if err := prepare(current); err != nil {
		call.cancelAttached()
		<-call.done
		return call.result, err
	}
	if !call.handoff(ctx) {
		return terminal()
	}
	return current, nil
}

// Cancel addresses only this service's generation. Persistent PIDs never enter
// this path. Acknowledgement means control was requested, not cleanup completed.
func (s *Service) Cancel(ctx context.Context, id string) (runstore.Result, bool, error) {
	result, err := s.Status(ctx, id)
	if err != nil {
		return result, false, err
	}
	// A list/status reader can see the admission commit before the worker has
	// published its in-memory control handle. Join that publication before
	// deciding whether this owner can still cancel the run.
	s.admissionMu.Lock()
	s.mu.Lock()
	owner := s.owner
	call := s.running[id]
	s.mu.Unlock()
	s.admissionMu.Unlock()
	if owner == nil || result.OwnerID != owner.ID || result.OwnerType != "mcp" {
		return result, false, &runstore.Error{Code: "different_owner", Message: "run belongs to " + result.OwnerType + " owner " + result.OwnerID + "; cancel through that owner"}
	}
	if call == nil {
		return result, false, nil
	}
	call.mu.Lock()
	defer call.mu.Unlock()
	if call.finished {
		return call.result, false, nil
	}
	if isTerminal(result.State) {
		return result, false, nil
	}
	call.cancelled = true
	call.cancel()
	return result, true, nil
}

func isTerminal(state string) bool {
	return state != "admitted" && state != "queued" && state != "running"
}

func (s *Service) capacity() *runstore.Capacity {
	s.mu.Lock()
	defer s.mu.Unlock()
	info := &runstore.Capacity{MaxActive: s.policy.MaxActive, ActiveCount: s.active, ListAction: "capy_runs(action=list)", CancelAction: "capy_runs(action=cancel,run_id=...)"}
	if s.ownerType == "cli" {
		info.CancelAction = "interrupt the attached CLI process"
	}
	for id, call := range s.running {
		entry := runstore.ActiveRun{RunID: id, AgeMS: max(0, time.Since(call.reservedAt).Milliseconds())}
		if !call.deadline.IsZero() {
			remaining := max(0, time.Until(call.deadline).Milliseconds())
			entry.RemainingMS = &remaining
		}
		info.Runs = append(info.Runs, entry)
	}
	return info
}
