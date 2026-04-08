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
	pruned, err := st.Cleanup(dryRun)
	if err != nil {
		return errorResult(fmt.Sprintf("Cleanup error: %v", err)), nil
	}

	if len(pruned) == 0 {
		return s.trackToolResponse("capy_cleanup",
			textResult("No evictable sources found.")), nil
	}

	var lines []string
	if dryRun {
		lines = append(lines, fmt.Sprintf("## Cleanup preview (dry run) — %d sources would be removed:", len(pruned)))
	} else {
		lines = append(lines, fmt.Sprintf("## Cleanup — %d sources removed:", len(pruned)))
	}

	lines = append(lines, "",
		"| Source | Score | Age | Chunks |",
		"|--------|-------|-----|--------|",
	)
	for _, src := range pruned {
		ageDays := int(time.Since(src.IndexedAt).Hours() / 24)
		lines = append(lines, fmt.Sprintf("| %s | %.2f | %dd | %d |", src.Label, src.RetentionScore, ageDays, src.ChunkCount))
	}

	if dryRun {
		lines = append(lines, "", "Run with `dry_run: false` to actually remove these sources.")
	}

	return s.trackToolResponse("capy_cleanup", textResult(strings.Join(lines, "\n"))), nil
}
