package server

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/store"
	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Task 2 (compact-search-replies): every capy_search reply fits the 40,960-byte
// budget, display fields are redacted before clipping, and an interrupted
// batch names an inclusive retry range.

// decodedText round-trips a tool result through JSON, as an MCP client would,
// and returns the text item the client reads.
func decodedText(t *testing.T, r *mcp.CallToolResult) string {
	t.Helper()
	encoded, err := json.Marshal(r)
	require.NoError(t, err)
	var decoded struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.Len(t, decoded.Content, 1)
	return decoded.Content[0].Text
}

// assertBoundedReply checks the client-visible text: valid UTF-8, within the
// reply budget, and equal to what response stats tracked for this call.
func assertBoundedReply(t *testing.T, srv *Server, r *mcp.CallToolResult, trackedBefore int64) string {
	t.Helper()
	return assertBoundedToolReply(t, srv, "capy_search", r, trackedBefore)
}

// assertBoundedToolReply is assertBoundedReply for the named tool's stats.
func assertBoundedToolReply(t *testing.T, srv *Server, tool string, r *mcp.CallToolResult, trackedBefore int64) string {
	t.Helper()
	text := decodedText(t, r)
	assert.True(t, utf8.ValidString(text), "reply must be valid UTF-8")
	assert.LessOrEqual(t, len(text), searchReplyMaxBytes)
	assert.Equal(t, int64(len(text)), srv.stats.Snapshot().BytesReturned[tool]-trackedBefore,
		"tracked bytes must equal the decoded text")
	return text
}

// entryNumberRe matches the entry number prefixed to a full hit block.
var entryNumberRe = regexp.MustCompile(`^\[\d+\] `)

// knowledgeBlock renders a knowledge hit's full block.
func knowledgeBlock(r store.SearchResult, query string) string {
	return renderKnowledgeHit(r, query).block
}

// vaultBlock renders a vault hit's full block.
func vaultBlock(r vault.SearchResult) string {
	return renderVaultHit(r).block
}

func trackedSearchBytes(srv *Server) int64 {
	return srv.stats.Snapshot().BytesReturned["capy_search"]
}

func resetSearchThrottle(srv *Server) {
	srv.throttle.mu.Lock()
	srv.throttle.count = 0
	srv.throttle.mu.Unlock()
}

func TestDisplayFieldBounds(t *testing.T) {
	t.Run("interior multibyte cut stays valid and inside the cap", func(t *testing.T) {
		got := displayField(strings.Repeat("é", 300), 101)
		assert.True(t, utf8.ValidString(got))
		assert.LessOrEqual(t, len(got), 101)
		assert.True(t, strings.HasSuffix(got, omissionMarker))
	})
	t.Run("secret crossing the cut is redacted whole", func(t *testing.T) {
		value := strings.Repeat("L", 100) + "sk-ant-" + strings.Repeat("Q", 60)
		got := displayField(value, 112)
		assert.NotContains(t, got, "sk-ant")
		assert.NotContains(t, got, "QQ")
		assert.LessOrEqual(t, len(got), 112)
	})
	t.Run("line breaks and separators are escaped", func(t *testing.T) {
		assert.Equal(t, `a\nb\rc\r\nd`, displayField("a\nb\rc\r\nd", 100))
		got := displayField("x\u2028y\u2029z\u0085w\vv\ff", 100)
		assert.Equal(t, `x\u2028y\u2029z\u0085w\vv\ff`, got)
	})
	t.Run("invalid UTF-8 is normalized", func(t *testing.T) {
		assert.True(t, utf8.ValidString(displayField("bad\xffbyte", 100)))
	})
	t.Run("argument preview redacts before quoting and bounds the quote", func(t *testing.T) {
		// The secret starts inside the preview, so only redaction hides it.
		got := argPreview("ghp_" + strings.Repeat("A", 36) + strings.Repeat("k", 1000))
		assert.LessOrEqual(t, len(got), searchArgPreviewMaxBytes)
		assert.NotContains(t, got, "ghp_")
		assert.True(t, strings.HasPrefix(got, `"`))
	})
}

