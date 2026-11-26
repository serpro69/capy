package server

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/sanitize"
	"github.com/serpro69/capy/internal/terminal"
	"github.com/serpro69/capy/internal/vault"
)

// vaultSearchMaxLimit caps results-per-query regardless of the caller's request,
// so an over-broad limit can't exhaust the context window.
const vaultSearchMaxLimit = 10

// handleVaultSearch serves capy_vault_search: it runs the shared retrieval
// engine over the vault's chunk corpus (VaultStore.SearchChunksPartial) and
// formats chunk-level hits from past sessions. It degrades loudly in two states
// the caller cannot see: the vault being disabled (no CAPY_VAULT_KEY) and the
// vault being enabled but holding a reindex backlog that can hide
// otherwise-matching sessions (design vault-session-search §D5).
func (s *Server) handleVaultSearch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return s.vaultSearch(ctx, req, searchReplyMaxBytes)
}

// vaultSearch serves capy_vault_search within a reply of at most replyMax
// UTF-8 bytes, under the same reply contract as capy_search: numbered query
// groups, whole ranked hits or references to an earlier passage, and a
// completion notice naming the inclusive retry when the budget stops the batch.
// It takes no shared search admission and has no budget footer.
// handleVaultSearch always passes searchReplyMaxBytes; tests pass a smaller
// reply to place a budget boundary without depending on exact rendered sizes.
func (s *Server) vaultSearch(ctx context.Context, req mcp.CallToolRequest, replyMax int) (*mcp.CallToolResult, error) {
	args := req.GetArguments()

	// Normalize: accept both queries (array) and query (string), mirroring
	// capy_search. A supplied malformed array is rejected even when a query
	// fallback is present: dropping a member would shift every later position.
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
		// Validation exits (queries/query above and here, project scope and
		// the date-parse errors below) return a bare errorResult without
		// trackToolResponse, matching handleSearch: malformed calls don't skew
		// per-tool usage stats. Enablement/degrade paths past this point ARE
		// tracked.
		return errorResult("Error: provide query or queries"), nil
	}

	project, projectPath, err := vaultProjectScope(req, s.projectDir)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	// Degrade loudly: session archival (and therefore vault search) is opt-in.
	vlt := s.getVault()
	if vlt == nil {
		return s.trackToolResponse("capy_vault_search", errorResult(
			"Vault search is disabled — set CAPY_VAULT_KEY to archive and search past sessions.\n"+
				"Once enabled, run `capy vault reindex` to update already-archived sessions to the current index.")), nil
	}

	// Bound the limit so a hallucinated `limit: 100` can't flood the context
	// window; the reply writer bounds the whole batch.
	limit := int(req.GetFloat("limit", 3))
	if limit <= 0 {
		limit = 3
	}
	limit = min(limit, vaultSearchMaxLimit)

	after, err := parseDateArg(req.GetString("after", ""), false)
	if err != nil {
		return errorResult("Error: " + err.Error()), nil
	}
	before, err := parseDateArg(req.GetString("before", ""), true)
	if err != nil {
		return errorResult("Error: " + err.Error()), nil
	}

	searchChunks := s.vaultChunkSearch
	if searchChunks == nil {
		searchChunks = vlt.SearchChunksPartial
	}

	w := newReplyWriter(replyMax)
	completion := ""
	// hasResults: some query that ran returned a hit, whether or not it fit.
	hasResults := false
	for i, q := range queryList {
		ordinal := i + 1
		if !w.canStartGroup() {
			completion = unsearchedNotice(ordinal, len(queryList))
			break
		}
		if s.searchDispatched != nil {
			s.searchDispatched(ordinal, q)
		}

		search, err := searchChunks(ctx, vault.SearchOptions{
			Query:       q,
			Project:     project,
			ProjectPath: projectPath,
			After:       after,
			Before:      before,
			Limit:       limit,
		})
		if err == nil {
			err = search.LayerErr
		}
		// Best-effort batch: a query's failure is reported in-band beside any
		// surviving ranked hits, so the remaining queries still run.
		var causes []string
		if err != nil {
			causes = []string{fmt.Sprintf("vault: %v", err)}
		}
		results := search.Results

		w.startGroup(queryFrame(ordinal, q, causes, len(results)))
		shown := 0
		for _, r := range results {
			if !w.addHit(renderVaultHit(r)) {
				break
			}
			shown++
		}
		if len(results) > 0 {
			hasResults = true
		}
		if shown < len(results) {
			completion = partialGroupNotice(ordinal, len(queryList), shown, len(results), limit)
			break
		}
	}

	// Request-wide notices share searchNoticeReserve; the query groups used
	// the rest of the reply.
	var parts []string
	if groups := w.String(); groups != "" {
		parts = append(parts, groups)
	}
	if completion != "" {
		parts = append(parts, completion)
	}
	// Degrade loudly on the enabled-but-empty state: a manual reindex backlog
	// (`capy vault reindex` is not automatic) can leave archived sessions missing
	// newer indexed content, so a reply whose queries returned no hit names the
	// command that fixes it, even when only a prefix of the batch ran.
	if !hasResults {
		if vs, sErr := vlt.Stats(ctx); sErr == nil && vs.OutdatedSessions > 0 {
			parts = append(parts, fmt.Sprintf(
				"%d archived session(s) were indexed by an older version (v%d) and may omit newer indexed content — run `capy vault reindex` to update them.",
				vs.OutdatedSessions, vs.IndexVersion))
		}
	}

	return s.trackToolResponse("capy_vault_search", textResult(strings.Join(parts, noticeSeparator))), nil
}

