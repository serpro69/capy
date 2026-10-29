package runstore

// These are fixed metadata bounds, separate from payload/index reservations.
// Incident publication reserves its independent debit before insertion. Later
// apply transactions must do the same for receipts, dispositions and membership.
const (
	recoveryIncidentReserve    = 4096
	recoveryReceiptReserve     = 4096
	recoveryDispositionReserve = 16384
	recoveryMembershipReserve  = 4096
)

// Applied/detected/resolved clocks use Unix nanoseconds. Retention/audit
// deadlines use Unix milliseconds rounded up (like runs.expires_at), allowing
// the maximum admission TTL to extend beyond time.Time's UnixNano range.
const recoverySchema = `
CREATE TABLE run_recovery_receipts(
 id TEXT PRIMARY KEY CHECK(length(id)=36), plan_hash TEXT NOT NULL UNIQUE CHECK(length(plan_hash)=64),
 store_id TEXT NOT NULL CHECK(length(store_id)=36), version INTEGER NOT NULL CHECK(version=1),
 applied_at INTEGER NOT NULL, method TEXT NOT NULL CHECK(method='operator'),
 basis TEXT NOT NULL CHECK(basis IN ('groups_stopped','host_restarted')), uid INTEGER NOT NULL CHECK(uid>=0),
 explanation TEXT NOT NULL CHECK(length(CAST(explanation AS BLOB)) BETWEEN 1 AND 1024),
 incident_count INTEGER NOT NULL CHECK(incident_count>=0), run_count INTEGER NOT NULL CHECK(run_count>=0),
 owner_count INTEGER NOT NULL CHECK(owner_count>=0), store_count INTEGER NOT NULL CHECK(store_count>=0),
 charged_bytes INTEGER NOT NULL CHECK(charged_bytes BETWEEN 0 AND 4096), audit_expires_at INTEGER NOT NULL,
 pruning INTEGER NOT NULL DEFAULT 0 CHECK(pruning IN (0,1))
);
CREATE INDEX run_recovery_receipts_expiry ON run_recovery_receipts(pruning,audit_expires_at,id);
CREATE TABLE run_incidents(
 id TEXT PRIMARY KEY CHECK(length(id)=36), version INTEGER NOT NULL CHECK(version=1),
 subject_kind TEXT NOT NULL CHECK(subject_kind IN ('run','owner','store')),
 subject_key TEXT NOT NULL CHECK(length(subject_key) BETWEEN 1 AND 256),
 slot INTEGER CHECK(slot>=0 AND slot<64), generation TEXT CHECK(length(generation)=36),
 run_id TEXT CHECK(length(run_id)=36), batch_id TEXT CHECK(length(batch_id)=36),
 reason TEXT NOT NULL CHECK(reason IN ('pending_cleanup','finalization_unpersisted','supervisor_lost','startup_uncertain','generation_mismatch','invalid_proof','invalid_manifest','structural_inconsistency','legacy_unattributed')),
 attribution TEXT NOT NULL CHECK(attribution IN ('recorded','inferred','unattributed')),
 detected_at INTEGER, stage TEXT NOT NULL CHECK(stage IN ('startup','helper_wait','finalization','reconciliation','legacy_inspection')),
 last_phase TEXT NOT NULL CHECK(last_phase IN ('unknown','admitted','queued','authorized','compile','run')),
 revision INTEGER NOT NULL CHECK(revision>0), receipt_id TEXT REFERENCES run_recovery_receipts(id),
 charged_bytes INTEGER NOT NULL CHECK(charged_bytes BETWEEN 0 AND 4096), audit_expires_at INTEGER,
 CHECK((subject_kind='run' AND run_id IS NOT NULL AND generation IS NOT NULL AND slot IS NOT NULL) OR
       (subject_kind='owner' AND run_id IS NULL AND generation IS NOT NULL AND slot IS NOT NULL) OR
       (subject_kind='store' AND run_id IS NULL AND generation IS NULL AND slot IS NULL))
);
CREATE UNIQUE INDEX run_incidents_open_subject ON run_incidents(subject_key) WHERE receipt_id IS NULL;
CREATE INDEX run_incidents_run ON run_incidents(run_id,receipt_id);
CREATE INDEX run_incidents_owner ON run_incidents(slot,generation,receipt_id);
CREATE INDEX run_incidents_receipt ON run_incidents(receipt_id,id);
CREATE TABLE run_resolutions(
 run_id TEXT PRIMARY KEY CHECK(length(run_id)=36), store_id TEXT NOT NULL CHECK(length(store_id)=36),
 generation TEXT NOT NULL CHECK(length(generation)=36), slot INTEGER NOT NULL CHECK(slot>=0 AND slot<64),
 batch_id TEXT CHECK(length(batch_id)=36), receipt_id TEXT NOT NULL REFERENCES run_recovery_receipts(id),
 incident_id TEXT NOT NULL REFERENCES run_incidents(id), version INTEGER NOT NULL CHECK(version=1),
 original_result BLOB NOT NULL CHECK(length(original_result) BETWEEN 1 AND 8192),
 result_sha256 BLOB NOT NULL CHECK(length(result_sha256)=32),
 cleanup_scope TEXT NOT NULL CHECK(cleanup_scope IN ('linux_descendants_v1','darwin_process_group_observation_v1')),
 cleanup TEXT NOT NULL CHECK(cleanup='pending'), terminal INTEGER NOT NULL CHECK(terminal=0),
 ttl_hours INTEGER NOT NULL CHECK(ttl_hours>0 AND ttl_hours<=2562047), policy_revision INTEGER NOT NULL CHECK(policy_revision>0),
 resolved_at INTEGER NOT NULL, retention_expires_at INTEGER NOT NULL, audit_expires_at INTEGER NOT NULL,
 charged_bytes INTEGER NOT NULL CHECK(charged_bytes BETWEEN 0 AND 16384),
 payload_state TEXT NOT NULL CHECK(payload_state IN ('retained','evicting','evicted')),
 evicted_at INTEGER, eviction_cause TEXT CHECK(eviction_cause IN ('ttl','quota')),
 CHECK((payload_state='evicted')=(evicted_at IS NOT NULL)),
 CHECK((payload_state='retained')=(eviction_cause IS NULL))
);
CREATE INDEX run_resolutions_receipt ON run_resolutions(receipt_id,run_id);
CREATE INDEX run_resolutions_incident ON run_resolutions(incident_id,run_id);
CREATE INDEX run_resolutions_batch ON run_resolutions(batch_id,run_id);
CREATE INDEX run_resolutions_expiry ON run_resolutions(payload_state,retention_expires_at,run_id);
CREATE INDEX run_resolutions_order ON run_resolutions(payload_state,resolved_at,run_id);
CREATE TABLE run_incident_resolutions(
 receipt_id TEXT NOT NULL REFERENCES run_recovery_receipts(id), incident_id TEXT NOT NULL UNIQUE REFERENCES run_incidents(id),
 subject_snapshot BLOB NOT NULL CHECK(length(subject_snapshot) BETWEEN 1 AND 2048),
 audit_expires_at INTEGER NOT NULL, charged_bytes INTEGER NOT NULL CHECK(charged_bytes BETWEEN 0 AND 4096),
 PRIMARY KEY(receipt_id,incident_id)
);
CREATE TABLE run_batch_resolutions(
 batch_id TEXT PRIMARY KEY REFERENCES run_batches(id) ON DELETE CASCADE,
 receipt_id TEXT NOT NULL REFERENCES run_recovery_receipts(id),
 closed_at INTEGER NOT NULL, retention_expires_at INTEGER NOT NULL,
 charged_bytes INTEGER NOT NULL CHECK(charged_bytes BETWEEN 0 AND 4096)
);
CREATE INDEX run_batch_resolutions_receipt ON run_batch_resolutions(receipt_id,batch_id);
CREATE INDEX run_batch_resolutions_expiry ON run_batch_resolutions(retention_expires_at,batch_id);
CREATE TABLE run_search_state(
 run_id TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
 state TEXT NOT NULL CHECK(state IN ('not_requested','building','partial','ready','failed')),
 cursor TEXT CHECK(length(CAST(cursor AS BLOB))<=1024),
 error_code TEXT CHECK(error_code IN ('storage_unavailable','capture_limit','run_unavailable','index_unavailable','index_capacity','cleanup_unverified'))
);
`
