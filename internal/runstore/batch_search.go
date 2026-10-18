package runstore

import (
	"context"
	"errors"
	"slices"
	"time"
)

type SearchCoverage struct {
	RunID           string           `json:"run_id"`
	Index           Index            `json:"index"`
	Bytes           map[string]int64 `json:"searched_bytes"`
	OutputAvailable bool             `json:"output_available"`
}

func (s *Store) searchBatch(ctx context.Context, owner *Owner, o SearchOptions) (SearchPage, error) {
	page := SearchPage{Version: 1, Kind: "query", BatchID: o.BatchID, QueryScope: o.QueryScope, Index: Index{State: "ready"}, Coverage: map[string]int64{}, Results: []QueryHits{}, Children: []SearchCoverage{}}
	_, queries, _, err := s.batchMetadata(ctx, o.BatchID)
	if err != nil {
		return page, err
	}
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
	// Snapshot only bounded identities; close rows before using the one-connection
	// pool for indexing. Results remain live retained coverage, not a DB snapshot.
	rows, err := s.db.QueryContext(ctx, "SELECT run_id FROM run_batch_children WHERE batch_id=? ORDER BY ordinal", o.BatchID)
	if err != nil {
		return page, storageError()
	}
	ids := make([]string, 0, 256)
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			rows.Close()
			return page, storageError()
		}
		ids = append(ids, id)
	}
	readErr, closeErr := rows.Err(), rows.Close()
	if readErr != nil || closeErr != nil {
		return page, storageError()
	}
	ranked := make([][]batchRankedHit, len(o.Queries))
	for i := range o.Queries {
		page.Results = append(page.Results, QueryHits{Query: i, Hits: []SearchHit{}})
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for _, id := range ids {
		if ctx.Err() != nil {
			page.Partial = true
			break
		}
		child, err := s.Search(ctx, owner, SearchOptions{RunID: id, Queries: o.Queries, Limit: o.Limit})
		coverage := SearchCoverage{RunID: id, Index: child.Index, Bytes: child.Coverage, OutputAvailable: true}
		if err != nil {
			page.Partial = true
			var typed *Error
			if errors.As(err, &typed) && typed.Code == "run_unavailable" {
				coverage.OutputAvailable = false
				coverage.Index.State = "not_requested"
			} else {
				page.Error = indexError(err)
				coverage.Index = Index{State: "failed", Error: page.Error}
			}
			page.Children = append(page.Children, coverage)
			continue
		}
		page.Children = append(page.Children, coverage)
		page.Partial = page.Partial || child.Partial
		if child.Error != nil {
			page.Error = child.Error
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
	if page.Partial {
		page.Index.State = "building"
	}
	if page.Error != nil {
		searchPageError(&page, page.Error)
	}
	if _, _, _, err := s.batchMetadata(ctx, o.BatchID); err != nil && ctx.Err() == nil {
		return page, err
	}
	return page, nil
}

// Reciprocal-rank ordering across child corpora; ties retain child ordinal.
type batchRankedHit struct {
	hit  SearchHit
	rank int
}
