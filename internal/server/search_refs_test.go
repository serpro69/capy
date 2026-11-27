package server

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/serpro69/capy/internal/store"
	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Task 3 (compact-search-replies): a passage repeated within one reply is
// cited by the response-local number of its first full block, and only a
// matching corpus, location, window and rendered block establishes a repeat.

func TestReplyWriter_NumbersEntriesAndCitesRepeats(t *testing.T) {
	a := renderedHit{block: "A", key: passageKey{corpus: "knowledge", block: "A"}, citable: true}
	b := renderedHit{block: "B", key: passageKey{corpus: "knowledge", block: "B"}}

	w := newReplyWriter(searchReplyMaxBytes)
	w.startGroup("## 1. q")
	for _, h := range []renderedHit{a, a, b, b, a} {
		require.True(t, w.addHit(h))
	}
	assert.Equal(t, "## 1. q\n\n[1] A\n\n↑ same passage as [1]\n\n[2] B\n\n[3] B\n\n↑ same passage as [1]", w.String(),
		"a non-citable repeat is shown in full under a new number")
}

func TestReplyWriter_RegistersOnlyCommittedBlocks(t *testing.T) {
	big := renderedHit{block: strings.Repeat("x", 100), key: passageKey{block: "big"}, citable: true}
	entry := len(hitSeparator) + len("[1] ") + len(big.block)

	w := newReplyWriter(searchNoticeReserve + entry - 1)
	assert.False(t, w.addHit(big))
	assert.False(t, w.addHit(big), "a block that did not fit is not citable")
	assert.Empty(t, w.String())
	assert.Zero(t, w.entries)

	w = newReplyWriter(searchNoticeReserve + entry + len(hitSeparator) + len(referenceBlock(1)))
	assert.True(t, w.addHit(big))
	assert.True(t, w.addHit(big), "the reference fits the remaining space")
	assert.Equal(t, "\n\n[1] "+big.block+"\n\n↑ same passage as [1]", w.String())
}

