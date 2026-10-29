package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
)

// Service owns runs independently of transport requests and joins every helper
// before closing database/lease handles.
type Service struct {
	launch               supervisorLaunch
	mu                   sync.Mutex
	attachMu             sync.Mutex // serialize attachment without blocking owner cancellation during migration
	admissionMu          sync.Mutex // publishes metadata and its control handle together
	closed               bool
	unverified           bool
	unverifiedContext    *runstore.RuntimeIncident
	unverifiedDurable    bool
	jobs                 map[*serviceCall]context.CancelFunc
	running              map[string]*runCall
	active               int
	pending              int
	batches              map[string]*batchCall
	queue                []*batchQueue
	wg                   sync.WaitGroup
	store                *runstore.Store
	owner                *runstore.Owner
	project, key, binary string
	ownerType            string
	policy               config.RunsConfig
	closeDone            chan struct{}
	closeErr             error
	onCompletion         func(runstore.Result)
	previewLimit         int
}
type serviceCall struct{ marker byte }

type ServiceOption func(*Service)

// WithPreviewLimit applies the legacy executor setting only to optional preview
// text. Nonpositive values retain the shared response budget's default behavior.
func WithPreviewLimit(bytes int) ServiceOption {
	return func(s *Service) { s.previewLimit = bytes }
}

// WithCompletionObserver installs a synchronous observer before any work starts.
// It receives each admitted standalone/child outcome once, including after
// handoff. The observer must be concurrency-safe and must not block on the owner.
func WithCompletionObserver(observer func(runstore.Result)) ServiceOption {
	return func(s *Service) { s.onCompletion = observer }
}

func NewService(project, key string, policy config.RunsConfig, options ...ServiceOption) *Service {
	s := &Service{launch: newSupervisorLaunch(), project: project, key: key, policy: policy, jobs: make(map[*serviceCall]context.CancelFunc), running: make(map[string]*runCall), batches: make(map[string]*batchCall), closeDone: make(chan struct{})}
	for _, option := range options {
		option(s)
	}
	return s
}

func (s *Service) observeCompletion(result runstore.Result) {
	if result.RunID != nil && s.onCompletion != nil {
		s.onCompletion(result)
	}
}

// WorkCounts reports only this owner's live reservations, including finalizers.
// It never opens storage or treats persisted metadata as live process evidence.
func (s *Service) WorkCounts() (active, pending int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active, s.pending
}

func (s *Service) begin(ctx context.Context, execute bool) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, &runstore.Error{Code: "storage_unavailable", Message: "execution owner is shutting down"}
	}
	if execute && s.unverified {
		return nil, nil, s.unverifiedErrorLocked()
	}
	if execute {
		s.dispatchLocked(time.Now())
	}
	if execute && s.active >= s.policy.MaxActive {
		return nil, nil, &runstore.Error{Code: "capacity_exhausted", Message: "execution owner is at capacity; recover existing runs before retrying"}
	}
	job := new(serviceCall)
	ctx, cancel := context.WithCancel(ctx)
	s.jobs[job] = cancel
	if execute {
		s.active++
	}
	s.wg.Add(1)
	return ctx, func() {
		cancel()
		s.mu.Lock()
		delete(s.jobs, job)
		if execute {
			s.active--
			s.dispatchLocked(time.Now())
		}
		s.mu.Unlock()
		s.wg.Done()
	}, nil
}

func (s *Service) attached(ctx context.Context, create bool) (*runstore.Store, *runstore.Owner, error) {
	s.attachMu.Lock()
	defer s.attachMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, &runstore.Error{Code: "storage_unavailable", Message: "execution owner is shutting down"}
	}
	if err := ValidateCredentialTransfer(s.key); err != nil {
		return nil, nil, err
	}
	if s.store == nil {
		path, err := config.ResolveRunDBPath(s.project)
		if err != nil {
			return nil, nil, &runstore.Error{Code: "storage_unavailable", Message: "selected run owner is unavailable"}
		}
		// Schema migration may copy substantial history. Close must be able to
		// acquire mu and cancel this registered request while attachment runs.
		s.mu.Unlock()
		st, err := runstore.OpenWithPolicy(ctx, path, s.key, create, runstore.PolicyConfig{
			TTLHours: s.policy.TTLHours, MaxRunBytes: s.policy.MaxRunBytes, MaxRetainedBytes: s.policy.MaxRetainedBytes,
		})
		s.mu.Lock()
		if err != nil {
			return nil, nil, err
		}
		if s.closed {
			s.mu.Unlock()
			closeErr := st.Close()
			s.mu.Lock()
			return nil, nil, errors.Join(&runstore.Error{Code: "storage_unavailable", Message: "execution owner is shutting down"}, closeErr)
		}
		s.store = st
	}
	if create && s.owner == nil {
		owner, err := s.store.ClaimOwner(ctx)
		if err != nil {
			return nil, nil, err
		}
		s.owner = owner
	}
	return s.store, s.owner, nil
}

