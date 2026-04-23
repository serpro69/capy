package store

import (
	"regexp"
	"strings"
)

var wordSplitRe = regexp.MustCompile(`[^\p{L}\p{N}_-]+`)

// extractAndStoreVocabulary splits content into words, filters by length
// and stopwords, and inserts unique words into the vocabulary table
// in a single transaction.
func (s *ContentStore) extractAndStoreVocabulary(content string) error {
	words := wordSplitRe.Split(strings.ToLower(content), -1)

	seen := make(map[string]struct{})
	var unique []string
	for _, w := range words {
		if len(w) < 3 || IsStopword(w) {
			continue
		}
		if _, ok := seen[w]; ok {
			continue
		}
		seen[w] = struct{}{}
		unique = append(unique, w)
	}

	if len(unique) == 0 {
		return nil
	}

	db, err := s.getDB()
	if err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt := tx.Stmt(s.stmtInsertVocab)
	for _, w := range unique {
		if _, err := stmt.Exec(w); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	s.fuzzyCacheMu.Lock()
	s.fuzzyCache = make(map[string]*string)
	s.fuzzyCacheMu.Unlock()

	return nil
}
