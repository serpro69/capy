package store

import (
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
	results, err := s.SearchWithFallback("authenticating", 5, "")
	require.NoError(t, err)
	require.NotEmpty(t, results)
	assert.Equal(t, "porter+AND", results[0].MatchLayer)
}

// --- Trigram search ---

func TestSearchTrigramPartialMatch(t *testing.T) {
	s := newTestStore(t)
	indexTestContent(t, s)

	// "authent" is a substring — trigram should catch it.
	// First try porter (likely no match for partial), then trigram.
	results, err := s.SearchWithFallback("authent", 5, "")
	require.NoError(t, err)
	require.NotEmpty(t, results)
	assert.True(t,
		strings.HasPrefix(results[0].MatchLayer, "trigram") ||
			strings.HasPrefix(results[0].MatchLayer, "porter"),
		"should match via trigram or porter, got: %s", results[0].MatchLayer)
}

// --- Fuzzy correction ---

func TestSearchFuzzyCorrection(t *testing.T) {
	s := newTestStore(t)
	indexTestContent(t, s)

	// "authentcation" is a typo for "authentication".
	results, err := s.SearchWithFallback("authentcation", 5, "")
	require.NoError(t, err)
	require.NotEmpty(t, results, "fuzzy correction should find results for typo")
	assert.True(t,
		strings.HasPrefix(results[0].MatchLayer, "fuzzy"),
		"should match via fuzzy layer, got: %s", results[0].MatchLayer)
}

// --- 8-layer fallback ---

func TestSearchFallbackLayers(t *testing.T) {
	s := newTestStore(t)
	indexTestContent(t, s)

	// Exact word — should hit porter+AND (layer 1).
	r1, err := s.SearchWithFallback("authentication", 5, "")
	require.NoError(t, err)
	require.NotEmpty(t, r1)
	assert.Equal(t, "porter+AND", r1[0].MatchLayer)

	// Non-existent word — should return nil.
	r2, err := s.SearchWithFallback("xyznonexistent", 5, "")
	require.NoError(t, err)
	assert.Empty(t, r2)
}

// --- Source filtering ---

func TestSearchSourceFiltering(t *testing.T) {
	s := newTestStore(t)
	indexTestContent(t, s)

	// Search with source filter.
	results, err := s.SearchWithFallback("optimization", 5, "db-optimization")
	require.NoError(t, err)
	require.NotEmpty(t, results)
	for _, r := range results {
		assert.Contains(t, r.Label, "db-optimization")
	}

	// Same query, wrong source filter — should not match.
	results2, err := s.SearchWithFallback("optimization", 5, "auth-middleware")
	require.NoError(t, err)
	assert.Empty(t, results2)
}

// --- Access tracking ---

func TestSearchAccessTracking(t *testing.T) {
	s := newTestStore(t)
	indexTestContent(t, s)

	// Search to trigger access tracking.
	results, err := s.SearchWithFallback("authentication", 5, "")
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

	results, err := s.SearchWithFallback("", 5, "")
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
