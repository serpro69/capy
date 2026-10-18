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
	"github.com/serpro69/capy/internal/sanitize"
)

// Service owns runs independently of transport requests and joins every helper
// before closing database/lease handles.
type Service struct {
	mu                   sync.Mutex
	admissionMu          sync.Mutex // publishes metadata and its control handle together
	closed               bool
	unverified           bool
	jobs                 map[*serviceCall]context.CancelFunc
	running              map[string]*runCall
	active               int
	pending              int
	batches              map[string]*batchCall
	wg                   sync.WaitGroup
	store                *runstore.Store
	owner                *runstore.Owner
	project, key, binary string
	ownerType            string
	policy               config.RunsConfig
	closeDone            chan struct{}
	closeErr             error
}
type serviceCall struct{ marker byte }

func NewService(project, key string, policy config.RunsConfig) *Service {
	return &Service{project: project, key: key, policy: policy, jobs: make(map[*serviceCall]context.CancelFunc), running: make(map[string]*runCall), batches: make(map[string]*batchCall), closeDone: make(chan struct{})}
}

func (s *Service) begin(ctx context.Context, execute bool) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, &runstore.Error{Code: "storage_unavailable", Message: "execution owner is shutting down"}
	}
	if execute && s.unverified {
		return nil, nil, &runstore.Error{Code: "cleanup_unverified", Message: "this owner has unverified run cleanup; stop execution groups and recover before admitting more work"}
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
		}
		s.mu.Unlock()
		s.wg.Done()
	}, nil
}

