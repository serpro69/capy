package store

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func indexTestContent(t *testing.T, s *ContentStore) {
	t.Helper()
	// Index a document about authentication middleware.
	_, err := s.Index(
		"# Authentication Middleware\n\nThe authentication_handler validates JWT tokens.\n\n"+
			"## Token Verification\n\nTokens are verified using RS256 algorithm.\n\n"+
			"## Error Handling\n\nInvalid tokens return 401 Unauthorized.\n\n"+
			"## Rate Limiting\n\nRate limiting prevents brute force attacks.\n\n"+
			"## Session Management\n\nSessions expire after 30 minutes of inactivity.",
		"auth-middleware",
		"markdown",
	)
	require.NoError(t, err)

	// Index a second document about database queries.
	_, err = s.Index(
		"# Database Query Optimization\n\nOptimize SQL queries for performance.\n\n"+
			"## Indexing Strategy\n\nCreate indexes on frequently queried columns.\n\n"+
			"## Query Planning\n\nUse EXPLAIN to analyze query execution plans.",
		"db-optimization",
		"markdown",
	)
	require.NoError(t, err)
}

// --- Query sanitization ---

func TestSanitizeQuery(t *testing.T) {
	tests := []struct {
		query, mode, want string
	}{
		{"hello world", "AND", `"hello" "world"`},
		{"hello world", "OR", `"hello" OR "world"`},
		{`test "quoted" [brackets]`, "AND", `"test" "quoted" "brackets"`},
		{"AND OR NOT NEAR", "AND", ""},
		{"", "AND", ""},
		{"single", "AND", `"single"`},
	}
	for _, tt := range tests {
		got := sanitizeQuery(tt.query, tt.mode)
		assert.Equal(t, tt.want, got, "sanitizeQuery(%q, %q)", tt.query, tt.mode)
	}
}

func TestSanitizeTrigramQuery(t *testing.T) {
	tests := []struct {
		query, mode, want string
	}{
		{"authentication", "AND", `"authentication"`},
		{"ab", "AND", ""},                                 // too short
		{"hello world", "OR", `"hello" OR "world"`},
		{"hi lo", "AND", ""},                              // all words < 3 chars
	}
	for _, tt := range tests {
		got := sanitizeTrigramQuery(tt.query, tt.mode)
		assert.Equal(t, tt.want, got, "sanitizeTrigramQuery(%q, %q)", tt.query, tt.mode)
	}
}

// --- Levenshtein ---

func TestLevenshteinDistance(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "", 3},
		{"", "abc", 3},
		{"kitten", "sitting", 3},
		{"saturday", "sunday", 3},
		{"HELLO", "hello", 0}, // case insensitive
		{"abc", "abc", 0},
		{"abc", "abd", 1},
	}
	for _, tt := range tests {
		got := levenshteinDistance(tt.a, tt.b)
		assert.Equal(t, tt.want, got, "levenshtein(%q, %q)", tt.a, tt.b)
	}
}

func TestMaxEditDistance(t *testing.T) {
	assert.Equal(t, 1, maxEditDistance(3))
	assert.Equal(t, 1, maxEditDistance(4))
	assert.Equal(t, 2, maxEditDistance(5))
	assert.Equal(t, 2, maxEditDistance(12))
	assert.Equal(t, 3, maxEditDistance(13))
}

// --- Porter search ---

func TestSearchPorterStemming(t *testing.T) {
	s := newTestStore(t)
	indexTestContent(t, s)

	// "authenticating" should match "authentication" via Porter stemming.
	results, err := s.SearchWithFallback("authenticating", 5, SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, results)
	// RRF: porter will find it; trigram may or may not. Accept either.
	assert.True(t,
		results[0].MatchLayer == "porter+OR" || results[0].MatchLayer == "rrf(porter+trigram)",
		"expected porter+OR or rrf(porter+trigram), got: %s", results[0].MatchLayer)
}

// --- Trigram search ---

