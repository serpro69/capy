package main

import (
	"context"
	"fmt"
	"time"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/session"
	"github.com/serpro69/capy/internal/store"
	"github.com/spf13/cobra"
)

func newSweepCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sweep",
		Short: "Index past Claude Code sessions into the knowledge base",
		Long:  "Parse and index session JSONL files. Runs in dry-run mode by default, showing what would be indexed. Use --force to actually index.",
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir, _ := cmd.Flags().GetString("project-dir")
			if projectDir == "" {
				projectDir = config.DetectProjectRoot()
			} else {
				res, err := config.ResolveSourceProject(projectDir)
				if err != nil {
					return err
				}
				projectDir = res.SourceDir
				if res.IsSessionDir && res.SourceDir == "" {
					projectDir = res.SessionDir
				}
			}

			cfg, _ := config.Load(projectDir)
			if cfg == nil {
				cfg = config.DefaultConfig()
			}

			force, _ := cmd.Flags().GetBool("force")
			reindex, _ := cmd.Flags().GetBool("reindex")
			opts := session.SweepOptions{Reindex: reindex}

			dbPath := cfg.ResolveDBPath(projectDir)
			st := store.NewContentStore(dbPath, projectDir, 0, 0)
			defer st.Close()

			if force {
				return runSweepForce(st, projectDir, opts)
			}
			return runSweepDryRun(st, projectDir, opts)
		},
	}
	cmd.Flags().Bool("force", false, "actually index sessions (default is dry-run)")
	cmd.Flags().Bool("reindex", false, "ignore mtime checks, re-parse all sessions")
	return cmd
}

func runSweepDryRun(st *store.ContentStore, projectDir string, opts session.SweepOptions) error {
	results, err := session.DryRunSweep(projectDir, st, opts)
	if err != nil {
		return fmt.Errorf("sweep failed: %w", err)
	}

	if len(results) == 0 {
		fmt.Println("capy sweep: no session files found")
		return nil
	}

	fmt.Printf("%-38s %8s %5s %5s %8s  %s\n", "UUID", "SIZE", "PAIRS", "MAIN", "CHARS", "STATUS")
	fmt.Println("-----------------------------------------------------------------------------------------------")

	var indexable, alreadyIndexed, notIndexable, parseErrors int
	for _, d := range results {
		status := sweepStatus(d)
		fmt.Printf("%-38s %8s %5d %5d %8d  %s\n",
			d.UUID, formatSize(d.Size), d.TurnPairs, d.MainPairs, d.AssistantChars, status)

		switch {
		case d.ParseError != "":
			parseErrors++
		case d.AlreadyIndexed:
			alreadyIndexed++
		case d.Indexable:
			indexable++
		default:
			notIndexable++
		}
	}

	fmt.Printf("\nTotal: %d | Indexable: %d | Already indexed: %d | Not indexable: %d | Errors: %d\n",
		len(results), indexable, alreadyIndexed, notIndexable, parseErrors)

	if indexable > 0 {
		fmt.Println("\nUse --force to index the indexable sessions.")
	}

	return nil
}

func runSweepForce(st *store.ContentStore, projectDir string, opts session.SweepOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	indexed, skipped, errors := session.SweepWithOptions(ctx, st, projectDir, opts)
	fmt.Printf("capy sweep: indexed %d, skipped %d, errors %d\n", indexed, skipped, errors)
	if errors > 0 {
		return fmt.Errorf("%d session(s) failed to index", errors)
	}
	return nil
}

func sweepStatus(d session.SessionDiagnostic) string {
	if d.ParseError != "" {
		return fmt.Sprintf("error: %s", d.ParseError)
	}
	if d.AlreadyIndexed {
		return "already indexed"
	}
	if d.Indexable {
		return "indexable"
	}
	if d.MainPairs < 2 {
		return fmt.Sprintf("too few pairs (%d, need ≥2)", d.MainPairs)
	}
	return fmt.Sprintf("too few chars (%d, need ≥200)", d.AssistantChars)
}

func formatSize(bytes int64) string {
	switch {
	case bytes >= 1024*1024:
		return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
	case bytes >= 1024:
		return fmt.Sprintf("%.0fKB", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}
