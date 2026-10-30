package runstore

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/serpro69/capy/internal/executor"
)

type SearchCoverage struct {
	Recovery        *Recovery                `json:"recovery,omitempty"`
	Cleanup         executor.CleanupEvidence `json:"cleanup"`
	RunID           string                   `json:"run_id"`
	Index           Index                    `json:"index"`
	Bytes           map[string]int64         `json:"searched_bytes"`
	OutputAvailable bool                     `json:"output_available"`
}

func (s *Store) searchBatch(ctx context.Context, owner *Owner, o SearchOptions) (SearchPage, error) {
	page := SearchPage{Version: EnvelopeVersion, Kind: "query", BatchID: o.BatchID, QueryScope: o.QueryScope, Index: Index{State: "ready"}, Coverage: map[string]int64{}, Results: []QueryHits{}, Children: []SearchCoverage{}}
	batch, queries, children, err := s.batchSnapshot(ctx, o.BatchID)
	if err != nil {
		return page, err
	}
	page.Cleanup = batch.Cleanup
	page.Recovery = batch.Recovery
	if page.QueryScope == "" {
		page.QueryScope = "batch"
	}
	if len(o.Queries) == 0 {
		o.Queries = queries
	}
	if len(o.Queries) == 0 {
		return page, failure("invalid_argument", "batch has no stored queries; supply queries")
	}
	page.Queries = append([]string(nil), o.Queries...)
	// Proof and retained-row comparisons were snapshotted together above; the
	// transaction is closed before indexing uses the one-connection pool.
	ranked := make([][]batchRankedHit, len(o.Queries))
	for i := range o.Queries {
		page.Results = append(page.Results, QueryHits{Query: i, Hits: []SearchHit{}})
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	for _, proof := range children {
		id := proof.id
		if ctx.Err() != nil {
			page.Partial = true
			page.CoverageOmitted = true
			break
		}
		if !proof.outputAvailable {
			page.Partial = true
			page.Children = append(page.Children, SearchCoverage{Recovery: proof.result.Recovery, Cleanup: proof.result.Cleanup, RunID: id, Index: Index{State: "not_requested"}, Bytes: map[string]int64{}})
			continue
		}
		child, err := s.Search(ctx, owner, SearchOptions{RunID: id, Queries: o.Queries, Limit: o.Limit})
		coverage := SearchCoverage{Recovery: proof.result.Recovery, Cleanup: proof.result.Cleanup, RunID: id, Index: child.Index, Bytes: child.Coverage, OutputAvailable: true}
		if err != nil {
			page.Partial = true
			var typed *Error
			if errors.As(err, &typed) && typed.Code == "run_unavailable" {
				coverage.OutputAvailable = false
				coverage.Index.State = "not_requested"
			} else {
				problem := indexError(err)
				batchSearchError(&page, problem)
				coverage.Index = Index{State: "failed", Error: problem}
			}
			page.Children = append(page.Children, coverage)
			continue
		}
		coverage.Recovery = child.Recovery
		page.Children = append(page.Children, coverage)
		page.Partial = page.Partial || child.Partial
		if child.Error != nil {
			batchSearchError(&page, child.Error)
		}
		for i, group := range child.Results {
			for rank, hit := range group.Hits {
				ranked[i] = append(ranked[i], batchRankedHit{hit: hit, rank: rank})
			}
			slices.SortStableFunc(ranked[i], func(a, b batchRankedHit) int { return a.rank - b.rank })
			if len(ranked[i]) > o.Limit {
				page.Omitted = true
				clear(ranked[i][o.Limit:])
				ranked[i] = ranked[i][:o.Limit]
			}
		}

	}
	for i, hits := range ranked {
		for _, hit := range hits {
			page.Results[i].Hits = append(page.Results[i].Hits, hit.hit)
		}
	}
	// A later child can consume the remaining budget or race TTL/quota eviction
	// of an earlier child. Recheck all retained subjects before publishing hits;
	// missing output is partial/unavailable, never authoritative zero matches.
	// Reserve a separate bounded metadata read after the index-work budget.
	// The caller's deadline/cancellation still applies. If unavailable, fail
	// explicitly instead of publishing stale output-availability assertions.
	recheckCtx, stop := context.WithTimeout(parent, time.Second)
	defer stop()
	if err := s.recheckBatchSearch(recheckCtx, o.BatchID, &page); err != nil {
		return page, err
	}
	if page.Partial {
		page.Index.State = "partial"
		if page.CoverageOmitted {
			page.Index.State = "building"
		}
		for _, child := range page.Children {
			if child.OutputAvailable && child.Index.State == "building" {
				page.Index.State = "building"
			}
		}
	}
	if page.Error != nil {
		searchPageError(&page, page.Error)
	}
	return page, nil
}

func batchSearchError(page *SearchPage, problem *Error) {
	// Keep actionable store-recovery guidance when a later child's local index
	// failure is less informative; each child's own error stays in coverage.
	if page.Error == nil || page.Error.RecoveryHint == nil {
		page.Error = problem
	}
}

func (s *Store) recheckBatchSearch(ctx context.Context, id string, page *SearchPage) error {
	batch, _, children, err := s.batchSnapshot(ctx, id)
	if err != nil {
		return err
	}
	page.Recovery = batch.Recovery
	available := make(map[string]bool, len(children))
	for _, child := range children {
		available[child.id] = child.outputAvailable
	}
	for i := range page.Children {
		coverage := &page.Children[i]
		if !available[coverage.RunID] {
			coverage.OutputAvailable = false
			coverage.Index = Index{State: "not_requested"}
			page.Partial = true
		}
	}
	for i := range page.Results {
		page.Results[i].Hits = slices.DeleteFunc(page.Results[i].Hits, func(hit SearchHit) bool { return !available[hit.RunID] })
	}
	return nil
}

// Reciprocal-rank ordering across child corpora; ties retain child ordinal.
type batchRankedHit struct {
	hit  SearchHit
	rank int
}