func (s *Service) Execute(ctx context.Context, request executor.ExecRequest, intent string, timeout time.Duration) runstore.Result {
	if timeout <= 0 {
		return runstore.Rejection("invalid_argument", "execution timeout must be positive")
	}
	result, _ := s.executeResponse(ctx, request, intent, timeout, -1, nil)
	return result
}

// execute runs on an owner-managed worker. Request cancellation is forwarded
// through runCall, never inherited directly by this context.
func (s *Service) execute(ctx context.Context, request executor.ExecRequest, intent string, deadline time.Time, call *runCall) runstore.Result {
	if err := Supported(); err != nil {
		return s.reject(err)
	}
	if err := ValidateCredentialTransfer(s.key); err != nil {
		return s.reject(err)
	}
	cwd, err := executor.ResolveCwd(s.project, request.Cwd)
	if err != nil {
		return runstore.Rejection("invalid_argument", "execution cwd is unavailable")
	}
	request.Cwd = cwd
	if request.Code == "" || request.Background {
		return runstore.Rejection("invalid_argument", "captured execution requires nonempty synchronous code")
	}
	st, owner, err := s.attached(ctx, true)
	if err != nil {
		return s.reject(err)
	}
	physical := filepath.Dir(filepath.Dir(filepath.Dir(st.Path())))
	program := runstore.Program{Owner: physical, OwnerType: s.ownerType, Request: request, Intent: intent, Deadline: deadline}
	admissionCtx, cancelAdmission := context.WithTimeout(ctx, 3*time.Second)
	s.admissionMu.Lock()
	row, err := st.Admit(admissionCtx, owner, supervisorCleanupScope(), program)
	cancelAdmission()
	if err == nil {
		s.mu.Lock()
		s.running[row.ID] = call
		s.mu.Unlock()
	}
	s.admissionMu.Unlock()
	if err != nil {
		return s.reject(err)
	}
	return s.executeAdmitted(ctx, st, owner, row, call)
}

