package store

import "strings"

// synonymGroups defines developer-domain synonym groups. Each group maps
// abbreviations and related terms to one another for query expansion.
// Ported from agentmemory/src/state/synonyms.ts.
var synonymGroups = [][]string{
	{"auth", "authentication", "authn", "authenticating"},
	{"authz", "authorization", "authorizing"},
	{"db", "database", "datastore"},
	{"perf", "performance", "latency", "throughput", "slow", "bottleneck"},
	{"optim", "optimization", "optimizing", "optimise"},
	{"k8s", "kubernetes", "kube"},
	{"config", "configuration", "configuring"},
	{"env", "environment"},
	{"fn", "function"},
	{"impl", "implementation", "implementing"},
	{"msg", "message", "messaging"},
	{"repo", "repository"},
	{"req", "request"},
	{"res", "response"},
	{"ts", "typescript"},
	{"js", "javascript"},
	{"pg", "postgres", "postgresql"},
	{"err", "error", "errors"},
	{"api", "endpoint", "endpoints"},
	{"ci", "continuous-integration"},
	{"cd", "continuous-deployment"},
	{"doc", "documentation", "docs"},
	{"infra", "infrastructure"},
	{"deploy", "deployment", "deploying"},
	{"cache", "caching", "cached"},
	{"log", "logging", "logs"},
	{"monitor", "monitoring"},
	{"observe", "observability"},
	{"sec", "security", "secure"},
	{"validate", "validation", "validating"},
	{"migrate", "migration", "migrations"},
	{"debug", "debugging"},
	{"container", "containerization", "docker"},
	{"webhook", "webhooks", "callback"},
	{"middleware", "mw"},
	{"paginate", "pagination"},
	{"serialize", "serialization"},
	{"encrypt", "encryption"},
	{"hash", "hashing"},
}

// synonymMap is a bidirectional lookup: each term maps to all other terms in its group.
var synonymMap map[string][]string

func init() {
	synonymMap = make(map[string][]string)
	for _, group := range synonymGroups {
		for _, term := range group {
			lower := strings.ToLower(term)
			if _, exists := synonymMap[lower]; exists {
				panic("synonym term appears in multiple groups: " + lower)
			}
			if IsStopword(lower) {
				panic("synonym term is also a stopword (would be stripped at index time): " + lower)
			}
			// Collect all other terms in the group.
			others := make([]string, 0, len(group)-1)
			for _, other := range group {
				otherLower := strings.ToLower(other)
				if otherLower != lower {
					others = append(others, otherLower)
				}
			}
			synonymMap[lower] = others
		}
	}
}

// ExpandSynonyms returns the synonyms for the given term (lowercased), or nil
// if the term has no synonyms. The returned slice does NOT include the term itself.
// Returns a copy to prevent callers from mutating the package-level map.
func ExpandSynonyms(term string) []string {
	syns := synonymMap[strings.ToLower(term)]
	if len(syns) == 0 {
		return nil
	}
	return append([]string(nil), syns...)
}

// HasSynonym reports whether the given term exists in the synonym map.
func HasSynonym(term string) bool {
	_, ok := synonymMap[strings.ToLower(term)]
	return ok
}
