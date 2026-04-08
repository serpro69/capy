package store

import (
	"strings"
	"testing"
)

func TestExtractQuotedPhrases(t *testing.T) {
	entities := ExtractEntities(`"React useEffect" cleanup`)
	if len(entities) != 1 {
		t.Fatalf("expected 1 entity, got %d: %v", len(entities), entities)
	}
	if entities[0] != "React useEffect" {
		t.Errorf("expected 'React useEffect', got %q", entities[0])
	}
}

func TestExtractCapitalizedIdentifiers(t *testing.T) {
	entities := ExtractEntities("ContentStore FTS5 search")
	expected := map[string]bool{"ContentStore": true, "FTS5": true}
	for _, e := range entities {
		if !expected[e] {
			t.Errorf("unexpected entity %q", e)
		}
		delete(expected, e)
	}
	for e := range expected {
		t.Errorf("missing entity %q", e)
	}
}

func TestExtractFiltersStopWords(t *testing.T) {
	entities := ExtractEntities("The ContentStore")
	if len(entities) != 1 {
		t.Fatalf("expected 1 entity, got %d: %v", len(entities), entities)
	}
	if entities[0] != "ContentStore" {
		t.Errorf("expected 'ContentStore', got %q", entities[0])
	}
}

func TestExtractNoEntities(t *testing.T) {
	entities := ExtractEntities("database migration")
	if len(entities) != 0 {
		t.Errorf("expected no entities, got %v", entities)
	}
}

func TestExtractDeduplicates(t *testing.T) {
	entities := ExtractEntities(`ContentStore uses ContentStore internally`)
	count := 0
	for _, e := range entities {
		if e == "ContentStore" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected ContentStore once, got %d times in %v", count, entities)
	}
}

func TestExtractSentenceStarter(t *testing.T) {
	entities := ExtractEntities("Getting started with deploy")
	if len(entities) != 0 {
		t.Errorf("expected no entities (sentence starter excluded), got %v", entities)
	}
}

func TestExtractCamelCaseNotFiltered(t *testing.T) {
	entities := ExtractEntities("GetConfig options")
	if len(entities) != 1 {
		t.Fatalf("expected 1 entity, got %d: %v", len(entities), entities)
	}
	if entities[0] != "GetConfig" {
		t.Errorf("expected 'GetConfig', got %q", entities[0])
	}
}

func TestExtractSentenceStarterWithMultipleIdentifiers(t *testing.T) {
	// When there are multiple capitalized words, only the first (if it looks
	// like a sentence starter) should be excluded.
	entities := ExtractEntities("Find ContentStore methods")
	// "Find" is position 0, no identifier signal → excluded
	// "ContentStore" has camelCase → included
	found := false
	for _, e := range entities {
		if e == "Find" {
			t.Error("sentence starter 'Find' should have been excluded")
		}
		if e == "ContentStore" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected ContentStore in entities, got %v", entities)
	}
}

func TestBoostWithEntities(t *testing.T) {
	results := []SearchResult{
		{Content: "The ContentStore handles indexing", FusedScore: 1.0, SourceID: 1},
		{Content: "some unrelated content here", FusedScore: 0.8, SourceID: 2},
	}
	entities := []string{"ContentStore"}
	boosted := BoostByEntities(results, entities)

	// 1.0 * (1 + 0.3*1) = 1.3 > 0.8, so ContentStore result should be first.
	if boosted[0].SourceID != 1 {
		t.Errorf("expected ContentStore result (source 1) first after boost, got source %d", boosted[0].SourceID)
	}
	if boosted[0].FusedScore != 1.3 {
		t.Errorf("expected boosted score 1.3, got %f", boosted[0].FusedScore)
	}
}

func TestBoostNoEntitiesPassthrough(t *testing.T) {
	results := []SearchResult{
		{Content: "first", FusedScore: 2.0, SourceID: 1},
		{Content: "second", FusedScore: 1.0, SourceID: 2},
	}
	boosted := BoostByEntities(results, nil)
	if boosted[0].FusedScore != 2.0 || boosted[1].FusedScore != 1.0 {
		t.Error("expected scores unchanged with nil entities")
	}

	boosted = BoostByEntities(results, []string{})
	if boosted[0].FusedScore != 2.0 || boosted[1].FusedScore != 1.0 {
		t.Error("expected scores unchanged with empty entities")
	}
}

func TestBoostResortsResults(t *testing.T) {
	results := []SearchResult{
		{Content: "no match here at all", FusedScore: 2.0, SourceID: 1},
		{Content: "ContentStore is the main store", FusedScore: 1.0, SourceID: 2},
	}
	entities := []string{"ContentStore"}
	boosted := BoostByEntities(results, entities)

	// 1.0 * 1.3 = 1.3 < 2.0, so order unchanged but score should be boosted.
	if boosted[1].FusedScore != 1.3 {
		t.Errorf("expected boosted score 1.3, got %f", boosted[1].FusedScore)
	}

	// Use a bigger gap to verify re-sort actually happens.
	results2 := []SearchResult{
		{Content: "no match here", FusedScore: 1.1, SourceID: 1},
		{Content: "ContentStore is the main ContentStore store", FusedScore: 1.0, SourceID: 2},
	}
	boosted = BoostByEntities(results2, entities)
	// 1.0 * (1 + 0.3*2) = 1.6 > 1.1, so ContentStore result should be first.
	if boosted[0].SourceID != 2 {
		t.Errorf("expected ContentStore result (source 2) first after re-sort, got source %d", boosted[0].SourceID)
	}
}

func TestBoostWordBoundary(t *testing.T) {
	results := []SearchResult{
		{Content: "the sandbox environment is ready", FusedScore: 1.0, SourceID: 1},
	}
	entities := []string{"DB"}
	boosted := BoostByEntities(results, entities)
	if boosted[0].FusedScore != 1.0 {
		t.Errorf("expected no boost (DB should not match inside sandbox), got %f", boosted[0].FusedScore)
	}
}

func TestBoostMatchCountCapped(t *testing.T) {
	// Create content with entity repeated many times.
	content := strings.Repeat("ContentStore ", 50)
	results := []SearchResult{
		{Content: content, FusedScore: 1.0, SourceID: 1},
	}
	entities := []string{"ContentStore"}
	boosted := BoostByEntities(results, entities)
	// Max boost: 1.0 * (1 + 0.3*5) = 2.5
	if boosted[0].FusedScore != 2.5 {
		t.Errorf("expected capped boost of 2.5, got %f", boosted[0].FusedScore)
	}
}

func TestBoostMultiWordCaseInsensitive(t *testing.T) {
	results := []SearchResult{
		{Content: "Use react useeffect for side effects", FusedScore: 1.0, SourceID: 1},
	}
	entities := []string{"React useEffect"}
	boosted := BoostByEntities(results, entities)
	// "react useeffect" should match case-insensitively.
	if boosted[0].FusedScore <= 1.0 {
		t.Errorf("expected boost for case-insensitive multi-word match, got %f", boosted[0].FusedScore)
	}
}

func TestExtractMixedQuotedAndIdentifiers(t *testing.T) {
	entities := ExtractEntities(`"React useEffect" ContentStore cleanup`)
	expected := map[string]bool{"React useEffect": true, "ContentStore": true}
	for _, e := range entities {
		if !expected[e] {
			t.Errorf("unexpected entity %q", e)
		}
		delete(expected, e)
	}
	for e := range expected {
		t.Errorf("missing entity %q", e)
	}
}
