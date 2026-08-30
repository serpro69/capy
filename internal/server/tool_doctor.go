package server

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/platform"
	"github.com/serpro69/capy/internal/vault"
)

func (s *Server) handleDoctor(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
	ephemeralTTL := s.ephemeralTTL()
	sessionTTL := s.sessionTTL()

	// Shared checks
	results := []platform.CheckResult{
		platform.CheckVersion(),
		platform.CheckRuntimes(runtimeStrs, executor.TotalLanguages),
	}

	// FTS5 — use the store directly since we have it
	fts5OK := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Debug("FTS5 check panicked", "panic", r)
			}
		}()
		st := s.getStore()
		if st != nil {
			_, err := st.Stats(ephemeralTTL, sessionTTL)
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
		kbStats, err := s.store.Stats(ephemeralTTL, sessionTTL)
		if err == nil {
			results = append(results, platform.CheckResult{
				Name:   "Knowledge base",
				Status: platform.Pass,
				Detail: fmt.Sprintf("%d sources, %d chunks", kbStats.SourceCount, kbStats.ChunkCount),
			})
			// Legacy session rows: the knowledge.db session sweep was removed
			// (vault-session-search D8); the vault is now the session store. Any
			// `kind='session'` rows are pre-removal leftovers draining by TTL —
			// surface them loudly with the reclaim command (design D4: report
			// both the knowledge.db reclaim and the vault reindex backlog).
			if kbStats.SessionSourceCount > 0 {
				results = append(results, platform.CheckResult{
					Name:   "Legacy sessions",
					Status: platform.Warn,
					Detail: fmt.Sprintf("%d legacy knowledge.db session row(s) — reclaim now with `capy_cleanup purge_session` (the vault is the session store)",
						kbStats.SessionSourceCount),
				})
			}
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

	// Vault — opt-in session archive. When enabled, surface the reindex backlog
	// loudly (design vault-session-search D4: the chunk backfill is manual via
	// `capy vault reindex`, so its pendency must be visible, not silent).
	results = append(results, s.vaultCheck(ctx))

	// Security
	results = append(results, platform.CheckSecurity(totalDeny, len(s.security)))

	// Project dir
	results = append(results, platform.CheckResult{
		Name: "Project", Status: platform.Pass, Detail: s.projectDir,
	})

	text := platform.FormatDiagnostics(results)
	return s.trackToolResponse("capy_doctor", textResult(text)), nil
}

// vaultCheck reports the vault's health for capy_doctor: disabled (opt-in key
// unset), unreadable, or enabled with its session count and — when any archived
// session predates the current indexer — the reindex backlog and the command
// that clears it.
func (s *Server) vaultCheck(ctx context.Context) platform.CheckResult {
	if _, err := vault.RequireVaultKey(); err != nil {
		return platform.CheckResult{
			Name:   "Vault",
			Status: platform.Warn,
			Detail: "disabled (CAPY_VAULT_KEY not set) — sessions are not archived",
		}
	}

	vs, err := s.vaultStats(ctx)
	if err != nil {
		return platform.CheckResult{
			Name:   "Vault",
			Status: platform.Warn,
			Detail: fmt.Sprintf("error reading stats (%v)", err),
		}
	}

	detail := fmt.Sprintf("%d sessions archived", vs.Sessions)
	if vs.OutdatedSessions > 0 {
		return platform.CheckResult{
			Name:   "Vault",
			Status: platform.Warn,
			Detail: fmt.Sprintf("%s; %d below index v%d — run `capy vault reindex` to make them chunk-searchable",
				detail, vs.OutdatedSessions, vs.IndexVersion),
		}
	}
	return platform.CheckResult{Name: "Vault", Status: platform.Pass, Detail: detail}
}

// vaultStats reads the vault's stats through the server-owned long-lived handle
// (getVault); it neither opens nor closes a connection — shutdown() owns the
// close/checkpoint. Both callers gate on RequireVaultKey first, so getVault is
// expected non-nil; a nil handle (vault disabled) is reported loudly rather than
// nil-panicking.
func (s *Server) vaultStats(ctx context.Context) (*vault.VaultStats, error) {
	vs := s.getVault()
	if vs == nil {
		return nil, fmt.Errorf("vault is disabled (%s not set)", "CAPY_VAULT_KEY")
	}
	return vs.Stats(ctx)
}
