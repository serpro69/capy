package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Task 4 (compact-search-replies): capy_vault_search follows the same reply
// contract as capy_search — numbered groups, numbered entries and references,
// a 40,960-byte bound and an inclusive retry — without shared admission.

func trackedVaultSearchBytes(srv *Server) int64 {
	return srv.stats.Snapshot().BytesReturned["capy_vault_search"]
}

func vaultSearchCalls(srv *Server) int {
	return srv.stats.Snapshot().Calls["capy_vault_search"]
}

// maxVaultHit is a distinct vault hit whose every display field exceeds its
// cap, so its rendered block is as large as a vault hit gets.
func maxVaultHit(i int) vault.SearchResult {
	huge := strings.Repeat("ű", 2000)
	id := fmt.Sprintf("hit-%02d-", i)
	return vault.SearchResult{
		SessionUUID:   id + huge,
		SubagentID:    id + huge,
		Title:         id + huge,
		Project:       id + huge,
		EndTime:       time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		LineIndex:     1_000_000 + i,
		ChunkIndex:    i,
		HasChunkIndex: true,
		Platform:      vault.Platform(id + huge),
		ParentUUID:    id + huge,
		Snippet:       id + strings.Repeat("passage ", 400),
	}
}

// stubVaultChunks serves capy_vault_search's retrieval from fixed ranked hits
// per query, honoring the requested limit like the real engine.
func stubVaultChunks(srv *Server, hits map[string][]vault.SearchResult) {
	srv.vaultChunkSearch = func(_ context.Context, opts vault.SearchOptions) (vault.PartialChunkSearch, error) {
		r := hits[opts.Query]
		return vault.PartialChunkSearch{Results: r[:min(len(r), opts.Limit)]}, nil
	}
}

func maxVaultHits(n int) []vault.SearchResult {
	out := make([]vault.SearchResult, n)
	for i := range out {
		out[i] = maxVaultHit(i)
	}
	return out
}

// entryBytes is what the writer charges for full entry n.
func entryBytes(n int, r vault.SearchResult) int {
	return len(hitSeparator) + len(fmt.Sprintf("[%d] ", n)) + len(renderVaultHit(r).block)
}

func TestVaultSearchReply_Contract(t *testing.T) {
	projectDir, uuid1, _ := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
	srv := newTestServerWithProjectDir(t, nil, projectDir)
	srv.vaultSweep(t.Context())

	call := func(t *testing.T, args map[string]any) string {
		t.Helper()
		before := trackedVaultSearchBytes(srv)
		res, err := srv.handleVaultSearch(t.Context(), vaultSearchReq(args))
		require.NoError(t, err)
		require.False(t, res.IsError, resultText(res))
		return assertBoundedToolReply(t, srv, "capy_vault_search", res, before)
	}

	t.Run("numbered groups without admission or footer", func(t *testing.T) {
		admitted := srv.throttle.count
		out := call(t, map[string]any{"queries": []any{"configure the database", "zzzzz-no-such-term-anywhere"}})
		assert.Contains(t, out, "## 1. configure the database\n\n[1] --- [session:"+uuid1+"] ---")
		assert.Contains(t, out, "## 2. zzzzz-no-such-term-anywhere\nNo results found.")
		assert.NotContains(t, out, "Shared MCP server search budget")
		assert.NotContains(t, out, "Reply budget reached")
		assert.Equal(t, admitted, srv.throttle.count, "no shared search admission")
	})

	t.Run("a repeated passage is cited", func(t *testing.T) {
		out := call(t, map[string]any{"queries": []any{"configure the database", "configure the database"}, "limit": 1})
		assert.Equal(t, 1, strings.Count(out, "[session:"+uuid1+"]"))
		assert.Contains(t, out, "## 2. configure the database\n\n↑ same passage as [1]")
	})

	t.Run("date filters", func(t *testing.T) {
		// The fixture sessions end on 2026-04-05.
		for _, tt := range []struct {
			name string
			args map[string]any
			hit  bool
		}{
			{"after the sessions", map[string]any{"after": "2030-01-01"}, false},
			{"before the sessions", map[string]any{"before": "2026-01-01"}, false},
			{"inclusive end day", map[string]any{"before": "2026-04-05"}, true},
			{"window around the sessions", map[string]any{"after": "2026-04-01", "before": "2026-04-30T00:00:00Z"}, true},
		} {
			t.Run(tt.name, func(t *testing.T) {
				args := map[string]any{"queries": []any{"configure the database"}}
				for k, v := range tt.args {
					args[k] = v
				}
				out := call(t, args)
				if tt.hit {
					assert.Contains(t, out, "[session:"+uuid1+"]")
				} else {
					assert.Contains(t, out, "## 1. configure the database\nNo results found.")
				}
			})
		}
	})
}

