package vault

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/sqliteutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testVaultKey = "test-vault-key-at-least-32-characters!!"

func newTestVault(t *testing.T) *VaultStore {
	t.Helper()
	t.Setenv(vaultKeyEnv, testVaultKey)
	dir := t.TempDir()
	s := NewVaultStore(filepath.Join(dir, "vault.db"))
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func sampleRecord(uuid string) *SessionRecord {
	return &SessionRecord{
		Session: Session{
			UUID:             uuid,
			Title:            "Sample session",
			StartTime:        time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC),
			EndTime:          time.Date(2026, 5, 1, 11, 0, 0, 0, time.UTC),
			MessageCount:     4,
			SizeBytes:        1234,
			ContentHash:      "hash-" + uuid,
			MachineID:        "machine-a",
			ClaudeProjectDir: "-home-user-proj",
			ProjectPath:      "/home/user/proj",
			GitBranch:        "main",
			RawJSONL:         []byte(`{"type":"user","text":"hello"}` + "\n"),
		},
		Files: []File{
			{RelativePath: "subagents/agent-1.jsonl", RawContent: []byte(`{"type":"user"}`)},
			{RelativePath: "tool-results/t1.json", RawContent: []byte(`{"ok":true}`)},
		},
		FTS: []FTSRow{
			{SessionUUID: uuid, Role: "user", LineIndex: 0, ContentText: "hello brontosaurus"},
			{SessionUUID: uuid, Role: "assistant", TurnIndex: 0, MessageIndex: 1, LineIndex: 1, ContentText: "farewell"},
			{SessionUUID: uuid, SubagentID: "agent-1", Role: "assistant", LineIndex: 0, ContentText: "stegosaurus subagent"},
		},
	}
}

func TestVaultStore_CreateAndSchema(t *testing.T) {
	s := newTestVault(t)
	db, err := s.getDB()
	require.NoError(t, err)

	for _, name := range []string{"vault_sessions", "vault_files", "vault_fts", "vault_meta", "vault_migrations"} {
		var got string
		err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&got)
		require.NoErrorf(t, err, "table %s should exist", name)
		assert.Equal(t, name, got)
	}

	var idx string
	err = db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, "idx_sessions_end_time").Scan(&idx)
	require.NoError(t, err, "idx_sessions_end_time should exist")
}

func TestVaultStore_EncryptedAtRest(t *testing.T) {
	t.Setenv(vaultKeyEnv, testVaultKey)
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.db")

	s := NewVaultStore(path)
	require.NoError(t, s.InsertSession(sampleRecord("11111111-1111-1111-1111-111111111111")))
	require.NoError(t, s.Close())

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(raw), 15)
	assert.NotEqual(t, "SQLite format 3", string(raw[:15]), "vault.db must be encrypted at rest")
}

func TestVaultStore_WrongKeyFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.db")

	t.Setenv(vaultKeyEnv, "key-one-at-least-32-characters-long!!!")
	s1 := NewVaultStore(path)
	require.NoError(t, s1.InsertSession(sampleRecord("22222222-2222-2222-2222-222222222222")))
	require.NoError(t, s1.Close())

	t.Setenv(vaultKeyEnv, "key-two-at-least-32-characters-long!!!")
	s2 := NewVaultStore(path)
	_, err := s2.ListSessions(ListOptions{})
	require.Error(t, err)
	assert.True(t, sqliteutil.IsWrongPassphrase(err), "wrong key should yield WrongPassphraseError, got: %v", err)
	_ = s2.Close()
}

func TestVaultStore_EmptyDBQueries(t *testing.T) {
	s := newTestVault(t)

	sessions, err := s.ListSessions(ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, sessions)

	results, err := s.Search(SearchOptions{Query: "anything"})
	require.NoError(t, err)
	assert.Empty(t, results)

	_, err = s.GetSession("00000000")
	assert.ErrorIs(t, err, ErrSessionNotFound)
}

func TestVaultStore_InsertGetListSearch(t *testing.T) {
	s := newTestVault(t)
	uuid := "aaaaaaaa-1111-2222-3333-444444444444"
	require.NoError(t, s.InsertSession(sampleRecord(uuid)))

	got, err := s.GetSession(uuid[:8])
	require.NoError(t, err)
	assert.Equal(t, uuid, got.UUID)
	assert.Equal(t, "Sample session", got.Title)
	assert.Equal(t, "main", got.GitBranch)
	assert.Equal(t, "/home/user/proj", got.ProjectPath)
	assert.Equal(t, int64(1234), got.SizeBytes)
	assert.True(t, got.StartTime.Equal(time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)))
	assert.NotEmpty(t, got.RawJSONL)

	files, err := s.GetFiles(uuid)
	require.NoError(t, err)
	require.Len(t, files, 2)
	assert.Equal(t, "subagents/agent-1.jsonl", files[0].RelativePath)

	listed, err := s.ListSessions(ListOptions{})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, uuid, listed[0].UUID)
	assert.Nil(t, listed[0].RawJSONL, "list should not load the blob")

	results, err := s.Search(SearchOptions{Query: "brontosaurus"})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, uuid, results[0].SessionUUID)
	assert.Equal(t, "user", results[0].Role)
	assert.Equal(t, 0, results[0].LineIndex)
	assert.Empty(t, results[0].SubagentID, "main-session rows carry the empty-string sentinel")

	// A subagent FTS row carries its SubagentID through to the result — the
	// anchor the TUI uses to open the subagent transcript at the matched line.
	subResults, err := s.Search(SearchOptions{Query: "stegosaurus"})
	require.NoError(t, err)
	require.Len(t, subResults, 1)
	assert.Equal(t, "agent-1", subResults[0].SubagentID)

	// --role filter keeps tool/assistant rows out of a user-scoped search.
	roleResults, err := s.Search(SearchOptions{Query: "farewell", Role: "user"})
	require.NoError(t, err)
	assert.Empty(t, roleResults, "assistant row must not match --role user")
}

