package runstore

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// All probes start with a receipt/incident index and then use the referenced
// primary/unique key. Incident-only members pin their run/manifest just like
// dispositions. Cross-receipt references also keep their dependency alive.
// Expiry and quota pressure cannot override any of these pins.
const recoveryGroupUnreferencedSQL = `
 NOT EXISTS(SELECT 1 FROM run_batch_resolutions b WHERE b.receipt_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM run_resolutions d WHERE d.receipt_id=r.id AND
  (d.payload_state<>'evicted' OR EXISTS(SELECT 1 FROM runs p WHERE p.id=d.run_id)
   OR EXISTS(SELECT 1 FROM run_batches b WHERE b.id=d.batch_id)
   OR EXISTS(SELECT 1 FROM run_batch_children c WHERE c.run_id=d.run_id)))
 AND NOT EXISTS(SELECT 1 FROM run_incidents i WHERE i.receipt_id=r.id AND
  (EXISTS(SELECT 1 FROM runs p WHERE p.id=i.run_id)
   OR EXISTS(SELECT 1 FROM run_batches b WHERE b.id=i.batch_id)
   OR EXISTS(SELECT 1 FROM run_batch_children c WHERE c.run_id=i.run_id)
   OR EXISTS(SELECT 1 FROM run_resolutions d WHERE d.incident_id=i.id AND d.receipt_id<>r.id)
   OR EXISTS(SELECT 1 FROM run_incident_resolutions m WHERE m.incident_id=i.id AND m.receipt_id<>r.id)))
 AND NOT EXISTS(SELECT 1 FROM run_incident_resolutions m JOIN run_incidents i ON i.id=m.incident_id
  WHERE m.receipt_id=r.id AND (i.receipt_id IS NULL OR i.receipt_id<>r.id))`

// pruneRecovery is called only by ordinary Maintain, never admission/capture,
// upgrade or apply. One pass advances one group by at most budget rows. The
// receipt's identity and immutable counts survive every intermediate commit.
func pruneRecovery(ctx context.Context, tx *sql.Tx, budget int) (int64, bool, error) {
	if budget <= 0 {
		return 0, false, nil
	}
	var id string
	err := tx.QueryRowContext(ctx, "SELECT id FROM run_recovery_receipts WHERE pruning=1 ORDER BY audit_expires_at,id LIMIT 1").Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		now := time.Now().UTC().UnixMilli()
		err = tx.QueryRowContext(ctx, `SELECT r.id FROM run_recovery_receipts r
 WHERE r.pruning=0 AND r.audit_expires_at<=? AND `+recoveryGroupUnreferencedSQL+`
 AND NOT EXISTS(SELECT 1 FROM run_resolutions d WHERE d.receipt_id=r.id AND d.audit_expires_at>?)
 AND NOT EXISTS(SELECT 1 FROM run_incidents i WHERE i.receipt_id=r.id AND (i.audit_expires_at IS NULL OR i.audit_expires_at>?))
 AND NOT EXISTS(SELECT 1 FROM run_incident_resolutions m WHERE m.receipt_id=r.id AND m.audit_expires_at>?)
 ORDER BY r.audit_expires_at,r.id LIMIT 1`, now, now, now, now).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, "UPDATE run_recovery_receipts SET pruning=1 WHERE id=?", id)
		}
	}
	if err != nil {
		return 0, false, metadataReadError(err)
	}
	// A partially pruned group has no remaining operational consumers. Refuse
	// unexpected new references rather than deleting underneath their evidence.
	var unreferenced bool
	if err := tx.QueryRowContext(ctx, "SELECT "+recoveryGroupUnreferencedSQL+" FROM run_recovery_receipts r WHERE r.id=?", id).Scan(&unreferenced); err != nil {
		return 0, false, metadataReadError(err)
	}
	if !unreferenced {
		return 0, false, recoveryInconsistent("referenced pruning receipt")
	}
	var deleted, debit int64
	// Fixed table/key identifiers. No cascade may silently remove charged audit.
	for _, table := range []struct{ name, key string }{
		{"run_resolutions", "run_id"},
		{"run_incident_resolutions", "incident_id"},
		{"run_incidents", "id"},
		{"run_recovery_receipts", "id"},
	} {
		filter := "receipt_id=?"
		if table.name == "run_recovery_receipts" {
			filter = "id=?"
		}
		rows, err := tx.QueryContext(ctx, "SELECT "+table.key+",charged_bytes FROM "+table.name+" WHERE "+filter+" ORDER BY "+table.key+" LIMIT ?", id, budget-int(deleted))
		if err != nil {
			return 0, false, metadataReadError(err)
		}
		type member struct {
			id     string
			charge int64
		}
		var members []member
		for rows.Next() {
			var m member
			if err := rows.Scan(&m.id, &m.charge); err != nil {
				rows.Close()
				return 0, false, metadataReadError(err)
			}
			members = append(members, m)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return 0, false, metadataReadError(err)
		}
		for _, m := range members {
			if m.charge < 0 || m.charge > recoveryDispositionReserve {
				return 0, false, recoveryInconsistent("audit deletion accounting")
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+table.name+" WHERE "+table.key+"=? AND "+filter, m.id, id); err != nil {
				return 0, false, metadataFailure(ctx, "deleting recovery audit")
			}
			deleted++
			debit += m.charge
		}
		if deleted == int64(budget) {
			break
		}
	}
	result, err := tx.ExecContext(ctx, "UPDATE run_meta SET accounted_bytes=accounted_bytes-? WHERE singleton=1 AND accounted_bytes>=?", debit, debit)
	if err != nil {
		return 0, false, metadataFailure(ctx, "releasing recovery audit debit")
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return 0, false, recoveryInconsistent("audit deletion accounting")
	}
	if err := advanceRecoveryRevision(ctx, tx); err != nil {
		return 0, false, err
	}
	var pending bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM run_recovery_receipts WHERE id=?)", id).Scan(&pending); err != nil {
		return 0, false, metadataReadError(err)
	}
	return deleted, pending, nil
}
