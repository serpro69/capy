package server

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/sanitize"
	"github.com/serpro69/capy/internal/store"
	"github.com/serpro69/capy/internal/vault"
)

// federationRRFK is the Reciprocal Rank Fusion constant used to merge the
// knowledge and vault ranked lists. Raw BM25 scores are not comparable across
// corpora (IDF is per-table), but rank positions are — so cross-corpus
// federation fuses on 1/(k+rank), never on raw bm25() values (kk:arch-decisions
// "Cross-corpus search federation via RRF rank-merge"). It matches the engine's
// internal rrfK so a hit's federated weight is on the same scale as its
// within-corpus fusion weight.
const federationRRFK = 60.0

func (s *Server) handleSearch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return s.search(ctx, req, searchReplyMaxBytes)
}

// search serves capy_search within a reply of at most replyMax UTF-8 bytes.
// handleSearch always passes searchReplyMaxBytes; tests pass a smaller reply to
// place a budget boundary without depending on exact rendered sizes.
func (s *Server) search(ctx context.Context, req mcp.CallToolRequest, replyMax int) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	for _, name := range []string{"query", "source", "project"} {
		if raw, present := args[name]; present {
			if _, ok := raw.(string); !ok {
				return errorResult(fmt.Sprintf("invalid parameter %q: expected a string", name)), nil
			}
		}
	}

	// Normalize: accept both queries (array) and query (string)
	var queryList []string
	if raw, ok := args["queries"]; ok {
		var err error
		queryList, err = stringArrayArg(raw, "queries")
		if err != nil {
			return errorResult(err.Error()), nil
		}
	}
	if len(queryList) == 0 {
		if q := req.GetString("query", ""); q != "" {
			queryList = []string{q}
		}
	}
	if len(queryList) == 0 {
		return errorResult("Error: provide query or queries."), nil
	}
	for _, query := range queryList {
		if strings.TrimSpace(query) == "" {
			return errorResult("Error: queries must not be blank."), nil
		}
	}

	limit, err := searchLimitArg(args)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	source := req.GetString("source", "")

	var kinds []string
	if raw, present := args["include_kinds"]; present {
		kinds, err = stringArrayArg(raw, "include_kinds")
		if err != nil {
			return errorResult(err.Error()), nil
		}
	}
	includeKinds, err := parseIncludeKinds(kinds)
	if err != nil {
		return errorResult("Error: " + err.Error()), nil
	}
	vaultProject, vaultProjectPath, err := vaultProjectScope(req, s.projectDir)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	budget := s.throttle.advance(limit)
	// Every valid reply ends with the exact admission snapshot; it is charged to
	// the request-wide notice reserve together with any body notices.
	respond := func(body string, isError bool) (*mcp.CallToolResult, error) {
		result := textResult(body + noticeSeparator + budget.notice())
		result.IsError = isError
		return s.trackToolResponse("capy_search", result), nil
	}
	if budget.limit == 0 {
		return respond("BLOCKED: shared search budget exhausted. Retry after the reset time below; all callers of this MCP server share the window.", true)
	}
	effectiveLimit := budget.limit

	st := s.getStore()

	// ── Vault federation setup ──────────────────────────────────────────────
	// The vault owns the session corpus (design vault-session-search §D7). A
	// capy_search that includes the `session` kind federates a vault chunk pass
	// with the knowledge pass and RRF-merges the two ranked lists.
	searchOpts := store.SearchOptions{
		Source:       source,
		IncludeKinds: includeKinds,
	}
	sessionInScope := store.KindScopeIncludes(searchOpts, store.KindSession)
	vlt := s.getVault()
	// Run the vault pass only when the vault is enabled, `session` is in the
	// caller's scope, and no explicit source filter is set — a `source:` filter
	// targets knowledge-store labels, which the vault has no analogue for, so it
	// keeps the search knowledge-only.
	runVaultPass := vlt != nil && sessionInScope && source == ""

	// Knowledge pass kinds: when the vault serves the session corpus, strip
	// `session` from the knowledge filter so the same conversations aren't served
	// twice (knowledge.db still holds legacy session rows until the sweep is
	// removed in Task 9). A session-only request leaves no knowledge pass to run.
	knowledgeOpts := searchOpts
	runKnowledgePass := true
	if runVaultPass {
		knowledgeOpts.IncludeKinds = knowledgeKindsWithoutSession(searchOpts)
		runKnowledgePass = len(knowledgeOpts.IncludeKinds) > 0
	}

	// Vault stats feed only the reindex-backlog hint. Grouped statistics cannot
	// determine membership in both effective-project and imported-path scopes.
	var vaultStats *vault.VaultStats
	if runVaultPass {
		if vs, sErr := vlt.Stats(ctx); sErr == nil {
			vaultStats = vs
		}
	}

	// Corpus-aware empty-KB preflight (review #4): the guide-to-indexing early
	// return must NOT fire when the vault can still serve this search — otherwise
	// a vault-only project is told to index knowledge it doesn't need. The check
	// is scoped to the SAME project filter the vault pass uses: the vault is
	// cross-project, so a global "any sessions?" count would wrongly suppress the
	// guide on a fresh project whose only vault neighbors belong to other projects.
	kbStats, statErr := st.Stats(s.ephemeralTTL(), s.sessionTTL())
	kbEmpty := statErr == nil && kbStats.SourceCount == 0
	var vaultCanServe bool
	var availabilityErr error
	if runVaultPass {
		vaultCanServe, availabilityErr = vlt.HasSessionsInProject(ctx, vaultProject, vaultProjectPath)
	}
	if kbEmpty && !vaultCanServe && availabilityErr == nil {
		return respond(
			"The knowledge base is empty — nothing has been indexed yet.\n\n"+
				"To populate it, use:\n"+
				"  • capy_index(content, source) — index text you want to search later\n"+
				"  • capy_fetch_and_index(url) — fetch a web page and index it\n\n"+
				"New captured command output is kept in run storage, not knowledge — recover it with capy_runs using its run or batch ID.", true)
	}

	// Request-wide notices share searchNoticeReserve with the footer; query
	// groups use the rest of the reply (see replyWriter).
	var leading []string
	if availabilityErr != nil {
		// A failed preflight leaves availability unknown. Surface it once and
		// still attempt both normal passes, preserving any successful results.
		leading = append(leading, "⚠ vault availability check failed: "+displayField(availabilityErr.Error(), searchCauseMaxBytes))
	}

	w := newReplyWriter(replyMax)
	completion := ""
	// hasResults: some query that ran returned a hit, whether or not it fit.
	hasResults := false
	// allClean: every query ran and every eligible pass succeeded.
	allClean := true
	// cleanKnowledgeMiss: some query completed every eligible pass, including a
	// knowledge pass, with zero hits.
	cleanKnowledgeMiss := false

	for i, q := range queryList {
		ordinal := i + 1
		if !w.canStartGroup() {
			completion = unsearchedNotice(ordinal, len(queryList))
			allClean = false
			break
		}
		if s.searchDispatched != nil {
			s.searchDispatched(ordinal, q)
		}

		// Best-effort per pass, but NOT silent (fail-loud): a swallowed error is
		// especially dangerous under federation — a knowledge-DB failure yields
		// zero knowledge hits, yet vault hits keep the merged block non-empty, so
		// the failure would never surface. Capture each setup or FTS layer error
		// and report it in-band beside any surviving ranked hits (mirrors
		// handleVaultSearch's per-query degradation).
		var errs []string
		var kResults []store.SearchResult
		if runKnowledgePass {
			r, sErr := st.SearchPartialContext(ctx, q, effectiveLimit, knowledgeOpts)
			if sErr == nil {
				sErr = r.LayerErr
			}
			if sErr != nil {
				errs = append(errs, fmt.Sprintf("knowledge: %v", sErr))
			}
			kResults = r.Results
		}
		var vResults []vault.SearchResult
		if runVaultPass {
			r, sErr := vlt.SearchChunksPartial(ctx, vault.SearchOptions{
				Query:       q,
				Project:     vaultProject,
				ProjectPath: vaultProjectPath,
				Limit:       effectiveLimit,
			})
			if sErr == nil {
				sErr = r.LayerErr
			}
			if sErr != nil {
				errs = append(errs, fmt.Sprintf("vault: %v", sErr))
			}
			vResults = r.Results
		}

		hits := federateHits(kResults, vResults, effectiveLimit)
		// A failed pass takes precedence over miss advice: the caller needs the
		// failure, not advice to widen kinds.
		if len(errs) > 0 {
			allClean = false
		} else if len(hits) == 0 && runKnowledgePass {
			cleanKnowledgeMiss = true
		}

		w.startGroup(queryFrame(ordinal, q, errs, len(hits)))
		shown := 0
		// Render in final rank order only; a repeat of an earlier passage in
		// this reply is shown as a reference to it.
		for _, h := range hits {
			if !w.addHit(h.render(q)) {
				break
			}
			shown++
		}
		if len(hits) > 0 {
			hasResults = true
		}
		if shown < len(hits) {
			completion = partialNotice(ordinal, len(queryList), shown, len(hits), effectiveLimit)
			allClean = false
			break
		}
	}

	trailing := s.searchHints(searchHintInput{
		st:                 st,
		searchOpts:         searchOpts,
		source:             source,
		sessionInScope:     sessionInScope,
		vaultEnabled:       vlt != nil,
		runVaultPass:       runVaultPass,
		vaultStats:         vaultStats,
		hasResults:         hasResults,
		allClean:           allClean,
		cleanKnowledgeMiss: cleanKnowledgeMiss,
	})
	if completion != "" {
		trailing = append([]string{completion}, trailing...)
	}

	parts := leading
	if groups := w.String(); groups != "" {
		parts = append(parts, groups)
	}
	parts = append(parts, trailing...)
	return respond(strings.Join(parts, noticeSeparator), false)
}

