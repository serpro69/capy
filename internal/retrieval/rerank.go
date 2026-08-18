package retrieval

import (
	"math"
	"sort"
	"strings"
)

// rerank applies title-match boost and proximity reranking to fused results.
// Title-match boost applies to all queries (including single-term).
// Proximity boost only applies for multi-term queries (2+ terms).
func rerank(results []SearchResult, query string) []SearchResult {
	terms := filterQueryTerms(query)
	if len(terms) == 0 {
		return results
	}

	// Title-match boost: reward results whose title contains query terms.
	for i := range results {
		r := &results[i]
		lowerTitle := strings.ToLower(r.Title)
		titleHits := 0
		for _, t := range terms {
			if strings.Contains(lowerTitle, t) {
				titleHits++
			}
		}
		if titleHits > 0 {
			weight := 0.3
			if r.ContentType == "code" {
				weight = 0.6
			}
			titleBoost := weight * (float64(titleHits) / float64(len(terms)))
			r.FusedScore *= (1.0 + titleBoost)
		}
	}

	// Proximity span boost (multi-term only).
	if len(terms) >= 2 {
		termGroups := make([][]string, len(terms))
		for i, w := range terms {
			if syns := ExpandSynonyms(w); len(syns) > 0 {
				group := make([]string, 0, len(syns)+1)
				group = append(group, w)
				group = append(group, syns...)
				termGroups[i] = group
			} else {
				termGroups[i] = []string{w}
			}
		}

		for i := range results {
			r := &results[i]
			content := strings.ToLower(r.Content)
			minSpan := -1

			if r.Highlighted != "" {
				minSpan = findMinSpanFromHighlights(r.Highlighted, termGroups)
			}

			if minSpan < 0 {
				posLists := make([][]int, len(termGroups))
				allFound := true
				for j, group := range termGroups {
					var merged []int
					for _, term := range group {
						merged = append(merged, findAllPositions(content, term)...)
					}
					if len(merged) == 0 {
						allFound = false
						break
					}
					sort.Ints(merged)
					posLists[j] = merged
				}
				if allFound {
					minSpan = findMinSpan(posLists)
				}
			}

			// Proximity boost: tighter minSpan (relative to content length)
			// scores higher. Normalized by content length so a tight span in a
			// long document isn't penalized (ADR-014).
			proximityBoost := 0.0
			if minSpan >= 0 {
				contentLen := max(len(r.Content), 1)
				proximityBoost = 1.0 / (1.0 + float64(minSpan)/float64(contentLen))
			}

			// Phrase-frequency boost: count adjacent ordered occurrences of the
			// raw (non-synonym-expanded) query terms in the content. Unlike the
			// minSpan path above, this scan always rebuilds position lists from
			// the raw terms — it can't reuse the synonym-expanded posLists (wrong
			// term-length offsets) and those may be nil when minSpan came from the
			// highlight fast path. Rewards documents that repeat the literal
			// phrase, which a single-window minSpan can't distinguish.
			// findAllPositions scans left-to-right, so each list is ascending —
			// satisfying countAdjacentPairs' sorted-input precondition.
			rawPosLists := make([][]int, len(terms))
			for j, term := range terms {
				rawPosLists[j] = findAllPositions(content, term)
			}
			adjacentPairs := countAdjacentPairs(rawPosLists, terms, phraseGapChars)
			phraseBoost := 0.5 * math.Min(1.0, float64(adjacentPairs)/4.0)

			// Combine proximity and phrase boosts in a single multiplicative
			// pass. Title boost stays a separate pass (capy's two-pass approach,
			// deliberate divergence from upstream's single additive pass — ADR-014).
			if proximityBoost > 0 || phraseBoost > 0 {
				r.FusedScore *= (1.0 + proximityBoost + phraseBoost)
			}
		}
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].FusedScore != results[j].FusedScore {
			return results[i].FusedScore > results[j].FusedScore
		}
		if results[i].SourceID != results[j].SourceID {
			return results[i].SourceID < results[j].SourceID
		}
		return results[i].Title < results[j].Title
	})
	return results
}

