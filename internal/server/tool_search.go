package server

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
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

const (
	searchWindowDuration  = 60 * time.Second
	searchMaxResultsAfter = 3 // after 3 calls: 1 result per query
	searchBlockAfter      = 8 // after 8 calls: refuse
	searchMaxTotalBytes   = 40 * 1024
	searchSnippetMaxLen   = 1500
)

func (s *Server) handleSearch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()

	// Normalize: accept both queries (array) and query (string)
	var queryList []string
	if raw, ok := args["queries"]; ok {
		queryList = coerceStringArray(raw)
	}
	if len(queryList) == 0 {
		if q := req.GetString("query", ""); q != "" {
			queryList = []string{q}
		}
	}
	if len(queryList) == 0 {
		return errorResult("Error: provide query or queries."), nil
	}

	limit := int(req.GetFloat("limit", 3))
	source := req.GetString("source", "")

	includeKinds, err := parseIncludeKinds(args["include_kinds"])
	if err != nil {
		return errorResult("Error: " + err.Error()), nil
	}

	// Progressive throttling (atomic check+reset+increment)
	callNum, windowAge := s.throttle.advance(searchWindowDuration)

	if callNum > searchBlockAfter {
		text := fmt.Sprintf(
			"BLOCKED: %d search calls in %ds. You're flooding context. STOP making individual search calls. Use batch_execute(commands, queries) for your next research step.",
			callNum, int(windowAge.Seconds()),
		)
		return s.trackToolResponse("capy_search", errorResult(text)), nil
	}

	// Determine effective limit based on throttle level
	effectiveLimit := min(limit, 2)
	if callNum > searchMaxResultsAfter {
		effectiveLimit = 1
	}

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

	// Project scope for the vault pass: the current project by default;
	// all_projects (or project:"*") widens to every archived project; an explicit
	// project substring narrows. Mirrors capy_vault_search. Knowledge.db is
	// already per-project, so these fields only affect the vault pass.
	vaultProject := s.projectDir
	explicitProject := req.GetString("project", "")
	switch {
	case req.GetBool("all_projects", false) || explicitProject == "*":
		vaultProject = ""
	case explicitProject != "":
		vaultProject = explicitProject
	}

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

	// Vault stats feed the corpus-aware empty-KB preflight and the reindex-backlog
	// hint; fetch once when a vault pass will run.
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
	vaultCanServe := runVaultPass && vaultStatsHaveSessions(vaultStats, vaultProject)
	if kbEmpty && !vaultCanServe {
		return s.trackToolResponse("capy_search", &mcp.CallToolResult{
			Content: []mcp.Content{mcp.NewTextContent(
				"The knowledge base is empty — nothing has been indexed yet.\n\n" +
					"To populate it, use:\n" +
					"  • capy_batch_execute(commands, queries) — run commands, auto-index output, and search in one call\n" +
					"  • capy_fetch_and_index(url) — fetch a URL, index it, then search with capy_search\n" +
					"  • capy_index(content, source) — manually index text content\n\n" +
					"After indexing, capy_search becomes available for follow-up queries.",
			)},
			IsError: true,
		}), nil
	}

	var sections []string
	totalSize := 0
	hasResults := false

	// Lazily fetched once if any query returns zero results while ephemeral is
	// excluded. Cached across queries in this request; a concurrent write could
	// shift the count by ±1 — acceptable, the user-facing number is directional,
	// not authoritative. -1 = not yet checked. Ephemeral membership is identical
	// in searchOpts and knowledgeOpts (federation only strips session), so either
	// works; searchOpts keeps this a knowledge-corpus concern.
	ephemeralExcluded := !store.KindScopeIncludes(searchOpts, store.KindEphemeral)
	ephemeralCount := -1

	for _, q := range queryList {
		if totalSize > searchMaxTotalBytes {
			sections = append(sections, fmt.Sprintf("## %s\n(output cap reached)\n", q))
			continue
		}

		// Best-effort per pass, but NOT silent (fail-loud): a swallowed error is
		// especially dangerous under federation — a knowledge-DB failure yields
		// zero knowledge hits, yet vault hits keep the merged block non-empty, so
		// the failure would never surface. Capture each error and report it
		// in-band (mirrors handleVaultSearch's per-query degradation).
		var errs []string
		var kResults []store.SearchResult
		if runKnowledgePass {
			r, sErr := st.SearchWithFallback(q, effectiveLimit, knowledgeOpts)
			if sErr != nil {
				errs = append(errs, fmt.Sprintf("knowledge: %v", sErr))
			}
			kResults = r
		}
		var vResults []vault.SearchResult
		if runVaultPass {
			r, sErr := vlt.SearchChunks(ctx, vault.SearchOptions{
				Query:   q,
				Project: vaultProject,
				Limit:   effectiveLimit,
			})
			if sErr != nil {
				errs = append(errs, fmt.Sprintf("vault: %v", sErr))
			}
			vResults = r
		}

		blocks := federateHits(kResults, vResults, q, effectiveLimit)

		if len(blocks) == 0 {
			// A pass that errored takes precedence over the ephemeral hint: the
			// user needs the failure, not advice to widen kinds.
			if len(errs) > 0 {
				sections = append(sections, fmt.Sprintf("## %s\nError: %s", q, strings.Join(errs, " | ")))
				continue
			}
			noResults := fmt.Sprintf("## %s\nNo results found.", q)
			if ephemeralExcluded {
				if ephemeralCount < 0 {
					if n, cErr := st.CountSourcesByKind(store.KindEphemeral); cErr == nil {
						ephemeralCount = n
					} else {
						ephemeralCount = 0
					}
				}
				if ephemeralCount > 0 {
					noResults += fmt.Sprintf(
						"\n\n%d ephemeral source(s) present but excluded by default. Ephemeral sources include command output (capy_execute / capy_execute_file / capy_batch_execute) and fetched web pages (capy_fetch_and_index). To include them, retry with:\n"+
							"  • include_kinds: [\"durable\",\"ephemeral\"]  (search across both kinds), or\n"+
							"  • source: \"<label>\"  (explicit-source filter bypasses kind filtering)",
						ephemeralCount,
					)
				}
			}
			sections = append(sections, noResults)
			continue
		}

		hasResults = true
		block := strings.Join(blocks, "\n\n")
		// Partial results: one corpus failed but the other returned hits. Flag the
		// degradation so the user knows the result set is incomplete.
		if len(errs) > 0 {
			block = fmt.Sprintf("⚠ partial results (%s)\n\n%s", strings.Join(errs, " | "), block)
		}
		sections = append(sections, fmt.Sprintf("## %s\n\n%s", q, block))
		totalSize += len(block)
	}

	output := strings.Join(sections, "\n\n---\n\n")

	// Add throttle warning
	if callNum >= searchMaxResultsAfter {
		output += fmt.Sprintf(
			"\n\n⚠ search call #%d/%d in this window. Results limited to %d/query. Batch queries: search(queries: [\"q1\",\"q2\",\"q3\"]) or use batch_execute.",
			callNum, searchBlockAfter, effectiveLimit,
		)
	}

	// Vault session messaging (design §D7, replaces the stale knowledge.db
	// session-exclusion copy): when a session-scoped search comes back empty,
	// name the command that makes sessions searchable — CAPY_VAULT_KEY when the
	// vault is disabled, `capy vault reindex` when a chunk backlog can hide
	// otherwise-matching sessions. Gated on source=="" so a source-filtered
	// (knowledge-only) search doesn't get spurious vault advice. Fires at most
	// once per request (the `!hasResults` gate, not per-query): if any query in
	// the batch produced hits the vault is clearly usable, so repeating a
	// setup/backlog nag for the queries that missed would be noise.
	if !hasResults && source == "" {
		switch {
		case sessionInScope && vlt == nil:
			output += "\n\nPast sessions aren't searchable — set CAPY_VAULT_KEY to archive and search session transcripts."
		case runVaultPass && vaultStats != nil && vaultStats.OutdatedSessions > 0:
			output += fmt.Sprintf(
				"\n\n%d archived session(s) are below index v%d and not yet chunk-searchable — run `capy vault reindex` to include them.",
				vaultStats.OutdatedSessions, vaultStats.IndexVersion)
		}
	}

	// Count sources for each kind within the caller's search scope.
	// Only *included* kinds are counted here; *excluded* kinds are handled by
	// the per-query ephemeral hint above. Uses searchOpts (the caller's declared
	// scope), not knowledgeOpts: this is a knowledge-corpus count, and post-Task-9
	// knowledge.db holds no session rows, so the searchOpts/knowledgeOpts
	// distinction for KindSession is moot in the shipped product.
	if !hasResults {
		countParts := make([]string, 0, 3)
		for _, kind := range []store.SourceKind{store.KindDurable, store.KindEphemeral, store.KindSession} {
			if !store.KindScopeIncludes(searchOpts, kind) {
				continue
			}
			n, err := st.CountSourcesByKind(kind)
			if err == nil && n > 0 {
				countParts = append(countParts, fmt.Sprintf("%d %s", n, kind))
			}
		}
		if len(countParts) > 0 {
			output += fmt.Sprintf("\n\n%s source(s) indexed. Refine your query terms, or use capy_stats for source details.", strings.Join(countParts, ", "))
		}
	}

	return s.trackToolResponse("capy_search", textResult(output)), nil
}

