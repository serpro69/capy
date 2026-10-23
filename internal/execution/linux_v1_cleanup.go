package execution

import (
	"errors"

	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
)

// linuxV1Completion is the Task 5 transitional adapter for the explicitly
// selected Linux backend. TODO(Task 8): remove when production protocol v2
// carries typed evidence. Unknown/Darwin scopes cannot use the v1 Retired bit.
func linuxV1Completion(scope executor.CleanupScope, out Outcome) (runstore.Completion, error) {
	if scope != executor.CleanupLinuxDescendants {
		return runstore.Completion{}, errors.New("linux v1 completion requires linux descendant scope")
	}
	state := executor.CleanupPending
	if out.Result.Retired {
		state = executor.CleanupRetired
		if !out.Result.Started {
			state = executor.CleanupNotSpawned
		}
	}
	return runstore.Completion{Cleanup: executor.CleanupEvidence{Scope: scope, State: state},
		SpawnCode: out.Result.SpawnCode, Started: out.Result.Started,
		CaptureLost: out.Result.CaptureLost, CaptureReason: out.Result.CaptureReason,
		ExitCode: out.Result.ExitCode, Signal: out.Result.Signal, Reason: out.Reason,
		ObservedBytes: out.Result.ObservedBytes, ElapsedMS: out.Result.ElapsedMS}, nil
}
