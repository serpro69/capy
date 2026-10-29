package runstore

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const recoveryBatchReserve = 4096

type batchClosure struct {
	ReceiptID                         string
	ClosedAt, ExpiresAt, ChargedBytes int64
	Receipt                           recoveryReceipt
}

// The admission formula is unchanged since schema 8. Preserve any conservative
// historical over-reservation. A first disposition adds one independent reserve
// to the manifest debit; closure transfers it without spending quota twice.
func batchRecoveryDebit(ctx context.Context, q runQuerier, id string) (base, charged int64, err error) {
	err = q.QueryRowContext(ctx, `SELECT 4096+length(CAST(queries AS BLOB))+
 (SELECT COALESCE(sum(?+length(CAST(label AS BLOB))+length(CAST(command AS BLOB))),0) FROM run_batch_children WHERE batch_id=b.id),accounted_bytes
 FROM run_batches b WHERE id=?`, batchChildReserve, id).Scan(&base, &charged)
	if err != nil {
		return 0, 0, metadataReadError(err)
	}
	if base < 4096 || charged < 0 {
		return 0, 0, recoveryInconsistent("batch recovery accounting")
	}
	return base, charged, nil
}

func reserveBatchRecovery(ctx context.Context, tx *sql.Tx, id string) error {
	if id == "" {
		return nil
	}
	var funded bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM run_resolutions WHERE batch_id=?)", id).Scan(&funded); err != nil {
		return metadataReadError(err)
	}
	if funded {
		return nil
	}
	base, charged, err := batchRecoveryDebit(ctx, tx, id)
	if err != nil {
		return err
	}
	if charged < base {
		return recoveryInconsistent("batch recovery reservation")
	}
	_, err = tx.ExecContext(ctx, "UPDATE run_batches SET accounted_bytes=accounted_bytes+? WHERE id=?", recoveryBatchReserve, id)
	if err != nil {
		return metadataFailure(ctx, "reserving batch recovery metadata")
	}
	return nil
}

func readBatchClosure(ctx context.Context, q runQuerier, id string) (*batchClosure, error) {
	c := &batchClosure{}
	var ttl int
	var store string
	var bound bool
	err := q.QueryRowContext(ctx, `SELECT d.receipt_id,d.closed_at,d.retention_expires_at,d.charged_bytes,b.ttl_hours,m.store_id,
 EXISTS(SELECT 1 FROM run_resolutions r WHERE r.batch_id=b.id AND r.receipt_id=d.receipt_id AND r.generation=b.generation AND r.slot=b.slot)
 FROM run_batch_resolutions d JOIN run_batches b ON b.id=d.batch_id JOIN run_meta m ON m.singleton=1 WHERE d.batch_id=?`, id).Scan(&c.ReceiptID, &c.ClosedAt, &c.ExpiresAt, &c.ChargedBytes, &ttl, &store, &bound)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, metadataReadError(err)
	}
	if !bound || c.ChargedBytes != recoveryBatchReserve || ttl < 1 || ttl > 2562047 || c.ExpiresAt != time.Unix(0, c.ClosedAt).Add(time.Duration(ttl)*time.Hour).UnixMilli()+1 {
		return nil, recoveryInconsistent("batch closure binding")
	}
	c.Receipt, err = readRecoveryReceipt(ctx, q, c.ReceiptID, "")
	if err != nil {
		return nil, err
	}
	if c.Receipt.StoreID != store || c.Receipt.Pruning {
		return nil, recoveryInconsistent("batch closure receipt")
	}
	return c, nil
}

