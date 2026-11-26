package vault

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// userLineAt / assistantLineAt mirror scanner_test.go's fixed-timestamp
// builders but take an explicit timestamp, for time-filter tests.
func userLineAt(uuid, cwd, ts string, content any) map[string]any {
	return map[string]any{
		"type": "user", "uuid": uuid, "timestamp": ts,
		"cwd": cwd, "gitBranch": "main",
		"message": map[string]any{"role": "user", "content": content},
	}
}

func assistantLineAt(uuid, msgID, ts string, blocks []map[string]any) map[string]any {
	return map[string]any{
		"type": "assistant", "uuid": uuid, "timestamp": ts,
		"message": map[string]any{"id": msgID, "role": "assistant", "content": blocks},
	}
}

const (
	chunkSearchUUIDA = "aaaa1111-2222-3333-4444-555566667777" // sampleMainJSONL + zebrafinch subagent, projA, May
	chunkSearchUUIDB = "bbbb1111-2222-3333-4444-555566667777" // zorbed/gizmoflux text, projB, June
	chunkSearchUUIDC = "cccc1111-2222-3333-4444-555566667777" // gizmoflux tool_use (title), projC, June
)

// newChunkSearchVault imports three distinguishable sessions:
//
//   - A: the sampleMainJSONL fixture ("timeout", "pterodactyl") in
//     /home/user/proj at 2026-05-01, plus a "zebrafinch" subagent whose id
//     appears ONLY in its chunk title.
//   - B: text mentioning "zorbed" and "gizmoflux" in /home/user/projB at
//     2026-06-15.
//   - C: a tool_use named "gizmoflux" in /home/user/projC at 2026-06-15 — the
//     term lands in the chunk title (Tools: …) AND, via the bare-name tool
//     summary, in the content; B carries it in content only.
func newChunkSearchVault(t *testing.T) *VaultStore {
	t.Helper()
	s := newTestVault(t)
	root := t.TempDir()

	writeSession(t, filepath.Join(root, "-home-user-proj"), chunkSearchUUIDA, sampleMainJSONL(t),
		map[string][]byte{"subagents/agent-zebrafinch.jsonl": jsonlBytes(t,
			userLine("su1", "", "", "Investigate the flaky quokka test"),
			assistantLine("sa1", "smsg1", []map[string]any{
				{"type": "text", "text": "The quokka test races on the shared port."},
			}),
		)})

	writeSession(t, filepath.Join(root, "-home-user-projB"), chunkSearchUUIDB, jsonlBytes(t,
		userLineAt("bu1", "/home/user/projB", "2026-06-15T09:00:00Z", "Why does the widget telemetry stall?"),
		assistantLineAt("ba1", "bmsg1", "2026-06-15T09:00:05Z", []map[string]any{
			{"type": "text", "text": "The zorbed widget emits gizmoflux telemetry on every restart."},
		}),
	), nil)

	writeSession(t, filepath.Join(root, "-home-user-projC"), chunkSearchUUIDC, jsonlBytes(t,
		userLineAt("cu1", "/home/user/projC", "2026-06-15T11:00:00Z", "Collect the telemetry report"),
		assistantLineAt("ca1", "cmsg1", "2026-06-15T11:00:05Z", []map[string]any{
			{"type": "text", "text": "Collecting the report now."},
			{"type": "tool_use", "id": "ct1", "name": "gizmoflux", "input": map[string]any{}},
		}),
	), nil)

	require.Equal(t, 3, importFixture(t, s, root, ImportOptions{}).Imported)
	return s
}

func searchChunks(t *testing.T, s *VaultStore, opts SearchOptions) []SearchResult {
	t.Helper()
	results, err := s.SearchChunks(context.Background(), opts)
	require.NoError(t, err)
	return results
}

// TestSearchChunksPartial pins the diagnostic entry point: losing one FTS layer
// keeps the surviving ranked hits beside the failure, losing both reports it
// with no hits, and SearchChunks keeps its best-effort policy.
func TestSearchChunksPartial(t *testing.T) {
	s := newChunkSearchVault(t)
	for _, query := range []string{"zorbing", "unmatchedquasar"} {
		got, err := s.SearchChunksPartial(t.Context(), SearchOptions{Query: query})
		require.NoError(t, err)
		require.NoError(t, got.LayerErr, "an intact vault reports no diagnostic for %q", query)
		assert.Equal(t, query == "zorbing", len(got.Results) > 0, query)
	}

	db, err := s.getDB(t.Context())
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), "DROP TABLE vault_chunks_trigram")
	require.NoError(t, err)

	// "zorbing" is porter-only, so its hit survives the trigram failure.
	got, err := s.SearchChunksPartial(t.Context(), SearchOptions{Query: "zorbing"})
	require.NoError(t, err)
	require.ErrorContains(t, got.LayerErr, "vault_chunks_trigram")
	require.NotEmpty(t, got.Results)
	assert.Equal(t, chunkSearchUUIDB, got.Results[0].SessionUUID)
	assert.NotEmpty(t, searchChunks(t, s, SearchOptions{Query: "zorbing"}), "SearchChunks stays best-effort")

	_, err = db.ExecContext(t.Context(), "DROP TABLE vault_chunks")
	require.NoError(t, err)
	got, err = s.SearchChunksPartial(t.Context(), SearchOptions{Query: "zorbing"})
	require.NoError(t, err)
	require.Error(t, got.LayerErr)
	assert.Empty(t, got.Results)

	// Validation errors stay on the error return, not the diagnostic.
	_, err = s.SearchChunksPartial(t.Context(), SearchOptions{Query: "zorbing", Raw: true})
	require.Error(t, err)
}

