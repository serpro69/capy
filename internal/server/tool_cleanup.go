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
	purgeEphemeral := false
	if v, ok := args["purge_ephemeral"]; ok {
		if b, ok := v.(bool); ok {
			purgeEphemeral = b
		}
	}
	purgeSession := false
	if v, ok := args["purge_session"]; ok {
		if b, ok := v.(bool); ok {
			purgeSession = b
		}
	}
	purgeAll := false
	if v, ok := args["purge_all"]; ok {
		if b, ok := v.(bool); ok {
			purgeAll = b
		}
	}

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

	// Full project-scope reset: wipe every source/chunk/vocab row and reset
	// session stats. Mutually exclusive with all other cleanup modes above.
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
		return s.trackToolResponse("capy_cleanup", textResult(text)), nil
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
		return s.trackToolResponse("capy_cleanup", textResult(text)), nil
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
		return s.trackToolResponse("capy_cleanup",
			textResult("No evictable sources found.")), nil
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

	return s.trackToolResponse("capy_cleanup", textResult(strings.Join(lines, "\n"))), nil
}
