package runstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/sanitize"
	"github.com/serpro69/capy/internal/sqliteutil"
)

func (s *Store) Admit(ctx context.Context, owner *Owner, scope executor.CleanupScope, program Program) (Admitted, error) {
	// The attached store is authoritative. Callers cannot select a different
	// TTL or capture ceiling through per-program metadata.
	program.PolicyRevision = s.policy.Revision
	program.TTLHours = s.policy.TTLHours
	program.MaxRunBytes = s.policy.MaxRunBytes
	ctx, cancel := context.WithTimeout(ctx, recoveryTimeout)
	defer cancel()
	registry, err := s.recoveryLease(ctx)
	if err != nil {
		return Admitted{}, err
	}
	defer registry.Close()
	if _, err := s.reconcile(ctx); err != nil {
		return Admitted{}, err
	}
	if err := s.checkQuarantine(ctx); err != nil {
		return Admitted{}, err
	}
	row, encodedProgram, encodedResult, err := s.prepareRun(owner, scope, program)
	if err != nil {
		return Admitted{}, err
	}
	if err := s.prepareCapacity(ctx, int64(len(encodedProgram))+runReserve); err != nil {
		return Admitted{}, err
	}
	tx, err := sqliteutil.BeginImmediateContext(ctx, s.db, "runs")
	if err != nil {
		return Admitted{}, storageError()
	}
	defer tx.Rollback()
	var blocked bool
	if err := tx.QueryRowContext(ctx, "SELECT admission_blocked FROM run_meta WHERE singleton=1").Scan(&blocked); err != nil {
		return Admitted{}, storageError()
	}
	if blocked {
		return Admitted{}, failure("cleanup_unverified", "run store admission is blocked by unverified process cleanup")
	}
	var generation string
	if err := tx.QueryRowContext(ctx, "SELECT generation FROM run_owners WHERE slot=?", owner.Slot).Scan(&generation); err != nil || generation != owner.ID {
		return Admitted{}, storageError()
	}
	if err := insertRun(ctx, tx, row, encodedProgram, encodedResult); err != nil {
		return Admitted{}, err
	}
	// The admission commit is the write probe. No subprocess is authorized until
	// it succeeds and the helper authenticates/reloads this exact admitted row.
	if err := charge(ctx, tx, row.ID, int64(len(encodedProgram))+runReserve, s.policy.MaxRetainedBytes, true); err != nil {
		return Admitted{}, err
	}
	if err := tx.Commit(); err != nil {
		return Admitted{}, storageError()
	}
	return row, nil
}

type runQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) Load(ctx context.Context, id string) (Admitted, error) {
	return loadRun(ctx, s.db, id)
}

func loadRun(ctx context.Context, query runQuerier, id string) (Admitted, error) {
	var row Admitted
	row.ID = id
	var program, result []byte
	var manifest []byte
	var manifestTerminal sql.NullBool
	var terminal, unverified bool
	err := query.QueryRowContext(ctx, `SELECT generation,slot,program,result,cleanup_scope,cleanup,terminal,
EXISTS(SELECT 1 FROM run_owners o WHERE o.slot=runs.slot AND o.generation=runs.generation AND o.cleanup_unverified=1),
(SELECT c.result FROM run_batch_children c WHERE c.run_id=runs.id),
(SELECT c.terminal FROM run_batch_children c WHERE c.run_id=runs.id)
FROM runs WHERE id=? AND evicting=0`, id).Scan(&row.Generation, &row.Slot, &program, &result, &row.Cleanup.Scope, &row.Cleanup.State, &terminal, &unverified, &manifest, &manifestTerminal)
	if errors.Is(err, sql.ErrNoRows) {
		return row, failure("run_unavailable", "run is unavailable in this owner's store")
	}
	if err != nil {
		return row, storageError()
	}
	if json.Unmarshal(program, &row.Program) != nil || decodeRunResult(result, row.Cleanup, &row.Result) != nil {
		return row, storageError()
	}
	row.Result.Cleanup = row.Cleanup
	if err := validateTerminal(row.Cleanup, row.Result, terminal); err != nil {
		return row, err
	}
	if manifestTerminal.Valid {
		if err := validateManifestCopy(manifest, row.Cleanup, terminal, manifestTerminal.Bool); err != nil {
			return row, err
		}
	}
	// A completed result without terminal cleanup is already evidence of
	// unverified retirement, even while its owner lease prevents reconciliation.
	unverified = unverified || (!terminal && row.Result.CompletedAt != nil)
	if unverified && !terminal {
		// Preserve the last recorded state as evidence, not a fresh assertion of
		// liveness. Unknown cleanup never expires away or becomes safe reuse.
		markUnverified(&row.Result)
	} else if row.Result.ExpiresAt != nil && !time.Now().Before(*row.Result.ExpiresAt) {
		return Admitted{}, failure("run_unavailable", "run is unavailable in this owner's store")
	}
	return row, nil
}

