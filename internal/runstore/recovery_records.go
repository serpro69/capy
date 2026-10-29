package runstore

import (
	"context"
	"database/sql"
	"errors"
	"unicode/utf8"

	"github.com/google/uuid"
)

// recoveryReceipt is the bounded private record boundary shared by inspection
// and future application. Schema upgrade never creates audit records.
type recoveryReceipt struct {
	ID, PlanHash, StoreID, Method, Basis, Explanation               string
	Version                                                         int
	AppliedAt, UID, IncidentCount, RunCount, OwnerCount, StoreCount int64
	ChargedBytes, AuditExpiresAt                                    int64
	Pruning                                                         bool
}

// readRecoveryReceipt reads one identity or plan-hash key through the caller's
// connection/transaction. It never opens storage or scans/reconstructs a plan.
func readRecoveryReceipt(ctx context.Context, q runQuerier, id, planHash string) (recoveryReceipt, error) {
	var receipt recoveryReceipt
	column, value := "id", id
	if id == "" {
		column, value = "plan_hash", planHash
	}
	if id != "" && planHash != "" || id == "" && len(planHash) != 64 || id != "" && uuid.Validate(id) != nil {
		return receipt, failure("invalid_argument", "choose an exact recovery receipt UUID or plan hash")
	}
	// The identifier is selected above from two internal constants; the value is
	// always bound. CASE bounds reads even if stored constraints were bypassed.
	err := q.QueryRowContext(ctx, `SELECT CASE WHEN length(id)=36 THEN id ELSE NULL END,CASE WHEN length(plan_hash)=64 THEN plan_hash ELSE NULL END,CASE WHEN length(store_id)=36 THEN store_id ELSE NULL END,version,applied_at,CASE WHEN length(method)<=8 THEN method ELSE NULL END,CASE WHEN length(basis)<=16 THEN basis ELSE NULL END,uid,
 CASE WHEN length(CAST(explanation AS BLOB)) BETWEEN 1 AND 1024 THEN explanation ELSE NULL END,
 incident_count,run_count,owner_count,store_count,charged_bytes,audit_expires_at,pruning
 FROM run_recovery_receipts WHERE `+column+`=?`, value).Scan(&receipt.ID, &receipt.PlanHash, &receipt.StoreID, &receipt.Version, &receipt.AppliedAt, &receipt.Method, &receipt.Basis, &receipt.UID, &receipt.Explanation, &receipt.IncidentCount, &receipt.RunCount, &receipt.OwnerCount, &receipt.StoreCount, &receipt.ChargedBytes, &receipt.AuditExpiresAt, &receipt.Pruning)
	if errors.Is(err, sql.ErrNoRows) {
		return receipt, receiptUnavailable()
	}
	if err != nil {
		return recoveryReceipt{}, storageError()
	}
	valid := receipt.Version == 1 && uuid.Validate(receipt.ID) == nil && uuid.Validate(receipt.StoreID) == nil && len(receipt.PlanHash) == 64 && receipt.Method == "operator" && (receipt.Basis == "groups_stopped" || receipt.Basis == "host_restarted") && utf8.ValidString(receipt.Explanation) && receipt.UID >= 0 && receipt.ChargedBytes >= 0 && receipt.ChargedBytes <= recoveryReceiptReserve
	if !valid {
		return recoveryReceipt{}, failure("recovery_inconsistent", "retained recovery receipt metadata is inconsistent")
	}
	return receipt, nil
}
