package vault

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"sync"
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

// buildPre0006Source writes an encrypted source vault in the 0005 shape — the
// `encoding` column present, NO platform / parent_uuid columns — holding one
// session whose FIRST LINE is a file-history-snapshot (41 of 467 real Claude
// sessions open that way). A merge that sniffed the first line of an
// absent-column source could misjudge such rows; the contract is "absent ⇒
// Claude, no sniff".
func buildPre0006Source(t *testing.T, path, key, uuid, token string) []byte {
	t.Helper()
	dsn := sqliteutil.EncryptedDSN(path, key) + "&_busy_timeout=5000"
	db, err := sql.Open("sqlite3", dsn)
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec(pre0006SessionsDDL + `
		CREATE TABLE vault_files (
		  session_uuid TEXT NOT NULL, relative_path TEXT NOT NULL, raw_content BLOB NOT NULL, encoding TEXT,
		  PRIMARY KEY (session_uuid, relative_path)
		);
		CREATE TABLE vault_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
		INSERT INTO vault_meta (key, value) VALUES ('min_reader_version', '2');
	`)
	require.NoError(t, err)

	main := jsonlBytes(t,
		map[string]any{"type": "file-history-snapshot", "messageId": "x", "snapshot": map[string]any{"messageId": "x", "trackedFileBackups": map[string]any{}}, "isSnapshotUpdate": false},
		userLine("u1", "/pre/proj", "main", "marker "+token),
		assistantLine("a1", "m1", []map[string]any{{"type": "text", "text": "ack"}}),
		aiTitleLine("pre-0006 title"),
	)
	_, err = db.Exec(`INSERT INTO vault_sessions
		(uuid, title, message_count, size_bytes, content_hash, machine_id, claude_project_dir,
		 project_path, git_branch, index_version, raw_jsonl, encoding)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, 'raw')`,
		uuid, "pre-0006 title", 3, int64(len(main)), "prehash", "machine-pre", "-pre-proj", "/pre/proj", "main", main)
	require.NoError(t, err)
	return main
}

// TestMergeFrom_Pre0006SourceMergesAsClaudeWithoutSniff: a source lacking the
// platform / parent_uuid columns merges completely, every row lands as a
// top-level claude-code session, and the file-history-snapshot first line is
// never consulted (it would be undetectable as anything but Claude anyway, but
// the point is the merge must not error or route on it).
func TestMergeFrom_Pre0006SourceMergesAsClaudeWithoutSniff(t *testing.T) {
	const key = "shared-vault-key-at-least-32-characters!!"
	ctx := context.Background()
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "pre0006.db")
	destPath := filepath.Join(dir, "dest.db")

	const uuid = "9e000600-0000-0000-0000-000000000006"
	mainBytes := buildPre0006Source(t, srcPath, key, uuid, "pretoken")
	firstLine, _, _ := bytes.Cut(mainBytes, []byte("\n"))
	require.Contains(t, string(firstLine), `"type":"file-history-snapshot"`, "fixture: first line is a snapshot")
	detected, err := DetectFormat(mainBytes)
	require.NoError(t, err)
	require.Equal(t, PlatformClaudeCode, detected, "fixture: the snapshot line is Claude-shaped for the sniff too")

	dest := openDest(t, destPath, key)
	res, err := MergeFrom(ctx, dest, srcPath, key, "CAPY_VAULT_KEY", MergeOptions{})
	require.NoError(t, err, "a pre-0006 source must merge cleanly (no 'no such column: platform')")
	assert.Equal(t, 1, res.Imported)
	assert.Equal(t, 0, res.Errors)

	got, err := dest.GetSession(ctx, uuid)
	require.NoError(t, err)
	assert.Equal(t, PlatformClaudeCode, got.Platform, "absent platform column ⇒ Claude, no sniff")
	assert.Empty(t, got.ParentUUID, "absent parent_uuid column ⇒ top-level")
	assert.Equal(t, "machine-pre", got.MachineID)
	assert.True(t, bytes.Equal(mainBytes, got.RawJSONL))
	assertSearchCount(t, dest, "pretoken", 1)

	// Only Claude rows were written: the destination marker stays at the zstd
	// milestone at most, never the platform one.
	db, err := dest.getDB(ctx)
	require.NoError(t, err)
	var marker sql.NullString
	err = db.QueryRow(`SELECT value FROM vault_meta WHERE key = ?`, minReaderVersionKey).Scan(&marker)
	if err == nil {
		assert.NotEqual(t, "3", marker.String, "a Claude-only merge must not stamp the platform reader version")
	} else {
		assert.ErrorIs(t, err, sql.ErrNoRows)
	}
}

