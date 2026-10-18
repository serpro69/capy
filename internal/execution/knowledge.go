package execution

import (
	"context"
	"time"

	"github.com/serpro69/capy/internal/runstore"
	"github.com/serpro69/capy/internal/store"
)

// AddKnowledge keeps the two corpora explicitly separate. The adapter supplies
// the selected knowledge store lazily; batch-only searches never open it. Run
// recovery neither consumes nor depends on the capy_search throttle.
func AddKnowledge(ctx context.Context, page runstore.SearchPage, limit int, open func() *store.ContentStore) runstore.SearchPage {
	if page.QueryScope != "global" {
		return page
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	st := open()
	for i, query := range page.Queries {
		matches, err := st.SearchWithFallbackContext(ctx, query, limit, store.SearchOptions{IncludeKinds: []store.SourceKind{store.KindDurable, store.KindEphemeral}, FailOnError: true})
		if err != nil {
			page.Partial = true
			page.KnowledgeError = &runstore.Error{Code: "knowledge_unavailable", Message: "knowledge search unavailable; run results and reads remain available"}
			break
		}
		group := runstore.KnowledgeHits{Query: i, Hits: []runstore.KnowledgeHit{}}
		for _, hit := range matches {
			group.Hits = append(group.Hits, runstore.KnowledgeHit{Source: SafeMessage(hit.Label, 512), Title: SafeMessage(hit.Title, 256), Text: SafeMessage(hit.Content, 2048)})
		}
		page.Knowledge = append(page.Knowledge, group)
	}
	return page
}
