package store

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func BenchmarkIndex(b *testing.B) {
	for _, ct := range contentTypes {
		b.Run(ct, func(b *testing.B) {
			entries := loadFixtures(b, ct)
			haystack := entries[0].Haystack
			kind := entries[0].SourceKind

			s := newBenchStore(b)
			i := 0
			for b.Loop() {
				label := fmt.Sprintf("bench-%s-%d", ct, i)
				i++
				var err error
				switch ct {
				case "markdown", "curated":
					_, err = s.Index(haystack, label, "", kind)
				case "json":
					_, err = s.IndexJSON(haystack, label, kind)
				case "plaintext":
					_, err = s.IndexPlainText(haystack, label, kind)
				case "transcript":
					_, err = s.Index(haystack, label, "", kind)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkSearch(b *testing.B) {
	corpusSizes := []int{100, 1000, 10000}
	opts := benchSearchOpts()

	for _, ct := range contentTypes {
		entries := loadFixtures(b, ct)
		query := entries[0].Cases[0].Query

		for _, n := range corpusSizes {
			b.Run(fmt.Sprintf("%s/%d", ct, n), func(b *testing.B) {
				s := newBenchStore(b)
				seedCorpus(b, s, entries, ct, n)
				for b.Loop() {
					if _, err := s.SearchWithFallback(query, 10, opts); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func BenchmarkSearchByTier(b *testing.B) {
	tiers := make(map[string]string)

	for _, ct := range contentTypes {
		for _, e := range loadFixtures(b, ct) {
			for _, c := range e.Cases {
				if c.ExpectedLayer == "none" || c.ExpectedLayer == "" {
					continue
				}
				if _, exists := tiers[c.ExpectedLayer]; !exists {
					tiers[c.ExpectedLayer] = c.Query
				}
			}
		}
	}

	if len(tiers) == 0 {
		b.Fatal("no tiers found in fixtures — fixture expected_layer values may need updating")
	}

	opts := benchSearchOpts()
	s := newBenchStore(b)
	for _, ct := range contentTypes {
		seedStore(b, s, loadFixtures(b, ct))
	}

	tierNames := make([]string, 0, len(tiers))
	for tier := range tiers {
		tierNames = append(tierNames, tier)
	}
	slices.Sort(tierNames)

	for _, tier := range tierNames {
		b.Run(tier, func(b *testing.B) {
			query := tiers[tier]
			for b.Loop() {
				if _, err := s.SearchWithFallback(query, 10, opts); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// seedCorpus seeds a store with n synthetic entries derived from fixtures.
func seedCorpus(tb testing.TB, s *ContentStore, entries []benchEntry, ct string, n int) {
	tb.Helper()
	for i := range n {
		src := entries[i%len(entries)]
		label := fmt.Sprintf("corpus-%s-%d", ct, i)
		// Vary content slightly to avoid content-hash dedup.
		haystack := fmt.Sprintf("--- entry %d ---\n%s", i, src.Haystack)

		var err error
		switch ct {
		case "markdown", "curated":
			_, err = s.Index(haystack, label, "", src.SourceKind)
		case "json":
			wrapper := struct {
				BenchID int             `json:"_bench_id"`
				Data    json.RawMessage `json:"data"`
			}{i, json.RawMessage(strings.TrimSpace(src.Haystack))}
			raw, _ := json.Marshal(wrapper)
			haystack = string(raw)
			_, err = s.IndexJSON(haystack, label, src.SourceKind)
		case "plaintext":
			_, err = s.IndexPlainText(haystack, label, src.SourceKind)
		case "transcript":
			_, err = s.Index(haystack, label, "", src.SourceKind)
		}
		if err != nil {
			tb.Fatalf("seeding corpus entry %d: %v", i, err)
		}
	}
}