func markUnverified(result *Result) {
	result.Error = &Error{Code: "cleanup_unverified", Message: "last recorded state; owner cleanup is unverified and execution may still exist; stop old execution groups before explicit operator reset"}
	result.ExpiresAt = nil
	if result.Capture.State != "not_started" {
		result.Capture.State = "incomplete"
		reason := "cleanup_unverified"
		result.Capture.Reason = &reason
	}
}

func (s *Store) Pending(ctx context.Context, id, generation string, scope executor.CleanupScope) error {
	return s.update(ctx, id, func(_ *sql.Tx, row *Admitted) error {
		if row.Generation != generation || row.Cleanup.Scope != scope || row.Cleanup.State != executor.CleanupNotSpawned {
			return failure("cleanup_unverified", "run start was already authorized or owner generation/scope changed")
		}
		row.Cleanup.State = executor.CleanupPending
		return nil
	})
}

func (s *Store) Started(ctx context.Context, id string) error {
	return s.update(ctx, id, func(tx *sql.Tx, row *Admitted) error {
		if row.Cleanup.State != executor.CleanupPending {
			return storageError()
		}
		if row.Result.StartedAt == nil {
			now := time.Now().UTC()
			row.Result.StartedAt = &now
		}
		row.Result.State = "running"
		if row.Result.Capture.State != "incomplete" {
			row.Result.Capture.State = "recording"
		}
		return nil
	})
}

func (s *Store) Append(ctx context.Context, id string, event executor.OutputEvent) error {
	return s.AppendBatch(ctx, id, []executor.OutputEvent{event})
}

// AppendBatch commits a bounded contiguous prefix atomically. A failed batch
// cannot leave a hole followed by later output.
func (s *Store) AppendBatch(ctx context.Context, id string, events []executor.OutputEvent) error {
	if len(events) == 0 || len(events) > 16 {
		return storageError()
	}
	for _, event := range events {
		if len(event.Data) == 0 || len(event.Data) > executor.EventBlockBytes || event.Sequence == 0 {
			return storageError()
		}
	}
	ctx, cancel := context.WithTimeout(ctx, WriteTimeout)
	defer cancel()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := s.appendBatch(ctx, id, events)
		if !errors.Is(err, retryCapture) {
			return err
		}
	}
}

