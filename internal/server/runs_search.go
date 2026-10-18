package server

import (
	"context"

	"github.com/serpro69/capy/internal/execution"
	"github.com/serpro69/capy/internal/runstore"
)

func (s *Server) searchRuns(ctx context.Context, options runstore.SearchOptions) (runstore.SearchPage, error) {
	page, err := s.executionService().Search(ctx, options)
	if err != nil {
		return page, err
	}
	return execution.AddKnowledge(ctx, page, options.Limit, s.getStore), nil
}
