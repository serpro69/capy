package vault

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/sqliteutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mergeRecord builds a SessionRecord whose RawJSONL is real, scannable session
// JSONL carrying a unique `token` (so a post-merge FTS search can prove the
// content was re-scanned into the destination). content_hash and size_bytes are
// set explicitly so a test can drive the larger-wins idempotent decision, and the
// location columns are distinct so "carried verbatim" is verifiable.
func mergeRecord(t *testing.T, uuid, token string, msgCount int, size int64, hash, machineID, projectPath string) *SessionRecord {
	t.Helper()
	// `token` appears in exactly ONE indexed message (the user line) so a keyword
	// search yields exactly one hit; the assistant text is token-free filler.
	main := jsonlBytes(t,
		userLine("u1", projectPath, "main", "marker "+token),
		assistantLine("a1", "m1", []map[string]any{{"type": "text", "text": "acknowledged"}}),
		aiTitleLine("session "+uuid),
	)
	return &SessionRecord{
		Session: Session{
			UUID:             uuid,
			Title:            "title " + token,
			StartTime:        time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC),
			EndTime:          time.Date(2026, 5, 1, 11, 0, 0, 0, time.UTC),
			MessageCount:     msgCount,
			SizeBytes:        size,
			ContentHash:      hash,
			MachineID:        machineID,
			ClaudeProjectDir: "-src-proj",
			ProjectPath:      projectPath,
			GitBranch:        "main",
			IndexVersion:     currentIndexVersion,
			RawJSONL:         main,
		},
		// FTS the store indexes for THIS record verbatim (InsertSession does not
		// re-scan). It mirrors the single-token user line so a pre-populated
		// destination row is searchable by `token`; for a merge SOURCE this index
		// is irrelevant — merge re-scans RawJSONL, not the source's stored FTS.
		FTS: []FTSRow{{SessionUUID: uuid, Role: "user", ContentText: "marker " + token}},
	}
}

// buildVault opens (creating) a vault at path under key, inserts recs, and closes
// it — leaving a clean, checkpointed on-disk vault for use as a merge source or
// destination. It sets CAPY_VAULT_KEY to key for the duration of the inserts.
func buildVault(t *testing.T, path, key string, recs ...*SessionRecord) {
	t.Helper()
	t.Setenv(vaultKeyEnv, key)
	s := NewVaultStore(path)
	for _, r := range recs {
		require.NoError(t, s.InsertSession(context.Background(), r))
	}
	require.NoError(t, s.Close())
}

// openDest sets CAPY_VAULT_KEY to key and returns an open destination vault.
func openDest(t *testing.T, path, key string) *VaultStore {
	t.Helper()
	t.Setenv(vaultKeyEnv, key)
	s := NewVaultStore(path)
	t.Cleanup(func() { _ = s.Close() })
	require.NoError(t, s.Open(context.Background()))
	return s
}

