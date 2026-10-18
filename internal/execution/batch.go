package execution

import (
	"context"
	"time"

	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
)

type batchCall struct {
	control *runCall
	ready   chan runstore.BatchResult
	done    chan struct{}
	result  runstore.BatchResult
}

// ExecuteBatchResponse owns a serial coordinator separately from active child
// slots. Task 13 adds fair parallel dispatch; this path never uses the legacy
// executor or knowledge-store output pipeline.
func (s *Service) ExecuteBatchResponse(ctx context.Context, input runstore.BatchInput, timeout, wait time.Duration, prepare func(runstore.BatchResult) error) (runstore.BatchResult, error) {
	if err := input.Validate(); err != nil {
		return runstore.BatchResult{}, err
	}
	if timeout <= 0 || wait < 0 || prepare == nil {
		return runstore.BatchResult{}, &runstore.Error{Code: "invalid_argument", Message: "batch requires a positive timeout and nonnegative wait"}
	}
	if err := Supported(); err != nil {
		return runstore.BatchResult{}, err
	}
	if err := ValidateCredentialTransfer(s.key); err != nil {
		return runstore.BatchResult{}, err
	}
	cwd, err := executor.ResolveCwd(s.project, input.Cwd)
	if err != nil {
		return runstore.BatchResult{}, &runstore.Error{Code: "invalid_argument", Message: "execution cwd is unavailable"}
	}
	input.Cwd = cwd
	input.Commands = append([]runstore.BatchCommand(nil), input.Commands...)
	input.Queries = append([]string(nil), input.Queries...)
	ownerCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	jobCtx, release, err := s.begin(ownerCtx, false)
	if err != nil {
		cancel()
		return runstore.BatchResult{}, err
	}
	// Reserve all pending inventory and the first dispatchable slot atomically.
	s.mu.Lock()
	switch {
	case s.unverified:
		err = &runstore.Error{Code: "cleanup_unverified", Message: "execution owner has unverified cleanup"}
	case len(input.Commands) > s.policy.MaxPending-s.pending:
		err = &runstore.Error{Code: "pending_capacity_exhausted", Message: "batch inventory exceeds pending capacity; recover existing batches before retrying"}
	case s.active >= s.policy.MaxActive:
		err = &runstore.Error{Code: "capacity_exhausted", Message: "batch requires one dispatchable slot; recover existing work before retrying"}
	default:
		s.active++
		s.pending += len(input.Commands)
	}
	s.mu.Unlock()
	if err != nil {
		cancel()
		release()
		return runstore.BatchResult{}, err
	}
	control := &runCall{cancel: cancel, reservedAt: time.Now()}
	input.Deadline = control.reservedAt.Add(timeout)
	control.stopRequest = context.AfterFunc(ctx, control.cancelAttached)
	if ctx.Err() != nil {
		control.cancelAttached()
	}
	call := &batchCall{control: control, ready: make(chan runstore.BatchResult, 1), done: make(chan struct{})}
	go func() {
		defer release()
		defer cancel()
		call.result = s.runBatch(jobCtx, input, call)
		control.mu.Lock()
		control.finished = true
		control.stopRequest()
		control.mu.Unlock()
		s.mu.Lock()
		delete(s.batches, call.result.BatchID)
		s.mu.Unlock()
		close(call.done)
	}()
	terminal := func() (runstore.BatchResult, error) { <-call.done; return call.result, prepare(call.result) }
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-call.done:
		return terminal()
	case <-ctx.Done():
		control.cancelAttached()
		return terminal()
	case <-timer.C:
	}
	var current runstore.BatchResult
	select {
	case <-call.done:
		return terminal()
	case <-ctx.Done():
		control.cancelAttached()
		return terminal()
	case current = <-call.ready:
	}
	queryCtx, stop := context.WithTimeout(jobCtx, 3*time.Second)
	if latest, err := s.BatchStatus(queryCtx, current.BatchID); err == nil {
		current = latest
	}
	stop()
	if err := prepare(current); err != nil {
		control.cancelAttached()
		<-call.done
		return call.result, err
	}
	if !control.handoff(ctx) {
		return terminal()
	}
	return current, nil
}