// TestMergeFrom_0006SourceCarriesPlatformAndParent: a source that already has
// the 0006 columns has its platform and parent_uuid carried verbatim (a Codex
// parent + child and a Claude row), the child stays a child in the destination,
// and the first Codex row raises the destination marker to 3.
func TestMergeFrom_0006SourceCarriesPlatformAndParent(t *testing.T) {
	const key = "shared-vault-key-at-least-32-characters!!"
	ctx := context.Background()
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.db")
	destPath := filepath.Join(dir, "dest.db")

	const (
		claude = "c1a0de06-0000-0000-0000-000000000001"
		parent = "c0de0006-0000-0000-0000-000000000002"
		child  = "c0de0006-0000-0000-0000-000000000003"
	)
	claudeRec := mergeRecord(t, claude, "claudetok", 3, 1000, "hC", "machine-src", "/src/claude")
	parentRec := mergeRecord(t, parent, "parenttok", 3, 1000, "hP", "machine-src", "/src/codex")
	parentRec.Session.Platform = PlatformCodex
	parentRec.Session.ClaudeProjectDir = "sessions/2026/09/01/rollout-2026-09-01T10-00-00-" + parent + ".jsonl"
	childRec := mergeRecord(t, child, "childtok", 3, 1000, "hK", "machine-src", "/src/codex")
	childRec.Session.Platform = PlatformCodex
	childRec.Session.ParentUUID = parent
	childRec.Session.ClaudeProjectDir = "sessions/2026/09/01/rollout-2026-09-01T10-01-00-" + child + ".jsonl"
	buildVault(t, srcPath, key, claudeRec, parentRec, childRec)

	dest := openDest(t, destPath, key)
	res, err := MergeFrom(ctx, dest, srcPath, key, "CAPY_VAULT_KEY", MergeOptions{})
	require.NoError(t, err)
	assert.Equal(t, 3, res.Imported)
	assert.Equal(t, 0, res.Errors)

	gotClaude, err := dest.GetSession(ctx, claude)
	require.NoError(t, err)
	assert.Equal(t, PlatformClaudeCode, gotClaude.Platform)
	assert.Empty(t, gotClaude.ParentUUID)

	gotParent, err := dest.GetSession(ctx, parent)
	require.NoError(t, err)
	assert.Equal(t, PlatformCodex, gotParent.Platform)
	assert.Empty(t, gotParent.ParentUUID)
	assert.Equal(t, parentRec.Session.ClaudeProjectDir, gotParent.ClaudeProjectDir, "location hint carried verbatim")

	gotChild, err := dest.GetSession(ctx, child)
	require.NoError(t, err)
	assert.Equal(t, PlatformCodex, gotChild.Platform)
	assert.Equal(t, parent, gotChild.ParentUUID)

	children, err := dest.Children(ctx, parent)
	require.NoError(t, err)
	require.Len(t, children, 1)
	assert.Equal(t, child, children[0].UUID)

	top, err := dest.ListSessions(ctx, ListOptions{})
	require.NoError(t, err)
	assert.Len(t, top, 2, "the merged child stays hidden from the default listing")

	for _, tok := range []string{"claudetok", "parenttok", "childtok"} {
		assertSearchCount(t, dest, tok, 1)
	}

	db, err := dest.getDB(ctx)
	require.NoError(t, err)
	var marker string
	require.NoError(t, db.QueryRow(`SELECT value FROM vault_meta WHERE key = ?`, minReaderVersionKey).Scan(&marker))
	assert.Equal(t, "3", marker, "the first Codex row written raises the destination's reader marker")

	// Idempotent re-merge: nothing new, nothing changed.
	res, err = MergeFrom(ctx, dest, srcPath, key, "CAPY_VAULT_KEY", MergeOptions{})
	require.NoError(t, err)
	assert.Equal(t, 0, res.Imported)
	assert.Equal(t, 3, res.Skipped)
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

// ---------------------------------------------------------------------------
// session-name reconciliation (vault_session_names merges independently of
// transcript content)
// ---------------------------------------------------------------------------

func namePtr(s string) *string { return &s }

// nameSpec pins one vault_session_names row to an exact tuple. A nil title is
// a clear tombstone.
type nameSpec struct {
	uuid    string
	title   *string
	ns      int64
	machine string
}

// buildNamedVault is buildVault plus exact name tuples, applied through the
// renameSessionAt seam so renamed_at_ns and machine_id are pinned verbatim.
func buildNamedVault(t *testing.T, path, key string, recs []*SessionRecord, names []nameSpec) {
	t.Helper()
	t.Setenv(vaultKeyEnv, key)
	s := NewVaultStore(path)
	for _, r := range recs {
		require.NoError(t, s.InsertSession(context.Background(), r))
	}
	for _, n := range names {
		opts := RenameOptions{Clear: n.title == nil}
		if n.title != nil {
			opts.Name = *n.title
		}
		_, err := s.renameSessionAt(context.Background(), n.uuid, opts, time.Unix(0, n.ns), n.machine)
		require.NoError(t, err)
	}
	require.NoError(t, s.Close())
}

// TestSessionNameSupersedes exercises the full total order the merge relies on:
// (renamed_at_ns, machine_id) lexicographic, completed by the value tie-break.
func TestSessionNameSupersedes(t *testing.T) {
	name := func(title *string, ns int64, machine string) SessionName {
		return SessionName{CustomTitle: title, RenamedAtNS: ns, MachineID: machine}
	}
	cases := []struct {
		desc string
		src  SessionName
		dest *SessionName
		want bool
	}{
		{"nil destination loses to any source", name(namePtr("a"), 1, "m"), nil, true},
		{"nil destination loses even to a tombstone", name(nil, 1, "m"), nil, true},
		{"newer timestamp wins", name(namePtr("a"), 2, "m"), &SessionName{CustomTitle: namePtr("b"), RenamedAtNS: 1, MachineID: "z"}, true},
		{"older timestamp loses", name(namePtr("z"), 1, "z"), &SessionName{CustomTitle: namePtr("a"), RenamedAtNS: 2, MachineID: "a"}, false},
		{"equal timestamp greater machine wins", name(namePtr("a"), 1, "mB"), &SessionName{CustomTitle: namePtr("z"), RenamedAtNS: 1, MachineID: "mA"}, true},
		{"equal timestamp smaller machine loses", name(namePtr("z"), 1, "mA"), &SessionName{CustomTitle: namePtr("a"), RenamedAtNS: 1, MachineID: "mB"}, false},
		{"equal tuple non-null beats tombstone", name(namePtr("a"), 1, "m"), &SessionName{CustomTitle: nil, RenamedAtNS: 1, MachineID: "m"}, true},
		{"equal tuple tombstone loses to non-null", name(nil, 1, "m"), &SessionName{CustomTitle: namePtr("a"), RenamedAtNS: 1, MachineID: "m"}, false},
		{"equal tuple bytewise greater title wins", name(namePtr("bbb"), 1, "m"), &SessionName{CustomTitle: namePtr("aaa"), RenamedAtNS: 1, MachineID: "m"}, true},
		{"equal tuple bytewise smaller title loses", name(namePtr("aaa"), 1, "m"), &SessionName{CustomTitle: namePtr("bbb"), RenamedAtNS: 1, MachineID: "m"}, false},
		{"identical non-null state is a no-op", name(namePtr("same"), 1, "m"), &SessionName{CustomTitle: namePtr("same"), RenamedAtNS: 1, MachineID: "m"}, false},
		{"identical tombstone state is a no-op", name(nil, 1, "m"), &SessionName{CustomTitle: nil, RenamedAtNS: 1, MachineID: "m"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			assert.Equal(t, tc.want, sessionNameSupersedes(tc.src, tc.dest))
		})
	}
}

// TestMergeFrom_NameReconciliationMatrix drives the merge-level decision over
// the same-hash skip branch (the most common cross-machine case): both vaults
// hold identical content, so ONLY name state can change. Each case asserts the
// reported status, that a winning source tuple is stored VERBATIM (an
// accidental local re-stamp would change renamed_at_ns and fail), and the
// resulting effective title.
func TestMergeFrom_NameReconciliationMatrix(t *testing.T) {
	const key = "shared-vault-key-at-least-32-characters!!"
	const uuid = "aaaa1111-0000-0000-0000-00000000000a"
	ctx := context.Background()

	cases := []struct {
		desc     string
		srcName  *nameSpec
		destName *nameSpec
		// wantUpdated: the name reconciliation changed the destination (reported
		// StatusUpdated); otherwise the same-hash session reports StatusSkipped.
		wantUpdated bool
		wantName    *SessionName // expected stored state after merge; nil == no row
		wantTitle   string       // expected effective title after merge
	}{
		{
			desc:        "newer source wins",
			srcName:     &nameSpec{uuid: uuid, title: namePtr("SRC"), ns: 2000, machine: "m"},
			destName:    &nameSpec{uuid: uuid, title: namePtr("DEST"), ns: 1000, machine: "m"},
			wantUpdated: true,
			wantName:    &SessionName{CustomTitle: namePtr("SRC"), RenamedAtNS: 2000, MachineID: "m"},
			wantTitle:   "SRC",
		},
		{
			desc:      "older source loses",
			srcName:   &nameSpec{uuid: uuid, title: namePtr("SRC"), ns: 1000, machine: "m"},
			destName:  &nameSpec{uuid: uuid, title: namePtr("DEST"), ns: 2000, machine: "m"},
			wantName:  &SessionName{CustomTitle: namePtr("DEST"), RenamedAtNS: 2000, MachineID: "m"},
			wantTitle: "DEST",
		},
		{
			desc:        "equal timestamp greater machine wins",
			srcName:     &nameSpec{uuid: uuid, title: namePtr("SRC"), ns: 1000, machine: "mB"},
			destName:    &nameSpec{uuid: uuid, title: namePtr("DEST"), ns: 1000, machine: "mA"},
			wantUpdated: true,
			wantName:    &SessionName{CustomTitle: namePtr("SRC"), RenamedAtNS: 1000, MachineID: "mB"},
			wantTitle:   "SRC",
		},
		{
			desc:      "equal timestamp smaller machine loses",
			srcName:   &nameSpec{uuid: uuid, title: namePtr("SRC"), ns: 1000, machine: "mA"},
			destName:  &nameSpec{uuid: uuid, title: namePtr("DEST"), ns: 1000, machine: "mB"},
			wantName:  &SessionName{CustomTitle: namePtr("DEST"), RenamedAtNS: 1000, MachineID: "mB"},
			wantTitle: "DEST",
		},
		{
			desc:        "equal tuple non-null beats tombstone",
			srcName:     &nameSpec{uuid: uuid, title: namePtr("SRC"), ns: 1000, machine: "m"},
			destName:    &nameSpec{uuid: uuid, title: nil, ns: 1000, machine: "m"},
			wantUpdated: true,
			wantName:    &SessionName{CustomTitle: namePtr("SRC"), RenamedAtNS: 1000, MachineID: "m"},
			wantTitle:   "SRC",
		},
		{
			desc:      "equal tuple source tombstone loses to non-null",
			srcName:   &nameSpec{uuid: uuid, title: nil, ns: 1000, machine: "m"},
			destName:  &nameSpec{uuid: uuid, title: namePtr("DEST"), ns: 1000, machine: "m"},
			wantName:  &SessionName{CustomTitle: namePtr("DEST"), RenamedAtNS: 1000, MachineID: "m"},
			wantTitle: "DEST",
		},
		{
			desc:        "equal tuple bytewise greater title wins",
			srcName:     &nameSpec{uuid: uuid, title: namePtr("bbb"), ns: 1000, machine: "m"},
			destName:    &nameSpec{uuid: uuid, title: namePtr("aaa"), ns: 1000, machine: "m"},
			wantUpdated: true,
			wantName:    &SessionName{CustomTitle: namePtr("bbb"), RenamedAtNS: 1000, MachineID: "m"},
			wantTitle:   "bbb",
		},
		{
			desc:      "equal tuple bytewise smaller title loses",
			srcName:   &nameSpec{uuid: uuid, title: namePtr("aaa"), ns: 1000, machine: "m"},
			destName:  &nameSpec{uuid: uuid, title: namePtr("bbb"), ns: 1000, machine: "m"},
			wantName:  &SessionName{CustomTitle: namePtr("bbb"), RenamedAtNS: 1000, MachineID: "m"},
			wantTitle: "bbb",
		},
		{
			desc:      "identical state is a skipped no-op",
			srcName:   &nameSpec{uuid: uuid, title: namePtr("same"), ns: 1000, machine: "m"},
			destName:  &nameSpec{uuid: uuid, title: namePtr("same"), ns: 1000, machine: "m"},
			wantName:  &SessionName{CustomTitle: namePtr("same"), RenamedAtNS: 1000, MachineID: "m"},
			wantTitle: "same",
		},
		{
			desc:        "never-named destination adopts source name",
			srcName:     &nameSpec{uuid: uuid, title: namePtr("SRC"), ns: 1000, machine: "m"},
			wantUpdated: true,
			wantName:    &SessionName{CustomTitle: namePtr("SRC"), RenamedAtNS: 1000, MachineID: "m"},
			wantTitle:   "SRC",
		},
		{
			desc:        "never-named destination adopts source tombstone",
			srcName:     &nameSpec{uuid: uuid, title: nil, ns: 1000, machine: "m"},
			wantUpdated: true,
			wantName:    &SessionName{CustomTitle: nil, RenamedAtNS: 1000, MachineID: "m"},
			wantTitle:   "title desttok", // tombstone: falls back to imported title
		},
		{
			desc:        "newer source tombstone clears destination name",
			srcName:     &nameSpec{uuid: uuid, title: nil, ns: 2000, machine: "m"},
			destName:    &nameSpec{uuid: uuid, title: namePtr("DEST"), ns: 1000, machine: "m"},
			wantUpdated: true,
			wantName:    &SessionName{CustomTitle: nil, RenamedAtNS: 2000, MachineID: "m"},
			wantTitle:   "title desttok",
		},
		{
			desc:      "source without name row leaves destination untouched",
			destName:  &nameSpec{uuid: uuid, title: namePtr("DEST"), ns: 1000, machine: "m"},
			wantName:  &SessionName{CustomTitle: namePtr("DEST"), RenamedAtNS: 1000, MachineID: "m"},
			wantTitle: "DEST",
		},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			dir := t.TempDir()
			srcPath := filepath.Join(dir, "source.db")
			destPath := filepath.Join(dir, "dest.db")

			var srcNames, destNames []nameSpec
			if tc.srcName != nil {
				srcNames = append(srcNames, *tc.srcName)
			}
			if tc.destName != nil {
				destNames = append(destNames, *tc.destName)
			}
			// Identical content_hash on both sides drives the same-hash skip branch.
			buildNamedVault(t, srcPath, key,
				[]*SessionRecord{mergeRecord(t, uuid, "srctok", 3, 1000, "samehash", "machine-src", "/src/p")}, srcNames)
			buildNamedVault(t, destPath, key,
				[]*SessionRecord{mergeRecord(t, uuid, "desttok", 3, 1000, "samehash", "machine-dest", "/dest/p")}, destNames)

			dest := openDest(t, destPath, key)
			res, err := MergeFrom(ctx, dest, srcPath, key, "CAPY_VAULT_KEY", MergeOptions{})
			require.NoError(t, err)
			require.Equal(t, 0, res.Errors)
			require.Len(t, res.Sessions, 1)

			if tc.wantUpdated {
				assert.Equal(t, 1, res.Updated, "a name-only change must report updated")
				assert.Equal(t, 0, res.Skipped)
				assert.Equal(t, tc.wantTitle, res.Sessions[0].Title, "entry must surface the effective title")
			} else {
				assert.Equal(t, 0, res.Updated)
				assert.Equal(t, 1, res.Skipped, "an older or identical source name must report skipped")
				// Skipped rows keep the pre-existing convention: Title is empty
				// ("not scanned" — see ImportedSession), not the effective title.
				assert.Empty(t, res.Sessions[0].Title)
			}

			got, err := dest.GetSession(ctx, uuid)
			require.NoError(t, err)
			if tc.wantName == nil {
				assert.Nil(t, got.Name)
			} else {
				require.NotNil(t, got.Name)
				assert.Equal(t, *tc.wantName, *got.Name, "stored tuple must be the winning side's, verbatim")
			}
			assert.Equal(t, tc.wantTitle, got.EffectiveTitle())
			// The same-hash branch never rewrites the transcript.
			assert.Equal(t, "machine-dest", got.MachineID, "destination transcript metadata must be untouched")
		})
	}
}