// TestMergeFrom_DistinctAndLargerWins is the headline path: distinct source
// sessions are added, a larger source variant of an overlapping UUID replaces the
// destination's, a smaller source variant is skipped, and re-scanned FTS + carried
// metadata are correct. Uses DIFFERENT keys for source and destination to exercise
// the explicit-source-key plumbing.
func TestMergeFrom_DistinctAndLargerWins(t *testing.T) {
	const srcKey = "source-vault-key-at-least-32-characters!!"
	const destKey = "dest-vault-key-at-least-32-characters-ab!"
	ctx := context.Background()
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.db")
	destPath := filepath.Join(dir, "dest.db")

	const (
		uuidA = "aaaaaaaa-0000-0000-0000-000000000001" // overlap, source larger → updated
		uuidB = "bbbbbbbb-0000-0000-0000-000000000002" // source-only → new
		uuidC = "cccccccc-0000-0000-0000-000000000003" // overlap, source smaller → skipped
	)

	// Source (its own key): A large, B distinct, C small. Source machine/path differ.
	buildVault(t, srcPath, srcKey,
		mergeRecord(t, uuidA, "srcAonly", 4, 2000, "srcA", "machine-src", "/src/projA"),
		mergeRecord(t, uuidB, "srcBonly", 3, 1500, "srcB", "machine-src", "/src/projB"),
		mergeRecord(t, uuidC, "srcConly", 2, 100, "srcC", "machine-src", "/src/projC"),
	)

	// Destination (its own key): A small (will lose), C large (will win). No B.
	buildVault(t, destPath, destKey,
		mergeRecord(t, uuidA, "destAonly", 2, 500, "destA", "machine-dest", "/dest/projA"),
		mergeRecord(t, uuidC, "destConly", 5, 3000, "destC", "machine-dest", "/dest/projC"),
	)

	dest := openDest(t, destPath, destKey)
	res, err := MergeFrom(ctx, dest, srcPath, srcKey, "--key", MergeOptions{})
	require.NoError(t, err)

	assert.Equal(t, 1, res.Imported, "B is the only brand-new session")
	assert.Equal(t, 1, res.Updated, "A is replaced by the larger source variant")
	assert.Equal(t, 1, res.Skipped, "C's larger destination copy is kept")
	assert.Equal(t, 0, res.Errors)

	// A was replaced: it now holds the source's content + metadata, and its FTS was
	// rebuilt (the old dest-only token is gone, the source token is searchable).
	gotA, err := dest.GetSession(ctx, uuidA)
	require.NoError(t, err)
	assert.Equal(t, "machine-src", gotA.MachineID, "source machine_id carried verbatim")
	assert.Equal(t, "/src/projA", gotA.ProjectPath, "source project_path carried verbatim (not recomputed)")
	assert.Equal(t, "srcA", gotA.ContentHash)
	assert.Equal(t, int64(2000), gotA.SizeBytes)
	assertSearchCount(t, dest, "srcAonly", 1)
	assertSearchCount(t, dest, "destAonly", 0)

	// B is brand-new from the source.
	gotB, err := dest.GetSession(ctx, uuidB)
	require.NoError(t, err)
	assert.Equal(t, "machine-src", gotB.MachineID)
	assert.Equal(t, "/src/projB", gotB.ProjectPath)
	assertSearchCount(t, dest, "srcBonly", 1)

	// C kept the destination's larger copy untouched.
	gotC, err := dest.GetSession(ctx, uuidC)
	require.NoError(t, err)
	assert.Equal(t, "machine-dest", gotC.MachineID, "smaller source variant must not overwrite C")
	assert.Equal(t, int64(3000), gotC.SizeBytes)
	assertSearchCount(t, dest, "destConly", 1)
	assertSearchCount(t, dest, "srcConly", 0)
}

// TestMergeFrom_Idempotent proves a second merge of the same source is a clean
// no-op: everything is skipped (same content_hash) and nothing changes.
func TestMergeFrom_Idempotent(t *testing.T) {
	const key = "shared-vault-key-at-least-32-characters!!"
	ctx := context.Background()
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.db")
	destPath := filepath.Join(dir, "dest.db")

	buildVault(t, srcPath, key,
		mergeRecord(t, "11111111-0000-0000-0000-000000000001", "tokone", 3, 1000, "h1", "machine-src", "/src/p1"),
		mergeRecord(t, "22222222-0000-0000-0000-000000000002", "toktwo", 3, 1000, "h2", "machine-src", "/src/p2"),
	)

	dest := openDest(t, destPath, key)
	first, err := MergeFrom(ctx, dest, srcPath, key, "CAPY_VAULT_KEY", MergeOptions{})
	require.NoError(t, err)
	assert.Equal(t, 2, first.Imported)

	second, err := MergeFrom(ctx, dest, srcPath, key, "CAPY_VAULT_KEY", MergeOptions{})
	require.NoError(t, err)
	assert.Equal(t, 0, second.Imported)
	assert.Equal(t, 0, second.Updated)
	assert.Equal(t, 2, second.Skipped, "a re-merge of identical content skips everything")
}

// TestMergeFrom_DryRunWritesNothing proves --dry-run reports the decision but
// leaves the destination empty.
func TestMergeFrom_DryRunWritesNothing(t *testing.T) {
	const key = "shared-vault-key-at-least-32-characters!!"
	ctx := context.Background()
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.db")
	destPath := filepath.Join(dir, "dest.db")

	buildVault(t, srcPath, key,
		mergeRecord(t, "33333333-0000-0000-0000-000000000003", "dryrun", 3, 1000, "h3", "machine-src", "/src/p3"),
	)

	dest := openDest(t, destPath, key)
	res, err := MergeFrom(ctx, dest, srcPath, key, "CAPY_VAULT_KEY", MergeOptions{DryRun: true})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Imported, "dry run still reports what it would import")

	sessions, err := dest.ListSessions(ctx, ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, sessions, "dry run must not write to the destination")
}

