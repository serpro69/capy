# Implementation Plan: Synonym-Aware Proximity Reranking

**Design doc:** [design.md → Addendum, Improvement A](./design.md#improvement-a-synonym-aware-proximity-reranking)
**Task list:** [tasks-v2.md](./tasks-v2.md)

## Overview

Modify the proximity reranking system to recognize synonym variants when computing term proximity. Currently, `proximityRerank` uses raw query terms literally — a query `"k8s config"` won't get proximity boost from content containing `"kubernetes configuration"` because `"k8s"` doesn't appear in the content.

The fix expands each query term into a synonym group and matches any variant in the group when computing proximity positions.

## Files to modify

- `internal/store/search.go` — `proximityRerank`, `findMinSpanFromHighlights`, content fallback

## Implementation details

### `proximityRerank` (search.go:237)

**Current:** Tokenizes `rawQuery` into `words []string`, passes to `findMinSpanFromHighlights` and the content fallback.

**Change:** After tokenizing, build term groups by expanding each word:

```go
// Build term groups: each group is [original, syn1, syn2, ...].
termGroups := make([][]string, len(words))
for i, w := range words {
    if syns := ExpandSynonyms(w); len(syns) > 0 {
        group := make([]string, 0, len(syns)+1)
        group = append(group, w)
        group = append(group, syns...)
        termGroups[i] = group
    } else {
        termGroups[i] = []string{w}
    }
}
```

Pass `termGroups` (instead of `words`) to both the highlights path and content fallback. The `len(words) < 2` guard stays — it checks query term count, not synonym count.

### `findMinSpanFromHighlights` (search.go:289)

**Current signature:** `findMinSpanFromHighlights(highlighted string, terms []string) int`

**New signature:** `findMinSpanFromHighlights(highlighted string, termGroups [][]string) int`

**Change:** The inner match loop currently checks each term against the highlighted span. Change to check each group:

```go
// Current:
for i, term := range terms {
    if strings.Contains(matched, term) {
        posLists[i] = append(posLists[i], strippedPos)
    }
}

// New:
for i, group := range termGroups {
    for _, term := range group {
        if strings.Contains(matched, term) {
            posLists[i] = append(posLists[i], strippedPos)
            break // one match per group per position is enough
        }
    }
}
```

The `break` after the first match within a group prevents duplicate positions when multiple synonyms match the same highlighted span (e.g., content has `"kubernetes"` and the group contains both `"kube"` and `"kubernetes"` — both are substrings).

The `posLists` allocation changes from `make([][]int, len(terms))` to `make([][]int, len(termGroups))`. The rest of the function (position tracking, `findMinSpan` call) stays the same.

### Content fallback in `proximityRerank` (search.go:255-269)

**Current:** Finds positions of each word in lowercased content, then calls `findMinSpan`.

**Change:** For each group, find positions of all terms in the group, merge into a single sorted list:

```go
posLists := make([][]int, len(termGroups))
content := strings.ToLower(r.Content)
allFound := true
for j, group := range termGroups {
    var merged []int
    for _, term := range group {
        merged = append(merged, findAllPositions(content, term)...)
    }
    if len(merged) == 0 {
        allFound = false
        break
    }
    sort.Ints(merged)
    posLists[j] = merged
}
```

**Important:** `sort.Ints(merged)` is required because `findMinSpan`'s sweep-line algorithm assumes sorted position lists. Positions from different terms within the same group may interleave.

### Functions NOT changed

- `findMinSpan` — unchanged, already works with `[][]int` position lists
- `findAllPositions` — unchanged, still returns positions for a single term
- `ExpandSynonyms` — already exists, returns synonyms excluding the original term

## Testing

Existing tests for `proximityRerank` and `findMinSpanFromHighlights` need signature updates (`[]string` → `[][]string`). Each existing term becomes a single-element group `[]string{term}` — behavior is identical.

New tests:

- `TestProximityRerankWithSynonyms` — index content with "kubernetes configuration", search "k8s config", verify proximity boost is applied (FusedScore increases)
- `TestProximityRerankSynonymHighlights` — verify `findMinSpanFromHighlights` matches synonym variants in highlighted spans
- `TestProximityRerankSynonymContentFallback` — verify content fallback finds positions via synonym variants when highlights are empty
- `TestProximityRerankMixedTerms` — query with one synonym term and one non-synonym term (e.g., "k8s search") — verify partial expansion works
- `TestProximityRerankNoSynonymPassthrough` — query with no synonym terms behaves identically to before
