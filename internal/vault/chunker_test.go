package vault

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/serpro69/capy/internal/store"
)

var chunkerBase = time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)

// mkTurnResults builds one ScanResult per turn, each of roughly contentBytes of
// text, with LineIndex = turn*10 so line anchors are distinguishable.
func mkTurnResults(turns, contentBytes int) []ScanResult {
	results := make([]ScanResult, 0, turns)
	for t := range turns {
		word := fmt.Sprintf("turn%02d ", t)
		results = append(results, ScanResult{
			TurnIndex:   t,
			LineIndex:   t * 10,
			Role:        roleUser,
			ContentText: strings.TrimSpace(strings.Repeat(word, contentBytes/len(word)+1))[:contentBytes],
			Timestamp:   chunkerBase.Add(time.Duration(t) * time.Minute),
		})
	}
	return results
}

func TestChunkScanResults_SmallTranscriptSingleChunk(t *testing.T) {
	results := []ScanResult{
		{TurnIndex: 0, LineIndex: 3, Role: roleUser, ContentText: "fix the flaky quokka test", Timestamp: chunkerBase},
		{TurnIndex: 0, LineIndex: 4, Role: roleAssistant, ContentText: "Bash go test ./...", ToolNames: []string{"Bash"}},
		{TurnIndex: 1, LineIndex: 7, Role: roleUser, ContentText: "now update the docs"},
	}

	chunks := chunkScanResults(results, chunkerBase, "")
	require.Len(t, chunks, 1, "a transcript under MaxChunkBytes is one full-range chunk")

	c := chunks[0]
	assert.Equal(t, 3, c.FirstLineIndex, "anchor is the first result's raw-JSONL line")
	assert.Empty(t, c.SubagentID)
	assert.Contains(t, c.Title, "Session 2026-05-01T10:00:00Z")
	assert.Contains(t, c.Title, "Turns 1-2")
	assert.Contains(t, c.Title, "Tools: Bash")
	assert.Contains(t, c.ContentText, "quokka")
	assert.Contains(t, c.ContentText, "update the docs")
}

func TestChunkScanResults_WindowSizingAndOverlap(t *testing.T) {
	// 6 turns × ~700B ≈ 4.2KB total: over MaxChunkBytes so windowing engages,
	// but each 4-turn window (~2.8KB) stays under the split threshold. With
	// window=4/overlap=1 (step 3) that must yield windows [0-3] and [3-5] —
	// turn 3 appearing in both is the overlap.
	results := mkTurnResults(6, 700)

	chunks := chunkScanResults(results, chunkerBase, "")
	require.Len(t, chunks, 2)

	assert.Contains(t, chunks[0].Title, "Turns 1-4")
	assert.Equal(t, 0, chunks[0].FirstLineIndex)
	assert.Contains(t, chunks[0].ContentText, "turn00")
	assert.Contains(t, chunks[0].ContentText, "turn03")
	assert.NotContains(t, chunks[0].ContentText, "turn04")

	assert.Contains(t, chunks[1].Title, "Turns 4-6")
	assert.Equal(t, 30, chunks[1].FirstLineIndex, "second window anchors at turn 3's line")
	assert.Contains(t, chunks[1].ContentText, "turn03", "overlap: the window shares one turn with its predecessor")
	assert.Contains(t, chunks[1].ContentText, "turn05")
}

func TestChunkScanResults_OversizedWindowSplits(t *testing.T) {
	// One turn far over MaxChunkBytes, in paragraphs so SplitOversized has
	// boundaries to cut at. Every part keeps the window's line anchor.
	para := strings.TrimSpace(strings.Repeat("wombat burrows collapse under load ", 20))
	content := strings.Repeat(para+"\n\n", 8) // ~5.7KB
	results := []ScanResult{{TurnIndex: 0, LineIndex: 42, Role: roleTool, ContentText: strings.TrimSpace(content)}}

	chunks := chunkScanResults(results, chunkerBase, "")
	require.Greater(t, len(chunks), 1, "an oversized window is split")
	for _, c := range chunks {
		assert.LessOrEqual(t, len(c.ContentText), store.MaxChunkBytes+len(para), "parts stay near the cap")
		assert.Equal(t, 42, c.FirstLineIndex, "split parts inherit the window anchor")
		assert.Contains(t, c.Title, "Turns 1-1")
	}
}