func TestSearchTrigramPartialMatch(t *testing.T) {
	s := newTestStore(t)
	indexTestContent(t, s)

	// "authent" is a substring — trigram should catch it.
	results, err := s.SearchWithFallback("authent", 5, SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, results)
	assert.True(t,
		strings.Contains(results[0].MatchLayer, "trigram") ||
			strings.Contains(results[0].MatchLayer, "porter") ||
			results[0].MatchLayer == "rrf(porter+trigram)",
		"should match via trigram, porter, or rrf, got: %s", results[0].MatchLayer)
}

// --- Fuzzy correction ---

func TestSearchFuzzyCorrection(t *testing.T) {
	s := newTestStore(t)
	indexTestContent(t, s)

	// "authentcation" is a typo for "authentication".
	// Request limit=1 so direct RRF pass returns < limit (no direct hits for typo),
	// triggering the fuzzy correction pass.
	results, err := s.SearchWithFallback("authentcation", 1, SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, results, "fuzzy correction should find results for typo")
	assert.True(t,
		strings.HasPrefix(results[0].MatchLayer, "fuzzy+"),
		"should match via fuzzy layer, got: %s", results[0].MatchLayer)
}

// --- RRF ---

func TestSearchRRF(t *testing.T) {
	s := newTestStore(t)
	indexTestContent(t, s)

	// Exact word — both porter and trigram should find it, giving rrf(porter+trigram).
	r1, err := s.SearchWithFallback("authentication", 5, SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, r1)
	// "authentication" is long enough for trigram and matches porter stemming,
	// so RRF should fuse results from both layers.
	assert.True(t,
		r1[0].MatchLayer == "rrf(porter+trigram)" || r1[0].MatchLayer == "porter+OR",
		"expected rrf or porter match, got: %s", r1[0].MatchLayer)
	assert.Greater(t, r1[0].FusedScore, 0.0, "FusedScore should be set")

	// Non-existent word — should return empty.
	r2, err := s.SearchWithFallback("xyznonexistent", 5, SearchOptions{})
	require.NoError(t, err)
	assert.Empty(t, r2)
}

func TestRRFMultiLayerHitsRankHigher(t *testing.T) {
	s := newTestStore(t)

	// Index content where "authentication" appears (matchable by both porter and trigram).
	_, err := s.Index(
		"# Authentication\n\nThe authentication module validates users.\n\n"+
			"## Zeta Module\n\nThe zeta module does something else entirely with no auth.",
		"rrf-test",
		"markdown",
	)
	require.NoError(t, err)

	results, err := s.SearchWithFallback("authentication", 5, SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, results)

	// Results appearing in both layers should have higher fused scores.
	for _, r := range results {
		if r.MatchLayer == "rrf(porter+trigram)" {
			singleLayerMax := 1.0 / float64(rrfK)
			assert.Greater(t, r.FusedScore, singleLayerMax,
				"multi-layer result should score above single-layer max")
		}
	}
}

func TestFuzzyOnlyTriggersWhenResultsSparse(t *testing.T) {
	s := newTestStore(t)
	indexTestContent(t, s)

	// "authentication" has plenty of direct hits — fuzzy should NOT trigger.
	results, err := s.SearchWithFallback("authentication", 1, SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, results)
	for _, r := range results {
		assert.False(t, strings.HasPrefix(r.MatchLayer, "fuzzy+"),
			"direct RRF results should not be tagged fuzzy, got: %s", r.MatchLayer)
	}
}

func TestFuzzyResultsDontDuplicateDirectResults(t *testing.T) {
	s := newTestStore(t)
	indexTestContent(t, s)

	// Use a typo with a very high limit so fuzzy pass triggers and merges.
	results, err := s.SearchWithFallback("authentcation", 20, SearchOptions{})
	require.NoError(t, err)

	// Check no duplicates by (sourceID, title).
	seen := make(map[string]bool)
	for _, r := range results {
		key := fmt.Sprintf("%d:%s", r.SourceID, r.Title)
		assert.False(t, seen[key], "duplicate result: %s", key)
		seen[key] = true
	}
}

// --- Source filtering ---

