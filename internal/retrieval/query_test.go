package retrieval

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- Query sanitization ---

func TestSanitizePorterQuery(t *testing.T) {
	tests := []struct {
		query, mode, want string
	}{
		{"hello world", "AND", `"hello" "world"`},
		{"hello world", "OR", `"hello" OR "world"`},
		{`test "quoted" [brackets]`, "AND", `"quoted" "brackets"`},
		{"AND OR NOT NEAR", "AND", ""},
		{"", "AND", ""},
		{"single", "AND", `"single"`},
	}
	for _, tt := range tests {
		got := SanitizePorterQuery(tt.query, tt.mode, false)
		assert.Equal(t, tt.want, got, "SanitizePorterQuery(%q, %q, false)", tt.query, tt.mode)
	}
}

func TestSanitizeTrigramQueryNoSynonyms(t *testing.T) {
	tests := []struct {
		query, mode, want string
	}{
		{"authentication", "AND", `"authentication"`},
		{"ab", "AND", ""}, // too short
		{"hello world", "OR", `"hello" OR "world"`},
		{"hi lo", "AND", ""}, // all words < 3 chars
	}
	for _, tt := range tests {
		got := SanitizeTrigramQuery(tt.query, tt.mode, false)
		assert.Equal(t, tt.want, got, "SanitizeTrigramQuery(%q, %q, false)", tt.query, tt.mode)
	}
}

// --- filterQueryTerms ---

func TestFilterQueryTerms(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{
			name:  "removes stopwords",
			query: "the error in the code",
			want:  []string{"error", "in"},
		},
		{
			name:  "deduplicates case-insensitively",
			query: "Error error ERROR",
			want:  []string{"error"},
		},
		{
			name:  "all-stopword query falls back to unfiltered",
			query: "the and for",
			want:  []string{"the", "and", "for"},
		},
		{
			name:  "strips FTS5 special chars",
			query: `"error:" [test] (hello)`,
			want:  []string{"error", "hello"},
		},
		{
			name:  "empty query returns nil",
			query: "",
			want:  nil,
		},
		{
			name:  "mixed stopwords and real terms",
			query: "the authentication for deployment",
			want:  []string{"authentication", "deployment"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterQueryTerms(tt.query)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSanitizePorterQueryStopwordFiltering(t *testing.T) {
	// "the error in the code" → "the" and "code" are stopwords, "in" is not
	result := SanitizePorterQuery("the error in the code", "AND", false)
	assert.Equal(t, `"error" "in"`, result)

	// Duplicate-cased terms are deduplicated
	result2 := SanitizePorterQuery("Error error ERROR", "AND", false)
	assert.Equal(t, `"error"`, result2)
}

func TestSanitizePorterQueryDotSeparatedTerms(t *testing.T) {
	// "k8s.io" — filterQueryTerms preserves the dot (ftsSpecialRe doesn't strip it).
	// Porter FTS5 tokenizer handles the dot at query time, but verify our sanitizer
	// produces a valid quoted literal so it reaches the tokenizer intact.
	result := SanitizePorterQuery("k8s.io", "AND", false)
	assert.Equal(t, `"k8s.io"`, result)

	// "config.yaml" — both parts are valid terms joined by dot
	result2 := SanitizePorterQuery("config.yaml", "AND", false)
	assert.Equal(t, `"config.yaml"`, result2)
}

func TestSanitizeTrigramQueryStopwordFiltering(t *testing.T) {
	// "the error in the code" → stopwords removed, "in" < 3 chars dropped by trigram
	result := SanitizeTrigramQuery("the error in the code", "AND", false)
	assert.Equal(t, `"error"`, result)
}

func TestFilterQueryTermsPunctuationTrimming(t *testing.T) {
	// Trailing punctuation is trimmed before stopword check
	got := filterQueryTerms("the, error. in! code?")
	assert.Equal(t, []string{"error", "in"}, got)

	// Punctuation-only tokens are dropped
	got2 := filterQueryTerms("hello ... world")
	assert.Equal(t, []string{"hello", "world"}, got2)
}

func TestSanitizeTrigramQueryPunctuatedTerms(t *testing.T) {
	// "k8s.io" should split into "k8s" and not concatenate to "k8sio"
	result := SanitizeTrigramQuery("k8s.io", "AND", false)
	assert.Equal(t, `"k8s"`, result, "dot-separated terms should split, not concatenate")

	// "config.yaml" should produce two trigram-valid terms
	result2 := SanitizeTrigramQuery("config.yaml", "AND", false)
	assert.Contains(t, result2, `"config"`)
	assert.Contains(t, result2, `"yaml"`)
}

// --- Synonym sanitizer unit tests ---

func TestSanitizePorterQueryWithSynonyms(t *testing.T) {
	// Term with synonyms should produce OR group.
	result := SanitizePorterQuery("db", "AND", true)
	assert.Contains(t, result, `"db"`)
	assert.Contains(t, result, `"database"`)
	assert.Contains(t, result, `"datastore"`)
	assert.Contains(t, result, " OR ")
	assert.Contains(t, result, "(")

	// Term without synonyms should be quoted without grouping.
	result2 := SanitizePorterQuery("widget", "AND", true)
	assert.Equal(t, `"widget"`, result2)

	// Multi-term AND: space between groups = implicit AND.
	result3 := SanitizePorterQuery("db perf", "AND", true)
	assert.Contains(t, result3, `("db"`)
	assert.Contains(t, result3, `("perf"`)
	// Groups should NOT be joined by OR in AND mode.
	// Count OR occurrences — should only appear inside groups, not between them.
	assert.NotContains(t, result3, `) OR (`, "AND mode should not join groups with OR")

	// Multi-term OR: groups joined by OR.
	result4 := SanitizePorterQuery("db perf", "OR", true)
	assert.Contains(t, result4, `) OR (`, "OR mode should join groups with OR")

	// Empty/keyword-only queries return empty.
	assert.Equal(t, "", SanitizePorterQuery("", "AND", true))
	assert.Equal(t, "", SanitizePorterQuery("AND OR", "AND", true))
}

func TestSanitizeTrigramQueryWithSynonyms(t *testing.T) {
	// "db" is only 2 chars — dropped from trigram. But its synonyms >= 3 chars remain.
	result := SanitizeTrigramQuery("db", "AND", true)
	assert.NotContains(t, result, `"db"`, "2-char term should be dropped from trigram")
	assert.Contains(t, result, `"database"`)
	assert.Contains(t, result, `"datastore"`)

	// Term with all synonyms >= 3 chars.
	result2 := SanitizeTrigramQuery("perf", "AND", true)
	assert.Contains(t, result2, `"perf"`)
	assert.Contains(t, result2, `"performance"`)

	// Short input returns empty.
	assert.Equal(t, "", SanitizeTrigramQuery("ab", "AND", true))
}
