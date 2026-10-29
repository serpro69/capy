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

// ExecuteBatchResponse owns a coordinator separately from active child slots.
// Admission reserves the full inventory and one slot before any child can spawn.
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
		err = s.unverifiedErrorLocked()
	case len(input.Commands) > s.policy.MaxPending-s.pending:
		err = &runstore.Error{Code: "pending_capacity_exhausted", Message: "batch inventory exceeds pending capacity; recover existing batches before retrying"}
	case s.active >= s.policy.MaxActive || s.eligibleBatchLocked(time.Now()):
		err = &runstore.Error{Code: "capacity_exhausted", Message: "batch requires one dispatchable slot; recover existing work before retrying"}
	default:
		s.active++
		s.pending += len(input.Commands)
	}
	s.mu.Unlock()
	if err != nil {
		cancel()
		release()
		result := batchRejection(err)
		result.Capacity = s.capacity()
		return result, prepare(result)
	}
	control := &runCall{cancel: cancel, reservedAt: time.Now()}
	input.Deadline = control.reservedAt.Add(timeout)
	input.EffectiveConcurrency = min(input.Concurrency, s.policy.MaxActive, len(input.Commands))
	control.stopRequest = context.AfterFunc(ctx, control.cancelAttached)
	if ctx.Err() != nil {
		control.cancelAttached()
	}
	call := &batchCall{control: control, ready: make(chan runstore.BatchResult, 1), done: make(chan struct{})}
	go func() {
		defer release()
		defer cancel()
		call.result = s.runBatch(jobCtx, input, timeout, call)
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
	} else {
		current.Cleanup.State = executor.CleanupPending
		current.Error = &runstore.Error{Code: "storage_unavailable", Message: "batch status refresh failed; recover the admitted batch before retrying"}
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

func (s *Service) runBatch(ctx context.Context, input runstore.BatchInput, timeout time.Duration, call *batchCall) runstore.BatchResult {
	registered := false
	defer func() {
		if !registered {
			s.mu.Lock()
			s.pending -= len(input.Commands)
			s.active--
			s.dispatchLocked(time.Now())
			s.mu.Unlock()
		}
	}()
	st, owner, err := s.attached(ctx, true)
	if err != nil {
		return batchRejection(err)
	}
	s.admissionMu.Lock()
	result, rows, err := st.AdmitBatch(ctx, owner, supervisorCleanupScope(), input)
	queue := &batchQueue{cutoff: input.Deadline, timeout: timeout, parallel: input.Concurrency > 1,
		ceiling: input.EffectiveConcurrency, work: make(chan batchWork, len(rows))}
	if err == nil {
		s.mu.Lock()
		s.batches[result.BatchID] = call
		for _, row := range rows {
			childCtx, cancel := context.WithCancel(ctx)
			child := &runCall{cancel: cancel, ready: make(chan runstore.Result, 1)}
			queue.children = append(queue.children, batchChild{ctx: childCtx, row: row, call: child})
			s.running[row.ID] = child
		}
		// Convert the admission reservation into the first child reservation.
		// Existing eligible batches have already had priority at admission.
		s.active--
		s.queue = append(s.queue, queue)
		s.dispatchLocked(time.Now())
		registered = true
		s.mu.Unlock()
	}
	s.admissionMu.Unlock()
	if err != nil {
		return batchRejection(err)
	}
	call.ready <- result
	// A single bounded coordinator handles skipped children. Only reserved
	// active children get workers; every worker is joined before owner release.
	finished := make(chan runstore.Result, len(rows))
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for completed := 0; completed < len(rows); {
		select {
		case work := <-queue.work:
			child := queue.children[work.ordinal]
			if work.reason != "" {
				outcome := s.finishUnstarted(st, child.row, work.reason)
				s.finishBatchChild(queue, child, outcome, false)
				finished <- outcome
				continue
			}
			go func() {
				row := child.row
				row.Program.Deadline = work.deadline
				var outcome runstore.Result
				if child.ctx.Err() != nil {
					outcome = s.finishUnstarted(st, row, "cancelled")
				} else if err := st.Dispatch(child.ctx, row.ID, owner.ID, work.deadline); err != nil {
					reason := "spawn_failed"
					if child.ctx.Err() != nil {
						reason = "cancelled"
					}
					outcome = s.finishUnstarted(st, row, reason)
				} else {
					outcome = s.executeAdmitted(child.ctx, st, owner, row, child.call)
				}
				if child.call.cleanup != nil {
					<-child.call.cleanup
				}
				s.finishBatchChild(queue, child, outcome, true)
				finished <- outcome
			}()
		case outcome := <-finished:
			completed++
			if !outcome.Capture.TerminalPersisted {
				result.Error = &runstore.Error{Code: "storage_unavailable", Message: "a child terminal outcome could not be persisted; recover batch status before any retry"}
			}
		case <-ticker.C:
			s.mu.Lock()
			s.dispatchLocked(time.Now())
			s.mu.Unlock()
		}
	}
	s.mu.Lock()
	for i, candidate := range s.queue {
		if candidate == queue {
			s.queue = append(s.queue[:i], s.queue[i+1:]...)
			break
		}
	}
	s.mu.Unlock()
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
	result.Cleanup.State = executor.CleanupPending
	return result
}

func (s *Service) finishBatchChild(queue *batchQueue, child batchChild, outcome runstore.Result, active bool) {
	s.observeCompletion(outcome)
	child.call.mu.Lock()
	child.call.result, child.call.finished = outcome, true
	child.call.mu.Unlock()
	child.call.cancel()
	s.mu.Lock()
	delete(s.running, child.row.ID)
	if active {
		s.active--
		queue.active--
	}
	if !outcome.Capture.TerminalPersisted {
		queue.stopped = true
	}
	s.dispatchLocked(time.Now())
	s.mu.Unlock()
}

func batchRejection(err error) runstore.BatchResult {
	r := reject(err)
	return runstore.BatchResult{Version: runstore.EnvelopeVersion, Kind: "rejection", State: "not_started", Error: r.Error}
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
	return (result.Search != nil && (result.Search.Error != nil || result.Search.KnowledgeError != nil)) || result.Error != nil || result.FailedCount > 0 || result.IncompleteCount > 0 || result.Kind == "rejection"
}
func BoundBatch(result runstore.BatchResult, n int) runstore.BatchResult {
	if result.Capacity != nil {
		result.Capacity = BoundResult(runstore.Result{Capacity: result.Capacity}, n).Capacity
	}
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
