package runstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	sqlite3 "github.com/mattn/go-sqlite3"
)

type IncidentCause string
type IncidentStage string
type incidentAttribution string

const (
	incidentPending      IncidentCause       = "pending_cleanup"
	incidentFinalization IncidentCause       = "finalization_unpersisted"
	incidentSupervisor   IncidentCause       = "supervisor_lost"
	incidentStartup      IncidentCause       = "startup_uncertain"
	incidentGeneration   IncidentCause       = "generation_mismatch"
	incidentProof        IncidentCause       = "invalid_proof"
	incidentManifest     IncidentCause       = "invalid_manifest"
	incidentStructure    IncidentCause       = "structural_inconsistency"
	incidentLegacy       IncidentCause       = "legacy_unattributed"
	stageStartup         IncidentStage       = "startup"
	stageHelperWait      IncidentStage       = "helper_wait"
	stageFinalization    IncidentStage       = "finalization"
	stageReconciliation  IncidentStage       = "reconciliation"
	stageLegacy          IncidentStage       = "legacy_inspection"
	attributionRecorded  incidentAttribution = "recorded"
	attributionInferred  incidentAttribution = "inferred"
	attributionUnknown   incidentAttribution = "unattributed"
)

type incident struct {
	Version                                                     int
	ID, Kind, Key, Generation, RunID, BatchID, Phase, ReceiptID string
	Slot                                                        *int
	Cause                                                       IncidentCause
	Stage                                                       IncidentStage
	Attribution                                                 incidentAttribution
	DetectedAt                                                  *int64
	Revision, ChargedBytes                                      int64
}

func validPhase(s string) bool {
	switch s {
	case "unknown", "admitted", "queued", "authorized", "compile", "run":
		return true
	}
	return false
}
func (c IncidentCause) rank() int {
	switch c {
	case incidentLegacy:
		return 1
	case incidentPending:
		return 2
	case incidentStartup, incidentSupervisor, incidentFinalization:
		return 3
	case incidentGeneration, incidentProof, incidentManifest, incidentStructure:
		return 4
	}
	return 0
}
func (i incident) valid() bool {
	if i.Cause.rank() == 0 || !validPhase(i.Phase) || (i.BatchID != "" && uuid.Validate(i.BatchID) != nil) {
		return false
	}
	switch i.Stage {
	case stageStartup, stageHelperWait, stageFinalization, stageReconciliation, stageLegacy:
	default:
		return false
	}
	switch i.Attribution {
	case attributionRecorded, attributionInferred, attributionUnknown:
	default:
		return false
	}
	switch i.Kind {
	case "store":
		return i.Slot == nil && i.Generation == "" && i.RunID == "" && i.BatchID == "" && i.Key == "store"
	case "owner":
		return i.Slot != nil && *i.Slot >= 0 && *i.Slot < OwnerSlots && uuid.Validate(i.Generation) == nil && i.RunID == "" && i.BatchID == "" && i.Key == fmt.Sprintf("owner:%d:%s", *i.Slot, i.Generation)
	case "run":
		return i.Slot != nil && *i.Slot >= 0 && *i.Slot < OwnerSlots && uuid.Validate(i.Generation) == nil && uuid.Validate(i.RunID) == nil && i.Key == "run:"+i.RunID+":"+i.Generation
	}
	return false
}
func storeIncident() incident {
	return incident{Kind: "store", Key: "store", Cause: incidentLegacy, Stage: stageLegacy, Attribution: attributionUnknown, Phase: "unknown"}
}
func ownerIncident(slot int, generation string, cause IncidentCause) incident {
	return incident{Kind: "owner", Key: fmt.Sprintf("owner:%d:%s", slot, generation), Slot: &slot, Generation: generation, Cause: cause, Stage: stageLegacy, Attribution: attributionUnknown, Phase: "unknown"}
}
func optionalString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func inconsistentIncident() error {
	return failure("recovery_inconsistent", "incident identity or evidence metadata is inconsistent; preserve history; operator confirmation cannot override it")
}

