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

func TestSanitizePorterQuery(t *testing.T) {
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
		got := sanitizePorterQuery(tt.query, tt.mode, false)
		assert.Equal(t, tt.want, got, "sanitizePorterQuery(%q, %q, false)", tt.query, tt.mode)
	}
}

func TestSanitizeTrigramQueryNoSynonyms(t *testing.T) {
	tests := []struct {
		query, mode, want string
	}{
		{"authentication", "AND", `"authentication"`},
		{"ab", "AND", ""},                                 // too short
		{"hello world", "OR", `"hello" OR "world"`},
		{"hi lo", "AND", ""},                              // all words < 3 chars
	}
	for _, tt := range tests {
		got := sanitizeTrigramQuery(tt.query, tt.mode, false)
		assert.Equal(t, tt.want, got, "sanitizeTrigramQuery(%q, %q, false)", tt.query, tt.mode)
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
		results[0].MatchLayer == "porter" || results[0].MatchLayer == "rrf(porter+trigram)",
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
		r1[0].MatchLayer == "rrf(porter+trigram)" || r1[0].MatchLayer == "porter",
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
	span := findMinSpanFromHighlights(highlighted, [][]string{{"jwt"}, {"token"}})
	// "JWT" starts at stripped position 4, "token" at 8. Span = 4.
	assert.Equal(t, 4, span)

	// Term not found in highlights — should return -1.
	span2 := findMinSpanFromHighlights(highlighted, [][]string{{"jwt"}, {"missing"}})
	assert.Equal(t, -1, span2)

	// Empty highlighted string.
	span3 := findMinSpanFromHighlights("", [][]string{{"jwt"}})
	assert.Equal(t, -1, span3)

	// Single term — span should be 0 (same position to same position).
	highlighted4 := "before " + mk("auth") + " after"
	span4 := findMinSpanFromHighlights(highlighted4, [][]string{{"auth"}})
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

// --- Synonym-aware proximity ---

func TestProximityRerankWithSynonyms(t *testing.T) {
	s := newTestStore(t)

	// Index content using full synonym forms — "kubernetes configuration".
	_, err := s.Index(
		"# Setup\n\nThe kubernetes configuration must be validated before deploy.\n\n"+
			"# Other\n\nSome unrelated content about testing and debugging.",
		"synonym-proximity-test",
		"markdown",
	)
	require.NoError(t, err)

	// Search with abbreviations — "k8s config".
	results, err := s.SearchWithFallback("k8s config", 5, SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, results)

	// The chunk with "kubernetes configuration" should get proximity boost
	// because "k8s" expands to include "kubernetes" and "config" expands
	// to include "configuration".
	assert.Contains(t, results[0].Content, "kubernetes configuration",
		"synonym-matched chunk should rank first via proximity boost")
}

func TestProximityRerankSynonymHighlights(t *testing.T) {
	mk := func(s string) string { return string(rune(2)) + s + string(rune(3)) }

	// Highlighted content uses full forms; query uses abbreviations.
	// "the \x02kubernetes\x03 \x02configuration\x03 is ready"
	highlighted := "the " + mk("kubernetes") + " " + mk("configuration") + " is ready"

	// Term groups: k8s → [k8s, kubernetes, kube], config → [config, configuration, configuring]
	termGroups := [][]string{
		{"k8s", "kubernetes", "kube"},
		{"config", "configuration", "configuring"},
	}
	span := findMinSpanFromHighlights(highlighted, termGroups)
	// "kubernetes" at stripped pos 4, "configuration" at stripped pos 15. Span = 11.
	assert.GreaterOrEqual(t, span, 0, "should find a valid span via synonym match")
	assert.Less(t, span, 50, "span should be reasonable for adjacent terms")
}

func TestProximityRerankSynonymContentFallback(t *testing.T) {
	// No highlights — force content fallback path.
	results := []SearchResult{
		{FusedScore: 1.0, Content: "the kubernetes configuration is ready"},
	}

	// Query uses abbreviations that are synonyms of content terms.
	reranked := proximityRerank(results, "k8s config")
	assert.Greater(t, reranked[0].FusedScore, 1.0,
		"content fallback should find synonyms and apply proximity boost")
}

func TestProximityRerankMixedTerms(t *testing.T) {
	// One synonym term ("k8s") and one non-synonym term ("search").
	results := []SearchResult{
		{FusedScore: 1.0, Content: "kubernetes search is fast and reliable"},
	}

	reranked := proximityRerank(results, "k8s search")
	assert.Greater(t, reranked[0].FusedScore, 1.0,
		"mixed synonym/non-synonym query should still get proximity boost")
}

func TestProximityRerankNoSynonymPassthrough(t *testing.T) {
	// Query with no synonym terms — behaviour identical to before.
	results := []SearchResult{
		{FusedScore: 1.0, Content: "hello world greeting"},
		{FusedScore: 0.5, Content: "hello there world is great"},
	}

	reranked := proximityRerank(results, "hello world")
	// "hello world" adjacent in first result → bigger boost.
	assert.Greater(t, reranked[0].FusedScore, reranked[1].FusedScore,
		"non-synonym query should still rank by proximity")
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

func TestSanitizePorterQueryWithSynonyms(t *testing.T) {
	// Term with synonyms should produce OR group.
	result := sanitizePorterQuery("db", "AND", true)
	assert.Contains(t, result, `"db"`)
	assert.Contains(t, result, `"database"`)
	assert.Contains(t, result, `"datastore"`)
	assert.Contains(t, result, " OR ")
	assert.Contains(t, result, "(")

	// Term without synonyms should be quoted without grouping.
	result2 := sanitizePorterQuery("widget", "AND", true)
	assert.Equal(t, `"widget"`, result2)

	// Multi-term AND: space between groups = implicit AND.
	result3 := sanitizePorterQuery("db perf", "AND", true)
	assert.Contains(t, result3, `("db"`)
	assert.Contains(t, result3, `("perf"`)
	// Groups should NOT be joined by OR in AND mode.
	// Count OR occurrences — should only appear inside groups, not between them.
	assert.NotContains(t, result3, `) OR (`, "AND mode should not join groups with OR")

	// Multi-term OR: groups joined by OR.
	result4 := sanitizePorterQuery("db perf", "OR", true)
	assert.Contains(t, result4, `) OR (`, "OR mode should join groups with OR")

	// Empty/keyword-only queries return empty.
	assert.Equal(t, "", sanitizePorterQuery("", "AND", true))
	assert.Equal(t, "", sanitizePorterQuery("AND OR", "AND", true))
}

func TestSanitizeTrigramQueryWithSynonyms(t *testing.T) {
	// "db" is only 2 chars — dropped from trigram. But its synonyms >= 3 chars remain.
	result := sanitizeTrigramQuery("db", "AND", true)
	assert.NotContains(t, result, `"db"`, "2-char term should be dropped from trigram")
	assert.Contains(t, result, `"database"`)
	assert.Contains(t, result, `"datastore"`)

	// Term with all synonyms >= 3 chars.
	result2 := sanitizeTrigramQuery("perf", "AND", true)
	assert.Contains(t, result2, `"perf"`)
	assert.Contains(t, result2, `"performance"`)

	// Short input returns empty.
	assert.Equal(t, "", sanitizeTrigramQuery("ab", "AND", true))
}

func TestFuzzyCorrectedQueryGetsSynonymExpansion(t *testing.T) {
	s := newTestStore(t)

	// Index content with "authentication" and "deployment".
	_, err := s.Index(
		"# Auth Deploy Guide\n\nThe authentication service handles deployment tokens.\n\n"+
			"## Pipeline\n\nAuthentication is required before deployment can proceed.",
		"auth-deploy",
		"markdown",
	)
	require.NoError(t, err)

	// Search with a typo "authentcation" — fuzzy should correct to "authentication"
	// and synonym expansion should then also match "auth", "authn", etc.
	results, err := s.SearchWithFallback("authentcation", 5, SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, results, "fuzzy correction should find results for misspelled synonym-known term")
}

func TestSecretStrippedBeforeIndexing(t *testing.T) {
	s := newTestStore(t)

	secret := "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij"
	content := fmt.Sprintf("# Config\n\nGitHub token: %s\n\nThis document describes deployment config.", secret)
	_, err := s.Index(content, "config-with-secret", "markdown")
	require.NoError(t, err)

	// Search for deployment config — should find the document.
	results, err := s.SearchWithFallback("deployment config", 5, SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, results, "should find indexed content by non-secret terms")

	// Verify the secret is absent from all returned content.
	for _, r := range results {
		assert.NotContains(t, r.Content, secret, "secret should be redacted in search results")
		assert.NotContains(t, r.Content, "ghp_", "secret prefix should not appear in results")
	}
}

// --- Per-source diversification tests ---

// indexDiversifyContent creates 3 sources: A with 5 chunks about deployment,
// B with 2 chunks, and C with 1 chunk — all matching "deployment".
func indexDiversifyContent(t *testing.T, s *ContentStore) {
	t.Helper()

	// Source A: 5 chunks — dominates search results for "deployment".
	_, err := s.Index(
		"# Deployment Guide\n\nDeployment automation is critical for reliability.\n\n"+
			"## Deployment Pipeline\n\nThe deployment pipeline runs staging then production.\n\n"+
			"## Deployment Rollback\n\nDeployment rollback requires version pinning.\n\n"+
			"## Deployment Monitoring\n\nMonitor deployment health with readiness probes.\n\n"+
			"## Deployment Security\n\nDeployment secrets must be encrypted at rest.",
		"deploy-guide-A",
		"markdown",
	)
	require.NoError(t, err)

	// Source B: 2 chunks about deployment.
	_, err = s.Index(
		"# Deployment Checklist\n\nPre-deployment checklist for production releases.\n\n"+
			"## Deployment Verification\n\nVerify deployment with smoke tests after rollout.",
		"deploy-checklist-B",
		"markdown",
	)
	require.NoError(t, err)

	// Source C: 1 chunk about deployment.
	_, err = s.Index(
		"# Deployment FAQ\n\nCommon deployment questions and troubleshooting tips.",
		"deploy-faq-C",
		"markdown",
	)
	require.NoError(t, err)
}

func TestDiversifyBySource(t *testing.T) {
	s := newTestStore(t)
	indexDiversifyContent(t, s)

	results, err := s.SearchWithFallback("deployment", 10, SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, results)

	// Count results per source label.
	counts := make(map[string]int)
	for _, r := range results {
		counts[r.Label]++
	}

	// Default maxPerSource is 2 — in the diversified top positions (before backfill),
	// source A should be capped at 2. Pass 1 selects: 2 from A, 2 from B, 1 from C = 5 items.
	assert.Equal(t, 2, countSourceInTopN(results, "deploy-guide-A", 5),
		"source A should be capped at 2 in the top 5 diversified results")

	// All three sources should appear in results.
	assert.Greater(t, counts["deploy-guide-A"], 0, "source A should appear")
	assert.Greater(t, counts["deploy-checklist-B"], 0, "source B should appear")
	assert.Greater(t, counts["deploy-faq-C"], 0, "source C should appear")
}

func TestDiversifyFillsRemaining(t *testing.T) {
	s := newTestStore(t)
	indexDiversifyContent(t, s)

	results, err := s.SearchWithFallback("deployment", 10, SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, results)

	// After pass 1 caps source A at 2, source B provides up to 2, source C up to 1.
	// Pass 2 fills remaining slots with skipped source A results.
	// So source A should appear more than 2 times total (pass 1 + pass 2 backfill).
	counts := make(map[string]int)
	for _, r := range results {
		counts[r.Label]++
	}
	assert.Greater(t, counts["deploy-guide-A"], 2,
		"source A should have >2 total results after pass 2 backfill")
}

func TestDiversifyNoReduction(t *testing.T) {
	s := newTestStore(t)
	indexDiversifyContent(t, s)

	// Request limit higher than total chunks — diversification should not reduce count.
	results, err := s.SearchWithFallback("deployment", 20, SearchOptions{})
	require.NoError(t, err)

	// Total chunks across all sources: 5 + 2 + 1 = 8.
	// All should be returned (limit 20 > total candidates).
	assert.GreaterOrEqual(t, len(results), 5,
		"diversification should not reduce total result count below available candidates")
}

func TestDiversifySingleSource(t *testing.T) {
	s := newTestStore(t)

	// Only one source.
	_, err := s.Index(
		"# Deployment Guide\n\nDeployment automation is critical.\n\n"+
			"## Deployment Pipeline\n\nThe deployment pipeline runs staging.\n\n"+
			"## Deployment Rollback\n\nDeployment rollback requires pinning.",
		"single-source",
		"markdown",
	)
	require.NoError(t, err)

	results, err := s.SearchWithFallback("deployment", 10, SearchOptions{})
	require.NoError(t, err)

	// All results come from single source — pass 2 backfills skipped results.
	assert.NotEmpty(t, results, "should return results even from a single source")
	for _, r := range results {
		assert.Equal(t, "single-source", r.Label)
	}
}

// --- diversifyBySource unit tests ---

func TestDiversifyBySourceUnit(t *testing.T) {
	// 6 results: 4 from source 1, 1 from source 2, 1 from source 3.
	results := []SearchResult{
		{SourceID: 1, Label: "A", FusedScore: 0.10},
		{SourceID: 1, Label: "A", FusedScore: 0.09},
		{SourceID: 1, Label: "A", FusedScore: 0.08},
		{SourceID: 1, Label: "A", FusedScore: 0.07},
		{SourceID: 2, Label: "B", FusedScore: 0.06},
		{SourceID: 3, Label: "C", FusedScore: 0.05},
	}

	diversified := diversifyBySource(results, 5, 2)

	// Pass 1 picks: A(0.10), A(0.09), B(0.06), C(0.05) — skips A(0.08), A(0.07).
	// Pass 2 fills: A(0.08) — total 5.
	require.Len(t, diversified, 5)
	assert.Equal(t, int64(1), diversified[0].SourceID) // A
	assert.Equal(t, int64(1), diversified[1].SourceID) // A
	assert.Equal(t, int64(2), diversified[2].SourceID) // B
	assert.Equal(t, int64(3), diversified[3].SourceID) // C
	assert.Equal(t, int64(1), diversified[4].SourceID) // A (backfill)
}

func TestDiversifyBySourceEmpty(t *testing.T) {
	result := diversifyBySource(nil, 5, 2)
	assert.Empty(t, result)
}

func TestDiversifyBySourceAllUnique(t *testing.T) {
	results := []SearchResult{
		{SourceID: 1, Label: "A", FusedScore: 0.10},
		{SourceID: 2, Label: "B", FusedScore: 0.09},
		{SourceID: 3, Label: "C", FusedScore: 0.08},
	}
	diversified := diversifyBySource(results, 5, 2)
	// No capping needed — all unique sources.
	require.Len(t, diversified, 3)
	assert.Equal(t, int64(1), diversified[0].SourceID)
	assert.Equal(t, int64(2), diversified[1].SourceID)
	assert.Equal(t, int64(3), diversified[2].SourceID)
}

// helpers for diversification assertions

// countSourceInTopN counts how many results from a given label appear in the first n positions.
func countSourceInTopN(results []SearchResult, label string, n int) int {
	count := 0
	for i := 0; i < n && i < len(results); i++ {
		if results[i].Label == label {
			count++
		}
	}
	return count
}

