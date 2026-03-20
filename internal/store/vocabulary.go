package store

import (
	"regexp"
	"strings"
)

var wordSplitRe = regexp.MustCompile(`[^\p{L}\p{N}_-]+`)

// extractAndStoreVocabulary splits content into words, filters by length
// and stopwords, and inserts unique words into the vocabulary table.
func (s *ContentStore) extractAndStoreVocabulary(content string) error {
	words := wordSplitRe.Split(strings.ToLower(content), -1)

	seen := make(map[string]struct{})
	for _, w := range words {
		if len(w) < 3 {
			continue
		}
		if IsStopword(w) {
			continue
		}
		if _, ok := seen[w]; ok {
			continue
		}
		seen[w] = struct{}{}
		if _, err := s.stmtInsertVocab.Exec(w); err != nil {
			return err
		}
	}
	return nil
}