// TestMergeFrom_ExcludesEmptySource proves a 0-message source session is excluded
// (the Task-11 guard), not carried into the destination.
func TestMergeFrom_ExcludesEmptySource(t *testing.T) {
	const key = "shared-vault-key-at-least-32-characters!!"
	ctx := context.Background()
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.db")
	destPath := filepath.Join(dir, "dest.db")

	buildVault(t, srcPath, key,
		mergeRecord(t, "44444444-0000-0000-0000-000000000004", "empty", 0, 200, "h4", "machine-src", "/src/p4"),
		mergeRecord(t, "55555555-0000-0000-0000-000000000005", "real", 3, 1000, "h5", "machine-src", "/src/p5"),
	)

	dest := openDest(t, destPath, key)
	res, err := MergeFrom(ctx, dest, srcPath, key, "CAPY_VAULT_KEY", MergeOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Imported, "only the non-empty session is merged")
	assert.Equal(t, 1, res.Excluded, "the 0-message session is excluded")

	_, err = dest.GetSession(ctx, "44444444-0000-0000-0000-000000000004")
	assert.ErrorIs(t, err, ErrSessionNotFound, "the empty session must be absent")
}

// TestMergeFrom_ProjectFilter restricts the merge to one mangled project dir.
func TestMergeFrom_ProjectFilter(t *testing.T) {
	const key = "shared-vault-key-at-least-32-characters!!"
	ctx := context.Background()
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.db")
	destPath := filepath.Join(dir, "dest.db")

	// Both records share ClaudeProjectDir "-src-proj" (set by mergeRecord), so a
	// filter that matches it brings in both; a non-matching filter brings none.
	buildVault(t, srcPath, key,
		mergeRecord(t, "66666666-0000-0000-0000-000000000006", "filt1", 3, 1000, "h6", "machine-src", "/src/p6"),
		mergeRecord(t, "77777777-0000-0000-0000-000000000007", "filt2", 3, 1000, "h7", "machine-src", "/src/p7"),
	)

	dest := openDest(t, destPath, key)
	none, err := MergeFrom(ctx, dest, srcPath, key, "CAPY_VAULT_KEY", MergeOptions{Project: "nonexistent"})
	require.NoError(t, err)
	assert.Equal(t, 0, none.Imported, "a non-matching project filter merges nothing")

	some, err := MergeFrom(ctx, dest, srcPath, key, "CAPY_VAULT_KEY", MergeOptions{Project: "src-proj"})
	require.NoError(t, err)
	assert.Equal(t, 2, some.Imported, "a matching project filter merges the project's sessions")
}