func TestPassageIdentity_Knowledge(t *testing.T) {
	base := store.SearchResult{
		Label: "doc", Title: "Title", Content: "body with needle", SourceID: 7,
		Table: "chunks", RowID: 3,
	}
	repeat := renderKnowledgeHit(base, "needle")
	require.True(t, repeat.citable)
	assert.Equal(t, repeat.key, renderKnowledgeHit(base, "needle").key, "the same row and window is a repeat")

	t.Run("distinct FTS tables sharing a rowid", func(t *testing.T) {
		other := base
		other.Table = "chunks_trigram"
		got := renderKnowledgeHit(other, "needle")
		assert.Equal(t, repeat.block, got.block, "the blocks look identical")
		assert.NotEqual(t, repeat.key, got.key)
	})

	t.Run("colliding titles and clipped label prefixes", func(t *testing.T) {
		prefix := strings.Repeat("L", knowledgeLabelMaxBytes)
		first, second := base, base
		first.Label, second.Label = prefix+"-first", prefix+"-second"
		second.SourceID, second.RowID = 8, 4
		f, s := renderKnowledgeHit(first, "needle"), renderKnowledgeHit(second, "needle")
		assert.Equal(t, f.block, s.block, "clipping hides the label difference")
		require.True(t, f.citable && s.citable, "a long label is still citable")
		assert.NotEqual(t, f.key, s.key)
		assert.Equal(t, f.key, renderKnowledgeHit(first, "needle").key, "the same long-labelled row is a repeat")

		// Equal labels and titles from different rows stay distinct.
		other := base
		other.SourceID, other.RowID = 8, 4
		o := renderKnowledgeHit(other, "needle")
		require.True(t, o.citable)
		assert.Equal(t, repeat.block, o.block)
		assert.NotEqual(t, repeat.key, o.key)
	})

	t.Run("a different window of the same chunk", func(t *testing.T) {
		long := base
		long.Content = "alpha " + strings.Repeat("x", 3000) + " omega"
		assert.NotEqual(t, renderKnowledgeHit(long, "alpha").key, renderKnowledgeHit(long, "omega").key)
	})

	t.Run("equal windows at different offsets", func(t *testing.T) {
		filler := strings.Repeat("a", 1000)
		long := base
		long.Content = filler + " needle " + filler + " needle " + filler
		second := 2*len(filler) + len(" needle ") + 1
		markFirst := long
		markFirst.Highlighted = filler + " \x02needle\x03 " + filler + " needle " + filler
		markSecond := long
		markSecond.Highlighted = long.Content[:second] + "\x02needle\x03" + long.Content[second+len("needle"):]
		f, s := renderKnowledgeHit(markFirst, "needle"), renderKnowledgeHit(markSecond, "needle")
		assert.Equal(t, f.block, s.block)
		assert.NotEqual(t, f.key, s.key, "the window starts tell the occurrences apart")
	})

	t.Run("windows clipped to a common prefix", func(t *testing.T) {
		// Both selections start at byte 0 and run past the passage cap, so
		// clipping leaves the same text; their window ends still differ.
		words := strings.Repeat("word ", 1000)
		mark := func(n int) string {
			return strings.Repeat("\x02word\x03 ", n) + strings.Repeat("word ", 1000-n)
		}
		long := base
		long.Content = words
		wide, narrow := long, long
		wide.Highlighted, narrow.Highlighted = mark(400), mark(360)
		w, n := renderKnowledgeHit(wide, "word"), renderKnowledgeHit(narrow, "word")
		assert.Equal(t, w.block, n.block)
		assert.NotEqual(t, w.key, n.key)
	})

	t.Run("a passage selected from many windows", func(t *testing.T) {
		// Matches 650 bytes apart never merge, so each forms its own window;
		// the key retains a fixed-size digest of them all.
		window := "needle " + strings.Repeat("x", 643)
		many := base
		many.Content = strings.Repeat(window, 1000)
		h := renderKnowledgeHit(many, "needle")
		require.True(t, h.citable)
		assert.Equal(t, h.key, renderKnowledgeHit(many, "needle").key)

		// A later window outside the shown passage still distinguishes selections.
		fewer := many
		fewer.Content = strings.Repeat(window, 999) + strings.Repeat("y", len(window))
		f := renderKnowledgeHit(fewer, "needle")
		assert.Equal(t, h.block, f.block, "the shown passage is the same")
		assert.NotEqual(t, h.key, f.key)
	})

	t.Run("an unlocated row is never cited", func(t *testing.T) {
		unlocated := base
		unlocated.Table, unlocated.RowID = "", 0
		assert.False(t, renderKnowledgeHit(unlocated, "needle").citable)
	})

	t.Run("identical passages in different corpora", func(t *testing.T) {
		k := renderKnowledgeHit(store.SearchResult{Label: "session:abc", Title: "T", Content: "same text", Table: "chunks", RowID: 1}, "same")
		v := renderVaultHit(vault.SearchResult{SessionUUID: "abc", Title: "T", Snippet: "same text", HasChunkIndex: true})
		assert.NotEqual(t, k.key, v.key)
		w := newReplyWriter(searchReplyMaxBytes)
		require.True(t, w.addHit(k))
		require.True(t, w.addHit(v))
		assert.NotContains(t, w.String(), "↑")
	})
}