func TestExtractSnippetBounds(t *testing.T) {
	t.Run("interior multibyte windows stay valid and bounded", func(t *testing.T) {
		content := strings.Repeat("é", 1000) + " needle " + strings.Repeat("ü", 1000) + " needle " + strings.Repeat("ö", 1000)
		got := snippetText(content, "needle", searchPassageMaxBytes, "")
		assert.True(t, utf8.ValidString(got))
		assert.LessOrEqual(t, len(got), searchPassageMaxBytes)
		assert.Contains(t, got, "needle")
	})
	t.Run("case folding that changes byte length keeps original offsets", func(t *testing.T) {
		// U+023A is 2 bytes but lowercases to U+2C65, 3 bytes, so offsets in
		// the lowered text drift right of the original by one byte per rune.
		content := strings.Repeat("Ⱥ", 1200) + " needle " + strings.Repeat("x", 2000)
		got := snippetText(content, "needle", 600, "")
		assert.Contains(t, got, "needle")
		assert.True(t, utf8.ValidString(got))
		assert.LessOrEqual(t, len(got), 600)
	})
	t.Run("highlights that do not reproduce the content are ignored", func(t *testing.T) {
		content := strings.Repeat("a", 2000) + " needle " + strings.Repeat("b", 2000)
		stale := "\x02" + strings.Repeat("z", 10) + "\x03" + strings.Repeat("a", 3000)
		got := snippetText(content, "needle", 600, stale)
		assert.Contains(t, got, "needle")
	})
	t.Run("prefix fallback stays inside the cap", func(t *testing.T) {
		got := snippetText(strings.Repeat("é", 1000), "zzz", 101, "")
		assert.LessOrEqual(t, len(got), 101)
		assert.True(t, utf8.ValidString(got))
	})
}

// TestHitBlockUpperBound renders hits whose every field is oversized: each
// numbered block with its separator stays within searchHitMaxBytes, and ten
// distinct full hits plus a maximal frame and the notice reserve fit 33,792
// bytes.
func TestHitBlockUpperBound(t *testing.T) {
	huge := strings.Repeat("ű", 5000)
	// No reply holds more entries than this, so no entry number is longer.
	number := fmt.Sprintf("[%d] ", searchReplyMaxBytes)
	k := knowledgeBlock(store.SearchResult{
		Label:   "session:" + huge,
		Title:   huge,
		Content: strings.Repeat("needle ", 2000),
	}, "needle")
	assert.LessOrEqual(t, len(hitSeparator)+len(number)+len(k), searchHitMaxBytes)
	assert.Contains(t, k, "(knowledge)", "a session:-looking knowledge label keeps explicit attribution")

	v := vaultBlock(vault.SearchResult{
		SessionUUID:   huge,
		SubagentID:    "sk-ant-" + strings.Repeat("Q", 40) + huge,
		Title:         huge,
		Project:       huge,
		EndTime:       time.Unix(0, 0),
		LineIndex:     math.MaxInt,
		ChunkIndex:    math.MaxInt,
		HasChunkIndex: true,
		Platform:      vault.Platform(huge),
		ParentUUID:    huge,
		Snippet:       huge,
	})
	assert.Contains(t, v, "(ID redacted and shortened; chunk ")
	assert.LessOrEqual(t, len(hitSeparator)+len(number)+len(v), searchHitMaxBytes)
	assert.LessOrEqual(t, len(hitSeparator)+len(referenceBlock(searchReplyMaxBytes)), searchHitMaxBytes)
	assert.True(t, utf8.ValidString(k) && utf8.ValidString(v))

	frame := queryFrame(math.MaxInt, huge, []string{"knowledge: " + huge, "vault: " + huge}, 1)
	assert.LessOrEqual(t, len(groupSeparator)+len(frame), searchFrameMaxBytes)
	frame = queryFrame(math.MaxInt, huge, []string{"knowledge: " + huge, "vault: " + huge}, 0)
	assert.LessOrEqual(t, len(groupSeparator)+len(frame), searchFrameMaxBytes)

	assert.LessOrEqual(t, 10*searchHitMaxBytes+searchFrameMaxBytes+searchNoticeReserve, 33792)
}

func TestHitFieldsStayOnOneLine(t *testing.T) {
	k := knowledgeBlock(store.SearchResult{
		Label:   "label\nwith break",
		Title:   "title\u2028with separator",
		Content: "body",
	}, "body")
	assert.Equal(t, "--- [label\\nwith break] ---\n### title\\u2028with separator\n\nbody", k)

	v := vaultBlock(vault.SearchResult{
		SessionUUID: "uuid",
		Title:       "title\nnext",
		Project:     "proj\rect",
		Snippet:     "line one\nline two",
	})
	assert.Equal(t, "--- [session:uuid] ---\n### title\\nnext\nproj\\rect · chunk from line 0 · claude-code\n\nline one\nline two", v,
		"metadata is escaped while the passage keeps its line breaks")
}