func (s *Store) appendBatch(ctx context.Context, id string, events []executor.OutputEvent) error {
	return s.update(ctx, id, func(tx *sql.Tx, row *Admitted) error {
		var last uint64
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq),0) FROM run_events WHERE run_id=?", id).Scan(&last); err != nil {
			return storageError()
		}
		for _, event := range events {
			if event.Sequence != last+1 {
				return storageError()
			}
			if row.Cleanup.State != executor.CleanupPending || row.Result.State != "running" {
				return storageError()
			}
			if int64(len(event.Data)) > row.Program.MaxRunBytes-row.Result.Capture.CapturedBytes {
				return failure("capture_limit", "run capture limit reached")
			}
			var outBytes, errBytes, outLines, errLines int64
			if err := tx.QueryRowContext(ctx, "SELECT stdout_bytes,stderr_bytes,stdout_lines,stderr_lines FROM runs WHERE id=?", id).Scan(&outBytes, &errBytes, &outLines, &errLines); err != nil {
				return storageError()
			}
			offset, line := outBytes, outLines
			switch event.Stream {
			case "stdout":
				outBytes += int64(len(event.Data))
				outLines += int64(bytes.Count(event.Data, []byte{'\n'}))
			case "stderr":
				offset, line = errBytes, errLines
				errBytes += int64(len(event.Data))
				errLines += int64(bytes.Count(event.Data, []byte{'\n'}))
			default:
				return storageError()
			}
			_, err := tx.ExecContext(ctx, "INSERT INTO run_events(run_id,seq,phase,stream,data,byte_offset,start_line) VALUES(?,?,?,?,?,?,?)", id, event.Sequence, event.Phase, event.Stream, event.Data, offset, line)
			if err != nil {
				return storageError()
			}
			if _, err := tx.ExecContext(ctx, "UPDATE runs SET stdout_bytes=?,stderr_bytes=?,stdout_lines=?,stderr_lines=? WHERE id=?", outBytes, errBytes, outLines, errLines, id); err != nil {
				return storageError()
			}
			if err := s.project(ctx, tx, row, event.Stream, event.Data, false); err != nil {
				return err
			}
			row.Result.Capture.CapturedBytes += int64(len(event.Data))
			row.chargeBytes += int64(len(event.Data)) + eventOverhead
			row.Result.Capture.ObservedBytes = row.Result.Capture.CapturedBytes
			cursor := s.cursor(id, "merged", event.Sequence+1, 0)
			row.Result.Capture.LastCommittedCursor = &cursor
			last = event.Sequence
		}
		return s.projectionState(ctx, tx, row)
	})
}

func (s *Store) Finish(ctx context.Context, id string, c Completion) error {
	return s.update(ctx, id, func(tx *sql.Tx, row *Admitted) error {
		if err := validateCleanupTransition(row.Cleanup, c.Cleanup); err != nil {
			return err
		}
		if c.Started && c.Cleanup.State == executor.CleanupNotSpawned {
			return failure("cleanup_unverified", "started run cannot finish as not spawned")
		}
		for _, stream := range []string{"stdout", "stderr"} {
			if err := s.project(ctx, tx, row, stream, nil, true); err != nil {
				return err
			}
		}
		if err := s.projectionState(ctx, tx, row); err != nil {
			return err
		}
		// EOF projection is paid by the admission reservation. The streaming
		// recognizer can emit only two markers over at most 128 anchors here.
		if row.chargeBytes > runReserve/2 {
			return storageError()
		}
		row.chargeBytes = 0
		result := &row.Result
		now := time.Now().UTC()
		result.CompletedAt = &now
		expires := now.Add(time.Duration(row.Program.TTLHours) * time.Hour)
		result.ExpiresAt = &expires
		ApplyOutcome(result, c)

		result.Capture.ObservedBytes = c.ObservedBytes
		result.Capture.TerminalPersisted = true
		switch {
		case !c.Started:
			result.Capture.State = "not_started"
		case c.CaptureLost || !CleanupAccepted(c.Cleanup):
			result.Capture.State = "incomplete"
			reason := c.CaptureReason
			if reason == "" {
				reason = "capture_failed"
			}
			result.Capture.Reason = &reason
		default:
			result.Capture.State = "complete"
		}
		row.Cleanup = c.Cleanup
		return nil
	})
}

