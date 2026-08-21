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
