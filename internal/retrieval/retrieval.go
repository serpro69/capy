// Package retrieval implements the corpus-agnostic search pipeline shared by
// the knowledge store and the session vault: Reciprocal Rank Fusion across a
// porter-stemmed and a trigram FTS5 layer, synonym-expanded AND → flat-OR
// fallback orchestration, an optional fuzzy-correction pass, proximity/title
// reranking, per-source diversification, and entity boosting.
//
// The engine is parameterized by a Corpus, which supplies the corpus-specific
// pieces: the two FTS5 layer table names (validated against a fixed
// allowlist), an Exec callback that runs one layer's query (owning the DB
// handle, corpus-specific WHERE filters, and row mapping), and an optional
// FuzzyCorrector for corpora backed by a vocabulary table.
package retrieval

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// SearchResult is a single result from a search query. It lives here so both
// the knowledge store and the vault can share it without an import cycle
// (store imports retrieval, never the reverse).
type SearchResult struct {
	Label       string
	Title       string
	Content     string
	SourceID    int64
	ContentType string
	Highlighted string
	Rank        float64
	FusedScore  float64
	MatchLayer  string
}

// FuzzyCorrector corrects a single query word against a corpus vocabulary,
// returning the corrected word or "" when no correction applies.
type FuzzyCorrector func(word string) string

// SearchFunc executes one sanitized FTS5 MATCH query against the given layer
// table and returns BM25-ranked results. Implementations own the DB handle,
// corpus-specific filtering, and row mapping, and should honor ctx
// cancellation on their database calls.
type SearchFunc func(ctx context.Context, table, ftsQuery string, limit int) []SearchResult

// tableAllowlist enumerates every FTS5 table name a Corpus may search.
// Corpus Exec implementations interpolate the table name into SQL, so it must
// never come from user input; NewCorpus enforces membership here so even a
// miswired corpus cannot smuggle an arbitrary identifier.
var tableAllowlist = map[string]bool{
	"chunks":         true,
	"chunks_trigram": true,
}

// Corpus supplies the corpus-specific parts of a search. The zero value is
// not usable — construct with NewCorpus.
type Corpus struct {
	porterTable  string
	trigramTable string
	exec         SearchFunc
	fuzzy        FuzzyCorrector
}

// NewCorpus validates the layer table names against the fixed allowlist and
// returns a Corpus. fuzzy may be nil, which disables the fuzzy-correction
// pass for this corpus (corpora without a vocabulary table).
func NewCorpus(porterTable, trigramTable string, exec SearchFunc, fuzzy FuzzyCorrector) (Corpus, error) {
	if !tableAllowlist[porterTable] || !tableAllowlist[trigramTable] {
		return Corpus{}, fmt.Errorf("retrieval: corpus tables %q/%q not in the fixed allowlist", porterTable, trigramTable)
	}
	if porterTable == trigramTable {
		return Corpus{}, fmt.Errorf("retrieval: porter and trigram tables must be distinct, got %q for both", porterTable)
	}
	if exec == nil {
		return Corpus{}, errors.New("retrieval: corpus requires an Exec search function")
	}
	return Corpus{
		porterTable:  porterTable,
		trigramTable: trigramTable,
		exec:         exec,
		fuzzy:        fuzzy,
	}, nil
}