// TestMergeFrom_NameOnTranscriptBranches proves name reconciliation runs on
// every transcript decision: smaller-skip, replace (both winner directions),
// brand-new session (name committed atomically with the session row), and the
// zero-message exclusion with and without a populated destination.
func TestMergeFrom_NameOnTranscriptBranches(t *testing.T) {
	const key = "shared-vault-key-at-least-32-characters!!"
	ctx := context.Background()

	t.Run("smaller source updates name only", func(t *testing.T) {
		const uuid = "bbbb2222-0000-0000-0000-00000000000b"
		dir := t.TempDir()
		srcPath := filepath.Join(dir, "source.db")
		destPath := filepath.Join(dir, "dest.db")
		buildNamedVault(t, srcPath, key,
			[]*SessionRecord{mergeRecord(t, uuid, "smallsrc", 2, 100, "srchash", "machine-src", "/src/p")},
			[]nameSpec{{uuid: uuid, title: namePtr("From the laptop"), ns: 2000, machine: "m"}})
		buildNamedVault(t, destPath, key,
			[]*SessionRecord{mergeRecord(t, uuid, "bigdest", 5, 3000, "desthash", "machine-dest", "/dest/p")}, nil)

		dest := openDest(t, destPath, key)
		before := snapshotArchivedData(t, dest, uuid)
		res, err := MergeFrom(ctx, dest, srcPath, key, "CAPY_VAULT_KEY", MergeOptions{})
		require.NoError(t, err)
		assert.Equal(t, 1, res.Updated, "the smaller transcript is skipped but the newer name lands")
		assert.Equal(t, 0, res.Skipped)

		after := snapshotArchivedData(t, dest, uuid)
		assert.True(t, bytes.Equal(before.raw, after.raw), "archived bytes must be unchanged by a name-only merge")
		assert.Equal(t, before.hash, after.hash)
		assert.Equal(t, before.size, after.size)
		assert.Equal(t, before.ftsCount, after.ftsCount)

		got, err := dest.GetSession(ctx, uuid)
		require.NoError(t, err)
		require.NotNil(t, got.Name)
		assert.Equal(t, SessionName{CustomTitle: namePtr("From the laptop"), RenamedAtNS: 2000, MachineID: "m"}, *got.Name)
	})

	t.Run("replace carries winning source name", func(t *testing.T) {
		const uuid = "cccc3333-0000-0000-0000-00000000000c"
		dir := t.TempDir()
		srcPath := filepath.Join(dir, "source.db")
		destPath := filepath.Join(dir, "dest.db")
		buildNamedVault(t, srcPath, key,
			[]*SessionRecord{mergeRecord(t, uuid, "bigsrc", 5, 3000, "srchash", "machine-src", "/src/p")},
			[]nameSpec{{uuid: uuid, title: namePtr("SRC"), ns: 2000, machine: "m"}})
		buildNamedVault(t, destPath, key,
			[]*SessionRecord{mergeRecord(t, uuid, "smalldest", 2, 500, "desthash", "machine-dest", "/dest/p")},
			[]nameSpec{{uuid: uuid, title: namePtr("DEST"), ns: 1000, machine: "m"}})

		dest := openDest(t, destPath, key)
		res, err := MergeFrom(ctx, dest, srcPath, key, "CAPY_VAULT_KEY", MergeOptions{})
		require.NoError(t, err)
		assert.Equal(t, 1, res.Updated)
		assert.Equal(t, "SRC", res.Sessions[0].Title, "entry title must reflect the winning name")

		got, err := dest.GetSession(ctx, uuid)
		require.NoError(t, err)
		assert.Equal(t, "machine-src", got.MachineID, "larger source transcript replaces")
		require.NotNil(t, got.Name)
		assert.Equal(t, SessionName{CustomTitle: namePtr("SRC"), RenamedAtNS: 2000, MachineID: "m"}, *got.Name)
		assertSearchCount(t, dest, "bigsrc", 1)
	})

	t.Run("replace keeps newer destination name", func(t *testing.T) {
		const uuid = "dddd4444-0000-0000-0000-00000000000d"
		dir := t.TempDir()
		srcPath := filepath.Join(dir, "source.db")
		destPath := filepath.Join(dir, "dest.db")
		buildNamedVault(t, srcPath, key,
			[]*SessionRecord{mergeRecord(t, uuid, "bigsrc", 5, 3000, "srchash", "machine-src", "/src/p")},
			[]nameSpec{{uuid: uuid, title: namePtr("SRC"), ns: 1000, machine: "m"}})
		buildNamedVault(t, destPath, key,
			[]*SessionRecord{mergeRecord(t, uuid, "smalldest", 2, 500, "desthash", "machine-dest", "/dest/p")},
			[]nameSpec{{uuid: uuid, title: namePtr("DEST keeps"), ns: 2000, machine: "m"}})

		dest := openDest(t, destPath, key)
		res, err := MergeFrom(ctx, dest, srcPath, key, "CAPY_VAULT_KEY", MergeOptions{})
		require.NoError(t, err)
		assert.Equal(t, 1, res.Updated)
		assert.Equal(t, "DEST keeps", res.Sessions[0].Title, "entry title must reflect the surviving destination name")

		got, err := dest.GetSession(ctx, uuid)
		require.NoError(t, err)
		assert.Equal(t, "machine-src", got.MachineID, "transcript still replaced")
		require.NotNil(t, got.Name)
		assert.Equal(t, SessionName{CustomTitle: namePtr("DEST keeps"), RenamedAtNS: 2000, MachineID: "m"}, *got.Name,
			"an older source name must not clobber the newer destination name during replace")
	})

	t.Run("new session inserts name atomically", func(t *testing.T) {
		const uuid = "eeee5555-0000-0000-0000-00000000000e"
		dir := t.TempDir()
		srcPath := filepath.Join(dir, "source.db")
		destPath := filepath.Join(dir, "dest.db")
		buildNamedVault(t, srcPath, key,
			[]*SessionRecord{mergeRecord(t, uuid, "freshsrc", 3, 1000, "srchash", "machine-src", "/src/p")},
			[]nameSpec{{uuid: uuid, title: namePtr("Fresh name"), ns: 1000, machine: "m"}})

		dest := openDest(t, destPath, key)
		res, err := MergeFrom(ctx, dest, srcPath, key, "CAPY_VAULT_KEY", MergeOptions{})
		require.NoError(t, err)
		assert.Equal(t, 1, res.Imported)
		assert.Equal(t, "Fresh name", res.Sessions[0].Title)

		got, err := dest.GetSession(ctx, uuid)
		require.NoError(t, err)
		require.NotNil(t, got.Name)
		assert.Equal(t, SessionName{CustomTitle: namePtr("Fresh name"), RenamedAtNS: 1000, MachineID: "m"}, *got.Name)
	})

	t.Run("zero-message source still names populated destination", func(t *testing.T) {
		const uuid = "ffff6666-0000-0000-0000-00000000000f"
		dir := t.TempDir()
		srcPath := filepath.Join(dir, "source.db")
		destPath := filepath.Join(dir, "dest.db")
		buildNamedVault(t, srcPath, key,
			[]*SessionRecord{mergeRecord(t, uuid, "emptysrc", 0, 200, "srchash", "machine-src", "/src/p")},
			[]nameSpec{{uuid: uuid, title: namePtr("Renamed shell"), ns: 2000, machine: "m"}})
		buildNamedVault(t, destPath, key,
			[]*SessionRecord{mergeRecord(t, uuid, "realdest", 4, 2000, "desthash", "machine-dest", "/dest/p")},
			[]nameSpec{{uuid: uuid, title: namePtr("Old dest name"), ns: 1000, machine: "m"}})

		dest := openDest(t, destPath, key)
		before := snapshotArchivedData(t, dest, uuid)
		res, err := MergeFrom(ctx, dest, srcPath, key, "CAPY_VAULT_KEY", MergeOptions{})
		require.NoError(t, err)
		assert.Equal(t, 1, res.Updated, "the excluded shell's newer name must still reconcile")
		assert.Equal(t, 0, res.Excluded)

		after := snapshotArchivedData(t, dest, uuid)
		assert.True(t, bytes.Equal(before.raw, after.raw), "the excluded transcript must not touch the destination")

		got, err := dest.GetSession(ctx, uuid)
		require.NoError(t, err)
		require.NotNil(t, got.Name)
		assert.Equal(t, SessionName{CustomTitle: namePtr("Renamed shell"), RenamedAtNS: 2000, MachineID: "m"}, *got.Name)
	})

	t.Run("zero-message source with older name stays excluded", func(t *testing.T) {
		const uuid = "abab7777-0000-0000-0000-00000000000a"
		dir := t.TempDir()
		srcPath := filepath.Join(dir, "source.db")
		destPath := filepath.Join(dir, "dest.db")
		buildNamedVault(t, srcPath, key,
			[]*SessionRecord{mergeRecord(t, uuid, "emptysrc", 0, 200, "srchash", "machine-src", "/src/p")},
			[]nameSpec{{uuid: uuid, title: namePtr("stale"), ns: 1000, machine: "m"}})
		buildNamedVault(t, destPath, key,
			[]*SessionRecord{mergeRecord(t, uuid, "realdest", 4, 2000, "desthash", "machine-dest", "/dest/p")},
			[]nameSpec{{uuid: uuid, title: namePtr("Current"), ns: 2000, machine: "m"}})

		dest := openDest(t, destPath, key)
		res, err := MergeFrom(ctx, dest, srcPath, key, "CAPY_VAULT_KEY", MergeOptions{})
		require.NoError(t, err)
		assert.Equal(t, 1, res.Excluded, "an older shell name changes nothing, so the shell stays excluded")
		assert.Equal(t, 0, res.Updated)

		got, err := dest.GetSession(ctx, uuid)
		require.NoError(t, err)
		require.NotNil(t, got.Name)
		assert.Equal(t, "Current", *got.Name.CustomTitle)
	})

	t.Run("zero-message source without destination contributes nothing", func(t *testing.T) {
		const uuid = "cdcd8888-0000-0000-0000-00000000000c"
		dir := t.TempDir()
		srcPath := filepath.Join(dir, "source.db")
		destPath := filepath.Join(dir, "dest.db")
		buildNamedVault(t, srcPath, key,
			[]*SessionRecord{mergeRecord(t, uuid, "emptysrc", 0, 200, "srchash", "machine-src", "/src/p")},
			[]nameSpec{{uuid: uuid, title: namePtr("orphan-to-be"), ns: 1000, machine: "m"}})

		dest := openDest(t, destPath, key)
		res, err := MergeFrom(ctx, dest, srcPath, key, "CAPY_VAULT_KEY", MergeOptions{})
		require.NoError(t, err)
		assert.Equal(t, 1, res.Excluded)
		assert.Equal(t, 0, res.Errors, "an excluded shell with no destination parent is not an error")

		_, err = dest.GetSession(ctx, uuid)
		assert.ErrorIs(t, err, ErrSessionNotFound, "no session row means no name row (FK parent required)")
	})
}

