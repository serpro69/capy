package vault

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateSessionFTS_ReplacesIndexBumpsVersionKeepsBlob(t *testing.T) {
	s := newTestVault(t)
	rec := sampleRecord("44444444-4444-4444-4444-444444444444")
	rec.Session.IndexVersion = 1
	require.NoError(t, s.InsertSession(context.Background(), rec))

	// Replace the whole FTS row set for the session.
	newFTS := []FTSRow{
		{SessionUUID: rec.Session.UUID, Role: "tool", LineIndex: 0, ContentText: "Read /x.go\nankylosaurus output"},
	}
	require.NoError(t, s.UpdateSessionFTS(context.Background(), rec.Session.UUID, currentIndexVersion, newFTS, nil))

	// Old rows gone (incl. the subagent row), new row searchable.
	old, err := s.Search(context.Background(), SearchOptions{Query: "brontosaurus"})
	require.NoError(t, err)
	assert.Empty(t, old, "previous FTS rows are replaced wholesale")
	got, err := s.Search(context.Background(), SearchOptions{Query: "ankylosaurus", Role: "tool"})
	require.NoError(t, err)
	require.Len(t, got, 1)

	// Version bumped; raw_jsonl NOT rewritten (the whole point of UpdateSessionFTS).
	sess, err := s.GetSession(context.Background(), rec.Session.UUID)
	require.NoError(t, err)
	assert.Equal(t, currentIndexVersion, sess.IndexVersion)
	assert.True(t, bytes.Equal(rec.Session.RawJSONL, sess.RawJSONL), "blob untouched by a reindex")
}

func TestOutdatedSessionUUIDs(t *testing.T) {
	s := newTestVault(t)
	mk := func(uuid string, version int, end time.Time) {
		rec := sampleRecord(uuid)
		rec.Session.IndexVersion = version
		rec.Session.EndTime = end
		require.NoError(t, s.InsertSession(context.Background(), rec))
	}
	base := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	mk("aaaaaaaa-0000-0000-0000-000000000000", 1, base)                   // stale, older
	mk("bbbbbbbb-0000-0000-0000-000000000000", 1, base.Add(time.Hour))    // stale, newer
	mk("cccccccc-0000-0000-0000-000000000000", currentIndexVersion, base) // current

	uuids, err := s.OutdatedSessionUUIDs(context.Background(), currentIndexVersion)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"bbbbbbbb-0000-0000-0000-000000000000",
		"aaaaaaaa-0000-0000-0000-000000000000",
	}, uuids, "only stale sessions, newest first; current excluded")
}

func TestUpdateSessionFTS_MissingSessionIsNoOpNoOrphans(t *testing.T) {
	// vault_fts has no FK to vault_sessions, so UpdateSessionFTS must refuse to
	// write FTS rows for a uuid that no longer exists (e.g. deleted concurrently),
	// to avoid orphaned rows.
	s := newTestVault(t)
	err := s.UpdateSessionFTS(context.Background(), "nonexistent-0000-0000-0000-000000000000", currentIndexVersion, []FTSRow{
		{Role: "tool", LineIndex: 0, ContentText: "should-not-be-indexed"},
	}, nil)
	require.NoError(t, err, "missing session is a safe no-op, not an error")

	hits, err := s.Search(context.Background(), SearchOptions{Query: "should-not-be-indexed", Role: "tool"})
	require.NoError(t, err)
	assert.Empty(t, hits, "no orphaned FTS rows inserted for a nonexistent session")
}

