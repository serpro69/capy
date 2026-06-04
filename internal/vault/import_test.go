package vault

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// importFixture discovers and imports a single session under root, returning the
// result and the session UUID.
func importFixture(t *testing.T, s *VaultStore, root string, opts ImportOptions) ImportResult {
	t.Helper()
	sessions, err := DiscoverSessions(root)
	require.NoError(t, err)
	return Import(context.Background(), s, sessions, opts)
}

func TestImport_InsertsSessionWithMetadataAndFTS(t *testing.T) {
	s := newTestVault(t)
	root := t.TempDir()
	uuid := "11111111-2222-3333-4444-555555555555"
	writeSession(t, filepath.Join(root, "-home-user-proj"), uuid, sampleMainJSONL(t), map[string][]byte{
		"subagents/agent-abc.jsonl": jsonlBytes(t,
			userLine("su1", "/home/user/proj", "feature/x", "explore the codebase for triceratops")),
		"tool-results/t1.json": []byte(`{"ok":true}`),
	})

	res := importFixture(t, s, root, ImportOptions{})
	assert.Equal(t, 1, res.Imported)
	assert.Equal(t, 0, res.Skipped)
	require.Len(t, res.Sessions, 1)
	assert.Equal(t, StatusNew, res.Sessions[0].Status)
	assert.Equal(t, "Fix the timeout bug", res.Sessions[0].Title)

	got, err := s.GetSession(uuid[:8])
	require.NoError(t, err)
	assert.Equal(t, "Fix the timeout bug", got.Title)
	assert.Equal(t, "feature/x", got.GitBranch)
	assert.Equal(t, "/home/user/proj", got.ProjectPath, "project_path comes from the JSONL cwd")
	assert.Equal(t, "-home-user-proj", got.ClaudeProjectDir)
	assert.Equal(t, MachineID(), got.MachineID)
	assert.Equal(t, 2, got.MessageCount, "human user + assistant; tool_result-only user excluded")
	assert.NotEmpty(t, got.ContentHash)
	assert.Greater(t, got.SizeBytes, int64(0))
	assert.False(t, got.StartTime.IsZero())
	assert.False(t, got.EndTime.IsZero())
	assert.True(t, bytes.Equal(sampleMainJSONL(t), got.RawJSONL), "raw_jsonl preserved verbatim")

	files, err := s.GetFiles(uuid)
	require.NoError(t, err)
	require.Len(t, files, 2, "subagent + tool-result preserved")

	// tool_result text indexes as role="tool", kept out of --role user.
	toolHits, err := s.Search(SearchOptions{Query: "pterodactyl", Role: "tool"})
	require.NoError(t, err)
	require.Len(t, toolHits, 1)
	userOnly, err := s.Search(SearchOptions{Query: "pterodactyl", Role: "user"})
	require.NoError(t, err)
	assert.Empty(t, userOnly, "tool output must not appear under --role user")

	// Subagent content is searchable and carries the subagent_id anchor.
	subHits, err := s.Search(SearchOptions{Query: "triceratops"})
	require.NoError(t, err)
	require.Len(t, subHits, 1)
	assert.Equal(t, "abc", subHits[0].SubagentID)
}

func TestImport_IdempotentSkipsUnchanged(t *testing.T) {
	s := newTestVault(t)
	root := t.TempDir()
	writeSession(t, filepath.Join(root, "-home-user-proj"), "11111111-2222-3333-4444-555555555555",
		sampleMainJSONL(t), nil)

	first := importFixture(t, s, root, ImportOptions{})
	assert.Equal(t, 1, first.Imported)

	second := importFixture(t, s, root, ImportOptions{})
	assert.Equal(t, 0, second.Imported)
	assert.Equal(t, 1, second.Skipped)
	require.Len(t, second.Sessions, 1)
	assert.Equal(t, StatusSkipped, second.Sessions[0].Status)
}

