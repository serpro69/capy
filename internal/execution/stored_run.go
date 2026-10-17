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
	if runtime.GOOS != "linux" {
		return &runstore.Error{Code: "storage_unavailable", Message: "captured execution currently requires Linux supervision"}
	}
	return nil
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
	if row.Generation != start.Generation || row.Cleanup != "not_spawned" || row.Result.State != "admitted" || !row.Program.Deadline.Equal(start.Deadline) {
		return nil, errors.New("admitted run generation or state does not match")
	}
	if err := st.ValidateLeaseBinding(row.Slot, leases.Owner, leases.Lifecycle); err != nil {
		return nil, err
	}
	capture := &storedCapture{store: st, id: row.ID, generation: row.Generation}
	ok = true
	return &Run{Executor: executor.NewExecutor(row.Program.Owner, 0), Request: row.Program.Request, Store: capture, Started: capture.started}, nil
}

type storedCapture struct {
	store          *runstore.Store
	id, generation string
}

func (s *storedCapture) Pending(ctx context.Context) error {
	return s.store.Pending(ctx, s.id, s.generation)
}
func (s *storedCapture) started(string) error {
	// Capture writes are independently bounded even if execution cancellation is
	// already requested; a failed write must not abandon pipe draining.
	ctx, cancel := context.WithTimeout(context.Background(), runstore.WriteTimeout)
	defer cancel()
	return s.store.Started(ctx, s.id)
}
func (s *storedCapture) Append(e executor.OutputEvent) error {
	ctx, cancel := context.WithTimeout(context.Background(), runstore.WriteTimeout)
	defer cancel()
	return s.store.Append(ctx, s.id, e)
}
func (s *storedCapture) Finish(ctx context.Context, out Outcome) error {
	return s.store.Finish(ctx, s.id, runstore.Completion{Started: out.Result.Started, Retired: out.Result.Retired,
		CaptureLost: out.Result.CaptureLost, CaptureReason: out.Result.CaptureReason, ExitCode: out.Result.ExitCode, Signal: out.Result.Signal, Reason: out.Reason,
		ObservedBytes: out.Result.ObservedBytes, ElapsedMS: out.Result.ElapsedMS})
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