func TestSearchSourceFiltering(t *testing.T) {
	s := newTestStore(t)
	indexTestContent(t, s)

	// Search with source filter.
	results, err := s.SearchWithFallback("optimization", 5, SearchOptions{Source: "db-optimization"})
	require.NoError(t, err)
	require.NotEmpty(t, results)
	for _, r := range results {
		assert.Contains(t, r.Label, "db-optimization")
	}

	// Same query, wrong source filter — should not match.
	results2, err := s.SearchWithFallback("optimization", 5, SearchOptions{Source: "auth-middleware"})
	require.NoError(t, err)
	assert.Empty(t, results2)
}

// --- Access tracking ---

func TestSearchAccessTracking(t *testing.T) {
	s := newTestStore(t)
	indexTestContent(t, s)

	// Search to trigger access tracking.
	results, err := s.SearchWithFallback("authentication", 5, SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, results)

	// Give the async goroutine time to complete.
	time.Sleep(100 * time.Millisecond)

	db, _ := s.getDB()
	var accessCount int
	err = db.QueryRow("SELECT access_count FROM sources WHERE label = 'auth-middleware'").Scan(&accessCount)
	require.NoError(t, err)
	assert.Greater(t, accessCount, 0, "access_count should be incremented after search hit")
}

// --- Empty query ---

func TestSearchEmptyQuery(t *testing.T) {
	s := newTestStore(t)
	indexTestContent(t, s)

	results, err := s.SearchWithFallback("", 5, SearchOptions{})
	require.NoError(t, err)
	assert.Empty(t, results)
}

// --- Distinctive terms ---

func TestGetDistinctiveTerms(t *testing.T) {
	s := newTestStore(t)

	// Index a document with 10 chunks where "authentication_handler" appears
	// in 3 of them (within the 2..40% doc-frequency window for 10 chunks).
	sections := []string{
		"## Auth Setup\n\nThe authentication_handler initializes connections.",
		"## Auth Validation\n\nThe authentication_handler validates tokens.",
		"## Auth Logging\n\nThe authentication_handler logs access events.",
		"## Routing\n\nThe router dispatches requests to controllers.",
		"## Database\n\nThe database connection pool manages resources.",
		"## Caching\n\nThe cache layer stores frequently accessed data.",
		"## Logging\n\nThe logger writes structured output to stderr.",
		"## Monitoring\n\nThe monitoring system collects metrics.",
		"## Testing\n\nThe test suite verifies integration correctness.",
		"## Deployment\n\nThe deployment pipeline runs automated checks.",
	}
	content := "# System Architecture\n\n" + strings.Join(sections, "\n\n")
	r, err := s.Index(content, "terms-test", "markdown")
	require.NoError(t, err)
	require.GreaterOrEqual(t, r.TotalChunks, 3, "need at least 3 chunks for distinctive terms")

	terms, err := s.GetDistinctiveTerms(r.SourceID, 10)
	require.NoError(t, err)
	assert.NotEmpty(t, terms, "should return distinctive terms for source with %d chunks", r.TotalChunks)

	// "authentication_handler" appears in 3/10 chunks and has underscore bonus.
	found := false
	for _, term := range terms {
		if strings.Contains(term, "authentication_handler") {
			found = true
		}
	}
	assert.True(t, found, "distinctive terms should include authentication_handler, got: %v", terms)
}

func TestGetDistinctiveTermsTooFewChunks(t *testing.T) {
	s := newTestStore(t)

	// Index small content — will produce < 3 chunks.
	r, err := s.Index("short content", "small", "plaintext")
	require.NoError(t, err)

	terms, err := s.GetDistinctiveTerms(r.SourceID, 10)
	require.NoError(t, err)
	assert.Empty(t, terms)
}

// --- Direct queries ---

func TestListSources(t *testing.T) {
	s := newTestStore(t)
	indexTestContent(t, s)

	sources, err := s.ListSources()
	require.NoError(t, err)
	assert.Len(t, sources, 2)

	// Timestamps should be parsed (non-zero).
	for _, src := range sources {
		assert.False(t, src.IndexedAt.IsZero(), "IndexedAt should be parsed for source %q", src.Label)
		assert.False(t, src.LastAccessedAt.IsZero(), "LastAccessedAt should be parsed for source %q", src.Label)
	}
}