func TestSearchChunks_PorterOnlyTerm(t *testing.T) {
	s := newChunkSearchVault(t)

	// "zorbing" stems to "zorb" like the indexed "zorbed", but is not a
	// substring of it — only the porter layer can match.
	results := searchChunks(t, s, SearchOptions{Query: "zorbing"})
	require.NotEmpty(t, results, "porter-only term returns hits")
	assert.Equal(t, chunkSearchUUIDB, results[0].SessionUUID)
	assert.Equal(t, "porter", results[0].MatchLayer)
}

func TestSearchChunks_TrigramOnlyTerm(t *testing.T) {
	s := newChunkSearchVault(t)

	// "terodact" is a substring of the indexed "pterodactyl" but a different
	// porter token — only the trigram layer can match.
	results := searchChunks(t, s, SearchOptions{Query: "terodact"})
	require.NotEmpty(t, results, "trigram-only term returns hits")
	assert.Equal(t, chunkSearchUUIDA, results[0].SessionUUID)
	assert.Equal(t, "trigram", results[0].MatchLayer)
}

func TestSearchChunks_TitleOnlyTermMatches(t *testing.T) {
	s := newChunkSearchVault(t)

	// "zebrafinch" appears only in the subagent chunk's title
	// ("… | Subagent: zebrafinch"), never in any chunk content — a hit proves
	// the title column is indexed (review #2).
	results := searchChunks(t, s, SearchOptions{Query: "zebrafinch"})
	require.NotEmpty(t, results, "title-only term returns hits")
	r := results[0]
	assert.Equal(t, chunkSearchUUIDA, r.SessionUUID)
	assert.Equal(t, "zebrafinch", r.SubagentID)
	assert.NotContains(t, r.Content, "zebrafinch", "the term must not leak into content for this test to prove title indexing")
}

func TestSearchChunks_TitleMatchRanksSession(t *testing.T) {
	s := newChunkSearchVault(t)

	// Both B and C contain "gizmoflux" in content; only C also carries it in
	// the chunk title (Tools: gizmoflux). The BM25 title weight must rank C
	// first.
	results := searchChunks(t, s, SearchOptions{Query: "gizmoflux"})
	require.GreaterOrEqual(t, len(results), 2, "both sessions hit")
	assert.Equal(t, chunkSearchUUIDC, results[0].SessionUUID, "title match outranks content-only match")

	uuids := make(map[string]bool)
	for _, r := range results {
		uuids[r.SessionUUID] = true
	}
	assert.True(t, uuids[chunkSearchUUIDB], "content-only session still hits")
}

func TestSearchChunks_AnchorAndMetadata(t *testing.T) {
	s := newChunkSearchVault(t)

	results := searchChunks(t, s, SearchOptions{Query: "timeout"})
	require.NotEmpty(t, results)
	r := results[0]
	assert.Equal(t, chunkSearchUUIDA, r.SessionUUID)
	assert.Equal(t, "", r.SubagentID, "main-transcript chunk")
	// sampleMainJSONL's first line (raw-JSONL line 0) is the "Please fix the
	// timeout bug" user turn — the chunk's first_line_index anchor.
	assert.Equal(t, 0, r.LineIndex)
	assert.Equal(t, "Fix the timeout bug", r.Title, "session title, mirroring per-line Search")
	assert.Equal(t, "/home/user/proj", r.ProjectPath)
	assert.False(t, r.EndTime.IsZero(), "end_time carried for display")
	assert.Equal(t, "", r.Role, "role is undefined at chunk granularity")
	assert.Contains(t, r.Snippet, "[timeout]", "snippet marks the match")
	assert.Contains(t, r.Content, "Please fix the timeout bug", "full chunk content carried")
	assert.Equal(t, PlatformClaudeCode, r.Platform, "chunk hits carry the session's platform")
	assert.Empty(t, r.ParentUUID)
}

