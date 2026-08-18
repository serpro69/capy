package retrieval

import (
	"regexp"
	"strings"
)

var (
	ftsSpecialRe   = regexp.MustCompile(`['"(){}[\]*:^~]`)
	trigramCleanRe = regexp.MustCompile(`[^a-zA-Z0-9 _-]`)
	ftsKeywords    = map[string]bool{"AND": true, "OR": true, "NOT": true, "NEAR": true}
)

// filterQueryTerms strips FTS5 special chars, splits, lowercases,
// deduplicates case-insensitively, and filters stopwords. Falls back
// to the deduplicated (unfiltered) list if all terms are stopwords.
func filterQueryTerms(query string) []string {
	cleaned := ftsSpecialRe.ReplaceAllString(query, " ")
	words := strings.Fields(cleaned)
	if len(words) == 0 {
		return nil
	}

	seen := make(map[string]bool, len(words))
	deduped := make([]string, 0, len(words))
	for _, w := range words {
		lower := strings.ToLower(strings.Trim(w, ".,!?;:"))
		if lower == "" || seen[lower] {
			continue
		}
		seen[lower] = true
		deduped = append(deduped, lower)
	}

	filtered := make([]string, 0, len(deduped))
	for _, w := range deduped {
		if !IsStopword(w) {
			filtered = append(filtered, w)
		}
	}

	if len(filtered) == 0 {
		return deduped
	}
	return filtered
}

// SanitizePorterQuery cleans a query for the Porter FTS5 table. When
// expandSyns is true, each term is expanded via the synonym map into an OR
// group. Mode controls how groups are joined: "AND" uses space (implicit AND
// in FTS5), "OR" uses " OR ".
// Note: quoted phrase preservation is not yet implemented — all FTS5 special
// characters (including quotes) are stripped before tokenization.
func SanitizePorterQuery(query, mode string, expandSyns bool) string {
	terms := filterQueryTerms(query)
	var groups []string
	for _, w := range terms {
		if ftsKeywords[strings.ToUpper(w)] {
			continue
		}
		if expandSyns {
			if syns := ExpandSynonyms(w); len(syns) > 0 {
				parts := make([]string, 0, len(syns)+1)
				parts = append(parts, `"`+w+`"`)
				for _, s := range syns {
					parts = append(parts, `"`+s+`"`)
				}
				groups = append(groups, "("+strings.Join(parts, " OR ")+")")
				continue
			}
		}
		groups = append(groups, `"`+w+`"`)
	}
	if len(groups) == 0 {
		return ""
	}
	sep := " "
	if mode == "OR" {
		sep = " OR "
	}
	return strings.Join(groups, sep)
}

// SanitizeTrigramQuery cleans a query for the trigram FTS5 table (min 3 chars
// per term). When expandSyns is true, each term is expanded via the synonym
// map; short terms (<3 chars) are dropped but their longer synonyms are kept.
func SanitizeTrigramQuery(query, mode string, expandSyns bool) string {
	terms := filterQueryTerms(query)
	var groups []string
	for _, w := range terms {
		subs := strings.Fields(trigramCleanRe.ReplaceAllString(w, " "))
		for _, sub := range subs {
			if expandSyns {
				if syns := ExpandSynonyms(sub); len(syns) > 0 {
					parts := make([]string, 0, len(syns)+1)
					if len(sub) >= 3 {
						parts = append(parts, `"`+sub+`"`)
					}
					for _, s := range syns {
						sc := trigramCleanRe.ReplaceAllString(s, "")
						if len(sc) >= 3 {
							parts = append(parts, `"`+sc+`"`)
						}
					}
					if len(parts) > 0 {
						if len(parts) == 1 {
							groups = append(groups, parts[0])
						} else {
							groups = append(groups, "("+strings.Join(parts, " OR ")+")")
						}
					}
					continue
				}
			}
			if len(sub) >= 3 {
				groups = append(groups, `"`+sub+`"`)
			}
		}
	}
	if len(groups) == 0 {
		return ""
	}
	sep := " "
	if mode == "OR" {
		sep = " OR "
	}
	return strings.Join(groups, sep)
}