// TestSearchNoticeReserveWorstCase combines every request-wide notice that can
// appear together at its largest and checks it fits the reserve.
func TestSearchNoticeReserveWorstCase(t *testing.T) {
	srv := newTestServer(t, nil)
	st := srv.getStore()
	_, err := st.Index("ephemeral body", "eph", "plaintext", store.KindEphemeral)
	require.NoError(t, err)

	hints := srv.searchHints(searchHintInput{
		st:                 st,
		sessionInScope:     true,
		vaultEnabled:       true,
		runVaultPass:       true,
		vaultStats:         &vault.VaultStats{OutdatedSessions: math.MaxInt, IndexVersion: math.MaxInt},
		cleanKnowledgeMiss: true,
	})
	require.Len(t, hints, 2, "miss guidance and backlog hint")
	budget := searchBudget{
		count: math.MaxInt, limit: 2, remaining: time.Duration(math.MaxInt64),
		policy: config.SearchConfig{WindowMS: math.MaxInt, TaperAfter: math.MinInt, BlockAfter: math.MaxInt},
	}
	notices := []string{
		"⚠ vault availability check failed: " + displayField(strings.Repeat("x", 5000), searchCauseMaxBytes),
		partialNotice(math.MaxInt-1, math.MaxInt, math.MaxInt, math.MaxInt, math.MaxInt),
	}
	notices = append(notices, hints...)
	notices = append(notices, budget.notice())
	// Each notice is joined to its neighbour (or to the query groups) once.
	total := 0
	for _, n := range notices {
		total += len(n) + len(noticeSeparator)
	}
	assert.LessOrEqual(t, total, searchNoticeReserve, strings.Join(notices, "\n"))
}

func TestSearchReply_SanitizedBoundedFields(t *testing.T) {
	srv := newTestServer(t, nil)
	st := srv.getStore()

	// An oversized label carrying a secret across its 512-byte cut. Labels are
	// not redacted at index time.
	labelSecret := "sk-ant-" + strings.Repeat("Q", 40)
	label := strings.Repeat("L", 505) + labelSecret
	r := callIndex(t, srv, map[string]any{"content": "# Label Doc\n\nlabelneedle body text", "source": label})
	require.False(t, r.IsError, resultText(r))

	// A chunk body with a secret crossing the passage window. IndexChunked does
	// not redact chunk bodies, so the display path must.
	passageSecret := "sk-ant-" + strings.Repeat("P", 60)
	body := strings.Repeat("y", 1000) + " passageneedle " + strings.Repeat("z", 270) + passageSecret + strings.Repeat("w", 2000)
	_, err := st.IndexChunked(body, "secret-chunk", "plaintext", store.KindDurable,
		[]store.Chunk{{Title: "Secret Chunk", Content: body}})
	require.NoError(t, err)

	// Multibyte content whose windows end inside runes.
	multi := strings.Repeat("é", 1000) + " runeneedle " + strings.Repeat("ü", 1000)
	r = callIndex(t, srv, map[string]any{"content": multi, "source": "multibyte"})
	require.False(t, r.IsError, resultText(r))

	before := trackedSearchBytes(srv)
	res := callSearch(t, srv, map[string]any{
		"queries":       []any{"labelneedle", "passageneedle", "runeneedle"},
		"include_kinds": []any{"durable"},
	})
	require.False(t, res.IsError)
	text := assertBoundedReply(t, srv, res, before)
	assert.NotContains(t, text, "sk-ant-")
	assert.NotContains(t, text, "QQQQ")
	assert.NotContains(t, text, "PPPP")
	// Each query must have produced its hit, so the checks above are not
	// satisfied by an empty reply.
	assert.Contains(t, text, "--- ["+strings.Repeat("L", 505)+"[RED", "label hit with its secret redacted before clipping")
	assert.Contains(t, text, "--- [secret-chunk] ---")
	assert.Contains(t, text, "--- [multibyte] ---")
	assert.Contains(t, text, "z[REDACTED", "the passage window shows the redaction, not a fragment")
	assert.Contains(t, text, "## 1. labelneedle")
	assert.Contains(t, text, "## 2. passageneedle")
	assert.Contains(t, text, "passageneedle")
	assert.Contains(t, text, "## 3. runeneedle")
	assert.Contains(t, text, "runeneedle")
	headers := 0
	for _, line := range strings.Split(text, "\n") {
		line = entryNumberRe.ReplaceAllString(line, "")
		if strings.HasPrefix(line, "--- [") {
			headers++
			assert.LessOrEqual(t, len(line), knowledgeLabelMaxBytes+len("--- [] (knowledge) ---"))
		}
	}
	assert.Equal(t, 3, headers, "every hit header is checked")
}

