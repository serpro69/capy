package runstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/serpro69/capy/internal/retrieval"
	"github.com/serpro69/capy/internal/sanitize"
	"github.com/serpro69/capy/internal/sqliteutil"
)

type SearchOptions struct {
	RunID      string
	BatchID    string
	Queries    []string
	Limit      int
	QueryScope string
}

func (o SearchOptions) Validate() error {
	if (o.RunID == "") == (o.BatchID == "") || (o.RunID != "" && uuid.Validate(o.RunID) != nil) || (o.BatchID != "" && uuid.Validate(o.BatchID) != nil) {
		return failure("invalid_argument", "search requires exactly one run_id or batch_id UUID")
	}
	if o.QueryScope != "" && (o.BatchID == "" || (o.QueryScope != "batch" && o.QueryScope != "global")) {
		return failure("invalid_argument", "query_scope is batch or global and requires batch_id")
	}
	if o.Limit < 1 || o.Limit > 20 || len(o.Queries) > 8 || (len(o.Queries) == 0 && o.RunID != "") {
		return failure("invalid_argument", "search requires 1–8 queries and a result limit between 1 and 20")
	}
	for _, query := range o.Queries {
		if len(query) > 512 || strings.TrimSpace(query) == "" || !utf8.ValidString(query) {
			return failure("invalid_argument", "queries must be nonblank UTF-8 strings of at most 512 bytes")
		}
	}
	return nil
}

type SearchHit struct {
	RunID      string `json:"run_id"`
	Stream     string `json:"stream"`
	Phase      string `json:"phase"`
	RawStart   int64  `json:"raw_start"`
	RawEnd     int64  `json:"raw_end"`
	StartLine  int64  `json:"start_line"`
	ReadCursor string `json:"read_cursor"`
	Text       string `json:"text"`
	MatchLayer string `json:"match_layer"`
}

type QueryHits struct {
	Query int         `json:"query"` // zero-based input ordinal; do not echo possible secrets
	Hits  []SearchHit `json:"hits"`
}

type SearchPage struct {
	Version  int              `json:"version"`
	Kind     string           `json:"kind"`
	RunID    string           `json:"run_id,omitempty"`
	Index    Index            `json:"index"`
	Coverage map[string]int64 `json:"searched_bytes"` // raw per-stream prefix positions
	Partial  bool             `json:"partial"`
	Results  []QueryHits      `json:"results"`
	Omitted  bool             `json:"results_omitted,omitempty"`
	Error    *Error           `json:"error,omitempty"`
}