func TestRenderVaultHit_ChunkAttribution(t *testing.T) {
	main := vault.SearchResult{
		SessionUUID: "11111111-0000-4000-8000-000000000001", Title: "Session",
		LineIndex: 4, ChunkIndex: 0, HasChunkIndex: true, Snippet: "shared text",
	}
	sidecar := main
	sidecar.SubagentID, sidecar.ChunkIndex = "agent-explorer", 9

	m, s := renderVaultHit(main), renderVaultHit(sidecar)
	assert.Contains(t, m.block, "\nchunk starts at line 4 · main transcript · claude-code\n"+vaultBriefAction+"\n")
	assert.NotContains(t, m.block, "sidecar")
	assert.Contains(t, s.block, "\nchunk starts at line 4 · sidecar agent-explorer · claude-code\n\n")
	assert.NotContains(t, s.block, vaultBriefAction, "a sidecar hit offers no brief")
	assert.True(t, m.citable, "chunk index zero is a valid coordinate")
	assert.NotEqual(t, m.key, s.key, "main and sidecar hits at the same line are distinct")

	t.Run("split windows sharing a first line", func(t *testing.T) {
		next := main
		next.ChunkIndex = 1
		n := renderVaultHit(next)
		assert.Equal(t, m.block, n.block)
		assert.NotEqual(t, m.key, n.key)
		w := newReplyWriter(searchReplyMaxBytes)
		for _, h := range []renderedHit{m, s, n, m, s} {
			require.True(t, w.addHit(h))
		}
		assert.Equal(t, []string{"[1]", "[2]", "[3]", "↑1", "↑2"}, entryMarkers(w.String()),
			"each chunk keeps its own entry and repeats cite the right one")
	})

	t.Run("a missing coordinate still renders a full hit", func(t *testing.T) {
		missing := main
		missing.HasChunkIndex = false
		h := renderVaultHit(missing)
		assert.False(t, h.citable)
		assert.Equal(t, m.block, h.block)
		w := newReplyWriter(searchReplyMaxBytes)
		require.True(t, w.addHit(h))
		require.True(t, w.addHit(h))
		assert.Equal(t, []string{"[1]", "[2]"}, entryMarkers(w.String()))
	})

	t.Run("an altered sidecar ID is marked with its chunk", func(t *testing.T) {
		secret := sidecar
		secret.SubagentID = "sk-ant-" + strings.Repeat("Q", 40)
		assert.Contains(t, renderVaultHit(secret).block, "sidecar [REDACTED_SECRET] (ID redacted; chunk 9)")

		long := sidecar
		long.SubagentID = strings.Repeat("s", vaultSidecarIDMaxBytes+1)
		got := renderVaultHit(long)
		assert.Contains(t, got.block, omissionMarker+" (ID shortened; chunk 9)")
		other := long
		other.SubagentID += "x"
		assert.NotEqual(t, got.key, renderVaultHit(other).key, "IDs sharing a preview stay distinct")

		long.HasChunkIndex = false
		assert.Contains(t, renderVaultHit(long).block, omissionMarker+" (ID shortened) · ")
	})

	t.Run("a rename hidden by the title preview is not a repeat", func(t *testing.T) {
		// Each line break displays as two bytes, so the preview is clipped
		// before the final character while the stored title stays in bounds.
		before, after := main, main
		before.Title = strings.Repeat("\n", 130) + "a"
		after.Title = strings.Repeat("\n", 130) + "b"
		b, a := renderVaultHit(before), renderVaultHit(after)
		require.True(t, b.citable)
		assert.Equal(t, b.block, a.block, "the clipped previews are identical")
		w := newReplyWriter(searchReplyMaxBytes)
		require.True(t, w.addHit(b))
		require.True(t, w.addHit(a))
		assert.Equal(t, []string{"[1]", "[2]"}, entryMarkers(w.String()), "the renamed session is shown in full")
	})

	t.Run("long identity fields are compared in full", func(t *testing.T) {
		for name, vary := range map[string]func(r *vault.SearchResult, suffix string){
			"session": func(r *vault.SearchResult, x string) { r.SessionUUID = strings.Repeat("u", vaultSessionIDMaxBytes) + x },
			"passage": func(r *vault.SearchResult, x string) { r.Snippet = strings.Repeat("p", searchPassageMaxBytes) + x },
			"title":   func(r *vault.SearchResult, x string) { r.Title = strings.Repeat("€", 90) + x },
			"project": func(r *vault.SearchResult, x string) { r.Project = strings.Repeat("p", vaultProjectMaxBytes) + x },
			"platform": func(r *vault.SearchResult, x string) {
				r.Platform = vault.Platform(strings.Repeat("c", vaultMetaFieldMaxBytes) + x)
			},
		} {
			a, b := main, main
			vary(&a, "a")
			vary(&b, "b")
			ha, hb := renderVaultHit(a), renderVaultHit(b)
			require.True(t, ha.citable, name)
			assert.Equal(t, ha.block, hb.block, "%s: the previews are identical", name)
			assert.NotEqual(t, ha.key, hb.key, name)
			assert.Equal(t, ha.key, renderVaultHit(a).key, "%s: the same hit is a repeat", name)
		}
	})
}

// entryMarkers lists each entry of a reply as "[n]" for a full block or "↑n"
// for a reference.
func entryMarkers(reply string) []string {
	var out []string
	for _, m := range entryRe.FindAllStringSubmatch(reply, -1) {
		if m[1] != "" {
			out = append(out, "["+m[1]+"]")
		} else {
			out = append(out, "↑"+m[2])
		}
	}
	return out
}

var entryRe = regexp.MustCompile(`(?m)^(?:\[(\d+)\] |↑ same passage as \[(\d+)\]$)`)

