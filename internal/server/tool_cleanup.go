package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

func (s *Server) handleCleanup(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	maxAgeDays := int(req.GetFloat("max_age_days", 30))
	dryRun := true
	if v, ok := req.GetArguments()["dry_run"]; ok {
		if b, ok := v.(bool); ok {
			dryRun = b
		}
	}

	st := s.getStore()
	pruned, err := st.Cleanup(maxAgeDays, dryRun)
	if err != nil {
		return errorResult(fmt.Sprintf("Cleanup error: %v", err)), nil
	}

	if len(pruned) == 0 {
		return s.trackToolResponse("capy_cleanup",
			textResult(fmt.Sprintf("No cold sources older than %d days found.", maxAgeDays))), nil
	}

	var lines []string
	if dryRun {
		lines = append(lines, fmt.Sprintf("## Cleanup preview (dry run) — %d sources would be removed:", len(pruned)))
	} else {
		lines = append(lines, fmt.Sprintf("## Cleanup — %d sources removed:", len(pruned)))
	}

	lines = append(lines, "",
		"| Source | Age | Chunks |",
		"|--------|-----|--------|",
	)
	for _, src := range pruned {
		ageDays := int(time.Since(src.IndexedAt).Hours() / 24)
		lines = append(lines, fmt.Sprintf("| %s | %dd | %d |", src.Label, ageDays, src.ChunkCount))
	}

	if dryRun {
		lines = append(lines, "", "Run with `dry_run: false` to actually remove these sources.")
	}

	return s.trackToolResponse("capy_cleanup", textResult(strings.Join(lines, "\n"))), nil
}
