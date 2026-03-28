package server

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/version"
)

func (s *Server) handleDoctor(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var lines []string
	lines = append(lines, "## capy doctor", "")

	// Version
	lines = append(lines, fmt.Sprintf("- [x] Version: %s", version.Version))

	// Runtimes
	runtimes := s.executor.Runtimes()
	if len(runtimes) > 0 {
		langs := make([]string, 0, len(runtimes))
		for lang := range runtimes {
			langs = append(langs, string(lang))
		}
		slices.Sort(langs)
		lines = append(lines, fmt.Sprintf("- [x] Runtimes: %d/%d (%s)",
			len(runtimes), executor.TotalLanguages, strings.Join(langs, ", ")))
	} else {
		lines = append(lines, "- [ ] Runtimes: none detected")
	}

	// FTS5 — try initializing the store
	fts5OK := false
	func() {
		defer func() { recover() }() // guard against panics
		st := s.getStore()
		if st != nil {
			_, err := st.Stats()
			fts5OK = err == nil
		}
	}()
	if fts5OK {
		lines = append(lines, "- [x] FTS5: available")
	} else {
		lines = append(lines, "- [ ] FTS5: unavailable (binary may not be built with -tags fts5)")
	}

	// Config
	if s.config != nil {
		lines = append(lines, fmt.Sprintf("- [x] Config: loaded (db path: %s)", s.config.ResolveDBPath(s.projectDir)))
	} else {
		lines = append(lines, "- [-] Config: using defaults")
	}

	// Knowledge base
	if s.store != nil {
		kbStats, err := s.store.Stats()
		if err == nil {
			lines = append(lines, fmt.Sprintf("- [x] Knowledge base: %d sources, %d chunks",
				kbStats.SourceCount, kbStats.ChunkCount))
		} else {
			lines = append(lines, fmt.Sprintf("- [-] Knowledge base: error reading stats (%v)", err))
		}
	} else {
		lines = append(lines, "- [-] Knowledge base: not initialized (lazy — will init on first use)")
	}

	// Security policies
	if len(s.security) > 0 {
		totalDeny := 0
		for _, p := range s.security {
			totalDeny += len(p.Deny)
		}
		lines = append(lines, fmt.Sprintf("- [x] Security: %d policy files, %d deny patterns",
			len(s.security), totalDeny))
	} else {
		lines = append(lines, "- [-] Security: no deny policies loaded")
	}

	// Project dir
	lines = append(lines, fmt.Sprintf("- [x] Project: %s", s.projectDir))

	text := strings.Join(lines, "\n")
	return s.trackToolResponse("capy_doctor", textResult(text)), nil
}