// findMinSpanFromHighlights extracts match positions from FTS5 highlight
// markers (char(2) = start, char(3) = end) and finds the minimum window
// containing all term groups. Each group is a synonym set — a match
// against any term in the group counts. Single-pass: tracks stripped
// byte offset incrementally to avoid repeated string allocations.
func findMinSpanFromHighlights(highlighted string, termGroups [][]string) int {
	posLists := make([][]int, len(termGroups))
	pos := 0
	strippedPos := 0

	for {
		startIdx := strings.IndexByte(highlighted[pos:], 2)
		if startIdx < 0 {
			break
		}
		startIdx += pos

		endIdx := strings.IndexByte(highlighted[startIdx:], 3)
		if endIdx < 0 {
			break
		}
		endIdx += startIdx

		// Advance stripped position by the unhighlighted text before this marker.
		strippedPos += startIdx - pos

		matched := strings.ToLower(highlighted[startIdx+1 : endIdx])
		for i, group := range termGroups {
			for _, term := range group {
				if strings.Contains(matched, term) {
					posLists[i] = append(posLists[i], strippedPos)
					break // one match per group per highlight span
				}
			}
		}

		// Advance stripped position by the matched text length.
		strippedPos += endIdx - (startIdx + 1)
		pos = endIdx + 1
	}

	for _, list := range posLists {
		if len(list) == 0 {
			return -1
		}
	}
	return findMinSpan(posLists)
}

// findAllPositions returns all start positions of term in text.
func findAllPositions(text, term string) []int {
	var positions []int
	start := 0
	for {
		idx := strings.Index(text[start:], term)
		if idx < 0 {
			break
		}
		positions = append(positions, start+idx)
		start += idx + 1
	}
	return positions
}

// phraseGapChars is the maximum byte gap between the end of one query term and
// the start of the next for the two to count as an adjacent phrase occurrence.
const phraseGapChars = 30

// countAdjacentPairs counts ordered adjacent-pair occurrences of consecutive
// query terms within a gap window. positionLists[i] holds the sorted start
// positions of terms[i] in the content (the two slices are parallel). For each
// consecutive pair (terms[i], terms[i+1]) it sweeps the left positions against
// the right ones: a left position p matches the nearest unconsumed right
// position in [p+len(terms[i]), p+len(terms[i])+gap]. Each right position is
// consumed at most once (greedy left-to-right), so "foo foo bar" counts one
// pair for query "foo bar", not two — matching IR phrase-occurrence intent.
func countAdjacentPairs(positionLists [][]int, terms []string, gap int) int {
	pairs := 0
	for i := 0; i+1 < len(terms); i++ {
		left := positionLists[i]
		right := positionLists[i+1]
		termLen := len(terms[i])

		// Single right pointer: windowStart = p + termLen grows monotonically
		// with p, so a right position already behind one window can never match
		// a later (larger) one — advance past it permanently.
		rj := 0
		for _, p := range left {
			windowStart := p + termLen
			windowEnd := windowStart + gap
			for rj < len(right) && right[rj] < windowStart {
				rj++
			}
			if rj < len(right) && right[rj] <= windowEnd {
				pairs++
				rj++ // consume this right position
			}
		}
	}
	return pairs
}

// findMinSpan finds the minimum window containing at least one element from
// each position list using a sweep-line algorithm.
func findMinSpan(positionLists [][]int) int {
	n := len(positionLists)
	if n == 0 {
		return 0
	}

	// Initialize pointers — one per list.
	ptrs := make([]int, n)
	best := math.MaxInt

	for {
		// Find current min and max positions across all pointers.
		curMin, curMax := math.MaxInt, math.MinInt
		minList := 0
		for i, p := range ptrs {
			val := positionLists[i][p]
			if val < curMin {
				curMin = val
				minList = i
			}
			if val > curMax {
				curMax = val
			}
		}

		span := curMax - curMin
		if span < best {
			best = span
		}

		// Advance the pointer at the minimum position.
		ptrs[minList]++
		if ptrs[minList] >= len(positionLists[minList]) {
			break
		}
	}

	return best
}
