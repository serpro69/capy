package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newCleanupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Remove stale data from the knowledge base",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("capy: cleanup not yet implemented")
			return nil
		},
	}
	cmd.Flags().Int("max-age-days", 30, "maximum age in days for cold sources")
	cmd.Flags().Bool("dry-run", true, "show what would be removed without removing")
	cmd.Flags().Bool("force", false, "actually remove stale data")
	return cmd
}