// Search performs one bounded index batch, then ranks retained coverage. An
// index failure is represented in the page, preserving usable earlier matches;
// it can never be confused with an authoritative absence of matches.
func (s *Store) Search(ctx context.Context, owner *Owner, options SearchOptions) (page SearchPage, returnedErr error) {
	page = SearchPage{Version: 1, Kind: "query", RunID: options.RunID, Results: []QueryHits{}, Coverage: map[string]int64{}}
	if err := options.Validate(); err != nil {
		return page, err
	}
	if options.BatchID != "" {
		// Batch manifests are introduced by Task 12. Never treat a batch UUID
		// as a run or widen it to unscoped knowledge/run search.
		return page, failure("batch_unavailable", "batch output is unavailable in this owner's store")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	row, err := s.Load(ctx, options.RunID)
	if err != nil {
		return page, err
	}
	if row.Result.Redaction.State == "unavailable" || row.Result.Redaction.Version != sanitize.StreamVersion {
		return page, failure("redaction_unavailable", "sanitized output is unavailable; explicit raw reads remain independent")
	}
	var claimed bool
	err = s.retryIndexRelease(ctx, options.RunID)
	if err == nil {
		claimed, err = s.ClaimIndex(ctx, owner, options.RunID)
	}
	if err != nil {
		page.Error = indexError(err)
	}
	if claimed {
		defer func() {
			releaseCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), WriteTimeout)
			defer stop()
			if err := s.ReleaseIndex(releaseCtx, owner, options.RunID); err != nil {
				s.rememberIndexRelease(owner, options.RunID)
				searchPageError(&page, indexError(err))
			}
		}()
		if err := s.indexBatch(ctx, owner, options.RunID); err != nil {
			page.Error = indexError(err)
			if saveErr := s.failIndex(ctx, owner, options.RunID, page.Error); saveErr != nil {
				page.Error = indexError(saveErr)
			}
		}
	}
	row, err = s.Load(ctx, options.RunID)
	if err != nil {
		return page, err
	}
	page.Index = row.Result.Index
	page.Partial = page.Index.State != "ready"
	if page.Error != nil {
		searchPageError(&page, page.Error)
	}
	if !claimed {
		page.Partial = true
	}
	rows, err := s.db.QueryContext(ctx, "SELECT stream,covered FROM run_search_progress WHERE run_id=?", options.RunID)
	if err != nil {
		return page, storageError()
	}
	for rows.Next() {
		var stream string
		var covered int64
		if err := rows.Scan(&stream, &covered); err != nil {
			rows.Close()
			return page, storageError()
		}
		page.Coverage[stream] = covered
	}
	rowErr, closeErr := rows.Err(), rows.Close()
	if rowErr != nil || closeErr != nil {
		return page, storageError()
	}
	var mu sync.Mutex
	var layerErr error
	corpus, err := retrieval.NewCorpus(retrieval.CorpusConfig{
		DB: s.db, PorterTable: "run_search_fts", TrigramTable: "run_search_trigram", TitleWeight: 1,
		SelectColumns: "w.id,c.title,c.content,w.stream,w.raw_start,w.raw_end,w.seq,w.phase,w.start_line",
		Join:          "JOIN run_search_windows w ON w.id=c.rowid JOIN runs r ON r.id=w.run_id",
		FilterSQL:     " AND w.run_id=? AND r.evicting=0 AND (r.expires_at IS NULL OR r.expires_at>?) AND w.raw_end<=CASE w.stream WHEN 'stdout' THEN ? ELSE ? END",
		FilterParams:  []any{options.RunID, time.Now().UTC().UnixMilli(), page.Coverage["stdout"], page.Coverage["stderr"]},
		OnError:       func(err error) { mu.Lock(); layerErr = errors.Join(layerErr, err); mu.Unlock() },
		MapRow: func(rows *sql.Rows) (retrieval.SearchResult, error) {
			var result retrieval.SearchResult
			hit := SearchHit{RunID: options.RunID}
			var seq uint64
			err := rows.Scan(&result.SourceID, &result.Title, &result.Content, &hit.Stream, &hit.RawStart, &hit.RawEnd, &seq, &hit.Phase, &hit.StartLine, &result.Highlighted, &result.Rank)
			hit.ReadCursor = s.projectionCursor(options.RunID, hit.Stream, seq, hit.RawStart, 0)
			result.Meta = hit
			return result, err
		},
	})
	if err != nil {
		return page, storageError()
	}
	for i, query := range options.Queries {
		matches := retrieval.SearchWithFallback(ctx, corpus, query, options.Limit, options.Limit)
		if layerErr != nil {
			page.Error = &Error{Code: "index_search_failed", Message: "an index layer could not be searched; read retained output or retry search"}
			page.Partial = true
			page.Results = nil
			page.Index.State, page.Index.Error = "failed", page.Error
			if claimed {
				if err := s.failIndex(ctx, owner, options.RunID, page.Error); err != nil {
					page.Error = indexError(err)
				}
			}
			return page, nil
		}
		group := QueryHits{Query: i, Hits: []SearchHit{}}
		for _, match := range matches {
			hit, ok := match.Meta.(SearchHit)
			if !ok {
				return page, storageError()
			}
			hit.Text, hit.MatchLayer = match.Content, match.MatchLayer
			group.Hits = append(group.Hits, hit)
		}
		page.Results = append(page.Results, group)
	}
	// Expiry/eviction and projection invalidation remain authoritative, even
	// when they raced the individual layer reads.
	current, err := s.Load(ctx, options.RunID)
	if err != nil {
		return page, err
	}
	if current.Result.Redaction.State == "unavailable" {
		return page, failure("redaction_unavailable", "sanitized output became unavailable")
	}
	return page, nil
}

func searchPageError(page *SearchPage, problem *Error) {
	page.Error = problem
	page.Partial = true
	page.Index.State, page.Index.Error = "failed", problem
	if problem.Code == "index_quota_blocked" {
		page.Index.State = "quota_blocked"
	}
}

func indexError(err error) *Error {
	var typed *Error
	if errors.As(err, &typed) && (typed.Code == "index_quota_blocked" || typed.Code == "redaction_unavailable") {
		return typed
	}
	return &Error{Code: "index_failed", Message: "index work was interrupted or unavailable; retry search to resume retained coverage"}
}

func (s *Store) failIndex(ctx context.Context, owner *Owner, id string, problem *Error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), WriteTimeout)
	defer cancel()
	tx, err := sqliteutil.BeginImmediateContext(ctx, s.db, "runs")
	if err != nil {
		return storageError()
	}
	defer tx.Rollback()
	var claimed bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM run_work_claims WHERE run_id=? AND slot=? AND generation=?)", id, owner.Slot, owner.ID).Scan(&claimed); err != nil || !claimed {
		return storageError()
	}
	row, err := loadRun(ctx, tx, id)
	if err != nil {
		return err
	}
	row.Result.Index.State, row.Result.Index.Error = "failed", problem
	if problem.Code == "index_quota_blocked" {
		row.Result.Index.State = "quota_blocked"
	}
	if err := saveIndexResult(ctx, tx, row); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return storageError()
	}
	return nil
}
