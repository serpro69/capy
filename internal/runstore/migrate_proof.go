package runstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/google/uuid"
	"github.com/serpro69/capy/internal/executor"
)

func legacyResult(encoded []byte, id, batch, generation string) (Result, error) {
	var r Result
	if len(encoded) == 0 || len(encoded) > batchChildReserve || json.Unmarshal(encoded, &r) != nil {
		return r, failure("schema_upgrade_failed", "malformed legacy run result; history was not changed")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(encoded, &fields) != nil || fields["cleanup"] != nil {
		return r, failure("schema_upgrade_failed", "schema-8 result unexpectedly contains scoped cleanup evidence")
	}
	if r.Version != 1 || r.Kind != "run" || r.RunID == nil || *r.RunID != id || uuid.Validate(id) != nil || r.BatchID != batch || r.OwnerID != generation {
		return r, failure("schema_upgrade_failed", "legacy run identity/version disagrees with stored history")
	}
	if r.ExitCode != nil && r.Signal != nil || r.Capture.CapturedBytes < 0 || r.Capture.ObservedBytes < 0 {
		return r, failure("schema_upgrade_failed", "legacy run outcome is inconsistent")
	}
	switch r.Capture.State {
	case "not_started", "recording", "complete", "incomplete":
	default:
		return r, failure("schema_upgrade_failed", "legacy run capture state is unsupported")
	}
	if r.CompletedAt == nil {
		if r.Capture.State == "complete" {
			return r, failure("schema_upgrade_failed", "legacy unfinished run claims complete capture")
		}
		if r.State != "admitted" && r.State != "queued" && r.State != "running" || r.Capture.TerminalPersisted {
			return r, failure("schema_upgrade_failed", "legacy unfinished run outcome is inconsistent")
		}
	} else {
		if r.Capture.State == "recording" {
			return r, failure("schema_upgrade_failed", "legacy completed run still claims recording capture")
		}
		switch r.State {
		case "exited", "start_failed", "cancelled", "timed_out", "interrupted", "skipped":
		default:
			return r, failure("schema_upgrade_failed", "legacy completed run outcome is inconsistent")
		}
		unverified := r.Error != nil && r.Error.Code == "cleanup_unverified"
		if !r.Capture.TerminalPersisted || r.ExpiresAt == nil && !unverified {
			return r, failure("schema_upgrade_failed", "legacy completion lacks its persisted outcome or expiry")
		}
	}
	return r, nil
}

func validateLegacyTerminal(proof executor.CleanupEvidence, r Result, terminal bool) error {
	if err := validateTerminal(proof, r, terminal); err != nil {
		return err
	}
	if terminal && (r.ExpiresAt == nil || r.Error != nil && r.Error.Code == "cleanup_unverified") {
		return failure("schema_upgrade_failed", "legacy terminal marker disagrees with unverified cleanup metadata")
	}
	return nil
}

// Validate in bounded batches; do not accumulate all programs/results in memory.
func validateLegacyRuns(ctx context.Context, tx *sql.Tx) error {
	last := ""
	for {
		rows, err := tx.QueryContext(ctx, `SELECT id,generation,cleanup,terminal,
CASE WHEN length(result)<=8192 THEN result ELSE NULL END FROM runs WHERE id>? ORDER BY id LIMIT 64`, last)
		if err != nil {
			return upgradeError(err)
		}
		count := 0
		for rows.Next() {
			var id, generation string
			var state executor.CleanupState
			var terminal bool
			var encoded []byte
			if err := rows.Scan(&id, &generation, &state, &terminal, &encoded); err != nil {
				rows.Close()
				return upgradeError(err)
			}
			var identity struct {
				BatchID string `json:"batch_id"`
			}
			if json.Unmarshal(encoded, &identity) != nil {
				rows.Close()
				return failure("schema_upgrade_failed", "malformed legacy run result")
			}
			r, err := legacyResult(encoded, id, identity.BatchID, generation)
			if err == nil {
				err = validateLegacyTerminal(executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: state}, r, terminal)
			}
			if err != nil {
				rows.Close()
				return err
			}
			last, count = id, count+1
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return upgradeError(err)
		}
		if count < 64 {
			return nil
		}
	}
}