// TestMergeFrom_NameLegacySourceLeavesDestNames proves a source without
// vault_session_names (legacy schema) merges cleanly and leaves destination
// name state untouched.
func TestMergeFrom_NameLegacySourceLeavesDestNames(t *testing.T) {
	const key = "shared-vault-key-at-least-32-characters!!"
	ctx := context.Background()
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "v1source.db")
	destPath := filepath.Join(dir, "dest.db")

	const uuid = "efef9999-0000-0000-0000-00000000000e"
	buildV1Source(t, srcPath, key, uuid, "v1token")
	// Destination holds a LARGER named copy, so the v1 source hits the
	// smaller-skip branch — the branch that now reads name state.
	buildNamedVault(t, destPath, key,
		[]*SessionRecord{mergeRecord(t, uuid, "bigdest", 5, 100000, "desthash", "machine-dest", "/dest/p")},
		[]nameSpec{{uuid: uuid, title: namePtr("Kept name"), ns: 1000, machine: "m"}})

	dest := openDest(t, destPath, key)
	res, err := MergeFrom(ctx, dest, srcPath, key, "CAPY_VAULT_KEY", MergeOptions{})
	require.NoError(t, err, "a legacy source without vault_session_names must merge cleanly")
	assert.Equal(t, 1, res.Skipped)
	assert.Equal(t, 0, res.Errors)

	got, err := dest.GetSession(ctx, uuid)
	require.NoError(t, err)
	require.NotNil(t, got.Name)
	assert.Equal(t, SessionName{CustomTitle: namePtr("Kept name"), RenamedAtNS: 1000, MachineID: "m"}, *got.Name)
}

