package vault

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// zstdMagic is the 4-byte zstd frame header (0xFD2FB528, little-endian on disk).
// A compressed blob must begin with it; the regression test below proves a RAW
// sidecar that merely starts with these bytes is never mistaken for one.
var zstdMagic = []byte{0x28, 0xB5, 0x2F, 0xFD}

// compressibleBytes returns a blob large and repetitive enough that zstd shrinks
// it well below its original size.
func compressibleBytes() []byte {
	return []byte(strings.Repeat(`{"type":"assistant","text":"the quick brown fox jumps over the lazy dog"}`+"\n", 200))
}

func TestEncodeBlob_CompressibleUsesZstd(t *testing.T) {
	src := compressibleBytes()
	data, enc := encodeBlob(src)
	assert.Equal(t, encodingZstd, enc)
	assert.Less(t, len(data), len(src), "compressed blob should be smaller")
	assert.True(t, bytes.HasPrefix(data, zstdMagic), "zstd blob should carry the frame magic")

	got, err := decodeBlob(enc, data)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(src, got), "round-trip must be byte-identical")
}

func TestEncodeBlob_IncompressibleStaysRaw(t *testing.T) {
	// A tiny blob and a blob that starts with the zstd magic but is too small to
	// shrink both stay raw — encodeBlob never returns bytes larger than its input.
	for name, src := range map[string][]byte{
		"tiny":           []byte("{}"),
		"magic-prefixed": append(append([]byte{}, zstdMagic...), []byte("xy")...),
		"empty":          {},
	} {
		t.Run(name, func(t *testing.T) {
			data, enc := encodeBlob(src)
			assert.Equal(t, encodingRaw, enc)
			assert.True(t, bytes.Equal(src, data), "raw encoding stores the bytes verbatim")
			got, err := decodeBlob(enc, data)
			require.NoError(t, err)
			assert.True(t, bytes.Equal(src, got))
		})
	}
}

func TestEncodeBlob_NoCompressEnvForcesRaw(t *testing.T) {
	t.Setenv(noCompressEnv, "1")
	src := compressibleBytes()
	data, enc := encodeBlob(src)
	assert.Equal(t, encodingRaw, enc, "CAPY_VAULT_NO_COMPRESS must disable compression")
	assert.True(t, bytes.Equal(src, data))
}

