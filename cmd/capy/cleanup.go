package main

import (
	"fmt"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/store"
	"github.com/spf13/cobra"
)

func newCleanupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Remove stale data from the knowledge base",
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir, _ := cmd.Flags().GetString("project-dir")
			if projectDir == "" {
				projectDir = config.DetectProjectRoot()
			}

			cfg, _ := config.Load(projectDir)
			if cfg == nil {
				cfg = config.DefaultConfig()
			}

			maxAgeDays, _ := cmd.Flags().GetInt("max-age-days")
			force, _ := cmd.Flags().GetBool("force")
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			if force {
				dryRun = false
			}

			dbPath := cfg.ResolveDBPath(projectDir)
			st := store.NewContentStore(dbPath, projectDir)
			defer st.Close()

			pruned, err := st.Cleanup(maxAgeDays, dryRun)
			if err != nil {
				return fmt.Errorf("cleanup failed: %w", err)
			}

			if dryRun {
				if len(pruned) == 0 {
					fmt.Println("capy cleanup: no stale sources found")
				} else {
					fmt.Printf("capy: would remove %d stale source(s):\n", len(pruned))
					for _, s := range pruned {
						fmt.Printf("  - %s (last accessed: %s)\n", s.Label, s.LastAccessedAt.Format("2006-01-02"))
					}
					fmt.Println("\nUse --force to actually remove these sources.")
				}
			} else {
				if len(pruned) == 0 {
					fmt.Println("capy cleanup: no stale sources found")
				} else {
					fmt.Printf("capy: removed %d stale source(s)\n", len(pruned))
					for _, s := range pruned {
						fmt.Printf("  - %s\n", s.Label)
					}
				}
			}

			return nil
		},
	}
	cmd.Flags().Int("max-age-days", 30, "maximum age in days for cold sources")
	cmd.Flags().Bool("dry-run", true, "show what would be removed without removing")
	cmd.Flags().Bool("force", false, "actually remove stale data")
	return cmd
}
