package vault

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/serpro69/capy/internal/sqliteutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrateVault_FreshDBHasIndexVersion(t *testing.T) {
	s := newTestVault(t)
	db, err := s.getDB(context.Background()) // runs schemaSQL + migrateVault
	require.NoError(t, err)

	// Fresh DBs get index_version from schemaSQL.
	_, err = db.Exec(`SELECT index_version FROM vault_sessions WHERE 0`)
	require.NoError(t, err, "index_version column must exist on a fresh vault")

	// The migration is still recorded so it never re-runs.
	var cnt int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM vault_migrations WHERE name='0003_add_index_version'`).Scan(&cnt))
	assert.Equal(t, 1, cnt)

	// Idempotent: re-running migrateVault is a no-op (no error, no duplicate).
	require.NoError(t, migrateVault(context.Background(), db))
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM vault_migrations WHERE name='0003_add_index_version'`).Scan(&cnt))
	assert.Equal(t, 1, cnt)
}

func TestMigrate0003_AddsColumnToLegacyVault(t *testing.T) {
	// A pre-feature vault lacks index_version. Build that legacy shape directly
	// (plaintext DB — encryption is irrelevant to the migration logic) and verify
	// the ALTER adds the column with every existing row flagged stale (1).
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "legacy.db"))
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE vault_sessions (uuid TEXT PRIMARY KEY, content_hash TEXT NOT NULL, raw_jsonl BLOB NOT NULL);
		CREATE TABLE vault_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
		CREATE TABLE vault_migrations (name TEXT PRIMARY KEY, applied_at TEXT DEFAULT CURRENT_TIMESTAMP);
		INSERT INTO vault_sessions (uuid, content_hash, raw_jsonl) VALUES ('legacy', 'h', x'7b7d');
	`)
	require.NoError(t, err)

	require.NoError(t, migrate0003AddIndexVersion(context.Background(), db))

	var v int
	require.NoError(t, db.QueryRow(`SELECT index_version FROM vault_sessions WHERE uuid='legacy'`).Scan(&v))
	assert.Equal(t, 1, v, "legacy rows default to stale (1) → eligible for reindex")

	var cnt int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM vault_migrations WHERE name='0003_add_index_version'`).Scan(&cnt))
	assert.Equal(t, 1, cnt)

	// Idempotent re-run: the guard short-circuits, no duplicate-column error.
	require.NoError(t, migrate0003AddIndexVersion(context.Background(), db))
}

func TestMigrateVault_FreshDBHasChunkFTS(t *testing.T) {
	s := newTestVault(t)
	db, err := s.getDB(context.Background()) // runs schemaSQL + migrateVault
	require.NoError(t, err)

	// Fresh DBs get both chunk tables from schemaSQL, with the full column set
	// (title/content_text first — the retrieval-skeleton column-order invariant
	// is positional, so the SELECT below also pins the declared order).
	for _, table := range []string{"vault_chunks", "vault_chunks_trigram"} {
		//nolint:gosec // table is a test-controlled constant, never user input
		_, err = db.Exec(`SELECT title, content_text, session_uuid, subagent_id,
			chunk_index, first_line_index FROM ` + table + ` WHERE 0`)
		require.NoError(t, err, "%s must exist with the full column set on a fresh vault", table)
	}

	// The migration is still recorded so it never re-runs.
	var cnt int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM vault_migrations WHERE name='0004_add_chunk_fts'`).Scan(&cnt))
	assert.Equal(t, 1, cnt)

	// Idempotent: re-running migrateVault is a no-op (no error, no duplicate).
	require.NoError(t, migrateVault(context.Background(), db))
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM vault_migrations WHERE name='0004_add_chunk_fts'`).Scan(&cnt))
	assert.Equal(t, 1, cnt)

	// Each indexer-logic change that must re-index archived sessions ships with an
	// index-version bump: every session archived before it reads as below-current so
	// `capy vault reindex` rebuilds its FTS. v3 added chunk FTS (migration 0004),
	// v4 added generic/MCP tool-input summaries (issue #89), and v5 added Claude
	// torn-line recovery. Bump this assertion deliberately with the constant.
	assert.Equal(t, 5, currentIndexVersion, "Claude torn-line recovery requires the v5 index-version bump")
}

