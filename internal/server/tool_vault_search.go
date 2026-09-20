package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/vault"
)

// vaultSearchMaxLimit caps results-per-query regardless of the caller's request,
// so an over-broad limit can't exhaust the context window.
const vaultSearchMaxLimit = 10

// handleVaultSearch serves capy_vault_search: it runs the shared retrieval
// engine over the vault's chunk corpus (VaultStore.SearchChunks) and formats
// chunk-level hits from past sessions. It degrades loudly in two states the
// caller cannot see: the vault being disabled (no CAPY_VAULT_KEY) and the vault
// being enabled but holding a reindex backlog that can hide otherwise-matching
// sessions (design vault-session-search §D5).
func (s *Server) handleVaultSearch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()

	// Normalize: accept both queries (array) and query (string), mirroring capy_search.
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
		// Early-validation exits (this and the date-parse errors below) return a
		// bare errorResult without trackToolResponse, matching handleSearch:
		// malformed calls don't skew per-tool usage stats. Enablement/degrade
		// paths past this point ARE tracked.
		return errorResult("Error: provide query or queries"), nil
	}

	// Degrade loudly: session archival (and therefore vault search) is opt-in.
	vlt := s.getVault()
	if vlt == nil {
		return s.trackToolResponse("capy_vault_search", errorResult(
			"Vault search is disabled — set CAPY_VAULT_KEY to archive and search past sessions.\n"+
				"Once enabled, run `capy vault reindex` to update already-archived sessions to the current index.")), nil
	}

	// Bound the limit so a hallucinated `limit: 100` (or a large queries array)
	// can't flood the context window — the very problem capy exists to prevent.
	// The sibling handleSearch caps hits + total bytes for the same reason.
	limit := int(req.GetFloat("limit", 3))
	if limit <= 0 {
		limit = 3
	}
	limit = min(limit, vaultSearchMaxLimit)

	project, projectPath := vaultProjectScope(req, s.projectDir)

	after, err := parseDateArg(req.GetString("after", ""), false)
	if err != nil {
		return errorResult("Error: " + err.Error()), nil
	}
	before, err := parseDateArg(req.GetString("before", ""), true)
	if err != nil {
		return errorResult("Error: " + err.Error()), nil
	}

	var sections []string
	hasResults := false
	for _, q := range queryList {
		results, err := vlt.SearchChunks(ctx, vault.SearchOptions{
			Query:       q,
			Project:     project,
			ProjectPath: projectPath,
			After:       after,
			Before:      before,
			Limit:       limit,
		})
		if err != nil {
			// Best-effort batch: a single query's failure is surfaced in-band so
			// the remaining queries still run, rather than failing the whole call
			// (mirrors handleSearch's per-query degradation).
			sections = append(sections, fmt.Sprintf("## %s\nError: %v", q, err))
			continue
		}
		if len(results) == 0 {
			sections = append(sections, fmt.Sprintf("## %s\nNo results found.", q))
			continue
		}

		hasResults = true
		formatted := make([]string, 0, len(results))
		for _, r := range results {
			formatted = append(formatted, formatVaultHit(r))
		}
		sections = append(sections, fmt.Sprintf("## %s\n\n%s", q, strings.Join(formatted, "\n\n")))
	}

	output := strings.Join(sections, "\n\n---\n\n")

	// Degrade loudly on the enabled-but-empty state: a manual reindex backlog
	// (`capy vault reindex` is not automatic) can leave archived sessions missing
	// newer indexed content, so a zero-hit result names the command that fixes it.
	if !hasResults {
		if vs, sErr := vlt.Stats(ctx); sErr == nil && vs.OutdatedSessions > 0 {
			output += fmt.Sprintf(
				"\n\n%d archived session(s) were indexed by an older version (v%d) and may omit newer indexed content — run `capy vault reindex` to update them.",
				vs.OutdatedSessions, vs.IndexVersion)
		}
	}

	return s.trackToolResponse("capy_vault_search", textResult(output)), nil
}

// vaultProjectScope resolves the existing MCP selectors without changing the
// server's physical project directory. Widening wins over explicit selection;
// an omitted/empty selector keeps imported-path scope even after reassignment.
func vaultProjectScope(req mcp.CallToolRequest, projectDir string) (project, projectPath string) {
	explicit := req.GetString("project", "")
	switch {
	case req.GetBool("all_projects", false) || explicit == "*":
		return "", ""
	case explicit != "":
		return explicit, ""
	default:
		return "", projectDir
	}
}

// formatVaultHit renders one chunk hit: a session:<uuid> label (so the assistant
// can pivot to `capy vault show`/the TUI), the session title, its project and end
// date, the first-line anchor, the producing platform and — for a sub-agent
// session — its parent, then the pre-windowed chunk snippet.
//
// The platform is the STORED token ("claude-code", "codex"), the same value
// `capy vault list --platform` accepts and `--json` emits, so the assistant can
// pivot without translating a display name. It is appended for every hit,
// Claude included, so a mixed result set reads uniformly; a Claude hit is
// otherwise byte-identical to the pre-platform format. The parent marker uses
// the platform's short-id rule (12 chars for Codex) — parent and child always
// share a platform (see vault.Session.ParentUUID).
func formatVaultHit(r vault.SearchResult) string {
	title := r.Title
	if title == "" {
		title = "(untitled session)"
	}

	platform := r.Platform.OrClaude()
	meta := make([]string, 0, 5)
	if r.Project != "" {
		meta = append(meta, r.Project)
	}
	if !r.EndTime.IsZero() {
		meta = append(meta, r.EndTime.Format("2006-01-02"))
	}
	meta = append(meta, fmt.Sprintf("line %d", r.LineIndex), platform.String())
	if r.ParentUUID != "" {
		meta = append(meta, "child of "+platform.ShortID(r.ParentUUID))
	}

	return fmt.Sprintf("--- [session:%s] ---\n### %s\n%s\n\n%s",
		r.SessionUUID, title, strings.Join(meta, " · "), r.Snippet)
}

// parseDateArg parses a before/after argument as either a YYYY-MM-DD date or an
// RFC3339 timestamp. A date-only before is snapped to end-of-day so the whole
// target day is inclusive (mirrors cmd/capy's parseDateFlag). Empty == unset.
// time.Parse("2006-01-02", …) returns UTC midnight, so +24h-1s yields 23:59:59
// UTC — matching vault_sessions.end_time's UTC storage (no DST ambiguity).
func parseDateArg(s string, endOfDay bool) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		if endOfDay {
			t = t.Add(24*time.Hour - time.Second)
		}
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid date %q (want YYYY-MM-DD or RFC3339)", s)
}