type migratedChild struct {
	id      string
	encoded []byte
}

func migrateBatchProofs(ctx context.Context, tx *sql.Tx) error {
	last := ""
	for {
		rows, err := tx.QueryContext(ctx, `SELECT c.run_id,c.batch_id,b.generation,c.terminal,
CASE WHEN length(c.result)<=8192 THEN c.result ELSE NULL END,
r.cleanup,r.terminal,CASE WHEN length(r.result)<=8192 THEN r.result ELSE NULL END
FROM run_batch_children c JOIN run_batches b ON b.id=c.batch_id
LEFT JOIN runs r ON r.id=c.run_id WHERE c.run_id>? ORDER BY c.run_id LIMIT 64`, last)
		if err != nil {
			return upgradeError(err)
		}
		children := make([]migratedChild, 0, 64)
		for rows.Next() {
			var id, batch, generation string
			var terminal bool
			var copy, retained []byte
			var state sql.NullString
			var retainedTerminal sql.NullBool
			if err := rows.Scan(&id, &batch, &generation, &terminal, &copy, &state, &retainedTerminal, &retained); err != nil {
				rows.Close()
				return upgradeError(err)
			}
			encoded, err := migrateChildProof(id, batch, generation, terminal, copy, state, retainedTerminal, retained)
			if err != nil {
				rows.Close()
				return err
			}
			children = append(children, migratedChild{id: id, encoded: encoded})
			last = id
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return upgradeError(err)
		}
		for _, child := range children {
			if _, err := tx.ExecContext(ctx, "UPDATE run_batch_children SET result=? WHERE run_id=?", child.encoded, child.id); err != nil {
				return upgradeError(err)
			}
		}
		if len(children) < 64 {
			return nil
		}
	}
}

func migrateChildProof(id, batch, generation string, terminal bool, copy []byte, state sql.NullString, retainedTerminal sql.NullBool, retained []byte) ([]byte, error) {
	saved, err := legacyResult(copy, id, batch, generation)
	if err != nil {
		return nil, err
	}
	proof := executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants}
	source := copy
	if state.Valid {
		row, err := legacyResult(retained, id, batch, generation)
		if err != nil {
			return nil, err
		}
		// Schema 8 index workers updated only the retained result. Besides Index,
		// an already-claimed worker could save loadRun's unverified annotation
		// after concurrent incomplete finalization. Compare that known transform;
		// arbitrary execution/capture/terminal differences still reject.
		if !terminal && row.Error != nil && row.Error.Code == "cleanup_unverified" {
			markUnverified(&saved)
			saved.Error = row.Error // diagnostic wording is not cleanup proof
		}
		row.Index, saved.Index = Index{}, Index{}
		if !retainedTerminal.Valid || terminal != retainedTerminal.Bool || !reflect.DeepEqual(saved, row) {
			return nil, failure("schema_upgrade_failed", "legacy batch child disagrees with its retained run")
		}
		proof.State = executor.CleanupState(state.String)
		source = retained
	} else {
		if !terminal {
			return nil, failure("schema_upgrade_failed", "legacy nonterminal batch child has no retained run")
		}
		proof.State = executor.CleanupRetired
		if saved.Capture.State == "not_started" {
			proof.State = executor.CleanupNotSpawned
		}
	}
	if err := validateLegacyTerminal(proof, saved, terminal); err != nil {
		return nil, err
	}
	// RawMessage retains timestamps, counters and other historical metadata
	// without converting numbers through float64 or rewriting unrelated fields.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(source, &fields); err != nil {
		return nil, upgradeError(err)
	}
	fields["version"] = json.RawMessage("2")
	fields["cleanup"], err = json.Marshal(proof)
	if err != nil {
		return nil, upgradeError(err)
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil, upgradeError(err)
	}
	if len(encoded) > batchChildReserve {
		return nil, failure("schema_upgrade_capacity", "scoped legacy batch metadata exceeds its reserved capacity; history was not changed")
	}
	return encoded, nil
}
