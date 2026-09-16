package vault

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// codex_import_test.go covers Import over Codex rollouts (Slice 7.3) and the
// import → search → show → restore round-trip (Slice 7.7): decompress-before-
// hash, the in-run reconciliation map (a thread present under both sessions/
// and archived_sessions/), the Codex-only location policy (a same-hash rollout
// at a new relative path moves the hint, metadata-only), the filename-uuid row
// identity, platform/project filters, the zero-message exclusion of aborted
// shells, both child variants, and the reader-version bump on the first Codex
// write. Fixture lines come from codex_fixtures_test.go.

func codexActiveRel(id string) string {
	return "sessions/2026/05/01/rollout-2026-05-01T10-00-00-" + id + ".jsonl"
}

func codexArchivedRel(id string) string {
	return "archived_sessions/rollout-2026-05-01T10-00-00-" + id + ".jsonl"
}

// codexImportRollout is a minimal archivable rollout: session_meta, one human
// turn and one assistant reply (MessageCount 2), plus any extra lines.
func codexImportRollout(t testing.TB, mode codexMode, id, cwd, prompt string, extra ...map[string]any) []byte {
	t.Helper()
	lines := []map[string]any{
		codexSessionMetaLine(at(0), mode, codexMetaOpts{id: id, cwd: cwd, payloadTS: codexPayloadTS}),
		codexHuman(mode, at(1), prompt),
		codexAssistant(at(2), "Sure — "+prompt),
	}
	lines = append(lines, extra...)
	return codexRollout(t, mode, lines...)
}

// importCodexHome discovers home with the Codex walker and imports the result.
func importCodexHome(t *testing.T, s *VaultStore, home string, opts ImportOptions) ImportResult {
	t.Helper()
	sessions, _, err := DiscoverCodexSessions(home, CodexDiscoverOptions{})
	require.NoError(t, err)
	return Import(context.Background(), s, sessions, opts)
}

// minReaderMarker reads vault_meta.min_reader_version ("" when absent).
func minReaderMarker(t *testing.T, s *VaultStore) string {
	t.Helper()
	db, err := s.getDB(context.Background())
	require.NoError(t, err)
	var v string
	err = db.QueryRow(`SELECT value FROM vault_meta WHERE key=?`, minReaderVersionKey).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	require.NoError(t, err)
	return v
}

// statusByUUID groups the reported statuses per uuid. Order within a group is
// not discovery order — skips are recorded immediately while batched writes are
// recorded at flush — so compare with ElementsMatch.
func statusByUUID(res ImportResult) map[string][]string {
	out := map[string][]string{}
	for _, s := range res.Sessions {
		out[s.UUID] = append(out[s.UUID], s.Status)
	}
	return out
}

func TestImportCodex_NewDistinctThreads(t *testing.T) {
	s := newTestVault(t)
	home := t.TempDir()
	plainA := codexImportRollout(t, codexLegacy, codexDiscUUIDA, "/p/a", "first thread")
	plainB := codexImportRollout(t, codexPaginated, codexDiscUUIDB, "/p/b", "second thread, compressed")
	writeCodexRollout(t, home, codexActiveRel(codexDiscUUIDA), plainA, false)
	writeCodexRollout(t, home, codexActiveRel(codexDiscUUIDB)+".zst", plainB, true)
	require.Equal(t, "", minReaderMarker(t, s), "fresh vault has no marker")

	res := importCodexHome(t, s, home, ImportOptions{})
	assert.Equal(t, 2, res.Imported)
	assert.Equal(t, 0, res.Errors)
	require.Len(t, res.Sessions, 2)
	for _, r := range res.Sessions {
		assert.Equal(t, StatusNew, r.Status)
		assert.Equal(t, PlatformCodex, r.Platform, "every outcome carries the platform")
	}

	a, err := s.GetSession(context.Background(), codexDiscUUIDA)
	require.NoError(t, err)
	assert.Equal(t, PlatformCodex, a.Platform)
	assert.Equal(t, codexActiveRel(codexDiscUUIDA), a.ClaudeProjectDir, "the location hint is the relative rollout path")
	assert.Equal(t, "/p/a", a.ProjectPath, "project_path is session_meta.cwd")
	assert.Equal(t, "master", a.GitBranch)
	assert.Equal(t, "first thread", a.Title)
	assert.Equal(t, 2, a.MessageCount)
	assert.Empty(t, a.ParentUUID)
	assert.True(t, time.Date(2026, 5, 1, 9, 58, 30, 0, time.UTC).Equal(a.StartTime),
		"StartTime is the payload (creation) timestamp, not line 0's envelope time: %s", a.StartTime)
	assert.True(t, bytes.Equal(plainA, a.RawJSONL))

	// The compressed rollout is stored as its PLAIN bytes: raw_jsonl, hash, size
	// and the hint (no .zst) are all about the decompressed JSONL.
	b, err := s.GetSession(context.Background(), codexDiscUUIDB)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(plainB, b.RawJSONL), "raw_jsonl is the decompressed JSONL")
	assert.Equal(t, int64(len(plainB)), b.SizeBytes)
	wantHash, _ := computeContentHash(map[string][]byte{codexDiscUUIDB + ".jsonl": plainB})
	assert.Equal(t, wantHash, b.ContentHash, "content_hash is computed on the plain bytes")
	assert.Equal(t, codexActiveRel(codexDiscUUIDB), b.ClaudeProjectDir, ".zst stripped from the hint")
	assert.Equal(t, "second thread, compressed", b.Title)

	assert.Equal(t, "3", minReaderMarker(t, s), "the first Codex write raises min_reader_version to 3")

	// Idempotent: a second run over the same files skips everything.
	again := importCodexHome(t, s, home, ImportOptions{})
	assert.Equal(t, 0, again.Imported)
	assert.Equal(t, 2, again.Skipped)
}