// TestSearchChunks_PlatformAndParentCarried proves the chunk corpus threads the
// 0006 columns through the retrieval engine's Meta: a Codex child session's
// chunk hit reports its platform and parent.
func TestSearchChunks_PlatformAndParentCarried(t *testing.T) {
	s := newChunkSearchVault(t)
	const parent = "c0dec0de-0000-0000-0000-0000000000aa"
	db, err := s.getDB(context.Background())
	require.NoError(t, err)
	// Flip session B into a Codex child of an (unarchived) parent — a child may
	// legitimately be archived before its parent, so no parent row is needed.
	_, err = db.Exec(`UPDATE vault_sessions SET platform = ?, parent_uuid = ? WHERE uuid = ?`,
		string(PlatformCodex), parent, chunkSearchUUIDB)
	require.NoError(t, err)

	results := searchChunks(t, s, SearchOptions{Query: "zorbing"})
	require.NotEmpty(t, results)
	assert.Equal(t, chunkSearchUUIDB, results[0].SessionUUID)
	assert.Equal(t, PlatformCodex, results[0].Platform)
	assert.Equal(t, parent, results[0].ParentUUID)
}

func TestSearchChunks_ProjectFilter(t *testing.T) {
	s := newChunkSearchVault(t)

	results := searchChunks(t, s, SearchOptions{Query: "gizmoflux", Project: "projB"})
	require.NotEmpty(t, results)
	for _, r := range results {
		assert.Equal(t, chunkSearchUUIDB, r.SessionUUID, "project filter scopes to projB")
	}

	// Literal substring, like ListSessions/Search/merge (likeContains + ESCAPE):
	// LIKE's `_` must not match the `/` in "/home/user/projB", and a bare `%`
	// must not match everything.
	for _, wildcard := range []string{"user_projB", "%"} {
		assert.Empty(t, searchChunks(t, s, SearchOptions{Query: "gizmoflux", Project: wildcard}),
			"SearchChunks --project %q must not act as a LIKE wildcard", wildcard)
	}
}

