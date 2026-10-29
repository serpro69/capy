package runstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/sqliteutil"
)

// Manifests own their summaries; run eviction never cascades into this table.
// Reservations pay for bounded mutable results even after child logs disappear.
const batchChildReserve = 8192
const batchSchema = `
CREATE TABLE run_batches(
 id TEXT PRIMARY KEY, generation TEXT NOT NULL, slot INTEGER NOT NULL,
 created_at INTEGER NOT NULL, completed_at INTEGER, expires_at INTEGER,
 ttl_hours INTEGER NOT NULL, policy_revision INTEGER NOT NULL,
 queries BLOB NOT NULL, query_scope TEXT NOT NULL,
 requested INTEGER NOT NULL, effective INTEGER NOT NULL,
 accounted_bytes INTEGER NOT NULL CHECK(accounted_bytes>=0)
);
CREATE INDEX run_batches_expiry ON run_batches(expires_at,id) WHERE completed_at IS NOT NULL;
CREATE TABLE run_batch_children(
 batch_id TEXT NOT NULL REFERENCES run_batches(id) ON DELETE CASCADE,
 ordinal INTEGER NOT NULL CHECK(ordinal>=0 AND ordinal<256),
 run_id TEXT NOT NULL UNIQUE, label TEXT NOT NULL, command TEXT NOT NULL,
 result BLOB NOT NULL, terminal INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(batch_id,ordinal)
);
`

type BatchCommand struct {
	Label   string `json:"label"`
	Command string `json:"command"`
}
type BatchInput struct {
	Commands             []BatchCommand
	Queries              []string
	QueryScope           string
	Cwd                  string
	Deadline             time.Time
	Concurrency          int
	EffectiveConcurrency int
}

func (b BatchInput) Validate() error {
	if len(b.Commands) == 0 || len(b.Commands) > 256 || (b.Concurrency < 1 || b.Concurrency > 8) {
		return failure("invalid_argument", "capture requires 1–256 commands and concurrency between 1 and 8")
	}
	size := 0
	for _, c := range b.Commands {
		size += len(c.Command) + len(c.Label)
		if size > 1<<20 {
			return failure("invalid_argument", "input bytes limit exceeded: batch commands and labels must total at most 1 MiB")
		}
		if strings.TrimSpace(c.Command) == "" || !utf8.ValidString(c.Command) || !utf8.ValidString(c.Label) || len(c.Label) > 512 {
			return failure("invalid_argument", "batch requires nonempty UTF-8 commands, labels at most 512 bytes and at most 1 MiB total input")
		}
	}
	return (SearchOptions{BatchID: uuid.Nil.String(), Queries: b.Queries, Limit: 5, QueryScope: b.QueryScope}).Validate()
}

type BatchResult struct {
	Cleanup              executor.CleanupEvidence `json:"cleanup"`
	Capacity             *Capacity                `json:"capacity,omitempty"`
	Version              int                      `json:"version"`
	Kind                 string                   `json:"kind"`
	BatchID              string                   `json:"batch_id"`
	OwnerID              string                   `json:"owner_id"`
	OwnerType            string                   `json:"owner_type"`
	State                string                   `json:"state"`
	RequestedConcurrency int                      `json:"requested_concurrency"`
	EffectiveConcurrency int                      `json:"effective_concurrency"`
	CurrentConcurrency   int                      `json:"current_concurrency"`
	Counts               map[string]int           `json:"counts"`
	FailedCount          int                      `json:"failed_count"`
	ObservedBytes        int64                    `json:"observed_bytes"`
	CapturedBytes        int64                    `json:"captured_bytes"`
	IncompleteCount      int                      `json:"incomplete_count"`
	CompletedAt          *time.Time               `json:"completed_at"`
	ExpiresAt            *time.Time               `json:"expires_at"`
	ChildrenAction       string                   `json:"children_action"`
	SearchAction         *BatchSearchRequest      `json:"search_action,omitempty"`
	Search               *SearchPage              `json:"search,omitempty"`
	Error                *Error                   `json:"error,omitempty"`
}
type BatchSearchRequest struct {
	Action     string `json:"action"`
	BatchID    string `json:"batch_id"`
	QueryScope string `json:"query_scope"`
}

