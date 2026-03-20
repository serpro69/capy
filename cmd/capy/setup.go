package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newSetupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Configure capy for the current project",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("capy: setup not yet implemented")
			return nil
		},
	}
	cmd.Flags().String("platform", "claude-code", "target platform")
	cmd.Flags().String("binary", "", "path to capy binary")
	return cmd
}