func TestSearchChunks_PlatformFilter(t *testing.T) {
	s := newChunkSearchVault(t)
	db, err := s.getDB(context.Background())
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE vault_sessions SET platform = ? WHERE uuid = ?`,
		string(PlatformCodex), chunkSearchUUIDB)
	require.NoError(t, err)

	codexOnly := searchChunks(t, s, SearchOptions{Query: "gizmoflux", Platform: PlatformCodex})
	require.NotEmpty(t, codexOnly)
	for _, result := range codexOnly {
		assert.Equal(t, chunkSearchUUIDB, result.SessionUUID)
	}

	claudeOnly := searchChunks(t, s, SearchOptions{Query: "gizmoflux", Platform: PlatformClaudeCode})
	require.NotEmpty(t, claudeOnly)
	for _, result := range claudeOnly {
		assert.NotEqual(t, chunkSearchUUIDB, result.SessionUUID)
	}
}

func TestSearchChunks_TimeFilters(t *testing.T) {
	s := newChunkSearchVault(t)
	cut := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	// B and C end 2026-06-15 — After the cut keeps them, Before drops them.
	after := searchChunks(t, s, SearchOptions{Query: "gizmoflux", After: cut})
	require.NotEmpty(t, after)
	before := searchChunks(t, s, SearchOptions{Query: "gizmoflux", Before: cut})
	assert.Empty(t, before, "gizmoflux sessions all end after the cut")

	// A ends 2026-05-01 — the inverse.
	assert.NotEmpty(t, searchChunks(t, s, SearchOptions{Query: "timeout", Before: cut}))
	assert.Empty(t, searchChunks(t, s, SearchOptions{Query: "timeout", After: cut}))
}

func TestSearchChunks_LimitAndDefault(t *testing.T) {
	s := newChunkSearchVault(t)

	results := searchChunks(t, s, SearchOptions{Query: "gizmoflux", Limit: 1})
	assert.Len(t, results, 1, "explicit limit caps results")
}

func TestSearchChunks_RejectsRoleAndRaw(t *testing.T) {
	s := newChunkSearchVault(t)

	_, err := s.SearchChunks(context.Background(), SearchOptions{Query: "timeout", Role: "user"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "role")

	_, err = s.SearchChunks(context.Background(), SearchOptions{Query: "timeout", Raw: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "raw FTS5")
}

func TestSearchChunks_EmptyQueryReturnsNil(t *testing.T) {
	s := newChunkSearchVault(t)

	results, err := s.SearchChunks(context.Background(), SearchOptions{Query: "   "})
	require.NoError(t, err)
	assert.Nil(t, results)
}

func TestChunkSnippet(t *testing.T) {
	// highlighted wraps content's FTS5 highlight() output: content with the
	// match markers removed.
	snippet := func(highlighted string) string {
		return chunkSnippet(strings.NewReplacer("\x02", "", "\x03", "").Replace(highlighted), highlighted)
	}

	t.Run("marks match and windows around it", func(t *testing.T) {
		long := strings.Repeat("a", 300) + " \x02needle\x03 " + strings.Repeat("b", 300)
		snip := snippet(long)
		assert.Contains(t, snip, "[needle]")
		assert.Less(t, len(snip), 300, "windowed, not the full content")
		assert.True(t, strings.HasPrefix(snip, "…") && strings.HasSuffix(snip, "…"))
	})

	t.Run("short content returned whole", func(t *testing.T) {
		assert.Equal(t, "a [b] c", snippet("a \x02b\x03 c"))
	})

	t.Run("no markers falls back to head", func(t *testing.T) {
		long := strings.Repeat("x", 500)
		snip := snippet(long)
		assert.True(t, strings.HasSuffix(snip, "…"))
		assert.Less(t, len(snip), 500)
	})

	t.Run("never splits a multi-byte rune", func(t *testing.T) {
		long := strings.Repeat("é", 200) + "\x02match\x03" + strings.Repeat("ü", 200)
		snip := snippet(long)
		assert.True(t, utf8ValidString(snip))
		assert.True(t, utf8.ValidString(snip))
		assert.Contains(t, snip, "[match]")
	})

	t.Run("redacts a secret crossing the window before cutting", func(t *testing.T) {
		// The secret starts inside the head window and ends past it: cutting
		// first would leave a fragment too short for the redaction pattern.
		secret := "sk-ant-" + strings.Repeat("Q", 60)
		content := strings.Repeat("x", 180) + secret + " tail"
		snip := chunkSnippet(content, content)
		assert.NotContains(t, snip, "sk-ant-")
		assert.NotContains(t, snip, "QQQQ")
		assert.Contains(t, snip, "[REDACTED_SECRET]")
	})

	t.Run("discards highlights that do not reproduce the redacted content", func(t *testing.T) {
		secret := "sk-ant-" + strings.Repeat("Q", 40)
		content := "before " + secret + " needle after"
		highlighted := "before " + secret + " \x02needle\x03 after"
		snip := chunkSnippet(content, highlighted)
		assert.NotContains(t, snip, "sk-ant-")
		assert.NotContains(t, snip, "[needle]", "stale highlight offsets are not reused")
		assert.Contains(t, snip, "needle")
	})

	t.Run("content containing marker bytes is not treated as highlighted", func(t *testing.T) {
		content := "literal \x02bytes\x03 inside"
		snip := chunkSnippet(content, content)
		assert.NotContains(t, snip, "[bytes]")
		assert.Equal(t, "literal \uFFFDbytes\uFFFD inside", snip, "marker bytes are shown, never deleted")
	})

	t.Run("a marker byte never joins fragments of a split secret", func(t *testing.T) {
		// Neither fragment is a recognizable secret; deleting the marker would
		// join them into one.
		content := "head sk-ant-" + strings.Repeat("A", 10) + "\x02" + strings.Repeat("A", 20) + " tail"
		snip := chunkSnippet(content, content)
		assert.NotContains(t, snip, "sk-ant-"+strings.Repeat("A", 30))
		assert.Contains(t, snip, "sk-ant-"+strings.Repeat("A", 10)+"\uFFFD")
	})

	t.Run("a marker never joins a redacted value to its suffix", func(t *testing.T) {
		// As stored after per-message redaction: the placeholder stays
		// separated from the text after the marker.
		content := "head [REDACTED_SECRET]\x02SUFFIXTAILXYZ end"
		snip := chunkSnippet(content, content)
		assert.Equal(t, "head [REDACTED_SECRET]\uFFFDSUFFIXTAILXYZ end", snip)
	})

	t.Run("a private span crossing joined messages is redacted whole", func(t *testing.T) {
		// Only redaction of the complete content recognizes the span; the
		// marker inside the early "</priv\x02ate>" must not close it.
		content := "head <private>first</priv\x02ate> hiddentail</private> after"
		snip := chunkSnippet(content, content)
		assert.NotContains(t, snip, "hiddentail")
		assert.NotContains(t, snip, "first")
		assert.Equal(t, "head [REDACTED] after", snip)
	})

	t.Run("invalid UTF-8 is normalized", func(t *testing.T) {
		content := "bad \xff byte"
		snip := chunkSnippet(content, content)
		assert.True(t, utf8.ValidString(snip))
	})
}

func utf8ValidString(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}
