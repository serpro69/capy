package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

func (s *Server) handleCleanup(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	dryRun := true
	if v, ok := req.GetArguments()["dry_run"]; ok {
		if b, ok := v.(bool); ok {
			dryRun = b
		}
	}

	st := s.getStore()
	ephemeralTTL := time.Duration(s.config.Store.Cleanup.EphemeralTTLHours) * time.Hour
	pruned, err := st.Cleanup(dryRun, ephemeralTTL)
	if err != nil {
		return errorResult(fmt.Sprintf("Cleanup error: %v", err)), nil
	}

	if len(pruned) == 0 {
		return s.trackToolResponse("capy_cleanup",
			textResult("No evictable sources found.")), nil
	}

	var durableN, ephemeralN int
	for _, src := range pruned {
		if src.EvictionReason == "ttl" {
			ephemeralN++
		} else {
			durableN++
		}
	}

	var lines []string
	summary := fmt.Sprintf("%d durable (retention), %d ephemeral (TTL)", durableN, ephemeralN)
	if dryRun {
		lines = append(lines, fmt.Sprintf("## Cleanup preview (dry run) — %d sources would be removed: %s", len(pruned), summary))
	} else {
		lines = append(lines, fmt.Sprintf("## Cleanup — %d sources removed: %s", len(pruned), summary))
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