func TestMigrate0004_CreatesChunkTablesOnLegacyVault(t *testing.T) {
	// A pre-feature vault lacks the chunk tables. Build that legacy shape
	// directly (plaintext DB — encryption is irrelevant to the migration logic)
	// and verify the migration creates both tables.
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "legacy.db"))
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE vault_sessions (uuid TEXT PRIMARY KEY, content_hash TEXT NOT NULL, raw_jsonl BLOB NOT NULL);
		CREATE TABLE vault_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
		CREATE TABLE vault_migrations (name TEXT PRIMARY KEY, applied_at TEXT DEFAULT CURRENT_TIMESTAMP);
	`)
	require.NoError(t, err)

	require.NoError(t, migrate0004AddChunkFTS(context.Background(), db))

	for _, table := range []string{"vault_chunks", "vault_chunks_trigram"} {
		var cnt int
		//nolint:gosec // table is a test-controlled constant, never user input
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&cnt))
		assert.Equal(t, 0, cnt, "%s must exist and start empty (backfilled by reindex)", table)
	}

	var cnt int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM vault_migrations WHERE name='0004_add_chunk_fts'`).Scan(&cnt))
	assert.Equal(t, 1, cnt)

	// Idempotent re-run: the guard short-circuits, no duplicate-table error.
	require.NoError(t, migrate0004AddChunkFTS(context.Background(), db))
}

func TestMigrateVault_FreshDBHasSessionNames(t *testing.T) {
	s := newTestVault(t)
	db, err := s.getDB(context.Background())
	require.NoError(t, err)

	_, err = db.Exec(`SELECT session_uuid, custom_title, renamed_at_ns, machine_id
		FROM vault_session_names WHERE 0`)
	require.NoError(t, err, "fresh vault must expose the complete session-name schema")

	var count int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM vault_migrations WHERE name = '0005_session_names'`).Scan(&count))
	assert.Equal(t, 1, count)

	require.NoError(t, migrateVault(context.Background(), db))
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM vault_migrations WHERE name = '0005_session_names'`).Scan(&count))
	assert.Equal(t, 1, count, "migration rerun must not duplicate its record")
}