func TestGetSourceMeta_Unknown(t *testing.T) {
	s := newTestStore(t)
	meta, err := s.GetSourceMeta("nonexistent")
	require.NoError(t, err)
	assert.Nil(t, meta)
}

func TestGetSourceMeta_AfterIndexing(t *testing.T) {
	s := newTestStore(t)
	indexTestContent(t, s)

	meta, err := s.GetSourceMeta("auth-middleware")
	require.NoError(t, err)
	require.NotNil(t, meta)
	assert.Equal(t, "auth-middleware", meta.Label)
	assert.Greater(t, meta.ChunkCount, 0)
	assert.False(t, meta.IndexedAt.IsZero())
	assert.WithinDuration(t, time.Now(), meta.IndexedAt, 5*time.Second)
}

func TestGetChunksBySource(t *testing.T) {
	s := newTestStore(t)

	r, err := s.Index("# Title\n\nContent A\n\n## Sub\n\nContent B", "chunks-test", "markdown")
	require.NoError(t, err)

	chunks, err := s.GetChunksBySource(r.SourceID)
	require.NoError(t, err)
	assert.Equal(t, r.TotalChunks, len(chunks))
	for _, c := range chunks {
		assert.Equal(t, "chunks-test", c.Label)
	}
}

// --- Proximity reranking ---

func TestProximityRerankMultiTerm(t *testing.T) {
	s := newTestStore(t)

	// Index two chunks: one where "JWT" and "token" are adjacent,
	// another where they're far apart.
	_, err := s.Index(
		"# Close Terms\n\nThe JWT token verification is fast.\n\n"+
			"# Far Terms\n\nThe JWT standard defines many things. "+
			strings.Repeat("Filler text here. ", 20)+
			"A token is issued after login.",
		"proximity-test",
		"markdown",
	)
	require.NoError(t, err)

	results, err := s.SearchWithFallback("JWT token", 5, SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, results)

	// The chunk with close terms should rank first after proximity boosting.
	assert.Contains(t, results[0].Content, "JWT token",
		"close-proximity chunk should rank first")
}

func TestProximityRerankSingleTermSkips(t *testing.T) {
	results := []SearchResult{
		{FusedScore: 0.5, Content: "hello world"},
		{FusedScore: 1.0, Content: "world hello"},
	}
	reranked := proximityRerank(results, "hello")
	// Single term — no reranking, original order preserved.
	assert.Equal(t, 0.5, reranked[0].FusedScore)
	assert.Equal(t, 1.0, reranked[1].FusedScore)
}

func TestFindAllPositions(t *testing.T) {
	assert.Equal(t, []int{0, 6}, findAllPositions("hello hello world", "hello"))
	assert.Equal(t, []int{12}, findAllPositions("hello hello world", "world"))
	assert.Empty(t, findAllPositions("hello world", "missing"))
	assert.Equal(t, []int{0, 1, 2}, findAllPositions("aaa", "a"))
}

func TestFindMinSpanFromHighlights(t *testing.T) {
	// char(2) = start marker, char(3) = end marker (FTS5 highlight convention).
	mk := func(s string) string { return string(rune(2)) + s + string(rune(3)) }

	// Two terms adjacent: "the \x02JWT\x03 \x02token\x03 is valid"
	highlighted := "the " + mk("JWT") + " " + mk("token") + " is valid"
	span := findMinSpanFromHighlights(highlighted, []string{"jwt", "token"})
	// "JWT" starts at stripped position 4, "token" at 8. Span = 4.
	assert.Equal(t, 4, span)

	// Term not found in highlights — should return -1.
	span2 := findMinSpanFromHighlights(highlighted, []string{"jwt", "missing"})
	assert.Equal(t, -1, span2)

	// Empty highlighted string.
	span3 := findMinSpanFromHighlights("", []string{"jwt"})
	assert.Equal(t, -1, span3)

	// Single term — span should be 0 (same position to same position).
	highlighted4 := "before " + mk("auth") + " after"
	span4 := findMinSpanFromHighlights(highlighted4, []string{"auth"})
	assert.Equal(t, 0, span4)
}