func (s *Service) executeAdmitted(ctx context.Context, st *runstore.Store, owner *runstore.Owner, row runstore.Admitted, call *runCall) runstore.Result {
	if ctx.Err() != nil {
		return s.finishUnstarted(st, row, "cancelled")
	}
	var err error
	binary := s.binary
	if binary == "" {
		binary, err = os.Executable()
	}
	if err != nil {
		return s.startFailure(st, row)
	}
	refs, err := st.BorrowLeases(owner)
	if err != nil {
		return s.startFailure(st, row)
	}
	h, launchErr := s.launch(ctx, binary, Startup{Version: protocolVersion, RunID: row.ID, Generation: owner.ID, StorePath: st.Path(), Deadline: row.Program.Deadline, CleanupScope: string(row.Cleanup.Scope)}, s.key, refs)
	launchErr = errors.Join(launchErr, refs.Close())
	if launchErr != nil {
		if h == nil || h.knownNotSpawned() {
			if ctx.Err() != nil {
				return s.finishUnstarted(st, row, "cancelled")
			}
			return s.startFailure(st, row)
		}
		if row.Result.BatchID != "" && ctx.Err() != nil {
			// The batch coordinator already joins all children before completing.
			// Retain its slot/leases while resolving cancellation during startup;
			// a joined helper and durable not_spawned proof permit finalization.
			h.Wait()
			call.cleanup = h.cleanupPending()
			queryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			latest, err := st.Load(queryCtx, row.ID)
			cancel()
			if err == nil && latest.Result.CompletedAt != nil && runstore.CleanupAccepted(latest.Cleanup) && latest.Result.Capture.TerminalPersisted {
				return latest.Result
			}
			if err == nil && latest.Result.CompletedAt == nil && latest.Cleanup == (executor.CleanupEvidence{Scope: row.Cleanup.Scope, State: executor.CleanupNotSpawned}) {
				return s.finishUnstarted(st, row, "cancelled")
			}
			// Unknown proof still quarantines; this helper has already been joined.
			result := row.Result
			if err == nil {
				result = latest.Result
			} else {
				result.Cleanup.State = executor.CleanupPending
			}
			result.State = "interrupted"
			reason := "supervisor_startup_failed"
			result.Reason = &reason
			result.Error = &runstore.Error{Code: "cleanup_unverified", Message: "cancelled helper cleanup could not be verified; recover batch status before retrying"}
			if err := s.quarantine(st, row, runstore.IncidentStartupUncertain, runstore.StageStartup); err != nil {
				result.Error.Message += "; incident attribution persistence was not confirmed"
			}
			return result
		}
		quarantineErr := s.quarantine(st, row, runstore.IncidentStartupUncertain, runstore.StageStartup)
		// Report the startup failure promptly, but retain this worker's active
		// slot and lifecycle ownership until even a stopped helper is joined.
		call.cleanup = h.cleanupPending()
		go func() { h.Wait() }()
		result := row.Result
		// Launch can fail after attempting start authorization. The admission
		// snapshot is no longer evidence that no command phase was started.
		queryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		latest, err := st.Load(queryCtx, row.ID)
		cancel()
		if err == nil {
			result = latest.Result
		} else {
			result.Cleanup.State = executor.CleanupPending
		}
		result.State = "interrupted"
		reason := "supervisor_startup_failed"
		result.Reason = &reason
		result.Error = &runstore.Error{Code: "cleanup_unverified", Message: "supervisor startup outcome is unknown; inspect this run before retrying"}
		if quarantineErr != nil {
			result.Error.Message += "; incident attribution persistence was not confirmed"
		}
		return result
	}
	stop := context.AfterFunc(ctx, func() { h.Cancel() })
	call.ready <- row.Result
	outcome, waitErr := h.Wait()
	call.cleanup = h.cleanupPending()
	stop()
	queryCtx, cancelQuery := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancelQuery()
	latest, readErr := st.Load(queryCtx, row.ID)
	var quarantineErr error
	// A locally completed policy cannot authorize reuse when finalization left
	// durable proof pending. Reconciliation skips this still-held owner slot,
	// so quarantine it here as well as during later owner recovery.
	if waitErr != nil || !outcome.TerminalPersisted || !outcome.Result.Cleanup.Complete() {
		if readErr != nil || latest.Cleanup == (executor.CleanupEvidence{Scope: row.Cleanup.Scope, State: executor.CleanupPending}) {
			cause, stage := runstore.IncidentPendingCleanup, runstore.StageFinalization
			if waitErr != nil {
				cause, stage = runstore.IncidentSupervisorLost, runstore.StageHelperWait
			} else if !outcome.TerminalPersisted {
				cause = runstore.IncidentFinalizationUnpersisted
			}
			quarantineErr = s.quarantine(st, row, cause, stage)
		}
		if readErr == nil && latest.Result.CompletedAt == nil && latest.Cleanup == (executor.CleanupEvidence{Scope: row.Cleanup.Scope, State: executor.CleanupNotSpawned}) {
			if ctx.Err() != nil {
				return s.finishUnstarted(st, row, "cancelled")
			}
			return s.startFailure(st, row)
		}
	}
	if readErr == nil && latest.Result.Capture.TerminalPersisted {
		if quarantineErr != nil {
			latest.Result.Error = &runstore.Error{Code: "cleanup_unverified", Message: "process cleanup is unverified and incident attribution persistence was not confirmed"}
		}
		return s.withPreview(queryCtx, st, latest.Result)
	}
	result := row.Result
	if readErr == nil {
		result = latest.Result
	} else {
		result.Cleanup.State = executor.CleanupPending
	}
	result.State = "interrupted"
	result.Capture.State = "incomplete"
	result.Capture.TerminalPersisted = false
	if waitErr == nil {
		result = knownOutcome(result, outcome, st.CommittedCursor(row.ID, outcome.LastSequence))
	} else {
		reason := "terminal_unavailable"
		result.Reason = &reason
		result.Error = &runstore.Error{Code: "storage_unavailable", Message: "terminal outcome is unknown; recover this run before any retry"}
	}

	if quarantineErr != nil {
		result.Error.Message += "; incident attribution persistence was not confirmed"
	}
	return result
}

func (s *Service) startFailure(st *runstore.Store, row runstore.Admitted) runstore.Result {
	if !row.Program.Deadline.IsZero() && !time.Now().Before(row.Program.Deadline) {
		return s.finishUnstarted(st, row, "deadline")
	}
	return s.finishUnstarted(st, row, "spawn_failed")
}