func (s *Service) runBatch(ctx context.Context, input runstore.BatchInput, call *batchCall) runstore.BatchResult {
	remaining := len(input.Commands)
	reserved := true
	defer func() {
		s.mu.Lock()
		s.pending -= remaining
		if reserved {
			s.active--
		}
		s.mu.Unlock()
	}()
	st, owner, err := s.attached(ctx, true)
	if err != nil {
		return batchRejection(err)
	}
	s.admissionMu.Lock()
	result, rows, err := st.AdmitBatch(ctx, owner, input)
	children := make([]*runCall, len(rows))
	contexts := make([]context.Context, len(rows))
	if err == nil {
		s.mu.Lock()
		s.batches[result.BatchID] = call
		for i, row := range rows {
			childCtx, cancel := context.WithCancel(ctx)
			contexts[i] = childCtx
			child := &runCall{cancel: cancel, ready: make(chan runstore.Result, 1), reservedAt: time.Now(), deadline: input.Deadline}
			children[i] = child
			s.running[row.ID] = child
		}
		s.mu.Unlock()
	}
	s.admissionMu.Unlock()
	if err != nil {
		return batchRejection(err)
	}
	call.ready <- result
	stopped := false
	for i, row := range rows {
		child := children[i]
		childCtx := contexts[i]
		reason := ""
		if childCtx.Err() != nil {
			reason = "cancelled"
		} else if !time.Now().Before(input.Deadline) {
			reason = "queue_timeout"
		} else if stopped {
			reason = "batch_stopped"
		}
		if reason == "" && !reserved {
			reason = s.reserveBatchChild(childCtx, input.Deadline)
			reserved = reason == ""
		}
		s.mu.Lock()
		s.pending--
		s.mu.Unlock()
		remaining--
		var outcome runstore.Result
		if reason != "" {
			outcome = s.finishUnstarted(st, row, reason)
		} else {
			outcome = s.executeAdmitted(childCtx, st, owner, row, child)
		}
		child.mu.Lock()
		child.result = outcome
		child.finished = true
		child.mu.Unlock()
		child.cancel()
		if child.cleanup != nil {
			<-child.cleanup
		}
		s.mu.Lock()
		delete(s.running, row.ID)
		if reserved {
			s.active--
			reserved = false
		}
		stopped = stopped || s.unverified
		s.mu.Unlock()
		if !outcome.Capture.TerminalPersisted {
			result.Error = &runstore.Error{Code: "storage_unavailable", Message: "a child terminal outcome could not be persisted; recover batch status before any retry"}
			stopped = true
		}
	}
	queryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	latest, err := st.BatchStatus(queryCtx, result.BatchID)
	if err == nil {
		if result.Error != nil {
			latest.Error = result.Error
		}
		return latest
	}
	result.Error = &runstore.Error{Code: "storage_unavailable", Message: "batch status refresh failed; recover the admitted batch before retrying"}
	return result
}

func batchRejection(err error) runstore.BatchResult {
	r := reject(err)
	return runstore.BatchResult{Version: 1, Kind: "rejection", State: "not_started", Error: r.Error}
}

func (s *Service) reserveBatchChild(ctx context.Context, deadline time.Time) string {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return "cancelled"
		}
		if !time.Now().Before(deadline) {
			return "queue_timeout"
		}
		s.mu.Lock()
		if s.unverified {
			s.mu.Unlock()
			return "batch_stopped"
		}
		if s.active < s.policy.MaxActive {
			s.active++
			s.mu.Unlock()
			return ""
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return "cancelled"
		case <-ticker.C:
		}
	}
}

func (s *Service) BatchStatus(ctx context.Context, id string) (runstore.BatchResult, error) {
	ctx, done, err := s.begin(ctx, false)
	if err != nil {
		return runstore.BatchResult{}, err
	}
	defer done()
	st, _, err := s.attached(ctx, false)
	if err != nil {
		return runstore.BatchResult{}, err
	}
	return st.BatchStatus(ctx, id)
}
func (s *Service) CancelBatch(ctx context.Context, id string) (runstore.BatchResult, bool, error) {
	result, err := s.BatchStatus(ctx, id)
	if err != nil {
		return result, false, err
	}
	s.admissionMu.Lock()
	s.mu.Lock()
	owner := s.owner
	call := s.batches[id]
	s.mu.Unlock()
	s.admissionMu.Unlock()
	if owner == nil || result.OwnerID != owner.ID || s.ownerType == "cli" {
		return result, false, &runstore.Error{Code: "different_owner", Message: "batch belongs to mcp owner " + result.OwnerID + "; cancel through that owner"}
	}
	if call == nil || result.CompletedAt != nil {
		return result, false, nil
	}
	call.control.mu.Lock()
	defer call.control.mu.Unlock()
	if call.control.finished {
		return result, false, nil
	}
	call.control.cancelled = true
	call.control.cancel()
	return result, true, nil
}

func BatchFailed(result runstore.BatchResult) bool {
	return result.Error != nil || result.FailedCount > 0 || result.IncompleteCount > 0 || result.Kind == "rejection"
}
func BoundBatch(result runstore.BatchResult, n int) runstore.BatchResult {
	if result.Error != nil {
		e := *result.Error
		e.Message = SafeMessage(e.Message, n)
		result.Error = &e
	}
	if result.Search != nil {
		page := BoundSearch(*result.Search, n)
		result.Search = &page
	}
	return result
}