// A thread present under both roots in one run (Codex archive is a move; a stale
// copy can linger): the sessions/ copy is seen first and wins, the archived copy
// is skipped by the in-run map — never a primary-key StatusError — in a real run
// and a dry run alike. A second run with both copies still present skips both
// and leaves the hint alone.
func TestImportCodex_ActiveAndArchivedPairInOneRun(t *testing.T) {
	home := t.TempDir()
	raw := codexImportRollout(t, codexLegacy, codexDiscUUIDA, "/p/a", "one thread, two files")
	writeCodexRollout(t, home, codexActiveRel(codexDiscUUIDA), raw, false)
	writeCodexRollout(t, home, codexArchivedRel(codexDiscUUIDA), raw, false)

	t.Run("dry run", func(t *testing.T) {
		s := newTestVault(t)
		res := importCodexHome(t, s, home, ImportOptions{DryRun: true})
		assert.Equal(t, 1, res.Imported)
		assert.Equal(t, 1, res.Skipped)
		assert.Equal(t, 0, res.Errors)
		assert.ElementsMatch(t, []string{StatusNew, StatusSkipped}, statusByUUID(res)[codexDiscUUIDA])
		listed, err := s.ListSessions(context.Background(), ListOptions{})
		require.NoError(t, err)
		assert.Empty(t, listed)
	})

	t.Run("real run then re-run", func(t *testing.T) {
		s := newTestVault(t)
		res := importCodexHome(t, s, home, ImportOptions{})
		assert.Equal(t, 1, res.Imported)
		assert.Equal(t, 1, res.Skipped)
		assert.Equal(t, 0, res.Errors, "the archived copy must not collide on the primary key")
		assert.ElementsMatch(t, []string{StatusNew, StatusSkipped}, statusByUUID(res)[codexDiscUUIDA])

		got, err := s.GetSession(context.Background(), codexDiscUUIDA)
		require.NoError(t, err)
		assert.Equal(t, codexActiveRel(codexDiscUUIDA), got.ClaudeProjectDir, "the active path is stored")

		again := importCodexHome(t, s, home, ImportOptions{})
		assert.Equal(t, 0, again.Imported)
		assert.Equal(t, 0, again.Updated)
		assert.Equal(t, 2, again.Skipped)
		got, err = s.GetSession(context.Background(), codexDiscUUIDA)
		require.NoError(t, err)
		assert.Equal(t, codexActiveRel(codexDiscUUIDA), got.ClaudeProjectDir, "a same-hash later copy never touches the hint")
	})
}

