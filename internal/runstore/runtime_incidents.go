package runstore

import (
	"context"
	"database/sql"

	"github.com/serpro69/capy/internal/sqliteutil"
)

// Runtime causes and detection stages are independent of the last phase the
// helper durably recorded. No backend error or command text enters attribution.
const (
	IncidentPendingCleanup          = incidentPending
	IncidentFinalizationUnpersisted = incidentFinalization
	IncidentSupervisorLost          = incidentSupervisor
	IncidentStartupUncertain        = incidentStartup
	StageStartup                    = stageStartup
	StageHelperWait                 = stageHelperWait
	StageFinalization               = stageFinalization
)

// RuntimeIncident carries only bounded admitted identity and observed cause.
// Callers may retain one as volatile context; its presence is not durable proof.
type RuntimeIncident struct {
	RunID, Generation, BatchID string
	Slot                       int
	Cause                      IncidentCause
	Stage                      IncidentStage
}

// QuarantineRun commits attribution and the global safety marker together when
// possible. Audit failures roll back only attribution, so quota/index failures
// cannot prevent a writable marker from blocking other live owners. Any error
// means attribution persistence was not confirmed; the caller must keep its latch.
func (s *Store) QuarantineRun(ctx context.Context, subject RuntimeIncident) error {
	tx, err := sqliteutil.BeginImmediateContext(ctx, s.db, "runs")
	if err != nil {
		return storageError()
	}
	defer tx.Rollback()
	markerChanged, err := quarantineMarkers(ctx, tx, nil, "")
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "SAVEPOINT runtime_incident"); err != nil {
		return storageError()
	}
	changed, publicationErr := observeRuntimeIncident(ctx, tx, subject)
	if publicationErr == nil {
		publicationErr = finishIncidentChanges(ctx, tx, markerChanged || changed)
	}
	if publicationErr != nil {
		if _, err := tx.ExecContext(ctx, "ROLLBACK TO runtime_incident"); err != nil {
			return metadataFailureIf(ctx, err)
		}
		if markerChanged {
			if err := advanceRecoveryRevision(ctx, tx); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, "RELEASE runtime_incident"); err != nil {
		return metadataFailureIf(ctx, err)
	}
	if err := tx.Commit(); err != nil {
		return metadataFailureIf(ctx, err)
	}
	return publicationErr
}

func observeRuntimeIncident(ctx context.Context, tx *sql.Tx, subject RuntimeIncident) (bool, error) {
	i := incident{Kind: "run", Key: "run:" + subject.RunID + ":" + subject.Generation,
		RunID: subject.RunID, Generation: subject.Generation, BatchID: subject.BatchID,
		Slot: &subject.Slot, Cause: subject.Cause, Stage: subject.Stage,
		Attribution: attributionRecorded, Phase: "unknown"}
	validPair := subject.Cause == incidentStartup && subject.Stage == stageStartup ||
		subject.Cause == incidentSupervisor && subject.Stage == stageHelperWait ||
		(subject.Cause == incidentFinalization || subject.Cause == incidentPending) && subject.Stage == stageFinalization
	if !i.valid() || !validPair {
		return false, inconsistentIncident()
	}
	var c recoveryCandidate
	c.id = subject.RunID
	var ownerMatches bool
	err := tx.QueryRowContext(ctx, `SELECT slot,CASE WHEN length(generation)=36 THEN generation ELSE NULL END,
 CASE WHEN length(last_phase)<=16 THEN last_phase ELSE NULL END,CASE WHEN length(result)<=8192 THEN result ELSE NULL END,
 CASE WHEN length(cleanup_scope)<=64 THEN cleanup_scope ELSE NULL END,CASE WHEN length(cleanup)<=16 THEN cleanup ELSE NULL END,terminal,
 EXISTS(SELECT 1 FROM run_owners o WHERE o.slot=runs.slot AND o.generation=runs.generation)
 FROM runs WHERE id=? AND evicting=0`, subject.RunID).Scan(&c.slot, &c.generation, &c.phase, &c.result, &c.cleanup.Scope, &c.cleanup.State, &c.terminal, &ownerMatches)
	if err != nil {
		return false, metadataReadError(err)
	}
	var result Result
	if !ownerMatches || c.slot != subject.Slot || c.generation != subject.Generation || !validPhase(c.phase) ||
		decodeRunResult(c.result, c.cleanup, &result) != nil || validateTerminal(c.cleanup, result, c.terminal) != nil ||
		result.RunID == nil || *result.RunID != subject.RunID || result.OwnerID != subject.Generation || result.BatchID != subject.BatchID {
		return false, inconsistentIncident()
	}
	cause, err := reconciliationManifestCause(ctx, tx, c, result)
	if err != nil {
		return false, err
	}
	if cause != "" {
		return false, inconsistentIncident()
	}
	i.Phase = c.phase
	return observeIncident(ctx, tx, i)
}