func TestSearchReply_LongInvalidKind(t *testing.T) {
	srv := newTestServer(t, nil)
	// The secret starts inside the preview, so only redaction hides it.
	kind := "ghp_" + strings.Repeat("A", 36) + strings.Repeat("bogus", 20000)
	res := callSearch(t, srv, map[string]any{"query": "marker", "include_kinds": []any{kind}})
	assert.True(t, res.IsError)
	text := decodedText(t, res)
	assert.True(t, utf8.ValidString(text))
	assert.Less(t, len(text), 512)
	assert.NotContains(t, text, "ghp_")
	assert.Contains(t, text, "invalid include_kinds value")
	assert.Empty(t, srv.stats.Snapshot().Calls, "validation errors stay untracked")
}

func TestSearchReply_LongBatchOfMisses(t *testing.T) {
	srv := newTestServer(t, nil)
	seedDurableDBGuide(t, srv)
	_, err := srv.getStore().Index("ephemeral body", "eph-src", "plaintext", store.KindEphemeral)
	require.NoError(t, err)

	var dispatched []int
	srv.searchDispatched = func(ordinal int, _ string) { dispatched = append(dispatched, ordinal) }
	queries := make([]any, 3000)
	for i := range queries {
		queries[i] = fmt.Sprintf("unmatched%05d %s", i, strings.Repeat("q", 400))
	}
	before := trackedSearchBytes(srv)
	res := callSearch(t, srv, map[string]any{"queries": queries})
	require.False(t, res.IsError)
	text := assertBoundedReply(t, srv, res, before)

	require.NotEmpty(t, dispatched)
	next := len(dispatched) + 1
	assert.Less(t, next, len(queries), "the budget stops the batch")
	assert.Contains(t, text, fmt.Sprintf("Query %d was not searched; %d following queries were not searched.", next, len(queries)-next))
	assert.Contains(t, text, fmt.Sprintf("retry queries %d–%d (inclusive)", next, len(queries)))
	assert.NotContains(t, text, fmt.Sprintf("## %d.", next), "an unsearched query has no group")
	assert.Equal(t, len(dispatched), strings.Count(text, "No results found."))
	assert.Equal(t, 1, strings.Count(text, "For queries without hits"), "miss guidance appears once")
	assert.NotContains(t, text, "Refine your query terms", "no whole-batch miss claim for an unsearched suffix")
	assert.Equal(t, 1, strings.Count(text, "Shared MCP server search budget:"))
}