func TestMigrate0005_CreatesSessionNamesOnLegacyVault(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "legacy.db"))
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec(`
		PRAGMA foreign_keys = ON;
		CREATE TABLE vault_sessions (uuid TEXT PRIMARY KEY, content_hash TEXT NOT NULL, raw_jsonl BLOB NOT NULL);
		CREATE TABLE vault_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
		CREATE TABLE vault_migrations (name TEXT PRIMARY KEY, applied_at TEXT DEFAULT CURRENT_TIMESTAMP);
		INSERT INTO vault_sessions (uuid, content_hash, raw_jsonl) VALUES ('legacy', 'h', x'7b7d');
	`)
	require.NoError(t, err)

	require.NoError(t, migrate0005AddSessionNames(context.Background(), db))
	_, err = db.Exec(`INSERT INTO vault_session_names
		(session_uuid, custom_title, renamed_at_ns, machine_id)
		VALUES ('legacy', 'Legacy name', 1, 'machine-a')`)
	require.NoError(t, err)

	var count int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM vault_migrations WHERE name = '0005_session_names'`).Scan(&count))
	assert.Equal(t, 1, count)

	require.NoError(t, migrate0005AddSessionNames(context.Background(), db))
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM vault_migrations WHERE name = '0005_session_names'`).Scan(&count))
	assert.Equal(t, 1, count, "migration rerun must be idempotent")

	_, err = db.Exec(`DELETE FROM vault_sessions WHERE uuid = 'legacy'`)
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM vault_session_names`).Scan(&count))
	assert.Equal(t, 0, count, "legacy-migrated table must cascade with its parent")
}

// pre0006SessionsDDL is the vault_sessions shape a 0005-era binary wrote: every
// column up to `encoding`, no platform / parent_uuid, no idx_sessions_parent.
const pre0006SessionsDDL = `
	CREATE TABLE vault_sessions (
	  uuid TEXT PRIMARY KEY, title TEXT, start_time DATETIME, end_time DATETIME,
	  message_count INTEGER NOT NULL DEFAULT 0, size_bytes INTEGER NOT NULL DEFAULT 0,
	  content_hash TEXT NOT NULL, machine_id TEXT NOT NULL, claude_project_dir TEXT NOT NULL,
	  project_path TEXT NOT NULL, git_branch TEXT, archived_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	  index_version INTEGER NOT NULL DEFAULT 1, raw_jsonl BLOB NOT NULL, encoding TEXT
	);`

// TestMigrate0006_AddsColumnsAndIndexToLegacyVault runs the migration directly
// against a plaintext pre-0006 table: both columns are added with their defaults,
// the index is created AFTER them, the record lands, and a rerun is a no-op.
func TestMigrate0006_AddsColumnsAndIndexToLegacyVault(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "legacy.db"))
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec(pre0006SessionsDDL + `
		CREATE TABLE vault_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
		CREATE TABLE vault_migrations (name TEXT PRIMARY KEY, applied_at TEXT DEFAULT CURRENT_TIMESTAMP);
		INSERT INTO vault_sessions (uuid, content_hash, machine_id, claude_project_dir, project_path, raw_jsonl)
		VALUES ('legacy', 'h', 'm', '-d', '/p', x'7b7d');
	`)
	require.NoError(t, err)

	require.NoError(t, migrate0006AddPlatform(context.Background(), db))

	var platform string
	var parent sql.NullString
	require.NoError(t, db.QueryRow(`SELECT platform, parent_uuid FROM vault_sessions WHERE uuid='legacy'`).Scan(&platform, &parent))
	assert.Equal(t, string(PlatformClaudeCode), platform, "pre-existing rows read as Claude through the column default")
	assert.False(t, parent.Valid, "pre-existing rows have no parent (NULL)")

	assertParentIndex(t, db)

	var cnt int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM vault_migrations WHERE name='0006_platform'`).Scan(&cnt))
	assert.Equal(t, 1, cnt)

	// Idempotent re-run: the guard short-circuits, no duplicate-column error.
	require.NoError(t, migrate0006AddPlatform(context.Background(), db))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM vault_migrations WHERE name='0006_platform'`).Scan(&cnt))
	assert.Equal(t, 1, cnt)
}

// TestMigrate0006_LegacyVaultOpensThroughStore is the end-to-end legacy path: an
// encrypted pre-0006 vault (0005 shape, migrations recorded) is opened through
// VaultStore — schemaSQL runs first and must NOT trip over the missing column
// (the reason idx_sessions_parent lives only in the migration) — and afterwards
// the row reads with the defaults, the index exists, and the store's own
// statements (GetSession / ListSessions / Insert) work against the migrated shape.
func TestMigrate0006_LegacyVaultOpensThroughStore(t *testing.T) {
	const key = "legacy-vault-key-at-least-32-characters!!"
	t.Setenv(vaultKeyEnv, key)
	path := filepath.Join(t.TempDir(), "legacy.db")

	legacy, err := sql.Open("sqlite3", sqliteutil.EncryptedDSN(path, key)+"&_busy_timeout=5000")
	require.NoError(t, err)
	_, err = legacy.Exec(pre0006SessionsDDL + `
		CREATE TABLE vault_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
		CREATE TABLE vault_migrations (name TEXT PRIMARY KEY, applied_at TEXT DEFAULT CURRENT_TIMESTAMP);
		INSERT INTO vault_migrations (name) VALUES
		  ('0001_blob_encoding'), ('0003_add_index_version'), ('0004_add_chunk_fts'), ('0005_session_names');
		INSERT INTO vault_sessions (uuid, title, message_count, content_hash, machine_id, claude_project_dir, project_path, raw_jsonl)
		VALUES ('1e9ac100-0000-0000-0000-000000000001', 'legacy row', 1, 'h', 'm', '-home-user-proj', '/home/user/proj', x'7b7d');
	`)
	require.NoError(t, err)
	require.NoError(t, legacy.Close())

	s := NewVaultStore(path)
	t.Cleanup(func() { _ = s.Close() })
	require.NoError(t, s.Open(context.Background()), "a pre-0006 vault must open (schemaSQL before migrateVault)")

	db, err := s.getDB(context.Background())
	require.NoError(t, err)
	assertParentIndex(t, db)
	var cnt int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM vault_migrations WHERE name='0006_platform'`).Scan(&cnt))
	assert.Equal(t, 1, cnt)

	got, err := s.GetSession(context.Background(), "1e9ac100-0000-0000-0000-000000000001")
	require.NoError(t, err)
	assert.Equal(t, PlatformClaudeCode, got.Platform)
	assert.Empty(t, got.ParentUUID)
	assert.Equal(t, "legacy row", got.Title)

	listed, err := s.ListSessions(context.Background(), ListOptions{})
	require.NoError(t, err)
	require.Len(t, listed, 1, "the parent_uuid IS NULL default keeps a migrated legacy row listed")

	// The prepared INSERT (which names platform/parent_uuid) works against the
	// migrated table, and a Codex row is accepted.
	rec := sampleRecord("1e9ac100-0000-0000-0000-000000000002")
	rec.Session.Platform = PlatformCodex
	require.NoError(t, s.InsertSession(context.Background(), rec))
	st, err := s.Stats(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []PlatformStat{
		{Platform: PlatformClaudeCode, Sessions: 1, Bytes: 0},
		{Platform: PlatformCodex, Sessions: 1, Bytes: rec.Session.SizeBytes},
	}, st.ByPlatform)
}