func TestImport_LargerTotalReplaces_ShrinkingMainGrownSidecar(t *testing.T) {
	s := newTestVault(t)
	root := t.TempDir()
	projDir := filepath.Join(root, "-home-user-proj")
	uuid := "11111111-2222-3333-4444-555555555555"

	// v1: full main JSONL, small sidecar.
	writeSession(t, projDir, uuid, sampleMainJSONL(t),
		map[string][]byte{"subagents/agent-abc.jsonl": []byte(`{"type":"user"}` + "\n")})
	require.Equal(t, 1, importFixture(t, s, root, ImportOptions{}).Imported)

	// Pin archived_at so we can prove the replace UPDATE preserves it.
	db, err := s.getDB()
	require.NoError(t, err)
	const sentinel = "2000-01-01T00:00:00Z"
	_, err = db.Exec(`UPDATE vault_sessions SET archived_at=? WHERE uuid=?`, sentinel, uuid)
	require.NoError(t, err)

	// v2: main shrinks, but a grown subagent makes the TOTAL content larger —
	// must replace, not skip (the tiebreaker is total size, not main size).
	smallerMain := jsonlBytes(t, userLine("u1", "/home/user/proj", "feature/x", "short"))
	grownSidecar := bytes.Repeat([]byte("x"), 4096)
	require.Less(t, len(smallerMain), len(sampleMainJSONL(t)))
	writeSession(t, projDir, uuid, smallerMain,
		map[string][]byte{"subagents/agent-abc.jsonl": grownSidecar})

	res := importFixture(t, s, root, ImportOptions{})
	assert.Equal(t, 1, res.Updated)
	assert.Equal(t, 0, res.Skipped)

	got, err := s.GetSession(uuid[:8])
	require.NoError(t, err)
	assert.True(t, bytes.Equal(smallerMain, got.RawJSONL), "raw_jsonl overwritten with the smaller main")
	assert.Equal(t, sentinel, got.ArchivedAt, "archived_at survives replacement")

	files, err := s.GetFiles(uuid)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.True(t, bytes.Equal(grownSidecar, files[0].RawContent), "sidecar rebuilt with grown content")
}

func TestImport_SmallerTotalSkipped(t *testing.T) {
	s := newTestVault(t)
	root := t.TempDir()
	projDir := filepath.Join(root, "-home-user-proj")
	uuid := "11111111-2222-3333-4444-555555555555"

	writeSession(t, projDir, uuid, sampleMainJSONL(t),
		map[string][]byte{"tool-results/t1.json": bytes.Repeat([]byte("y"), 2048)})
	require.Equal(t, 1, importFixture(t, s, root, ImportOptions{}).Imported)

	// Shrink total: same main, drop the sidecar (different hash, smaller total)
	// → treated as a likely-compacted variant and skipped.
	writeSession(t, projDir, uuid, sampleMainJSONL(t), nil)
	require.NoError(t, removeSidecar(t, projDir, uuid, "tool-results/t1.json"))

	res := importFixture(t, s, root, ImportOptions{})
	assert.Equal(t, 0, res.Updated)
	assert.Equal(t, 1, res.Skipped)
}

func TestImport_SubagentChangeDetectedByCompositeHash(t *testing.T) {
	s := newTestVault(t)
	root := t.TempDir()
	projDir := filepath.Join(root, "-home-user-proj")
	uuid := "11111111-2222-3333-4444-555555555555"

	writeSession(t, projDir, uuid, sampleMainJSONL(t),
		map[string][]byte{"subagents/agent-abc.jsonl": []byte(`{"type":"user"}` + "\n")})
	require.Equal(t, 1, importFixture(t, s, root, ImportOptions{}).Imported)

	// Only the subagent changes (and grows). The composite hash + total size
	// cover sidecars, so this is detected as a replace.
	writeSession(t, projDir, uuid, sampleMainJSONL(t), map[string][]byte{
		"subagents/agent-abc.jsonl": jsonlBytes(t,
			userLine("su1", "/home/user/proj", "feature/x", "new subagent content velociraptor")),
	})

	res := importFixture(t, s, root, ImportOptions{})
	assert.Equal(t, 1, res.Updated)

	hits, err := s.Search(SearchOptions{Query: "velociraptor"})
	require.NoError(t, err)
	require.Len(t, hits, 1)
	assert.Equal(t, "abc", hits[0].SubagentID)
}

