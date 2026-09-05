package vault

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/serpro69/capy/internal/retrieval"
)

// chunk_search.go runs the shared retrieval engine (internal/retrieval) over
// the vault's chunk-granularity corpus (vault_chunks / vault_chunks_trigram,
// built by chunker.go) — design docs/wip/vault-session-search §D5. The
// per-line Search over vault_fts (store.go) is unrelated and untouched: it
// keeps the line_index/role anchors the TUI and `capy vault search --role`
// depend on.

// chunkTitleWeight mirrors the knowledge store's default BM25 title weight
// (store.NewContentStore) so a title match boosts identically in both corpora.
const chunkTitleWeight = 2.0

const (
	// chunkSnippetContext bounds the snippet window: this many bytes on each
	// side of the first highlighted match (or twice it for match-less head
	// snippets — e.g. a title-column match, whose content highlight yields no
	// markers, so chunkSnippet falls back to the head of the content).
	chunkSnippetContext = 100

	// stx/etx are the FTS5 highlight markers the shared query skeleton embeds
	// (highlight(<table>, 1, char(2), char(3)) — retrieval.CorpusConfig).
	stx = '\x02'
	etx = '\x03'
)

// chunkMeta rides retrieval.SearchResult.Meta through the shared engine: the
// navigation anchors and session metadata the engine has no slots for. Every
// row scanned by scanChunkSearchRow carries one.
type chunkMeta struct {
	subagentID     string
	firstLineIndex int
	sessionTitle   string
	projectPath    string
	endTime        time.Time
}