func (s *Service) attached(ctx context.Context, create bool) (*runstore.Store, *runstore.Owner, error) {
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
		st, err := runstore.OpenWithPolicy(ctx, path, s.key, create, runstore.PolicyConfig{
			TTLHours: s.policy.TTLHours, MaxRunBytes: s.policy.MaxRunBytes, MaxRetainedBytes: s.policy.MaxRetainedBytes,
		})
		if err != nil {
			var typed *runstore.Error
			if errors.As(err, &typed) {
				copy := *typed
				copy.Message += "; selected path: " + sanitize.StripSecrets(path)
				err = &copy
			}
			return nil, nil, err
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
		return reject(err)
	}
	if err := ValidateCredentialTransfer(s.key); err != nil {
		return reject(err)
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
		return reject(err)
	}
	physical := filepath.Dir(filepath.Dir(filepath.Dir(st.Path())))
	program := runstore.Program{Owner: physical, OwnerType: s.ownerType, Request: request, Intent: intent, Deadline: deadline}
	admissionCtx, cancelAdmission := context.WithTimeout(ctx, 3*time.Second)
	s.admissionMu.Lock()
	row, err := st.Admit(admissionCtx, owner, program)
	cancelAdmission()
	if err == nil {
		s.mu.Lock()
		s.running[row.ID] = call
		s.mu.Unlock()
	}
	s.admissionMu.Unlock()
	if err != nil {
		return reject(err)
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
	h, launchErr := Launch(ctx, binary, Startup{Version: protocolVersion, RunID: row.ID, Generation: owner.ID, StorePath: st.Path(), Deadline: row.Program.Deadline}, s.key, Leases{Owner: owner.Lease, Lifecycle: st.LifecycleLease()})
	if launchErr != nil {
		if h == nil {
			if ctx.Err() != nil {
				return s.finishUnstarted(st, row, "cancelled")
			}
			return s.startFailure(st, row)
		}
		quarantineErr := s.quarantine(st)
		// Report the startup failure promptly, but retain this worker's active
		// slot and lifecycle ownership until even a stopped helper is joined.
		call.cleanup = make(chan struct{})
		go func() { h.Wait(); close(call.cleanup) }()
		result := row.Result
		result.State = "interrupted"
		reason := "supervisor_startup_failed"
		result.Reason = &reason
		result.Error = &runstore.Error{Code: "cleanup_unverified", Message: "supervisor startup outcome is unknown; inspect this run before retrying"}
		if quarantineErr != nil {
			result.Error.Message += "; admission quarantine could not be persisted"
		}
		return result
	}
	stop := context.AfterFunc(ctx, func() { h.Cancel() })
	call.ready <- row.Result
	outcome, waitErr := h.Wait()
	stop()
	queryCtx, cancelQuery := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancelQuery()
	latest, readErr := st.Load(queryCtx, row.ID)
	var quarantineErr error
	if waitErr != nil || !outcome.Result.Retired {
		if readErr != nil || latest.Cleanup == "pending" {
			quarantineErr = s.quarantine(st)
		}
		if readErr == nil && latest.Cleanup == "not_spawned" {
			return s.startFailure(st, row)
		}
	}
	if readErr == nil && latest.Result.Capture.TerminalPersisted {
		if quarantineErr != nil {
			latest.Result.Error = &runstore.Error{Code: "cleanup_unverified", Message: "process cleanup is unverified and admission quarantine could not be persisted"}
		}
		return s.withPreview(queryCtx, st, latest.Result)
	}
	result := row.Result
	if readErr == nil {
		result = latest.Result
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
		result.Error.Message += "; admission quarantine could not be persisted"
	}
	return result
}

func (s *Service) startFailure(st *runstore.Store, row runstore.Admitted) runstore.Result {
	return s.finishUnstarted(st, row, "spawn_failed")
}

func (s *Service) finishUnstarted(st *runstore.Store, row runstore.Admitted, reason string) runstore.Result {
	ctx, cancel := context.WithTimeout(context.Background(), runstore.WriteTimeout)
	defer cancel()
	if err := st.Finish(ctx, row.ID, runstore.Completion{Retired: true, Reason: reason}); err == nil {
		if latest, err := st.Load(ctx, row.ID); err == nil {
			return latest.Result
		}
	}
	result := row.Result
	runstore.ApplyOutcome(&result, runstore.Completion{Retired: true, Reason: reason})
	result.Error = &runstore.Error{Code: "storage_unavailable", Message: "supervisor did not start; finalization was unavailable"}
	return result
}

func reject(err error) runstore.Result {
	var e *runstore.Error
	if errors.As(err, &e) {
		return runstore.Rejection(e.Code, e.Message)
	}
	return runstore.Rejection("storage_unavailable", "run admission failed before execution")
}

func (s *Service) quarantine(st *runstore.Store) error {
	s.mu.Lock()
	s.unverified = true
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), runstore.WriteTimeout)
	defer cancel()
	// Pending rows and retained leases remain the fallback proof if this write
	// fails; all future calls through this owner are blocked in memory as well.
	return st.Quarantine(ctx)
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
	runstore.ApplyOutcome(&result, runstore.Completion{SpawnCode: outcome.Result.SpawnCode, Started: outcome.Result.Started, Retired: outcome.Result.Retired,
		ExitCode: outcome.Result.ExitCode, Signal: outcome.Result.Signal, Reason: outcome.Reason,
		ObservedBytes: outcome.Result.ObservedBytes, ElapsedMS: outcome.Result.ElapsedMS})
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
		case outcome.Result.CaptureLost || !outcome.Result.Retired:
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

func (s *Service) withPreview(ctx context.Context, st *runstore.Store, result runstore.Result) runstore.Result {
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
	page, err := st.ReadProjection(ctx, *result.RunID, "merged", "", min(2048, s.policy.MaxResponseBytes/4))
	if err != nil {
		result.Redaction.State = "unavailable"
		result.Redaction.CoverageCursor = nil
		return result
	}
	for _, fragment := range page.Fragments {
		result.Preview += "[" + fragment.Stream + "] " + fragment.Text
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

func NewCLIService(project, key string, policy config.RunsConfig) *Service {
	s := NewService(project, key, policy)
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