// TestMergeFrom_NameDryRun proves dry-run reports the prospective name decision
// (status + effective title) without writing anything.
func TestMergeFrom_NameDryRun(t *testing.T) {
	const key = "shared-vault-key-at-least-32-characters!!"
	ctx := context.Background()
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.db")
	destPath := filepath.Join(dir, "dest.db")

	const uuid = "0101aaaa-0000-0000-0000-000000000001"
	buildNamedVault(t, srcPath, key,
		[]*SessionRecord{mergeRecord(t, uuid, "srctok", 3, 1000, "samehash", "machine-src", "/src/p")},
		[]nameSpec{{uuid: uuid, title: namePtr("Prospective"), ns: 2000, machine: "m"}})
	buildNamedVault(t, destPath, key,
		[]*SessionRecord{mergeRecord(t, uuid, "desttok", 3, 1000, "samehash", "machine-dest", "/dest/p")},
		[]nameSpec{{uuid: uuid, title: namePtr("Current"), ns: 1000, machine: "m"}})

	dest := openDest(t, destPath, key)
	res, err := MergeFrom(ctx, dest, srcPath, key, "CAPY_VAULT_KEY", MergeOptions{DryRun: true})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Updated, "dry-run reports the prospective name-only update")
	require.Len(t, res.Sessions, 1)
	assert.Equal(t, "Prospective", res.Sessions[0].Title)

	got, err := dest.GetSession(ctx, uuid)
	require.NoError(t, err)
	require.NotNil(t, got.Name)
	assert.Equal(t, SessionName{CustomTitle: namePtr("Current"), RenamedAtNS: 1000, MachineID: "m"}, *got.Name,
		"dry-run must not write name state")
}

