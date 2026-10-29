package runstore

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
)

type recoveryBatchEvidence struct {
	ID, Generation                  string
	Slot, TTL, Requested, Effective int
	Created, Policy                 int64
	Completed, Expires              sql.NullInt64
}

func (b *recoveryInventory) batches() error {
	var orphan bool
	if err := b.tx.QueryRowContext(b.ctx, "SELECT EXISTS(SELECT 1 FROM run_batch_children c WHERE NOT EXISTS(SELECT 1 FROM run_batches b WHERE b.id=c.batch_id))").Scan(&orphan); err != nil {
		return err
	}
	if orphan {
		return recoveryInconsistent("orphan batch manifest")
	}
	after := ""
	for {
		if err := b.checkpoint(); err != nil {
			return err
		}
		rows, err := b.tx.QueryContext(b.ctx, `SELECT CASE WHEN length(id)=36 THEN id ELSE NULL END,CASE WHEN length(generation)=36 THEN generation ELSE NULL END,
 slot,ttl_hours,requested,effective,created_at,policy_revision,completed_at,expires_at FROM run_batches WHERE id>? ORDER BY id LIMIT 64`, after)
		if err != nil {
			return err
		}
		items := []recoveryBatchEvidence{}
		for rows.Next() {
			var v recoveryBatchEvidence
			if err := rows.Scan(&v.ID, &v.Generation, &v.Slot, &v.TTL, &v.Requested, &v.Effective, &v.Created, &v.Policy, &v.Completed, &v.Expires); err != nil {
				rows.Close()
				return recoveryInconsistent("batch metadata")
			}
			items = append(items, v)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, v := range items {
			if uuid.Validate(v.ID) != nil || uuid.Validate(v.Generation) != nil || v.Slot < 0 || v.Slot >= OwnerSlots || v.TTL < 1 || int64(v.TTL) > math.MaxInt64/int64(time.Hour) || v.Policy < 1 || v.Policy > b.admin.policy.Revision || v.Requested < 1 || v.Requested > 8 || v.Effective < 1 || v.Effective > v.Requested || v.Completed.Valid != v.Expires.Valid {
				return recoveryInconsistent("batch identity or retention")
			}
			if err := b.record("batch", v); err != nil {
				return err
			}
			if err := b.batch(v); err != nil {
				return err
			}
			after = v.ID
		}
		if len(items) < 64 {
			return nil
		}
	}
}

func (b *recoveryInventory) batch(batch recoveryBatchEvidence) error {
	count := 0
	allTerminal := true
	allClosed, hasDisposition := true, false
	pending := false
	scope := ""
	for {
		if err := b.checkpoint(); err != nil {
			return err
		}
		rows, err := b.tx.QueryContext(b.ctx, `SELECT CASE WHEN length(run_id)=36 THEN run_id ELSE NULL END,ordinal,terminal,
 CASE WHEN length(result) BETWEEN 1 AND 8192 THEN result ELSE NULL END FROM run_batch_children WHERE batch_id=? AND ordinal>=? ORDER BY ordinal LIMIT 64`, batch.ID, count)
		if err != nil {
			return err
		}
		type child struct {
			ID       string
			Ordinal  int
			Terminal bool
			Result   []byte
		}
		children := []child{}
		for rows.Next() {
			var c child
			if err := rows.Scan(&c.ID, &c.Ordinal, &c.Terminal, &c.Result); err != nil {
				rows.Close()
				return recoveryInconsistent("child manifest")
			}
			children = append(children, c)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, c := range children {
			if count >= 256 || c.Ordinal != count || uuid.Validate(c.ID) != nil {
				return recoveryInconsistent("batch child inventory")
			}
			r, err := decodeManifest(c.Result)
			if err != nil || !validPlanState(r.State) || validateTerminal(r.Cleanup, r, c.Terminal) != nil || r.RunID == nil || *r.RunID != c.ID || r.BatchID != batch.ID || r.OwnerID != batch.Generation {
				return recoveryInconsistent("batch child proof or identity")
			}
			if scope != "" && scope != string(r.Cleanup.Scope) {
				return recoveryInconsistent("mixed batch cleanup scopes")
			}
			scope = string(r.Cleanup.Scope)
			d, err := readRunResolution(b.ctx, b.tx, c.ID)
			if err != nil {
				return err
			}
			hasDisposition = hasDisposition || d != nil
			allClosed = allClosed && (c.Terminal || d != nil)
			if d != nil {
				if err := b.record("child-disposition", d); err != nil {
					return err
				}
			}
			var generation string
			var slot int
			var terminal bool
			var encoded []byte
			err = b.tx.QueryRowContext(b.ctx, `SELECT CASE WHEN length(generation)=36 THEN generation ELSE NULL END,slot,terminal,CASE WHEN length(result) BETWEEN 1 AND 8192 THEN result ELSE NULL END FROM runs WHERE id=?`, c.ID).Scan(&generation, &slot, &terminal, &encoded)
			if errors.Is(err, sql.ErrNoRows) {
				if !c.Terminal && (d == nil || d.PayloadState != "evicted") {
					return recoveryInconsistent("missing nonterminal batch child")
				}
			} else if err != nil {
				return err
			} else {
				var retained Result
				if generation != batch.Generation || slot != batch.Slot || terminal != c.Terminal || decodeRunResult(encoded, r.Cleanup, &retained) != nil || !samePlanExecution(retained, r) || !c.Terminal && !sameDispositionExecution(r, retained) {
					return recoveryInconsistent("batch outcome copy")
				}
				var actualScope, actualState string
				if err := b.tx.QueryRowContext(b.ctx, "SELECT CASE WHEN length(cleanup_scope)<=64 THEN cleanup_scope ELSE NULL END,CASE WHEN length(cleanup)<=16 THEN cleanup ELSE NULL END FROM runs WHERE id=?", c.ID).Scan(&actualScope, &actualState); err != nil {
					return err
				}
				if actualScope != string(r.Cleanup.Scope) || actualState != string(r.Cleanup.State) {
					return recoveryInconsistent("batch cleanup copy")
				}
			}
			if err := b.record("child", struct {
				Batch, Run string
				Ordinal    int
				Terminal   bool
				Result     [32]byte
			}{batch.ID, c.ID, c.Ordinal, c.Terminal, sha256.Sum256(c.Result)}); err != nil {
				return err
			}
			allTerminal = allTerminal && c.Terminal
			pending = pending || r.Cleanup.State == "pending" && d == nil
			count++
		}
		if len(children) < 64 {
			break
		}
	}
	if count == 0 || allTerminal != batch.Completed.Valid {
		return recoveryInconsistent("batch completion inventory")
	}
	if pending {
		b.view.Counts.Batches++
		if !hasDisposition {
			b.view.batchReservations++
		}
	}
	closure, err := readBatchClosure(b.ctx, b.tx, batch.ID)
	if err != nil {
		return err
	}
	if (closure != nil) != (hasDisposition && allClosed) {
		return recoveryInconsistent("batch closure inventory")
	}
	if hasDisposition || pending {
		base, charged, err := batchRecoveryDebit(b.ctx, b.tx, batch.ID)
		if err != nil {
			return err
		}
		if hasDisposition && closure == nil {
			base += recoveryBatchReserve
		}
		if charged < base {
			return recoveryInconsistent("batch recovery reservation")
		}
		if err := b.record("batch-recovery", struct {
			Batch   string
			Closure *batchClosure
			Debit   int64
		}{batch.ID, closure, charged}); err != nil {
			return err
		}
	}
	return nil
}

func validPlanState(state string) bool {
	return diagnosticState(state, "admitted", "queued", "running", "exited", "start_failed", "timed_out", "cancelled", "interrupted", "skipped") != "unknown"
}

// Index/error/preview fields can evolve independently of the manifest. Compare
// only execution identity/outcome facts; each proof is validated separately.
func samePlanExecution(a, b Result) bool {
	return equalPlanValue(a.RunID, b.RunID) && a.OwnerID == b.OwnerID && a.BatchID == b.BatchID && a.State == b.State && equalPlanValue(a.ExitCode, b.ExitCode) && equalPlanValue(a.Signal, b.Signal) && equalPlanValue(a.StartedAt, b.StartedAt) && equalPlanValue(a.CompletedAt, b.CompletedAt) && a.Capture.TerminalPersisted == b.Capture.TerminalPersisted
}

func equalPlanValue[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
