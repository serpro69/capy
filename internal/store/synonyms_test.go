package store

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExpandSynonymsBidirectional(t *testing.T) {
	// "db" → ["database", "datastore"]
	syns := ExpandSynonyms("db")
	assert.Contains(t, syns, "database")
	assert.Contains(t, syns, "datastore")

	// Reverse: "database" → ["db", "datastore"]
	syns2 := ExpandSynonyms("database")
	assert.Contains(t, syns2, "db")
	assert.Contains(t, syns2, "datastore")

	// And the third member
	syns3 := ExpandSynonyms("datastore")
	assert.Contains(t, syns3, "db")
	assert.Contains(t, syns3, "database")
}

func TestExpandSynonymsCaseInsensitive(t *testing.T) {
	syns := ExpandSynonyms("DB")
	assert.Contains(t, syns, "database")

	syns2 := ExpandSynonyms("Database")
	assert.Contains(t, syns2, "db")
}

func TestExpandSynonymsUnknownTerm(t *testing.T) {
	syns := ExpandSynonyms("xyznonexistent")
	assert.Nil(t, syns)
}

func TestExpandSynonymsDoesNotIncludeSelf(t *testing.T) {
	syns := ExpandSynonyms("auth")
	assert.NotContains(t, syns, "auth")
	assert.Contains(t, syns, "authentication")
}

func TestHasSynonymTrue(t *testing.T) {
	assert.True(t, HasSynonym("db"))
	assert.True(t, HasSynonym("Database"))
	assert.True(t, HasSynonym("perf"))
	assert.True(t, HasSynonym("k8s"))
}

func TestHasSynonymFalse(t *testing.T) {
	assert.False(t, HasSynonym("xyznonexistent"))
	assert.False(t, HasSynonym(""))
	assert.False(t, HasSynonym("foobar"))
}

func TestSynonymGroupCompleteness(t *testing.T) {
	// Verify a few groups have expected members.
	authSyns := ExpandSynonyms("auth")
	assert.ElementsMatch(t, []string{"authentication", "authn", "authenticating"}, authSyns)

	perfSyns := ExpandSynonyms("perf")
	assert.ElementsMatch(t, []string{"performance", "latency", "throughput", "slow", "bottleneck"}, perfSyns)

	k8sSyns := ExpandSynonyms("kubernetes")
	assert.ElementsMatch(t, []string{"k8s", "kube"}, k8sSyns)
}

func TestSynonymGroupsNoDuplicatesOrStopwords(t *testing.T) {
	// Verify init() invariants hold: no duplicate terms across groups,
	// no terms that overlap with the stopword list. If init() panics,
	// this test (and all others in the package) would fail at load time,
	// but this test documents the invariant explicitly.
	seen := make(map[string]bool)
	for _, group := range synonymGroups {
		for _, term := range group {
			lower := strings.ToLower(term)
			assert.False(t, seen[lower], "duplicate synonym term: %s", lower)
			assert.False(t, IsStopword(lower), "synonym term is also a stopword: %s", lower)
			seen[lower] = true
		}
	}
}

func TestExpandSynonymsReturnsCopy(t *testing.T) {
	// Verify that mutating the returned slice doesn't corrupt the synonym map.
	syns := ExpandSynonyms("db")
	require.NotNil(t, syns)
	original := make([]string, len(syns))
	copy(original, syns)

	// Mutate the returned slice.
	syns[0] = "corrupted"

	// The map should be unaffected.
	syns2 := ExpandSynonyms("db")
	assert.ElementsMatch(t, original, syns2, "ExpandSynonyms should return a copy, not a reference")
}
