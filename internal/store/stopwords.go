package store

var stopwords = map[string]struct{}{
	"the": {}, "and": {}, "for": {}, "are": {}, "but": {}, "not": {}, "you": {}, "all": {}, "can": {}, "had": {},
	"her": {}, "was": {}, "one": {}, "our": {}, "out": {}, "has": {}, "his": {}, "how": {}, "its": {}, "may": {},
	"new": {}, "now": {}, "old": {}, "see": {}, "way": {}, "who": {}, "did": {}, "get": {}, "got": {}, "let": {},
	"say": {}, "she": {}, "too": {}, "use": {}, "will": {}, "with": {}, "this": {}, "that": {}, "from": {},
	"they": {}, "been": {}, "have": {}, "many": {}, "some": {}, "them": {}, "than": {}, "each": {}, "make": {},
	"like": {}, "just": {}, "over": {}, "such": {}, "take": {}, "into": {}, "year": {}, "your": {}, "good": {},
	"could": {}, "would": {}, "about": {}, "which": {}, "their": {}, "there": {}, "other": {}, "after": {},
	"should": {}, "through": {}, "also": {}, "more": {}, "most": {}, "only": {}, "very": {}, "when": {},
	"what": {}, "then": {}, "these": {}, "those": {}, "being": {}, "does": {}, "done": {}, "both": {},
	"same": {}, "still": {}, "while": {}, "where": {}, "here": {}, "were": {}, "much": {},
	// Common in code/changelogs
	"update": {}, "updates": {}, "updated": {}, "deps": {}, "dev": {}, "tests": {}, "test": {},
	"add": {}, "added": {}, "fix": {}, "fixed": {}, "run": {}, "running": {}, "using": {},
	// FTS5/search-domain terms (prevent matching on internal metadata)
	"code": {}, "fuzzy": {}, "porter": {}, "trigram": {}, "prose": {}, "untitled": {},
	"string": {}, "number": {}, "object": {},
	// Common data field names (appear in chunk titles/metadata)
	"name": {}, "title": {}, "label": {}, "path": {}, "slug": {}, "key": {}, "id": {},
}

// IsStopword returns true if the word is in the stopword set.
func IsStopword(word string) bool {
	_, ok := stopwords[word]
	return ok
}