// scanIncident bounds every variable-sized field before database/sql allocates
// it. Callers share their transaction; this layer never opens another handle.
const incidentColumns = `CASE WHEN length(id)=36 THEN id ELSE NULL END,version,CASE WHEN length(subject_kind)<=8 THEN subject_kind ELSE NULL END,
 CASE WHEN length(subject_key)<=256 THEN subject_key ELSE NULL END,slot,
 CASE WHEN generation IS NULL THEN '' WHEN length(generation)=36 THEN generation ELSE NULL END,
 CASE WHEN run_id IS NULL THEN '' WHEN length(run_id)=36 THEN run_id ELSE NULL END,
 CASE WHEN batch_id IS NULL THEN '' WHEN length(batch_id)=36 THEN batch_id ELSE NULL END,
 CASE WHEN length(reason)<=32 THEN reason ELSE NULL END,CASE WHEN length(attribution)<=16 THEN attribution ELSE NULL END,detected_at,CASE WHEN length(stage)<=32 THEN stage ELSE NULL END,CASE WHEN length(last_phase)<=16 THEN last_phase ELSE NULL END,
 CASE WHEN receipt_id IS NULL THEN '' WHEN length(receipt_id)=36 THEN receipt_id ELSE NULL END,revision,charged_bytes`

type incidentScanner interface{ Scan(...any) error }

func scanIncident(row incidentScanner) (incident, error) {
	var i incident
	var slot, detected sql.NullInt64
	var generation, run, batch, receipt sql.NullString
	err := row.Scan(&i.ID, &i.Version, &i.Kind, &i.Key, &slot, &generation, &run, &batch, &i.Cause, &i.Attribution, &detected, &i.Stage, &i.Phase, &receipt, &i.Revision, &i.ChargedBytes)
	if err != nil {
		return i, err
	}
	if !generation.Valid || !run.Valid || !batch.Valid || !receipt.Valid {
		return incident{}, inconsistentIncident()
	}
	i.Generation, i.RunID, i.BatchID, i.ReceiptID = generation.String, run.String, batch.String, receipt.String
	if slot.Valid {
		v := int(slot.Int64)
		i.Slot = &v
	}
	if detected.Valid {
		i.DetectedAt = &detected.Int64
	}
	if i.Version != 1 || !i.valid() || uuid.Validate(i.ID) != nil || i.Revision < 1 || i.ChargedBytes < 0 || i.ChargedBytes > recoveryIncidentReserve || (i.ReceiptID != "" && uuid.Validate(i.ReceiptID) != nil) {
		return incident{}, inconsistentIncident()
	}
	return i, nil
}

// observeIncident only revises unresolved episodes. The first detection time and
// immutable subject survive repeat observations; resolved subjects get new UUIDs.
// The caller must commit markers and finishIncidentChanges in the same transaction.
func observeIncident(ctx context.Context, tx *sql.Tx, next incident) (bool, error) {
	if !next.valid() {
		return false, inconsistentIncident()
	}
	old, err := scanIncident(tx.QueryRowContext(ctx, "SELECT "+incidentColumns+" FROM run_incidents WHERE subject_key=? AND receipt_id IS NULL", next.Key))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, metadataReadError(err)
	}
	if err == nil {
		// Less precise fallback evidence cannot erase a known cause or phase.
		if next.Cause.rank() < old.Cause.rank() {
			next.Cause, next.Stage, next.Attribution = old.Cause, old.Stage, old.Attribution
		}
		if old.Attribution == attributionRecorded {
			next.Attribution = old.Attribution
		}
		if phaseOrder(next.Phase) <= phaseOrder(old.Phase) {
			next.Phase = old.Phase
		}
		if next.BatchID == "" {
			next.BatchID = old.BatchID
		}
		if next.Cause == old.Cause && next.Stage == old.Stage && next.Attribution == old.Attribution && next.Phase == old.Phase && next.BatchID == old.BatchID {
			return false, nil
		}
		if old.Revision == math.MaxInt64 {
			return false, failure("recovery_revision_exhausted", "incident revision exhausted; preserve history and use a compatible recovery tool")
		}
		_, err = tx.ExecContext(ctx, `UPDATE run_incidents SET reason=?,stage=?,attribution=?,last_phase=?,batch_id=?,revision=revision+1 WHERE id=? AND receipt_id IS NULL`, next.Cause, next.Stage, next.Attribution, next.Phase, optionalString(next.BatchID), old.ID)
		return err == nil, metadataFailureIf(ctx, err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE run_meta SET accounted_bytes=accounted_bytes+? WHERE singleton=1 AND accounted_bytes<=(SELECT max_retained_bytes FROM run_policy WHERE singleton=1)-?`, recoveryIncidentReserve, recoveryIncidentReserve)
	if err != nil {
		return false, metadataFailureIf(ctx, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, storageError()
	}
	if n != 1 {
		return false, failure("recovery_capacity", "incident metadata exceeds retained capacity; increase runs.max_retained_bytes with offline runs upgrade; no history was evicted")
	}
	if next.DetectedAt == nil && next.Stage != stageLegacy {
		now := time.Now().UTC().UnixNano()
		next.DetectedAt = &now
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO run_incidents(id,version,subject_kind,subject_key,slot,generation,run_id,batch_id,reason,attribution,detected_at,stage,last_phase,revision,charged_bytes) VALUES(?,1,?,?,?,?,?,?,?,?,?,?,?,1,?)`, uuid.NewString(), next.Kind, next.Key, next.Slot, optionalString(next.Generation), optionalString(next.RunID), optionalString(next.BatchID), next.Cause, next.Attribution, next.DetectedAt, next.Stage, next.Phase, recoveryIncidentReserve)
	return err == nil, metadataFailureIf(ctx, err)
}
func metadataFailureIf(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	return errors.Join(failure("incident_publication_failed", "incident attribution could not be persisted; admission remains refused; inspect retained evidence and repair storage before restarting execution"), ctx.Err())
}

