package main

import (
	"fmt"

	"github.com/serpro69/capy/internal/config"
	"github.com/spf13/cobra"
)

func newWhichCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "which",
		Short: "Print the knowledge base path for the current project",
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir, _ := cmd.Flags().GetString("project-dir")
			if projectDir == "" {
				projectDir = config.DetectProjectRoot()
			}

			cfg, _ := config.Load(projectDir)
			if cfg == nil {
				cfg = config.DefaultConfig()
			}

			fmt.Println(cfg.ResolveDBPath(projectDir))
			return nil
		},
	}
}