// searchHintInput carries the request facts searchHints needs.
type searchHintInput struct {
	st                 *store.ContentStore
	searchOpts         store.SearchOptions
	source             string
	sessionInScope     bool
	vaultEnabled       bool
	runVaultPass       bool
	vaultStats         *vault.VaultStats
	hasResults         bool
	allClean           bool
	cleanKnowledgeMiss bool
}

// searchHints returns the request-wide guidance, each hint at most once per
// reply. Every hint is bounded so that, with the completion notice,
// availability notice and footer, it fits searchNoticeReserve.
func (s *Server) searchHints(in searchHintInput) []string {
	var hints []string

	// Miss guidance, once per reply: new command output lives in run storage,
	// and retained ephemeral knowledge is excluded unless selected. An explicit
	// source or an ephemeral-inclusive kind scope opts out of the ephemeral part
	// (KindScopeIncludes treats an explicit source as including every kind).
	if in.cleanKnowledgeMiss {
		hint := "For queries without hits: new captured command output is kept in run storage, not knowledge — use capy_runs with its run or batch ID."
		if !store.KindScopeIncludes(in.searchOpts, store.KindEphemeral) {
			if n, err := in.st.CountSourcesByKind(store.KindEphemeral); err == nil && n > 0 {
				hint += fmt.Sprintf(" %d ephemeral knowledge source(s) — fetched web pages (capy_fetch_and_index) and historical command output — are excluded by default. To include them, retry with include_kinds: [\"durable\",\"ephemeral\"] or with source: \"<label>\" (an explicit source bypasses kind filtering).", n)
			}
		}
		hints = append(hints, hint)
	}

	// Vault session messaging (design §D7): when the queries that ran found no
	// hits, name the command that makes sessions searchable — CAPY_VAULT_KEY
	// when the vault is disabled, `capy vault reindex` when a chunk backlog can
	// hide otherwise-matching sessions. This also applies when only a prefix of
	// the batch ran. Gated on source=="" so a source-filtered (knowledge-only)
	// search doesn't get spurious vault advice. If any query produced hits the
	// vault is clearly usable, so repeating a setup/backlog nag would be noise.
	if !in.hasResults && in.source == "" {
		switch {
		case in.sessionInScope && !in.vaultEnabled:
			hints = append(hints, "Past sessions aren't searchable — set CAPY_VAULT_KEY to archive and search session transcripts.")
		case in.runVaultPass && in.vaultStats != nil && in.vaultStats.OutdatedSessions > 0:
			hints = append(hints, fmt.Sprintf(
				"%d archived session(s) were indexed by an older version (v%d) and may omit newer indexed content — run `capy vault reindex` to update them.",
				in.vaultStats.OutdatedSessions, in.vaultStats.IndexVersion))
		}
	}

	// Whole-batch miss statement: only when every query ran, every eligible
	// pass succeeded and none returned a hit. Counts only kinds *included* in
	// the caller's scope; excluded ephemeral sources are covered by the miss
	// guidance above.
	if in.allClean && !in.hasResults {
		countParts := make([]string, 0, 3)
		for _, kind := range []store.SourceKind{store.KindDurable, store.KindEphemeral, store.KindSession} {
			if !store.KindScopeIncludes(in.searchOpts, kind) {
				continue
			}
			n, err := in.st.CountSourcesByKind(kind)
			if err == nil && n > 0 {
				countParts = append(countParts, fmt.Sprintf("%d %s", n, kind))
			}
		}
		if len(countParts) > 0 {
			hints = append(hints, fmt.Sprintf("%s source(s) indexed. Refine your query terms, or use capy_stats for source details.", strings.Join(countParts, ", ")))
		}
	}
	return hints
}