// advanceRecoveryRevision is shared by incident/marker publication and future
// disposition, batch closure and audit-GC transactions. Never wrap the epoch.
func advanceRecoveryRevision(ctx context.Context, tx *sql.Tx) error {
	r, err := tx.ExecContext(ctx, `UPDATE run_meta SET recovery_revision=recovery_revision+1 WHERE singleton=1 AND typeof(recovery_revision)='integer' AND recovery_revision>=0 AND recovery_revision<?`, int64(math.MaxInt64))
	if err != nil {
		return metadataFailureIf(ctx, err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return storageError()
	}
	if n != 1 {
		return failure("recovery_revision_exhausted", "recovery revision exhausted; no recovery metadata was changed")
	}
	return nil
}
func finishIncidentChanges(ctx context.Context, tx *sql.Tx, changed bool) error {
	if !changed {
		return nil
	}
	if err := advanceRecoveryRevision(ctx, tx); err != nil {
		return err
	}
	used, err := refreshIndexDebit(ctx, tx)
	if err != nil {
		return err
	}
	var limit int64
	if err := tx.QueryRowContext(ctx, "SELECT max_retained_bytes FROM run_policy WHERE singleton=1").Scan(&limit); err != nil {
		return storageError()
	}
	if used > limit {
		return failure("recovery_capacity", "incident metadata exceeds retained capacity; increase runs.max_retained_bytes with offline runs upgrade; no history was evicted")
	}
	return nil
}
func quarantineMarkers(ctx context.Context, tx *sql.Tx, slot *int, generation string) (bool, error) {
	r, err := tx.ExecContext(ctx, "UPDATE run_meta SET admission_blocked=1 WHERE singleton=1 AND admission_blocked=0")
	if err != nil {
		return false, storageError()
	}
	n, err := r.RowsAffected()
	if err != nil {
		return false, storageError()
	}
	changed := n != 0
	if slot != nil {
		r, err = tx.ExecContext(ctx, "UPDATE run_owners SET cleanup_unverified=1 WHERE slot=? AND generation=? AND cleanup_unverified=0", *slot, generation)
		if err != nil {
			return false, storageError()
		}
		n, err = r.RowsAffected()
		if err != nil {
			return false, storageError()
		}
		changed = changed || n != 0
	}
	return changed, nil
}

func phaseOrder(phase string) int {
	switch phase {
	case "admitted", "queued":
		return 1
	case "authorized":
		return 2
	case "compile":
		return 3
	case "run":
		return 4
	}
	return 0
}

// Keep transient SQLite/context errors distinct from unsupported stored facts,
// while never exposing backend text through inspection or admission.
func metadataReadError(err error) error {
	var typed *Error
	if errors.As(err, &typed) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return errors.Join(storageError(), err)
	}
	var driver sqlite3.Error
	if errors.As(err, &driver) {
		return storageError()
	}
	return inconsistentIncident()
}