// TestMigrateVault_FreshDBHasPlatformColumns: a fresh vault gets the two columns
// from schemaSQL (no ALTER), the index from the migration, and the record.
func TestMigrateVault_FreshDBHasPlatformColumns(t *testing.T) {
	s := newTestVault(t)
	db, err := s.getDB(context.Background())
	require.NoError(t, err)

	_, err = db.Exec(`SELECT platform, parent_uuid FROM vault_sessions WHERE 0`)
	require.NoError(t, err, "platform/parent_uuid must exist on a fresh vault")

	// Column contract as declared: NOT NULL DEFAULT 'claude-code' / nullable.
	rows, err := db.Query(`PRAGMA table_info(vault_sessions)`)
	require.NoError(t, err)
	defer rows.Close()
	seen := map[string]struct {
		notNull int
		dflt    sql.NullString
	}{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var dflt sql.NullString
		require.NoError(t, rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk))
		seen[name] = struct {
			notNull int
			dflt    sql.NullString
		}{notNull, dflt}
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, 1, seen["platform"].notNull)
	assert.Equal(t, `'claude-code'`, seen["platform"].dflt.String)
	assert.Equal(t, 0, seen["parent_uuid"].notNull)
	assert.False(t, seen["parent_uuid"].dflt.Valid)

	assertParentIndex(t, db)

	var cnt int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM vault_migrations WHERE name='0006_platform'`).Scan(&cnt))
	assert.Equal(t, 1, cnt)

	require.NoError(t, migrateVault(context.Background(), db))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM vault_migrations WHERE name='0006_platform'`).Scan(&cnt))
	assert.Equal(t, 1, cnt, "migration rerun must not duplicate its record")
}

// TestSchemaSQL_HasNoParentIndex pins the ordering hazard mechanically: openDB
// runs schemaSQL BEFORE migrateVault, so an index over parent_uuid in schemaSQL
// would fail every open of a legacy vault. The index belongs to migration 0006
// only (the `grep idx_sessions_parent store.go` gate from the plan, in code).
func TestSchemaSQL_HasNoParentIndex(t *testing.T) {
	assert.NotContains(t, schemaSQL, "idx_sessions_parent")
	assert.NotContains(t, schemaSQL, "parent_uuid)", "no index or constraint over parent_uuid may live in schemaSQL")
}

