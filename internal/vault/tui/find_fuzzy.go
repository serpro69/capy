package tui

import (
	"context"
	"sort"
	"unicode"
	"unicode/utf8"
)

// A result is one original content line. Spans are immutable original-field
// byte ranges, one per query rune; ranking never changes the greedy alignment.
type findFuzzyHit struct {
	line  int
	spans []findHit
	score int64
}

func findFold(r rune) rune {
	// ASCII folding avoids a Unicode table lookup on the common scan path.
	if r < utf8.RuneSelf {
		if r >= 'a' && r <= 'z' {
			return r - ('a' - 'A')
		}
		return r
	}
	least := r
	for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
		least = min(least, next)
	}
	return least
}

func findWordBoundary(r rune) bool {
	return unicode.IsSpace(r) || r == '/' || r == '-' || r == '_' || r == '.' || r == '\\'
}

func matchFindFuzzy(ctx context.Context, text string, query []rune) ([]findHit, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if len(query) == 0 || len(query) > 256 {
		return nil, 0, nil
	}
	// Most lines fail. Allocate result storage only after a complete alignment.
	var spans [256]findHit
	matched, leading, gaps, trailing := 0, 0, 0, 0
	var score int64
	var previous rune
	adjacent, first := false, true
	nextCheck := 0
	for offset := 0; offset < len(text); {
		if offset >= nextCheck {
			if err := ctx.Err(); err != nil {
				return nil, 0, err
			}
			nextCheck = offset + findCheckBytes
		}
		r, size := utf8.DecodeRuneInString(text[offset:])
		switch {
		case matched == len(query):
			trailing = min(4096, trailing+1)
		case findFold(r) == query[matched]:
			spans[matched] = findHit{start: offset, end: offset + size}
			matched++
			score += 16
			if adjacent {
				score += 8
			}
			if first || findWordBoundary(previous) {
				score += 8
			}
			if unicode.IsLower(previous) && unicode.IsUpper(r) {
				score += 4
			}
			if first {
				score += 16
			}
			adjacent = true
		default:
			if matched == 0 {
				leading = min(256, leading+1)
			} else {
				gaps = min(4096, gaps+1)
			}
			adjacent = false
		}
		offset += size
		previous, first = r, false
		if trailing == 4096 {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if matched != len(query) {
		return nil, 0, nil
	}
	return append([]findHit(nil), spans[:matched]...), score - int64(leading+gaps+trailing), nil
}

func findFuzzy(ctx context.Context, c *findCorpus, query string) ([]findFuzzyHit, error) {
	runes := []rune(query)
	if len(runes) > 256 {
		return nil, ctx.Err()
	}
	for i := range runes {
		runes[i] = findFold(runes[i])
	}
	hits := make([]findFuzzyHit, 0)
	for i, line := range c.lines {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if query == "" {
			if line.text != "" {
				hits = append(hits, findFuzzyHit{line: i})
			}
			continue
		}
		spans, score, err := matchFindFuzzy(ctx, line.text, runes)
		if err != nil {
			return nil, err
		}
		if len(spans) == 0 {
			continue
		}
		for j := range spans {
			spans[j].line = i
			spans[j].start += line.position.offset
			spans[j].end += line.position.offset
		}
		hits = append(hits, findFuzzyHit{i, spans, score})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].line < hits[j].line
	})
	return hits, ctx.Err()
}
