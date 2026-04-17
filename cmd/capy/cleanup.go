package main

import (
	"fmt"
	"time"

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

			force, _ := cmd.Flags().GetBool("force")
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			if force {
				dryRun = false
			}

			dbPath := cfg.ResolveDBPath(projectDir)
			st := store.NewContentStore(dbPath, projectDir, 0)
			defer st.Close()

			ephemeralTTL := time.Duration(cfg.Store.Cleanup.EphemeralTTLHours) * time.Hour
			pruned, err := st.Cleanup(dryRun, ephemeralTTL)
			if err != nil {
				return fmt.Errorf("cleanup failed: %w", err)
			}

			if dryRun {
				if len(pruned) == 0 {
					fmt.Println("capy cleanup: no evictable sources found")
				} else {
					fmt.Printf("capy: would remove %d evictable source(s):\n", len(pruned))
					for _, s := range pruned {
						fmt.Printf("  - %s (%s)\n", s.Label, formatCleanupDetail(s))
					}
					fmt.Println("\nUse --force to actually remove these sources.")
				}
			} else {
				if len(pruned) == 0 {
					fmt.Println("capy cleanup: no evictable sources found")
				} else {
					fmt.Printf("capy: removed %d evictable source(s)\n", len(pruned))
					for _, s := range pruned {
						fmt.Printf("  - %s (%s)\n", s.Label, formatCleanupDetail(s))
					}
				}
			}

			return nil
		},
	}
	cmd.Flags().Bool("dry-run", true, "show what would be removed without removing")
	cmd.Flags().Bool("force", false, "actually remove stale data")
	return cmd
}

// formatCleanupDetail renders per-source eviction detail, switching between
// retention-score framing and TTL-age framing based on EvictionReason.
func formatCleanupDetail(s store.SourceInfo) string {
	if s.EvictionReason == "ttl" {
		return fmt.Sprintf("reason: ttl, age: %s", time.Since(s.IndexedAt).Truncate(time.Minute))
	}
	return fmt.Sprintf("reason: retention, score: %.2f, last accessed: %s",
		s.RetentionScore, s.LastAccessedAt.Format("2006-01-02"))
}