// rankedHit is one federated hit, kept typed until the final rank order is
// known; exactly one of knowledge and vault is set.
type rankedHit struct {
	knowledge *store.SearchResult
	vault     *vault.SearchResult
}

// render formats the hit for query.
func (h rankedHit) render(query string) renderedHit {
	if h.knowledge != nil {
		return renderKnowledgeHit(*h.knowledge, query)
	}
	return renderVaultHit(*h.vault)
}

// federateHits RRF-merges the knowledge and vault ranked lists for one query
// into a single ordered slice, capped at limit. Each item appears in exactly
// one list (the two corpora are disjoint), so there is no cross-corpus dedup —
// the merge is a rank interleave: each hit scores 1/(k+rank) by its position in
// its own already-ranked list, with rank 0-based (matching the retrieval
// engine's own convention, so federated weights are on the same scale). Ties
// keep knowledge before vault (append order) via a stable sort, gently favoring
// curated durable knowledge over session recall.
func federateHits(kResults []store.SearchResult, vResults []vault.SearchResult, limit int) []rankedHit {
	type scoredHit struct {
		rrf float64
		hit rankedHit
	}
	hits := make([]scoredHit, 0, len(kResults)+len(vResults))
	for i := range kResults {
		hits = append(hits, scoredHit{
			rrf: 1.0 / (federationRRFK + float64(i)),
			hit: rankedHit{knowledge: &kResults[i]},
		})
	}
	for i := range vResults {
		hits = append(hits, scoredHit{
			rrf: 1.0 / (federationRRFK + float64(i)),
			hit: rankedHit{vault: &vResults[i]},
		})
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].rrf > hits[b].rrf })
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]rankedHit, len(hits))
	for i, h := range hits {
		out[i] = h.hit
	}
	return out
}