// TestSearchReply_RetryAcrossThrottle cuts call 3 after the first of two hits
// in a boundary query, then retries from that query on tapered call 4 and after
// the window resets.
func TestSearchReply_RetryAcrossThrottle(t *testing.T) {
	srv := newTestServer(t, nil)
	now := time.Unix(100, 0)
	srv.throttle.windowStart = now
	srv.throttle.now = func() time.Time { return now }

	// Two large hits for "boundaryterm". The forced reply leaves
	// searchFrameMaxBytes+searchHitMaxBytes (4,096) bytes for groups; each block
	// here (500-byte label, 240-byte title, 1,500-byte passage) exceeds 2,048
	// bytes, so after two small frames exactly one of the two fits.
	labels := map[string]string{
		"a": "rankdoc-a-" + strings.Repeat("a", 490),
		"b": "rankdoc-b-" + strings.Repeat("b", 490),
	}
	for _, id := range []string{"a", "b"} {
		title := "Boundary " + id + " " + strings.Repeat("t", 229)
		content := strings.Repeat("boundaryterm filler words here ", 120)
		_, err := srv.getStore().IndexChunked(content, labels[id], "plaintext", store.KindDurable,
			[]store.Chunk{{Title: title, Content: content}})
		require.NoError(t, err)
	}
	opts := map[string]any{"include_kinds": []any{"durable"}, "limit": 2}
	withQueries := func(qs ...any) map[string]any {
		args := map[string]any{"queries": qs}
		for k, v := range opts {
			args[k] = v
		}
		return args
	}

	var dispatched []string
	srv.searchDispatched = func(_ int, q string) { dispatched = append(dispatched, q) }

	// Calls 1 and 2 consume untapered admissions.
	for range 2 {
		callSearch(t, srv, withQueries("unrelatedprobe"))
	}
	dispatched = nil

	// Call 3: a reply forced to one frame and one hit beyond the notice reserve.
	req := mcp.CallToolRequest{}
	req.Params.Arguments = withQueries("zzzunmatchedzzz", "boundaryterm", "suffixalpha", "suffixbeta")
	before := trackedSearchBytes(srv)
	res, err := srv.search(t.Context(), req, searchNoticeReserve+searchFrameMaxBytes+searchHitMaxBytes)
	require.NoError(t, err)
	require.False(t, res.IsError)
	third := assertBoundedReply(t, srv, res, before)
	assert.Contains(t, third, "search call #3/8")
	assert.Contains(t, third, "Results limited to 2/query")
	assert.Contains(t, third, "Query 2 showed 1 of 2 hit(s) returned at limit 2/query; 2 following queries were not searched.")
	assert.Contains(t, third, "retry queries 2–4 (inclusive)")
	assert.Contains(t, third, "If your next call's effective limit is below 2, retry after the reset time below to see the omitted ranks.")
	assert.Equal(t, []string{"zzzunmatchedzzz", "boundaryterm"}, dispatched, "the original suffix never ran")
	assert.Equal(t, 1, strings.Count(third, "--- [rankdoc-"))
	first := "a"
	if strings.Contains(third, labels["b"]) {
		first = "b"
	}
	second := map[string]string{"a": "b", "b": "a"}[first]

	// Call 4 (tapered): retry from query 2, inclusive.
	dispatched = nil
	res = callSearch(t, srv, withQueries("boundaryterm", "suffixalpha", "suffixbeta"))
	fourth := resultText(res)
	assert.Contains(t, fourth, "search call #4/8")
	assert.Contains(t, fourth, "Results limited to 1/query")
	assert.Contains(t, fourth, "## 1. boundaryterm")
	assert.Contains(t, fourth, labels[first], "rank 1 is shown again")
	assert.NotContains(t, fourth, labels[second], "the tapered limit drops rank 2")
	assert.NotContains(t, fourth, "Reply budget reached")
	assert.Equal(t, []string{"boundaryterm", "suffixalpha", "suffixbeta"}, dispatched)

	// After the reported reset: an untapered window shows both ranks in order.
	now = now.Add(time.Duration(srv.throttle.policy.WindowMS) * time.Millisecond)
	res = callSearch(t, srv, withQueries("boundaryterm", "suffixalpha", "suffixbeta"))
	reset := resultText(res)
	assert.Contains(t, reset, "search call #1/8")
	assert.Contains(t, reset, "Results limited to 2/query")
	i, j := strings.Index(reset, labels[first]), strings.Index(reset, labels[second])
	require.NotEqual(t, -1, i)
	require.NotEqual(t, -1, j)
	assert.Less(t, i, j, "ranks keep their order")
	assert.NotContains(t, reset, "Reply budget reached")
}