func (s *Service) finishUnstarted(st *runstore.Store, row runstore.Admitted, reason string) runstore.Result {
	ctx, cancel := context.WithTimeout(context.Background(), runstore.WriteTimeout)
	defer cancel()
	if err := st.Finish(ctx, row.ID, runstore.Completion{Cleanup: executor.CleanupEvidence{Scope: row.Cleanup.Scope, State: executor.CleanupNotSpawned}, Reason: reason}); err == nil {
		if latest, err := st.Load(ctx, row.ID); err == nil {
			return latest.Result
		}
	}
	result := row.Result
	runstore.ApplyOutcome(&result, runstore.Completion{Cleanup: executor.CleanupEvidence{Scope: row.Cleanup.Scope, State: executor.CleanupNotSpawned}, Reason: reason})
	result.Error = &runstore.Error{Code: "storage_unavailable", Message: "supervisor did not start; finalization was unavailable"}
	return result
}

func (s *Service) reject(err error) runstore.Result {
	e := runstore.AdmissionError(err, s.project)
	result := runstore.Rejection(e.Code, e.Message)
	result.Error = e
	return result
}

func (s *Service) quarantine(st *runstore.Store, row runstore.Admitted, cause runstore.IncidentCause, stage runstore.IncidentStage) error {
	// Keep one immutable subject, not the admitted program/result or an unbounded
	// incident list. Concurrent failures still each attempt durable publication.
	subject := runstore.RuntimeIncident{RunID: row.ID, Generation: row.Generation, Slot: row.Slot, BatchID: row.Result.BatchID, Cause: cause, Stage: stage}
	s.mu.Lock()
	s.unverified = true
	if s.unverifiedContext == nil {
		s.unverifiedContext = &subject
	}
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), runstore.WriteTimeout)
	// Pending rows and retained leases remain the fallback proof if this write
	// fails; all future calls through this owner are blocked in memory as well.
	err := st.QuarantineRun(ctx, subject)
	cancel()
	if err != nil {
		// Deadline expiry or SQLite transaction-wide rollback can invalidate the
		// attribution savepoint and its marker. Give the existing marker-only
		// path an independent bounded attempt; never retry execution/finalization
		// or claim that this fallback made attribution durable.
		markerCtx, stopMarker := context.WithTimeout(context.Background(), runstore.WriteTimeout)
		markerErr := st.Quarantine(markerCtx)
		stopMarker()
		return errors.Join(err, markerErr)
	}
	s.mu.Lock()
	if s.unverifiedContext == &subject {
		s.unverifiedDurable = true
	}
	s.mu.Unlock()
	return nil
}

// The caller holds mu. Context describes this owner's first refusal only; the
// durable inventory may contain additional incidents from concurrent children.
func (s *Service) unverifiedErrorLocked() *runstore.Error {
	return runstore.RuntimeAdmissionError(s.project, s.unverifiedContext, s.unverifiedDurable)
}

func (s *Service) Status(ctx context.Context, id string) (runstore.Result, error) {
	ctx, done, err := s.begin(ctx, false)
	if err != nil {
		return runstore.Result{}, err
	}
	defer done()
	st, _, err := s.attached(ctx, false)
	if err != nil {
		return runstore.Result{}, err
	}
	row, err := st.Load(ctx, id)
	return row.Result, err
}

func (s *Service) ReadRaw(ctx context.Context, id, stream, cursor string, maxBytes int) (runstore.ReadPage, error) {
	ctx, done, err := s.begin(ctx, false)
	if err != nil {
		return runstore.ReadPage{}, err
	}
	defer done()
	st, _, err := s.attached(ctx, false)
	if err != nil {
		return runstore.ReadPage{}, err
	}
	return st.ReadRaw(ctx, id, stream, cursor, maxBytes)
}

func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.closeDone
		return s.closeErr
	}
	s.closed = true
	for _, cancel := range s.jobs {
		cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
	var err error
	if s.owner != nil {
		err = s.owner.Lease.Close()
	}
	if s.store != nil {
		err = errors.Join(err, s.store.Close())
	}
	s.closeErr = err
	close(s.closeDone)
	return err
}