func TestReindex_RebuildsStaleSessionFTSAndBumpsVersion(t *testing.T) {
	s := newTestVault(t)
	root := t.TempDir()
	uuid := "11111111-2222-3333-4444-555555555555"
	writeSession(t, filepath.Join(root, "-home-user-proj"), uuid, sampleMainJSONL(t), nil)
	require.Equal(t, 1, importFixture(t, s, root, ImportOptions{}).Imported)

	db, err := s.getDB(context.Background())
	require.NoError(t, err)

	// Simulate a legacy index: stale version + an un-enriched tool row (no call
	// prefix), as the v1 indexer produced.
	_, err = db.Exec(`UPDATE vault_sessions SET index_version=1 WHERE uuid=?`, uuid)
	require.NoError(t, err)
	_, err = db.Exec(`DELETE FROM vault_fts WHERE session_uuid=? AND role='tool'`, uuid)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO vault_fts
		(content_text, session_uuid, subagent_id, turn_index, message_index, line_index, role)
		VALUES (?, ?, '', 0, 0, 2, 'tool')`, "build log: pterodactyl error at line 5", uuid)
	require.NoError(t, err)

	// Pre-reindex: the Bash call path is not searchable from the un-enriched tool row.
	pre, err := s.Search(context.Background(), SearchOptions{Query: "vault", Role: "tool"})
	require.NoError(t, err)
	assert.Empty(t, pre, "legacy tool row lacks the call summary")

	res, err := Reindex(context.Background(), s)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Reindexed)
	assert.Equal(t, 0, res.Errors)

	got, err := s.GetSession(context.Background(), uuid[:8])
	require.NoError(t, err)
	assert.Equal(t, currentIndexVersion, got.IndexVersion, "reindex bumps the version to current")

	// Post-reindex: the tool result now carries its "Bash go test ./internal/vault"
	// call summary, so it is searchable by a token from the command.
	post, err := s.Search(context.Background(), SearchOptions{Query: "vault", Role: "tool"})
	require.NoError(t, err)
	require.Len(t, post, 1, "rebuilt tool row carries the enriched call summary")

	// A second run is a no-op: nothing is outdated anymore.
	res2, err := Reindex(context.Background(), s)
	require.NoError(t, err)
	assert.Equal(t, 0, res2.Reindexed)
}

func TestReindex_RebuildsGenericToolInputSummary(t *testing.T) {
	// A session archived before v4 lacks the generic/MCP tool-input summary in its
	// assistant tool_use row: v3's toolUseSummary rendered non-common tools (MCP,
	// ToolSearch, …) as the bare name, so nothing from the input was searchable.
	// Reindex must rebuild the assistant row from raw_jsonl with the current
	// scanner (which emits the bounded key=value summary) and stamp the session to
	// currentIndexVersion. Covers tasks.md Task 2.4 / design.md § Index version.
	s := newTestVault(t)
	root := t.TempDir()
	uuid := "22222222-3333-4444-5555-666666666666"

	main := jsonlBytes(t,
		userLine("u1", "/home/user/proj", "feature/x", "search the vault"),
		assistantLine("a1", "msg1", []map[string]any{
			{"type": "text", "text": "Searching now."},
			{"type": "tool_use", "id": "t1", "name": "mcp__capy__capy_search", "input": map[string]any{
				"queries": []string{"pterodactyl needle"},
				"source":  "kk:arch-decisions",
			}},
		}),
		userToolResultLine("u2", "no results"),
		aiTitleLine("Search the vault"),
	)
	writeSession(t, filepath.Join(root, "-home-user-proj"), uuid, main, nil)
	require.Equal(t, 1, importFixture(t, s, root, ImportOptions{}).Imported)

	db, err := s.getDB(context.Background())
	require.NoError(t, err)

	// Simulate a v3 index: stale version + the bare-name assistant row v3 produced
	// (the text block, but the tool_use as a bare name with no input summary).
	_, err = db.Exec(`UPDATE vault_sessions SET index_version=? WHERE uuid=?`, currentIndexVersion-1, uuid)
	require.NoError(t, err)
	_, err = db.Exec(`DELETE FROM vault_fts WHERE session_uuid=? AND role='assistant'`, uuid)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO vault_fts
		(content_text, session_uuid, subagent_id, turn_index, message_index, line_index, role)
		VALUES (?, ?, '', 0, 0, 1, 'assistant')`, "Searching now.\n→ mcp__capy__capy_search", uuid)
	require.NoError(t, err)

	// Pre-reindex: neither salient input field is searchable from the bare-name row.
	for _, tok := range []string{"pterodactyl", "arch-decisions"} {
		t.Run("pre-reindex "+tok, func(t *testing.T) {
			pre, err := s.Search(context.Background(), SearchOptions{Query: tok, Role: "assistant"})
			require.NoError(t, err)
			assert.Empty(t, pre, "legacy assistant row lacks the generic input summary for %q", tok)
		})
	}

	res, err := Reindex(context.Background(), s)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Reindexed)
	assert.Equal(t, 0, res.Errors)

	got, err := s.GetSession(context.Background(), uuid[:8])
	require.NoError(t, err)
	assert.Equal(t, currentIndexVersion, got.IndexVersion, "reindex bumps the version to current")

	// Post-reindex: the rebuilt assistant row carries the bounded key=value summary,
	// so both the queries value and the source value are now searchable.
	for _, tok := range []string{"pterodactyl", "arch-decisions"} {
		t.Run("post-reindex "+tok, func(t *testing.T) {
			post, err := s.Search(context.Background(), SearchOptions{Query: tok, Role: "assistant"})
			require.NoError(t, err)
			require.Len(t, post, 1, "rebuilt assistant row carries the generic tool-input summary for %q", tok)
		})
	}
}