// SearchWithFallback runs Reciprocal Rank Fusion across the corpus's porter
// and trigram layers, with fuzzy correction as a secondary pass when results
// are sparse and the corpus supplies a FuzzyCorrector.
func SearchWithFallback(ctx context.Context, c Corpus, query string, limit, maxPerSource int) []SearchResult {
	// RRF pass 1: synonym-expanded query (implicit AND between groups).
	synPorter := SanitizePorterQuery(query, "AND", true)
	synTrigram := SanitizeTrigramQuery(query, "AND", true)
	results := RRFSearch(ctx, c, synPorter, synTrigram, query, limit)

	// Fallback: if synonym AND grouping returned zero results, retry with
	// flat OR using the user's original terms as a precision anchor.
	// Synonym expansion is intentionally dropped here to avoid relevance
	// dilution (e.g., "latency" expanding to "slow" in OR mode would drown
	// the user's intent with unrelated matches).
	if len(results) == 0 {
		flatPorter := SanitizePorterQuery(query, "OR", false)
		flatTrigram := SanitizeTrigramQuery(query, "OR", false)
		results = RRFSearch(ctx, c, flatPorter, flatTrigram, query, limit)
	}

	// RRF pass 2: fuzzy correction (only if pass 1 returned fewer than limit
	// and the corpus supplies a corrector — corpora without a vocabulary
	// table leave it nil and rely on the trigram layer for typo tolerance).
	// Corrected queries re-enter the synonym AND pass first — a typo like
	// "authentcation" corrected to "authentication" should get full synonym
	// expansion, not just flat OR.
	if c.fuzzy != nil && len(results) < limit {
		corrected := fuzzyCorrectQuery(c.fuzzy, query)
		if corrected != "" && corrected != query {
			// Try synonym AND on corrected query first.
			fzPorter := SanitizePorterQuery(corrected, "AND", true)
			fzTrigram := SanitizeTrigramQuery(corrected, "AND", true)
			fuzzyResults := RRFSearch(ctx, c, fzPorter, fzTrigram, corrected, limit)
			// If synonym AND on corrected query also returns nothing, fall back to flat OR.
			if len(fuzzyResults) == 0 {
				fzPorter = SanitizePorterQuery(corrected, "OR", false)
				fzTrigram = SanitizeTrigramQuery(corrected, "OR", false)
				fuzzyResults = RRFSearch(ctx, c, fzPorter, fzTrigram, corrected, limit)
			}
			for i := range fuzzyResults {
				fuzzyResults[i].MatchLayer = "fuzzy+" + fuzzyResults[i].MatchLayer
			}
			results = mergeRRFResults(results, fuzzyResults)
		}
	}

	// Per-source diversification: cap results from any single source,
	// then fill remaining slots with skipped results.
	if maxPerSource <= 0 {
		maxPerSource = 2
	}
	results = diversifyBySource(results, limit, maxPerSource)

	// Entity-aware boosting: extract quoted phrases and capitalized
	// identifiers from the original query, boost results that contain them.
	entities := ExtractEntities(query)
	results = BoostByEntities(results, entities)

	return results
}

const rrfK = 60 // standard RRF constant

// RRFSearch runs porter and trigram searches concurrently, fuses results
// using Reciprocal Rank Fusion, applies proximity reranking, and returns
// candidates. It accepts pre-sanitized FTS5 query strings for both
// layers so the caller can control synonym expansion vs flat-OR fallback.
// rawQuery is the original unsanitized query, used for proximity reranking.
// Exported for benchmark harnesses that measure the fusion stage in
// isolation from diversification and entity boosting.
//
// Note: RRFSearch intentionally does NOT truncate results to limit. It fetches
// limit*5 candidates per layer to give the caller (SearchWithFallback) a large
// enough pool for diversification and entity boosting. The caller is responsible
// for applying the final limit after post-processing.
func RRFSearch(ctx context.Context, c Corpus, porterQuery, trigramQuery, rawQuery string, limit int) []SearchResult {
	// Fail loud on a zero-value Corpus: it can only arise from bypassing
	// NewCorpus, which is a programmer error, not a runtime condition.
	if c.exec == nil {
		panic("retrieval: zero-value Corpus is not usable — construct with NewCorpus")
	}

	fetchLimit := max(limit*5, 10)

	// Run both layers concurrently — SQLite WAL supports concurrent readers.
	var porterResults, trigramResults []SearchResult
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if porterQuery != "" {
			porterResults = c.exec(ctx, c.porterTable, porterQuery, fetchLimit)
		}
	}()
	go func() {
		defer wg.Done()
		if trigramQuery != "" {
			trigramResults = c.exec(ctx, c.trigramTable, trigramQuery, fetchLimit)
		}
	}()
	wg.Wait()

	// Build fusion map keyed by (sourceID, title).
	type fusedEntry struct {
		result     SearchResult
		fusedScore float64
	}
	fusionMap := make(map[string]*fusedEntry)

	addLayer := func(results []SearchResult, layerName string) {
		for i, r := range results {
			key := fmt.Sprintf("%d:%s", r.SourceID, r.Title)
			score := 1.0 / (float64(rrfK) + float64(i))
			if entry, ok := fusionMap[key]; ok {
				entry.fusedScore += score
				// Keep the version with the better individual rank.
				if r.Rank < entry.result.Rank {
					entry.result = r
				}
			} else {
				r.MatchLayer = layerName
				fusionMap[key] = &fusedEntry{result: r, fusedScore: score}
			}
		}
	}

	addLayer(porterResults, "porter")
	addLayer(trigramResults, "trigram")

	// Flatten and sort by fused score descending.
	fused := make([]SearchResult, 0, len(fusionMap))
	for _, entry := range fusionMap {
		entry.result.FusedScore = entry.fusedScore
		fused = append(fused, entry.result)
	}
	sort.Slice(fused, func(i, j int) bool {
		if fused[i].FusedScore != fused[j].FusedScore {
			return fused[i].FusedScore > fused[j].FusedScore
		}
		if fused[i].SourceID != fused[j].SourceID {
			return fused[i].SourceID < fused[j].SourceID
		}
		return fused[i].Title < fused[j].Title
	})

	// Tag multi-layer hits. A result appearing in both layers scores above
	// the single-layer max of 1/(60+0) ≈ 0.01667.
	singleLayerMax := 1.0 / float64(rrfK)
	for i := range fused {
		if fused[i].FusedScore > singleLayerMax {
			fused[i].MatchLayer = "rrf(porter+trigram)"
		}
	}

	fused = rerank(fused, rawQuery)

	return fused
}