// knownOutcome preserves an authenticated helper acknowledgement even when the
// subsequent metadata refresh fails. Query failure is not finalization failure.
func knownOutcome(result runstore.Result, outcome Outcome, cursor *string) runstore.Result {
	completion, err := completionFromOutcome(result.Cleanup.Scope, outcome)
	if err != nil {
		result.Error = &runstore.Error{Code: "cleanup_unverified", Message: err.Error()}
		return result
	}
	runstore.ApplyOutcome(&result, completion)
	if outcome.TerminalPersisted {
		result.Cleanup = completion.Cleanup
	} else {
		// The helper reached execution; failed finalization cannot replace its
		// durable pending proof with a local completed/not-spawned observation.
		result.Cleanup.State = executor.CleanupPending
	}
	result.Capture.CapturedBytes = outcome.CapturedBytes
	result.Capture.LastCommittedCursor = cursor
	result.Capture.TerminalPersisted = outcome.TerminalPersisted
	result.Capture.State = "incomplete"
	reason := "finalization_failed"
	if outcome.TerminalPersisted {
		result.Capture.State = "complete"
		switch {
		case !outcome.Result.Started:
			result.Capture.State = "not_started"
		case outcome.Result.CaptureLost || !outcome.Result.Cleanup.Complete():
			result.Capture.State = "incomplete"
		}
		result.Capture.Reason = nil
		if outcome.Result.CaptureReason != "" {
			reason = outcome.Result.CaptureReason
			result.Capture.Reason = &reason
		}
		result.Error = &runstore.Error{Code: "storage_unavailable", Message: "terminal outcome was recorded but metadata refresh failed; recover this run before any retry"}
	} else {
		result.Capture.Reason = &reason
		result.Error = &runstore.Error{Code: "storage_unavailable", Message: "terminal outcome was not durably recorded; recover this run before any retry"}
	}
	return result
}

func (s *Service) ReadProjection(ctx context.Context, id, stream, cursor string, maxBytes int) (runstore.ReadPage, error) {
	ctx, done, err := s.begin(ctx, false)
	if err != nil {
		return runstore.ReadPage{}, err
	}
	defer done()
	st, _, err := s.attached(ctx, false)
	if err != nil {
		return runstore.ReadPage{}, err
	}
	return st.ReadProjection(ctx, id, stream, cursor, maxBytes)
}

func (s *Service) withPreview(ctx context.Context, st *runstore.Store, result runstore.Result) (previewed runstore.Result) {
	defer func() {
		if s.previewLimit > 0 {
			previewed = boundPreview(previewed, s.previewLimit)
		}
	}()
	if result.RunID == nil || result.Redaction.State == "unavailable" {
		return result
	}
	row, loadErr := st.Load(ctx, *result.RunID)
	if loadErr == nil && row.Program.Intent != "" {
		page, err := s.Search(ctx, runstore.SearchOptions{RunID: *result.RunID, Queries: []string{row.Program.Intent}, Limit: 3})
		if err == nil {
			result.Index = page.Index
			for _, group := range page.Results {
				for _, hit := range group.Hits {
					result.Preview += "[" + hit.Stream + "] " + hit.Text
				}
			}
			result = BoundResult(result, min(2048, s.policy.MaxResponseBytes/4))
		} else {
			result.Index.State = "failed"
			result.Index.Error = &runstore.Error{Code: "index_failed", Message: "intent search unavailable; recover this run with read or search"}
		}
		if result.Preview != "" {
			return result
		}
	}
	budget := min(2048, s.policy.MaxResponseBytes/4)
	page, err := st.ReadProjection(ctx, *result.RunID, "merged", "", budget/2)
	if err != nil {
		result.Redaction.State = "unavailable"
		result.Redaction.CoverageCursor = nil
		return result
	}
	for _, fragment := range page.Fragments {
		result.Preview += "[" + fragment.Stream + "] " + fragment.Text
	}
	if !page.EOF {
		for _, stream := range []string{"stdout", "stderr"} {
			tail, err := st.ReadProjectionTail(ctx, *result.RunID, stream, budget/4)
			if err != nil {
				continue // The validated head remains a safe diagnostic hint.
			}
			for _, fragment := range tail.Fragments {
				result.Preview += "[" + stream + " tail] " + fragment.Text
			}
		}
	}
	return result
}

func (s *Service) Read(ctx context.Context, id string, options runstore.ReadOptions) (runstore.ReadPage, error) {
	ctx, done, err := s.begin(ctx, false)
	if err != nil {
		return runstore.ReadPage{}, err
	}
	defer done()
	st, _, err := s.attached(ctx, false)
	if err != nil {
		return runstore.ReadPage{}, err
	}
	return st.Read(ctx, id, options)
}

func NewCLIService(project, key string, policy config.RunsConfig, options ...ServiceOption) *Service {
	s := NewService(project, key, policy, options...)
	s.ownerType = "cli"
	return s
}

func (s *Service) List(ctx context.Context, options runstore.ListOptions) (runstore.ListPage, error) {
	ctx, done, err := s.begin(ctx, false)
	if err != nil {
		return runstore.ListPage{}, err
	}
	defer done()
	st, _, err := s.attached(ctx, false)
	if err != nil {
		return runstore.ListPage{}, err
	}
	return st.List(ctx, options)
}