func TestFindMinSpan(t *testing.T) {
	// Two lists: positions of "a" and "b" in "a...b".
	span := findMinSpan([][]int{{0, 10}, {5, 15}})
	assert.Equal(t, 5, span, "min span should be |5-0| = 5")

	// Adjacent.
	span2 := findMinSpan([][]int{{0}, {1}})
	assert.Equal(t, 1, span2)

	// Single list.
	span3 := findMinSpan([][]int{{5}})
	assert.Equal(t, 0, span3)
}

func TestProximityContentLengthNormalization(t *testing.T) {
	// Two results with the same absolute minSpan but different content lengths.
	// The formula boost = 1/(1 + minSpan/contentLen) means:
	// - Same span in longer content → smaller ratio → bigger boost
	// This is intentional: a 4-char span in 1000 chars means terms are
	// practically adjacent; a 4-char span in 14 chars is a significant
	// fraction of the content.
	short := SearchResult{FusedScore: 1.0, Content: "JWT token here"}
	long := SearchResult{FusedScore: 1.0, Content: "JWT token here " + strings.Repeat("x", 1000)}

	shortResults := proximityRerank([]SearchResult{short}, "JWT token")
	longResults := proximityRerank([]SearchResult{long}, "JWT token")

	// Both should be boosted above their original score.
	assert.Greater(t, shortResults[0].FusedScore, 1.0, "short content should be boosted")
	assert.Greater(t, longResults[0].FusedScore, 1.0, "long content should be boosted")

	// Longer content with same absolute span gets bigger boost (lower ratio).
	assert.Greater(t, longResults[0].FusedScore, shortResults[0].FusedScore,
		"same span in longer content should get bigger normalized boost")
}

// --- ContentType filtering ---

func TestSearchContentTypeFilter(t *testing.T) {
	s := newTestStore(t)

	// Index content with code blocks (produces both code and prose chunks).
	_, err := s.Index(
		"# API Guide\n\nThe API provides endpoints for auth.\n\n"+
			"```go\nfunc Authenticate(token string) error {\n\treturn nil\n}\n```\n\n"+
			"## Usage\n\nCall Authenticate with a valid token.",
		"code-filter-test",
		"markdown",
	)
	require.NoError(t, err)

	// Search with code filter.
	codeResults, err := s.SearchWithFallback("authenticate", 5, SearchOptions{ContentType: "code"})
	require.NoError(t, err)
	for _, r := range codeResults {
		assert.Equal(t, "code", r.ContentType,
			"code filter should only return code chunks, got: %s for %q", r.ContentType, r.Title)
	}

	// Search without filter should return all.
	allResults, err := s.SearchWithFallback("authenticate", 10, SearchOptions{})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(allResults), len(codeResults),
		"unfiltered search should return at least as many results as filtered")
}

// --- Synonym expansion ---

func TestSynonymExpansionPorter(t *testing.T) {
	s := newTestStore(t)

	// Index content mentioning "database performance" — search "db perf" should match.
	_, err := s.Index(
		"# Database Performance\n\nOptimize database performance with proper indexing strategies.\n\n"+
			"## Bottlenecks\n\nIdentify database latency bottlenecks in production.",
		"db-perf-doc",
		"markdown",
	)
	require.NoError(t, err)

	results, err := s.SearchWithFallback("db perf", 5, SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, results, "synonym expansion should find 'database performance' when searching 'db perf'")
	assert.Contains(t, strings.ToLower(results[0].Content), "database",
		"result should contain 'database' (synonym of 'db')")
}

func TestSynonymExpansionTrigram(t *testing.T) {
	s := newTestStore(t)

	// "kubernetes" has trigram-matchable length; searching "k8s" should find it via synonym expansion.
	_, err := s.Index(
		"# Kubernetes Setup\n\nDeploy your kubernetes cluster with proper configuration.\n\n"+
			"## Scaling\n\nKubernetes horizontal pod autoscaler manages workload scaling.",
		"k8s-doc",
		"markdown",
	)
	require.NoError(t, err)

	results, err := s.SearchWithFallback("kubernetes", 5, SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, results, "direct search for 'kubernetes' should find results")

	// Now search with abbreviation.
	results2, err := s.SearchWithFallback("k8s", 5, SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, results2, "synonym expansion should find 'kubernetes' when searching 'k8s'")
}

