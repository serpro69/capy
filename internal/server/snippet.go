package server

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
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

// markerVisible shows literal STX/ETX bytes in stored content as U+FFFD.
var markerVisible = strings.NewReplacer(string(stx), "\uFFFD", string(etx), "\uFFFD")

// stripMarkers removes STX/ETX highlight markers from FTS5 highlighted text.
func stripMarkers(highlighted string) string {
	return markerReplacer.Replace(highlighted)
}

// extractSnippet extracts a relevant snippet from content around query matches.
//
// It uses FTS5 highlight markers (STX/ETX) when they mark exactly this content,
// falling back to a case-insensitive search for query terms. Overlapping
// 300-byte windows are merged and collected up to maxLen bytes, which include
// the omission markers and joins. Invalid UTF-8 becomes U+FFFD and every cut
// falls on a rune boundary, so the result is valid UTF-8.
//
// It also returns the selected windows as start/end pairs in content after
// UTF-8 normalization, before clipping to maxLen. With the content they
// determine the snippet, so equal snippet text from different selections of
// one content (a repeated region, or windows clipped to a common prefix) can
// be told apart.
func extractSnippet(content, query string, maxLen int, highlighted string) (string, []int) {
	content = strings.ToValidUTF8(content, "\uFFFD")
	if len(content) <= maxLen {
		return content, []int{0, len(content)}
	}

	var positions []int
	// Highlight offsets index the highlighted text with its markers removed, so
	// they are only usable when that text is this exact content and the content
	// carries no marker bytes of its own.
	if highlighted != "" && !strings.ContainsAny(content, string(stx)+string(etx)) && stripMarkers(highlighted) == content {
		positions = positionsFromHighlight(highlighted)
	}
	if len(positions) == 0 {
		positions = termPositions(content, query)
	}

	if len(positions) == 0 {
		return clipBytes(content, maxLen, "\n…"), []int{0, len(content)}
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
			windows[len(windows)-1].end = max(windows[len(windows)-1].end, end)
		} else {
			windows = append(windows, span{start, end})
		}
	}

	spans := make([]int, 0, 2*len(windows))
	for _, w := range windows {
		spans = append(spans, w.start, w.end)
	}

	const marker, join = "…", "\n\n"
	var b strings.Builder
	for _, w := range windows {
		sep := ""
		if b.Len() > 0 {
			sep = join
		}
		start := snapRuneForward(content, w.start)
		prefix := ""
		if start > 0 {
			prefix = marker
		}
		// Always leave room for a trailing marker so a cut window can say so.
		room := maxLen - b.Len() - len(sep) - len(prefix) - len(marker)
		if room <= 0 {
			break
		}
		end := snapRuneBack(content, min(w.end, start+room))
		if end <= start {
			break
		}
		b.WriteString(sep)
		b.WriteString(prefix)
		b.WriteString(content[start:end])
		if end < len(content) {
			b.WriteString(marker)
		}
	}
	return b.String(), spans
}

// termPositions returns the byte offsets in content where each query term
// (see splitQueryTerms) begins, matching case-insensitively. Offsets index the
// original content even where lowercasing changes a rune's encoded length.
func termPositions(content, query string) []int {
	terms := splitQueryTerms(query)
	if len(terms) == 0 {
		return nil
	}
	var folded strings.Builder
	folded.Grow(len(content))
	// origin[i] is the content offset of the rune that produced folded byte i.
	origin := make([]int, 0, len(content))
	for i, r := range content {
		n, _ := folded.WriteRune(unicode.ToLower(r))
		for range n {
			origin = append(origin, i)
		}
	}
	lower := folded.String()

	var positions []int
	for _, term := range terms {
		for from := 0; from < len(lower); {
			idx := strings.Index(lower[from:], term)
			if idx == -1 {
				break
			}
			abs := from + idx
			positions = append(positions, origin[abs])
			from = abs + 1
		}
	}
	return positions
}

// clipBytes returns s when it fits in maxLen bytes; otherwise the longest
// rune-aligned prefix that fits together with marker, followed by marker.
func clipBytes(s string, maxLen int, marker string) string {
	if len(s) <= maxLen {
		return s
	}
	cut := maxLen - len(marker)
	if cut <= 0 {
		return marker[:snapRuneBack(marker, maxLen)]
	}
	return s[:snapRuneBack(s, cut)] + marker
}

// snapRuneBack moves i back to the nearest rune start in s.
func snapRuneBack(s string, i int) int {
	for i > 0 && i < len(s) && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

// snapRuneForward moves i forward to the nearest rune start in s.
func snapRuneForward(s string, i int) int {
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return i
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
