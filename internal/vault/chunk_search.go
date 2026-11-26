package vault

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/serpro69/capy/internal/retrieval"
	"github.com/serpro69/capy/internal/sanitize"
)

// chunk_search.go runs the shared retrieval engine (internal/retrieval) over
// the vault's chunk-granularity corpus (vault_chunks / vault_chunks_trigram,
// built by chunker.go) — design docs/feat/wip/vault-session-search §D5. The
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
	// chunkIndex is the session-wide chunk number; NULL in a row that lacks it.
	chunkIndex    sql.NullInt64
	sessionTitle  string
	projectPath   string
	customProject *string
	endTime       time.Time
	platform      Platform
	parentUUID    string
}

// SearchChunks runs a query through the vault chunk corpus via the shared
// retrieval engine: RRF across the porter and trigram layers, synonym-AND →
// flat-OR fallback, proximity rerank, per-session diversification, and entity
// boosting. There is no fuzzy-correction pass — the vault has no vocabulary
// table, so the corpus supplies a nil FuzzyCorrector and relies on the
// trigram layer for typo/substring tolerance (design §D1; adding a vault
// vocabulary is deferred until benchmark A2 shows the delta is unacceptable).
//
// Honored options: Query, Project (effective project substring), ProjectPath
// (imported path substring), Platform, After / Before (on session end_time),
// Limit (default 20). Project and ProjectPath are mutually exclusive. Role and Raw are
// rejected loudly: role is undefined at chunk granularity (Not Doing), and
// the retrieval engine owns query sanitization, so raw FTS5 syntax cannot be
// passed through.
func (s *VaultStore) SearchChunks(ctx context.Context, opts SearchOptions) ([]SearchResult, error) {
	return s.searchChunks(ctx, opts, nil)
}

// PartialChunkSearch is a best-effort chunk search outcome that keeps the
// failure SearchChunks would otherwise only log.
type PartialChunkSearch struct {
	// Results are the ranked hits retrieval produced from the layers and rows
	// that did not fail; they may be incomplete when LayerErr is set.
	Results []SearchResult
	// LayerErr is the first layer query, row-scan or iteration failure observed
	// during retrieval, or nil when every attempted layer completed. Results may
	// be non-empty alongside it.
	LayerErr error
}

// SearchChunksPartial runs the same search as SearchChunks but returns
// surviving ranked hits together with the first layer failure, so a caller can
// present partial results without mistaking them for a complete search. The
// returned error reports validation and setup failures, which yield no results.
func (s *VaultStore) SearchChunksPartial(ctx context.Context, opts SearchOptions) (PartialChunkSearch, error) {
	var out PartialChunkSearch
	var firstError sync.Once
	onError := func(err error) {
		// Keep the operator log best-effort search emits; the caller reports
		// only the first failure in-band.
		slog.Warn("vault chunk search layer failed", "error", err)
		firstError.Do(func() { out.LayerErr = err })
	}
	results, err := s.searchChunks(ctx, opts, onError)
	if err != nil {
		return PartialChunkSearch{}, err
	}
	// Retrieval has joined every layer worker before returning.
	out.Results = results
	return out, nil
}