func TestSynonymFallbackToOR(t *testing.T) {
	s := newTestStore(t)

	// Index content with only "authentication" — search "auth deploy" where only
	// "auth" has matches. The AND grouping will fail; fallback to OR should succeed.
	_, err := s.Index(
		"# Authentication Guide\n\nThe authentication module handles user identity verification.\n\n"+
			"## Tokens\n\nJWT tokens are used for stateless authentication.",
		"auth-only",
		"markdown",
	)
	require.NoError(t, err)

	results, err := s.SearchWithFallback("auth deploy", 5, SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, results,
		"fallback to OR should return results when only one synonym group matches")
}

func TestSynonymSkipsFuzzy(t *testing.T) {
	s := newTestStore(t)
	indexTestContent(t, s)

	// "perf" is a synonym-known term. fuzzyCorrectWord should return "" for it
	// (no correction), even if a similar vocabulary word exists.
	fix := s.fuzzyCorrectWord("perf")
	assert.Empty(t, fix, "synonym-known term 'perf' should not be fuzzy-corrected")

	fix2 := s.fuzzyCorrectWord("auth")
	assert.Empty(t, fix2, "synonym-known term 'auth' should not be fuzzy-corrected")
}

func TestNoSynonymPassthrough(t *testing.T) {
	s := newTestStore(t)

	// Index content with a unique term that has no synonyms.
	_, err := s.Index(
		"# Widget Architecture\n\nThe widget subsystem handles rendering.\n\n"+
			"## Lifecycle\n\nWidgets follow a mount-update-unmount lifecycle.",
		"widget-doc",
		"markdown",
	)
	require.NoError(t, err)

	results, err := s.SearchWithFallback("widget", 5, SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, results, "terms without synonyms should still match normally")
	assert.Contains(t, strings.ToLower(results[0].Content), "widget")
}

// --- Synonym sanitizer unit tests ---

func TestSanitizeQueryWithSynonyms(t *testing.T) {
	// Term with synonyms should produce OR group.
	result := sanitizeQueryWithSynonyms("db")
	assert.Contains(t, result, `"db"`)
	assert.Contains(t, result, `"database"`)
	assert.Contains(t, result, `"datastore"`)
	assert.Contains(t, result, " OR ")
	assert.Contains(t, result, "(")

	// Term without synonyms should be quoted without grouping.
	result2 := sanitizeQueryWithSynonyms("widget")
	assert.Equal(t, `"widget"`, result2)

	// Multi-term: space between groups = implicit AND.
	result3 := sanitizeQueryWithSynonyms("db perf")
	// Should have two parenthesized groups separated by space.
	assert.Contains(t, result3, `("db"`)
	assert.Contains(t, result3, `("perf"`)

	// Empty/keyword-only queries return empty.
	assert.Equal(t, "", sanitizeQueryWithSynonyms(""))
	assert.Equal(t, "", sanitizeQueryWithSynonyms("AND OR"))
}

func TestSanitizeTrigramWithSynonyms(t *testing.T) {
	// "db" is only 2 chars — dropped from trigram. But its synonyms >= 3 chars remain.
	result := sanitizeTrigramWithSynonyms("db")
	assert.NotContains(t, result, `"db"`, "2-char term should be dropped from trigram")
	assert.Contains(t, result, `"database"`)
	assert.Contains(t, result, `"datastore"`)

	// Term with all synonyms >= 3 chars.
	result2 := sanitizeTrigramWithSynonyms("perf")
	assert.Contains(t, result2, `"perf"`)
	assert.Contains(t, result2, `"performance"`)

	// Short input returns empty.
	assert.Equal(t, "", sanitizeTrigramWithSynonyms("ab"))
}