func TestReindex_CrossesBatchBoundary(t *testing.T) {
	s := newTestVault(t)
	// One more than a single batch so reindex flushes at least twice — proves the
	// batch traversal upgrades every stale session, not just the first batch.
	// (sampleRecord's blob scans to no FTS rows; this test verifies batch traversal
	// and version bumping, not FTS content — that is covered above.)
	const n = reindexBatchSessions + 1
	for i := range n {
		rec := sampleRecord(fmt.Sprintf("%08d-1111-2222-3333-444444444444", i))
		rec.Session.IndexVersion = 1
		require.NoError(t, s.InsertSession(context.Background(), rec))
	}

	res, err := Reindex(context.Background(), s)
	require.NoError(t, err)
	assert.Equal(t, n, res.Reindexed, "every stale session across batches is reindexed")
	assert.Equal(t, 0, res.Errors)

	// All sessions are now current: nothing remains outdated and a re-run is a no-op.
	uuids, err := s.OutdatedSessionUUIDs(context.Background(), currentIndexVersion)
	require.NoError(t, err)
	assert.Empty(t, uuids, "no sessions remain below currentIndexVersion after a full reindex")

	res2, err := Reindex(context.Background(), s)
	require.NoError(t, err)
	assert.Equal(t, 0, res2.Reindexed)
}

const reindexSniffWarning = "vault reindex: unrecognized stored platform, using the format detected from the transcript"

// stripIndex simulates a stale row: index_version 1 and no FTS rows at all, so
// a rebuild has to reproduce every searchable row from raw_jsonl.
func stripIndex(t *testing.T, s *VaultStore, uuid string) {
	t.Helper()
	db, err := s.getDB(context.Background())
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE vault_sessions SET index_version = 1 WHERE uuid = ?`, uuid)
	require.NoError(t, err)
	_, err = db.Exec(`DELETE FROM vault_fts WHERE session_uuid = ?`, uuid)
	require.NoError(t, err)
}

// importCodexForReindex archives one Codex rollout whose human turn carries
// `prompt` and returns its uuid.
func importCodexForReindex(t *testing.T, s *VaultStore, prompt string) string {
	t.Helper()
	home := t.TempDir()
	writeCodexRollout(t, home, codexActiveRel(codexFixtureID),
		codexImportRollout(t, codexPaginated, codexFixtureID, "/home/user/proj", prompt), false)
	require.Equal(t, 1, importCodexHome(t, s, home, ImportOptions{}).Imported)
	return codexFixtureID
}

// TestReindex_DispatchesOnStoredPlatform: a mixed vault rebuilds every stale row
// with its OWN platform's decoder — the Codex row's human turn is a role=user
// row again after the rebuild, which the Claude decoder (yielding no entries for
// Codex bytes) could not have produced.
func TestReindex_DispatchesOnStoredPlatform(t *testing.T) {
	s := newTestVault(t)
	ctx := context.Background()

	root := t.TempDir()
	const claude = "11111111-2222-3333-4444-555555555555"
	writeSession(t, filepath.Join(root, "-home-user-proj"), claude, sampleMainJSONL(t), nil)
	require.Equal(t, 1, importFixture(t, s, root, ImportOptions{}).Imported)
	codex := importCodexForReindex(t, s, "reindex the velociraptor rollout")

	stripIndex(t, s, claude)
	stripIndex(t, s, codex)
	pre, err := s.Search(ctx, SearchOptions{Query: "velociraptor"})
	require.NoError(t, err)
	require.Empty(t, pre, "fixture: the Codex row has no index left")

	h := captureSlog(t)
	res, err := Reindex(ctx, s)
	require.NoError(t, err)
	assert.Equal(t, 2, res.Reindexed)
	assert.Equal(t, 0, res.Errors)
	assert.Empty(t, h.recordsWithMessage(reindexSniffWarning), "recognized platforms are never sniffed")

	hits, err := s.Search(ctx, SearchOptions{Query: "velociraptor", Role: "user"})
	require.NoError(t, err)
	require.Len(t, hits, 1, "the Codex human turn is a role=user row again")
	assert.Equal(t, PlatformCodex, hits[0].Platform)
	claudeHits, err := s.Search(ctx, SearchOptions{Query: "vault", Role: "tool"})
	require.NoError(t, err)
	assert.Len(t, claudeHits, 1, "the Claude row is rebuilt with the Claude decoder")

	for _, uuid := range []string{claude, codex} {
		got, err := s.GetSession(ctx, uuid)
		require.NoError(t, err)
		assert.Equal(t, currentIndexVersion, got.IndexVersion, "%s is stamped current", uuid)
	}
}

// TestReindex_UnrecognizedPlatformIsSniffedNotRewritten: a row whose stored
// platform is corrupted is rebuilt with the decoder DetectFormat picks from its
// blob, with one warning naming the uuid and the bad value, and the stored
// column is left as it was — the rebuild path is FTS-only (ADR-025 D4) and the
// repair is the user's call.
func TestReindex_UnrecognizedPlatformIsSniffedNotRewritten(t *testing.T) {
	s := newTestVault(t)
	ctx := context.Background()
	codex := importCodexForReindex(t, s, "sniff the pterodactyl rollout")

	stripIndex(t, s, codex)
	db, err := s.getDB(ctx)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE vault_sessions SET platform = 'bogus' WHERE uuid = ?`, codex)
	require.NoError(t, err)

	h := captureSlog(t)
	res, err := Reindex(ctx, s)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Reindexed)
	assert.Equal(t, 0, res.Errors)

	warnings := h.recordsWithMessage(reindexSniffWarning)
	require.Len(t, warnings, 1)
	attrs := recordAttrs(warnings[0])
	assert.Equal(t, codex, attrs["uuid"])
	assert.Equal(t, "bogus", attrs["platform"])
	assert.Equal(t, PlatformCodex, attrs["detected"])

	hits, err := s.Search(ctx, SearchOptions{Query: "pterodactyl", Role: "user"})
	require.NoError(t, err)
	assert.Len(t, hits, 1, "rebuilt with the Codex decoder the sniff selected")

	got, err := s.GetSession(ctx, codex)
	require.NoError(t, err)
	assert.Equal(t, Platform("bogus"), got.Platform, "the stored value is NOT rewritten by a reindex")
	assert.Equal(t, currentIndexVersion, got.IndexVersion)
}

