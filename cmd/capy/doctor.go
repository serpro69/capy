package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/platform"
	"github.com/serpro69/capy/internal/security"
	"github.com/serpro69/capy/internal/store"
	"github.com/serpro69/capy/internal/vault"
	"github.com/spf13/cobra"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check capy installation and environment",
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir, _ := cmd.Flags().GetString("project-dir")
			if projectDir == "" {
				projectDir = config.DetectProjectRoot()
			}

			cfg, _ := config.Load(projectDir)
			if cfg == nil {
				cfg = config.DefaultConfig()
			}

			// Detect runtimes
			exec := executor.NewExecutor(projectDir, cfg.Executor.MaxOutputBytes)
			runtimes := exec.Runtimes()
			runtimeStrs := make(map[string]string, len(runtimes))
			for lang, path := range runtimes {
				runtimeStrs[string(lang)] = path
			}

			// Security policies
			policies := security.ReadBashPolicies(projectDir, "")
			totalDeny := 0
			for _, p := range policies {
				totalDeny += len(p.Deny)
			}

			// Run checks
			dbPath := cfg.ResolveDBPath(projectDir)
			results := []platform.CheckResult{
				platform.CheckVersion(),
				platform.CheckRuntimes(runtimeStrs, executor.TotalLanguages),
				platform.CheckFTS5(),
				platform.CheckConfig(projectDir, dbPath),
				platform.CheckHookRegistration(projectDir),
				platform.CheckMCPRegistration(projectDir),
				platform.CheckSecurity(totalDeny, len(policies)),
			}
			results = append(results, knowledgeBaseChecks(cfg, projectDir, dbPath)...)
			results = append(results,
				vaultCheck(cmd.Context()),
				platform.CheckResult{Name: "Project", Status: platform.Pass, Detail: projectDir},
			)

			fmt.Println(platform.FormatDiagnostics(results))
			return nil
		},
	}
}

// knowledgeBaseChecks mirrors the capy_doctor MCP tool's knowledge-base
// reporting: live source/chunk counts plus the "Legacy sessions" warning for
// leftover knowledge.db session rows (the vault is the session store, ADR-027).
// A DB that does not exist yet is reported without opening the store, which
// would otherwise create it as a side effect of a diagnostic.
func knowledgeBaseChecks(cfg *config.Config, projectDir, dbPath string) []platform.CheckResult {
	if _, err := os.Stat(dbPath); err != nil {
		return []platform.CheckResult{platform.CheckKnowledgeBase(dbPath)}
	}

	st := store.NewContentStore(dbPath, cfg.DBProjectDir(projectDir), 0, cfg.Store.MaxSourceBytes)
	defer st.Close()

	ephemeralTTL := time.Duration(cfg.Store.Cleanup.EphemeralTTLHours) * time.Hour
	sessionTTL := time.Duration(cfg.Store.Cleanup.SessionTTLDays) * 24 * time.Hour
	kbStats, err := st.Stats(ephemeralTTL, sessionTTL)
	if err != nil {
		return []platform.CheckResult{platform.CheckKnowledgeBaseError(err)}
	}

	results := []platform.CheckResult{platform.CheckKnowledgeBaseStats(kbStats.SourceCount, kbStats.ChunkCount)}
	if kbStats.SessionSourceCount > 0 {
		results = append(results, platform.CheckLegacySessions(kbStats.SessionSourceCount, "capy cleanup --kind session --force"))
	}
	return results
}

// vaultCheck mirrors the capy_doctor MCP tool's vault reporting: disabled
// (opt-in key unset), unreadable, or enabled with the session count and any
// reindex backlog. A vault DB that does not exist yet is reported as empty
// rather than created by the diagnostic.
func vaultCheck(ctx context.Context) platform.CheckResult {
	if _, err := vault.RequireVaultKey(); err != nil {
		return platform.CheckVaultDisabled()
	}
	path := vault.VaultDBPath()
	if _, err := os.Stat(path); err != nil {
		return platform.CheckResult{
			Name:   "Vault",
			Status: platform.Pass,
			Detail: fmt.Sprintf("enabled — no sessions archived yet (%s not created)", path),
		}
	}

	vs := vault.NewVaultStore(path)
	defer vs.Close()
	stats, err := vs.Stats(ctx)
	if err != nil {
		return platform.CheckVault(0, 0, 0, err)
	}
	return platform.CheckVault(stats.Sessions, stats.OutdatedSessions, stats.IndexVersion, nil)
}