func assertParentIndex(t *testing.T, db *sql.DB) {
	t.Helper()
	var tbl string
	err := db.QueryRow(`SELECT tbl_name FROM sqlite_master WHERE type='index' AND name='idx_sessions_parent'`).Scan(&tbl)
	require.NoError(t, err, "idx_sessions_parent must exist")
	assert.Equal(t, "vault_sessions", tbl)
}

func TestChunkFTSTokenizers(t *testing.T) {
	// Prove the two layers actually tokenize differently — a typo in the
	// tokenize= argument would otherwise surface only at search time (Task 5).
	s := newTestVault(t)
	db, err := s.getDB(context.Background())
	require.NoError(t, err)

	for _, table := range []string{"vault_chunks", "vault_chunks_trigram"} {
		//nolint:gosec // table is a test-controlled constant, never user input
		_, err = db.Exec(`INSERT INTO ` + table +
			` (title, content_text, session_uuid, subagent_id, chunk_index, first_line_index)
			VALUES ('t', 'authentication middleware', 'u1', '', 0, 0)`)
		require.NoError(t, err)
	}

	countMatches := func(table, query string) int {
		t.Helper()
		var cnt int
		//nolint:gosec // table is a test-controlled constant, never user input
		require.NoError(t, db.QueryRow(
			`SELECT COUNT(*) FROM `+table+` WHERE `+table+` MATCH ?`, query).Scan(&cnt))
		return cnt
	}

	// Porter layer stems: "authenticated" and "authentication" share a stem.
	assert.Equal(t, 1, countMatches("vault_chunks", `"authenticated"`),
		"porter layer must stem-match")
	// Trigram layer matches substrings; porter does not.
	assert.Equal(t, 1, countMatches("vault_chunks_trigram", `"thentica"`),
		"trigram layer must substring-match")
	assert.Equal(t, 0, countMatches("vault_chunks", `"thentica"`),
		"porter layer must not substring-match")
}