func TestVaultSearchReply_Disabled(t *testing.T) {
	projectDir, _, _ := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", "")
	srv := newTestServerWithProjectDir(t, nil, projectDir)

	before := trackedVaultSearchBytes(srv)
	res, err := srv.handleVaultSearch(t.Context(), vaultSearchReq(map[string]any{"queries": []any{"anything"}}))
	require.NoError(t, err)
	require.True(t, res.IsError)
	text := assertBoundedToolReply(t, srv, "capy_vault_search", res, before)
	assert.Contains(t, text, "CAPY_VAULT_KEY")
}

func TestVaultSearchReply_OversizedInvalidDate(t *testing.T) {
	projectDir, _, _ := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
	srv := newTestServerWithProjectDir(t, nil, projectDir)

	// The secret starts inside the preview, so only redaction hides it.
	date := "ghp_" + strings.Repeat("A", 36) + strings.Repeat("not-a-date", 20000)
	for _, name := range []string{"after", "before"} {
		t.Run(name, func(t *testing.T) {
			res, err := srv.handleVaultSearch(t.Context(), vaultSearchReq(map[string]any{
				"queries": []any{"database"},
				name:      date,
			}))
			require.NoError(t, err)
			require.True(t, res.IsError)
			text := decodedText(t, res)
			assert.True(t, utf8.ValidString(text))
			assert.Less(t, len(text), 512)
			assert.NotContains(t, text, "ghp_")
			assert.Contains(t, text, "invalid date")
			assert.Zero(t, vaultSearchCalls(srv), "validation errors stay untracked")
		})
	}
}

// Main-transcript and sidecar chunks of one session with identical text are
// distinct passages: each names where its chunk starts, only the sidecar hit
// names its sidecar, and neither cites the other.
func TestVaultSearchReply_MainAndSidecarAttribution(t *testing.T) {
	projectDir, uuid1, _ := setupVaultSweepProject(t)
	sessionDir, err := vault.ProjectSessionDir(projectDir)
	require.NoError(t, err)
	subagents := filepath.Join(sessionDir, uuid1, "subagents")
	require.NoError(t, os.MkdirAll(subagents, 0o755))
	writeSessionJSONL(t, subagents, "agent-helper", "How do I configure the database?",
		"Set DATABASE_URL in your environment to configure the database.")
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
	srv := newTestServerWithProjectDir(t, nil, projectDir)
	srv.vaultSweep(t.Context())

	res, err := srv.handleVaultSearch(t.Context(), vaultSearchReq(map[string]any{
		"queries": []any{"configure the database"},
		"limit":   10,
	}))
	require.NoError(t, err)
	require.False(t, res.IsError, resultText(res))
	out := resultText(res)

	var main, sidecar int
	for _, block := range strings.Split(out, hitSeparator+"[")[1:] {
		if !strings.Contains(block, "[session:"+uuid1+"]") {
			continue
		}
		assert.Regexp(t, `· chunk from line \d+ ·`, block)
		if strings.Contains(block, " · sidecar helper · ") {
			sidecar++
		} else {
			assert.NotContains(t, block, "sidecar")
			main++
		}
	}
	assert.Positive(t, main, "a main-transcript hit:\n%s", out)
	assert.Positive(t, sidecar, "a sidecar hit:\n%s", out)
	assert.NotContains(t, out, "same passage as", "main and sidecar passages are never cited for each other")
}

func TestVaultSearchReply_BacklogHintOnce(t *testing.T) {
	projectDir, uuid1, uuid2 := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
	srv := newTestServerWithProjectDir(t, nil, projectDir)
	srv.vaultSweep(t.Context())
	stampVaultIndexVersion(t, uuid1, 1)
	stampVaultIndexVersion(t, uuid2, 1)

	before := trackedVaultSearchBytes(srv)
	res, err := srv.handleVaultSearch(t.Context(), vaultSearchReq(map[string]any{
		"queries":      []any{"zzzzz-no-such-term-anywhere", "yyyyy-no-such-term-either"},
		"all_projects": true,
	}))
	require.NoError(t, err)
	require.False(t, res.IsError)
	out := assertBoundedToolReply(t, srv, "capy_vault_search", res, before)
	assert.Equal(t, 2, strings.Count(out, "No results found."))
	assert.Equal(t, 1, strings.Count(out, "capy vault reindex"), "the backlog hint appears once per reply")
}

