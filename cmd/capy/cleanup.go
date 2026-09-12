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
			reclaim := optimize || vacuum

			dbPath := cfg.ResolveDBPath(projectDir)
			st := store.NewContentStore(dbPath, cfg.DBProjectDir(projectDir), 0, cfg.Store.MaxSourceBytes)
			defer st.Close()

			// Standalone reclamation: --optimize / --vacuum with no eviction
			// requested (dry-run still at its default and no --source/--kind).
			// Reclamation is a maintenance operation, not data eviction, so it
			// deliberately ignores the dry-run default (ADR-029 §4) — gating it
			// would turn `capy cleanup --optimize` into a silent no-op.
			//
			// Any real-eviction request — --force or an explicit
			// --dry-run=false, or a --source/--kind selector — means the user
			// asked to evict data too, so we fall through: evict first, then
			// reclaim below. Keying on dryRun (not just --force) keeps
			// `--dry-run=false --optimize` evicting exactly like plain
			// `--dry-run=false` does, and mirrors the MCP tool's `dry_run`.
			evictionRequested := !dryRun || sourceLabel != "" || kind != ""
			if reclaim && !evictionRequested {
				return runReclaim(st, optimize, vacuum)
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
				return finishReclaim(st, dryRun, optimize, vacuum)
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

			return finishReclaim(st, dryRun, optimize, vacuum)
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

// runReclaim performs the requested post-eviction reclamation. --optimize does
// the full FTS-rebuild + VACUUM (the only path that reclaims FTS tombstone
// bloat, see store.Optimize / ADR-029); --vacuum alone only compacts the
// freelist. --optimize supersedes --vacuum since it already vacuums.
func runReclaim(st *store.ContentStore, optimize, vacuum bool) error {
	switch {
	case optimize:
		if err := st.Optimize(); err != nil {
			return err
		}
		fmt.Println("capy: optimize complete (FTS rebuilt, VACUUM reclaimed freed pages)")
	case vacuum:
		if err := st.Vacuum(); err != nil {
			return fmt.Errorf("vacuum failed: %w", err)
		}
		fmt.Println("capy: vacuum complete")
	}
	return nil
}

// finishReclaim runs reclamation after an eviction pass, or — when the pass was
// a dry run — says loudly that it was skipped rather than silently no-op'ing.
func finishReclaim(st *store.ContentStore, dryRun, optimize, vacuum bool) error {
	if !optimize && !vacuum {
		return nil
	}
	if dryRun {
		fmt.Printf("capy: %s skipped (dry run) — add --force to evict and reclaim, or drop --source/--kind to reclaim only\n", reclaimName(optimize))
		return nil
	}
	return runReclaim(st, optimize, vacuum)
}

// reclaimName labels the requested reclamation mode for user-facing messages.
func reclaimName(optimize bool) string {
	if optimize {
		return "--optimize"
	}
	return "--vacuum"
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
