package runstore

// Frozen schema-9 destination. Never derive a historical stage from fresh-store DDL.
const runTableSchemaV9 = `
CREATE TABLE runs(
 id TEXT PRIMARY KEY, generation TEXT NOT NULL, slot INTEGER NOT NULL CHECK(slot>=0 AND slot<64),
 created_at INTEGER NOT NULL,
 program BLOB NOT NULL, result BLOB NOT NULL,
 cleanup_scope TEXT NOT NULL CHECK(cleanup_scope IN ('linux_descendants_v1','darwin_process_group_observation_v1')),
 cleanup TEXT NOT NULL CHECK(cleanup IN ('not_spawned','pending','retired','observed')),
 terminal INTEGER NOT NULL DEFAULT 0 CHECK(terminal IN (0,1)),
 accounted_bytes INTEGER NOT NULL DEFAULT 0 CHECK(accounted_bytes>=0),
 evicting INTEGER NOT NULL DEFAULT 0 CHECK(evicting IN (0,1)),
 expires_at INTEGER, completed_at INTEGER,
 stdout_bytes INTEGER NOT NULL DEFAULT 0, stderr_bytes INTEGER NOT NULL DEFAULT 0,
 stdout_lines INTEGER NOT NULL DEFAULT 1, stderr_lines INTEGER NOT NULL DEFAULT 1,
 CHECK((cleanup_scope='linux_descendants_v1' AND cleanup IN ('not_spawned','pending','retired')) OR
       (cleanup_scope='darwin_process_group_observation_v1' AND cleanup IN ('not_spawned','pending','observed')))
);
`
