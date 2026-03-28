package server

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/platform"
)

func (s *Server) handleDoctor(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Runtimes — convert executor types to strings for shared check
	runtimes := s.executor.Runtimes()
	runtimeStrs := make(map[string]string, len(runtimes))
	for lang, path := range runtimes {
		runtimeStrs[string(lang)] = path
	}

	// Security stats
	totalDeny := 0
	for _, p := range s.security {
		totalDeny += len(p.Deny)
	}

	// DB path
	dbPath := ""
	if s.config != nil {
		dbPath = s.config.ResolveDBPath(s.projectDir)
	}

	// Shared checks
	results := []platform.CheckResult{
		platform.CheckVersion(),
		platform.CheckRuntimes(runtimeStrs, executor.TotalLanguages),
	}

	// FTS5 — use the store directly since we have it
	fts5OK := false
	func() {
		defer func() { recover() }()
		st := s.getStore()
		if st != nil {
			_, err := st.Stats()
			fts5OK = err == nil
		}
	}()
	if fts5OK {
		results = append(results, platform.CheckResult{Name: "FTS5", Status: platform.Pass, Detail: "available"})
	} else {
		results = append(results, platform.CheckResult{Name: "FTS5", Status: platform.Fail, Detail: "unavailable (binary may not be built with -tags fts5)"})
	}

	// Config
	results = append(results, platform.CheckConfig(s.projectDir, dbPath))

	// Hook and MCP registration
	results = append(results,
		platform.CheckHookRegistration(s.projectDir),
		platform.CheckMCPRegistration(s.projectDir),
	)

	// Knowledge base — use store directly for richer stats
	if s.store != nil {
		kbStats, err := s.store.Stats()
		if err == nil {
			results = append(results, platform.CheckResult{
				Name:   "Knowledge base",
				Status: platform.Pass,
				Detail: fmt.Sprintf("%d sources, %d chunks", kbStats.SourceCount, kbStats.ChunkCount),
			})
		} else {
			results = append(results, platform.CheckResult{
				Name:   "Knowledge base",
				Status: platform.Warn,
				Detail: fmt.Sprintf("error reading stats (%v)", err),
			})
		}
	} else {
		results = append(results, platform.CheckResult{
			Name:   "Knowledge base",
			Status: platform.Warn,
			Detail: "not initialized (lazy — will init on first use)",
		})
	}

	// Security
	results = append(results, platform.CheckSecurity(totalDeny, len(s.security)))

	// Project dir
	results = append(results, platform.CheckResult{
		Name: "Project", Status: platform.Pass, Detail: s.projectDir,
	})

	text := platform.FormatDiagnostics(results)
	return s.trackToolResponse("capy_doctor", textResult(text)), nil
}