func TestChunkScanResults_SubagentTitleAndTimestampFallback(t *testing.T) {
	results := []ScanResult{
		{TurnIndex: 0, LineIndex: 0, Role: roleUser, ContentText: "investigate the port race",
			Timestamp: chunkerBase.Add(time.Hour)},
	}

	// Zero start (subagent scans discard their ScanOutput) → first result's
	// timestamp stamps the title.
	chunks := chunkScanResults(results, time.Time{}, "abc123")
	require.Len(t, chunks, 1)
	assert.Equal(t, "abc123", chunks[0].SubagentID)
	assert.Contains(t, chunks[0].Title, "Subagent: abc123")
	assert.Contains(t, chunks[0].Title, "Session 2026-05-01T11:00:00Z")
}

func TestChunkScanResults_PALToolsSplitOut(t *testing.T) {
	results := []ScanResult{
		{TurnIndex: 0, LineIndex: 0, Role: roleAssistant, ContentText: "planning",
			ToolNames: []string{"mcp__pal__chat", "Bash", "Bash", "mcp__pal__chat"}, Timestamp: chunkerBase},
	}
	chunks := chunkScanResults(results, chunkerBase, "")
	require.Len(t, chunks, 1)
	assert.Contains(t, chunks[0].Title, "PAL: chat")
	assert.Contains(t, chunks[0].Title, "Tools: Bash")
	assert.Equal(t, 1, strings.Count(chunks[0].Title, "Bash"), "tool names dedup")
}

func TestChunkScanResults_Empty(t *testing.T) {
	assert.Nil(t, chunkScanResults(nil, chunkerBase, ""))
	assert.Nil(t, chunkScanResults([]ScanResult{}, chunkerBase, ""))
}

// --- integration: chunk rows through import / reindex / delete ---

// chunkCounts returns the session's row count in each chunk layer table.
func chunkCounts(t *testing.T, s *VaultStore, uuid string) (porter, trigram int) {
	t.Helper()
	db, err := s.getDB(context.Background())
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM vault_chunks WHERE session_uuid = ?`, uuid).Scan(&porter))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM vault_chunks_trigram WHERE session_uuid = ?`, uuid).Scan(&trigram))
	return porter, trigram
}

func subagentFixtureJSONL(t *testing.T) []byte {
	t.Helper()
	return jsonlBytes(t,
		userLine("su1", "", "", "Investigate the flaky quokka test"),
		assistantLine("sa1", "smsg1", []map[string]any{
			{"type": "text", "text": "The quokka test races on the shared port."},
		}),
	)
}

func TestImport_PopulatesChunkTables(t *testing.T) {
	s := newTestVault(t)
	root := t.TempDir()
	uuid := "aaaabbbb-cccc-dddd-eeee-ffff00001111"
	main := sampleMainJSONL(t)
	writeSession(t, filepath.Join(root, "-home-user-proj"), uuid, main,
		map[string][]byte{"subagents/agent-abc.jsonl": subagentFixtureJSONL(t)})

	require.Equal(t, 1, importFixture(t, s, root, ImportOptions{}).Imported)

	porter, trigram := chunkCounts(t, s, uuid)
	require.Positive(t, porter, "import populates vault_chunks")
	assert.Equal(t, porter, trigram, "both layer tables carry the same rows")

	db, err := s.getDB(context.Background())
	require.NoError(t, err)

	// Sub-agent chunks append after the main transcript's, in chunk_index order.
	rows, err := db.Query(`SELECT subagent_id, chunk_index, first_line_index, content_text
		FROM vault_chunks WHERE session_uuid = ? ORDER BY chunk_index`, uuid)
	require.NoError(t, err)
	defer rows.Close()
	type row struct {
		subagent  string
		idx, line int
		content   string
	}
	var got []row
	for rows.Next() {
		var r row
		require.NoError(t, rows.Scan(&r.subagent, &r.idx, &r.line, &r.content))
		got = append(got, r)
	}
	require.NoError(t, rows.Err())
	require.Len(t, got, 2, "one small main chunk + one subagent chunk")

	assert.Equal(t, "", got[0].subagent)
	assert.Equal(t, 0, got[0].idx)
	assert.Equal(t, "abc", got[1].subagent, "subagent chunk follows the main transcript's")
	assert.Equal(t, 1, got[1].idx)

	// The anchor resolves to the expected raw-JSONL line: the main chunk starts
	// at the fixture's first line, whose text opens the chunk content.
	lines := bytes.Split(main, []byte("\n"))
	require.Greater(t, len(lines), got[0].line)
	assert.Contains(t, string(lines[got[0].line]), "Please fix the timeout bug")
	assert.True(t, strings.HasPrefix(got[0].content, "Please fix the timeout bug"),
		"chunk content begins at its first_line_index line")

	// Chunk content is searchable in both layers.
	var n int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM vault_chunks WHERE vault_chunks MATCH 'timeout'`).Scan(&n))
	assert.Positive(t, n)
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM vault_chunks_trigram WHERE vault_chunks_trigram MATCH '"imeou"'`).Scan(&n))
	assert.Positive(t, n, "trigram layer substring-matches chunk content")
}