// Dispatch persists the spawn-reservation deadline before helper authorization.
// Its bounded change fits the mutable metadata reservation paid at admission.
func (s *Store) Dispatch(ctx context.Context, id, generation string, deadline time.Time) error {
	return s.update(ctx, id, func(tx *sql.Tx, row *Admitted) error {
		if row.Result.OwnerID != generation || row.Cleanup.State != executor.CleanupNotSpawned || row.Result.State != "queued" {
			return storageError()
		}
		row.Program.Deadline = deadline
		encoded, err := json.Marshal(row.Program)
		if err != nil {
			return storageError()
		}
		if _, err := tx.ExecContext(ctx, "UPDATE runs SET program=? WHERE id=?", encoded, id); err != nil {
			return storageError()
		}
		return nil
	})
}

// AdmitBatch commits the entire inventory, quota reservation and manifest before
// authorizing any child. Every ordinal has a run ID even if it never starts.
func (s *Store) AdmitBatch(ctx context.Context, owner *Owner, cleanupScope executor.CleanupScope, input BatchInput) (_ BatchResult, _ []Admitted, retErr error) {
	defer func() {
		if retErr != nil {
			retErr = AdmissionError(retErr, s.owner)
		}
	}()
	var empty BatchResult
	if input.EffectiveConcurrency == 0 {
		input.EffectiveConcurrency = min(input.Concurrency, len(input.Commands))
	}
	if input.EffectiveConcurrency < 1 || input.EffectiveConcurrency > min(input.Concurrency, len(input.Commands)) {
		return empty, nil, failure("invalid_argument", "invalid effective concurrency")
	}
	if err := input.Validate(); err != nil {
		return empty, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, recoveryTimeout)
	defer cancel()
	registry, err := s.recoveryLease(ctx)
	if err != nil {
		return empty, nil, err
	}
	defer registry.Close()
	if _, err = s.reconcile(ctx); err != nil {
		return empty, nil, err
	}
	if err = s.checkQuarantine(ctx); err != nil {
		return empty, nil, err
	}
	id := uuid.NewString()
	rows := make([]Admitted, 0, len(input.Commands))
	programs := make([][]byte, 0, len(input.Commands))
	results := make([][]byte, 0, len(input.Commands))
	queries, err := json.Marshal(input.Queries)
	if err != nil {
		return empty, nil, storageError()
	}
	manifestCharge := int64(4096 + len(queries))
	var runCharge int64
	for _, c := range input.Commands {
		row, program, _, err := s.prepareRun(owner, cleanupScope, Program{Owner: s.owner, Request: executor.ExecRequest{Language: executor.Shell, Code: c.Command, Cwd: input.Cwd}, Deadline: input.Deadline})
		if err != nil {
			return empty, nil, err
		}
		row.Result.BatchID = id
		row.Result.State = "queued"
		row.LastPhase = "queued"
		result, err := encodeManifest(row.Result)
		if err != nil {
			return empty, nil, storageError()
		}
		rows = append(rows, row)
		programs = append(programs, program)
		results = append(results, result)
		manifestCharge += int64(batchChildReserve + len(c.Label) + len(c.Command))
		runCharge += runReserve + int64(len(program))
	}
	needed := manifestCharge + runCharge
	if err = s.prepareCapacity(ctx, needed); err != nil {
		return empty, nil, err
	}
	tx, err := sqliteutil.BeginImmediateContext(ctx, s.db, "runs")
	if err != nil {
		return empty, nil, storageError()
	}
	defer tx.Rollback()
	if err := s.checkQuarantineQuery(ctx, tx); err != nil {
		return empty, nil, err
	}
	var generation string
	if err = tx.QueryRowContext(ctx, "SELECT generation FROM run_owners WHERE slot=?", owner.Slot).Scan(&generation); err != nil || generation != owner.ID {
		return empty, nil, storageError()
	}
	// All growth is reserved under this writer lock; other owners cannot spend it.
	report, err := retain(ctx, tx, s.policy.MaxRetainedBytes, needed, true)
	if err != nil {
		return empty, nil, err
	}
	if report.More {
		return empty, nil, quotaError()
	}
	scope := input.QueryScope
	if scope == "" {
		scope = "batch"
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO run_batches(id,generation,slot,created_at,ttl_hours,policy_revision,queries,query_scope,requested,effective,accounted_bytes) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, owner.ID, owner.Slot, time.Now().UTC().UnixNano(), s.policy.TTLHours, s.policy.Revision, queries, scope, input.Concurrency, input.EffectiveConcurrency, manifestCharge)
	if err != nil {
		return empty, nil, storageError()
	}
	for i, row := range rows {
		// The private manifest is scoped v2; public run results stay v1 until
		// Task 7. Do not reuse the manifest encoding for the run-row blob.
		runResult, err := json.Marshal(row.Result)
		if err != nil {
			return empty, nil, storageError()
		}
		if err = insertRun(ctx, tx, row, programs[i], runResult); err != nil {
			return empty, nil, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO run_batch_children(batch_id,ordinal,run_id,label,command,result) VALUES(?,?,?,?,?,?)`, id, i, row.ID, input.Commands[i].Label, input.Commands[i].Command, results[i])
		if err != nil {
			return empty, nil, storageError()
		}
		if _, err = tx.ExecContext(ctx, "UPDATE runs SET accounted_bytes=? WHERE id=?", runReserve+int64(len(programs[i])), row.ID); err != nil {
			return empty, nil, storageError()
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE run_meta SET accounted_bytes=accounted_bytes+? WHERE singleton=1", needed); err != nil {
		return empty, nil, storageError()
	}
	used, err := refreshIndexDebit(ctx, tx)
	if err != nil {
		return empty, nil, err
	}
	if used > s.policy.MaxRetainedBytes {
		return empty, nil, quotaError()
	}
	if err = tx.Commit(); err != nil {
		return empty, nil, storageError()
	}
	// Construct from committed admission; a post-commit read failure must never
	// misreport this non-idempotent admission as an unstarted rejection.
	result := BatchResult{Version: EnvelopeVersion, Kind: "batch", BatchID: id, OwnerID: owner.ID, OwnerType: "mcp", State: "queued", RequestedConcurrency: input.Concurrency, EffectiveConcurrency: input.EffectiveConcurrency, Counts: map[string]int{"queued": len(rows)}, ChildrenAction: "capy_runs(action=list,batch_id=" + id + ")"}
	result.Cleanup = rows[0].Cleanup
	if len(input.Queries) > 0 {
		result.SearchAction = &BatchSearchRequest{Action: "search", BatchID: id, QueryScope: scope}
	}
	return result, rows, nil
}

// Called in the same transaction as every child state change and crash recovery.
func syncBatchChild(ctx context.Context, tx *sql.Tx, result Result, terminal bool) error {
	if result.BatchID == "" {
		return nil
	}
	if err := validateTerminal(result.Cleanup, result, terminal); err != nil {
		return err
	}
	encoded, err := encodeManifest(result)
	if err != nil || len(encoded) > batchChildReserve {
		return storageError()
	}
	if _, err = tx.ExecContext(ctx, "UPDATE run_batch_children SET result=?,terminal=? WHERE run_id=? AND batch_id=?", encoded, terminal, *result.RunID, result.BatchID); err != nil {
		return storageError()
	}
	if !terminal {
		return nil
	}
	var pending bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM run_batch_children WHERE batch_id=? AND terminal=0)", result.BatchID).Scan(&pending); err != nil {
		return storageError()
	}
	if !pending {
		// Validate the aggregate only once, at final completion. Re-decoding all
		// children on every recovered child makes bounded recovery quadratic.
		if _, _, err := batchChildren(ctx, tx, result.BatchID); err != nil {
			return err
		}
		now := time.Now().UTC()
		_, err = tx.ExecContext(ctx, "UPDATE run_batches SET completed_at=?,expires_at=?+ttl_hours*3600000 WHERE id=? AND completed_at IS NULL", now.UnixNano(), now.UnixMilli()+1, result.BatchID)
		if err != nil {
			return storageError()
		}
	}
	return nil
}

func (s *Store) batchMetadata(ctx context.Context, id string) (BatchResult, []string, string, error) {
	return batchMetadata(ctx, s.db, id)
}

func batchMetadata(ctx context.Context, query runQuerier, id string) (BatchResult, []string, string, error) {
	b := BatchResult{Version: EnvelopeVersion, Kind: "batch", BatchID: id, OwnerType: "mcp", Counts: map[string]int{}, ChildrenAction: "capy_runs(action=list,batch_id=" + id + ")"}
	if uuid.Validate(id) != nil {
		return b, nil, "", failure("invalid_argument", "batch_id must be a UUID")
	}
	var completed, expires sql.NullInt64
	var unverified bool
	var queries []byte
	var scope string
	err := query.QueryRowContext(ctx, `SELECT generation,requested,effective,completed_at,expires_at,queries,query_scope,EXISTS(SELECT 1 FROM run_owners o WHERE o.generation=run_batches.generation AND o.cleanup_unverified=1) FROM run_batches WHERE id=? AND (expires_at IS NULL OR expires_at>?)`, id, time.Now().UTC().UnixMilli()).Scan(&b.OwnerID, &b.RequestedConcurrency, &b.EffectiveConcurrency, &completed, &expires, &queries, &scope, &unverified)
	if errors.Is(err, sql.ErrNoRows) {
		return b, nil, "", failure("batch_unavailable", "batch is unavailable in this owner's store")
	}
	if err != nil {
		return b, nil, "", storageError()
	}
	if unverified && !completed.Valid {
		b.Error = &Error{Code: "cleanup_unverified", Message: "last recorded child states; cleanup is unverified; stop old execution groups before explicit operator reset"}
	}
	if completed.Valid {
		t := time.Unix(0, completed.Int64).UTC()
		b.CompletedAt = &t
	}
	if expires.Valid {
		t := time.UnixMilli(expires.Int64).UTC()
		b.ExpiresAt = &t
	}
	var q []string
	if json.Unmarshal(queries, &q) != nil {
		return b, nil, "", storageError()
	}
	if len(q) > 0 {
		b.SearchAction = &BatchSearchRequest{Action: "search", BatchID: id, QueryScope: scope}
	}
	return b, q, scope, nil
}
func (s *Store) BatchStatus(ctx context.Context, id string) (BatchResult, error) {
	b, _, children, err := s.batchSnapshot(ctx, id)
	if err != nil {
		return b, err
	}
	pending := false
	for _, child := range children {
		r, terminal := child.result, child.terminal
		b.Counts[r.State]++
		b.ObservedBytes += r.Capture.ObservedBytes
		b.CapturedBytes += r.Capture.CapturedBytes
		pending = pending || !terminal
		if r.State == "running" {
			b.CurrentConcurrency++
		}
		if r.Capture.State == "incomplete" {
			b.IncompleteCount++
		}
		if r.State != "admitted" && r.State != "queued" && r.State != "running" && (r.State != "exited" || r.ExitCode == nil || *r.ExitCode != 0 || r.Signal != nil) {
			b.FailedCount++
		}
	}
	b.State = "completed"
	if pending {
		b.State = "running"
		if b.Counts["queued"] == sumCounts(b.Counts) {
			b.State = "queued"
		}
	} else if b.Counts["interrupted"] > 0 {
		b.State = "interrupted"
	} else if b.Counts["cancelled"] > 0 {
		b.State = "cancelled"
	}
	return b, nil
}
func sumCounts(counts map[string]int) int {
	n := 0
	for _, v := range counts {
		n += v
	}
	return n
}

func retainBatch(ctx context.Context, tx *sql.Tx, pressure, expire bool, budget int) (bool, int64, int64, error) {
	if (!pressure && !expire) || budget < 257 {
		return false, 0, 0, nil
	}
	var id string
	var debit, children int64
	query := "SELECT id,accounted_bytes,(SELECT count(*) FROM run_batch_children c WHERE c.batch_id=run_batches.id) FROM run_batches WHERE completed_at IS NOT NULL"
	args := []any{}
	if !pressure {
		query += " AND expires_at<=?"
		args = append(args, time.Now().UTC().UnixMilli())
	}
	query += " ORDER BY expires_at,id LIMIT 1"
	err := tx.QueryRowContext(ctx, query, args...).Scan(&id, &debit, &children)
	if errors.Is(err, sql.ErrNoRows) {
		return false, 0, 0, nil
	}
	if err != nil {
		return false, 0, 0, storageError()
	}
	proofs, _, err := batchChildren(ctx, tx, id)
	if err != nil {
		return false, 0, 0, err
	}
	for _, child := range proofs {
		if !child.terminal || !CleanupAccepted(child.result.Cleanup) {
			return false, 0, 0, failure("cleanup_unverified", "batch expiry requires completed child cleanup")
		}
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM run_batches WHERE id=?", id); err != nil {
		return false, 0, 0, storageError()
	}
	if _, err = tx.ExecContext(ctx, "UPDATE run_meta SET accounted_bytes=accounted_bytes-? WHERE singleton=1", debit); err != nil {
		return false, 0, 0, storageError()
	}
	return true, debit, children + 1, nil
}
