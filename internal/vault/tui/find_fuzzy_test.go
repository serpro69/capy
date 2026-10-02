package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fuzzyLine(t *testing.T, text, query string) []findFuzzyHit {
	t.Helper()
	c, err := buildFindCorpus(t.Context(), "test", []vault.TranscriptMessage{{Body: text}})
	require.NoError(t, err)
	hits, err := findFuzzy(t.Context(), c, query)
	require.NoError(t, err)
	return hits
}

func assertFuzzySpans(t *testing.T, text, query string, hit findFuzzyHit) {
	t.Helper()
	runes := []rune(query)
	require.Len(t, hit.spans, len(runes))
	previous := 0
	for i, span := range hit.spans {
		require.GreaterOrEqual(t, span.start, previous)
		require.Less(t, span.start, span.end)
		require.LessOrEqual(t, span.end, len(text))
		r, size := utf8.DecodeRuneInString(text[span.start:])
		assert.Equal(t, span.start+size, span.end)
		assert.True(t, strings.EqualFold(string(r), string(runes[i])), "span %d: %q versus %q", i, r, runes[i])
		previous = span.end
	}
	assert.GreaterOrEqual(t, hit.score, int64(-8448))
	assert.LessOrEqual(t, hit.score, int64(9232))
}

func TestFindFuzzy(t *testing.T) {
	for _, tc := range []struct{ name, text, query string }{
		{"nul-before", "\x00abc", "abc"},
		{"nul-after", "abc\x00", "abc"},
		{"nul-between", "a\x00b\x00c", "abc"},
		{"nul-query", "a\x00bc", "a\x00c"},
		{"fold-cycle", "ſKςΣσkS", "SKσσσks"},
		{"invalid-byte", "a\xffb", "a�b"},
		{"leftmost", "a----b--ab", "ab"},
		{"spaces-literal", "a x b", "a b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hits := fuzzyLine(t, tc.text, tc.query)
			require.Len(t, hits, 1)
			assertFuzzySpans(t, tc.text, tc.query, hits[0])
			if tc.name == "leftmost" {
				assert.Equal(t, []findHit{{0, 0, 1}, {0, 5, 6}}, hits[0].spans)
			}
		})
	}
	t.Run("every supported length including score overflow boundaries", func(t *testing.T) {
		for length := 1; length <= 256; length++ {
			for _, alphabet := range []string{"a", "abCd", "界ΣKé", "\x00a界"} {
				runes := []rune(strings.Repeat(alphabet, length))[:length]
				query := string(runes)
				t.Run(fmt.Sprintf("%d/%q", length, alphabet), func(t *testing.T) {
					hits := fuzzyLine(t, query, query)
					require.Len(t, hits, 1)
					assertFuzzySpans(t, query, query, hits[0])
					offset := 0
					for _, span := range hits[0].spans {
						assert.Equal(t, offset, span.start, "identical input must cover every byte")
						offset = span.end
					}
					assert.Equal(t, len(query), offset)
					text := "\x00" + strings.Join(strings.Split(query, ""), "-") + "\x00"
					hits = fuzzyLine(t, text, query)
					require.Len(t, hits, 1)
					assertFuzzySpans(t, text, query, hits[0])
				})
			}
		}
	})
	t.Run("bounded scores and documented ranking", func(t *testing.T) {
		hits := fuzzyLine(t, "abc\na_b_c\na long b long c\nabc", "abc")
		require.Len(t, hits, 4)
		assert.Equal(t, []int{0, 3, 1, 2}, []int{hits[0].line, hits[1].line, hits[2].line, hits[3].line})
		assert.Equal(t, []int64{88, 88, 86, 76}, []int64{hits[0].score, hits[1].score, hits[2].score, hits[3].score})
		text := strings.Repeat("x", 20000) + "a" + strings.Repeat("x", 20000) + "b" + strings.Repeat("x", 20000)
		hits = fuzzyLine(t, text, "ab")
		require.Len(t, hits, 1)
		assertFuzzySpans(t, text, "ab", hits[0])
		assert.Equal(t, int64(32-256-4096-4096), hits[0].score)
		assert.Empty(t, fuzzyLine(t, "é", "é"), "no canonical normalization")
		assert.Empty(t, fuzzyLine(t, "ß", "ss"), "no multi-rune expansion")
		assert.Empty(t, fuzzyLine(t, strings.Repeat("a", 300), strings.Repeat("a", 257)))
	})
}

// Independent existence oracle: dynamic programming over rune-prefix pairs,
// using the standard library's EqualFold rather than the matcher's fold helper.
func fuzzyOracle(text, query string) bool {
	q := []rune(query)
	dp := make([]bool, len(q)+1)
	dp[0] = true
	for _, r := range text {
		for i := len(q); i > 0; i-- {
			dp[i] = dp[i] || dp[i-1] && strings.EqualFold(string(r), string(q[i-1]))
		}
	}
	return dp[len(q)]
}

func TestFindFuzzyOracle(t *testing.T) {
	generate := func(maxLength int) []string {
		all, previous := []string{""}, []string{""}
		for range maxLength {
			var next []string
			for _, prefix := range previous {
				for _, r := range "aA\x00界" {
					next = append(next, prefix+string(r))
				}
			}
			all = append(all, next...)
			previous = next
		}
		return all
	}
	for _, text := range generate(4) {
		for _, query := range generate(3)[1:] {
			hits := fuzzyLine(t, text, query)
			require.Equal(t, fuzzyOracle(text, query), len(hits) == 1, "text=%q query=%q", text, query)
			if len(hits) > 0 {
				assertFuzzySpans(t, text, query, hits[0])
			}
		}
	}
}

func TestFindFuzzyCancellation(t *testing.T) {
	for _, text := range []string{strings.Repeat("x", 1<<20), strings.Repeat("界", 1<<18)} {
		ctx := &findCheckpointContext{Context: context.Background(), remaining: 4}
		_, _, err := matchFindFuzzy(ctx, text, []rune{'Z'})
		require.True(t, errors.Is(err, context.Canceled))
	}
}