func TestSearchReply_Guidance(t *testing.T) {
	t.Run("empty store", func(t *testing.T) {
		srv := newTestServer(t, nil)
		res := callSearch(t, srv, map[string]any{"query": "anything"})
		assert.True(t, res.IsError)
		text := resultText(res)
		assert.Contains(t, text, "knowledge base is empty")
		assert.Contains(t, text, "capy_index")
		assert.Contains(t, text, "capy_runs")
		assert.NotContains(t, text, "batch_execute")
	})
	t.Run("blocked", func(t *testing.T) {
		srv := newTestServer(t, nil)
		srv.throttle.policy = config.SearchConfig{WindowMS: 60000, TaperAfter: 0, BlockAfter: 0}
		res := callSearch(t, srv, map[string]any{"query": "anything"})
		assert.True(t, res.IsError)
		text := resultText(res)
		assert.Contains(t, text, "BLOCKED")
		assert.Contains(t, text, "reset time")
		assert.Contains(t, text, "resets in")
		assert.NotContains(t, text, "batch_execute")
	})
	t.Run("many misses share one hint with both selectors", func(t *testing.T) {
		srv := newTestServer(t, nil)
		seedDurableDBGuide(t, srv)
		_, err := srv.getStore().Index("ephemeral body", "eph-src", "plaintext", store.KindEphemeral)
		require.NoError(t, err)
		res := callSearch(t, srv, map[string]any{"queries": []any{"nomatchone", "nomatchtwo", "nomatchthree", "database"}})
		text := resultText(res)
		assert.Equal(t, 3, strings.Count(text, "No results found."))
		assert.Equal(t, 1, strings.Count(text, "For queries without hits"))
		assert.Contains(t, text, `include_kinds: ["durable","ephemeral"]`)
		assert.Contains(t, text, `source: "<label>"`)
		assert.Contains(t, text, "capy_runs")
		assert.Contains(t, text, "1 ephemeral knowledge source(s)")
		assert.NotContains(t, text, "Refine your query terms", "a hit means the batch did not wholly miss")
		assert.Contains(t, text, "Batch related questions in one capy_search queries array.")
	})
	t.Run("whole-batch miss and opt-outs", func(t *testing.T) {
		srv := newTestServer(t, nil)
		seedDurableDBGuide(t, srv)
		_, err := srv.getStore().Index("ephemeral body", "eph-src", "plaintext", store.KindEphemeral)
		require.NoError(t, err)
		text := resultText(callSearch(t, srv, map[string]any{"queries": []any{"nomatchone", "nomatchtwo"}}))
		assert.Equal(t, 1, strings.Count(text, "Refine your query terms"))
		for _, args := range []map[string]any{
			{"queries": []any{"nomatchone"}, "source": "db-guide"},
			{"queries": []any{"nomatchone"}, "include_kinds": []any{"durable", "ephemeral"}},
		} {
			resetSearchThrottle(srv)
			text := resultText(callSearch(t, srv, args))
			assert.Contains(t, text, "capy_runs")
			assert.NotContains(t, text, "excluded by default", "explicit source and ephemeral scope opt out")
		}
	})
	t.Run("tool schema", func(t *testing.T) {
		tool := toolSearch()
		assert.Contains(t, tool.Description, "capy_runs")
		assert.Contains(t, tool.Description, "inclusive")
		assert.Contains(t, tool.Description, "number query groups from 1")
	})
}

func TestFormatKnowledgeHit_ShowsLiteralMarkerBytes(t *testing.T) {
	got := knowledgeBlock(store.SearchResult{
		Label:       "doc",
		Title:       "T",
		Content:     "head [REDACTED_SECRET]\x02SUFFIX end",
		Highlighted: "head [REDACTED_SECRET]\x02SUFFIX \x02end\x03",
	}, "end")
	assert.NotContains(t, got, "\x02")
	assert.Contains(t, got, "[REDACTED_SECRET]\uFFFDSUFFIX")
}

func TestFormatKnowledgeHit_LiteralMarkerQueryFindsItsExcerpt(t *testing.T) {
	// The only match lies beyond the passage cap, so the window must come
	// from the query term, normalized like the displayed content.
	content := strings.Repeat("x", 3000) + " alpha\x02beta " + strings.Repeat("y", 3000)
	got := knowledgeBlock(store.SearchResult{Label: "doc", Title: "T", Content: content}, "alpha\x02beta")
	assert.Contains(t, got, "alpha\uFFFDbeta")
}

func TestVaultBlock_RedactsParentBeforeShortening(t *testing.T) {
	secret := "sk-ant-" + strings.Repeat("Q", 40)
	got := vaultBlock(vault.SearchResult{
		SessionUUID: "child-session",
		Platform:    vault.PlatformCodex,
		ParentUUID:  secret,
		Snippet:     "body",
	})
	assert.NotContains(t, got, "sk-ant-")
	assert.Contains(t, got, "child of [REDACTED_SECRET]")

	ordinary := vaultBlock(vault.SearchResult{
		SessionUUID: "child-session",
		Platform:    vault.PlatformCodex,
		ParentUUID:  "019a1b2c-3d4e-7f00-8000-000000000000",
		Snippet:     "body",
	})
	assert.Contains(t, ordinary, "child of 019a1b2c-3d4")
}
