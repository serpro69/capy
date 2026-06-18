package vault

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// largeCompressibleBytes is big and repetitive enough that zstd shrinks it
// dramatically AND its removal from the file is visible after VACUUM.
func largeCompressibleBytes() []byte {
	return bytes.Repeat([]byte(`{"type":"assistant","text":"the quick brown fox jumps over the lazy dog"}`+"\n"), 12000)
}

// incompressibleBytes returns deterministic high-entropy bytes that zstd cannot
// shrink, so encodeBlob stores them 'raw'. A fixed seed keeps the test stable.
func incompressibleBytes(n int) []byte {
	r := rand.New(rand.NewSource(42)) //nolint:gosec // deterministic test fixture, not security-sensitive
	b := make([]byte, n)
	_, _ = r.Read(b)
	return b
}

// insertLegacyRow writes a v1-shaped session straight to the tables with
// encoding left NULL — exactly what a pre-compression binary wrote — bypassing
// the codec so Compact has real legacy work to do.
func insertLegacyRow(t *testing.T, db *sql.DB, uuid string, main []byte, files map[string][]byte, ftsText string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO vault_sessions
		(uuid, message_count, size_bytes, content_hash, machine_id, claude_project_dir, project_path, raw_jsonl)
		VALUES (?, 1, ?, ?, 'machine-a', '-home-user-proj', '/home/user/proj', ?)`,
		uuid, int64(len(main)), "hash-"+uuid, main)
	require.NoError(t, err)
	for rel, content := range files {
		_, err = db.Exec(`INSERT INTO vault_files (session_uuid, relative_path, raw_content) VALUES (?, ?, ?)`,
			uuid, rel, content)
		require.NoError(t, err)
	}
	if ftsText != "" {
		_, err = db.Exec(`INSERT INTO vault_fts
			(content_text, session_uuid, subagent_id, turn_index, message_index, line_index, role)
			VALUES (?, ?, '', 0, 0, 0, 'assistant')`, ftsText, uuid)
		require.NoError(t, err)
	}
}

// vaultFileSize sums the on-disk footprint (main + WAL + SHM) of a vault.
func vaultFileSize(t *testing.T, path string) int64 {
	t.Helper()
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if info, err := os.Stat(path + suffix); err == nil {
			total += info.Size()
		}
	}
	return total
}

// TestVaultStore_Compact_RewritesLegacyBlobs is the headline path: a legacy
// (encoding IS NULL) session with a compressible sidecar and an incompressible
// sidecar is compacted. Every row ends 'zstd' or 'raw' (none left NULL), the
// incompressible blob round-trips byte-identical, the file shrinks, and search +
// show still return the same content.
func TestVaultStore_Compact_RewritesLegacyBlobs(t *testing.T) {
	s := newTestVault(t)
	ctx := context.Background()
	db, err := s.getDB(ctx)
	require.NoError(t, err)

	uuid := "1eadc0de-0000-0000-0000-000000000001"
	main := largeCompressibleBytes()
	compFile := largeCompressibleBytes()
	rawFile := incompressibleBytes(4096)
	insertLegacyRow(t, db, uuid, main, map[string][]byte{
		"subagents/agent-1.jsonl": compFile,
		"tool-results/binary.bin": rawFile,
	}, "pterodactyl legacy transcript")

	before := vaultFileSize(t, s.dbPath)

	res, err := s.Compact(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, res.SessionsRewritten)
	assert.Equal(t, 2, res.FilesRewritten)
	assert.True(t, res.Vacuumed, "a rewrite must trigger VACUUM")

	after := vaultFileSize(t, s.dbPath)
	assert.Less(t, after, before, "VACUUM should reclaim the freed pages")

	// No row may be left NULL — that is the terminal state Compact guarantees.
	require.NoError(t, s.Open(ctx)) // Compact closed the pool; reopen for assertions
	assert.Equal(t, 0, countNullEncoding(t, s, ctx, "vault_sessions"))
	assert.Equal(t, 0, countNullEncoding(t, s, ctx, "vault_files"))

	// The compressible session/file became 'zstd'; the incompressible file 'raw'.
	assert.Equal(t, encodingZstd, sessionEncoding(t, s, ctx, uuid))
	assert.Equal(t, encodingZstd, fileEncoding(t, s, ctx, uuid, "subagents/agent-1.jsonl"))
	assert.Equal(t, encodingRaw, fileEncoding(t, s, ctx, uuid, "tool-results/binary.bin"))

	// show: blobs round-trip byte-identical despite being stored compressed.
	got, err := s.GetSession(ctx, uuid)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(main, got.RawJSONL), "session transcript round-trips after compact")

	files, err := s.GetFiles(ctx, uuid)
	require.NoError(t, err)
	byPath := map[string][]byte{}
	for _, f := range files {
		byPath[f.RelativePath] = f.RawContent
	}
	assert.True(t, bytes.Equal(compFile, byPath["subagents/agent-1.jsonl"]), "compressible sidecar round-trips")
	assert.True(t, bytes.Equal(rawFile, byPath["tool-results/binary.bin"]), "incompressible raw sidecar round-trips byte-identical")

	// search: the legacy session's FTS rows are untouched by compact.
	hits, err := s.Search(ctx, SearchOptions{Query: "pterodactyl"})
	require.NoError(t, err)
	require.Len(t, hits, 1)
	assert.Equal(t, uuid, hits[0].SessionUUID)

	// A compressed write must stamp the forward-compat marker (design.md §1).
	assert.Equal(t, "2", metaValue(t, s, ctx, minReaderVersionKey))
}

// TestVaultStore_Compact_SecondRunIsNoOp proves Compact is idempotent: once every
// blob carries an encoding, a second run rewrites nothing and skips VACUUM.
func TestVaultStore_Compact_SecondRunIsNoOp(t *testing.T) {
	s := newTestVault(t)
	ctx := context.Background()
	db, err := s.getDB(ctx)
	require.NoError(t, err)

	insertLegacyRow(t, db, "2eadc0de-0000-0000-0000-000000000002", largeCompressibleBytes(),
		map[string][]byte{"f.txt": []byte("legacy file contents")}, "")

	first, err := s.Compact(ctx)
	require.NoError(t, err)
	require.Greater(t, first.SessionsRewritten, 0)

	second, err := s.Compact(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, second.SessionsRewritten)
	assert.Equal(t, 0, second.FilesRewritten)
	assert.False(t, second.Vacuumed, "a no-op compact must not VACUUM")
}

// TestVaultStore_Compact_MultipleBatches exercises the batch loop boundary: more
// legacy rows than compactBatchSize must all be rewritten across several
// transactions, leaving none NULL. The tiny blobs stay 'raw' (incompressible), so
// this also covers the all-raw rewrite path (rewritten > 0 still VACUUMs).
func TestVaultStore_Compact_MultipleBatches(t *testing.T) {
	s := newTestVault(t)
	ctx := context.Background()
	db, err := s.getDB(ctx)
	require.NoError(t, err)

	const n = compactBatchSize*2 + 20 // 120 → batches of 50, 50, 20
	for i := 0; i < n; i++ {
		uuid := fmt.Sprintf("badc0de0-0000-0000-0000-%012d", i)
		insertLegacyRow(t, db, uuid, []byte(`{"type":"user","text":"x"}`+"\n"), nil, "")
	}

	res, err := s.Compact(ctx)
	require.NoError(t, err)
	assert.Equal(t, n, res.SessionsRewritten)
	assert.True(t, res.Vacuumed)

	require.NoError(t, s.Open(ctx))
	assert.Equal(t, 0, countNullEncoding(t, s, ctx, "vault_sessions"), "every legacy row must be rewritten across batches")
}

// TestVaultStore_Compact_NoCompressEnvErrors proves Compact refuses to run while
// CAPY_VAULT_NO_COMPRESS is set and leaves the DB untouched (the legacy row stays
// NULL — nothing was rewritten).
func TestVaultStore_Compact_NoCompressEnvErrors(t *testing.T) {
	s := newTestVault(t)
	ctx := context.Background()
	db, err := s.getDB(ctx)
	require.NoError(t, err)

	uuid := "3eadc0de-0000-0000-0000-000000000003"
	insertLegacyRow(t, db, uuid, largeCompressibleBytes(), nil, "")

	t.Setenv(noCompressEnv, "1")
	_, err = s.Compact(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), noCompressEnv)

	// DB unmodified: the legacy row is still NULL-encoded (no rewrite happened).
	assert.Equal(t, 1, countNullEncoding(t, s, ctx, "vault_sessions"))
}

// --- small query helpers (package-internal, test-only) ---------------------

func countNullEncoding(t *testing.T, s *VaultStore, ctx context.Context, table string) int {
	t.Helper()
	db, err := s.getDB(ctx)
	require.NoError(t, err)
	var n int
	//nolint:gosec // table is a test-controlled constant, never user input
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE encoding IS NULL`).Scan(&n))
	return n
}

func sessionEncoding(t *testing.T, s *VaultStore, ctx context.Context, uuid string) string {
	t.Helper()
	db, err := s.getDB(ctx)
	require.NoError(t, err)
	var enc sql.NullString
	require.NoError(t, db.QueryRow(`SELECT encoding FROM vault_sessions WHERE uuid = ?`, uuid).Scan(&enc))
	return enc.String
}

func fileEncoding(t *testing.T, s *VaultStore, ctx context.Context, uuid, rel string) string {
	t.Helper()
	db, err := s.getDB(ctx)
	require.NoError(t, err)
	var enc sql.NullString
	require.NoError(t, db.QueryRow(`SELECT encoding FROM vault_files WHERE session_uuid = ? AND relative_path = ?`, uuid, rel).Scan(&enc))
	return enc.String
}

func metaValue(t *testing.T, s *VaultStore, ctx context.Context, key string) string {
	t.Helper()
	db, err := s.getDB(ctx)
	require.NoError(t, err)
	var v string
	require.NoError(t, db.QueryRow(`SELECT value FROM vault_meta WHERE key = ?`, key).Scan(&v))
	return v
}