// mergeRRFResults deduplicates primary and secondary results by (sourceID, title).
// On conflict, the primary version is kept. Does not truncate — the caller
// applies diversification and final limit.
func mergeRRFResults(primary, secondary []SearchResult) []SearchResult {
	seen := make(map[string]bool, len(primary))
	for _, r := range primary {
		key := fmt.Sprintf("%d:%s", r.SourceID, r.Title)
		seen[key] = true
	}

	merged := make([]SearchResult, len(primary))
	copy(merged, primary)

	for _, r := range secondary {
		key := fmt.Sprintf("%d:%s", r.SourceID, r.Title)
		if !seen[key] {
			seen[key] = true
			merged = append(merged, r)
		}
	}

	return merged
}

// diversifyBySource caps results from any single source to avoid a dominant
// source drowning out others. Two-pass: first pass enforces the per-source cap,
// second pass fills remaining slots with previously skipped results.
func diversifyBySource(results []SearchResult, limit, maxPerSource int) []SearchResult {
	if len(results) == 0 {
		return results
	}

	selected := make([]SearchResult, 0, min(limit, len(results)))
	var skipped []SearchResult
	counts := make(map[int64]int)

	// Pass 1: accept results in rank order, skip when source exceeds cap.
	for _, r := range results {
		if len(selected) >= limit {
			return selected
		}
		if counts[r.SourceID] >= maxPerSource {
			skipped = append(skipped, r)
			continue
		}
		counts[r.SourceID]++
		selected = append(selected, r)
	}

	// Pass 2: fill remaining slots with skipped results.
	for _, r := range skipped {
		if len(selected) >= limit {
			break
		}
		selected = append(selected, r)
	}

	return selected
}

// fuzzyCorrectQuery corrects each word in the query using the corpus-supplied
// corrector. Stopwords and words shorter than 3 characters pass through
// unchanged. Returns the corrected query, or "" if no correction was made.
func fuzzyCorrectQuery(correct FuzzyCorrector, query string) string {
	cleaned := ftsSpecialRe.ReplaceAllString(strings.ToLower(query), " ")
	words := strings.Fields(cleaned)
	corrected := false
	var result []string

	for _, word := range words {
		word = strings.Trim(word, ".,!?;:")
		if len(word) < 3 {
			result = append(result, word)
			continue
		}
		if IsStopword(word) {
			result = append(result, word)
			continue
		}
		fix := correct(word)
		if fix != "" {
			result = append(result, fix)
			corrected = true
		} else {
			result = append(result, word)
		}
	}

	if !corrected {
		return ""
	}
	return strings.Join(result, " ")
}
