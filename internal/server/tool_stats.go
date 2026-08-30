package server

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/vault"
)

func (s *Server) handleStats(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
		kbStats, err := s.store.Stats(s.ephemeralTTL(), s.sessionTTL())
		if err == nil && kbStats.SourceCount > 0 {
			lines = append(lines, "", "### Knowledge Base", "",
				"| Metric | Value |",
				"|--------|------:|",
				fmt.Sprintf("| Sources | %d (durable: %d, ephemeral: %d, session: %d) |",
					kbStats.SourceCount, kbStats.DurableSourceCount, kbStats.EphemeralSourceCount, kbStats.SessionSourceCount),
				fmt.Sprintf("| Chunks | %d |", kbStats.ChunkCount),
				fmt.Sprintf("| Vocabulary | %d terms |", kbStats.VocabCount),
				fmt.Sprintf("| DB size | %s |", formatBytes(kbStats.DBSizeBytes)),
			)
			if kbStats.DurableSourceCount > 0 {
				lines = append(lines, "",
					"#### Durable retention tiers",
					"| Tier | Count |",
					"|------|------:|",
					fmt.Sprintf("| hot | %d |", kbStats.DurableHotCount),
					fmt.Sprintf("| warm | %d |", kbStats.DurableWarmCount),
					fmt.Sprintf("| cold | %d |", kbStats.DurableColdCount),
					fmt.Sprintf("| evictable | %d |", kbStats.DurableEvictableCount),
				)
			}
			if kbStats.EphemeralSourceCount > 0 {
				lines = append(lines, "",
					"#### Ephemeral TTL buckets",
					"| Bucket | Count |",
					"|--------|------:|",
					fmt.Sprintf("| fresh | %d |", kbStats.EphemeralFreshCount),
					fmt.Sprintf("| stale | %d |", kbStats.EphemeralStaleCount),
				)
			}
			if kbStats.SessionSourceCount > 0 {
				// Deprecated tier: the knowledge.db session sweep was removed
				// (vault-session-search D8) — the vault is the session store now.
				// These rows are pre-removal leftovers draining by TTL; name the
				// reclaim command so they don't linger silently.
				lines = append(lines, "",
					"#### Session TTL buckets (legacy — draining)",
					"| Bucket | Count |",
					"|--------|------:|",
					fmt.Sprintf("| fresh | %d |", kbStats.SessionFreshCount),
					fmt.Sprintf("| stale | %d |", kbStats.SessionStaleCount),
					"",
					"_Legacy knowledge.db session rows. Reclaim now with `capy_cleanup purge_session`; new sessions live in the vault._",
				)
			}
		}
	}

	// Vault section (opt-in): session-archive size and — loudly — any reindex
	// backlog, since the chunk backfill is a manual `capy vault reindex` (design
	// vault-session-search D4).
	if _, err := vault.RequireVaultKey(); err == nil {
		switch vs, err := s.vaultStats(ctx); {
		case err != nil:
			// An unreadable vault is a different state than an empty one — say so
			// (mirrors capy_doctor's Warn) instead of silently omitting the section.
			lines = append(lines, "", "### Vault", "",
				fmt.Sprintf("Error reading vault stats: %v", err))
		case vs.Sessions > 0:
			lines = append(lines, "", "### Vault", "",
				"| Metric | Value |",
				"|--------|------:|",
				fmt.Sprintf("| Archived sessions | %d |", vs.Sessions),
				fmt.Sprintf("| Content size | %s |", formatBytes(vs.TotalBytes)),
				fmt.Sprintf("| Index version | v%d |", vs.IndexVersion),
			)
			if vs.OutdatedSessions > 0 {
				lines = append(lines,
					fmt.Sprintf("| **Reindex backlog** | **%d session(s) below v%d — run `capy vault reindex`** |",
						vs.OutdatedSessions, vs.IndexVersion),
				)
			}
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