func TestReindex_BackfillsChunksForBelowVersionSession(t *testing.T) {
	s := newTestVault(t)
	root := t.TempDir()
	uuid := "22222222-3333-4444-5555-666666666666"
	writeSession(t, filepath.Join(root, "-home-user-proj"), uuid, sampleMainJSONL(t), nil)
	require.Equal(t, 1, importFixture(t, s, root, ImportOptions{}).Imported)

	// Simulate a pre-v3 vault: below-version session with empty chunk tables
	// (exactly what migration 0004 leaves behind for already-archived rows).
	db, err := s.getDB(context.Background())
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE vault_sessions SET index_version = 2 WHERE uuid = ?`, uuid)
	require.NoError(t, err)
	for _, table := range []string{"vault_chunks", "vault_chunks_trigram"} {
		//nolint:gosec // table is a test-controlled constant, never user input
		_, err = db.Exec(`DELETE FROM ` + table)
		require.NoError(t, err)
	}

	// Backlog is visible before the reindex…
	st, err := s.Stats(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, st.OutdatedSessions, "below-version session counts as reindex backlog")

	res, err := Reindex(context.Background(), s)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Reindexed)

	// …chunks are rebuilt from the stored raw_jsonl, and the backlog clears.
	porter, trigram := chunkCounts(t, s, uuid)
	assert.Positive(t, porter, "reindex backfills vault_chunks from the DB blob")
	assert.Equal(t, porter, trigram)

	sess, err := s.GetSession(context.Background(), uuid[:8])
	require.NoError(t, err)
	assert.Equal(t, currentIndexVersion, sess.IndexVersion)

	st, err = s.Stats(context.Background())
	require.NoError(t, err)
	assert.Zero(t, st.OutdatedSessions, "backlog is zero after reindex")
}

func TestImport_VersionStaleRebuildBackfillsChunks(t *testing.T) {
	// The import ftsOnly path (unchanged content, stale index) must rebuild
	// chunks too — it shares RebuildFTSBatch with reindex.
	s := newTestVault(t)
	root := t.TempDir()
	uuid := "33333333-4444-5555-6666-777777777777"
	writeSession(t, filepath.Join(root, "-home-user-proj"), uuid, sampleMainJSONL(t), nil)
	require.Equal(t, 1, importFixture(t, s, root, ImportOptions{}).Imported)

	db, err := s.getDB(context.Background())
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE vault_sessions SET index_version = 1 WHERE uuid = ?`, uuid)
	require.NoError(t, err)
	for _, table := range []string{"vault_chunks", "vault_chunks_trigram"} {
		//nolint:gosec // table is a test-controlled constant, never user input
		_, err = db.Exec(`DELETE FROM ` + table)
		require.NoError(t, err)
	}

	res := importFixture(t, s, root, ImportOptions{})
	require.Equal(t, 1, res.Updated, "hash-identical stale session takes the FTS-only upgrade path")

	porter, trigram := chunkCounts(t, s, uuid)
	assert.Positive(t, porter)
	assert.Equal(t, porter, trigram)

	// No duplicates: chunk_index values are unique per session.
	var distinct, total int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(DISTINCT chunk_index), COUNT(*) FROM vault_chunks WHERE session_uuid = ?`, uuid).
		Scan(&distinct, &total))
	assert.Equal(t, distinct, total, "rebuild replaces chunk rows wholesale, no stale leftovers")
}

func TestDeleteSession_RemovesChunkRows(t *testing.T) {
	s := newTestVault(t)
	root := t.TempDir()
	uuid := "44444444-5555-6666-7777-888888888888"
	writeSession(t, filepath.Join(root, "-home-user-proj"), uuid, sampleMainJSONL(t), nil)
	require.Equal(t, 1, importFixture(t, s, root, ImportOptions{}).Imported)

	porter, _ := chunkCounts(t, s, uuid)
	require.Positive(t, porter)

	ok, err := s.DeleteSession(context.Background(), uuid)
	require.NoError(t, err)
	require.True(t, ok)

	// vault_chunks has no FK to vault_sessions — the explicit delete must cover it.
	porter, trigram := chunkCounts(t, s, uuid)
	assert.Zero(t, porter, "DeleteSession clears vault_chunks")
	assert.Zero(t, trigram, "DeleteSession clears vault_chunks_trigram")
}