// TestMergeFrom_NameIdempotentAndConverges proves a winning merge stores the
// source tuple verbatim (a re-stamp would break this), a repeated merge is a
// skipped no-op, and both merge directions converge on the same effective name.
func TestMergeFrom_NameIdempotentAndConverges(t *testing.T) {
	const key = "shared-vault-key-at-least-32-characters!!"
	ctx := context.Background()
	const uuid = "0202bbbb-0000-0000-0000-000000000002"

	winner := SessionName{CustomTitle: namePtr("Alpha wins"), RenamedAtNS: 2000, MachineID: "mA"}
	buildPair := func(dir string) (aPath, bPath string) {
		aPath = filepath.Join(dir, "a.db")
		bPath = filepath.Join(dir, "b.db")
		buildNamedVault(t, aPath, key,
			[]*SessionRecord{mergeRecord(t, uuid, "tok", 3, 1000, "samehash", "machine-a", "/a/p")},
			[]nameSpec{{uuid: uuid, title: winner.CustomTitle, ns: winner.RenamedAtNS, machine: winner.MachineID}})
		buildNamedVault(t, bPath, key,
			[]*SessionRecord{mergeRecord(t, uuid, "tok", 3, 1000, "samehash", "machine-b", "/b/p")},
			[]nameSpec{{uuid: uuid, title: namePtr("beta"), ns: 1000, machine: "mB"}})
		return aPath, bPath
	}

	// Direction 1: A (newer) into B — B adopts A's tuple verbatim.
	aPath, bPath := buildPair(t.TempDir())
	destB := openDest(t, bPath, key)
	first, err := MergeFrom(ctx, destB, aPath, key, "CAPY_VAULT_KEY", MergeOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, first.Updated)
	gotB, err := destB.GetSession(ctx, uuid)
	require.NoError(t, err)
	require.NotNil(t, gotB.Name)
	assert.Equal(t, winner, *gotB.Name, "the stored tuple must equal the source tuple exactly (no re-stamp)")

	// Repeat: identical states now, so the merge is a skipped no-op and the
	// tuple is byte-for-byte stable.
	second, err := MergeFrom(ctx, destB, aPath, key, "CAPY_VAULT_KEY", MergeOptions{})
	require.NoError(t, err)
	assert.Equal(t, 0, second.Updated)
	assert.Equal(t, 1, second.Skipped)
	gotB2, err := destB.GetSession(ctx, uuid)
	require.NoError(t, err)
	require.NotNil(t, gotB2.Name)
	assert.Equal(t, winner, *gotB2.Name)
	require.NoError(t, destB.Close())

	// Direction 2 on fresh copies: B (older) into A — A keeps its own state.
	// Both directions therefore converge on the same effective title.
	aPath2, bPath2 := buildPair(t.TempDir())
	destA := openDest(t, aPath2, key)
	back, err := MergeFrom(ctx, destA, bPath2, key, "CAPY_VAULT_KEY", MergeOptions{})
	require.NoError(t, err)
	assert.Equal(t, 0, back.Updated)
	assert.Equal(t, 1, back.Skipped)
	gotA, err := destA.GetSession(ctx, uuid)
	require.NoError(t, err)
	require.NotNil(t, gotA.Name)
	assert.Equal(t, winner, *gotA.Name)
	assert.Equal(t, gotB2.EffectiveTitle(), gotA.EffectiveTitle(), "both merge directions must converge")
}