func TestColumnExists(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "c.db"))
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE t (a INTEGER, b TEXT)`)
	require.NoError(t, err)

	tx, err := db.Begin()
	require.NoError(t, err)
	defer tx.Rollback() //nolint:errcheck

	has, err := columnExists(context.Background(), tx, "t", "b")
	require.NoError(t, err)
	assert.True(t, has)

	has, err = columnExists(context.Background(), tx, "t", "missing")
	require.NoError(t, err)
	assert.False(t, has)
}

func TestSessionProject_MigrationFreshLegacyParity(t *testing.T) {
	// Build the reference outside subtests so /legacy runs independently.
	fresh := newTestVault(t)
	freshDB, err := fresh.getDB(t.Context())
	require.NoError(t, err)
	var freshDDL string
	require.NoError(t, freshDB.QueryRowContext(t.Context(),
		`SELECT sql FROM sqlite_master WHERE name = 'vault_session_projects'`).Scan(&freshDDL))
	for _, mode := range []string{"fresh", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv(vaultKeyEnv, testVaultKey)
			t.Setenv("CAPY_MACHINE_ID", "project-test-writer")
			ctx := t.Context()
			path := filepath.Join(t.TempDir(), "vault.db")
			const uuid = "aaaaaaaa-1111-2222-3333-444444444444"
			if mode == "legacy" {
				legacy, err := sql.Open("sqlite3", sqliteutil.EncryptedDSN(path, testVaultKey))
				require.NoError(t, err)
				t.Cleanup(func() { _ = legacy.Close() })
				// The complete pre-project shape, including prior migration records.
				_, err = legacy.ExecContext(ctx, strings.Replace(schemaSQL, sessionProjectsTableSQL, "", 1))
				require.NoError(t, err)
				require.NoError(t, ensureVaultMigrationsTable(ctx, legacy))
				_, err = legacy.ExecContext(ctx, `INSERT INTO vault_migrations (name) VALUES
					('0001_blob_encoding'), ('0003_add_index_version'), ('0004_add_chunk_fts'),
					('0005_session_names'), ('0006_platform');
					INSERT INTO vault_sessions (uuid, content_hash, machine_id, claude_project_dir, project_path, raw_jsonl)
					VALUES ('aaaaaaaa-1111-2222-3333-444444444444', 'hash', 'importer', '-original', '/original', x'7b7d')`)
				require.NoError(t, err)
				var count int
				require.NoError(t, legacy.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name = 'vault_session_projects'`).Scan(&count))
				assert.Zero(t, count)
				// Exercise migration creation directly: schemaSQL must not hide a missing DDL.
				require.NoError(t, migrate0007AddSessionProjects(ctx, legacy))
				require.NoError(t, legacy.Close())
			}
			s := NewVaultStore(path)
			t.Cleanup(func() { _ = s.Close() })
			require.NoError(t, s.Open(ctx))
			if mode == "fresh" {
				require.NoError(t, s.InsertSession(ctx, sampleRecord(uuid)))
			}
			db, err := s.getDB(ctx)
			require.NoError(t, err)
			var ddl string
			require.NoError(t, db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE name = 'vault_session_projects'`).Scan(&ddl))
			assert.Equal(t, freshDDL, ddl)
			neverEdited, err := s.GetSession(ctx, uuid)
			require.NoError(t, err)
			assert.Nil(t, neverEdited.ProjectOverride, "migration must not invent overrides")

			// Both creation paths enforce the same storage constraints.
			for _, tt := range []struct {
				name, query string
				args        []any
			}{
				{name: "empty override", query: `INSERT INTO vault_session_projects VALUES (?, '', 1, 'writer')`, args: []any{uuid}},
				{name: "null timestamp", query: `INSERT INTO vault_session_projects VALUES (?, 'label', NULL, 'writer')`, args: []any{uuid}},
				{name: "null writer", query: `INSERT INTO vault_session_projects VALUES (?, 'label', 1, NULL)`, args: []any{uuid}},
				{name: "orphan", query: `INSERT INTO vault_session_projects VALUES ('missing', 'label', 1, 'writer')`},
			} {
				t.Run(tt.name, func(t *testing.T) {
					_, err := db.ExecContext(ctx, tt.query, tt.args...)
					require.Error(t, err)
				})
			}
			assigned, err := s.SetSessionProject(ctx, uuid, ProjectOptions{Name: "persisted"})
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, `INSERT INTO vault_session_projects VALUES (?, 'duplicate', 1, 'writer')`, uuid)
			require.Error(t, err, "one project state per UUID")
			require.NoError(t, s.Close())
			require.NoError(t, s.Open(ctx))
			got, err := s.GetSession(ctx, uuid)
			require.NoError(t, err)
			assert.Equal(t, assigned.ProjectOverride, got.ProjectOverride)
			cleared, err := s.SetSessionProject(ctx, uuid, ProjectOptions{Clear: true})
			require.NoError(t, err)
			require.NoError(t, s.Close())
			require.NoError(t, s.Open(ctx))
			got, err = s.GetSession(ctx, uuid)
			require.NoError(t, err)
			require.NotNil(t, got.ProjectOverride)
			assert.Equal(t, cleared.ProjectOverride, got.ProjectOverride)
			assert.Nil(t, got.ProjectOverride.CustomProject)
			assert.Equal(t, neverEdited.ProjectPath, got.EffectiveProject())
			db, err = s.getDB(ctx)
			require.NoError(t, err)
			require.NoError(t, migrateVault(ctx, db))
			var count int
			require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM vault_migrations WHERE name = '0007_session_projects'`).Scan(&count))
			assert.Equal(t, 1, count)
			deleted, err := s.DeleteSession(ctx, uuid)
			require.NoError(t, err)
			require.True(t, deleted)
			require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM vault_session_projects`).Scan(&count))
			assert.Zero(t, count, "deletion must cascade the project tombstone")
		})
	}
}