// SearchChunks runs a query through the vault chunk corpus via the shared
// retrieval engine: RRF across the porter and trigram layers, synonym-AND →
// flat-OR fallback, proximity rerank, per-session diversification, and entity
// boosting. There is no fuzzy-correction pass — the vault has no vocabulary
// table, so the corpus supplies a nil FuzzyCorrector and relies on the
// trigram layer for typo/substring tolerance (design §D1; adding a vault
// vocabulary is deferred until benchmark A2 shows the delta is unacceptable).
//
// Honored options: Query, Project (substring match on project_path), After /
// Before (on session end_time), Limit (default 20). Role and Raw are rejected
// loudly: role is undefined at chunk granularity (Not Doing), and the
// retrieval engine owns query sanitization, so raw FTS5 syntax cannot be
// passed through.
func (s *VaultStore) SearchChunks(ctx context.Context, opts SearchOptions) ([]SearchResult, error) {
	if opts.Role != "" {
		return nil, errors.New("vault: chunk search does not support role filtering — a semantic chunk spans mixed roles; use per-line search (capy vault search --role)")
	}
	if opts.Raw {
		return nil, errors.New("vault: chunk search does not support raw FTS5 query syntax — the retrieval engine owns query sanitization; use per-line search (capy vault search --raw)")
	}
	if strings.TrimSpace(opts.Query) == "" {
		return nil, nil
	}

	db, err := s.getDB(ctx)
	if err != nil {
		return nil, err
	}

	var filter strings.Builder
	var params []any
	if opts.Project != "" {
		filter.WriteString(" AND s.project_path LIKE ?")
		params = append(params, "%"+opts.Project+"%")
	}
	if !opts.After.IsZero() {
		filter.WriteString(" AND s.end_time >= ?")
		params = append(params, writeTime(opts.After))
	}
	if !opts.Before.IsZero() {
		filter.WriteString(" AND s.end_time <= ?")
		params = append(params, writeTime(opts.Before))
	}

	corpus, err := retrieval.NewCorpus(retrieval.CorpusConfig{
		DB:           db,
		PorterTable:  "vault_chunks",
		TrigramTable: "vault_chunks_trigram",
		// s.rowid is the SourceID: RRF fuses on (SourceID, Title) and
		// diversification caps hits per SourceID, so per-source caps become
		// per-session caps — the vault analogue of knowledge.db's per-source
		// diversification.
		// vault_session_names is joined for display-title resolution only — the
		// name text is not in either chunk FTS table, so it never matches.
		SelectColumns: "c.session_uuid, c.title, c.content_text, s.rowid, " +
			"c.subagent_id, c.first_line_index, s.title, n.custom_title, s.project_path, s.end_time",
		Join: "JOIN vault_sessions s ON s.uuid = c.session_uuid " +
			"LEFT JOIN vault_session_names n ON n.session_uuid = s.uuid",
		TitleWeight:  chunkTitleWeight,
		FilterSQL:    filter.String(),
		FilterParams: params,
		MapRow:       scanChunkSearchRow,
		Fuzzy:        nil,
	})
	if err != nil {
		return nil, fmt.Errorf("building vault chunk corpus: %w", err)
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	results := retrieval.SearchWithFallback(ctx, corpus, opts.Query, limit, 0)

	out := make([]SearchResult, 0, len(results))
	for _, r := range results {
		meta, ok := r.Meta.(chunkMeta)
		if !ok {
			// Every row mapped by scanChunkSearchRow carries a chunkMeta, and
			// the engine passes Meta through untouched — a mismatch is a
			// programmer error, surfaced loudly rather than skipped.
			return nil, fmt.Errorf("vault: chunk search result %q carries no chunk metadata (got %T)", r.Title, r.Meta)
		}
		out = append(out, SearchResult{
			SessionUUID: r.Label,
			SubagentID:  meta.subagentID,
			LineIndex:   meta.firstLineIndex,
			Snippet:     chunkSnippet(r.Highlighted),
			Title:       meta.sessionTitle,
			ProjectPath: meta.projectPath,
			EndTime:     meta.endTime,
			Content:     r.Content,
			MatchLayer:  r.MatchLayer,
		})
	}
	return out, nil
}

// scanChunkSearchRow is the vault chunk corpus's retrieval.RowMapper: it scans
// the SELECT list built by SearchChunks plus the skeleton's highlighted/rank
// columns. The retrieval-facing fields feed the engine (Title/Content for
// rerank and fusion keys, SourceID for diversification); the vault-only
// anchors ride in Meta.
func scanChunkSearchRow(rows *sql.Rows) (retrieval.SearchResult, error) {
	var r retrieval.SearchResult
	var meta chunkMeta
	var sessionTitle, customTitle, endTime sql.NullString
	if err := rows.Scan(
		&r.Label, &r.Title, &r.Content, &r.SourceID,
		&meta.subagentID, &meta.firstLineIndex,
		&sessionTitle, &customTitle, &meta.projectPath, &endTime,
		&r.Highlighted, &r.Rank,
	); err != nil {
		return retrieval.SearchResult{}, err
	}
	meta.sessionTitle = effectiveSearchTitle(sessionTitle, customTitle)
	meta.endTime = parseTime(endTime)
	r.ContentType = "session"
	r.Meta = meta
	return r, nil
}

// chunkSnippet cuts a display snippet out of the skeleton's highlight()
// output: a window around the first match, with matches re-marked as
// [match] to mirror the per-line Search's snippet() style. The STX/ETX
// markers are single bytes replaced by single bytes, so byte offsets into the
// marked string equal offsets into the highlighted one.
var chunkMarkerReplacer = strings.NewReplacer(string(rune(stx)), "[", string(rune(etx)), "]")

func chunkSnippet(highlighted string) string {
	marked := chunkMarkerReplacer.Replace(highlighted)

	first := strings.IndexByte(highlighted, stx)
	if first == -1 {
		// No match markers — e.g. the match was in the title column. Fall
		// back to the head of the content.
		if len(marked) <= 2*chunkSnippetContext {
			return marked
		}
		return marked[:snapRuneStart(marked, 2*chunkSnippetContext)] + "…"
	}

	start := max(first-chunkSnippetContext, 0)
	end := min(first+chunkSnippetContext, len(marked))
	// Never cut through the first marked region: extend to its closing
	// bracket so the snippet always shows a complete [match].
	if close := strings.IndexByte(highlighted[first:], etx); close != -1 {
		end = max(end, min(first+close+1, len(marked)))
	}
	start = snapRuneStart(marked, start)
	end = snapRuneStart(marked, end)

	var b strings.Builder
	if start > 0 {
		b.WriteString("…")
	}
	b.WriteString(marked[start:end])
	if end < len(marked) {
		b.WriteString("…")
	}
	return b.String()
}

// snapRuneStart moves i back to the nearest UTF-8 rune boundary in s so a
// snippet cut never splits a multi-byte rune.
func snapRuneStart(s string, i int) int {
	for i > 0 && i < len(s) && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}