// Return whether this transaction created administrative closure. Its caller
// owns the transaction's single recovery-epoch increment and final quota check.
func closeBatchRecovery(ctx context.Context, tx *sql.Tx, id string, now time.Time) (bool, error) {
	var disposed, pending, closed bool
	err := tx.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM run_resolutions WHERE batch_id=?),
 EXISTS(SELECT 1 FROM run_batch_children c WHERE c.batch_id=? AND c.terminal=0 AND NOT EXISTS(SELECT 1 FROM run_resolutions d WHERE d.run_id=c.run_id)),
 EXISTS(SELECT 1 FROM run_batch_resolutions WHERE batch_id=?)`, id, id, id).Scan(&disposed, &pending, &closed)
	if err != nil {
		return false, metadataReadError(err)
	}
	if closed && (!disposed || pending) {
		return false, recoveryInconsistent("premature batch closure")
	}
	if !disposed || pending {
		return false, nil
	}
	children, _, err := batchChildren(ctx, tx, id)
	if err != nil {
		return false, err
	}
	var provenance *runResolution
	for _, child := range children {
		if !child.closed {
			return false, recoveryInconsistent("batch operational closure")
		}
		if d := child.disposition; d != nil && (provenance == nil || d.ResolvedAt > provenance.ResolvedAt || d.ResolvedAt == provenance.ResolvedAt && d.RunID > provenance.RunID) {
			provenance = d
		}
	}
	if provenance == nil {
		return false, recoveryInconsistent("batch resolution provenance")
	}
	existing, err := readBatchClosure(ctx, tx, id)
	if err != nil {
		return false, err
	}
	if existing != nil {
		return false, nil
	}
	base, charged, err := batchRecoveryDebit(ctx, tx, id)
	if err != nil {
		return false, err
	}
	if charged < base+recoveryBatchReserve {
		return false, recoveryInconsistent("unfunded batch closure")
	}
	var ttl int
	var complete sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT ttl_hours,completed_at FROM run_batches WHERE id=?", id).Scan(&ttl, &complete); err != nil {
		return false, metadataReadError(err)
	}
	if complete.Valid || ttl < 1 || ttl > 2562047 {
		return false, recoveryInconsistent("batch closure outcome or ttl")
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO run_batch_resolutions(batch_id,receipt_id,closed_at,retention_expires_at,charged_bytes) VALUES(?,?,?,?,?)", id, provenance.ReceiptID, now.UnixNano(), now.Add(time.Duration(ttl)*time.Hour).UnixMilli()+1, recoveryBatchReserve)
	if err != nil {
		return false, metadataFailure(ctx, "recording batch recovery closure")
	}
	_, err = tx.ExecContext(ctx, "UPDATE run_batches SET accounted_bytes=accounted_bytes-? WHERE id=?", recoveryBatchReserve, id)
	if err != nil {
		return false, metadataFailure(ctx, "transferring batch recovery reservation")
	}
	return true, nil
}

func closeAppliedBatches(ctx context.Context, tx *sql.Tx, receipt string, now time.Time) error {
	after := ""
	for {
		rows, err := tx.QueryContext(ctx, "SELECT DISTINCT batch_id FROM run_resolutions WHERE receipt_id=? AND batch_id>? ORDER BY batch_id LIMIT 64", receipt, after)
		if err != nil {
			return metadataReadError(err)
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return metadataReadError(err)
			}
			ids = append(ids, id)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return metadataReadError(err)
		}
		for _, id := range ids {
			if _, err := closeBatchRecovery(ctx, tx, id, now); err != nil {
				return err
			}
			after = id
		}
		if len(ids) < 64 {
			return nil
		}
	}
}

func projectBatchRecovery(ctx context.Context, tx *sql.Tx, b *BatchResult, children []batchChild) error {
	allClosed, anyHistory, blocked := true, b.Error != nil, false
	var historical *Recovery
	for _, child := range children {
		allClosed = allClosed && child.closed
		if child.disposition != nil {
			b.RecoveredCount++
		}
		if r := child.result.Recovery; r != nil {
			anyHistory = true
			blocked = blocked || r.BlocksAdmission
			historical = r
		}
	}
	closure, err := readBatchClosure(ctx, tx, b.BatchID)
	if err != nil {
		return err
	}
	if (closure != nil) != (b.RecoveredCount > 0 && allClosed) {
		return recoveryInconsistent("batch closure snapshot")
	}
	if b.RecoveredCount > 0 {
		base, charged, err := batchRecoveryDebit(ctx, tx, b.BatchID)
		if err != nil {
			return err
		}
		if closure == nil {
			base += recoveryBatchReserve
		}
		if charged < base {
			return recoveryInconsistent("batch recovery accounting")
		}
	}
	if !anyHistory && b.RecoveredCount == 0 {
		return nil
	}
	blocked = blocked || b.Error != nil && !allClosed
	b.Recovery = &Recovery{State: "unresolved", OperationallyClosed: allClosed, CaptureClosed: allClosed, BlocksAdmission: blocked}
	if b.RecoveredCount > 0 {
		b.Recovery.State = "partially_resolved"
	}
	if allClosed && !blocked {
		b.Recovery.State = "operator_resolved"
	}
	if closure != nil {
		resolved, closed, expiry := time.Unix(0, closure.Receipt.AppliedAt).UTC(), time.Unix(0, closure.ClosedAt).UTC(), time.UnixMilli(closure.ExpiresAt).UTC()
		b.Recovery.ReceiptID, b.Recovery.Basis = closure.ReceiptID, closure.Receipt.Basis
		b.Recovery.ResolvedAt, b.Recovery.ClosedAt, b.Recovery.RetentionExpiresAt = &resolved, &closed, &expiry
	} else if b.RecoveredCount == 0 && historical != nil && historical.ReceiptID != "" {
		b.Recovery.ReceiptID, b.Recovery.ResolvedAt, b.Recovery.Basis = historical.ReceiptID, historical.ResolvedAt, historical.Basis
	}
	if blocked {
		b.Error = &Error{Code: "cleanup_unverified", Message: "last recorded child states; unresolved cleanup concerns remain; inspect this owner with capy runs inspect"}
	} else if allClosed {
		b.Error = nil
	}
	return nil
}
