package main

import (
	"fmt"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/store"
	"github.com/spf13/cobra"
)

func newDBSizeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dbsize",
		Short: "Show knowledge base disk usage breakdown",
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir, _ := cmd.Flags().GetString("project-dir")
			if projectDir == "" {
				projectDir = config.DetectProjectRoot()
			}

			cfg, _ := config.Load(projectDir)
			if cfg == nil {
				cfg = config.DefaultConfig()
			}

			dbPath := cfg.ResolveDBPath(projectDir)
			st := store.NewContentStore(dbPath, projectDir, 0, 0)
			defer st.Close()

			breakdown, err := st.DiskUsage()
			if err != nil {
				return fmt.Errorf("disk usage query failed: %w", err)
			}

			fmt.Printf("Database: %s\n\n", dbPath)

			fmt.Println("=== Table sizes (pages × page_size) ===")
			fmt.Printf("  %-30s %10s %10s\n", "Table", "Pages", "Size")
			fmt.Printf("  %-30s %10s %10s\n", "-----", "-----", "----")
			for _, t := range breakdown.Tables {
				fmt.Printf("  %-30s %10d %10s\n", t.Name, t.Pages, humanBytes(t.Bytes))
			}
			fmt.Println()

			fmt.Println("=== Content by kind ===")
			fmt.Printf("  %-12s %8s %10s %12s\n", "Kind", "Sources", "Chunks", "ContentSize")
			fmt.Printf("  %-12s %8s %10s %12s\n", "----", "-------", "------", "-----------")
			for _, k := range breakdown.Kinds {
				fmt.Printf("  %-12s %8d %10d %12s\n", k.Kind, k.Sources, k.Chunks, humanBytes(k.ContentBytes))
			}
			fmt.Println()

			fmt.Println("=== Top 15 sources by content size ===")
			fmt.Printf("  %-50s %-10s %8s %12s\n", "Label", "Kind", "Chunks", "Size")
			fmt.Printf("  %-50s %-10s %8s %12s\n", "-----", "----", "------", "----")
			for _, s := range breakdown.TopSources {
				label := s.Label
				if len(label) > 50 {
					label = label[:47] + "..."
				}
				fmt.Printf("  %-50s %-10s %8d %12s\n", label, s.Kind, s.Chunks, humanBytes(s.ContentBytes))
			}
			fmt.Println()

			fmt.Printf("=== Summary ===\n")
			fmt.Printf("  DB file size:    %s\n", humanBytes(breakdown.DBFileSize))
			fmt.Printf("  Page size:       %d\n", breakdown.PageSize)
			fmt.Printf("  Total pages:     %d\n", breakdown.TotalPages)
			fmt.Printf("  Freelist pages:  %d (%s reclaimable via VACUUM)\n",
				breakdown.FreelistPages, humanBytes(breakdown.FreelistPages*breakdown.PageSize))
			fmt.Printf("  Vocabulary:      %d terms\n", breakdown.VocabTerms)

			return nil
		},
	}
	return cmd
}

func humanBytes(b int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(MB))
	case b >= KB:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(KB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
