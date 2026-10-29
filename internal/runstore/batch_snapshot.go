package runstore

import (
	"context"
	"database/sql"
	"time"

	"github.com/serpro69/capy/internal/executor"
)

type batchChild struct {
	id, label                 string
	ordinal                   int
	created                   int64
	terminal, outputAvailable bool
	result                    Result
	closed                    bool
	disposition               *runResolution
}

// batchChildren obtains each copy, retained row and aggregate in one snapshot.
// Evicting rows still carry authoritative proof until their final deletion.
func batchChildren(ctx context.Context, query *sql.Tx, id string) ([]batchChild, executor.CleanupEvidence, error) {
	rows, err := query.QueryContext(ctx, `SELECT c.run_id,c.ordinal,c.label,CASE WHEN length(c.result) BETWEEN 1 AND 8192 THEN c.result ELSE NULL END,c.terminal,b.created_at,b.generation,b.slot,
r.cleanup_scope,r.cleanup,r.terminal,r.generation,r.slot,CASE WHEN length(r.result) BETWEEN 1 AND 8192 THEN r.result ELSE NULL END,
EXISTS(SELECT 1 FROM run_owners o WHERE o.slot=b.slot AND o.generation=b.generation AND o.cleanup_unverified=1),
(r.id IS NOT NULL AND r.evicting=0 AND (r.expires_at IS NULL OR r.expires_at>?))
FROM run_batch_children c JOIN run_batches b ON b.id=c.batch_id
LEFT JOIN runs r ON r.id=c.run_id WHERE c.batch_id=? ORDER BY c.ordinal LIMIT 257`, time.Now().UTC().UnixMilli(), id)
	if err != nil {
		return nil, executor.CleanupEvidence{}, storageError()
	}
	defer rows.Close()
	children := make([]batchChild, 0)
	type retainedCopy struct {
		generation, scope, state sql.NullString
		slot                     sql.NullInt64
		terminal                 sql.NullBool
		encoded                  []byte
		batchGeneration          string
		batchSlot                int
		unverified               bool
	}
	retained := []retainedCopy{}
	var aggregate executor.CleanupEvidence
	allNotSpawned, allTerminal := true, true
	for rows.Next() {
		var child batchChild
		var encoded []byte
		var copy retainedCopy
		if err := rows.Scan(&child.id, &child.ordinal, &child.label, &encoded, &child.terminal, &child.created, &copy.batchGeneration, &copy.batchSlot, &copy.scope, &copy.state, &copy.terminal, &copy.generation, &copy.slot, &copy.encoded, &copy.unverified, &child.outputAvailable); err != nil {
			return nil, aggregate, storageError()
		}
		child.result, err = decodeManifest(encoded)
		if err != nil {
			return nil, aggregate, err
		}
		c := child.result.Cleanup
		if err := validateTerminal(c, child.result, child.terminal); err != nil {
			return nil, aggregate, err
		}
		if child.result.RunID == nil || *child.result.RunID != child.id || child.result.BatchID != id || child.result.OwnerID != copy.batchGeneration || child.ordinal != len(children) {
			return nil, aggregate, failure("cleanup_unverified", "batch child identity disagrees with its manifest")
		}
		if copy.scope.Valid || copy.state.Valid || copy.terminal.Valid {
			if !copy.scope.Valid || !copy.state.Valid || !copy.terminal.Valid || executor.CleanupScope(copy.scope.String) != c.Scope || executor.CleanupState(copy.state.String) != c.State || copy.terminal.Bool != child.terminal || !copy.generation.Valid || copy.generation.String != copy.batchGeneration || !copy.slot.Valid || int(copy.slot.Int64) != copy.batchSlot {
				return nil, aggregate, failure("cleanup_unverified", "batch child cleanup copy disagrees with retained run")
			}
			var current Result
			if decodeRunResult(copy.encoded, c, &current) != nil || !samePlanExecution(current, child.result) || !child.terminal && !sameDispositionExecution(child.result, current) {
				return nil, aggregate, failure("cleanup_unverified", "batch child outcome copy disagrees with retained run")
			}
		}
		if len(children) == 0 {
			aggregate.Scope = c.Scope
		} else if aggregate.Scope != c.Scope {
			return nil, aggregate, failure("cleanup_unverified", "batch children have mixed cleanup scopes")
		}
		allNotSpawned = allNotSpawned && c.State == executor.CleanupNotSpawned
		allTerminal = allTerminal && child.terminal && CleanupAccepted(c)
		children = append(children, child)
		retained = append(retained, copy)
	}
	if rows.Err() != nil || len(children) == 0 || len(children) > 256 {
		return nil, aggregate, storageError()
	}
	if err := rows.Close(); err != nil {
		return nil, aggregate, storageError()
	}
	// Additional metadata reads share this snapshot after releasing the active
	// iterator; the store's single-connection pool is never recursively acquired.
	for i := range children {
		child, copy := &children[i], retained[i]
		d, err := readRunResolution(ctx, query, child.id)
		if err != nil {
			return nil, aggregate, err
		}
		if !copy.terminal.Valid && !child.terminal && (d == nil || d.PayloadState != "evicted") {
			return nil, aggregate, failure("cleanup_unverified", "nonterminal batch child has no retained run or validated eviction")
		}
		child.disposition, child.closed = d, child.terminal || d != nil
		if d != nil {
			child.result.Recovery = dispositionView(d)
			if child.result.Capture.State != "not_started" {
				child.result.Capture.State = "incomplete"
			}
			child.outputAvailable = d.PayloadState == "retained" && time.Now().Before(*child.result.Recovery.RetentionExpiresAt)
		} else {
			available, err := projectRunHistory(ctx, query, child.id, copy.batchGeneration, &child.result, child.terminal, copy.unverified, time.Now())
			if err != nil {
				return nil, aggregate, err
			}
			child.outputAvailable = child.outputAvailable && available
		}
	}
	aggregate.State = executor.CleanupPending
	if allNotSpawned {
		aggregate.State = executor.CleanupNotSpawned
	} else if allTerminal {
		aggregate.State = executor.CleanupRetired
		if aggregate.Scope == executor.CleanupDarwinObservation {
			aggregate.State = executor.CleanupObserved
		}
	}
	return children, aggregate, nil
}

func (s *Store) batchSnapshot(ctx context.Context, id string) (BatchResult, []string, []batchChild, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return BatchResult{}, nil, nil, storageError()
	}
	defer tx.Rollback()
	b, queries, _, err := batchMetadata(ctx, tx, id)
	if err != nil {
		return b, nil, nil, err
	}
	children, proof, err := batchChildren(ctx, tx, id)
	if err != nil {
		return b, nil, nil, err
	}
	b.Cleanup = proof
	allTerminal := true
	for _, child := range children {
		allTerminal = allTerminal && child.terminal
	}
	if allTerminal != (b.CompletedAt != nil) {
		return b, nil, nil, failure("cleanup_unverified", "batch completion disagrees with child evidence")
	}
	if err := projectBatchRecovery(ctx, tx, &b, children); err != nil {
		return b, nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return b, nil, nil, storageError()
	}
	return b, queries, children, nil
}
