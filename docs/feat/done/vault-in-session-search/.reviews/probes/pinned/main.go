package main

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/sahilm/fuzzy"
)

func check(label, query, candidate string, identity bool) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%s panic=%v\n", label, r)
		}
	}()
	matches := fuzzy.FindNoSort(query, []string{candidate})
	if len(matches) != 1 {
		fmt.Printf("%s matches=%d\n", label, len(matches))
		return
	}
	m := matches[0]
	bad, prev := 0, -1
	qr := []rune(query)
	for i, at := range m.MatchedIndexes {
		invalid := at < 0 || at >= len(candidate) || at <= prev || i >= len(qr)
		if !invalid {
			r, _ := utf8.DecodeRuneInString(candidate[at:])
			invalid = !utf8.RuneStart(candidate[at]) || !strings.EqualFold(string(r), string(qr[i]))
		}
		if invalid {
			bad++
		}
		prev = at
	}
	fmt.Printf("%s score=%d positions=%d invalid=%d", label, m.Score, len(m.MatchedIndexes), bad)
	if identity && len(query) <= 43 {
		fmt.Printf(" offsets=%v", m.MatchedIndexes)
	}
	if !identity {
		fmt.Printf(" offsets=%v", m.MatchedIndexes)
	}
	fmt.Println()
}

func main() {
	fmt.Printf("%s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	var decoded string
	if err := json.Unmarshal([]byte("\"a\\u0000b\""), &decoded); err != nil {
		panic(err)
	}
	check("decoded_NUL_query_a", "a", decoded, false)
	check("decoded_NUL_query_b", "b", decoded, false)
	check("decoded_NUL_query_ab", "ab", decoded, false)
	check("leading_NUL", "ab", "\x00ab", false)
	check("trailing_NUL", "ab", "ab\x00", false)
	check("ordinary", "abc", "abc", true)
	for _, n := range []int{32, 39, 40, 41, 42, 43, 64, 128, 256} {
		s := strings.Repeat("a", n)
		check(fmt.Sprintf("repeat_%d", n), s, s, true)
	}
}