// Task 4.2: a supplied malformed queries array is rejected before the vault
// is searched or the call is tracked, even with a valid fallback query.
func TestVaultSearchReply_QueryArrays(t *testing.T) {
	projectDir, _, _ := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
	srv := newTestServerWithProjectDir(t, nil, projectDir)
	stubVaultChunks(srv, map[string][]vault.SearchResult{
		"alpha": {maxVaultHit(1)},
		"beta":  {maxVaultHit(2)},
	})
	var dispatched []int
	srv.searchDispatched = func(ordinal int, _ string) { dispatched = append(dispatched, ordinal) }

	for _, tt := range []struct {
		name    string
		queries any
	}{
		{"native non-string member", []any{"alpha", 7, "beta"}},
		{"serialized non-string member", `["alpha", 7, "beta"]`},
		{"serialized non-array", `"alpha"`},
		{"unparseable string", "alpha"},
		{"null", nil},
		{"object", map[string]any{"q": "alpha"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dispatched = nil
			res, err := srv.handleVaultSearch(t.Context(), vaultSearchReq(map[string]any{
				"queries": tt.queries,
				"query":   "alpha",
			}))
			require.NoError(t, err)
			require.True(t, res.IsError)
			assert.Equal(t, `invalid parameter "queries": expected an array of strings`, resultText(res))
			assert.Empty(t, dispatched, "nothing is searched")
			assert.Zero(t, vaultSearchCalls(srv), "validation errors stay untracked")
		})
	}

	for _, tt := range []struct {
		name string
		args map[string]any
		want []string
	}{
		{"valid serialized array", map[string]any{"queries": `["alpha","beta"]`}, []string{"## 1. alpha", "## 2. beta"}},
		{"empty array falls back to query", map[string]any{"queries": []any{}, "query": "beta"}, []string{"## 1. beta"}},
		{"empty serialized array falls back to query", map[string]any{"queries": `[]`, "query": "beta"}, []string{"## 1. beta"}},
		{"absent array uses query", map[string]any{"query": "alpha"}, []string{"## 1. alpha"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res, err := srv.handleVaultSearch(t.Context(), vaultSearchReq(tt.args))
			require.NoError(t, err)
			require.False(t, res.IsError, resultText(res))
			out := resultText(res)
			for _, w := range tt.want {
				assert.Contains(t, out, w)
			}
		})
	}

	t.Run("not disabled first", func(t *testing.T) {
		t.Setenv("CAPY_VAULT_KEY", "")
		disabled := newTestServerWithProjectDir(t, nil, projectDir)
		res, err := disabled.handleVaultSearch(t.Context(), vaultSearchReq(map[string]any{"queries": []any{"alpha", false}}))
		require.NoError(t, err)
		require.True(t, res.IsError)
		assert.Contains(t, resultText(res), `invalid parameter "queries"`, "validation precedes vault access")
	})

	t.Run("boundary keeps original input positions", func(t *testing.T) {
		stubVaultChunks(srv, map[string][]vault.SearchResult{"boundary": maxVaultHits(10)})
		dispatched = nil
		// Query 1 misses; the reply leaves room for query 2's frame but not
		// for its first hit.
		req := vaultSearchReq(map[string]any{"queries": `["miss","boundary","suffix"]`, "limit": 10})
		replyMax := searchNoticeReserve + len(queryFrame(1, "miss", nil, 0)) + searchFrameMaxBytes
		res, err := srv.vaultSearch(t.Context(), req, replyMax)
		require.NoError(t, err)
		out := resultText(res)
		assert.Contains(t, out, "## 1. miss\nNo results found.")
		assert.Contains(t, out, "## 2. boundary")
		assert.Contains(t, out, "Query 2 showed 0 of 10 hit(s)")
		assert.Equal(t, []int{1, 2}, dispatched)
	})
}

