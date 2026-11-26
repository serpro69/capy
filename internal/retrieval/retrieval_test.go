package retrieval

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCorpusReportsLayerErrors(t *testing.T) {
	for _, mode := range []string{"query", "row scan", "row iteration"} {
		t.Run(mode, func(t *testing.T) {
			cfg := validCorpusConfig(t)
			if mode != "query" {
				_, err := cfg.DB.ExecContext(t.Context(), `
					CREATE VIRTUAL TABLE chunks USING fts5(title, content);
					CREATE VIRTUAL TABLE chunks_trigram USING fts5(title, content);
					INSERT INTO chunks VALUES ('orchard', 'orchard');
					INSERT INTO chunks_trigram VALUES ('orchard', 'orchard');`)
				if err != nil {
					t.Fatal(err)
				}
			}
			fault := errors.New("row mapper failure")
			if mode == "row scan" {
				cfg.MapRow = func(*sql.Rows) (SearchResult, error) { return SearchResult{}, fault }
			}
			var mu sync.Mutex
			var reported []error
			cfg.OnError = func(err error) { mu.Lock(); defer mu.Unlock(); reported = append(reported, err) }
			corpus, err := NewCorpus(cfg)
			if err != nil {
				t.Fatal(err)
			}
			query := `"orchard"`
			if mode == "row iteration" {
				query = `"orchard" OR`
			}
			results := RRFSearch(t.Context(), corpus, query, query, "orchard", 3)
			if len(results) != 0 || len(reported) != 2 {
				t.Fatalf("results=%d errors=%v", len(results), reported)
			}
			for _, err := range reported {
				if !strings.Contains(err.Error(), "search "+mode) {
					t.Fatalf("wrong failure stage: %v", err)
				}
				if mode == "row scan" && !errors.Is(err, fault) {
					t.Fatalf("lost error cause: %v", err)
				}
			}
		})
	}
}

// TestCorpusKeepsRowsReadBeforeIterationFailure: when iteration fails after
// some rows were read, a caller observing failures through OnError still gets
// those rows (a partial search shows them beside the failure). The failure is
// a context cancelled after the first row: database/sql then stops Next and
// reports the cancellation from Err.
func TestCorpusKeepsRowsReadBeforeIterationFailure(t *testing.T) {
	cfg := validCorpusConfig(t)
	_, err := cfg.DB.ExecContext(t.Context(), `
		CREATE VIRTUAL TABLE chunks USING fts5(title, content);
		CREATE VIRTUAL TABLE chunks_trigram USING fts5(title, content);
		INSERT INTO chunks VALUES ('orchard one', 'orchard'), ('orchard two', 'orchard'), ('orchard three', 'orchard');`)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	scan := cfg.MapRow
	cfg.MapRow = func(rows *sql.Rows) (SearchResult, error) {
		r, err := scan(rows)
		cancel()
		// Wait until database/sql has recorded the cancellation, so the next
		// Next call deterministically stops iteration. The deadline turns a
		// lost context (execSearch no longer forwarding ctx) into a failure
		// instead of a hang.
		deadline := time.Now().Add(5 * time.Second)
		for rows.Err() == nil {
			if time.Now().After(deadline) {
				t.Fatal("the cancelled context never reached the rows")
			}
			runtime.Gosched()
		}
		return r, err
	}
	var reported []error
	cfg.OnError = func(err error) { reported = append(reported, err) }
	corpus, err := NewCorpus(cfg)
	require.NoError(t, err)

	results := corpus.exec(ctx, "chunks", `"orchard"`, 5)
	require.Len(t, reported, 1)
	assert.ErrorIs(t, reported[0], context.Canceled)
	assert.Contains(t, reported[0].Error(), "search row iteration")
	require.Len(t, results, 1, "the row read before the failure is kept")
	assert.Equal(t, "chunks", results[0].Table)
}

// --- Corpus construction ---

