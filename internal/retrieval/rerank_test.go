package retrieval

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- Proximity reranking ---

func TestRerankSingleTermNoProximity(t *testing.T) {
	results := []SearchResult{
		{FusedScore: 0.5, Content: "hello world"},
		{FusedScore: 1.0, Content: "world hello"},
	}
	reranked := rerank(results, "hello")
	// Single term, no titles — no title boost, no proximity boost.
	// Scores unchanged, sorted by fused score descending.
	assert.Equal(t, 1.0, reranked[0].FusedScore)
	assert.Equal(t, 0.5, reranked[1].FusedScore)
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

	shortResults := rerank([]SearchResult{short}, "JWT token")
	longResults := rerank([]SearchResult{long}, "JWT token")

	// Both should be boosted above their original score.
	assert.Greater(t, shortResults[0].FusedScore, 1.0, "short content should be boosted")
	assert.Greater(t, longResults[0].FusedScore, 1.0, "long content should be boosted")

	// Longer content with same absolute span gets bigger boost (lower ratio).
	assert.Greater(t, longResults[0].FusedScore, shortResults[0].FusedScore,
		"same span in longer content should get bigger normalized boost")
}

// --- Phrase-frequency reranking ---

func TestCountAdjacentPairs(t *testing.T) {
	// Build parallel position lists the same way rerank does: lowercase the
	// content, then findAllPositions per raw term.
	pairs := func(content string, terms ...string) int {
		lower := strings.ToLower(content)
		lists := make([][]int, len(terms))
		for i, term := range terms {
			lists[i] = findAllPositions(lower, term)
		}
		return countAdjacentPairs(lists, terms, phraseGapChars)
	}

	tests := []struct {
		name    string
		content string
		terms   []string
		want    int
	}{
		{"terms absent", "the quick brown fox", []string{"foo", "bar"}, 0},
		{"single adjacent pair", "foo bar baz", []string{"foo", "bar"}, 1},
		{"greedy no double count", "foo foo bar", []string{"foo", "bar"}, 1},
		{"three distinct pairs", "foo bar foo bar foo bar", []string{"foo", "bar"}, 3},
		{"five pairs not capped", "foo bar foo bar foo bar foo bar foo bar", []string{"foo", "bar"}, 5},
		{"beyond gap window", "foo" + strings.Repeat(" ", 40) + "bar", []string{"foo", "bar"}, 0},
		{"three-term chain", "alpha beta gamma", []string{"alpha", "beta", "gamma"}, 2},
		{"wrong order not counted", "bar foo", []string{"foo", "bar"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, pairs(tt.content, tt.terms...))
		})
	}
}

func TestPhraseBoostSaturatesAtFourPairs(t *testing.T) {
	// Same content length and same minSpan, differing only in adjacent-pair
	// count (4 vs 6). Since the phrase boost saturates at 4 pairs, the two
	// must receive identical final scores.
	const targetLen = 200
	base4 := "foo bar foo bar foo bar foo bar"                 // 4 pairs
	base6 := "foo bar foo bar foo bar foo bar foo bar foo bar" // 6 pairs
	contentA := base4 + strings.Repeat("z", targetLen-len(base4))
	contentB := base6 + strings.Repeat("z", targetLen-len(base6))

	resA := rerank([]SearchResult{{FusedScore: 1.0, Content: contentA}}, "foo bar")
	resB := rerank([]SearchResult{{FusedScore: 1.0, Content: contentB}}, "foo bar")

	// InDelta, not Equal: the two paths compute identical float expressions
	// today (same content length, saturated pair count), but a delta tolerates
	// future precision drift in the boost math without flaking.
	assert.InDelta(t, resA[0].FusedScore, resB[0].FusedScore, 1e-9,
		"4 and 6 pairs should saturate to the same phrase boost")
	assert.Greater(t, resA[0].FusedScore, 1.0, "phrase boost should raise the score")
}