// Only the archived copy is left on disk (a genuine move): the same bytes at a
// new relative path move the location hint — a metadata-only update that leaves
// hash, blob, title and archived_at untouched — and report `updated`. A dry run
// reports the same `updated` without writing.
func TestImportCodex_MovedRolloutUpdatesHintOnly(t *testing.T) {
	s := newTestVault(t)
	home := t.TempDir()
	raw := codexImportRollout(t, codexLegacy, codexDiscUUIDA, "/p/a", "a thread that gets archived")
	active := writeCodexRollout(t, home, codexActiveRel(codexDiscUUIDA), raw, false)
	require.Equal(t, 1, importCodexHome(t, s, home, ImportOptions{}).Imported)

	// Pin metadata a rescan or replace would rewrite, to prove neither happens.
	db, err := s.getDB(context.Background())
	require.NoError(t, err)
	const sentinelTitle, sentinelArchived = "SENTINEL title", "2000-01-01T00:00:00Z"
	_, err = db.Exec(`UPDATE vault_sessions SET title=?, archived_at=? WHERE uuid=?`, sentinelTitle, sentinelArchived, codexDiscUUIDA)
	require.NoError(t, err)
	before, err := s.GetSession(context.Background(), codexDiscUUIDA)
	require.NoError(t, err)

	// Codex archives: the file moves.
	require.NoError(t, os.Remove(active))
	writeCodexRollout(t, home, codexArchivedRel(codexDiscUUIDA), raw, false)

	dry := importCodexHome(t, s, home, ImportOptions{DryRun: true})
	assert.Equal(t, 1, dry.Updated, "dry run reports the move")
	assert.Equal(t, 0, dry.Skipped)
	mid, err := s.GetSession(context.Background(), codexDiscUUIDA)
	require.NoError(t, err)
	assert.Equal(t, codexActiveRel(codexDiscUUIDA), mid.ClaudeProjectDir, "dry run must not move the hint")

	res := importCodexHome(t, s, home, ImportOptions{})
	assert.Equal(t, 1, res.Updated)
	assert.Equal(t, 0, res.Imported)
	assert.Equal(t, 0, res.Skipped)
	assert.Equal(t, 0, res.Errors)
	require.Len(t, res.Sessions, 1)
	assert.Equal(t, StatusUpdated, res.Sessions[0].Status)
	assert.Equal(t, "/p/a", res.Sessions[0].ProjectPath, "the report shows the discovery hint (the row is not rescanned)")
	assert.Empty(t, res.Sessions[0].Title, "not rescanned → no title in the report")

	after, err := s.GetSession(context.Background(), codexDiscUUIDA)
	require.NoError(t, err)
	assert.Equal(t, codexArchivedRel(codexDiscUUIDA), after.ClaudeProjectDir, "the hint follows the file")
	assert.Equal(t, before.ContentHash, after.ContentHash)
	assert.Equal(t, sentinelTitle, after.Title, "metadata-only: the title is not rewritten")
	assert.Equal(t, sentinelArchived, after.ArchivedAt, "metadata-only: archived_at is preserved")
	assert.Equal(t, before.IndexVersion, after.IndexVersion)
	assert.True(t, bytes.Equal(raw, after.RawJSONL))
}