// validCorpusConfig returns a minimal CorpusConfig that passes NewCorpus
// validation. sql.Open connects lazily, so no database is touched unless a
// test actually runs a query.
func validCorpusConfig(t *testing.T) CorpusConfig {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "corpus.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return CorpusConfig{
		DB:            db,
		PorterTable:   "chunks",
		TrigramTable:  "chunks_trigram",
		SelectColumns: "c.title, c.content",
		TitleWeight:   2.0,
		MapRow: func(rows *sql.Rows) (SearchResult, error) {
			var r SearchResult
			err := rows.Scan(&r.Title, &r.Content, &r.Highlighted, &r.Rank)
			return r, err
		},
	}
}

func TestNewCorpusRejectsUnknownTables(t *testing.T) {
	cfg := validCorpusConfig(t)
	cfg.PorterTable = "sources"
	_, err := NewCorpus(cfg)
	assert.Error(t, err, "porter table outside the allowlist must be rejected")

	cfg = validCorpusConfig(t)
	cfg.TrigramTable = "chunks_trigram; DROP TABLE sources"
	_, err = NewCorpus(cfg)
	assert.Error(t, err, "trigram table outside the allowlist must be rejected")
}

func TestNewCorpusRejectsSameTableForBothLayers(t *testing.T) {
	cfg := validCorpusConfig(t)
	cfg.TrigramTable = cfg.PorterTable
	_, err := NewCorpus(cfg)
	assert.Error(t, err, "using one table for both layers would double-count every fusion hit")
}

func TestNewCorpusRequiredFields(t *testing.T) {
	tests := []struct {
		name       string
		invalidate func(*CorpusConfig)
	}{
		{"nil DB", func(cfg *CorpusConfig) { cfg.DB = nil }},
		{"nil MapRow", func(cfg *CorpusConfig) { cfg.MapRow = nil }},
		{"empty SelectColumns", func(cfg *CorpusConfig) { cfg.SelectColumns = "" }},
		{"non-positive TitleWeight", func(cfg *CorpusConfig) { cfg.TitleWeight = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validCorpusConfig(t)
			tt.invalidate(&cfg)
			_, err := NewCorpus(cfg)
			assert.Error(t, err, "corpus with %s must be rejected", tt.name)
		})
	}
}