func TestPhraseBoostOutranksLongSingleOccurrence(t *testing.T) {
	// Short doc with 4 adjacent "foo bar" pairs vs a long doc with one
	// occurrence at the same absolute minSpan (4). Content-length
	// normalization alone would favor the long doc (TestProximity
	// ContentLengthNormalization), but the phrase-frequency boost must flip
	// it so the repetition-dense short doc ranks first.
	short := SearchResult{FusedScore: 1.0, Content: "foo bar foo bar foo bar foo bar"}
	long := SearchResult{FusedScore: 1.0, Content: "foo bar " + strings.Repeat("z", 1000)}

	results := rerank([]SearchResult{long, short}, "foo bar")

	assert.Equal(t, short.Content, results[0].Content,
		"short doc with 4 phrase hits should outrank long single-occurrence doc")
	assert.Greater(t, results[0].FusedScore, results[1].FusedScore)
}

// --- Synonym-aware proximity ---

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
	reranked := rerank(results, "k8s config")
	assert.Greater(t, reranked[0].FusedScore, 1.0,
		"content fallback should find synonyms and apply proximity boost")
}

func TestProximityRerankMixedTerms(t *testing.T) {
	// One synonym term ("k8s") and one non-synonym term ("search").
	results := []SearchResult{
		{FusedScore: 1.0, Content: "kubernetes search is fast and reliable"},
	}

	reranked := rerank(results, "k8s search")
	assert.Greater(t, reranked[0].FusedScore, 1.0,
		"mixed synonym/non-synonym query should still get proximity boost")
}

func TestProximityRerankNoSynonymPassthrough(t *testing.T) {
	// Query with no synonym terms — behaviour identical to before.
	results := []SearchResult{
		{FusedScore: 1.0, Content: "hello world greeting"},
		{FusedScore: 0.5, Content: "hello there world is great"},
	}

	reranked := rerank(results, "hello world")
	// "hello world" adjacent in first result → bigger boost.
	assert.Greater(t, reranked[0].FusedScore, reranked[1].FusedScore,
		"non-synonym query should still rank by proximity")
}

// --- Title-match boost ---

func TestTitleMatchBoostSingleTerm(t *testing.T) {
	results := []SearchResult{
		{FusedScore: 1.0, Title: "Lines 1-20", Content: "The ContentStore handles indexing.", ContentType: "code"},
		{FusedScore: 1.0, Title: "ContentStore", Content: "The ContentStore handles indexing.", ContentType: "code"},
	}
	reranked := rerank(results, "ContentStore")
	assert.Greater(t, reranked[0].FusedScore, 1.0,
		"title-matching result should be boosted")
	assert.Equal(t, "ContentStore", reranked[0].Title,
		"result with matching title should rank first")
}

func TestTitleMatchBoostMultiTerm(t *testing.T) {
	results := []SearchResult{
		{FusedScore: 1.0, Title: "Utilities", Content: "content store operations here", ContentType: "code"},
		{FusedScore: 1.0, Title: "Content Store", Content: "content store operations here", ContentType: "code"},
	}
	reranked := rerank(results, "content store")
	assert.Equal(t, "Content Store", reranked[0].Title,
		"result with title matching both terms should rank first")
}

func TestTitleMatchBoostProseWeightLowerThanCode(t *testing.T) {
	results := []SearchResult{
		{FusedScore: 1.0, Title: "ContentStore", Content: "The ContentStore handles indexing.", ContentType: "prose"},
		{FusedScore: 1.0, Title: "ContentStore", Content: "The ContentStore handles indexing.", ContentType: "code"},
	}
	reranked := rerank(results, "ContentStore")
	var proseScore, codeScore float64
	for _, r := range reranked {
		if r.ContentType == "prose" {
			proseScore = r.FusedScore
		} else {
			codeScore = r.FusedScore
		}
	}
	assert.Greater(t, codeScore, proseScore,
		"code chunks should get higher title-match weight than prose")
}