// renderKnowledgeHit renders one knowledge-store hit: a source label header,
// the chunk title, and a query-highlighted snippet. renderVaultHit is its vault
// sibling in tool_vault_search.go.
//
// Every field is redacted on its complete value and bounded, so the block stays
// within searchHitMaxBytes. A label that looks like a vault handle
// ("session:…") is marked as knowledge so corpus attribution stays explicit.
//
// The hit is citable when retrieval located its row: the layer table and rowid
// with the source ID locate it (its label and title are fixed for that
// location), and the selected windows place the passage in it.
func renderKnowledgeHit(r store.SearchResult, query string) renderedHit {
	label := displayField(r.Label, knowledgeLabelMaxBytes)
	header := fmt.Sprintf("--- [%s] ---", label)
	if strings.HasPrefix(label, "session:") {
		header = fmt.Sprintf("--- [%s] (knowledge) ---", label)
	}
	heading := fmt.Sprintf("### %s", displayField(r.Title, knowledgeTitleMaxBytes))
	// Literal highlight-marker bytes are shown visibly rather than passed
	// through as invisible control bytes; the content then no longer matches
	// the highlight, so match positions come from the query terms, which are
	// normalized the same way.
	snippet, windows := extractSnippet(markerVisible.Replace(sanitize.StripSecrets(r.Content)),
		markerVisible.Replace(query), searchPassageMaxBytes, r.Highlighted)
	block := fmt.Sprintf("%s\n%s\n\n%s", header, heading, snippet)
	if r.Table == "" || r.RowID == 0 {
		return renderedHit{block: block}
	}
	return renderedHit{
		block: block,
		key: passageKey{
			corpus: "knowledge",
			identity: passageIdentity(windows, r.Table,
				strconv.FormatInt(r.RowID, 10), strconv.FormatInt(r.SourceID, 10)),
			block: block,
		},
		citable: true,
	}
}

// knowledgeKindsWithoutSession returns the kinds the knowledge pass should
// search when the vault serves the session corpus: every kind in the caller's
// effective scope EXCEPT session. Membership is derived from the store's own
// KindScopeIncludes (which mirrors effectiveKindFilter, the single source of
// truth for the empty-scope default) rather than re-hardcoding that default —
// so it can't drift if the store's default kinds ever change. Returning an
// explicit non-empty slice keeps the store from re-applying its default;
// returning empty signals "no knowledge pass" (a session-only request).
//
// Caller invariant: only reached when runVaultPass, i.e. opts.Source == "", so
// KindScopeIncludes never takes its source-bypass branch here.
func knowledgeKindsWithoutSession(opts store.SearchOptions) []store.SourceKind {
	out := make([]store.SourceKind, 0, 2)
	for _, k := range []store.SourceKind{store.KindDurable, store.KindEphemeral} {
		if store.KindScopeIncludes(opts, k) {
			out = append(out, k)
		}
	}
	return out
}

// parseIncludeKinds normalizes the include_kinds argument to a typed slice.
// Returns (nil, nil) when absent or empty so the store layer applies the default.
func parseIncludeKinds(raw any) ([]store.SourceKind, error) {
	if raw == nil {
		return nil, nil
	}
	values := coerceStringArray(raw)
	if len(values) == 0 {
		return nil, nil
	}
	out := make([]store.SourceKind, 0, len(values))
	seen := make(map[store.SourceKind]bool, len(values))
	for _, v := range values {
		k := store.SourceKind(v)
		if !k.Valid() {
			return nil, fmt.Errorf("invalid include_kinds value %s (accepted: \"durable\", \"ephemeral\", \"session\")", argPreview(v))
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out, nil
}