// TestCorpusExecSearchEndToEnd drives the shared layer-query skeleton against
// a real FTS5 database: SELECT/JOIN shape, pre-bound filter clauses, row
// mapping, and the skeleton-appended highlighted/rank columns.
func TestCorpusExecSearchEndToEnd(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "e2e.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec(`
		CREATE TABLE src (id INTEGER PRIMARY KEY, label TEXT);
		CREATE VIRTUAL TABLE chunks USING fts5(title, content, source_id UNINDEXED, tokenize='porter unicode61');
		CREATE VIRTUAL TABLE chunks_trigram USING fts5(title, content, source_id UNINDEXED, tokenize='trigram');
		INSERT INTO src (id, label) VALUES (1, 'keep'), (2, 'drop');
		INSERT INTO chunks (title, content, source_id) VALUES
			('Auth notes', 'authentication middleware setup', 1),
			('Auth notes', 'authentication middleware setup', 2);
		INSERT INTO chunks_trigram (title, content, source_id) VALUES
			('Auth notes', 'authentication middleware setup', 1),
			('Auth notes', 'authentication middleware setup', 2);
	`)
	require.NoError(t, err)

	c, err := NewCorpus(CorpusConfig{
		DB:            db,
		PorterTable:   "chunks",
		TrigramTable:  "chunks_trigram",
		SelectColumns: "s.label, c.title, c.content, c.source_id, c.rowid",
		Join:          "JOIN src s ON s.id = c.source_id",
		TitleWeight:   2.0,
		FilterSQL:     " AND s.label = ?",
		FilterParams:  []any{"keep"},
		MapRow: func(rows *sql.Rows) (SearchResult, error) {
			var r SearchResult
			err := rows.Scan(&r.Label, &r.Title, &r.Content, &r.SourceID, &r.RowID, &r.Highlighted, &r.Rank)
			return r, err
		},
	})
	require.NoError(t, err)

	results := SearchWithFallback(context.Background(), c, "authentication middleware", 5, 0)
	require.NotEmpty(t, results, "indexed content must be reachable through the skeleton")
	for _, r := range results {
		assert.Equal(t, "keep", r.Label, "pre-bound filter clause must exclude the filtered source")
		assert.Equal(t, int64(1), r.SourceID)
		assert.Contains(t, r.Highlighted, "\x02", "highlight() markers must survive row mapping")
		assert.Contains(t, []string{"chunks", "chunks_trigram"}, r.Table, "the producing layer table is stamped")
		assert.Equal(t, int64(1), r.RowID, "the scanned rowid locates the row in its table")
	}
	assert.Equal(t, "rrf(porter+trigram)", results[0].MatchLayer,
		"a term present in both layers must fuse across them")
}

func TestRRFSearchZeroValueCorpusPanics(t *testing.T) {
	assert.Panics(t, func() {
		RRFSearch(context.Background(), Corpus{}, `"q"`, `"q"`, "q", 5)
	}, "zero-value Corpus bypasses NewCorpus and must fail loud")
}

// --- Engine orchestration over a stub corpus ---

// stubCorpus returns a Corpus whose layers serve canned results and records
// every executed (table, query) pair for pass-count assertions. It sets the
// internal exec seam directly (bypassing NewCorpus, which would compose the
// SQL skeleton) so orchestration tests need no database. The engine runs the
// two layers concurrently, so the recorder must be mutex-guarded — real
// corpora get this safety from database/sql.
func stubCorpus(t *testing.T, byTable map[string][]SearchResult, fuzzy FuzzyCorrector, calls *[][2]string) Corpus {
	t.Helper()
	var mu sync.Mutex
	return Corpus{
		porterTable:  "chunks",
		trigramTable: "chunks_trigram",
		exec: func(ctx context.Context, table, ftsQuery string, limit int) []SearchResult {
			if calls != nil {
				mu.Lock()
				*calls = append(*calls, [2]string{table, ftsQuery})
				mu.Unlock()
			}
			return byTable[table]
		},
		fuzzy: fuzzy,
	}
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
	hit := SearchResult{SourceID: 1, Title: "Auth", Content: "authentication middleware", Table: "chunks", RowID: 42}
	served := false
	c := Corpus{
		porterTable:  "chunks",
		trigramTable: "chunks_trigram",
		exec: func(ctx context.Context, table, ftsQuery string, limit int) []SearchResult {
			// Serve the hit only for the corrected term.
			if table == "chunks" && strings.Contains(ftsQuery, "authentication") {
				served = true
				return []SearchResult{hit}
			}
			return nil
		},
		fuzzy: func(word string) string {
			if word == "authentcation" {
				return "authentication"
			}
			return ""
		},
	}

	results := SearchWithFallback(context.Background(), c, "authentcation", 5, 0)
	require.True(t, served, "corrected query should reach the corpus")
	require.NotEmpty(t, results, "fuzzy-corrected pass should surface results")
	assert.Contains(t, results[0].MatchLayer, "fuzzy+",
		"fuzzy-pass results must be tagged with the fuzzy+ layer prefix")
	assert.Equal(t, "chunks", results[0].Table, "the fuzzy merge keeps the row's location")
	assert.Equal(t, int64(42), results[0].RowID)
}

// TestRRFSearchWinnerCarriesItsLocation: when both layers return the same
// fusion key, the better-ranked version replaces the entry whole, so its
// content and location stay together.
func TestRRFSearchWinnerCarriesItsLocation(t *testing.T) {
	porter := SearchResult{SourceID: 1, Title: "Auth", Content: "porter body", Rank: -1, Table: "chunks", RowID: 5}
	trigram := SearchResult{SourceID: 1, Title: "Auth", Content: "trigram body", Rank: -2, Table: "chunks_trigram", RowID: 9}
	c := stubCorpus(t, map[string][]SearchResult{
		"chunks":         {porter},
		"chunks_trigram": {trigram},
	}, nil, nil)

	results := RRFSearch(context.Background(), c, `"auth"`, `"auth"`, "auth", 5)
	require.Len(t, results, 1)
	assert.Equal(t, "trigram body", results[0].Content)
	assert.Equal(t, "chunks_trigram", results[0].Table)
	assert.Equal(t, int64(9), results[0].RowID)
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
