package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/store"
)

func (s *Server) handleCleanup(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	dryRun := true
	if v, ok := args["dry_run"]; ok {
		if b, ok := v.(bool); ok {
			dryRun = b
		}
	}
	sourceLabel := req.GetString("source", "")
	purgeEphemeral := boolArg(args, "purge_ephemeral")
	purgeSession := boolArg(args, "purge_session")
	purgeAll := boolArg(args, "purge_all")
	optimize := boolArg(args, "optimize")
	vacuum := boolArg(args, "vacuum")
	reclaim := optimize || vacuum

	if purgeEphemeral && purgeSession {
		return errorResult("purge_ephemeral and purge_session are mutually exclusive"), nil
	}
	if sourceLabel != "" && (purgeEphemeral || purgeSession) {
		return errorResult("source cannot be combined with purge_ephemeral or purge_session"), nil
	}
	if purgeAll && (sourceLabel != "" || purgeEphemeral || purgeSession) {
		return errorResult("purge_all cannot be combined with source, purge_ephemeral, or purge_session"), nil
	}

	st := s.getStore()

	// Standalone reclamation: optimize/vacuum with no eviction requested (the
	// dry_run default and no source/purge mode). Mirrors `capy cleanup
	// --optimize`: reclamation is maintenance, not data eviction, so it
	// deliberately ignores the dry_run default (ADR-029 §4) — gating it would
	// make `optimize: true` a silent no-op. With dry_run=false or any eviction
	// mode set, evict first and reclaim below.
	evictionRequested := !dryRun || sourceLabel != "" || purgeEphemeral || purgeSession || purgeAll
	if reclaim && !evictionRequested {
		text, err := s.runReclaim(st, optimize, vacuum)
		if err != nil {
			return errorResult(fmt.Sprintf("Cleanup error: %v", err)), nil
		}
		return s.trackToolResponse("capy_cleanup",
			textResult(fmt.Sprintf("## Cleanup (%s)\n\n%s", reclaimMode(optimize), text))), nil
	}

	// Full project-scope reset: wipe every source/chunk/vocab row and reset
	// session stats. Mutually exclusive with all other eviction modes above.
	if purgeAll {
		counts, err := st.PurgeAll(dryRun)
		if err != nil {
			return errorResult(fmt.Sprintf("Cleanup error: %v", err)), nil
		}
		var text string
		if dryRun {
			text = fmt.Sprintf("## Cleanup (purge all) preview (dry run)\n\n"+
				"Would purge %d sources, %d chunks, %d vocab entries. "+
				"Run with `dry_run: false` to reset the knowledge base.",
				counts.Sources, counts.Chunks, counts.Vocab)
		} else {
			s.stats.Reset()
			text = fmt.Sprintf("## Cleanup (purge all)\n\n"+
				"Purged %d sources, %d chunks, %d vocab entries. Knowledge base reset.",
				counts.Sources, counts.Chunks, counts.Vocab)
		}
		return s.finishCleanup(st, text, dryRun, optimize, vacuum)
	}

	// Source-specific eviction: single source by exact label.
	if sourceLabel != "" {
		evicted, err := st.EvictByLabel(sourceLabel, dryRun)
		if err != nil {
			return errorResult(fmt.Sprintf("Cleanup error: %v", err)), nil
		}
		action := "would be removed"
		if !dryRun {
			action = "removed"
		}
		text := fmt.Sprintf("Source %q (%s, %d chunks) %s.", evicted.Label, evicted.Kind, evicted.ChunkCount, action)
		return s.finishCleanup(st, text, dryRun, optimize, vacuum)
	}

	ephTTL := s.ephemeralTTL()
	sessTTL := s.sessionTTL()
	var pruned []store.SourceInfo
	var err error
	switch {
	case purgeEphemeral:
		pruned, err = st.PurgeEphemeral(dryRun, ephTTL)
	case purgeSession:
		pruned, err = st.PurgeSession(dryRun, sessTTL)
	default:
		pruned, err = st.Cleanup(dryRun, ephTTL, sessTTL)
	}
	if err != nil {
		return errorResult(fmt.Sprintf("Cleanup error: %v", err)), nil
	}

	if len(pruned) == 0 {
		return s.finishCleanup(st, "No evictable sources found.", dryRun, optimize, vacuum)
	}

	var durableN, ephemeralN, sessionN, oversizedN int
	for _, src := range pruned {
		if src.EvictionReason == "oversized" {
			oversizedN++
			continue
		}
		switch src.Kind {
		case store.KindSession:
			sessionN++
		case store.KindEphemeral:
			ephemeralN++
		default:
			durableN++
		}
	}

	var lines []string
	heading := "Cleanup"
	switch {
	case purgeEphemeral:
		heading = "Cleanup (ephemeral purge)"
	case purgeSession:
		heading = "Cleanup (session purge)"
	}
	var parts []string
	if oversizedN > 0 {
		parts = append(parts, fmt.Sprintf("%d oversized", oversizedN))
	}
	parts = append(parts,
		fmt.Sprintf("%d durable (retention)", durableN),
		fmt.Sprintf("%d ephemeral (TTL)", ephemeralN),
		fmt.Sprintf("%d session (TTL)", sessionN),
	)
	summary := strings.Join(parts, ", ")
	if dryRun {
		lines = append(lines, fmt.Sprintf("## %s preview (dry run) — %d sources would be removed: %s", heading, len(pruned), summary))
	} else {
		lines = append(lines, fmt.Sprintf("## %s — %d sources removed: %s", heading, len(pruned), summary))
	}

	lines = append(lines, "",
		"| Source | Reason | Score | Age | Chunks |",
		"|--------|--------|-------|-----|--------|",
	)
	for _, src := range pruned {
		ageHours := time.Since(src.IndexedAt).Hours()
		ageStr := fmt.Sprintf("%dd", int(ageHours/24))
		if src.EvictionReason == "ttl" && ageHours < 48 {
			ageStr = fmt.Sprintf("%.1fh", ageHours)
		}
		lines = append(lines, fmt.Sprintf("| %s | %s | %.2f | %s | %d |",
			src.Label, src.EvictionReason, src.RetentionScore, ageStr, src.ChunkCount))
	}

	if dryRun {
		lines = append(lines, "", "Run with `dry_run: false` to actually remove these sources.")
	}

	return s.finishCleanup(st, strings.Join(lines, "\n"), dryRun, optimize, vacuum)
}

