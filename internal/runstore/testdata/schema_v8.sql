-- Frozen schema-8 DDL from capy 828ee23bc33e1a18d4a848cdd75feb793c76375d.
-- Original raw SQL constants; do not regenerate from the candidate schema.

-- internal/runstore/store.go; Git blob 9286b31409826fc4948f106290cdcef5c199aeb3
CREATE TABLE run_meta(singleton INTEGER PRIMARY KEY CHECK(singleton=1), version INTEGER NOT NULL, store_id TEXT NOT NULL, owner TEXT NOT NULL, admission_blocked INTEGER NOT NULL DEFAULT 0 CHECK(admission_blocked IN (0,1)), last_created INTEGER NOT NULL DEFAULT 0, accounted_bytes INTEGER NOT NULL DEFAULT 0 CHECK(accounted_bytes>=0), index_bytes INTEGER NOT NULL DEFAULT 0 CHECK(index_bytes>=0), index_enabled INTEGER NOT NULL DEFAULT 0 CHECK(index_enabled IN (0,1)));
CREATE TABLE run_owners(slot INTEGER PRIMARY KEY CHECK(slot>=0 AND slot<64), generation TEXT NOT NULL UNIQUE, cleanup_unverified INTEGER NOT NULL DEFAULT 0 CHECK(cleanup_unverified IN (0,1)));
CREATE TABLE runs(
 id TEXT PRIMARY KEY, generation TEXT NOT NULL, slot INTEGER NOT NULL CHECK(slot>=0 AND slot<64),
 created_at INTEGER NOT NULL,
 program BLOB NOT NULL, result BLOB NOT NULL,
 cleanup TEXT NOT NULL CHECK(cleanup IN ('not_spawned','pending','retired')),
 terminal INTEGER NOT NULL DEFAULT 0 CHECK(terminal IN (0,1)),
 accounted_bytes INTEGER NOT NULL DEFAULT 0 CHECK(accounted_bytes>=0),
 evicting INTEGER NOT NULL DEFAULT 0 CHECK(evicting IN (0,1)),
 expires_at INTEGER, completed_at INTEGER,
 stdout_bytes INTEGER NOT NULL DEFAULT 0, stderr_bytes INTEGER NOT NULL DEFAULT 0,
 stdout_lines INTEGER NOT NULL DEFAULT 1, stderr_lines INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX runs_created ON runs(created_at,id);
CREATE INDEX runs_owner ON runs(slot,generation,terminal,id);
CREATE INDEX runs_unfinished ON runs(slot,generation,id) WHERE terminal=0;
CREATE INDEX runs_expiry ON runs(expires_at,id) WHERE terminal=1;
CREATE INDEX runs_completed ON runs(completed_at,id) WHERE terminal=1;
CREATE INDEX runs_evicting ON runs(id) WHERE evicting=1;
CREATE TABLE run_work_claims(
 run_id TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
 slot INTEGER NOT NULL CHECK(slot>=0 AND slot<64), generation TEXT NOT NULL
);
CREATE INDEX run_work_owner ON run_work_claims(slot,generation,run_id);
CREATE TABLE run_events(
 run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
 seq INTEGER NOT NULL CHECK(seq>0), phase TEXT NOT NULL CHECK(phase IN ('compile','run')),
 stream TEXT NOT NULL CHECK(stream IN ('stdout','stderr')), data BLOB NOT NULL CHECK(length(data)>0 AND length(data)<=16384),
 byte_offset INTEGER NOT NULL CHECK(byte_offset>=0), start_line INTEGER NOT NULL CHECK(start_line>0),
 PRIMARY KEY(run_id,seq)
);
CREATE INDEX run_events_stream ON run_events(run_id,stream,seq);
CREATE INDEX run_events_offsets ON run_events(run_id,stream,byte_offset);
CREATE INDEX run_events_lines ON run_events(run_id,stream,start_line,seq);
CREATE TABLE run_streams(
 run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
 stream TEXT NOT NULL CHECK(stream IN ('stdout','stderr')),
 checkpoint BLOB NOT NULL CHECK(length(checkpoint)<=4096),
 covered INTEGER NOT NULL DEFAULT 0, observed INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(run_id,stream)
);
CREATE TABLE run_projection(
 run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
 stream TEXT NOT NULL, raw_start INTEGER NOT NULL, raw_end INTEGER NOT NULL,
 seq INTEGER NOT NULL, phase TEXT NOT NULL, start_line INTEGER NOT NULL,
 text TEXT NOT NULL, redacted INTEGER NOT NULL,
 PRIMARY KEY(run_id,stream,raw_start), CHECK(raw_end>raw_start)
);
CREATE INDEX run_projection_order ON run_projection(run_id,seq,raw_start);

-- internal/runstore/policy.go; Git blob c8ec3cfbe679036054def846a3995bf6bc275b5e
CREATE TABLE run_policy(
 singleton INTEGER PRIMARY KEY CHECK(singleton=1), revision INTEGER NOT NULL CHECK(revision>0),
 ttl_hours INTEGER NOT NULL CHECK(ttl_hours>0 AND ttl_hours<=2562047),
 max_run_bytes INTEGER NOT NULL CHECK(max_run_bytes>0),
 max_retained_bytes INTEGER NOT NULL CHECK(max_retained_bytes>=max_run_bytes),
 schema_version INTEGER NOT NULL, redaction_version INTEGER NOT NULL,
 accounting_mode TEXT NOT NULL CHECK(accounting_mode IN ('dbstat','conservative'))
);

-- internal/runstore/search_index.go; Git blob b2d9ec7469afb6dd42065f1947d779066eeb5000
CREATE TABLE run_search_progress(
 run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
 stream TEXT NOT NULL CHECK(stream IN ('stdout','stderr')),
 next_start INTEGER NOT NULL DEFAULT 0, covered INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(run_id,stream)
);
CREATE TABLE run_search_windows(
 id INTEGER PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id),
 stream TEXT NOT NULL, raw_start INTEGER NOT NULL, raw_end INTEGER NOT NULL,
 seq INTEGER NOT NULL, phase TEXT NOT NULL, start_line INTEGER NOT NULL
);
CREATE UNIQUE INDEX run_search_window_start ON run_search_windows(run_id,stream,raw_start);
CREATE VIRTUAL TABLE run_search_fts USING fts5(title,content,tokenize='porter unicode61');
CREATE VIRTUAL TABLE run_search_trigram USING fts5(title,content,tokenize='trigram');

-- internal/runstore/batch.go; Git blob 6d72a1024f106b9df4e4004e0dc9a34689067870
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
