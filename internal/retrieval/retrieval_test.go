package retrieval

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Corpus construction ---

func TestNewCorpusRejectsUnknownTables(t *testing.T) {
	exec := func(ctx context.Context, table, ftsQuery string, limit int) []SearchResult { return nil }

	_, err := NewCorpus("sources", "chunks_trigram", exec, nil)
	assert.Error(t, err, "porter table outside the allowlist must be rejected")

	_, err = NewCorpus("chunks", "chunks_trigram; DROP TABLE sources", exec, nil)
	assert.Error(t, err, "trigram table outside the allowlist must be rejected")
}

func TestNewCorpusRejectsSameTableForBothLayers(t *testing.T) {
	exec := func(ctx context.Context, table, ftsQuery string, limit int) []SearchResult { return nil }

	_, err := NewCorpus("chunks", "chunks", exec, nil)
	assert.Error(t, err, "using one table for both layers would double-count every fusion hit")
}

func TestNewCorpusRequiresExec(t *testing.T) {
	_, err := NewCorpus("chunks", "chunks_trigram", nil, nil)
	assert.Error(t, err, "corpus without an Exec search function must be rejected")
}

func TestRRFSearchZeroValueCorpusPanics(t *testing.T) {
	assert.Panics(t, func() {
		RRFSearch(context.Background(), Corpus{}, `"q"`, `"q"`, "q", 5)
	}, "zero-value Corpus bypasses NewCorpus and must fail loud")
}

// --- Engine orchestration over a stub corpus ---

// stubCorpus returns a Corpus whose layers serve canned results and records
// every executed (table, query) pair for pass-count assertions. The engine
// runs the two layers concurrently, so the recorder must be mutex-guarded —
// real corpora get this safety from database/sql.
func stubCorpus(t *testing.T, byTable map[string][]SearchResult, fuzzy FuzzyCorrector, calls *[][2]string) Corpus {
	t.Helper()
	var mu sync.Mutex
	c, err := NewCorpus("chunks", "chunks_trigram",
		func(ctx context.Context, table, ftsQuery string, limit int) []SearchResult {
			if calls != nil {
				mu.Lock()
				*calls = append(*calls, [2]string{table, ftsQuery})
				mu.Unlock()
			}
			return byTable[table]
		},
		fuzzy,
	)
	require.NoError(t, err)
	return c
}

func TestSearchWithFallbackNilFuzzySkipsFuzzyPass(t *testing.T) {
	// Zero results everywhere and a nil corrector: the engine must run the
	// synonym-AND pass and the flat-OR fallback, then stop — no fuzzy pass.
	var calls [][2]string
	c := stubCorpus(t, nil, nil, &calls)

	results := SearchWithFallback(context.Background(), c, "authentcation", 5, 0)
	assert.Empty(t, results)
	assert.Len(t, calls, 4, "expected exactly 2 passes × 2 layers with a nil FuzzyCorrector")
}

func TestSearchWithFallbackFuzzyPassUsesCorrector(t *testing.T) {
	// Sparse results + a corrector: the corrected query re-enters the engine,
	// and its results are tagged with the fuzzy+ layer prefix.
	hit := SearchResult{SourceID: 1, Title: "Auth", Content: "authentication middleware"}
	served := false
	c, err := NewCorpus("chunks", "chunks_trigram",
		func(ctx context.Context, table, ftsQuery string, limit int) []SearchResult {
			// Serve the hit only for the corrected term.
			if table == "chunks" && strings.Contains(ftsQuery, "authentication") {
				served = true
				return []SearchResult{hit}
			}
			return nil
		},
		func(word string) string {
			if word == "authentcation" {
				return "authentication"
			}
			return ""
		},
	)
	require.NoError(t, err)

	results := SearchWithFallback(context.Background(), c, "authentcation", 5, 0)
	require.True(t, served, "corrected query should reach the corpus")
	require.NotEmpty(t, results, "fuzzy-corrected pass should surface results")
	assert.Contains(t, results[0].MatchLayer, "fuzzy+",
		"fuzzy-pass results must be tagged with the fuzzy+ layer prefix")
}

// --- fuzzyCorrectQuery orchestration ---

func TestFuzzyCorrectQuerySkipsStopwords(t *testing.T) {
	// "the" is a stopword and "in" is < 3 chars — both must pass through
	// without ever reaching the corrector; "errro" is corrected; "code" is a
	// stopword too (code/changelog set) and must also pass through.
	corrector := func(word string) string {
		switch word {
		case "errro":
			return "error"
		case "the", "code":
			t.Fatalf("stopword %q must not reach the corrector", word)
		case "in":
			t.Fatalf("short word %q must not reach the corrector", word)
		}
		return ""
	}

	corrected := fuzzyCorrectQuery(corrector, "the errro in code")
	assert.Equal(t, "the error in code", corrected,
		"typo corrected; stopwords and short words pass through unchanged")
}

func TestFuzzyCorrectQueryNoCorrection(t *testing.T) {
	corrector := func(word string) string { return "" }
	assert.Equal(t, "", fuzzyCorrectQuery(corrector, "clean query words"),
		"no correction made should return the empty string")
}

// --- diversifyBySource unit tests ---

func TestDiversifyBySourceUnit(t *testing.T) {
	// 6 results: 4 from source 1, 1 from source 2, 1 from source 3.
	results := []SearchResult{
		{SourceID: 1, Label: "A", FusedScore: 0.10},
		{SourceID: 1, Label: "A", FusedScore: 0.09},
		{SourceID: 1, Label: "A", FusedScore: 0.08},
		{SourceID: 1, Label: "A", FusedScore: 0.07},
		{SourceID: 2, Label: "B", FusedScore: 0.06},
		{SourceID: 3, Label: "C", FusedScore: 0.05},
	}

	diversified := diversifyBySource(results, 5, 2)

	// Pass 1 picks: A(0.10), A(0.09), B(0.06), C(0.05) — skips A(0.08), A(0.07).
	// Pass 2 fills: A(0.08) — total 5.
	require.Len(t, diversified, 5)
	assert.Equal(t, int64(1), diversified[0].SourceID) // A
	assert.Equal(t, int64(1), diversified[1].SourceID) // A
	assert.Equal(t, int64(2), diversified[2].SourceID) // B
	assert.Equal(t, int64(3), diversified[3].SourceID) // C
	assert.Equal(t, int64(1), diversified[4].SourceID) // A (backfill)
}

func TestDiversifyBySourceEmpty(t *testing.T) {
	result := diversifyBySource(nil, 5, 2)
	assert.Empty(t, result)
}

func TestDiversifyBySourceAllUnique(t *testing.T) {
	results := []SearchResult{
		{SourceID: 1, Label: "A", FusedScore: 0.10},
		{SourceID: 2, Label: "B", FusedScore: 0.09},
		{SourceID: 3, Label: "C", FusedScore: 0.08},
	}
	diversified := diversifyBySource(results, 5, 2)
	// No capping needed — all unique sources.
	require.Len(t, diversified, 3)
	assert.Equal(t, int64(1), diversified[0].SourceID)
	assert.Equal(t, int64(2), diversified[1].SourceID)
	assert.Equal(t, int64(3), diversified[2].SourceID)
}
