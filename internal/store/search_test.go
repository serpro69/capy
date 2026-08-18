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
		KindDurable,
	)
	require.NoError(t, err)

	// Index a second document about database queries.
	_, err = s.Index(
		"# Database Query Optimization\n\nOptimize SQL queries for performance.\n\n"+
			"## Indexing Strategy\n\nCreate indexes on frequently queried columns.\n\n"+
			"## Query Planning\n\nUse EXPLAIN to analyze query execution plans.",
		"db-optimization",
		"markdown",
		KindDurable,
	)
	require.NoError(t, err)
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
		KindDurable,
	)
	require.NoError(t, err)

	results, err := s.SearchWithFallback("authentication", 5, SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, results)

	// Results appearing in both layers should have higher fused scores.
	for _, r := range results {
		if r.MatchLayer == "rrf(porter+trigram)" {
			singleLayerMax := 1.0 / 60.0 // 1/rrfK — the retrieval engine's RRF constant
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

// --- Kind filtering ---

// indexMixedKinds seeds two sources matching the same query term, one durable
// and one ephemeral. Both contain "kubernetes" so a single query can hit either.
func indexMixedKinds(t *testing.T, s *ContentStore) {
	t.Helper()
	_, err := s.Index(
		"# Kubernetes Reference\n\nkubernetes orchestrates containers across nodes.\n\n"+
			"## Pods\n\nA pod is the smallest deployable unit in kubernetes.",
		"k8s-docs",
		"markdown",
		KindDurable,
	)
	require.NoError(t, err)

	_, err = s.Index(
		"# kubectl get pods output\n\nkubernetes cluster shows running pods here.",
		"execute:shell",
		"markdown",
		KindEphemeral,
	)
	require.NoError(t, err)
}

func TestSearch_DefaultExcludesEphemeral(t *testing.T) {
	s := newTestStore(t)
	indexMixedKinds(t, s)

	results, err := s.SearchWithFallback("kubernetes pods", 10, SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, results)
	for _, r := range results {
		assert.NotEqual(t, "execute:shell", r.Label,
			"default search must not surface ephemeral sources, got: %s", r.Label)
	}
}

func TestSearch_IncludeKindsBoth(t *testing.T) {
	s := newTestStore(t)
	indexMixedKinds(t, s)

	results, err := s.SearchWithFallback("kubernetes pods", 10, SearchOptions{
		IncludeKinds: []SourceKind{KindDurable, KindEphemeral},
	})
	require.NoError(t, err)
	require.NotEmpty(t, results)

	labels := map[string]bool{}
	for _, r := range results {
		labels[r.Label] = true
	}
	assert.True(t, labels["k8s-docs"], "durable source missing from results: %v", labels)
	assert.True(t, labels["execute:shell"], "ephemeral source missing from results: %v", labels)
}

func TestSearch_IncludeKindsEphemeralOnly(t *testing.T) {
	s := newTestStore(t)
	indexMixedKinds(t, s)

	results, err := s.SearchWithFallback("kubernetes pods", 10, SearchOptions{
		IncludeKinds: []SourceKind{KindEphemeral},
	})
	require.NoError(t, err)
	require.NotEmpty(t, results)
	for _, r := range results {
		assert.Equal(t, "execute:shell", r.Label,
			"ephemeral-only filter must not surface durable sources, got: %s", r.Label)
	}
}

func TestSearch_ExplicitSourceOverridesKindFilter(t *testing.T) {
	s := newTestStore(t)
	indexMixedKinds(t, s)

	// IncludeKinds defaults to durable only, but explicit Source overrides
	// the kind filter so the ephemeral row surfaces.
	results, err := s.SearchWithFallback("kubernetes pods", 10, SearchOptions{
		Source: "execute:shell",
	})
	require.NoError(t, err)
	require.NotEmpty(t, results)
	for _, r := range results {
		assert.Equal(t, "execute:shell", r.Label,
			"explicit-source filter must scope to that source, got: %s", r.Label)
	}
}

func TestSearch_FuzzyCorrectionRespectsKindFilter(t *testing.T) {
	s := newTestStore(t)
	indexMixedKinds(t, s)

	// "kubernets" is a typo — fuzzy correction should fix it to "kubernetes".
	// Default filter (durable only) must still apply through the fuzzy re-entry.
	results, err := s.SearchWithFallback("kubernets pods", 1, SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, results, "fuzzy correction should find durable results for typo")
	for _, r := range results {
		assert.NotEqual(t, "execute:shell", r.Label,
			"fuzzy-corrected query must inherit the durable-only filter, got: %s", r.Label)
	}
}

func TestCountSourcesByKind(t *testing.T) {
	s := newTestStore(t)
	indexMixedKinds(t, s)

	durable, err := s.CountSourcesByKind(KindDurable)
	require.NoError(t, err)
	assert.Equal(t, 1, durable)

	ephemeral, err := s.CountSourcesByKind(KindEphemeral)
	require.NoError(t, err)
	assert.Equal(t, 1, ephemeral)
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

// trackAccess wraps its per-source Exec calls in a single transaction to
// batch fsyncs; this test guards that every distinct source in a result
// set still gets exactly one bump and the commit happens.
func TestSearchAccessTrackingMultipleSources(t *testing.T) {
	s := newTestStore(t)
	indexTestContent(t, s)

	// Query matches both auth-middleware and db-optimization chunks
	// (both mention "queries"/"query" and "tokens"/"rate" respectively — a
	// broad term catches both). Fall back to explicit multi-term OR.
	results, err := s.SearchWithFallback("authentication optimization", 10, SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, results)

	// Verify at least both seeded labels appear — otherwise the test
	// doesn't exercise the multi-source batch path.
	hit := map[string]bool{}
	for _, r := range results {
		hit[r.Label] = true
	}
	require.True(t, hit["auth-middleware"], "expected auth-middleware in results")
	require.True(t, hit["db-optimization"], "expected db-optimization in results")

	db, _ := s.getDB()
	for _, label := range []string{"auth-middleware", "db-optimization"} {
		var n int
		err := db.QueryRow("SELECT access_count FROM sources WHERE label = ?", label).Scan(&n)
		require.NoError(t, err, "label: %s", label)
		assert.Equal(t, 1, n, "each distinct source in the result set should be bumped exactly once")
	}
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
	r, err := s.Index(content, "terms-test", "markdown", KindDurable)
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
	r, err := s.Index("short content", "small", "plaintext", KindDurable)
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

	r, err := s.Index("# Title\n\nContent A\n\n## Sub\n\nContent B", "chunks-test", "markdown", KindDurable)
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
		KindDurable,
	)
	require.NoError(t, err)

	results, err := s.SearchWithFallback("JWT token", 5, SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, results)

	// The chunk with close terms should rank first after proximity boosting.
	assert.Contains(t, results[0].Content, "JWT token",
		"close-proximity chunk should rank first")
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
		KindDurable,
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
		KindDurable,
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
		KindDurable,
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
		KindDurable,
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
		KindDurable,
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
		KindDurable,
	)
	require.NoError(t, err)

	results, err := s.SearchWithFallback("widget", 5, SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, results, "terms without synonyms should still match normally")
	assert.Contains(t, strings.ToLower(results[0].Content), "widget")
}

func TestFuzzyCorrectedQueryGetsSynonymExpansion(t *testing.T) {
	s := newTestStore(t)

	// Index content with "authentication" and "deployment".
	_, err := s.Index(
		"# Auth Deploy Guide\n\nThe authentication service handles deployment tokens.\n\n"+
			"## Pipeline\n\nAuthentication is required before deployment can proceed.",
		"auth-deploy",
		"markdown",
		KindDurable,
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
	_, err := s.Index(content, "config-with-secret", "markdown", KindDurable)
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
		KindDurable,
	)
	require.NoError(t, err)

	// Source B: 2 chunks about deployment.
	_, err = s.Index(
		"# Deployment Checklist\n\nPre-deployment checklist for production releases.\n\n"+
			"## Deployment Verification\n\nVerify deployment with smoke tests after rollout.",
		"deploy-checklist-B",
		"markdown",
		KindDurable,
	)
	require.NoError(t, err)

	// Source C: 1 chunk about deployment.
	_, err = s.Index(
		"# Deployment FAQ\n\nCommon deployment questions and troubleshooting tips.",
		"deploy-faq-C",
		"markdown",
		KindDurable,
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
		KindDurable,
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

// --- Fuzzy correction cache ---

func TestFuzzyCacheHit(t *testing.T) {
	s := newTestStore(t)

	_, err := s.Index(
		"# Errors\n\nThe error handling module catches errors early.\n\n"+
			"## Common Errors\n\nOff-by-one errors are the most frequent bug type.",
		"cache-test",
		"markdown",
		KindDurable,
	)
	require.NoError(t, err)

	// First call — populates cache via DB.
	fix1 := s.fuzzyCorrectWord("errro")
	require.NotEmpty(t, fix1, "should correct 'errro' to a vocab word")

	// Verify cache entry exists.
	s.fuzzyCacheMu.Lock()
	cached, ok := s.fuzzyCache["errro"]
	s.fuzzyCacheMu.Unlock()
	require.True(t, ok, "cache should contain entry after first call")
	require.NotNil(t, cached)
	assert.Equal(t, fix1, *cached)

	// Second call — should return same result (from cache).
	fix2 := s.fuzzyCorrectWord("errro")
	assert.Equal(t, fix1, fix2)
}

func TestFuzzyCacheNilForNoCorrection(t *testing.T) {
	s := newTestStore(t)

	_, err := s.Index(
		"# Widgets\n\nThe widget subsystem handles rendering of widgets.",
		"cache-nil-test",
		"markdown",
		KindDurable,
	)
	require.NoError(t, err)

	// "widget" is an exact vocab match with no synonyms — no correction needed.
	fix := s.fuzzyCorrectWord("widget")
	assert.Empty(t, fix)

	s.fuzzyCacheMu.Lock()
	cached, ok := s.fuzzyCache["widget"]
	s.fuzzyCacheMu.Unlock()
	require.True(t, ok, "cache should contain entry for exact match")
	assert.Nil(t, cached, "nil value means no correction")
}

func TestFuzzyCacheClearedAfterVocabInsert(t *testing.T) {
	s := newTestStore(t)

	_, err := s.Index(
		"# Errors\n\nThe error handling module catches errors early.",
		"cache-clear-test",
		"markdown",
		KindDurable,
	)
	require.NoError(t, err)

	// Populate cache.
	s.fuzzyCorrectWord("errro")
	s.fuzzyCacheMu.Lock()
	preCacheLen := len(s.fuzzyCache)
	s.fuzzyCacheMu.Unlock()
	require.Greater(t, preCacheLen, 0)

	// Index new content — triggers extractAndStoreVocabulary which clears cache.
	_, err = s.Index(
		"# Authentication\n\nThe authentication module validates users.",
		"cache-clear-new",
		"markdown",
		KindDurable,
	)
	require.NoError(t, err)

	s.fuzzyCacheMu.Lock()
	postCacheLen := len(s.fuzzyCache)
	s.fuzzyCacheMu.Unlock()
	assert.Equal(t, 0, postCacheLen, "cache should be cleared after vocab insert")
}

func TestFuzzyCacheSizeCapEviction(t *testing.T) {
	s := newTestStore(t)

	// Manually fill the cache beyond max size.
	s.fuzzyCacheMu.Lock()
	for i := range fuzzyCacheMaxSize {
		key := fmt.Sprintf("word%d", i)
		s.fuzzyCache[key] = nil
	}
	assert.Equal(t, fuzzyCacheMaxSize, len(s.fuzzyCache))
	s.fuzzyCacheMu.Unlock()

	// Next cacheFuzzy call should evict all and start fresh.
	val := "test"
	s.cacheFuzzy("newword", &val)

	s.fuzzyCacheMu.Lock()
	assert.Equal(t, 1, len(s.fuzzyCache), "cache should have been evicted and contain only the new entry")
	cached, ok := s.fuzzyCache["newword"]
	s.fuzzyCacheMu.Unlock()
	require.True(t, ok)
	assert.Equal(t, "test", *cached)
}

// ─── Session kind search tests ────────────────────────────────────────────

func TestSearch_SessionIncludedByDefault(t *testing.T) {
	s := newTestStore(t)

	_, err := s.Index("authentication middleware validates tokens correctly", "docs-auth", "plaintext", KindDurable)
	require.NoError(t, err)
	_, err = s.Index("we decided to use JWT for authentication in the session discussion", "session:2026-04-05T12:00:00Z:abc123", "plaintext", KindSession)
	require.NoError(t, err)

	results, err := s.SearchWithFallback("authentication", 10, SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, results)

	labels := map[string]bool{}
	for _, r := range results {
		labels[r.Label] = true
	}
	assert.True(t, labels["docs-auth"], "durable source should appear")
	assert.True(t, labels["session:2026-04-05T12:00:00Z:abc123"], "session source should appear by default")
}

func TestSearch_SessionExcludedWhenNotInIncludeKinds(t *testing.T) {
	s := newTestStore(t)

	_, err := s.Index("authentication middleware validates tokens correctly", "docs-auth", "plaintext", KindDurable)
	require.NoError(t, err)
	_, err = s.Index("we decided to use JWT for authentication in the session discussion", "session:2026-04-05T12:00:00Z:abc123", "plaintext", KindSession)
	require.NoError(t, err)

	results, err := s.SearchWithFallback("authentication", 10, SearchOptions{
		IncludeKinds: []SourceKind{KindDurable},
	})
	require.NoError(t, err)
	for _, r := range results {
		assert.NotEqual(t, "session:2026-04-05T12:00:00Z:abc123", r.Label,
			"session source should be excluded when IncludeKinds is durable-only")
	}
}

func TestKindScopeIncludes(t *testing.T) {
	defaultOpts := SearchOptions{}
	assert.True(t, KindScopeIncludes(defaultOpts, KindDurable))
	assert.True(t, KindScopeIncludes(defaultOpts, KindSession))
	assert.False(t, KindScopeIncludes(defaultOpts, KindEphemeral))

	durableOnly := SearchOptions{IncludeKinds: []SourceKind{KindDurable}}
	assert.True(t, KindScopeIncludes(durableOnly, KindDurable))
	assert.False(t, KindScopeIncludes(durableOnly, KindSession))
	assert.False(t, KindScopeIncludes(durableOnly, KindEphemeral))

	withSource := SearchOptions{Source: "session:"}
	assert.True(t, KindScopeIncludes(withSource, KindSession))
	assert.True(t, KindScopeIncludes(withSource, KindEphemeral))
}
