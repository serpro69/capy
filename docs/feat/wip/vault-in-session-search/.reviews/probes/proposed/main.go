package main

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

type hit struct{ start, end int }

func fold(r rune) rune {
	best := r
	for x := unicode.SimpleFold(r); x != r; x = unicode.SimpleFold(x) {
		if x < best {
			best = x
		}
	}
	return best
}
func boundary(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune("/-_.\\", r) }
func match(ctx context.Context, q, s string) ([]hit, int64, bool, error) {
	p := []rune(q)
	if len(p) == 0 || len(p) > 256 {
		return nil, 0, false, nil
	}
	for i := range p {
		p[i] = fold(p[i])
	}
	out := make([]hit, 0, len(p))
	var prev rune
	ordinal, previousMatch := -1, -2
	lead, gaps, tail, adj, words, camel, first := 0, 0, 0, 0, 0, 0, 0
	nextCheck := 0
	for at, r := range s {
		if at >= nextCheck {
			if err := ctx.Err(); err != nil {
				return nil, 0, false, err
			}
			nextCheck = at + 4096
		}
		ordinal++
		if len(out) == len(p) {
			if tail < 4096 {
				tail++
			}
			if tail == 4096 {
				break
			}
			continue
		}
		if fold(r) == p[len(out)] {
			_, size := utf8.DecodeRuneInString(s[at:])
			out = append(out, hit{at, at + size})
			if len(out) > 1 && ordinal == previousMatch+1 {
				adj++
			}
			if ordinal == 0 || boundary(prev) {
				words++
			}
			if ordinal > 0 && unicode.IsLower(prev) && unicode.IsUpper(r) {
				camel++
			}
			if len(out) == 1 && ordinal == 0 {
				first = 1
			}
			previousMatch = ordinal
		} else if len(out) == 0 {
			if lead < 256 {
				lead++
			}
		} else {
			if gaps < 4096 {
				gaps++
			}
		}
		prev = r
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, false, err
	}
	if len(out) != len(p) {
		return nil, 0, false, nil
	}
	score := 16*int64(len(p)) + 8*int64(adj) + 8*int64(words) + 4*int64(camel) + 16*int64(first) - int64(lead+gaps+tail)
	return out, score, true, nil
}
func oracle(q, s string) bool {
	p := []rune(q)
	reachable := make([]bool, len(p)+1)
	reachable[0] = true
	for _, r := range s {
		for j := len(p); j > 0; j-- {
			if reachable[j-1] && strings.EqualFold(string(r), string(p[j-1])) {
				reachable[j] = true
			}
		}
	}
	return reachable[len(p)]
}
func validate(q, s string) {
	positions, score, ok, err := match(context.Background(), q, s)
	if err != nil || ok != oracle(q, s) {
		panic(fmt.Sprintf("existence q=%q s=%q", q, s))
	}
	if !ok {
		return
	}
	if score < -8448 || score > 9232 {
		panic("score bound")
	}
	qr := []rune(q)
	if len(positions) != len(qr) {
		panic("count")
	}
	prev := -1
	for i, h := range positions {
		if h.start <= prev || h.start < 0 || h.end > len(s) || h.end <= h.start {
			panic("range")
		}
		if !strings.EqualFold(string(qr[i]), s[h.start:h.end]) {
			panic("correspondence")
		}
		prev = h.start
	}
}
func words(alphabet []rune, max int) []string {
	all := []string{""}
	level := []string{""}
	for range max {
		var next []string
		for _, s := range level {
			for _, r := range alphabet {
				next = append(next, s+string(r))
			}
		}
		all = append(all, next...)
		level = next
	}
	return all
}
func main() {
	for _, q := range []string{"a", "b", "ab"} {
		for _, s := range []string{"a\x00b", "\x00ab", "ab\x00", "a\x00\x00b"} {
			validate(q, s)
		}
	}
	for n := 1; n <= 256; n++ {
		for _, r := range []string{"a", "é", "Σ"} {
			q := strings.Repeat(r, n)
			validate(q, q)
			validate(q, "\x00"+q+"\x00")
		}
	}
	candidates := words([]rune{'a', 'A', 'b', 0, 'é', 'É'}, 5)
	queries := words([]rune{'a', 'b', 'é'}, 3)[1:]
	cases := 0
	for _, q := range queries {
		for _, s := range candidates {
			validate(q, s)
			cases++
		}
	}
	for _, pair := range [][2]string{{"σ", "Σςσ"}, {"k", "KkK"}, {"é", "éé"}, {"�", string([]byte{0xff, 'a'})}} {
		validate(pair[0], pair[1])
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, _, err := match(ctx, "ab", "a\x00b")
	if err == nil {
		panic("cancellation")
	}
	for _, s := range []string{"abc", "a_b_c", "a long b long c"} {
		p, score, ok, _ := match(context.Background(), "abc", s)
		fmt.Printf("ranking candidate=%q score=%d ok=%t positions=%v\n", s, score, ok, p)
	}
	fmt.Printf("PASS: %d exhaustive query/candidate pairs; lengths 1..256 in ASCII/Unicode; NUL, score bounds, source ranges, fold cases, and cancellation\n", cases)
}