func (s *Store) update(ctx context.Context, id string, fn func(*sql.Tx, *Admitted) error) error {
	tx, err := sqliteutil.BeginImmediateContext(ctx, s.db, "runs")
	if err != nil {
		return storageError()
	}
	defer tx.Rollback()
	row := Admitted{ID: id}
	var program, result []byte
	var terminal bool
	if err := tx.QueryRowContext(ctx, "SELECT program,result,generation,cleanup_scope,cleanup,terminal FROM runs WHERE id=?", id).Scan(&program, &result, &row.Generation, &row.Cleanup.Scope, &row.Cleanup.State, &terminal); err != nil {
		return storageError()
	}
	if json.Unmarshal(program, &row.Program) != nil || decodeRunResult(result, row.Cleanup, &row.Result) != nil {
		return storageError()
	}
	if err := validateTerminal(row.Cleanup, row.Result, terminal); err != nil {
		return err
	}
	if err := validateRunManifest(ctx, tx, id, row.Cleanup, terminal); err != nil {
		return err
	}
	before := row.Cleanup
	if row.Result.CompletedAt != nil {
		return failure("run_unavailable", "run is already terminal")
	}
	if _, err := tx.ExecContext(ctx, "SAVEPOINT capture_payload"); err != nil {
		return storageError()
	}
	if err := fn(tx, &row); err != nil {
		return err
	}
	if err := validateCleanupTransition(before, row.Cleanup); err != nil {
		return err
	}
	row.Result.Cleanup = row.Cleanup
	encoded, err := json.Marshal(row.Result)
	if err != nil || len(encoded) > 8192 {
		return storageError()
	}
	if row.chargeBytes > 0 {
		used, err := refreshIndexDebit(ctx, tx)
		if err != nil {
			return err
		}
		if row.chargeBytes > s.policy.MaxRetainedBytes {
			return quotaError()
		}
		if used > s.policy.MaxRetainedBytes-row.chargeBytes {
			// Compute the exact projection debit first, then discard tentative
			// payload before committing bounded eviction progress. If this call
			// runs out of time, future maintenance can resume the tombstone even
			// though the failed append never increased retained usage above quota.
			if _, err := tx.ExecContext(ctx, "ROLLBACK TO capture_payload"); err != nil {
				return storageError()
			}
			before, err := refreshIndexDebit(ctx, tx)
			if err != nil {
				return err
			}
			// Conservative accounting also charges pages allocated by capture
			// after the first index exists. Rollback removes that debit, but
			// eviction must still reserve it or the retry makes no progress.
			pageGrowth := max(0, used-before)
			if pageGrowth > s.policy.MaxRetainedBytes-row.chargeBytes {
				return quotaError()
			}
			report, err := retain(ctx, tx, s.policy.MaxRetainedBytes, row.chargeBytes+pageGrowth, false)
			if err != nil {
				return err
			}
			if err := tx.Commit(); err != nil {
				return storageError()
			}
			if report.More && report.Expired+report.Evicted == 0 && report.DeletedRows == 0 {
				return quotaError()
			}
			return retryCapture
		}
		if err := charge(ctx, tx, id, row.chargeBytes, s.policy.MaxRetainedBytes, false); err != nil {
			return err
		}
	}
	terminal = row.Result.CompletedAt != nil && CleanupAccepted(row.Cleanup)
	var completed, expires any
	if terminal {
		completed, expires = row.Result.CompletedAt.UnixNano(), row.Result.ExpiresAt.UnixMilli()+1
	}
	r, err := tx.ExecContext(ctx, "UPDATE runs SET result=?,cleanup=?,terminal=?,completed_at=?,expires_at=? WHERE id=? AND generation=? AND cleanup_scope=? AND EXISTS(SELECT 1 FROM run_owners o WHERE o.slot=runs.slot AND o.generation=runs.generation)", encoded, row.Cleanup.State, terminal, completed, expires, id, row.Generation, before.Scope)
	if err != nil {
		return storageError()
	}
	if n, err := r.RowsAffected(); err != nil || n != 1 {
		return failure("cleanup_unverified", "run generation or cleanup scope changed")
	}
	if err := syncBatchChild(ctx, tx, row.Result, terminal); err != nil {
		return err
	}
	if _, err := refreshIndexDebit(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return storageError()
	}
	return nil
}