// TestMergeFrom_EmptySourceTitleNormalizesToTombstone proves a whitespace-only
// source custom_title (producible only by a foreign or hand-edited vault) is
// normalized to a clear tombstone rather than stored as a blank name.
func TestMergeFrom_EmptySourceTitleNormalizesToTombstone(t *testing.T) {
	const key = "shared-vault-key-at-least-32-characters!!"
	ctx := context.Background()
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.db")
	destPath := filepath.Join(dir, "dest.db")

	const uuid = "0303cccc-0000-0000-0000-000000000003"
	buildNamedVault(t, srcPath, key,
		[]*SessionRecord{mergeRecord(t, uuid, "srctok", 3, 1000, "samehash", "machine-src", "/src/p")}, nil)
	// Insert the malformed row directly — every supported writer rejects it.
	dsn := sqliteutil.EncryptedDSN(srcPath, key) + "&_busy_timeout=5000"
	raw, err := sql.Open("sqlite3", dsn)
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO vault_session_names (session_uuid, custom_title, renamed_at_ns, machine_id)
		VALUES (?, ?, ?, ?)`, uuid, "   ", int64(2000), "m")
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	buildNamedVault(t, destPath, key,
		[]*SessionRecord{mergeRecord(t, uuid, "desttok", 3, 1000, "samehash", "machine-dest", "/dest/p")},
		[]nameSpec{{uuid: uuid, title: namePtr("Old name"), ns: 1000, machine: "m"}})

	dest := openDest(t, destPath, key)
	res, err := MergeFrom(ctx, dest, srcPath, key, "CAPY_VAULT_KEY", MergeOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Updated)

	got, err := dest.GetSession(ctx, uuid)
	require.NoError(t, err)
	require.NotNil(t, got.Name)
	assert.Nil(t, got.Name.CustomTitle, "a whitespace-only source title must land as a clear tombstone")
	assert.Equal(t, int64(2000), got.Name.RenamedAtNS)
	assert.Equal(t, "title desttok", got.EffectiveTitle(), "cleared name falls back to the imported title")
}

// TestVaultStore_ReconcileSessionNameMissingSessionFails proves the name-only
// write path surfaces a concurrent delete as an actionable error (the FK has no
// parent) instead of silently inserting orphan metadata.
func TestVaultStore_ReconcileSessionNameMissingSessionFails(t *testing.T) {
	s := newTestVault(t)
	_, err := s.reconcileSessionName(context.Background(), "04040404-dead-beef-0000-000000000004",
		SessionName{CustomTitle: namePtr("orphan"), RenamedAtNS: 1, MachineID: "m"})
	require.Error(t, err, "a name row without a parent session must be rejected")
}

// TestMergeFrom_RenameMergeRaceDeterministicWinner races a concurrent local
// rename against MergeFrom on the same destination session. The local rename's
// tuple (ns 5000, via the deterministic clock seam) is greater than the source
// tuple (ns 1000) in every interleaving: if the rename lands first, the merge's
// in-transaction re-check sees the newer state and skips; if the merge lands
// first, the rename's monotonic max(now, stored+1) stamps 5000 over it. Run
// under -race this also exercises the snapshot-read/tx-write seam.
func TestMergeFrom_RenameMergeRaceDeterministicWinner(t *testing.T) {
	const key = "shared-vault-key-at-least-32-characters!!"
	ctx := context.Background()
	const uuid = "05050505-0000-0000-0000-000000000005"

	for range 4 {
		dir := t.TempDir()
		srcPath := filepath.Join(dir, "source.db")
		destPath := filepath.Join(dir, "dest.db")
		buildNamedVault(t, srcPath, key,
			[]*SessionRecord{mergeRecord(t, uuid, "srctok", 3, 1000, "samehash", "machine-src", "/src/p")},
			[]nameSpec{{uuid: uuid, title: namePtr("merge name"), ns: 1000, machine: "machine-src"}})
		buildNamedVault(t, destPath, key,
			[]*SessionRecord{mergeRecord(t, uuid, "desttok", 3, 1000, "samehash", "machine-dest", "/dest/p")}, nil)

		dest := openDest(t, destPath, key)
		start := make(chan struct{})
		var wg sync.WaitGroup
		var mergeErr, renameErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, mergeErr = MergeFrom(ctx, dest, srcPath, key, "CAPY_VAULT_KEY", MergeOptions{})
		}()
		go func() {
			defer wg.Done()
			<-start
			_, renameErr = dest.renameSessionAt(ctx, uuid, RenameOptions{Name: "local wins"},
				time.Unix(0, 5000), "machine-local")
		}()
		close(start)
		wg.Wait()
		require.NoError(t, mergeErr)
		require.NoError(t, renameErr)

		got, err := dest.GetSession(ctx, uuid)
		require.NoError(t, err)
		require.NotNil(t, got.Name)
		assert.Equal(t, SessionName{CustomTitle: namePtr("local wins"), RenamedAtNS: 5000, MachineID: "machine-local"},
			*got.Name, "the local rename must win every interleaving")
		require.NoError(t, dest.Close())
	}
}
