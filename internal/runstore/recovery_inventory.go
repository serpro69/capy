package runstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/serpro69/capy/internal/executor"
)

// The canonical sequence is versioned: header, registry, incidents, claims,
// manifests, then administrative subjects (store, owner-slot, run-ID order).
// Every variable-sized scan uses <=64 rows. Only one requested display page is
// retained; inferred keys are identities, never randomly allocated audit UUIDs.
type recoveryInventory struct {
	ctx     context.Context
	tx      *sql.Tx
	admin   *recoveryAdmin
	view    *RecoveryPlan
	encoder *json.Encoder
	limit   int
	step    func(context.Context) error
	owners  []inspectionRegistry
	visit   func(incident, string, int) error
}

func (b *recoveryInventory) record(kind string, value any) error {
	if err := b.ctx.Err(); err != nil {
		return err
	}
	if err := b.encoder.Encode(kind); err != nil {
		return err
	}
	return b.encoder.Encode(value)
}

func (b *recoveryInventory) checkpoint() error {
	if err := b.ctx.Err(); err != nil {
		return err
	}
	if b.step != nil {
		return b.step(b.ctx)
	}
	return nil
}

func buildRecoveryInventory(ctx context.Context, tx *sql.Tx, a *recoveryAdmin, view *RecoveryPlan, limit int, step func(context.Context) error) error {
	h := sha256.New()
	b := &recoveryInventory{ctx: ctx, tx: tx, admin: a, view: view, encoder: json.NewEncoder(h), limit: limit, step: step}
	var blocked, indexed bool
	var used, previousIndex, created, epoch int64
	if err := tx.QueryRowContext(ctx, "SELECT admission_blocked,recovery_revision,accounted_bytes,index_bytes,index_enabled,last_created FROM run_meta WHERE singleton=1").Scan(&blocked, &epoch, &used, &previousIndex, &indexed, &created); err != nil {
		return err
	}
	if epoch < 0 || epoch == math.MaxInt64 || used < 0 || previousIndex < 0 || created < 0 {
		return recoveryInconsistent("store counters or exhausted recovery revision")
	}
	view.RecoveryRevision = &epoch
	if err := b.record("header-v1", struct {
		Store, Target               string
		Policy                      Policy
		Epoch, Used, Index, Created int64
		Blocked, Indexed            bool
	}{a.id, a.binding, a.policy, epoch, used, previousIndex, created, blocked, indexed}); err != nil {
		return err
	}
	// Invalid keys must not fall before a keyset cursor's empty lower bound.
	// Validate metadata only; this is not a generic payload/corruption scanner.
	var invalid bool
	if err := tx.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM run_owners WHERE slot<0 OR slot>=64 OR generation IS NULL OR length(generation)<>36)
 OR EXISTS(SELECT 1 FROM runs WHERE (terminal=0 OR cleanup='pending' OR EXISTS(SELECT 1 FROM run_incidents i WHERE i.run_id=runs.id AND i.receipt_id IS NULL)) AND (id IS NULL OR length(id)<>36))
 OR EXISTS(SELECT 1 FROM run_incidents WHERE receipt_id IS NULL AND (id IS NULL OR length(id)<>36))
 OR EXISTS(SELECT 1 FROM run_batches WHERE id IS NULL OR length(id)<>36)
 OR EXISTS(SELECT 1 FROM run_batch_children WHERE ordinal<0 OR ordinal>=256)
 OR EXISTS(SELECT 1 FROM run_work_claims WHERE run_id IS NULL OR length(run_id)<>36)`).Scan(&invalid); err != nil {
		return err
	}
	if invalid {
		return recoveryInconsistent("inventory identity")
	}
	if err := b.registry(); err != nil {
		return err
	}
	if err := b.incidents(); err != nil {
		return err
	}
	if err := b.claims(); err != nil {
		return err
	}
	if err := b.batches(); err != nil {
		return err
	}
	if err := b.markers(blocked); err != nil {
		return err
	}
	if err := b.runs(); err != nil {
		return err
	}
	view.fingerprint = hex.EncodeToString(h.Sum(nil))
	if view.Counts.administrative() == 0 {
		return nil
	}
	// The snapshot's audit reservations are explicit independent charges. Apply
	// must also recheck actual index/page debit inside its eventual transaction.
	c := view.Counts
	if c.administrative() > math.MaxInt64/(4*recoveryDispositionReserve) {
		return failure("recovery_inventory_incomplete", "recovery inventory exceeds supported accounting bounds; no applicable token")
	}
	needed := int64(recoveryReceiptReserve) + c.administrative()*recoveryMembershipReserve + c.NewIncidents*recoveryIncidentReserve + c.Dispositions*recoveryDispositionReserve + c.Batches*recoveryMembershipReserve
	// Read-only equivalent of the accounting sample, without SAVEPOINT/UPDATE
	// or retention. Existing reservations and metadata remain untouched.
	if indexed {
		var debit int64
		if a.policy.AccountingMode == "dbstat" {
			if err := tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(pgsize),0) FROM dbstat WHERE name GLOB 'run_search_*' OR name='sqlite_autoindex_run_search_progress_1'").Scan(&debit); err != nil {
				return err
			}
		} else {
			var pages, free, size int64
			if err := tx.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
				return err
			}
			if err := tx.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&free); err != nil {
				return err
			}
			if err := tx.QueryRowContext(ctx, "PRAGMA page_size").Scan(&size); err != nil {
				return err
			}
			if free < 0 || free > pages || size < 1 || pages > math.MaxInt64/size {
				return recoveryInconsistent("accounting sample")
			}
			debit = (pages - free) * size
		}
		if previousIndex > used || debit > math.MaxInt64-(used-previousIndex) {
			return recoveryInconsistent("accounting debit")
		}
		used = used - previousIndex + debit
	}
	available := max(int64(0), a.policy.MaxRetainedBytes-used)
	free, err := filesystemAvailable(a.path)
	if err != nil {
		return failure("recovery_capacity", "cannot sample filesystem headroom; check the selected storage filesystem before planning")
	}
	diskNeeded := uint64(needed)*2 + 2<<20 // metadata plus WAL, using the migration slack
	view.Capacity = &RecoveryPlanCapacity{MetadataBytes: needed, AvailableBytes: available, DiskNeeded: diskNeeded, DiskAvailable: free, Sufficient: needed <= available && diskNeeded <= free}
	return nil
}

func (b *recoveryInventory) registry() error {
	rows, err := b.tx.QueryContext(b.ctx, "SELECT slot,CASE WHEN length(generation)=36 THEN generation ELSE NULL END,cleanup_unverified FROM run_owners ORDER BY slot LIMIT 64")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var o inspectionRegistry
		if err := rows.Scan(&o.Slot, &o.Generation, &o.Blocked); err != nil {
			return recoveryInconsistent("owner registry")
		}
		if o.Slot < 0 || o.Slot >= OwnerSlots || uuid.Validate(o.Generation) != nil {
			return recoveryInconsistent("owner registry")
		}
		b.owners = append(b.owners, o)
		if err := b.record("owner", o); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (b *recoveryInventory) incidents() error {
	after := ""
	for {
		if err := b.checkpoint(); err != nil {
			return err
		}
		rows, err := b.tx.QueryContext(b.ctx, "SELECT "+incidentColumns+" FROM run_incidents WHERE receipt_id IS NULL AND id>? ORDER BY id LIMIT 64", after)
		if err != nil {
			return err
		}
		items := []incident{}
		for rows.Next() {
			i, err := scanIncident(rows)
			if err != nil {
				rows.Close()
				return recoveryInconsistent("incident record")
			}
			items = append(items, i)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, i := range items {
			if i.Cause.rank() >= 4 {
				return recoveryInconsistent("recorded identity/proof/manifest incident")
			}
			if i.Kind == "run" {
				var matches bool
				if err := b.tx.QueryRowContext(b.ctx, "SELECT EXISTS(SELECT 1 FROM runs WHERE id=? AND generation=? AND slot=?)", i.RunID, i.Generation, *i.Slot).Scan(&matches); err != nil {
					return err
				}
				if !matches {
					return recoveryInconsistent("incident run identity")
				}
			} else if i.Kind == "owner" {
				var matches bool
				if err := b.tx.QueryRowContext(b.ctx, "SELECT EXISTS(SELECT 1 FROM run_owners WHERE slot=? AND generation=?)", *i.Slot, i.Generation).Scan(&matches); err != nil {
					return err
				}
				if !matches {
					return recoveryInconsistent("incident owner generation")
				}
			}
			if err := b.record("incident", i); err != nil {
				return err
			}
			after = i.ID
		}
		if len(items) < 64 {
			return nil
		}
	}
}

func (b *recoveryInventory) claims() error {
	after := ""
	for {
		if err := b.checkpoint(); err != nil {
			return err
		}
		rows, err := b.tx.QueryContext(b.ctx, `SELECT CASE WHEN length(c.run_id)=36 THEN c.run_id ELSE NULL END,c.slot,CASE WHEN length(c.generation)=36 THEN c.generation ELSE NULL END,
 EXISTS(SELECT 1 FROM runs r WHERE r.id=c.run_id),EXISTS(SELECT 1 FROM run_owners o WHERE o.slot=c.slot AND o.generation=c.generation)
 FROM run_work_claims c WHERE c.run_id>? ORDER BY c.run_id LIMIT 64`, after)
		if err != nil {
			return err
		}
		count := 0
		for rows.Next() {
			var id, generation string
			var slot int
			var run, owner bool
			if err := rows.Scan(&id, &slot, &generation, &run, &owner); err != nil {
				rows.Close()
				return recoveryInconsistent("work claim")
			}
			if !run || !owner || uuid.Validate(id) != nil || uuid.Validate(generation) != nil || slot < 0 || slot >= OwnerSlots {
				rows.Close()
				return recoveryInconsistent("work claim identity")
			}
			if err := b.record("claim", struct {
				Run, Generation string
				Slot            int
			}{id, generation, slot}); err != nil {
				rows.Close()
				return err
			}
			after = id
			count++
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		if count < 64 {
			return nil
		}
	}
}

func (b *recoveryInventory) markers(blocked bool) error {
	var attributed bool
	if err := b.tx.QueryRowContext(b.ctx, "SELECT EXISTS(SELECT 1 FROM run_incidents WHERE receipt_id IS NULL AND subject_kind<>'store') OR EXISTS(SELECT 1 FROM runs WHERE terminal=0 AND cleanup='pending' AND "+undisposedRunSQL+")").Scan(&attributed); err != nil {
		return err
	}
	i, found, err := b.openIncident("store")
	if err != nil {
		return err
	}
	if found || blocked && !attributed {
		if !found {
			i = storeIncident()
			b.view.Counts.NewIncidents++
		}
		b.view.Counts.StoreConcerns++
		if err := b.subject(i, "resolve_store", b.admin.policy.TTLHours); err != nil {
			return err
		}
	}
	for _, o := range b.owners {
		i := ownerIncident(o.Slot, o.Generation, incidentLegacy)
		stored, found, err := b.openIncident(i.Key)
		if err != nil {
			return err
		}
		if err := b.tx.QueryRowContext(b.ctx, `SELECT EXISTS(SELECT 1 FROM run_incidents WHERE slot=? AND receipt_id IS NULL AND subject_kind='run') OR EXISTS(SELECT 1 FROM runs WHERE slot=? AND terminal=0 AND cleanup='pending' AND `+undisposedRunSQL+`)`, o.Slot, o.Slot).Scan(&attributed); err != nil {
			return err
		}
		if found || o.Blocked && !attributed {
			if found {
				i = stored
			} else {
				b.view.Counts.NewIncidents++
			}
			b.view.Counts.OwnerConcerns++
			if err := b.subject(i, "resolve_owner", b.admin.policy.TTLHours); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *recoveryInventory) openIncident(key string) (incident, bool, error) {
	i, err := scanIncident(b.tx.QueryRowContext(b.ctx, "SELECT "+incidentColumns+" FROM run_incidents WHERE subject_key=? AND receipt_id IS NULL", key))
	if errors.Is(err, sql.ErrNoRows) {
		return incident{}, false, nil
	}
	if err != nil {
		return incident{}, false, recoveryInconsistent("incident subject")
	}
	return i, true, nil
}

func (b *recoveryInventory) subject(i incident, action string, ttl int) error {
	if b.visit != nil {
		if err := b.visit(i, action, ttl); err != nil {
			return err
		}
	}
	// Fingerprints include the stable identity and classification, not a
	// synthesized incident UUID/detection timestamp or estimated expiry.
	if err := b.record("subject", struct {
		Key, Action string
		TTL         int
	}{i.Key, action, ttl}); err != nil {
		return err
	}
	position := b.view.Counts.Subjects
	b.view.Counts.Subjects++
	if position < b.view.position || len(b.view.Subjects) >= b.limit {
		return nil
	}
	s := RecoveryPlanSubject{Key: i.Key, Action: action, Evidence: incidentSummary(i), TTLHours: ttl}
	if i.ID == "" {
		s.Evidence.BlocksAdmission = false
	}
	if action != "reconcile" {
		expiry := b.view.ObservedAt.Add(time.Duration(ttl) * time.Hour)
		s.EstimatedAuditExpiry = &expiry
		if action == "dispose_run" {
			s.EstimatedExpiry = &expiry
		}
	}
	b.view.Subjects = append(b.view.Subjects, s)
	return nil
}

type recoveryRunEvidence struct {
	ID, Generation, Phase                                     string
	Slot                                                      int
	Cleanup                                                   executor.CleanupEvidence
	Terminal, Evicting                                        bool
	TTL                                                       int
	Policy, Created, Stdout, Stderr, StdoutLines, StderrLines int64
	Completed, Expires                                        sql.NullInt64
	ResultDigest                                              [32]byte
	encoded                                                   []byte
}

func (b *recoveryInventory) runs() error {
	after := ""
	for {
		if err := b.checkpoint(); err != nil {
			return err
		}
		rows, err := b.tx.QueryContext(b.ctx, `SELECT CASE WHEN length(r.id)=36 THEN r.id ELSE NULL END,CASE WHEN length(r.generation)=36 THEN r.generation ELSE NULL END,r.slot,
 CASE WHEN length(r.last_phase)<=16 THEN r.last_phase ELSE NULL END,CASE WHEN length(r.result) BETWEEN 1 AND 8192 THEN r.result ELSE NULL END,
 CASE WHEN length(r.cleanup_scope)<=64 THEN r.cleanup_scope ELSE NULL END,CASE WHEN length(r.cleanup)<=16 THEN r.cleanup ELSE NULL END,r.terminal,r.evicting,
 CASE WHEN json_valid(CAST(r.program AS TEXT)) THEN json_extract(CAST(r.program AS TEXT),'$.ttl_hours') ELSE NULL END,
 CASE WHEN json_valid(CAST(r.program AS TEXT)) THEN json_extract(CAST(r.program AS TEXT),'$.policy_revision') ELSE NULL END,
 r.created_at,r.completed_at,r.expires_at,r.stdout_bytes,r.stderr_bytes,r.stdout_lines,r.stderr_lines
 FROM runs r WHERE r.id>? AND (r.terminal=0 OR r.cleanup='pending' OR EXISTS(SELECT 1 FROM run_incidents i WHERE i.run_id=r.id AND i.receipt_id IS NULL)) ORDER BY r.id LIMIT 64`, after)
		if err != nil {
			return err
		}
		items := []recoveryRunEvidence{}
		for rows.Next() {
			var r recoveryRunEvidence
			if err := rows.Scan(&r.ID, &r.Generation, &r.Slot, &r.Phase, &r.encoded, &r.Cleanup.Scope, &r.Cleanup.State, &r.Terminal, &r.Evicting, &r.TTL, &r.Policy, &r.Created, &r.Completed, &r.Expires, &r.Stdout, &r.Stderr, &r.StdoutLines, &r.StderrLines); err != nil {
				rows.Close()
				return recoveryInconsistent("run metadata")
			}
			items = append(items, r)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, r := range items {
			if err := b.run(r); err != nil {
				return err
			}
			after = r.ID
		}
		if len(items) < 64 {
			return nil
		}
	}
}

func (b *recoveryInventory) run(r recoveryRunEvidence) error {
	var result Result
	if uuid.Validate(r.ID) != nil || uuid.Validate(r.Generation) != nil || r.Slot < 0 || r.Slot >= OwnerSlots || !validPhase(r.Phase) || r.Evicting || r.TTL < 1 || int64(r.TTL) > math.MaxInt64/int64(time.Hour) || r.Policy < 1 || r.Policy > b.admin.policy.Revision || r.Created < 0 || r.Stdout < 0 || r.Stderr < 0 || r.StdoutLines < 1 || r.StderrLines < 1 || decodeRunResult(r.encoded, r.Cleanup, &result) != nil || validateTerminal(r.Cleanup, result, r.Terminal) != nil {
		return recoveryInconsistent("run identity, outcome or cleanup")
	}
	if result.RunID == nil || *result.RunID != r.ID || result.OwnerID != r.Generation {
		return recoveryInconsistent("run result identity")
	}
	if !validPlanState(result.State) {
		return recoveryInconsistent("run outcome state")
	}
	if r.Stdout > math.MaxInt64-r.Stderr || result.Capture.CapturedBytes != r.Stdout+r.Stderr ||
		r.Completed.Valid != r.Terminal || r.Expires.Valid != r.Terminal ||
		r.Terminal && (result.ExpiresAt == nil || r.Completed.Int64 != result.CompletedAt.UnixNano() || r.Expires.Int64 != result.ExpiresAt.UnixMilli()+1) {
		return recoveryInconsistent("run outcome timestamps or capture counters")
	}
	closed, err := operationallyClosed(b.ctx, b.tx, r.ID, r.Cleanup, result, r.Terminal)
	if err != nil {
		return err
	}
	if closed && !r.Terminal {
		// Exact immutable disposition replaces only operational ownership.
		// Still include its original evidence in the canonical fingerprint.
		r.ResultDigest = sha256.Sum256(r.encoded)
		if err := b.record("disposed-run", r); err != nil {
			return err
		}
		return b.streams(r)
	}
	if !r.Terminal {
		var owner bool
		if err := b.tx.QueryRowContext(b.ctx, "SELECT EXISTS(SELECT 1 FROM run_owners WHERE slot=? AND generation=?)", r.Slot, r.Generation).Scan(&owner); err != nil {
			return err
		}
		if !owner {
			return recoveryInconsistent("nonterminal owner generation")
		}
	}
	cause, err := reconciliationManifestCause(b.ctx, b.tx, recoveryCandidate{id: r.ID, generation: r.Generation, slot: r.Slot, result: r.encoded, cleanup: r.Cleanup, terminal: r.Terminal}, result)
	if err != nil {
		return err
	}
	if cause != "" && r.Terminal && uuid.Validate(result.BatchID) == nil {
		// Completed manifests can expire before a retained terminal run. No
		// missing nonterminal child or retained mismatching copy is excused.
		var retained bool
		if err := b.tx.QueryRowContext(b.ctx, "SELECT EXISTS(SELECT 1 FROM run_batches WHERE id=?)", result.BatchID).Scan(&retained); err != nil {
			return err
		}
		if !retained {
			cause = ""
		}
	}
	if cause != "" {
		return recoveryInconsistent("run manifest copy")
	}
	r.ResultDigest = sha256.Sum256(r.encoded)
	if err := b.record("run", r); err != nil {
		return err
	}
	if err := b.streams(r); err != nil {
		return err
	}
	i, found, err := b.openIncident("run:" + r.ID + ":" + r.Generation)
	if err != nil {
		return err
	}
	if found && i.BatchID != result.BatchID {
		return recoveryInconsistent("incident batch identity")
	}
	if !found {
		i = incident{Kind: "run", Key: "run:" + r.ID + ":" + r.Generation, RunID: r.ID, Generation: r.Generation, Slot: &r.Slot, BatchID: result.BatchID, Cause: incidentPending, Stage: stageReconciliation, Phase: r.Phase, Attribution: attributionInferred}
	}
	action := "reconcile"
	if r.Cleanup.State == executor.CleanupPending {
		if _, err := resolutionIndex(result.Index); err != nil {
			return err
		}
		action = "dispose_run"
		b.view.Counts.Dispositions++
		if !found {
			b.view.Counts.NewIncidents++
		}
	} else {
		if !r.Terminal {
			b.view.Counts.SafeReconciliation++
		}
		if found {
			action = "resolve_incident"
			b.view.Counts.IncidentOnly++
		}
	}
	before := len(b.view.Subjects)
	if err := b.subject(i, action, r.TTL); err != nil {
		return err
	}
	if len(b.view.Subjects) > before {
		s := &b.view.Subjects[len(b.view.Subjects)-1].Evidence
		s.Cleanup = &r.Cleanup
		s.Phase = r.Phase
		s.State = diagnosticState(result.State, "admitted", "queued", "running", "exited", "start_failed", "timed_out", "cancelled", "interrupted", "skipped")
		if action == "reconcile" {
			s.Reason = "accepted_cleanup"
		}
	}
	return nil
}

func (b *recoveryInventory) streams(r recoveryRunEvidence) error {
	rows, err := b.tx.QueryContext(b.ctx, `SELECT stream,covered,observed,CASE WHEN length(checkpoint)<=4096 THEN checkpoint ELSE NULL END FROM run_streams WHERE run_id=? ORDER BY stream LIMIT 3`, r.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var name string
		var covered, observed int64
		var checkpoint []byte
		if err := rows.Scan(&name, &covered, &observed, &checkpoint); err != nil {
			return err
		}
		size := r.Stdout
		if name == "stderr" {
			size = r.Stderr
		} else if name != "stdout" {
			return recoveryInconsistent("capture stream")
		}
		if covered < 0 || observed < covered || observed > size || len(checkpoint) == 0 {
			return recoveryInconsistent("capture prefix or projection checkpoint")
		}
		if err := b.record("stream", struct {
			Run, Stream       string
			Covered, Observed int64
			Checkpoint        [32]byte
		}{r.ID, name, covered, observed, sha256.Sum256(checkpoint)}); err != nil {
			return err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != 2 {
		return recoveryInconsistent("capture stream inventory")
	}
	return nil
}