// federateHits RRF-merges the knowledge and vault ranked lists for one query
// into a single ordered slice of formatted result blocks, capped at limit.
// Each item appears in exactly one list (the two corpora are disjoint), so
// there is no cross-corpus dedup — the merge is a rank interleave: each hit
// scores 1/(k+rank) by its position in its own already-ranked list, with rank
// 0-based (matching the retrieval engine's own convention, so federated weights
// are on the same scale). Ties keep knowledge before vault (append order) via a
// stable sort, gently favoring curated durable knowledge over session recall.
func federateHits(kResults []store.SearchResult, vResults []vault.SearchResult, query string, limit int) []string {
	type scoredBlock struct {
		rrf   float64
		block string
	}
	hits := make([]scoredBlock, 0, len(kResults)+len(vResults))
	for i, r := range kResults {
		hits = append(hits, scoredBlock{
			rrf:   1.0 / (federationRRFK + float64(i)),
			block: formatKnowledgeHit(r, query),
		})
	}
	for i, r := range vResults {
		hits = append(hits, scoredBlock{
			rrf:   1.0 / (federationRRFK + float64(i)),
			block: formatVaultHit(r),
		})
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].rrf > hits[b].rrf })
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.block
	}
	return out
}

// formatKnowledgeHit renders one knowledge-store hit: a source label header, the
// chunk title, and a query-highlighted snippet. Extracted from the per-query
// loop so federation renders knowledge and vault hits through parallel helpers
// (formatVaultHit is its vault sibling in tool_vault_search.go).
func formatKnowledgeHit(r store.SearchResult, query string) string {
	header := fmt.Sprintf("--- [%s] ---", r.Label)
	heading := fmt.Sprintf("### %s", r.Title)
	snippet := ExtractSnippet(r.Content, query, searchSnippetMaxLen, r.Highlighted)
	return fmt.Sprintf("%s\n%s\n\n%s", header, heading, snippet)
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

// vaultStatsHaveSessions reports whether the vault holds any archived sessions
// in the given project scope — the corpus-aware half of the empty-KB preflight.
// It is scoped to the same project filter the vault search pass uses: an empty
// project means "all projects" (global count); a non-empty project matches the
// per-project counts in VaultStats.ByProject by the same substring rule as
// SearchChunks' project filter. A nil stats (fetch failed / vault disabled) is
// treated as "cannot serve".
func vaultStatsHaveSessions(stats *vault.VaultStats, project string) bool {
	if stats == nil {
		return false
	}
	if project == "" {
		return stats.Sessions > 0
	}
	for _, p := range stats.ByProject {
		if p.Count > 0 && strings.Contains(p.ProjectPath, project) {
			return true
		}
	}
	return false
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
			return nil, fmt.Errorf("invalid include_kinds value %q (accepted: \"durable\", \"ephemeral\", \"session\")", v)
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out, nil
}