// replyGroups splits a capy_search reply into its query groups and expands
// every reference into the full block it cites, with entry numbers removed. It
// also returns the full blocks by number.
func replyGroups(t *testing.T, reply string) ([][]string, map[string]string) {
	t.Helper()
	if i := strings.Index(reply, noticeSeparator+"Shared MCP server search budget"); i >= 0 {
		reply = reply[:i]
	}
	full := map[string]string{}
	var groups [][]string
	for _, group := range strings.Split(reply, groupSeparator) {
		locs := entryRe.FindAllStringSubmatchIndex(group, -1)
		var entries []string
		for i, loc := range locs {
			end := len(group)
			if i+1 < len(locs) {
				end = locs[i+1][0] - len(hitSeparator)
			}
			if loc[2] >= 0 { // full block
				n := group[loc[2]:loc[3]]
				block := group[loc[1]:end]
				full[n] = block
				entries = append(entries, block)
				continue
			}
			n := group[loc[4]:loc[5]]
			block, ok := full[n]
			require.True(t, ok, "reference [%s] cites an earlier full block", n)
			entries = append(entries, block)
		}
		groups = append(groups, entries)
	}
	return groups, full
}

// TestSearch_RepeatedPassagesCiteEarlierEntries compares a duplicate-heavy
// batch, with references expanded, against each query searched alone.
func TestSearch_RepeatedPassagesCiteEarlierEntries(t *testing.T) {
	srv := newTestServer(t, nil)
	st := srv.getStore()
	for label, content := range map[string]string{
		"zephyr-one": "zephyr quartz notes for the first document",
		"zephyr-two": "zephyr notes for the second document",
	} {
		_, err := st.IndexChunked(content, label, "plaintext", store.KindDurable,
			[]store.Chunk{{Title: "Shared Title", Content: content}})
		require.NoError(t, err)
	}
	long := "firstterm opens the chunk " + strings.Repeat("filler words here ", 200) + " lastterm closes it"
	_, err := st.IndexChunked(long, "window-doc", "plaintext", store.KindDurable,
		[]store.Chunk{{Title: "Window Doc", Content: long}})
	require.NoError(t, err)

	queries := []any{"zephyr", "zephyr", "firstterm", "lastterm", "firstterm"}
	search := func(qs ...any) string {
		resetSearchThrottle(srv)
		res := callSearch(t, srv, map[string]any{"queries": qs, "include_kinds": []any{"durable"}, "limit": 2})
		require.False(t, res.IsError, resultText(res))
		return resultText(res)
	}

	batch := search(queries...)
	assert.Equal(t, []string{"[1]", "[2]", "↑1", "↑2", "[3]", "[4]", "↑3"}, entryMarkers(batch),
		"query 2 is all references, query 4 shows another window of query 3's chunk, query 5 cites query 3")
	assert.Contains(t, batch, "## 2. zephyr\n\n↑ same passage as [1]\n\n↑ same passage as [2]")

	groups, full := replyGroups(t, batch)
	require.Len(t, groups, len(queries))
	for i, q := range queries {
		alone, _ := replyGroups(t, search(q))
		require.Len(t, alone, 1)
		assert.Equal(t, alone[0], groups[i], "query %d expands to its own source/rank sequence", i+1)
	}

	// Bytes a reply repeating every block in full would have needed.
	saved := 0
	for _, m := range entryMarkers(batch) {
		if n, ok := strings.CutPrefix(m, "↑"); ok {
			num, err := strconv.Atoi(n)
			require.NoError(t, err)
			saved += len("["+n+"] ") + len(full[n]) - len(referenceBlock(num))
		}
	}
	t.Logf("duplicate-heavy batch: %d reply bytes, %d bytes saved by references", len(batch), saved)
	assert.Positive(t, saved)
}

// TestSearch_RepeatedVaultPassageCitesEarlierEntry covers the federated path:
// knowledge and vault hits repeated by a later query become references.
func TestSearch_RepeatedVaultPassageCitesEarlierEntry(t *testing.T) {
	projectDir, uuid1, _ := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
	srv := newTestServerWithProjectDir(t, nil, projectDir)
	srv.vaultSweep(context.Background())
	seedDurableDBGuide(t, srv)

	res := callSearch(t, srv, map[string]any{"queries": []any{"configure the database", "configure the database"}})
	require.False(t, res.IsError)
	text := resultText(res)
	require.Contains(t, text, "[session:"+uuid1+"]")
	assert.Contains(t, text, "chunk starts at line ")

	markers := entryMarkers(text)
	require.Len(t, markers, 4, text)
	assert.Equal(t, []string{"[1]", "[2]", "↑1", "↑2"}, markers)
	groups, _ := replyGroups(t, text)
	assert.Equal(t, groups[0], groups[1])
	assert.Equal(t, 1, strings.Count(text, "[session:"+uuid1+"]"), "the session passage is shown once:\n%s", text)
	assert.Equal(t, []string{uuid1}, briefActionSessions(text),
		"the federated reference cites the full entry carrying the latest-exchange instruction")
}
