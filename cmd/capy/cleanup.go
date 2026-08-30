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
			kind, _ := cmd.Flags().GetString("kind")
			if kind != "" && kind != "ephemeral" && kind != "session" {
				return fmt.Errorf("invalid --kind value %q: accepted values are \"ephemeral\", \"session\"", kind)
			}
			sourceLabel, _ := cmd.Flags().GetString("source")
			if sourceLabel != "" && kind != "" {
				return fmt.Errorf("--source cannot be combined with --kind")
			}

			vacuum, _ := cmd.Flags().GetBool("vacuum")
			optimize, _ := cmd.Flags().GetBool("optimize")

			dbPath := cfg.ResolveDBPath(projectDir)
			st := store.NewContentStore(dbPath, cfg.DBProjectDir(projectDir), 0, cfg.Store.MaxSourceBytes)
			defer st.Close()

			// Explicit optimize without cleanup: rebuild the FTS indexes to
			// release delete-tombstone segments, then VACUUM to reclaim the
			// freed pages. A plain VACUUM cannot shrink these — the pages are
			// held by the FTS5 data tables, not the freelist.
			//
			// The !force guard is deliberate: with --force the user asked to
			// evict data too, so we skip this standalone path and let the run
			// fall through to the cleanup + post-cleanup reclamation below.
			if optimize && !force && sourceLabel == "" && kind == "" {
				if err := reclaimFTS(st); err != nil {
					return err
				}
				return nil
			}

			// Explicit vacuum without cleanup.
			if vacuum && !force && sourceLabel == "" && kind == "" {
				if err := st.Vacuum(); err != nil {
					return fmt.Errorf("vacuum failed: %w", err)
				}
				fmt.Println("capy: vacuum complete")
				return nil
			}

			// Source-specific eviction.
			if sourceLabel != "" {
				evicted, err := st.EvictByLabel(sourceLabel, dryRun)
				if err != nil {
					return fmt.Errorf("cleanup failed: %w", err)
				}
				action := "would remove"
				if !dryRun {
					action = "removed"
				}
				fmt.Printf("capy: %s source %q (%s, %d chunks)\n", action, evicted.Label, evicted.Kind, evicted.ChunkCount)
				return nil
			}

			ephemeralTTL := time.Duration(cfg.Store.Cleanup.EphemeralTTLHours) * time.Hour
			sessionTTL := time.Duration(cfg.Store.Cleanup.SessionTTLDays) * 24 * time.Hour
			var pruned []store.SourceInfo
			var err error
			switch kind {
			case "ephemeral":
				pruned, err = st.PurgeEphemeral(dryRun, ephemeralTTL)
			case "session":
				pruned, err = st.PurgeSession(dryRun, sessionTTL)
			default:
				pruned, err = st.Cleanup(dryRun, ephemeralTTL, sessionTTL)
			}
			if err != nil {
				return fmt.Errorf("cleanup failed: %w", err)
			}

			// Explicit reclamation after cleanup. --optimize does the full
			// FTS-rebuild + VACUUM (the only path that reclaims FTS bloat);
			// --vacuum alone only compacts the freelist. --optimize supersedes
			// --vacuum since it already vacuums.
			switch {
			case optimize && !dryRun:
				if err := reclaimFTS(st); err != nil {
					return err
				}
			case vacuum && !dryRun:
				if err := st.Vacuum(); err != nil {
					return fmt.Errorf("vacuum failed: %w", err)
				}
				fmt.Println("capy: vacuum complete")
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
	cmd.Flags().String("kind", "", "only clean up sources of this kind (\"ephemeral\" or \"session\")")
	cmd.Flags().String("source", "", "evict a specific source by exact label")
	cmd.Flags().Bool("vacuum", false, "run VACUUM to reclaim dead pages (auto-runs after cleanup when freelist > 20%%)")
	cmd.Flags().Bool("optimize", false, "rebuild FTS indexes then VACUUM to reclaim FTS bloat that plain --vacuum cannot")
	return cmd
}

// reclaimFTS rebuilds the FTS5 indexes and then VACUUMs, the only sequence that
// reclaims disk pinned by accumulated FTS delete-tombstone segments (a plain
// VACUUM leaves them because they belong to the FTS data tables, not the
// freelist). Shared by the standalone --optimize path and the post-cleanup one.
func reclaimFTS(st *store.ContentStore) error {
	if err := st.RebuildFTS(); err != nil {
		return fmt.Errorf("optimize failed: %w", err)
	}
	if err := st.Vacuum(); err != nil {
		return fmt.Errorf("vacuum after optimize failed: %w", err)
	}
	fmt.Println("capy: optimize complete (FTS rebuilt, VACUUM reclaimed freed pages)")
	return nil
}

// formatCleanupDetail renders per-source eviction detail, switching between
// retention-score framing and TTL-age framing based on EvictionReason.
func formatCleanupDetail(s store.SourceInfo) string {
	switch s.EvictionReason {
	case "ttl":
		return fmt.Sprintf("reason: ttl, age: %s", time.Since(s.IndexedAt).Truncate(time.Minute))
	case "oversized":
		return fmt.Sprintf("reason: oversized, chunks: %d, kind: %s", s.ChunkCount, s.Kind)
	case "manual":
		return fmt.Sprintf("reason: manual, kind: %s, chunks: %d", s.Kind, s.ChunkCount)
	default:
		return fmt.Sprintf("reason: retention, score: %.2f, last accessed: %s",
			s.RetentionScore, s.LastAccessedAt.Format("2006-01-02"))
	}
}
