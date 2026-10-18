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

func (s *Store) Admit(ctx context.Context, owner *Owner, program Program) (Admitted, error) {
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
	id := uuid.NewString()
	ownerType := program.OwnerType
	if ownerType == "" {
		ownerType = "mcp"
	}
	if ownerType != "mcp" && ownerType != "cli" {
		return Admitted{}, failure("invalid_argument", "invalid execution owner type")
	}
	result := Result{Version: 1, Kind: "run", RunID: &id, OwnerID: owner.ID, OwnerType: ownerType, State: "admitted",
		Capture: Capture{State: "not_started"}, Redaction: Projection{Version: sanitize.StreamVersion, State: "ready"}, Index: Index{State: "not_requested"}}
	if program.Intent != "" {
		result.Index.State = "building"
	}
	encodedProgram, err := json.Marshal(program)
	if err != nil {
		return Admitted{}, storageError()
	}
	encodedResult, err := json.Marshal(result)
	if err != nil {
		return Admitted{}, storageError()
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
	if _, err := tx.ExecContext(ctx, "UPDATE run_meta SET last_created=max(last_created+1,?) WHERE singleton=1", time.Now().UTC().UnixNano()); err != nil {
		return Admitted{}, storageError()
	}
	var createdAt int64
	if err := tx.QueryRowContext(ctx, "SELECT last_created FROM run_meta WHERE singleton=1").Scan(&createdAt); err != nil {
		return Admitted{}, storageError()
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO runs(id,generation,slot,program,result,cleanup,created_at) VALUES(?,?,?,?,?,'not_spawned',?)", id, owner.ID, owner.Slot, encodedProgram, encodedResult, createdAt)
	if err != nil {
		return Admitted{}, storageError()
	}
	checkpoint, err := sanitize.NewStream().Checkpoint()
	if err != nil {
		return Admitted{}, storageError()
	}
	for _, stream := range []string{"stdout", "stderr"} {
		if _, err := tx.ExecContext(ctx, "INSERT INTO run_streams(run_id,stream,checkpoint) VALUES(?,?,?)", id, stream, checkpoint); err != nil {
			return Admitted{}, storageError()
		}
	}
	// The admission commit is the write probe. No subprocess is authorized until
	// it succeeds and the helper authenticates/reloads this exact admitted row.
	if err := charge(ctx, tx, id, int64(len(encodedProgram))+runReserve, s.policy.MaxRetainedBytes, true); err != nil {
		return Admitted{}, err
	}
	if err := tx.Commit(); err != nil {
		return Admitted{}, storageError()
	}
	return Admitted{ID: id, Generation: owner.ID, Slot: owner.Slot, Program: program, Result: result, Cleanup: "not_spawned"}, nil
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
	var terminal, unverified bool
	err := query.QueryRowContext(ctx, `SELECT generation,slot,program,result,cleanup,terminal,
EXISTS(SELECT 1 FROM run_owners o WHERE o.slot=runs.slot AND o.generation=runs.generation AND o.cleanup_unverified=1)
FROM runs WHERE id=? AND evicting=0`, id).Scan(&row.Generation, &row.Slot, &program, &result, &row.Cleanup, &terminal, &unverified)
	if errors.Is(err, sql.ErrNoRows) {
		return row, failure("run_unavailable", "run is unavailable in this owner's store")
	}
	if err != nil {
		return row, storageError()
	}
	if json.Unmarshal(program, &row.Program) != nil || json.Unmarshal(result, &row.Result) != nil {
		return row, storageError()
	}
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

func (s *Store) Pending(ctx context.Context, id, generation string) error {
	r, err := s.db.ExecContext(ctx, "UPDATE runs SET cleanup='pending' WHERE id=? AND generation=? AND cleanup='not_spawned' AND terminal=0 AND EXISTS(SELECT 1 FROM run_owners o WHERE o.slot=runs.slot AND o.generation=runs.generation)", id, generation)
	if err != nil {
		return storageError()
	}
	n, err := r.RowsAffected()
	if err != nil || n != 1 {
		return failure("cleanup_unverified", "run start was already authorized or owner generation changed")
	}
	return nil
}

func (s *Store) Started(ctx context.Context, id string) error {
	return s.update(ctx, id, func(tx *sql.Tx, row *Admitted) error {
		if row.Cleanup != "pending" {
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
			if row.Cleanup != "pending" || row.Result.State != "running" {
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
		case c.CaptureLost || !c.Retired:
			result.Capture.State = "incomplete"
			reason := c.CaptureReason
			if reason == "" {
				reason = "capture_failed"
			}
			result.Capture.Reason = &reason
		default:
			result.Capture.State = "complete"
		}
		if c.Retired {
			row.Cleanup = "retired"
		} else {
			row.Cleanup = "pending"
		}
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
	if err := tx.QueryRowContext(ctx, "SELECT program,result,cleanup FROM runs WHERE id=?", id).Scan(&program, &result, &row.Cleanup); err != nil {
		return storageError()
	}
	if json.Unmarshal(program, &row.Program) != nil || json.Unmarshal(result, &row.Result) != nil {
		return storageError()
	}
	if row.Result.CompletedAt != nil {
		return failure("run_unavailable", "run is already terminal")
	}
	if _, err := tx.ExecContext(ctx, "SAVEPOINT capture_payload"); err != nil {
		return storageError()
	}
	if err := fn(tx, &row); err != nil {
		return err
	}
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
			report, err := retain(ctx, tx, s.policy.MaxRetainedBytes, row.chargeBytes, false)
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
	terminal := row.Result.CompletedAt != nil && row.Cleanup == "retired"
	var completed, expires any
	if terminal {
		completed, expires = row.Result.CompletedAt.UnixNano(), row.Result.ExpiresAt.UnixMilli()+1
	}
	if _, err := tx.ExecContext(ctx, "UPDATE runs SET result=?,cleanup=?,terminal=?,completed_at=?,expires_at=? WHERE id=?", encoded, row.Cleanup, terminal, completed, expires, id); err != nil {
		return storageError()
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
	case !c.Retired:
		result.State = "interrupted"
	case c.Reason == "cancelled":
		result.State = "cancelled"
	case !c.Started:
		result.State = "start_failed"
		result.Error = &Error{Code: "spawn_failed", Message: "execution process could not start"}
		if c.SpawnCode == 127 {
			result.Error = &Error{Code: "runtime_unavailable", Message: "execution runtime was not found"}
		}
	case c.Reason == "deadline":
		result.State = "timed_out"
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
