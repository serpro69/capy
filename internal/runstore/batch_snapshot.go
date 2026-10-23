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
}

type rowsQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// batchChildren obtains each copy, retained row and aggregate in one snapshot.
// Evicting rows still carry authoritative proof until their final deletion.
func batchChildren(ctx context.Context, query rowsQuerier, id string) ([]batchChild, executor.CleanupEvidence, error) {
	rows, err := query.QueryContext(ctx, `SELECT c.run_id,c.ordinal,c.label,c.result,c.terminal,b.created_at,
r.cleanup_scope,r.cleanup,r.terminal,
(r.id IS NOT NULL AND r.evicting=0 AND (r.expires_at IS NULL OR r.expires_at>?))
FROM run_batch_children c JOIN run_batches b ON b.id=c.batch_id
LEFT JOIN runs r ON r.id=c.run_id WHERE c.batch_id=? ORDER BY c.ordinal LIMIT 257`, time.Now().UTC().UnixMilli(), id)
	if err != nil {
		return nil, executor.CleanupEvidence{}, storageError()
	}
	defer rows.Close()
	children := make([]batchChild, 0)
	var aggregate executor.CleanupEvidence
	allNotSpawned, allTerminal := true, true
	for rows.Next() {
		var child batchChild
		var encoded []byte
		var scope, state sql.NullString
		var terminal sql.NullBool
		if err := rows.Scan(&child.id, &child.ordinal, &child.label, &encoded, &child.terminal, &child.created, &scope, &state, &terminal, &child.outputAvailable); err != nil {
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
		if child.result.RunID == nil || *child.result.RunID != child.id || child.result.BatchID != id {
			return nil, aggregate, failure("cleanup_unverified", "batch child identity disagrees with its manifest")
		}
		if scope.Valid || state.Valid || terminal.Valid {
			if !scope.Valid || !state.Valid || !terminal.Valid || executor.CleanupScope(scope.String) != c.Scope || executor.CleanupState(state.String) != c.State || terminal.Bool != child.terminal {
				return nil, aggregate, failure("cleanup_unverified", "batch child cleanup copy disagrees with retained run")
			}
		} else if !child.terminal {
			return nil, aggregate, failure("cleanup_unverified", "nonterminal batch child has no retained run")
		}
		if len(children) == 0 {
			aggregate.Scope = c.Scope
		} else if aggregate.Scope != c.Scope {
			return nil, aggregate, failure("cleanup_unverified", "batch children have mixed cleanup scopes")
		}
		allNotSpawned = allNotSpawned && c.State == executor.CleanupNotSpawned
		allTerminal = allTerminal && child.terminal && CleanupAccepted(c)
		children = append(children, child)
	}
	if rows.Err() != nil || len(children) == 0 || len(children) > 256 {
		return nil, aggregate, storageError()
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
	if err := tx.Commit(); err != nil {
		return b, nil, nil, storageError()
	}
	return b, queries, children, nil
}
