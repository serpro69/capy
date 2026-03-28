package main

import (
	"fmt"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/platform"
	"github.com/serpro69/capy/internal/security"
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
				platform.CheckKnowledgeBase(dbPath),
				{Name: "Project", Status: platform.Pass, Detail: projectDir},
			}

			fmt.Println(platform.FormatDiagnostics(results))
			return nil
		},
	}
}