// boolArg reads an optional boolean tool argument, defaulting to false when
// absent or not a bool.
func boolArg(args map[string]any, name string) bool {
	if v, ok := args[name]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

// finishCleanup appends the reclamation outcome to an eviction result. After a
// real (non-dry-run) eviction it runs optimize/vacuum; after a dry run it says
// loudly that reclamation was skipped instead of silently no-op'ing — the same
// contract as the CLI's post-cleanup path.
func (s *Server) finishCleanup(st *store.ContentStore, text string, dryRun, optimize, vacuum bool) (*mcp.CallToolResult, error) {
	if optimize || vacuum {
		mode := reclaimMode(optimize)
		if dryRun {
			text += fmt.Sprintf("\n\n_%s skipped (dry run) — run with `dry_run: false` to evict and reclaim, "+
				"or pass `%s: true` alone to reclaim without evicting._", mode, mode)
		} else {
			reclaimed, err := s.runReclaim(st, optimize, vacuum)
			if err != nil {
				return errorResult(fmt.Sprintf("Cleanup error: %v", err)), nil
			}
			text += "\n\n" + reclaimed
		}
	}
	return s.trackToolResponse("capy_cleanup", textResult(text)), nil
}

// reclaimMode names the requested reclamation for headings and notes; optimize
// supersedes vacuum.
func reclaimMode(optimize bool) string {
	if optimize {
		return "optimize"
	}
	return "vacuum"
}

// runReclaim performs the requested reclamation and returns a one-line
// description of what ran. optimize does the full FTS-rebuild + VACUUM — the
// only path that reclaims FTS tombstone bloat (store.Optimize / ADR-029);
// vacuum alone only compacts the freelist. optimize supersedes vacuum since it
// already vacuums.
func (s *Server) runReclaim(st *store.ContentStore, optimize, vacuum bool) (string, error) {
	switch {
	case optimize:
		if err := st.Optimize(); err != nil {
			return "", err
		}
		return "Optimize complete: FTS indexes rebuilt, VACUUM reclaimed the freed pages.", nil
	case vacuum:
		if err := st.Vacuum(); err != nil {
			return "", fmt.Errorf("vacuum failed: %w", err)
		}
		return "Vacuum complete: freelist pages reclaimed.", nil
	}
	return "", nil
}
