package execution

import (
	"context"
	"unicode/utf8"

	"github.com/serpro69/capy/internal/runstore"
)

func (s *Service) Search(ctx context.Context, options runstore.SearchOptions) (runstore.SearchPage, error) {
	if err := options.Validate(); err != nil {
		return runstore.SearchPage{}, err
	}
	ctx, done, err := s.begin(ctx, false)
	if err != nil {
		return runstore.SearchPage{}, err
	}
	defer done()
	st, _, err := s.attached(ctx, false)
	if err != nil {
		return runstore.SearchPage{}, err
	}
	// Search needs a generation lease, but neither an execution slot nor a
	// newly created store. Owner publication shares the existing service lock.
	s.mu.Lock()
	if s.owner == nil {
		s.owner, err = st.ClaimOwner(ctx)
	}
	owner := s.owner
	s.mu.Unlock()
	if err != nil {
		return runstore.SearchPage{}, err
	}
	return st.Search(ctx, owner, options)
}

// BoundSearch only formats a completed search. Response budget retries must
// never perform more indexing, alter coverage, or spend a knowledge-search slot.
func BoundSearch(page runstore.SearchPage, allowance int) runstore.SearchPage {
	bounded := page
	budget := allowance
	// Keep most optional space for matches; a large manifest must not spend
	// the whole budget on coverage metadata and permanently hide every hit.
	count := min(len(page.Children), allowance/(4*512))
	bounded.Children = page.Children[:count]
	bounded.CoverageOmitted = page.CoverageOmitted || count < len(page.Children)
	allowance -= count * 512
	bounded.Results = make([]runstore.QueryHits, len(page.Results))
	// Reserve space for both explicitly labelled corpora in global queries.
	knowledgeAllowance := 0
	if len(page.Knowledge) > 0 {
		knowledgeAllowance = allowance / 2
		allowance -= knowledgeAllowance
	}
	for i, group := range page.Results {
		bounded.Results[i] = runstore.QueryHits{Query: group.Query, Hits: []runstore.SearchHit{}}
		for _, hit := range group.Hits {
			if allowance < 640 {
				bounded.Omitted = true
				continue
			}
			allowance -= 512
			n := min(len(hit.Text), allowance)
			for n > 0 && n < len(hit.Text) && !utf8.RuneStart(hit.Text[n]) {
				n--
			}
			if n < len(hit.Text) {
				bounded.Omitted = true
			}
			hit.Text = hit.Text[:n]
			allowance -= n
			bounded.Results[i].Hits = append(bounded.Results[i].Hits, hit)
		}
	}
	bounded.Knowledge = make([]runstore.KnowledgeHits, len(page.Knowledge))
	for i, group := range page.Knowledge {
		bounded.Knowledge[i] = runstore.KnowledgeHits{Query: group.Query, Hits: []runstore.KnowledgeHit{}}
		for _, hit := range group.Hits {
			if knowledgeAllowance < 256 {
				bounded.Omitted = true
				continue
			}
			original := hit
			hit.Source = SafeMessage(hit.Source, min(128, knowledgeAllowance/4))
			hit.Title = SafeMessage(hit.Title, min(128, knowledgeAllowance/4))
			hit.Text = SafeMessage(hit.Text, min(1024, knowledgeAllowance/2))
			knowledgeAllowance -= len(hit.Source) + len(hit.Title) + len(hit.Text) + 64
			if hit != original {
				bounded.Omitted = true
			}
			bounded.Knowledge[i].Hits = append(bounded.Knowledge[i].Hits, hit)
		}
	}
	if page.KnowledgeError != nil {
		e := *page.KnowledgeError
		e.Message = SafeMessage(e.Message, allowance)
		bounded.KnowledgeError = &e
	}
	if page.Error != nil {
		bounded.Error = BoundError(page.Error, allowance)
	}
	if page.Index.Error != nil {
		bounded.Index.Error = BoundError(page.Index.Error, allowance)
	}
	if budget < 128 && len(bounded.Results)+len(bounded.Knowledge) > 0 {
		bounded.Results = nil
		bounded.Knowledge = nil
		bounded.Omitted = true
	}
	return bounded
}
