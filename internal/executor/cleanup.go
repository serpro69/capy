package executor

import "errors"

// CleanupScope identifies the backend policy, not a universal quiescence proof.
type CleanupScope string

const (
	CleanupLinuxDescendants  CleanupScope = "linux_descendants_v1"
	CleanupDarwinObservation CleanupScope = "darwin_process_group_observation_v1"
)

type CleanupState string

const (
	CleanupNotSpawned CleanupState = "not_spawned"
	CleanupPending    CleanupState = "pending"
	CleanupRetired    CleanupState = "retired"
	CleanupObserved   CleanupState = "observed"
)

// CleanupEvidence must be interpreted as a pair. Darwin observation completion
// explicitly permits undetected racing survivors; it never proves retirement.
type CleanupEvidence struct {
	Scope CleanupScope `json:"scope"`
	State CleanupState `json:"state"`
}

func (c CleanupEvidence) Valid() bool {
	switch c.Scope {
	case CleanupLinuxDescendants:
		return c.State == CleanupNotSpawned || c.State == CleanupPending || c.State == CleanupRetired
	case CleanupDarwinObservation:
		return c.State == CleanupNotSpawned || c.State == CleanupPending || c.State == CleanupObserved
	default:
		return false
	}
}

// Complete means the selected policy has completed or no phase was spawned.
// In particular, Darwin observation completion is not host quiescence.
func (c CleanupEvidence) Complete() bool { return c.Valid() && c.State != CleanupPending }

func combinePhaseCleanup(prior, current CleanupEvidence, phaseErr error) (CleanupEvidence, error) {
	if !prior.Valid() || !current.Valid() || prior.Scope != current.Scope {
		return CleanupEvidence{Scope: prior.Scope, State: CleanupPending}, errors.Join(phaseErr, errors.New("inconsistent phase cleanup evidence"))
	}
	if prior.State == CleanupPending || current.State == CleanupPending {
		return CleanupEvidence{Scope: prior.Scope, State: CleanupPending}, phaseErr
	}
	if current.State == CleanupNotSpawned {
		return prior, phaseErr
	}
	return current, phaseErr
}