// buildV1Source writes a v1-shaped encrypted source vault: vault_sessions and
// vault_files WITHOUT the `encoding` column (it predates migration 0001), and an
// empty vault_meta (no min_reader_version marker). It is the regression fixture
// for "a v1 source merges cleanly" — a blind SELECT encoding would raise "no such
// column" against it.
func buildV1Source(t *testing.T, path, key, uuid, token string) []byte {
	t.Helper()
	dsn := sqliteutil.EncryptedDSN(path, key) + "&_busy_timeout=5000"
	db, err := sql.Open("sqlite3", dsn)
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE vault_sessions (
		  uuid TEXT PRIMARY KEY, title TEXT, start_time DATETIME, end_time DATETIME,
		  message_count INTEGER NOT NULL DEFAULT 0, size_bytes INTEGER NOT NULL DEFAULT 0,
		  content_hash TEXT NOT NULL, machine_id TEXT NOT NULL, claude_project_dir TEXT NOT NULL,
		  project_path TEXT NOT NULL, git_branch TEXT, archived_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		  index_version INTEGER NOT NULL DEFAULT 1, raw_jsonl BLOB NOT NULL
		);
		CREATE TABLE vault_files (
		  session_uuid TEXT NOT NULL, relative_path TEXT NOT NULL, raw_content BLOB NOT NULL,
		  PRIMARY KEY (session_uuid, relative_path)
		);
		CREATE TABLE vault_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
	`)
	require.NoError(t, err)

	main := jsonlBytes(t,
		userLine("u1", "/v1/proj", "main", "marker "+token),
		assistantLine("a1", "m1", []map[string]any{{"type": "text", "text": "ack"}}),
		aiTitleLine("v1 title"),
	)
	_, err = db.Exec(`INSERT INTO vault_sessions
		(uuid, title, message_count, size_bytes, content_hash, machine_id, claude_project_dir,
		 project_path, git_branch, index_version, raw_jsonl)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?)`,
		uuid, "v1 title", 3, int64(len(main)), "v1hash", "machine-v1", "-v1-proj", "/v1/proj", "main", main)
	require.NoError(t, err)
	// A raw sidecar with no encoding column — must read back as raw.
	_, err = db.Exec(`INSERT INTO vault_files (session_uuid, relative_path, raw_content) VALUES (?, ?, ?)`,
		uuid, "tool-results/out.txt", []byte("v1 sidecar contents "+token))
	require.NoError(t, err)
	return main
}

// TestMergeFrom_V1ShapedSourceMergesCleanly proves a source predating the encoding
// column (and the min_reader_version marker) merges without "no such column":
// its blobs read as raw, and content + search round-trip.
func TestMergeFrom_V1ShapedSourceMergesCleanly(t *testing.T) {
	const key = "shared-vault-key-at-least-32-characters!!"
	ctx := context.Background()
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "v1source.db")
	destPath := filepath.Join(dir, "dest.db")

	const uuid = "88888888-0000-0000-0000-000000000008"
	mainBytes := buildV1Source(t, srcPath, key, uuid, "v1token")

	dest := openDest(t, destPath, key)
	res, err := MergeFrom(ctx, dest, srcPath, key, "CAPY_VAULT_KEY", MergeOptions{})
	require.NoError(t, err, "a v1-shaped source must merge cleanly (no 'no such column')")
	assert.Equal(t, 1, res.Imported)
	assert.Equal(t, 0, res.Errors)

	got, err := dest.GetSession(ctx, uuid)
	require.NoError(t, err)
	assert.Equal(t, "machine-v1", got.MachineID, "v1 source metadata carried")
	assert.Equal(t, "/v1/proj", got.ProjectPath)
	assert.True(t, bytes.Equal(mainBytes, got.RawJSONL), "v1 raw blob round-trips through merge")

	files, err := dest.GetFiles(ctx, uuid)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "tool-results/out.txt", files[0].RelativePath)
	assert.Equal(t, "v1 sidecar contents v1token", string(files[0].RawContent), "v1 raw sidecar round-trips")

	assertSearchCount(t, dest, "v1token", 1)
}

// TestMergeFrom_MissingSourceKeyFails proves an explicitly wrong source key is a
// clear error, not a silent empty merge.
func TestMergeFrom_WrongSourceKeyFails(t *testing.T) {
	const key = "shared-vault-key-at-least-32-characters!!"
	const wrongKey = "wrong-vault-key-at-least-32-characters!!!"
	ctx := context.Background()
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.db")
	destPath := filepath.Join(dir, "dest.db")

	buildVault(t, srcPath, key,
		mergeRecord(t, "99999999-0000-0000-0000-000000000009", "wrongkey", 3, 1000, "h9", "machine-src", "/src/p9"),
	)

	dest := openDest(t, destPath, key)
	_, err := MergeFrom(ctx, dest, srcPath, wrongKey, "--key", MergeOptions{})
	require.Error(t, err, "a wrong source key must fail")
	assert.True(t, sqliteutil.IsWrongPassphrase(err), "wrong source key should yield WrongPassphraseError, got: %v", err)
}

// assertSearchCount asserts a plain-keyword search returns exactly want hits.
func assertSearchCount(t *testing.T, s *VaultStore, query string, want int) {
	t.Helper()
	hits, err := s.Search(context.Background(), SearchOptions{Query: query})
	require.NoError(t, err)
	assert.Lenf(t, hits, want, "search %q expected %d hit(s), got %d", query, want, len(hits))
}
