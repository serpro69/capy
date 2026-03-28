package server

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPositionsFromHighlight(t *testing.T) {
	// "hello \x02world\x03 foo" — "world" starts at clean offset 6
	highlighted := "hello \x02world\x03 foo"
	positions := positionsFromHighlight(highlighted)
	assert.Equal(t, []int{6}, positions)
}

func TestPositionsFromHighlight_Multiple(t *testing.T) {
	// "aa \x02bb\x03 cc \x02dd\x03"
	highlighted := "aa \x02bb\x03 cc \x02dd\x03"
	positions := positionsFromHighlight(highlighted)
	// "aa bb cc dd" — "bb" at 3, "dd" at 9
	assert.Equal(t, []int{3, 9}, positions)
}

func TestPositionsFromHighlight_Empty(t *testing.T) {
	assert.Empty(t, positionsFromHighlight("no markers here"))
}

func TestExtractSnippet_ShortContent(t *testing.T) {
	content := "short content"
	result := ExtractSnippet(content, "anything", 100, "")
	assert.Equal(t, content, result)
}

func TestExtractSnippet_WithHighlightMarkers(t *testing.T) {
	content := strings.Repeat("a", 500) + "MATCH" + strings.Repeat("b", 500)
	highlighted := strings.Repeat("a", 500) + "\x02MATCH\x03" + strings.Repeat("b", 500)

	result := ExtractSnippet(content, "match", 600, highlighted)
	assert.Contains(t, result, "MATCH")
	assert.True(t, len(result) <= 610) // maxLen + ellipsis overhead
}

func TestExtractSnippet_FallbackToIndexOf(t *testing.T) {
	content := strings.Repeat("x", 500) + "findme" + strings.Repeat("y", 500)
	result := ExtractSnippet(content, "findme", 600, "")
	assert.Contains(t, result, "findme")
}

func TestExtractSnippet_NoMatchesReturnsPrefix(t *testing.T) {
	content := strings.Repeat("a", 1000)
	result := ExtractSnippet(content, "zzz", 100, "")
	assert.True(t, strings.HasPrefix(result, strings.Repeat("a", 100)))
	assert.True(t, strings.HasSuffix(result, "…"))
}

func TestExtractSnippet_OverlappingWindowsMerge(t *testing.T) {
	// Two matches close together should merge into one window
	content := strings.Repeat("x", 100) + "MATCH1" + strings.Repeat("y", 50) + "MATCH2" + strings.Repeat("z", 1000)
	result := ExtractSnippet(content, "MATCH1 MATCH2", 600, "")
	// Both matches should appear in a single merged window
	assert.Contains(t, result, "MATCH1")
	assert.Contains(t, result, "MATCH2")
}

func TestStripMarkers(t *testing.T) {
	highlighted := "hello \x02world\x03 \x02foo\x03"
	assert.Equal(t, "hello world foo", stripMarkers(highlighted))
}

func TestSplitQueryTerms(t *testing.T) {
	terms := splitQueryTerms("the big FOO bar")
	assert.Equal(t, []string{"the", "big", "foo", "bar"}, terms)
}

func TestSplitQueryTerms_FiltersShort(t *testing.T) {
	terms := splitQueryTerms("a is foo")
	assert.Equal(t, []string{"foo"}, terms)
}