func TestImport_DryRunWritesNothing(t *testing.T) {
	s := newTestVault(t)
	root := t.TempDir()
	writeSession(t, filepath.Join(root, "-home-user-proj"), "11111111-2222-3333-4444-555555555555",
		sampleMainJSONL(t), nil)

	res := importFixture(t, s, root, ImportOptions{DryRun: true})
	assert.Equal(t, 1, res.Imported)
	require.Len(t, res.Sessions, 1)
	assert.Equal(t, StatusNew, res.Sessions[0].Status)

	listed, err := s.ListSessions(ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, listed, "dry-run must not write any sessions")
}

func TestImport_ProjectFilter(t *testing.T) {
	s := newTestVault(t)
	root := t.TempDir()
	writeSession(t, filepath.Join(root, "-home-user-proja"), "aaaaaaaa-2222-3333-4444-555555555555",
		sampleMainJSONL(t), nil)
	writeSession(t, filepath.Join(root, "-home-user-projb"), "bbbbbbbb-2222-3333-4444-555555555555",
		sampleMainJSONL(t), nil)

	res := importFixture(t, s, root, ImportOptions{Project: "proja"})
	assert.Equal(t, 1, res.Imported)
	require.Len(t, res.Sessions, 1)
	assert.Equal(t, "aaaaaaaa-2222-3333-4444-555555555555", res.Sessions[0].UUID)

	listed, err := s.ListSessions(ListOptions{})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, "-home-user-proja", listed[0].ClaudeProjectDir)
}

func TestMachineSummary_MismatchDetection(t *testing.T) {
	s := newTestVault(t)
	rec := sampleRecord("99999999-2222-3333-4444-555555555555")
	rec.Session.MachineID = "machine-x"
	require.NoError(t, s.InsertSession(rec))

	total, matching, err := s.MachineSummary("machine-y")
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Equal(t, 0, matching, "no row matches a foreign machine → warn-worthy")

	total, matching, err = s.MachineSummary("machine-x")
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Equal(t, 1, matching)
}

func TestImport_ReadErrorRecorded(t *testing.T) {
	s := newTestVault(t)
	// A discovered file that vanished before import → recorded as an error, not
	// a panic, and the run continues.
	bad := []SessionFile{{
		Path:       filepath.Join(t.TempDir(), "gone.jsonl"),
		UUID:       "deadbeef-1111-2222-3333-444444444444",
		ProjectDir: "-home-user-proj",
	}}
	res := Import(context.Background(), s, bad, ImportOptions{})
	assert.Equal(t, 1, res.Errors)
	require.Len(t, res.Sessions, 1)
	assert.Equal(t, StatusError, res.Sessions[0].Status)
	require.Error(t, res.Sessions[0].Err)
}

// A context cancelled before Import enters its loop stops at the first session
// boundary: nothing is scanned, batched, or written. (A cancellation that fires
// mid-batch instead still flushes the already-accumulated batch via the final
// flush — this test covers the pre-cancelled case.) This is the cooperative
// cancellation the server-startup sweep relies on so a shutdown mid-sweep does
// not block bgWg.Wait().
func TestImport_PreCancelledContextImportsNothing(t *testing.T) {
	s := newTestVault(t)
	root := t.TempDir()
	uuid := "11111111-2222-3333-4444-555555555555"
	writeSession(t, filepath.Join(root, "-home-user-proj"), uuid, sampleMainJSONL(t), nil)

	sessions, err := DiscoverSessions(root)
	require.NoError(t, err)
	require.Len(t, sessions, 1)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before Import runs

	res := Import(ctx, s, sessions, ImportOptions{})
	assert.Equal(t, 0, res.Imported)
	assert.Equal(t, 0, res.Updated)
	assert.Empty(t, res.Sessions, "a pre-cancelled import records no per-session outcomes")

	_, err = s.GetSession(uuid[:8])
	assert.ErrorIs(t, err, ErrSessionNotFound, "no session row should have been written")
}

