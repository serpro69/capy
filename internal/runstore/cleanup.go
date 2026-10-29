package runstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/serpro69/capy/internal/executor"
)

// These predicates are deliberately paired with scope even on schema-checked
// data. Recovery and retention must never treat a bare state as permission.
const acceptedCleanupSQL = `((cleanup_scope='linux_descendants_v1' AND cleanup IN ('not_spawned','retired')) OR (cleanup_scope='darwin_process_group_observation_v1' AND cleanup IN ('not_spawned','observed')))`

func ValidateCleanup(c executor.CleanupEvidence) error {
	if c.Valid() {
		return nil
	}
	return failure("cleanup_unverified", "missing, unsupported or inconsistent cleanup scope/state")
}

// CleanupAccepted permits normal reuse/retention under the named backend policy
// only with the separate completed-outcome and lease conditions. It does not
// establish that all processes are gone, especially for Darwin observation.
func CleanupAccepted(c executor.CleanupEvidence) bool {
	return ValidateCleanup(c) == nil && c.State != executor.CleanupPending
}

func validateCleanupTransition(before, after executor.CleanupEvidence) error {
	if err := ValidateCleanup(before); err != nil {
		return err
	}
	if err := ValidateCleanup(after); err != nil {
		return err
	}
	if before.Scope != after.Scope {
		return failure("cleanup_unverified", "run cleanup scope cannot change")
	}
	if before == after || before.State == executor.CleanupPending {
		return nil
	}
	if before.State == executor.CleanupNotSpawned && after.State == executor.CleanupPending {
		return nil
	}
	return failure("cleanup_unverified", "invalid run cleanup transition")
}

func validateTerminal(c executor.CleanupEvidence, r Result, terminal bool) error {
	if err := ValidateCleanup(c); err != nil {
		return err
	}
	if terminal != (r.CompletedAt != nil && CleanupAccepted(c)) {
		return failure("cleanup_unverified", "run terminal marker disagrees with cleanup evidence and outcome")
	}
	return nil
}

func encodeManifest(r Result) ([]byte, error) {
	if err := ValidateCleanup(r.Cleanup); err != nil {
		return nil, err
	}
	r.Version = EnvelopeVersion
	r.Recovery = nil // administrative views never become execution evidence
	return json.Marshal(r)
}

func decodeManifest(encoded []byte) (Result, error) {
	var stored Result
	var cached struct {
		*Result
		Recovery json.RawMessage `json:"recovery"`
	}
	cached.Result = &stored
	if err := json.Unmarshal(encoded, &cached); err != nil {
		return Result{}, storageError()
	}
	if stored.Version != 1 && stored.Version != 2 {
		return Result{}, storageError()
	}
	stored.Version = EnvelopeVersion
	stored.Recovery = nil
	return stored, ValidateCleanup(stored.Cleanup)
}

// validateRunManifest compares proof copies under the caller's transaction.
// No copy is required once an expired manifest has been legitimately evicted.
func validateRunManifest(ctx context.Context, tx *sql.Tx, id string, c executor.CleanupEvidence, terminal bool) error {
	var encoded []byte
	var copiedTerminal bool
	err := tx.QueryRowContext(ctx, "SELECT result,terminal FROM run_batch_children WHERE run_id=?", id).Scan(&encoded, &copiedTerminal)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return storageError()
	}
	return validateManifestCopy(encoded, c, terminal, copiedTerminal)
}

func validateManifestCopy(encoded []byte, c executor.CleanupEvidence, terminal, copiedTerminal bool) error {
	r, err := decodeManifest(encoded)
	if err != nil {
		return err
	}
	if err := validateTerminal(r.Cleanup, r, copiedTerminal); err != nil {
		return err
	}
	if r.Cleanup != c || copiedTerminal != terminal {
		return failure("cleanup_unverified", "batch child cleanup copy disagrees with retained run")
	}
	return nil
}

func retainedCleanup(ctx context.Context, tx *sql.Tx, id string) (executor.CleanupEvidence, error) {
	var c executor.CleanupEvidence
	var encoded []byte
	var terminal bool
	if err := tx.QueryRowContext(ctx, "SELECT cleanup_scope,cleanup,result,terminal FROM runs WHERE id=?", id).Scan(&c.Scope, &c.State, &encoded, &terminal); err != nil {
		return c, storageError()
	}
	var r Result
	if decodeRunResult(encoded, c, &r) != nil {
		return c, storageError()
	}
	return c, validateTerminal(c, r, terminal)
}