// A moved AND version-stale rollout does both — the FTS rebuild is batched as
// usual and the hint updates immediately — and reports exactly one `updated`.
func TestImportCodex_MovedAndVersionStaleRebuildsFTSAndMovesHint(t *testing.T) {
	s := newTestVault(t)
	home := t.TempDir()
	raw := codexImportRollout(t, codexLegacy, codexDiscUUIDA, "/p/a", "stale and moved",
		codexExecPair(t, at(3), at(4), "call_1", "ls", 0, "ankylosaurus.go")...)
	active := writeCodexRollout(t, home, codexActiveRel(codexDiscUUIDA), raw, false)
	require.Equal(t, 1, importCodexHome(t, s, home, ImportOptions{}).Imported)

	db, err := s.getDB(context.Background())
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE vault_sessions SET index_version=? WHERE uuid=?`, currentIndexVersion-1, codexDiscUUIDA)
	require.NoError(t, err)
	_, err = db.Exec(`DELETE FROM vault_fts WHERE session_uuid=? AND role='tool'`, codexDiscUUIDA)
	require.NoError(t, err)
	pre, err := s.Search(context.Background(), SearchOptions{Query: "ankylosaurus"})
	require.NoError(t, err)
	require.Empty(t, pre, "tool row removed to simulate a stale index")

	require.NoError(t, os.Remove(active))
	writeCodexRollout(t, home, codexArchivedRel(codexDiscUUIDA), raw, false)

	res := importCodexHome(t, s, home, ImportOptions{})
	assert.Equal(t, 1, res.Updated)
	assert.Equal(t, 0, res.Errors)
	require.Len(t, res.Sessions, 1, "one `updated`, not one per effect")

	got, err := s.GetSession(context.Background(), codexDiscUUIDA)
	require.NoError(t, err)
	assert.Equal(t, currentIndexVersion, got.IndexVersion, "FTS rebuilt to the current version")
	assert.Equal(t, codexArchivedRel(codexDiscUUIDA), got.ClaudeProjectDir, "hint moved")
	post, err := s.Search(context.Background(), SearchOptions{Query: "ankylosaurus", Role: "tool"})
	require.NoError(t, err)
	assert.Len(t, post, 1, "the tool row is back")
}

// A larger, divergent later copy of a uuid seen earlier in the SAME run (the
// archived copy grew past the active one) replaces it: the pending write is
// flushed first so the ordinary replace path sees a committed row. Real run and
// dry run report one `new` and one `updated`.
func TestImportCodex_LargerDivergentLaterCopyReplaces(t *testing.T) {
	home := t.TempDir()
	base := codexImportRollout(t, codexLegacy, codexDiscUUIDA, "/p/a", "diverging copies")
	larger := append(append([]byte(nil), base...), jsonlBytes(t, codexAssistant(at(5), "Follow-up about the brachiosaurus."))...)
	require.Greater(t, len(larger), len(base))
	writeCodexRollout(t, home, codexActiveRel(codexDiscUUIDA), base, false)
	writeCodexRollout(t, home, codexArchivedRel(codexDiscUUIDA), larger, false)

	t.Run("dry run", func(t *testing.T) {
		s := newTestVault(t)
		res := importCodexHome(t, s, home, ImportOptions{DryRun: true})
		assert.ElementsMatch(t, []string{StatusNew, StatusUpdated}, statusByUUID(res)[codexDiscUUIDA])
		assert.Equal(t, 0, res.Errors)
	})

	t.Run("real run", func(t *testing.T) {
		s := newTestVault(t)
		res := importCodexHome(t, s, home, ImportOptions{})
		assert.ElementsMatch(t, []string{StatusNew, StatusUpdated}, statusByUUID(res)[codexDiscUUIDA])
		assert.Equal(t, 1, res.Imported)
		assert.Equal(t, 1, res.Updated)
		assert.Equal(t, 0, res.Errors)

		got, err := s.GetSession(context.Background(), codexDiscUUIDA)
		require.NoError(t, err)
		assert.True(t, bytes.Equal(larger, got.RawJSONL), "the larger copy wins")
		assert.Equal(t, codexArchivedRel(codexDiscUUIDA), got.ClaudeProjectDir, "the hint names the winning file")
		hits, err := s.Search(context.Background(), SearchOptions{Query: "brachiosaurus"})
		require.NoError(t, err)
		assert.Len(t, hits, 1, "the replacement's FTS is what remains")
	})
}

// The location policy is Codex-only: a Claude session re-imported through a
// loose --source <dir> (a different containing-dir basename) is skipped with its
// mangled-dir hint untouched.
func TestImport_ClaudeLooseSourceReimportLeavesHintAlone(t *testing.T) {
	s := newTestVault(t)
	root := t.TempDir()
	uuid := "11111111-2222-3333-4444-555555555555"
	writeSession(t, filepath.Join(root, "-home-user-proj"), uuid, sampleMainJSONL(t), nil)
	require.Equal(t, 1, importFixture(t, s, root, ImportOptions{}).Imported)

	loose := filepath.Join(t.TempDir(), "loose")
	writeSession(t, loose, uuid, sampleMainJSONL(t), nil)
	res := importFixture(t, s, loose, ImportOptions{})
	assert.Equal(t, 1, res.Skipped)
	assert.Equal(t, 0, res.Updated)
	assert.Equal(t, PlatformClaudeCode, res.Sessions[0].Platform)

	got, err := s.GetSession(context.Background(), uuid)
	require.NoError(t, err)
	assert.Equal(t, "-home-user-proj", got.ClaudeProjectDir, "a Claude row never enters the location policy")
}

// Row identity is the filename uuid: a rollout whose session_meta.id disagrees
// with its filename is imported under the filename uuid with a warning naming both.
func TestImportCodex_PlatformIDMismatchWarnsAndKeepsFilenameUUID(t *testing.T) {
	s := newTestVault(t)
	home := t.TempDir()
	// meta id = codexFixtureID, filename uuid = codexDiscUUIDA.
	raw := codexImportRollout(t, codexLegacy, codexFixtureID, "/p/a", "mismatched ids")
	writeCodexRollout(t, home, codexActiveRel(codexDiscUUIDA), raw, false)
	h := captureSlog(t)

	res := importCodexHome(t, s, home, ImportOptions{})
	assert.Equal(t, 1, res.Imported)
	assert.Equal(t, codexDiscUUIDA, res.Sessions[0].UUID)

	_, err := s.GetSession(context.Background(), codexDiscUUIDA)
	require.NoError(t, err, "archived under the filename uuid")
	_, err = s.GetSession(context.Background(), codexFixtureID)
	assert.ErrorIs(t, err, ErrSessionNotFound, "never under the recorded id")

	recs := h.recordsWithMessage("vault import: session id recorded inside the file differs from its filename; the filename uuid is the row key")
	require.Len(t, recs, 1)
	attrs := recordAttrs(recs[0])
	assert.Equal(t, codexDiscUUIDA, attrs["uuid"])
	assert.Equal(t, codexFixtureID, attrs["platform_id"])
	assert.Equal(t, PlatformCodex, attrs["platform"])
}

// An aborted-at-startup shell (no human, no assistant) is a zero-message session
// and is excluded exactly like an empty Claude session; both child variants
// (with and without a spawn prompt) archive with their parent link.
func TestImportCodex_ShellExcludedChildrenArchived(t *testing.T) {
	s := newTestVault(t)
	home := t.TempDir()
	writeCodexRollout(t, home, codexActiveRel(codexFixtureID), codexCaseByName(t, "aborted_shell").raw, false)
	writeCodexRollout(t, home, codexActiveRel(codexFixtureChildID), codexCaseByName(t, "child_legacy_137").raw, false)
	writeCodexRollout(t, home, codexActiveRel(codexFixtureChild2ID), codexCaseByName(t, "child_paginated_147").raw, false)
	h := captureSlog(t)

	res := importCodexHome(t, s, home, ImportOptions{})
	assert.Equal(t, 2, res.Imported)
	assert.Equal(t, 1, res.Excluded)
	assert.Equal(t, 0, res.Errors)
	by := statusByUUID(res)
	assert.Equal(t, []string{StatusExcluded}, by[codexFixtureID])
	assert.Equal(t, []string{StatusNew}, by[codexFixtureChildID])
	assert.Equal(t, []string{StatusNew}, by[codexFixtureChild2ID])
	assert.Empty(t, h.recordsWithMessage(codexWarnNoHuman), "a shell and a ≥ 0.147 child are not zero-human warnings")

	_, err := s.GetSession(context.Background(), codexFixtureID)
	assert.ErrorIs(t, err, ErrSessionNotFound, "the shell is not archived")

	legacy, err := s.GetSession(context.Background(), codexFixtureChildID)
	require.NoError(t, err)
	assert.Equal(t, codexFixtureParentID, legacy.ParentUUID)
	assert.Equal(t, "Review the latest commit on the current branch…", legacy.Title)

	paginated, err := s.GetSession(context.Background(), codexFixtureChild2ID)
	require.NoError(t, err)
	assert.Equal(t, codexFixtureParentID, paginated.ParentUUID)
	assert.Equal(t, "Chandrasekhar · code-reviewer", paginated.Title, "agent identity title for a child with no human turn")
	assert.Equal(t, 1, paginated.MessageCount)

	// Children are hidden from the default listing (design § Sub-agent Model).
	top, err := s.ListSessions(context.Background(), ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, top)
	all, err := s.ListSessions(context.Background(), ListOptions{IncludeChildren: true})
	require.NoError(t, err)
	assert.Len(t, all, 2)
}

func TestImportOptions_PlatformAndProjectFilters(t *testing.T) {
	root := t.TempDir()
	claudeUUID := "aaaaaaaa-1111-2222-3333-444444444444"
	writeSession(t, filepath.Join(root, "-home-user-proj"), claudeUUID, sampleMainJSONL(t), nil)
	claude, err := DiscoverSessions(root)
	require.NoError(t, err)

	home := t.TempDir()
	writeCodexRollout(t, home, codexActiveRel(codexDiscUUIDA), codexImportRollout(t, codexLegacy, codexDiscUUIDA, "/p/alpha", "alpha"), false)
	writeCodexRollout(t, home, codexActiveRel(codexDiscUUIDB), codexImportRollout(t, codexLegacy, codexDiscUUIDB, "/p/beta", "beta"), false)
	codex, _, err := DiscoverCodexSessions(home, CodexDiscoverOptions{})
	require.NoError(t, err)
	all := append(claude, codex...)

	uuids := func(res ImportResult) []string {
		var out []string
		for _, s := range res.Sessions {
			out = append(out, s.UUID)
		}
		return out
	}

	t.Run("platform codex", func(t *testing.T) {
		s := newTestVault(t)
		res := Import(context.Background(), s, all, ImportOptions{Platform: PlatformCodex, DryRun: true})
		assert.ElementsMatch(t, []string{codexDiscUUIDA, codexDiscUUIDB}, uuids(res))
	})
	t.Run("platform claude-code", func(t *testing.T) {
		s := newTestVault(t)
		res := Import(context.Background(), s, all, ImportOptions{Platform: PlatformClaudeCode, DryRun: true})
		assert.Equal(t, []string{claudeUUID}, uuids(res))
	})
	t.Run("project matches the codex cwd hint", func(t *testing.T) {
		s := newTestVault(t)
		res := Import(context.Background(), s, all, ImportOptions{Project: "/p/beta", DryRun: true})
		assert.Equal(t, []string{codexDiscUUIDB}, uuids(res))
	})
	t.Run("project matches the claude mangled dir", func(t *testing.T) {
		s := newTestVault(t)
		res := Import(context.Background(), s, all, ImportOptions{Project: "-home-user", DryRun: true})
		assert.Equal(t, []string{claudeUUID}, uuids(res))
	})
	t.Run("no filter imports everything", func(t *testing.T) {
		s := newTestVault(t)
		res := Import(context.Background(), s, all, ImportOptions{})
		assert.Equal(t, 3, res.Imported)
		assert.Equal(t, 0, res.Errors)
	})
}

// A .zst rollout that does not decompress is a per-session StatusError (the run
// continues); nothing of it is hashed or written.
func TestImportCodex_CorruptZstRecordedAsError(t *testing.T) {
	s := newTestVault(t)
	home := t.TempDir()
	writeCodexRollout(t, home, codexActiveRel(codexDiscUUIDA), codexImportRollout(t, codexLegacy, codexDiscUUIDA, "/p/a", "fine"), false)
	// Discovery reads the first line through a streaming decoder and would skip a
	// file whose frame is garbage, so hand it a SessionFile directly — the shape
	// import sees when a rollout is truncated between discovery and read.
	bad := filepath.Join(home, "sessions", "2026", "05", "01", "rollout-2026-05-01T10-30-00-"+codexDiscUUIDB+".jsonl.zst")
	require.NoError(t, os.WriteFile(bad, []byte("not a zstd frame"), 0o644))
	sessions, _, err := DiscoverCodexSessions(home, CodexDiscoverOptions{})
	require.NoError(t, err)
	require.Len(t, sessions, 1, "the garbage .zst is already skipped by discovery")
	sessions = append(sessions, SessionFile{
		Platform: PlatformCodex, Path: bad, UUID: codexDiscUUIDB,
		RelativePath: codexActiveRel(codexDiscUUIDB), Compressed: true,
	})

	res := Import(context.Background(), s, sessions, ImportOptions{})
	assert.Equal(t, 1, res.Imported)
	assert.Equal(t, 1, res.Errors)
	by := statusByUUID(res)
	assert.Equal(t, []string{StatusError}, by[codexDiscUUIDB])
	for _, r := range res.Sessions {
		if r.UUID == codexDiscUUIDB {
			require.Error(t, r.Err)
			assert.Equal(t, PlatformCodex, r.Platform)
		}
	}
	_, err = s.GetSession(context.Background(), codexDiscUUIDB)
	assert.ErrorIs(t, err, ErrSessionNotFound)
}

// A hand-built SessionFile with no Platform (the pre-Slice-7 shape) is a Claude
// session — the import contract mirrors the store's write contract.
func TestImport_EmptyPlatformIsClaude(t *testing.T) {
	s := newTestVault(t)
	dir := filepath.Join(t.TempDir(), "-home-user-proj")
	uuid := "11111111-2222-3333-4444-555555555555"
	writeSession(t, dir, uuid, sampleMainJSONL(t), nil)
	res := Import(context.Background(), s, []SessionFile{{Path: filepath.Join(dir, uuid+".jsonl"), UUID: uuid, ProjectDir: "-home-user-proj"}}, ImportOptions{})
	require.Equal(t, 1, res.Imported)
	assert.Equal(t, PlatformClaudeCode, res.Sessions[0].Platform)
	got, err := s.GetSession(context.Background(), uuid)
	require.NoError(t, err)
	assert.Equal(t, PlatformClaudeCode, got.Platform)
	assert.Equal(t, "-home-user-proj", got.ClaudeProjectDir)
}

// Slice 7.7: import → search finds the human phrase as role=user → show names
// Codex → restore reproduces the decompressed input byte-for-byte at the
// discovered relative path (minus .zst).
func TestImportCodex_RoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name       string
		compressed bool
	}{{"plain", false}, {"zst", true}} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestVault(t)
			home := t.TempDir()
			plain := codexImportRollout(t, codexPaginated, codexDiscUUIDA, "/p/a", "Where does the velociraptor timeout come from?",
				codexExecPair(t, at(3), at(4), "call_1", "rg timeout", 0, "server.go:42: timeout = 30s")...)
			rel := codexActiveRel(codexDiscUUIDA)
			onDisk := rel
			if tc.compressed {
				onDisk += ".zst"
			}
			writeCodexRollout(t, home, onDisk, plain, tc.compressed)

			res := importCodexHome(t, s, home, ImportOptions{})
			require.Equal(t, 1, res.Imported, "%+v", res.Sessions)

			// Search: the human phrase is a role=user hit tagged with the platform.
			hits, err := s.Search(context.Background(), SearchOptions{Query: "velociraptor", Role: "user"})
			require.NoError(t, err)
			require.Len(t, hits, 1)
			assert.Equal(t, codexDiscUUIDA, hits[0].SessionUUID)
			assert.Equal(t, roleUser, hits[0].Role)
			assert.Equal(t, PlatformCodex, hits[0].Platform)
			toolHits, err := s.Search(context.Background(), SearchOptions{Query: "timeout", Role: "tool"})
			require.NoError(t, err)
			require.Len(t, toolHits, 1, "the exec_command result indexes as a tool row")

			// Show: the stored platform drives the renderer.
			sess, err := s.GetSession(context.Background(), codexDiscUUIDA)
			require.NoError(t, err)
			text := RenderText(sess.Platform, sess.RawJSONL)
			assert.Contains(t, text, "[Codex]\nSure — Where does the velociraptor timeout come from?")
			assert.Contains(t, text, "[You]\nWhere does the velociraptor timeout come from?")

			// Restore: byte-identical to the decompressed input, at the relative path.
			out := filepath.Join(t.TempDir(), "restore-root")
			files, err := s.GetFiles(context.Background(), sess.UUID)
			require.NoError(t, err)
			assert.Empty(t, files, "Codex rollouts have no sidecars")
			rres, err := RestoreSessionAt(sess.UUID, sess.ClaudeProjectDir, sess.RawJSONL, files, out, nil)
			require.NoError(t, err)
			target := filepath.Join(rres.Root, filepath.FromSlash(rel))
			assert.Equal(t, []string{target}, rres.Written)
			assert.Empty(t, rres.Notes)
			got, err := os.ReadFile(target)
			require.NoError(t, err)
			assert.True(t, bytes.Equal(plain, got), "restored bytes must equal the decompressed input")
			gotHash, _ := computeContentHash(map[string][]byte{sess.UUID + ".jsonl": got})
			assert.Equal(t, sess.ContentHash, gotHash, "the framed digest over the restored bytes equals the stored content_hash")
		})
	}
}