// TestReindex_UndetectableRowIsAnErrorNotClaude: when the stored platform is
// corrupted AND the blob's first line is not a JSON object, the row is counted
// as an error and left at its old version — never scanned as Claude by default.
func TestReindex_UndetectableRowIsAnErrorNotClaude(t *testing.T) {
	s := newTestVault(t)
	ctx := context.Background()
	rec := sampleRecord("66666666-6666-6666-6666-666666666666")
	rec.Session.IndexVersion = 1
	require.NoError(t, s.InsertSession(ctx, rec))
	db, err := s.getDB(ctx)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE vault_sessions SET platform = 'bogus', raw_jsonl = ?, encoding = 'raw' WHERE uuid = ?`,
		[]byte("not a json object\n"), rec.Session.UUID)
	require.NoError(t, err)

	h := captureSlog(t)
	res, err := Reindex(ctx, s)
	require.NoError(t, err)
	assert.Equal(t, 0, res.Reindexed)
	assert.Equal(t, 1, res.Errors)
	assert.Empty(t, h.recordsWithMessage(reindexSniffWarning), "nothing was resolved, so no resolution warning")

	got, err := s.GetSession(ctx, rec.Session.UUID)
	require.NoError(t, err)
	assert.Equal(t, 1, got.IndexVersion, "left stale for a later repair")
	assert.Equal(t, Platform("bogus"), got.Platform)
}

func TestReindex_CancelledContextDoesNothing(t *testing.T) {
	s := newTestVault(t)
	rec := sampleRecord("55555555-5555-5555-5555-555555555555")
	rec.Session.IndexVersion = 1
	require.NoError(t, s.InsertSession(context.Background(), rec))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before Reindex enters its loop

	res, err := Reindex(ctx, s)
	require.NoError(t, err)
	assert.Equal(t, 0, res.Reindexed, "a pre-cancelled reindex touches nothing")

	// The stale session is left untouched for a later run.
	got, err := s.GetSession(context.Background(), rec.Session.UUID)
	require.NoError(t, err)
	assert.Equal(t, 1, got.IndexVersion)
}