// searchChunks implements SearchChunks with the supplied layer error handler
// (nil logs).
func (s *VaultStore) searchChunks(ctx context.Context, opts SearchOptions, onError func(error)) ([]SearchResult, error) {
	predicate, projectArg, err := projectScopePredicate(opts.Project, opts.ProjectPath)
	if err != nil {
		return nil, err
	}
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
	if predicate != "" {
		filter.WriteString(" AND " + predicate)
		params = append(params, projectArg)
	}
	if opts.Platform != "" {
		filter.WriteString(" AND s.platform = ?")
		params = append(params, string(opts.Platform))
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
		// Title/project overrides are metadata only: their text never enters
		// MATCH. The shared project predicate filters both retrieval layers
		// before candidate limits, fusion and reranking.
		SelectColumns: "c.session_uuid, c.title, c.content_text, s.rowid, " +
			"c.subagent_id, c.first_line_index, c.chunk_index, s.title, n.custom_title, s.project_path, p.custom_project, s.end_time, " +
			"s.platform, s.parent_uuid",
		Join: "JOIN vault_sessions s ON s.uuid = c.session_uuid " +
			"LEFT JOIN vault_session_names n ON n.session_uuid = s.uuid " +
			"LEFT JOIN vault_session_projects p ON p.session_uuid = s.uuid",
		TitleWeight:  chunkTitleWeight,
		FilterSQL:    filter.String(),
		FilterParams: params,
		MapRow:       scanChunkSearchRow,
		Fuzzy:        nil,
		OnError:      onError,
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
		snippet, snippetStart := chunkSnippet(r.Content, r.Highlighted)
		out = append(out, SearchResult{
			SessionUUID:   r.Label,
			SubagentID:    meta.subagentID,
			LineIndex:     meta.firstLineIndex,
			ChunkIndex:    int(meta.chunkIndex.Int64),
			HasChunkIndex: meta.chunkIndex.Valid,
			Snippet:       snippet,
			SnippetStart:  snippetStart,
			Title:         meta.sessionTitle,
			ProjectPath:   meta.projectPath,
			Project:       effectiveProject(meta.projectPath, meta.customProject),
			CustomProject: meta.customProject,
			EndTime:       meta.endTime,
			Platform:      meta.platform,
			ParentUUID:    meta.parentUUID,
			Content:       r.Content,
			MatchLayer:    r.MatchLayer,
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
	var sessionTitle, customTitle, customProject, endTime, parentUUID sql.NullString
	var platform string
	if err := rows.Scan(
		&r.Label, &r.Title, &r.Content, &r.SourceID,
		&meta.subagentID, &meta.firstLineIndex, &meta.chunkIndex,
		&sessionTitle, &customTitle, &meta.projectPath, &customProject, &endTime,
		&platform, &parentUUID,
		&r.Highlighted, &r.Rank,
	); err != nil {
		return retrieval.SearchResult{}, err
	}
	meta.sessionTitle = effectiveSearchTitle(sessionTitle, customTitle)
	meta.customProject = nullStringPointer(customProject)
	meta.endTime = parseTime(endTime)
	meta.platform = Platform(platform)
	meta.parentUUID = parentUUID.String
	r.ContentType = "session"
	r.Meta = meta
	return r, nil
}

// Highlight-marker replacers: re-mark matches as [match], strip markers, or
// show literal marker bytes visibly.
var (
	chunkMarkerReplacer = strings.NewReplacer(string(rune(stx)), "[", string(rune(etx)), "]")
	chunkMarkerStripper = strings.NewReplacer(string(rune(stx)), "", string(rune(etx)), "")
	chunkMarkerVisible  = strings.NewReplacer(string(rune(stx)), "\uFFFD", string(rune(etx)), "\uFFFD")
)

// chunkSnippet cuts a display snippet out of the skeleton's highlight()
// output: a window around the first match, with matches re-marked as
// [match] to mirror the per-line Search's snippet() style. The STX/ETX
// markers are single bytes replaced by single bytes, so byte offsets into the
// marked string equal offsets into the highlighted one. It also returns the
// window's start offset in the redacted content, so equal snippet text from
// different places in one chunk can be told apart.
//
// Known secrets are redacted on the complete content before any cut, so a
// secret crossing a window edge cannot survive as an unrecognized fragment.
// The highlight is reused only when removing its markers reproduces that
// redacted, valid UTF-8 content; otherwise the markers are discarded and the
// head of the redacted content is shown. Content holding literal marker bytes
// of its own shows its head with each such byte as U+FFFD.
func chunkSnippet(content, highlighted string) (string, int) {
	clean := strings.ToValidUTF8(sanitize.StripSecrets(content), "\uFFFD")
	if strings.ContainsAny(content, string(rune(stx))+string(rune(etx))) {
		// Literal marker bytes make the highlight ambiguous: show the head of
		// the redacted content with each marker byte replaced visibly. They are
		// never deleted, because removing them could join fragments on either
		// side into text redaction did not see.
		snippet, _ := chunkSnippetFrom(chunkMarkerVisible.Replace(clean))
		return snippet, 0
	}
	if chunkMarkerStripper.Replace(highlighted) != clean {
		highlighted = clean
	}
	return chunkSnippetFrom(highlighted)
}

// chunkSnippetFrom windows highlighted text whose STX/ETX bytes are match
// markers only, returning the window and its start offset in the text with
// the markers removed.
func chunkSnippetFrom(highlighted string) (string, int) {
	marked := chunkMarkerReplacer.Replace(highlighted)

	first := strings.IndexByte(highlighted, stx)
	if first == -1 {
		// No match markers — e.g. the match was in the title column. Fall
		// back to the head of the content.
		if len(marked) <= 2*chunkSnippetContext {
			return marked, 0
		}
		return marked[:snapRuneStart(marked, 2*chunkSnippetContext)] + "…", 0
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
	// No marker precedes the first match, so start also indexes the text
	// with the markers removed.
	return b.String(), start
}

// snapRuneStart moves i back to the nearest UTF-8 rune boundary in s so a
// snippet cut never splits a multi-byte rune.
func snapRuneStart(s string, i int) int {
	for i > 0 && i < len(s) && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}