func TestDecodeBlob_EncodingDiscriminator(t *testing.T) {
	// NULL (legacy v1) and explicit "raw" both pass bytes through untouched —
	// even bytes that happen to begin with the zstd magic, which magic-byte
	// detection would have mis-decompressed (the bug the column prevents).
	rawSidecar := append(append([]byte{}, zstdMagic...), []byte("arbitrary sidecar payload")...)
	for _, enc := range []string{"", encodingRaw} {
		got, err := decodeBlob(enc, rawSidecar)
		require.NoError(t, err)
		assert.True(t, bytes.Equal(rawSidecar, got), "encoding %q must be a verbatim passthrough", enc)
	}

	// An unrecognized encoding fails loud rather than silently passing bytes back.
	_, err := decodeBlob("brotli", []byte("data"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown blob encoding")
}

// TestVaultStore_CompressionRoundTrip proves the write→store→read path: a
// compressible session and sidecar are stored zstd-compressed (smaller, carrying
// the frame magic) yet GetSession/GetFiles return byte-identical originals, and
// content_hash/size_bytes remain the UNCOMPRESSED values.
func TestVaultStore_CompressionRoundTrip(t *testing.T) {
	s := newTestVault(t)
	ctx := context.Background()
	uuid := "feedface-0000-0000-0000-000000000001"

	mainRaw := compressibleBytes()
	fileRaw := compressibleBytes()
	contents := map[string][]byte{
		uuid + ".jsonl":           mainRaw,
		"subagents/agent-1.jsonl": fileRaw,
	}
	hash, size := computeContentHash(contents)

	rec := &SessionRecord{
		Session: Session{
			UUID: uuid, Title: "compressible", MessageCount: 1,
			SizeBytes: size, ContentHash: hash, MachineID: "machine-a",
			ClaudeProjectDir: "-home-user-proj", ProjectPath: "/home/user/proj",
			IndexVersion: currentIndexVersion, RawJSONL: mainRaw,
		},
		Files: []File{{RelativePath: "subagents/agent-1.jsonl", RawContent: fileRaw}},
		FTS:   []FTSRow{{SessionUUID: uuid, Role: "assistant", ContentText: "round trip"}},
	}
	require.NoError(t, s.InsertSession(ctx, rec))

	// Read back: blobs are byte-identical despite being stored compressed.
	got, err := s.GetSession(ctx, uuid)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(mainRaw, got.RawJSONL), "raw_jsonl round-trips byte-identical")
	assert.Equal(t, hash, got.ContentHash, "content_hash unchanged by compression")
	assert.Equal(t, size, got.SizeBytes, "size_bytes is the uncompressed total")
	assert.Equal(t, int64(len(mainRaw)+len(fileRaw)), got.SizeBytes, "size_bytes counts uncompressed bytes")

	files, err := s.GetFiles(ctx, uuid)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.True(t, bytes.Equal(fileRaw, files[0].RawContent), "raw_content round-trips byte-identical")

	// Inspect the raw stored bytes: compressed, smaller, magic-prefixed.
	db, err := s.getDB(ctx)
	require.NoError(t, err)
	var sessEnc, fileEnc sql.NullString
	var sessBlob, fileBlob []byte
	require.NoError(t, db.QueryRow(`SELECT encoding, raw_jsonl FROM vault_sessions WHERE uuid=?`, uuid).Scan(&sessEnc, &sessBlob))
	require.NoError(t, db.QueryRow(`SELECT encoding, raw_content FROM vault_files WHERE session_uuid=?`, uuid).Scan(&fileEnc, &fileBlob))
	assert.Equal(t, encodingZstd, sessEnc.String)
	assert.Equal(t, encodingZstd, fileEnc.String)
	assert.True(t, bytes.HasPrefix(sessBlob, zstdMagic))
	assert.Less(t, len(sessBlob), len(mainRaw), "stored session blob is compressed")
	assert.Less(t, len(fileBlob), len(fileRaw), "stored file blob is compressed")
}

// TestVaultStore_MixedCorpusReadsLegacyAndCompressed proves a vault holding both a
// hand-inserted legacy (encoding IS NULL) row and a normally-written compressed row
// reads each correctly — the upgrade-in-place scenario.
func TestVaultStore_MixedCorpusReadsLegacyAndCompressed(t *testing.T) {
	s := newTestVault(t)
	ctx := context.Background()
	db, err := s.getDB(ctx)
	require.NoError(t, err)

	// Legacy row: raw bytes, encoding left NULL — exactly what a v1 binary wrote.
	legacyUUID := "0ldec0de-0000-0000-0000-000000000001"
	legacyRaw := []byte(`{"type":"user","text":"legacy v1 row"}` + "\n")
	_, err = db.Exec(`INSERT INTO vault_sessions
		(uuid, message_count, size_bytes, content_hash, machine_id, claude_project_dir, project_path, raw_jsonl)
		VALUES (?, 1, ?, 'h', 'm', 'd', '/p', ?)`, legacyUUID, int64(len(legacyRaw)), legacyRaw)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO vault_files (session_uuid, relative_path, raw_content) VALUES (?, 'f.txt', ?)`,
		legacyUUID, []byte("legacy file"))
	require.NoError(t, err)

	// Compressed row via the normal write path.
	compUUID := "c0mpec0d-0000-0000-0000-000000000002"
	compRaw := compressibleBytes()
	rec := sampleRecord(compUUID)
	rec.Session.RawJSONL = compRaw
	require.NoError(t, s.InsertSession(ctx, rec))

	gotLegacy, err := s.GetSession(ctx, legacyUUID)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(legacyRaw, gotLegacy.RawJSONL), "NULL-encoding legacy row reads as raw")
	legacyFiles, err := s.GetFiles(ctx, legacyUUID)
	require.NoError(t, err)
	require.Len(t, legacyFiles, 1)
	assert.Equal(t, []byte("legacy file"), legacyFiles[0].RawContent)

	gotComp, err := s.GetSession(ctx, compUUID)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(compRaw, gotComp.RawJSONL), "compressed row round-trips")
}

// TestVaultStore_RawSidecarWithZstdMagicRoundTrips is the regression that magic-byte
// detection would have failed: a raw sidecar whose first bytes ARE the zstd magic
// must round-trip byte-identical. encodeBlob keeps it raw (incompressible), and
// decodeBlob trusts the encoding column, never the leading bytes.
func TestVaultStore_RawSidecarWithZstdMagicRoundTrips(t *testing.T) {
	s := newTestVault(t)
	ctx := context.Background()
	uuid := "5afec0de-0000-0000-0000-000000000003"

	// 4 magic bytes + short incompressible tail → encodeBlob declines compression.
	sidecar := append(append([]byte{}, zstdMagic...), []byte{0x01, 0x9f, 0x42, 0x7e, 0x00}...)
	rec := sampleRecord(uuid)
	rec.Files = []File{{RelativePath: "tool-results/binary.bin", RawContent: sidecar}}
	require.NoError(t, s.InsertSession(ctx, rec))

	// It must be stored raw (else the test wouldn't exercise the magic-byte hazard).
	db, err := s.getDB(ctx)
	require.NoError(t, err)
	var enc sql.NullString
	require.NoError(t, db.QueryRow(`SELECT encoding FROM vault_files WHERE session_uuid=?`, uuid).Scan(&enc))
	require.Equal(t, encodingRaw, enc.String, "magic-prefixed incompressible sidecar must be stored raw")

	files, err := s.GetFiles(ctx, uuid)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.True(t, bytes.Equal(sidecar, files[0].RawContent), "raw sidecar starting with zstd magic must round-trip unchanged")
}

func TestVaultStore_MinReaderVersionSetAfterCompressedWrite(t *testing.T) {
	ctx := context.Background()

	t.Run("compressed write stamps the marker", func(t *testing.T) {
		s := newTestVault(t)
		rec := sampleRecord("11111111-0000-0000-0000-00000000000a")
		rec.Session.RawJSONL = compressibleBytes()
		require.NoError(t, s.InsertSession(ctx, rec))

		db, err := s.getDB(ctx)
		require.NoError(t, err)
		var v string
		require.NoError(t, db.QueryRow(`SELECT value FROM vault_meta WHERE key=?`, minReaderVersionKey).Scan(&v))
		assert.Equal(t, fmt.Sprintf("%d", supportedReaderVersion), v)
	})

	t.Run("no-compress write leaves the marker absent", func(t *testing.T) {
		t.Setenv(noCompressEnv, "1")
		s := newTestVault(t)
		rec := sampleRecord("22222222-0000-0000-0000-00000000000b")
		rec.Session.RawJSONL = compressibleBytes()
		require.NoError(t, s.InsertSession(ctx, rec))

		db, err := s.getDB(ctx)
		require.NoError(t, err)
		var v string
		err = db.QueryRow(`SELECT value FROM vault_meta WHERE key=?`, minReaderVersionKey).Scan(&v)
		assert.ErrorIs(t, err, sql.ErrNoRows, "raw-only write must not stamp min_reader_version")
	})
}

// TestVaultStore_ReaderVersionGate proves openDB refuses a vault marked for a newer
// reader version, while a supported or absent marker opens cleanly.
func TestVaultStore_ReaderVersionGate(t *testing.T) {
	t.Setenv(vaultKeyEnv, testVaultKey)
	ctx := context.Background()

	setMarker := func(t *testing.T, path, value string) {
		t.Helper()
		s := NewVaultStore(path)
		require.NoError(t, s.InsertSession(ctx, sampleRecord("33333333-0000-0000-0000-00000000000c")))
		db, err := s.getDB(ctx)
		require.NoError(t, err)
		_, err = db.Exec(`INSERT OR REPLACE INTO vault_meta (key, value) VALUES (?, ?)`, minReaderVersionKey, value)
		require.NoError(t, err)
		require.NoError(t, s.Close())
	}

	t.Run("future version is refused", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "vault.db")
		setMarker(t, path, fmt.Sprintf("%d", supportedReaderVersion+1))

		err := NewVaultStore(path).Open(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "requires reader version")
	})

	t.Run("supported version opens", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "vault.db")
		setMarker(t, path, fmt.Sprintf("%d", supportedReaderVersion))

		s := NewVaultStore(path)
		t.Cleanup(func() { _ = s.Close() })
		require.NoError(t, s.Open(ctx))
	})
}
