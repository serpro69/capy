package executor

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