func TestImport_CrossesBatchBoundary(t *testing.T) {
	s := newTestVault(t)
	projDir := filepath.Join(t.TempDir(), "-home-user-proj")

	// One more than maxBatchSessions so the importer flushes twice.
	const n = maxBatchSessions + 1
	for i := range n {
		uuid := fmt.Sprintf("%08d-1111-2222-3333-444444444444", i)
		writeSession(t, projDir, uuid, sampleMainJSONL(t), nil)
	}

	res := importFixture(t, s, projDir, ImportOptions{})
	assert.Equal(t, n, res.Imported)
	assert.Equal(t, 0, res.Errors)

	listed, err := s.ListSessions(ListOptions{})
	require.NoError(t, err)
	assert.Len(t, listed, n, "every session across both batches is persisted")
}

func TestImport_ProjectPathFallsBackToMangled(t *testing.T) {
	s := newTestVault(t)
	root := t.TempDir()
	// A user line with no cwd, under a mangled dir that resolves to nothing on
	// this filesystem → project_path falls back to the raw mangled name.
	mangled := "-no-such-dir-xyz"
	uuid := "abcd1234-1111-2222-3333-444444444444"
	main := jsonlBytes(t, map[string]any{
		"type": "user", "uuid": "u1", "timestamp": "2026-05-01T10:00:00Z",
		"message": map[string]any{"role": "user", "content": "hello world"},
	})
	writeSession(t, filepath.Join(root, mangled), uuid, main, nil)

	require.Equal(t, 1, importFixture(t, s, root, ImportOptions{}).Imported)

	got, err := s.GetSession(uuid[:8])
	require.NoError(t, err)
	assert.Equal(t, mangled, got.ProjectPath, "no cwd + unresolvable mangle → raw mangled name")
	assert.Empty(t, got.GitBranch, "absent gitBranch stays NULL/empty")
}

func TestSubagentID(t *testing.T) {
	tests := []struct {
		name, rel, want string
	}{
		{"valid subagent", "subagents/agent-abc123.jsonl", "abc123"},
		{"non-subagent sidecar", "tool-results/t1.json", ""},
		{"subagent dir but no agent- prefix", "subagents/notes.jsonl", ""},
		{"top-level jsonl", "main.jsonl", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, subagentID(tt.rel))
		})
	}
}

func TestComputeContentHash_FramingDistinguishesBoundaries(t *testing.T) {
	// Same concatenated bytes, different key/content split → must differ thanks
	// to length-prefix framing.
	a, sizeA := computeContentHash(map[string][]byte{"ab": []byte("c"), "x": []byte("yz")})
	b, _ := computeContentHash(map[string][]byte{"a": []byte("bc"), "xy": []byte("z")})
	assert.NotEqual(t, a, b)
	assert.Equal(t, int64(3), sizeA, "size sums content bytes only (not keys)")

	// Deterministic regardless of map iteration order.
	again, _ := computeContentHash(map[string][]byte{"x": []byte("yz"), "ab": []byte("c")})
	assert.Equal(t, a, again)
}

// removeSidecar deletes a session-relative sidecar file from disk.
func removeSidecar(t *testing.T, projectDir, uuid, rel string) error {
	t.Helper()
	return os.Remove(filepath.Join(projectDir, uuid, filepath.FromSlash(rel)))
}
