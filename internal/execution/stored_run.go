package execution

import (
	"context"
	"encoding/base64"
	"errors"
	"runtime"

	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
)

func Supported() error {
	switch runtime.GOOS {
	case "linux":
		// Subreaper preparation changes process-wide ownership and belongs only
		// in the helper. Preserve Linux's existing admission behavior.
		return nil
	case "darwin":
		// Darwin preparation only checks the compiled backend and verified host
		// matrix; it neither spawns a process nor opens run storage.
		if err := executor.PrepareSupervisor(); err != nil {
			return &runstore.Error{Code: "storage_unavailable", Message: "captured execution unavailable: " + err.Error()}
		}
		return nil
	default:
		return &runstore.Error{Code: "storage_unavailable", Message: "captured execution requires Linux or macOS 26 on Apple Silicon with CGO"}
	}
}

func ValidateCredentialTransfer(key string) error {
	if key == "" || len(key) > (maxCredentialFrameBytes-2)/4*3 || base64.StdEncoding.EncodedLen(len(key))+2 > maxCredentialFrameBytes {
		return &runstore.Error{Code: "storage_unavailable", Message: "selected run credential cannot be transferred through the bounded private channel"}
	}
	return nil
}

// OpenStoredRun is the production helper backend. It uses only the credential
// captured by the service, authenticates the selected cache, and binds the
// admitted generation to the inherited locks before returning any program.
func OpenStoredRun(ctx context.Context, start Startup, key string, leases Leases) (*Run, error) {
	st, err := runstore.Open(ctx, start.StorePath, key, false)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			st.Close()
		}
	}()
	row, err := st.Load(ctx, start.RunID)
	if err != nil {
		return nil, err
	}
	if row.Generation != start.Generation || start.CleanupScope != string(row.Cleanup.Scope) || !row.Cleanup.Valid() || row.Cleanup != (executor.CleanupEvidence{Scope: supervisorCleanupScope(), State: executor.CleanupNotSpawned}) || (row.Result.State != "admitted" && (row.Result.State != "queued" || row.Result.BatchID == "")) || !row.Program.Deadline.Equal(start.Deadline) {
		return nil, errors.New("admitted run generation or state does not match")
	}
	policy := st.Policy()
	if row.Program.PolicyRevision != policy.Revision || row.Program.TTLHours != policy.TTLHours || row.Program.MaxRunBytes != policy.MaxRunBytes {
		return nil, &runstore.Error{Code: "policy_mismatch", Message: "admitted run does not match the attached store policy"}
	}
	if err := st.ValidateLeaseBinding(row.Slot, leases.Owner, leases.Lifecycle); err != nil {
		return nil, err
	}
	capture := &storedCapture{store: st, id: row.ID, generation: row.Generation, scope: row.Cleanup.Scope}
	ok = true
	return &Run{Executor: executor.NewExecutor(row.Program.Owner, 0), Request: row.Program.Request, Store: capture, Started: capture.started}, nil
}

type storedCapture struct {
	scope          executor.CleanupScope
	store          *runstore.Store
	id, generation string
}

func (s *storedCapture) Pending(ctx context.Context) error {
	return s.store.Pending(ctx, s.id, s.generation, s.scope)
}
func (s *storedCapture) started(phase string) error {
	// Capture writes are independently bounded even if execution cancellation is
	// already requested; a failed write must not abandon pipe draining.
	ctx, cancel := context.WithTimeout(context.Background(), runstore.WriteTimeout)
	defer cancel()
	return s.store.Started(ctx, s.id, phase)
}
func (s *storedCapture) Append(e executor.OutputEvent) error {
	ctx, cancel := context.WithTimeout(context.Background(), runstore.WriteTimeout)
	defer cancel()
	return s.store.Append(ctx, s.id, e)
}
func (s *storedCapture) Finish(ctx context.Context, out Outcome) error {
	c, err := completionFromOutcome(s.scope, out)
	if err != nil {
		return err
	}
	return s.store.Finish(ctx, s.id, c)
}
func (s *storedCapture) Close() error { return s.store.Close() }

func (s *storedCapture) AppendBatch(events []executor.OutputEvent) error {
	ctx, cancel := context.WithTimeout(context.Background(), runstore.WriteTimeout)
	defer cancel()
	return s.store.AppendBatch(ctx, s.id, events)
}

func (s *storedCapture) CaptureFailed(reason string) error {
	ctx, cancel := context.WithTimeout(context.Background(), runstore.WriteTimeout)
	defer cancel()
	return s.store.CaptureFailed(ctx, s.id, reason)
}