// Task 4.3: the budget stops the batch at a boundary query that ran with no
// hit fitting, one partly shown, or one never dispatched; retrying the boundary
// query alone shows all ten maximum-size hits.
func TestVaultSearchReply_Boundaries(t *testing.T) {
	projectDir, _, _ := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
	srv := newTestServerWithProjectDir(t, nil, projectDir)
	ten := maxVaultHits(10)
	stubVaultChunks(srv, map[string][]vault.SearchResult{
		"boundary": ten,
		"single":   ten[:1],
	})
	var dispatched []string
	srv.searchDispatched = func(_ int, q string) { dispatched = append(dispatched, q) }

	for _, tt := range []struct {
		name       string
		queries    []any
		replyMax   int
		dispatched []string
		want       []string
		absent     []string
	}{
		{
			name:    "boundary ran with zero hits fitting",
			queries: []any{"miss", "boundary", "suffix"},
			// Room for query 2's frame but not its first hit.
			replyMax:   searchNoticeReserve + len(queryFrame(1, "miss", nil, 0)) + searchFrameMaxBytes,
			dispatched: []string{"miss", "boundary"},
			want: []string{
				"## 2. boundary",
				"Query 2 showed 0 of 10 hit(s) returned at limit 10/query; 1 following query was not searched.",
				"retry queries 2–3 (inclusive)",
			},
			absent: []string{"[1] ", "## 3."},
		},
		{
			name:    "boundary partly shown",
			queries: []any{"boundary", "suffixalpha", "suffixbeta"},
			// Room for the frame and the first entry, not the second.
			replyMax:   searchNoticeReserve + len(queryFrame(1, "boundary", nil, 10)) + entryBytes(1, ten[0]) + entryBytes(2, ten[1]) - 1,
			dispatched: []string{"boundary"},
			want: []string{
				"[1] --- [session:hit-00-",
				"Query 1 showed 1 of 10 hit(s) returned at limit 10/query; 2 following queries were not searched.",
				"retry queries 1–3 (inclusive)",
			},
			absent: []string{"[2] ", "## 2.", "reset time"},
		},
		{
			name:    "boundary skipped before dispatch",
			queries: []any{"single", "boundary", "suffix"},
			// One byte short of a maximal frame after query 1.
			replyMax:   searchNoticeReserve + len(queryFrame(1, "single", nil, 1)) + entryBytes(1, ten[0]) + searchFrameMaxBytes - 1,
			dispatched: []string{"single"},
			want: []string{
				"[1] --- [session:hit-00-",
				"Query 2 was not searched; 1 following query was not searched.",
				"retry queries 2–3 (inclusive)",
			},
			absent: []string{"## 2.", "## 3."},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dispatched = nil
			before := trackedVaultSearchBytes(srv)
			res, err := srv.vaultSearch(t.Context(), vaultSearchReq(map[string]any{"queries": tt.queries, "limit": 10}), tt.replyMax)
			require.NoError(t, err)
			require.False(t, res.IsError)
			out := assertBoundedToolReply(t, srv, "capy_vault_search", res, before)
			assert.LessOrEqual(t, len(out), tt.replyMax)
			assert.Equal(t, tt.dispatched, dispatched, "the suffix never ran")
			for _, w := range tt.want {
				assert.Contains(t, out, w)
			}
			for _, a := range tt.absent {
				assert.NotContains(t, out, a)
			}
			assert.Equal(t, 1, strings.Count(out, "Reply budget reached"))
		})
	}

	t.Run("retrying the boundary query alone shows all ten maximum-size hits", func(t *testing.T) {
		for i, h := range ten {
			assert.Greater(t, entryBytes(i+1, h), searchHitMaxBytes*9/10,
				"hit %d renders near the per-hit maximum", i)
		}
		dispatched = nil
		before := trackedVaultSearchBytes(srv)
		res, err := srv.handleVaultSearch(t.Context(), vaultSearchReq(map[string]any{
			"queries": []any{"boundary"},
			"limit":   10,
		}))
		require.NoError(t, err)
		require.False(t, res.IsError)
		out := assertBoundedToolReply(t, srv, "capy_vault_search", res, before)
		assert.Equal(t, []string{"boundary"}, dispatched)
		for n := 1; n <= 10; n++ {
			assert.Contains(t, out, fmt.Sprintf("[%d] --- [session:hit-%02d-", n, n-1))
		}
		assert.NotContains(t, out, "same passage as", "distinct hits are shown in full")
		assert.NotContains(t, out, "Reply budget reached")
		t.Logf("ten maximum-size vault hits: %d decoded bytes", len(out))
	})
}

// The vault partial notice omits capy_search's reduced-limit advice: the
// dedicated limit is never tapered.
func TestPartialGroupNotice(t *testing.T) {
	got := partialGroupNotice(4, 7, 1, 2, 3)
	assert.Equal(t, "⚠ Reply budget reached. Query 4 showed 1 of 2 hit(s) returned at limit 3/query; 3 following queries were not searched."+
		" To continue, retry queries 4–7 (inclusive) with the same selectors and requested limit; numbering restarts at 1 and the retry searches current data from the first rank.", got)
	assert.True(t, strings.HasPrefix(partialNotice(4, 7, 1, 2, 3), got))
	assert.Contains(t, partialNotice(4, 7, 1, 2, 3), "reset time")
}