func TestVaultStore_CascadeDelete(t *testing.T) {
	s := newTestVault(t)
	uuid := "bbbbbbbb-1111-2222-3333-444444444444"
	require.NoError(t, s.InsertSession(sampleRecord(uuid)))

	db, err := s.getDB()
	require.NoError(t, err)

	var fileCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM vault_files WHERE session_uuid=?`, uuid).Scan(&fileCount))
	require.Equal(t, 2, fileCount)

	deleted, err := s.DeleteSession(uuid)
	require.NoError(t, err)
	assert.True(t, deleted)

	_, err = s.GetSession(uuid[:8])
	assert.ErrorIs(t, err, ErrSessionNotFound)

	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM vault_files WHERE session_uuid=?`, uuid).Scan(&fileCount))
	assert.Equal(t, 0, fileCount, "vault_files should cascade-delete with the session")

	results, err := s.Search(SearchOptions{Query: "brontosaurus"})
	require.NoError(t, err)
	assert.Empty(t, results, "vault_fts rows should be removed on delete")
}

func TestVaultStore_DeleteMissing(t *testing.T) {
	s := newTestVault(t)
	deleted, err := s.DeleteSession("does-not-exist")
	require.NoError(t, err)
	assert.False(t, deleted)
}

func TestVaultStore_ReplaceSession(t *testing.T) {
	s := newTestVault(t)
	uuid := "cccccccc-1111-2222-3333-444444444444"
	require.NoError(t, s.InsertSession(sampleRecord(uuid)))

	db, err := s.getDB()
	require.NoError(t, err)

	// Pin archived_at to a sentinel so we can prove the UPDATE preserves it.
	// The go-sqlite3 driver normalizes DATETIME columns to RFC3339 on read, so
	// the sentinel is written in that form to round-trip identically.
	const sentinel = "2000-01-01T00:00:00Z"
	_, err = db.Exec(`UPDATE vault_sessions SET archived_at=? WHERE uuid=?`, sentinel, uuid)
	require.NoError(t, err)

	replacement := &SessionRecord{
		Session: Session{
			UUID:             uuid,
			Title:            "Replaced title",
			StartTime:        time.Date(2026, 5, 2, 9, 0, 0, 0, time.UTC),
			EndTime:          time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC),
			MessageCount:     9,
			SizeBytes:        9999,
			ContentHash:      "newhash",
			MachineID:        "machine-b",
			ClaudeProjectDir: "-home-user-other",
			ProjectPath:      "/home/user/other",
			GitBranch:        "feature",
			RawJSONL:         []byte(`{"type":"user","text":"replaced"}` + "\n"),
		},
		Files: []File{
			{RelativePath: "subagents/agent-9.jsonl", RawContent: []byte(`{"type":"assistant"}`)},
		},
		FTS: []FTSRow{
			{SessionUUID: uuid, Role: "user", LineIndex: 0, ContentText: "pterodactyl"},
		},
	}
	require.NoError(t, s.ReplaceSession(replacement))

	got, err := s.GetSession(uuid[:8])
	require.NoError(t, err)
	assert.Equal(t, "Replaced title", got.Title)
	assert.Equal(t, "/home/user/other", got.ProjectPath)
	assert.Equal(t, "-home-user-other", got.ClaudeProjectDir)
	assert.Equal(t, "feature", got.GitBranch)
	assert.Equal(t, "machine-b", got.MachineID)
	assert.True(t, bytes.Equal([]byte(`{"type":"user","text":"replaced"}`+"\n"), got.RawJSONL), "raw_jsonl should be overwritten")
	assert.Equal(t, sentinel, got.ArchivedAt, "archived_at must survive replacement")

	// Files rebuilt: old two gone, new one present.
	files, err := s.GetFiles(uuid)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "subagents/agent-9.jsonl", files[0].RelativePath)

	// FTS rebuilt: old term gone, new term present.
	oldHits, err := s.Search(SearchOptions{Query: "brontosaurus"})
	require.NoError(t, err)
	assert.Empty(t, oldHits)
	newHits, err := s.Search(SearchOptions{Query: "pterodactyl"})
	require.NoError(t, err)
	require.Len(t, newHits, 1)
	assert.Equal(t, uuid, newHits[0].SessionUUID)
}

func TestVaultStore_GetSessionPartialMatch(t *testing.T) {
	s := newTestVault(t)
	uuidA := "dddddddd-1111-aaaa-0000-000000000001"
	uuidB := "dddddddd-1111-bbbb-0000-000000000002"
	require.NoError(t, s.InsertSession(sampleRecord(uuidA)))
	require.NoError(t, s.InsertSession(sampleRecord(uuidB)))

	// Unambiguous full prefix resolves.
	got, err := s.GetSession(uuidA)
	require.NoError(t, err)
	assert.Equal(t, uuidA, got.UUID)

	// Shared 8-char prefix is ambiguous, surfacing both candidates.
	_, err = s.GetSession("dddddddd")
	var ambErr *AmbiguousUUIDError
	require.ErrorAs(t, err, &ambErr)
	assert.Len(t, ambErr.Candidates, 2)

	// Below the minimum prefix length is rejected.
	_, err = s.GetSession("ddd")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrSessionNotFound)

	// A non-matching 8+ char prefix is a clean not-found.
	_, err = s.GetSession("eeeeeeee")
	assert.True(t, errors.Is(err, ErrSessionNotFound))
}