// ApplyOutcome maps known process status independently of persistence success.
func ApplyOutcome(result *Result, c Completion) {
	result.ExitCode = c.ExitCode
	result.Signal = nil
	if c.Signal > 0 {
		result.Signal = &c.Signal
		result.ExitCode = nil
	}
	result.State = "exited"
	switch {
	case !CleanupAccepted(c.Cleanup):
		result.State = "interrupted"
	case !c.Started && (c.Reason == "queue_timeout" || c.Reason == "batch_stopped"):
		result.State = "skipped"
	case c.Reason == "cancelled":
		result.State = "cancelled"
	case c.Reason == "deadline":
		result.State = "timed_out"
	case !c.Started || c.SpawnCode != 0:
		result.State = "start_failed"
		result.Error = &Error{Code: "spawn_failed", Message: "execution process could not start"}
		if c.SpawnCode == 127 {
			result.Error = &Error{Code: "runtime_unavailable", Message: "execution runtime was not found"}
		}
	case c.Reason == "owner_lost" || c.Reason == "control_failed":
		result.State = "interrupted"
	}
	result.Reason = nil
	if c.Reason != "" {
		reason := c.Reason
		result.Reason = &reason
	}
	result.ElapsedMS = c.ElapsedMS
	result.Capture.ObservedBytes = c.ObservedBytes
}

func (s *Store) CaptureFailed(ctx context.Context, id, reason string) error {
	return s.update(ctx, id, func(_ *sql.Tx, row *Admitted) error {
		row.Result.Capture.State = "incomplete"
		row.Result.Capture.Reason = &reason
		return nil
	})
}

// prepareRun and insertRun are shared by single-run and atomic batch admission.
func (s *Store) prepareRun(owner *Owner, scope executor.CleanupScope, program Program) (Admitted, []byte, []byte, error) {
	cleanup := executor.CleanupEvidence{Scope: scope, State: executor.CleanupNotSpawned}
	if err := ValidateCleanup(cleanup); err != nil {
		return Admitted{}, nil, nil, err
	}
	program.PolicyRevision = s.policy.Revision
	program.TTLHours = s.policy.TTLHours
	program.MaxRunBytes = s.policy.MaxRunBytes
	id := uuid.NewString()
	ownerType := program.OwnerType
	if ownerType == "" {
		ownerType = "mcp"
	}
	if ownerType != "mcp" && ownerType != "cli" {
		return Admitted{}, nil, nil, failure("invalid_argument", "invalid execution owner type")
	}
	result := Result{Cleanup: cleanup, Version: EnvelopeVersion, Kind: "run", RunID: &id, OwnerID: owner.ID, OwnerType: ownerType, State: "admitted",
		Capture: Capture{State: "not_started"}, Redaction: Projection{Version: sanitize.StreamVersion, State: "ready"}, Index: Index{State: "not_requested"}}
	if program.Intent != "" {
		result.Index.State = "building"
	}
	encodedProgram, err := json.Marshal(program)
	if err != nil {
		return Admitted{}, nil, nil, storageError()
	}
	encodedResult, err := json.Marshal(result)
	if err != nil {
		return Admitted{}, nil, nil, storageError()
	}

	return Admitted{ID: id, Generation: owner.ID, Slot: owner.Slot, Program: program, Result: result, Cleanup: cleanup}, encodedProgram, encodedResult, nil
}
func insertRun(ctx context.Context, tx *sql.Tx, row Admitted, encodedProgram, encodedResult []byte) error {
	if _, err := tx.ExecContext(ctx, "UPDATE run_meta SET last_created=max(last_created+1,?) WHERE singleton=1", time.Now().UTC().UnixNano()); err != nil {
		return storageError()
	}
	var createdAt int64
	if err := tx.QueryRowContext(ctx, "SELECT last_created FROM run_meta WHERE singleton=1").Scan(&createdAt); err != nil {
		return storageError()
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO runs(id,generation,slot,program,result,cleanup_scope,cleanup,created_at) VALUES(?,?,?,?,?,?,?,?)", row.ID, row.Generation, row.Slot, encodedProgram, encodedResult, row.Cleanup.Scope, row.Cleanup.State, createdAt)
	if err != nil {
		return storageError()
	}
	checkpoint, err := sanitize.NewStream().Checkpoint()
	if err != nil {
		return storageError()
	}
	for _, stream := range []string{"stdout", "stderr"} {
		if _, err := tx.ExecContext(ctx, "INSERT INTO run_streams(run_id,stream,checkpoint) VALUES(?,?,?)", row.ID, stream, checkpoint); err != nil {
			return storageError()
		}
	}

	return nil
}
