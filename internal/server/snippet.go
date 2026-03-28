package server

import (
	"sort"
	"strings"
)

const (
	stx = '\x02' // FTS5 highlight start marker
	etx = '\x03' // FTS5 highlight end marker
)

// positionsFromHighlight parses FTS5 highlight markers (STX/ETX) to find
// match positions in the original (marker-free) text. Returns character
// offsets into the stripped content where each matched token begins.
func positionsFromHighlight(highlighted string) []int {
	var positions []int
	cleanOffset := 0

	i := 0
	for i < len(highlighted) {
		if highlighted[i] == byte(stx) {
			positions = append(positions, cleanOffset)
			i++ // skip STX
			for i < len(highlighted) && highlighted[i] != byte(etx) {
				cleanOffset++
				i++
			}
			if i < len(highlighted) {
				i++ // skip ETX
			}
		} else {
			cleanOffset++
			i++
		}
	}

	return positions
}

// markerReplacer strips STX/ETX highlight markers from FTS5 highlighted text.
var markerReplacer = strings.NewReplacer(string(stx), "", string(etx), "")

// stripMarkers removes STX/ETX highlight markers from FTS5 highlighted text.
func stripMarkers(highlighted string) string {
	return markerReplacer.Replace(highlighted)
}

// ExtractSnippet extracts a relevant snippet from content around query matches.
//
// It uses FTS5 highlight markers (STX/ETX) when available to locate matches,
// falling back to strings.Index on query terms. Merges overlapping 300-char
// windows and collects them up to maxLen.
func ExtractSnippet(content, query string, maxLen int, highlighted string) string {
	if len(content) <= maxLen {
		return content
	}

	var positions []int

	// Derive match positions from FTS5 highlight markers when available
	if highlighted != "" {
		positions = positionsFromHighlight(highlighted)
	}

	// Fallback: indexOf on raw query terms
	if len(positions) == 0 {
		terms := splitQueryTerms(query)
		lower := strings.ToLower(content)

		for _, term := range terms {
			searchFrom := 0
			for {
				idx := strings.Index(lower[searchFrom:], term)
				if idx == -1 {
					break
				}
				absIdx := searchFrom + idx
				positions = append(positions, absIdx)
				searchFrom = absIdx + 1
			}
		}
	}

	// No matches — return prefix
	if len(positions) == 0 {
		return content[:maxLen] + "\n…"
	}

	// Sort positions, merge overlapping windows
	sort.Ints(positions)
	const window = 300
	type span struct{ start, end int }
	var windows []span

	for _, pos := range positions {
		start := max(0, pos-window)
		end := min(len(content), pos+window)
		if len(windows) > 0 && start <= windows[len(windows)-1].end {
			windows[len(windows)-1].end = end
		} else {
			windows = append(windows, span{start, end})
		}
	}

	// Collect windows until maxLen
	var parts []string
	total := 0
	for _, w := range windows {
		if total >= maxLen {
			break
		}
		end := min(w.end, w.start+(maxLen-total))
		part := content[w.start:end]
		prefix := ""
		suffix := ""
		if w.start > 0 {
			prefix = "…"
		}
		if w.end < len(content) {
			suffix = "…"
		}
		parts = append(parts, prefix+part+suffix)
		total += len(part)
	}

	return strings.Join(parts, "\n\n")
}

// splitQueryTerms splits a query into lowercase terms > 2 chars.
func splitQueryTerms(query string) []string {
	var terms []string
	for _, t := range strings.Fields(strings.ToLower(query)) {
		if len(t) > 2 {
			terms = append(terms, t)
		}
	}
	return terms
}
