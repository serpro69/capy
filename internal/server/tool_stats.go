package server

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

func (s *Server) handleStats(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	snap := s.stats.Snapshot()

	totalBytesReturned := int64(0)
	for _, b := range snap.BytesReturned {
		totalBytesReturned += b
	}
	totalCalls := 0
	for _, c := range snap.Calls {
		totalCalls += c
	}
	uptimeMin := time.Since(snap.SessionStart).Minutes()

	// Savings calculation (includes cache savings — data that would have been fetched)
	keptOut := snap.BytesIndexed + snap.BytesSandboxed
	totalProcessed := keptOut + totalBytesReturned + snap.CacheBytesSaved
	savingsRatio := float64(totalProcessed) / float64(max(totalBytesReturned, 1))
	reductionPct := 0.0
	if totalProcessed > 0 {
		reductionPct = (1 - float64(totalBytesReturned)/float64(totalProcessed)) * 100
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("## capy — Session Report (%.1f min)", uptimeMin))

	lines = append(lines, "", "### Context Window Protection", "")

	if totalCalls == 0 {
		lines = append(lines, "No capy tool calls yet. Use `batch_execute`, `execute`, or `fetch_and_index` to keep raw output out of your context window.")
	} else {
		lines = append(lines,
			"| Metric | Value |",
			"|--------|------:|",
			fmt.Sprintf("| Total data processed | **%s** |", formatBytes(totalProcessed)),
			fmt.Sprintf("| Kept in sandbox (never entered context) | **%s** |", formatBytes(keptOut)),
			fmt.Sprintf("| Entered context | %s |", formatBytes(totalBytesReturned)),
			fmt.Sprintf("| Estimated tokens saved | ~%d |", keptOut/4),
			fmt.Sprintf("| **Context savings** | **%.1fx (%.0f%% reduction)** |", savingsRatio, reductionPct),
		)

		// Per-tool breakdown
		toolNames := make(map[string]struct{})
		for t := range snap.Calls {
			toolNames[t] = struct{}{}
		}
		for t := range snap.BytesReturned {
			toolNames[t] = struct{}{}
		}

		if len(toolNames) > 0 {
			sorted := make([]string, 0, len(toolNames))
			for t := range toolNames {
				sorted = append(sorted, t)
			}
			slices.Sort(sorted)

			lines = append(lines, "",
				"| Tool | Calls | Context | Tokens |",
				"|------|------:|--------:|-------:|",
			)
			for _, tool := range sorted {
				calls := snap.Calls[tool]
				bytes := snap.BytesReturned[tool]
				tokens := bytes / 4
				lines = append(lines, fmt.Sprintf("| %s | %d | %s | ~%d |", tool, calls, formatBytes(bytes), tokens))
			}
			lines = append(lines, fmt.Sprintf("| **Total** | **%d** | **%s** | **~%d** |",
				totalCalls, formatBytes(totalBytesReturned), totalBytesReturned/4))
		}

		if keptOut > 0 {
			lines = append(lines, "",
				fmt.Sprintf("Without capy, **%s** of raw output would flood your context window. Instead, **%.0f%%** stayed in sandbox.",
					formatBytes(totalProcessed), reductionPct))
		}
	}

	// Knowledge base stats (only if store was initialized)
	if s.store != nil {
		kbStats, err := s.store.Stats()
		if err == nil && kbStats.SourceCount > 0 {
			lines = append(lines, "", "### Knowledge Base", "",
				"| Metric | Value |",
				"|--------|------:|",
				fmt.Sprintf("| Sources | %d |", kbStats.SourceCount),
				fmt.Sprintf("| Chunks | %d |", kbStats.ChunkCount),
				fmt.Sprintf("| Vocabulary | %d terms |", kbStats.VocabCount),
				fmt.Sprintf("| DB size | %s |", formatBytes(kbStats.DBSizeBytes)),
				fmt.Sprintf("| Tier: hot | %d |", kbStats.HotCount),
				fmt.Sprintf("| Tier: warm | %d |", kbStats.WarmCount),
				fmt.Sprintf("| Tier: cold | %d |", kbStats.ColdCount),
			)
		}
	}

	// TTL cache section
	if snap.CacheHits > 0 {
		ttlHours := s.config.Store.Cache.FetchTTLHours
		ttlHoursLeft := max(0, ttlHours-int(time.Since(snap.SessionStart).Hours()))
		lines = append(lines, "", "### TTL Cache", "",
			"| Metric | Value |",
			"|--------|------:|",
			fmt.Sprintf("| Cache hits | **%d** |", snap.CacheHits),
			fmt.Sprintf("| Data avoided by cache | **%s** |", formatBytes(snap.CacheBytesSaved)),
			fmt.Sprintf("| Network requests saved | **%d** |", snap.CacheHits),
			fmt.Sprintf("| TTL remaining | **~%dh** |", ttlHoursLeft),
		)
	}

	lines = append(lines, "", "---",
		"_Display this entire report as-is. Do NOT summarize or collapse any section._")

	text := strings.Join(lines, "\n")
	return s.trackToolResponse("capy_stats", textResult(text)), nil
}

// formatBytes formats a byte count as KB or MB.
func formatBytes(b int64) string {
	if b >= 1024*1024 {
		return fmt.Sprintf("%.1fMB", float64(b)/(1024*1024))
	}
	return fmt.Sprintf("%.1fKB", float64(b)/1024)
}
