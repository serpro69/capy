package store

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

var (
	// quotedPhraseRe extracts text inside double quotes.
	quotedPhraseRe = regexp.MustCompile(`"([^"]+)"`)

	// capitalizedIdentRe matches capitalized identifiers: starts with uppercase,
	// followed by alphanumerics, underscores, dots, or hyphens.
	capitalizedIdentRe = regexp.MustCompile(`\b[A-Z][a-zA-Z0-9_.\-]+\b`)

	// identifierSignalRe detects identifier patterns: underscores, dots, or
	// interior uppercase (camelCase/PascalCase).
	identifierSignalRe = regexp.MustCompile(`[_.]|[a-z][A-Z]|[A-Z][A-Z][a-z]`)

	// entityStopWords are common sentence starters and conjunctions excluded from
	// entity extraction.
	entityStopWords map[string]bool
)

func init() {
	words := []string{
		"The", "This", "What", "How", "When", "Where", "Why", "Who", "Which",
		"Did", "Does", "Do", "Is", "Are", "Was", "Were",
		"Has", "Have", "Had", "Can", "Could", "Would", "Should", "Will",
		"May", "Might", "If", "And", "But", "Or", "Not",
		"For", "From", "With", "About", "After", "Before", "Between",
	}
	entityStopWords = make(map[string]bool, len(words))
	for _, w := range words {
		entityStopWords[w] = true
	}
}

// ExtractEntities extracts quoted phrases and capitalized identifiers from a
// query string. Stop words and sentence starters (single capitalized word at
// position 0 without identifier patterns) are filtered out.
func ExtractEntities(query string) []string {
	seen := make(map[string]bool)
	var entities []string

	add := func(e string) {
		if len(e) < 2 || seen[e] {
			return
		}
		seen[e] = true
		entities = append(entities, e)
	}

	// Extract quoted phrases first, then remove them from query for identifier pass.
	remaining := query
	for _, match := range quotedPhraseRe.FindAllStringSubmatch(query, -1) {
		phrase := strings.TrimSpace(match[1])
		if len(phrase) >= 2 {
			add(phrase)
		}
		remaining = strings.ReplaceAll(remaining, match[0], " ")
	}

	// Extract capitalized identifiers from remaining (unquoted) text.
	identifiers := capitalizedIdentRe.FindAllString(remaining, -1)

	for i, ident := range identifiers {
		if entityStopWords[ident] {
			continue
		}

		// Sentence-starter filter: a single capitalized word at position 0
		// that lacks identifier signals (no underscores, dots, or interior
		// capitals like camelCase) is treated as a sentence starter.
		if i == 0 && isAtQueryStart(remaining, ident) && !identifierSignalRe.MatchString(ident) {
			continue
		}

		add(ident)
	}

	return entities
}

// isAtQueryStart checks whether ident appears at the start of the query
// (ignoring leading whitespace).
func isAtQueryStart(query, ident string) bool {
	trimmed := strings.TrimLeftFunc(query, unicode.IsSpace)
	return strings.HasPrefix(trimmed, ident)
}

// BoostByEntities multiplies FusedScore for results containing extracted
// entities. Single-word entities use case-sensitive word-boundary matching;
// multi-word phrases use case-insensitive matching. Match count is capped at 5
// (max 2.5× boost). Results are re-sorted by boosted score.
func BoostByEntities(results []SearchResult, entities []string) []SearchResult {
	if len(entities) == 0 || len(results) == 0 {
		return results
	}

	// Pre-compile word-boundary regexes for each entity.
	type entityMatcher struct {
		re        *regexp.Regexp
		multiWord bool
	}
	matchers := make([]entityMatcher, 0, len(entities))
	for _, e := range entities {
		multiWord := strings.Contains(e, " ")
		escaped := regexp.QuoteMeta(e)
		var pattern string
		if multiWord {
			// Case-insensitive for multi-word phrases.
			pattern = `(?i)\b` + escaped + `\b`
		} else {
			// Case-sensitive for single-word identifiers.
			pattern = `\b` + escaped + `\b`
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue
		}
		matchers = append(matchers, entityMatcher{re: re, multiWord: multiWord})
	}

	if len(matchers) == 0 {
		return results
	}

	for i := range results {
		matchCount := 0
		for _, m := range matchers {
			if matchCount >= 6 {
				break
			}
			matches := m.re.FindAllStringIndex(results[i].Content, 6-matchCount)
			matchCount += len(matches)
		}
		if matchCount > 5 {
			matchCount = 5
		}
		if matchCount > 0 {
			results[i].FusedScore *= (1.0 + 0.3*float64(matchCount))
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].FusedScore > results[j].FusedScore
	})

	return results
}