// vaultProjectScope resolves the existing MCP selectors without changing the
// server's physical project directory. Widening wins over explicit selection;
// an omitted/empty selector keeps imported-path scope even after reassignment.
// Validate all_projects before throttle/storage work, even for knowledge-only
// queries or an explicit star selector. Knowledge scope remains per-project.
func vaultProjectScope(req mcp.CallToolRequest, projectDir string) (project, projectPath string, err error) {
	allProjects, err := boolArg(req.GetArguments(), "all_projects", false)
	if err != nil {
		return "", "", err
	}
	explicit := req.GetString("project", "")
	switch {
	case allProjects || explicit == "*":
		return "", "", nil
	case explicit != "":
		return explicit, "", nil
	default:
		return "", projectDir, nil
	}
}

// renderVaultHit renders one chunk hit: a session:<uuid> label (so the
// assistant can pivot to `capy vault show`/the TUI), the session title, its
// project and end date, the raw-JSONL line where the chunk starts, the sidecar
// holding that line (when not the main transcript), the producing platform and
// — for a sub-agent session — its parent, then the pre-windowed chunk snippet.
//
// The platform is the STORED token ("claude-code", "codex"), the same value
// `capy vault list --platform` accepts and `--json` emits, so the assistant can
// pivot without translating a display name. It is appended for every hit,
// Claude included, so a mixed result set reads uniformly. The parent marker uses
// the platform's short-id rule (12 chars for Codex) — parent and child always
// share a platform (see vault.Session.ParentUUID).
//
// Every field is redacted on its complete value and bounded, so the block stays
// within searchHitMaxBytes.
//
// The hit is citable when its chunk coordinate is known: the full session
// UUID, sidecar ID, chunk index and first line locate the chunk, the window
// start and complete snippet place the passage in it, and the complete title,
// project, platform, parent and end time keep a session renamed or reassigned
// between queries from citing its old entry.
func renderVaultHit(r vault.SearchResult) renderedHit {
	title := r.Title
	if title == "" {
		title = "(untitled session)"
	}

	platform := r.Platform.OrClaude()
	meta := make([]string, 0, 5)
	if r.Project != "" {
		meta = append(meta, displayField(r.Project, vaultProjectMaxBytes))
	}
	if !r.EndTime.IsZero() {
		meta = append(meta, r.EndTime.Format("2006-01-02"))
	}
	meta = append(meta, fmt.Sprintf("chunk from line %d", r.LineIndex))
	if r.SubagentID != "" {
		meta = append(meta, sidecarAttribution(r))
	}
	meta = append(meta, displayField(platform.String(), vaultMetaFieldMaxBytes))
	if r.ParentUUID != "" {
		// Redact the complete ID before shortening it: a secret-shaped value
		// cut to its short form would no longer be recognized.
		parent := platform.ShortID(r.ParentUUID)
		if redacted := sanitize.StripSecrets(r.ParentUUID); redacted != r.ParentUUID {
			parent = redacted
		}
		meta = append(meta, "child of "+displayField(parent, vaultMetaFieldMaxBytes))
	}

	block := fmt.Sprintf("--- [session:%s] ---\n### %s\n%s\n\n%s",
		displayField(r.SessionUUID, vaultSessionIDMaxBytes), displayField(title, vaultTitleMaxBytes),
		strings.Join(meta, " · "), displayPassage(r.Snippet))
	if !r.HasChunkIndex {
		return renderedHit{block: block}
	}
	return renderedHit{
		block: block,
		key: passageKey{
			corpus: "vault",
			identity: passageIdentity(nil, r.SessionUUID, r.SubagentID, strconv.Itoa(r.ChunkIndex),
				strconv.Itoa(r.LineIndex), strconv.Itoa(r.SnippetStart), r.Snippet,
				r.Title, r.Project, string(r.Platform), r.ParentUUID,
				r.EndTime.UTC().Format(time.RFC3339Nano)),
			block: block,
		},
		citable: true,
	}
}

// sidecarAttribution names the sidecar holding a chunk's first line. When
// redaction or the length cap alters the ID, it says so and adds the
// session-wide chunk index, when known, to tell similar previews apart.
func sidecarAttribution(r vault.SearchResult) string {
	shown := "sidecar " + displayField(r.SubagentID, vaultSidecarIDMaxBytes)
	redacted := sanitize.StripSecrets(r.SubagentID)
	var altered []string
	if redacted != r.SubagentID {
		altered = append(altered, "redacted")
	}
	if len(terminal.EscapeField(redacted)) > vaultSidecarIDMaxBytes {
		altered = append(altered, "shortened")
	}
	if len(altered) == 0 {
		return shown
	}
	note := " (ID " + strings.Join(altered, " and ")
	if r.HasChunkIndex {
		note += fmt.Sprintf("; chunk %d", r.ChunkIndex)
	}
	return shown + note + ")"
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
	return time.Time{}, fmt.Errorf("invalid date %s (want YYYY-MM-DD or RFC3339)", argPreview(s))
}
